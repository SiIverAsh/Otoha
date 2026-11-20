package scrape

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/SiIverAsh/otoha/internal/config"
	"github.com/SiIverAsh/otoha/internal/db"
	"github.com/SiIverAsh/otoha/internal/dlsite"
	"github.com/SiIverAsh/otoha/internal/search"
)

type MetadataOptions struct {
	Worknos        []string `json:"worknos,omitempty"`
	MaxWorks       int      `json:"max_works"`
	Workers        int      `json:"workers"`
	IntervalSec    *float64 `json:"interval_sec,omitempty"`
	RequestsPerSec float64  `json:"requests_per_sec,omitempty"`
	Refresh        bool     `json:"refresh"`
	DateOnly       bool     `json:"date_only"`
}

func (o MetadataOptions) Validate() error {
	if o.DateOnly && o.Refresh {
		return fmt.Errorf("date_only and all/refresh cannot both be specified")
	}
	if o.MaxWorks < 0 || o.Workers < 0 || o.Workers > 256 {
		return fmt.Errorf("metadata max_works must be nonnegative and workers must be between 0 and 256")
	}
	if math.IsNaN(o.RequestsPerSec) || math.IsInf(o.RequestsPerSec, 0) || o.RequestsPerSec < 0 || o.RequestsPerSec > float64(time.Second) {
		return fmt.Errorf("metadata requests_per_sec must be finite and between 0 and 1000000000")
	}
	if o.RequestsPerSec > 0 && o.IntervalSec != nil {
		return fmt.Errorf("metadata requests_per_sec and interval_sec cannot both be specified")
	}
	if o.IntervalSec != nil && (math.IsNaN(*o.IntervalSec) || math.IsInf(*o.IntervalSec, 0) || *o.IntervalSec <= 0 || *o.IntervalSec > float64((1<<63-1)/int64(time.Second))) {
		return fmt.Errorf("metadata interval must be a finite positive number")
	}
	for _, workno := range o.Worknos {
		if dlsite.NormalizeWorkno(workno) == "" {
			return fmt.Errorf("invalid metadata workno %q", workno)
		}
	}
	return nil
}

// MetadataProgress reports a metadata-only repair. A missing upstream work is
// counted separately from a request or persistence failure.
type MetadataProgress struct {
	Total        int               `json:"total"`
	Processed    int               `json:"processed"`
	Enriched     int               `json:"enriched"`
	DatesFilled  int               `json:"dates_filled"`
	NotFound     int               `json:"not_found"`
	Failures     int               `json:"failures"`
	Pending      int               `json:"pending"`
	Unresolved   int               `json:"unresolved"`
	Workno       string            `json:"workno,omitempty"`
	Message      string            `json:"message"`
	Completeness FieldCompleteness `json:"completeness"`
}

type metadataSource interface {
	Fetch(context.Context, string, bool) (*dlsite.Metadata, error)
	DownloadCover(context.Context, string, string) (string, error)
}

// MetadataEnricher shares the upstream rate limiter across works. It only fills
// empty metadata and never changes audio sources, user ratings, or history.
type MetadataEnricher struct {
	store     *db.Store
	index     *search.Engine
	client    metadataSource
	transport *metadataRateTransport
	dateOnly  bool
}

func NewMetadataEnricher(store *db.Store, index *search.Engine, cfg *config.Config) (*MetadataEnricher, error) {
	return newMetadataEnricher(store, index, cfg, MetadataOptions{})
}

func newMetadataEnricher(store *db.Store, index *search.Engine, cfg *config.Config, opts MetadataOptions) (*MetadataEnricher, error) {
	if err := opts.Validate(); err != nil {
		return nil, err
	}
	if store == nil || cfg == nil {
		return nil, fmt.Errorf("metadata repair requires a store and configuration")
	}
	cache, err := dlsite.NewCache(cfg.CacheDir())
	if err != nil {
		return nil, err
	}
	interval := time.Duration(cfg.Snapshot().ScraperIntervalSec * float64(time.Second))
	if interval <= 0 {
		interval = 3 * time.Second
	}
	if opts.IntervalSec != nil {
		interval = time.Duration(*opts.IntervalSec * float64(time.Second))
	} else if opts.RequestsPerSec > 0 {
		interval = time.Duration(math.Ceil(float64(time.Second) / opts.RequestsPerSec))
	}
	baseTransport, err := dlsite.BuildHTTPTransport(cfg.Snapshot().ScraperProxy)
	if err != nil {
		return nil, err
	}
	workers := opts.Workers
	if workers == 0 {
		workers = DefaultASMR18Workers
	}
	baseTransport.MaxIdleConns = max(10, workers*2)
	baseTransport.MaxIdleConnsPerHost = max(4, workers)
	transport := newMetadataRateTransport(baseTransport, interval)
	client, err := NewClientWithTransport(cfg, cache, interval, transport)
	if err != nil {
		transport.CloseIdleConnections()
		return nil, err
	}
	var source metadataSource = client
	if opts.DateOnly {
		source = dateMetadataSource{client}
	}
	return &MetadataEnricher{store: store, index: index, client: source, transport: transport, dateOnly: opts.DateOnly}, nil
}

type dateMetadataSource struct{ *dlsite.Client }

func (s dateMetadataSource) Fetch(ctx context.Context, workno string, _ bool) (*dlsite.Metadata, error) {
	return s.FetchReleaseDate(ctx, workno)
}

// Close releases idle connections after the importer or metadata job has ended.
func (e *MetadataEnricher) Close() {
	if e.transport != nil {
		e.transport.CloseIdleConnections()
	}
}

// metadataRateTransport spaces every HTTP attempt through one cancellable gate.
// Waiting holds no in-flight request slot: slow responses can still overlap up
// to the configured worker count, while starts share one global request budget.
type metadataRateTransport struct {
	base     http.RoundTripper
	interval time.Duration
	gate     chan struct{}
	last     time.Time
}

func newMetadataRateTransport(base http.RoundTripper, interval time.Duration) *metadataRateTransport {
	return &metadataRateTransport{base: base, interval: max(time.Nanosecond, interval), gate: make(chan struct{}, 1)}
}

func (t *metadataRateTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	select {
	case t.gate <- struct{}{}:
	case <-req.Context().Done():
		return nil, req.Context().Err()
	}
	if wait := t.interval - time.Since(t.last); !t.last.IsZero() && wait > 0 {
		timer := time.NewTimer(wait)
		select {
		case <-timer.C:
		case <-req.Context().Done():
			timer.Stop()
			<-t.gate
			return nil, req.Context().Err()
		}
	}
	if err := req.Context().Err(); err != nil {
		<-t.gate
		return nil, err
	}
	t.last = time.Now()
	<-t.gate
	return t.base.RoundTrip(req)
}

func (t *metadataRateTransport) CloseIdleConnections() {
	if closer, ok := t.base.(interface{ CloseIdleConnections() }); ok {
		closer.CloseIdleConnections()
	}
}

// EnrichWork obtains fresh, product-specific metadata from the existing DLsite
// pipeline (including its configured ASMR.one fallback) and fills only gaps.
// Fetching fresh data also permits recovery from an older cached 404 or an
// incomplete cache written by an earlier parser.
func (e *MetadataEnricher) EnrichWork(ctx context.Context, workno string) (*dlsite.Metadata, bool, error) {
	result := e.enrichWork(ctx, workno)
	return result.meta, result.dateFilled, result.err
}

type metadataWorkResult struct {
	workno     string
	meta       *dlsite.Metadata
	dateFilled bool
	unresolved bool
	err        error
}

func (e *MetadataEnricher) enrichWork(ctx context.Context, workno string) (result metadataWorkResult) {
	result.workno = workno
	result.unresolved = true
	wno := dlsite.NormalizeWorkno(workno)
	if wno == "" {
		result.err = fmt.Errorf("invalid metadata workno %q", workno)
		return result
	}
	if err := ctx.Err(); err != nil {
		result.err = err
		return result
	}
	before, err := e.store.GetWorkDetail(ctx, wno, 1)
	if err != nil {
		result.err = err
		return result
	}
	meta, err := e.client.Fetch(ctx, wno, true)
	if err != nil {
		result.err = err
		return result
	}
	if meta == nil || meta.NotFound {
		result.err = dlsite.ErrWorkNotFound
		return result
	}
	if dlsite.NormalizeWorkno(meta.Workno) != wno {
		result.err = fmt.Errorf("metadata source returned mismatched work %q for %s", meta.Workno, wno)
		return result
	}
	if err := ctx.Err(); err != nil {
		result.err = err
		return result
	}
	if e.dateOnly {
		result.meta = meta
		result.unresolved = strings.TrimSpace(before.ReleaseDate) == "" && meta.ReleaseDate == ""
		if meta.ReleaseDate != "" {
			result.dateFilled, result.err = e.store.FillASMR18ReleaseDate(ctx, wno, meta.ReleaseDate)
		}
		return result
	}
	// Persist useful metadata even if the optional local cover cache is unavailable.
	// Covers are served from their existing URL when no local path can be saved.
	coverPath := ""
	if before.CoverPath == "" && meta.CoverURL != "" {
		coverPath, _ = e.client.DownloadCover(ctx, wno, meta.CoverURL)
		if err := ctx.Err(); err != nil {
			result.err = err
			return result
		}
	}
	if err := e.store.FillMissingMetadata(ctx, meta, coverPath); err != nil {
		result.err = err
		return result
	}
	result.meta = meta
	result.dateFilled = before.ReleaseDate == "" && meta.ReleaseDate != ""
	result.unresolved = (before.ReleaseDate == "" && meta.ReleaseDate == "") ||
		(before.CircleID == "" && meta.CircleID == "") ||
		(before.CircleName == "" && meta.CircleName == "") ||
		(before.WorkType == "" && meta.WorkType == "") ||
		(before.Title == "" && meta.Title == "") ||
		(before.AgeCategory == 0 && meta.AgeCategory == 0) ||
		(before.CoverPath == "" && before.CoverURL == "" && coverPath == "" && meta.CoverURL == "")
	if e.index != nil {
		doc, err := e.store.GetSearchDocument(ctx, wno)
		if err != nil {
			result.err = err
			return result
		}
		if err := e.index.IndexWork(*doc); err != nil {
			result.err = fmt.Errorf("index enriched metadata: %w", err)
			return result
		}
	}
	return result
}

// SyncASMR18Metadata repairs already imported ASMR18 works with incomplete core
// metadata. Each work is committed separately, so cancellation preserves all
// completed repairs and the next invocation naturally selects remaining gaps.
func SyncASMR18Metadata(ctx context.Context, store *db.Store, index *search.Engine, cfg *config.Config, opts MetadataOptions, onProgress func(MetadataProgress)) (MetadataProgress, error) {
	if err := opts.Validate(); err != nil {
		return MetadataProgress{}, err
	}
	enricher, err := newMetadataEnricher(store, index, cfg, opts)
	if err != nil {
		return MetadataProgress{}, err
	}
	defer enricher.Close()
	worknos := opts.Worknos
	if len(worknos) == 0 {
		if opts.DateOnly {
			worknos, err = store.ListASMR18WorknosMissingReleaseDate(ctx)
		} else if opts.Refresh {
			worknos, err = store.ListAllASMR18Worknos(ctx)
		} else {
			worknos, err = store.ListIncompleteASMR18Worknos(ctx)
		}
	}
	if err != nil {
		return MetadataProgress{}, err
	}
	return syncASMR18Metadata(ctx, enricher, worknos, opts, onProgress)
}

func syncASMR18Metadata(ctx context.Context, enricher *MetadataEnricher, worknos []string, opts MetadataOptions, onProgress func(MetadataProgress)) (MetadataProgress, error) {
	// RJ IDs have both six- and eight-digit forms; lexical ordering would place
	// older six-digit works before the new eight-digit catalog.
	queue := make([]string, 0, len(worknos))
	seen := make(map[string]bool, len(worknos))
	for _, raw := range worknos {
		wno := dlsite.NormalizeWorkno(raw)
		if wno == "" || seen[wno] {
			continue
		}
		seen[wno] = true
		queue = append(queue, wno)
	}
	sort.Slice(queue, func(i, j int) bool {
		a, b := dlsite.NumericIDFromWorkno(queue[i]), dlsite.NumericIDFromWorkno(queue[j])
		if a == b {
			return queue[i] > queue[j]
		}
		return a > b
	})
	if opts.MaxWorks > 0 && len(queue) > opts.MaxWorks {
		queue = queue[:opts.MaxWorks]
	}
	p := MetadataProgress{Total: len(queue), Pending: len(queue), Message: "准备补全 ASMR18 元数据"}
	notify := func() {
		if onProgress != nil {
			onProgress(p)
		}
	}
	notify()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	workers := opts.Workers
	if workers == 0 {
		workers = DefaultASMR18Workers
	}
	jobs := make(chan string)
	results := make(chan metadataWorkResult, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for workno := range jobs {
				if ctx.Err() != nil {
					return
				}
				r := enricher.enrichWork(ctx, workno)
				select {
				case results <- r:
				case <-ctx.Done():
					return
				}
			}
		}()
	}
	go func() {
		defer close(jobs)
		for _, workno := range queue {
			select {
			case jobs <- workno:
			case <-ctx.Done():
				return
			}
		}
	}()
	go func() {
		wg.Wait()
		close(results)
	}()
	for result := range results {
		if ctx.Err() != nil {
			continue
		}
		p.Workno = result.workno
		p.Processed++
		p.Pending = p.Total - p.Processed
		if result.unresolved || result.err != nil {
			p.Unresolved++
		}
		if result.dateFilled {
			p.DatesFilled++
		}
		switch {
		case errors.Is(result.err, dlsite.ErrWorkNotFound):
			p.NotFound++
			p.Message = "来源未提供该作品的补充元数据"
		case result.err != nil:
			p.Failures++
			p.Message = result.err.Error()
		default:
			p.Enriched++
			p.Completeness.Add(result.meta)
			p.Message = strings.TrimSpace("已补充可得元数据 " + result.meta.Source)
			if result.unresolved {
				p.Message += "；来源仍缺少部分核心字段"
			}
		}
		notify()
	}
	return p, ctx.Err()
}
