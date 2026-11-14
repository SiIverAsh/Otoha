package search

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/SiIverAsh/otoha/internal/db"
	"github.com/blevesearch/bleve/v2"
	_ "github.com/blevesearch/bleve/v2/analysis/lang/cjk"
	"github.com/blevesearch/bleve/v2/mapping"
	"github.com/blevesearch/bleve/v2/search/query"
)

// Engine wraps Bleve v2 with its CJK bigram analyzer (`cjk`) and gracefully degrades
// to SQLite LIKE search if the Bleve index on disk is corrupted (§6).
type Engine struct {
	mu       sync.RWMutex
	path     string
	idx      bleve.Index
	degraded bool
	lastErr  string
}

func buildIndexMapping() mapping.IndexMapping {
	im := bleve.NewIndexMapping()
	im.DefaultAnalyzer = "cjk"

	cjkField := func() *mapping.FieldMapping {
		fm := bleve.NewTextFieldMapping()
		fm.Analyzer = "cjk"
		fm.Store = false
		fm.IncludeInAll = true
		return fm
	}

	docMapping := bleve.NewDocumentMapping()
	docMapping.AddFieldMappingsAt("workno", cjkField())
	docMapping.AddFieldMappingsAt("title", cjkField())
	docMapping.AddFieldMappingsAt("circle_name", cjkField())
	docMapping.AddFieldMappingsAt("series_name", cjkField())
	docMapping.AddFieldMappingsAt("vas", cjkField())
	docMapping.AddFieldMappingsAt("staff", cjkField())
	docMapping.AddFieldMappingsAt("tags", cjkField())

	im.DefaultMapping = docMapping
	return im
}

// Open opens an existing Bleve index at indexPath or creates a new one.
// If the existing index is corrupted, Engine enters degraded mode rather than crashing startup (§6).
func Open(indexPath string) *Engine {
	e := &Engine{path: indexPath}
	if _, err := os.Stat(indexPath); os.IsNotExist(err) {
		idx, createErr := bleve.New(indexPath, buildIndexMapping())
		if createErr != nil {
			e.degraded = true
			e.lastErr = createErr.Error()
			return e
		}
		e.idx = idx
		return e
	}

	idx, openErr := bleve.Open(indexPath)
	if openErr != nil {
		e.degraded = true
		e.lastErr = fmt.Sprintf("bleve index corrupted (%v); degraded to SQL LIKE until `otoha reindex`", openErr)
		return e
	}
	e.idx = idx
	return e
}

func (e *Engine) Close() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.idx != nil {
		err := e.idx.Close()
		e.idx = nil
		return err
	}
	return nil
}

// IsDegraded reports whether Bleve is unavailable and searches are falling back to SQLite LIKE (§6).
func (e *Engine) IsDegraded() (bool, string) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.degraded, e.lastErr
}

// ForceDegradedForTest simulates index corruption during tests.
func (e *Engine) ForceDegradedForTest(reason string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.idx != nil {
		_ = e.idx.Close()
		e.idx = nil
	}
	e.degraded = true
	e.lastErr = reason
}

// IndexWork indexes or updates a single work document in Bleve (§6).
func (e *Engine) IndexWork(doc db.SearchDocument) error {
	e.mu.RLock()
	idx := e.idx
	degraded := e.degraded
	e.mu.RUnlock()
	if degraded || idx == nil {
		return nil
	}
	return idx.Index(doc.Workno, doc)
}

// IndexWorkBatch indexes a slice of documents in a single Bleve batch for fast page syncs.
func (e *Engine) IndexWorkBatch(docs []db.SearchDocument) error {
	if len(docs) == 0 {
		return nil
	}
	e.mu.RLock()
	idx := e.idx
	degraded := e.degraded
	e.mu.RUnlock()
	if degraded || idx == nil {
		return nil
	}
	batch := idx.NewBatch()
	for _, doc := range docs {
		if doc.Workno == "" {
			continue
		}
		_ = batch.Index(doc.Workno, doc)
	}
	return idx.Batch(batch)
}

// DeleteWork removes a work from Bleve.
func (e *Engine) DeleteWork(workno string) error {
	e.mu.RLock()
	idx := e.idx
	degraded := e.degraded
	e.mu.RUnlock()
	if degraded || idx == nil {
		return nil
	}
	return idx.Delete(workno)
}

// SearchResult summarizes Bleve (or SQL LIKE fallback) search output.
type SearchResult struct {
	Worknos      []string `json:"worknos"`
	Degraded     bool     `json:"degraded"`
	NeedsReindex bool     `json:"needs_reindex"`
	Warning      string   `json:"warning,omitempty"`
}

// Search executes Bleve's native query_string query (supporting space-AND, `-exclude`, `"phrase"`, §6).
// If Bleve is corrupted or errors at query time, it sets Degraded=true so caller uses SQL LIKE fallback.
func (e *Engine) Search(queryStr string, maxHits int) (*SearchResult, error) {
	q := strings.TrimSpace(queryStr)
	if q == "" {
		return &SearchResult{Worknos: nil}, nil
	}
	if maxHits <= 0 {
		maxHits = 500
	}

	e.mu.RLock()
	idx := e.idx
	degraded := e.degraded
	lastErr := e.lastErr
	e.mu.RUnlock()

	if degraded || idx == nil {
		return &SearchResult{
			Degraded:     true,
			NeedsReindex: true,
			Warning:      lastErr,
		}, nil
	}

	bq := buildStrictQueryString(q)
	req := bleve.NewSearchRequestOptions(bq, maxHits, 0, false)
	res, err := idx.Search(req)
	if err != nil {
		e.mu.Lock()
		e.degraded = true
		e.lastErr = err.Error()
		e.mu.Unlock()
		return &SearchResult{
			Degraded:     true,
			NeedsReindex: true,
			Warning:      err.Error(),
		}, nil
	}

	worknos := make([]string, 0, len(res.Hits))
	for _, hit := range res.Hits {
		worknos = append(worknos, hit.ID)
	}
	return &SearchResult{
		Worknos: worknos,
	}, nil
}

// buildStrictQueryString prefixes bare top-level clauses with '+' so space-separated
// keywords perform intersection (AND) while preserving `-exclude`, `+must`, and `"phrases"` (§6),
// then feeds the result into bleve.NewQueryStringQuery.
func buildStrictQueryString(raw string) query.Query {
	clauses := splitQueryClauses(raw)
	for i, c := range clauses {
		if strings.HasPrefix(c, "+") || strings.HasPrefix(c, "-") {
			continue
		}
		clauses[i] = "+" + c
	}
	qsq := bleve.NewQueryStringQuery(strings.Join(clauses, " "))
	parsed, err := qsq.Parse()
	if err != nil {
		return qsq
	}
	tightenMatchOperators(parsed)
	return parsed
}

func tightenMatchOperators(q query.Query) {
	switch v := q.(type) {
	case *query.MatchQuery:
		v.SetOperator(query.MatchQueryOperatorAnd)
	case *query.BooleanQuery:
		if v.Must != nil {
			tightenMatchOperators(v.Must)
		}
		if v.Should != nil {
			tightenMatchOperators(v.Should)
		}
	case *query.ConjunctionQuery:
		for _, sub := range v.Conjuncts {
			tightenMatchOperators(sub)
		}
	case *query.DisjunctionQuery:
		for _, sub := range v.Disjuncts {
			tightenMatchOperators(sub)
		}
	}
}

func splitQueryClauses(s string) []string {
	var out []string
	var buf strings.Builder
	inQuote := false
	for i := 0; i < len(s); i++ {
		ch := s[i]
		if ch == '"' && (i == 0 || s[i-1] != '\\') {
			inQuote = !inQuote
			buf.WriteByte(ch)
			continue
		}
		if !inQuote && (ch == ' ' || ch == '\t' || ch == '\n' || ch == '\r') {
			if buf.Len() > 0 {
				out = append(out, buf.String())
				buf.Reset()
			}
			continue
		}
		buf.WriteByte(ch)
	}
	if buf.Len() > 0 {
		out = append(out, buf.String())
	}
	return out
}

// ReindexFromStore rebuilds the entire Bleve index from SQLite truth source (§6 `otoha reindex`).
func (e *Engine) ReindexFromStore(ctx context.Context, store *db.Store) (int, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.idx != nil {
		_ = e.idx.Close()
		e.idx = nil
	}

	tmpPath := e.path + ".rebuilding"
	_ = os.RemoveAll(tmpPath)

	newIdx, err := bleve.New(tmpPath, buildIndexMapping())
	if err != nil {
		e.degraded = true
		e.lastErr = err.Error()
		return 0, fmt.Errorf("create temp bleve index: %w", err)
	}

	batch := newIdx.NewBatch()
	count := 0
	err = store.IterAllSearchDocuments(ctx, func(doc db.SearchDocument) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := batch.Index(doc.Workno, doc); err != nil {
			return err
		}
		count++
		if batch.Size() >= 200 {
			if err := newIdx.Batch(batch); err != nil {
				return err
			}
			batch = newIdx.NewBatch()
		}
		return nil
	})
	if err == nil && batch.Size() > 0 {
		err = newIdx.Batch(batch)
	}
	_ = newIdx.Close()
	if err != nil {
		_ = os.RemoveAll(tmpPath)
		e.degraded = true
		e.lastErr = err.Error()
		return count, err
	}

	_ = os.RemoveAll(e.path)
	if err := os.Rename(tmpPath, e.path); err != nil {
		e.degraded = true
		e.lastErr = err.Error()
		return count, fmt.Errorf("swap rebuilt bleve index: %w", err)
	}

	opened, err := bleve.Open(e.path)
	if err != nil {
		e.degraded = true
		e.lastErr = err.Error()
		return count, err
	}
	e.idx = opened
	e.degraded = false
	e.lastErr = ""
	return count, nil
}
