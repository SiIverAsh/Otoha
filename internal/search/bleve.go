package search

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/SiIverAsh/otoha/internal/db"
	"github.com/SiIverAsh/otoha/internal/dlsite"
	"github.com/blevesearch/bleve/v2"
	"github.com/blevesearch/bleve/v2/analysis"
	_ "github.com/blevesearch/bleve/v2/analysis/lang/cjk"
	"github.com/blevesearch/bleve/v2/mapping"
	"github.com/blevesearch/bleve/v2/search/query"
)

const (
	minPartialKeywordRunes = 3
	minTypoKeywordRunes    = 4
	partialKeywordBoost    = 0.3
	typoKeywordBoost       = 0.1
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
// If opening fails, Engine enters degraded mode (§6).
func Open(indexPath string) *Engine {
	engine, err := openIndex(indexPath, nil)
	if err != nil {
		return &Engine{
			path:     indexPath,
			degraded: true,
			lastErr:  fmt.Sprintf("could not open Bleve index (%v). Using SQL LIKE until `otoha reindex`", err),
		}
	}
	return engine
}

// OpenWithLockTimeout opens or creates an index for a command that requires it.
// It returns an error if opening fails or an existing index stays locked.
func OpenWithLockTimeout(indexPath string, lockTimeout time.Duration) (*Engine, error) {
	return openIndex(indexPath, map[string]any{"bolt_timeout": lockTimeout.String()})
}

func openIndex(indexPath string, runtimeConfig map[string]any) (*Engine, error) {
	var idx bleve.Index
	var err error
	if _, statErr := os.Stat(indexPath); os.IsNotExist(statErr) {
		idx, err = bleve.New(indexPath, buildIndexMapping())
	} else {
		idx, err = bleve.OpenUsing(indexPath, runtimeConfig)
	}
	if err != nil {
		return nil, err
	}
	return &Engine{path: indexPath, idx: idx}, nil
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

// Search executes keyword search with fuzzy matches, conjunctions, exclusions and phrases.
// If Bleve is corrupted or errors at query time, it sets Degraded=true so caller uses SQL LIKE fallback.
func (e *Engine) Search(queryStr string, maxHits int) (*SearchResult, error) {
	return e.SearchWithinWorks(context.Background(), queryStr, maxHits, nil)
}

// SearchWithinWorks restricts matches to the given work IDs before applying the hit limit.
// A nil worknos slice searches the entire index.
func (e *Engine) SearchWithinWorks(ctx context.Context, queryStr string, maxHits int, worknos []string) (*SearchResult, error) {
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

	bq := buildSearchQuery(q, idx.Mapping())
	if worknos != nil {
		if len(worknos) == 0 {
			return &SearchResult{Worknos: worknos}, nil
		}
		bq = bleve.NewConjunctionQuery(bq, bleve.NewDocIDQuery(worknos))
	}
	req := bleve.NewSearchRequestOptions(bq, maxHits, 0, false)
	res, err := idx.SearchInContext(ctx, req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
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

	matches := make([]string, 0, len(res.Hits))
	for _, hit := range res.Hits {
		matches = append(matches, hit.ID)
	}
	return &SearchResult{
		Worknos: matches,
	}, nil
}

// buildSearchQuery requires each positive keyword and preserves Bleve query syntax.
func buildSearchQuery(raw string, indexMapping mapping.IndexMapping) query.Query {
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
	var analyzer analysis.Analyzer
	if !strings.ContainsRune(raw, '~') {
		analyzer = indexMapping.AnalyzerNamed(indexMapping.AnalyzerNameForPath(indexMapping.DefaultSearchField()))
	}
	return expandKeywordMatches(parsed, analyzer)
}

// Leave exclusions and phrase queries alone when expanding positive matches.
func expandKeywordMatches(q query.Query, analyzer analysis.Analyzer) query.Query {
	switch v := q.(type) {
	case *query.MatchQuery:
		v.SetOperator(query.MatchQueryOperatorAnd)
		return keywordAlternatives(v, analyzer)
	case *query.BooleanQuery:
		v.Must = expandKeywordMatches(v.Must, analyzer)
		v.Should = expandKeywordMatches(v.Should, analyzer)
	case *query.ConjunctionQuery:
		for i, sub := range v.Conjuncts {
			v.Conjuncts[i] = expandKeywordMatches(sub, analyzer)
		}
	case *query.DisjunctionQuery:
		for i, sub := range v.Disjuncts {
			v.Disjuncts[i] = expandKeywordMatches(sub, analyzer)
		}
	}
	return q
}

func keywordAlternatives(match *query.MatchQuery, analyzer analysis.Analyzer) query.Query {
	if analyzer == nil || match.Field() != "" {
		return match
	}
	if strings.IndexFunc(match.Match, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsNumber(r) && !unicode.IsMark(r)
	}) >= 0 {
		return match
	}
	tokens := analyzer.Analyze([]byte(match.Match))
	if len(tokens) != 1 {
		return match
	}
	token := tokens[0]
	term := string(token.Term)
	length := utf8.RuneCountInString(term)
	isCJKUnigram := token.Type == analysis.Single && length == 1
	if (length < minPartialKeywordRunes && !isCJKUnigram) || dlsite.IsValidWorkno(term) {
		return match
	}

	// CJK substrings of two or more characters already match the indexed bigrams.
	// A wildcard also finds CJK characters that only occur within a bigram.
	partial := bleve.NewWildcardQuery("*" + term + "*")
	partial.SetBoost(partialKeywordBoost * match.Boost())
	alternatives := []query.Query{match, partial}
	if length >= minTypoKeywordRunes && strings.IndexFunc(term, func(r rune) bool {
		return !unicode.Is(unicode.Latin, r) && !unicode.IsMark(r)
	}) < 0 {
		typo := bleve.NewFuzzyQuery(term)
		typo.SetFuzziness(1)
		typo.SetBoost(typoKeywordBoost * match.Boost())
		alternatives = append(alternatives, typo)
	}
	return bleve.NewDisjunctionQuery(alternatives...)
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
