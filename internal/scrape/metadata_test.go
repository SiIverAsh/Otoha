package scrape

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SiIverAsh/otoha/internal/config"
	"github.com/SiIverAsh/otoha/internal/db"
	"github.com/SiIverAsh/otoha/internal/dlsite"
	"github.com/SiIverAsh/otoha/internal/search"
)

type fakeMetadataSource struct {
	fetch func(context.Context, string, bool) (*dlsite.Metadata, error)
}

func (f fakeMetadataSource) Fetch(ctx context.Context, wno string, force bool) (*dlsite.Metadata, error) {
	return f.fetch(ctx, wno, force)
}

func (f fakeMetadataSource) DownloadCover(context.Context, string, string) (string, error) {
	return "", errors.New("optional cover cache unavailable")
}

func completeTestMetadata(wno string) *dlsite.Metadata {
	return &dlsite.Metadata{
		Workno: wno, Title: "Enriched title", CircleID: "RG12345", CircleName: "Circle",
		ReleaseDate: "2026-11-14", WorkType: "SOU", AgeCategory: dlsite.AgeR18,
		CoverURL: "https://example.test/cover.jpg", VAs: []string{"New voice"}, Tags: []string{"Test tag"},
	}
}

func seedMetadataWorks(t *testing.T, store *db.Store, worknos ...string) {
	t.Helper()
	for _, wno := range worknos {
		if err := store.SaveMetadata(context.Background(), &dlsite.Metadata{Workno: wno, Title: "Keep " + wno, AgeCategory: dlsite.AgeR18}, "", true); err != nil {
			t.Fatal(err)
		}
	}
}

func TestMetadataRepairContinuesAfterNotFoundAndUpdatesSearch(t *testing.T) {
	ctx := context.Background()
	store := asmr18TestStore(t)
	seedMetadataWorks(t, store, "RJ01760149", "RJ01756573", "RJ441526")
	index := search.Open(filepath.Join(t.TempDir(), "index.bleve"))
	defer index.Close()
	var calls []string
	client := fakeMetadataSource{fetch: func(_ context.Context, wno string, force bool) (*dlsite.Metadata, error) {
		if !force {
			t.Error("metadata repair must bypass stale incomplete/404 cache")
		}
		calls = append(calls, wno)
		if wno == "RJ01756573" {
			return nil, dlsite.ErrWorkNotFound
		}
		return completeTestMetadata(wno), nil
	}}
	enricher := &MetadataEnricher{store: store, index: index, client: client}
	p, err := syncASMR18Metadata(ctx, enricher, []string{"RJ441526", "rj01760149", "RJ01756573", "RJ01760149"}, MetadataOptions{Workers: 1}, nil)
	if err != nil || p.Total != 3 || p.Processed != 3 || p.Enriched != 2 || p.DatesFilled != 2 || p.NotFound != 1 || p.Failures != 0 || p.Pending != 0 || p.Unresolved != 1 {
		t.Fatalf("unexpected progress %+v, err=%v", p, err)
	}
	if fmt.Sprint(calls) != "[RJ01760149 RJ01756573 RJ441526]" {
		t.Fatalf("expected newest numeric RJ first and deduplication, got %v", calls)
	}
	detail, err := store.GetWorkDetail(ctx, "RJ01760149", 1)
	if err != nil || detail.ReleaseDate != "2026-11-14" || detail.Title != "Keep RJ01760149" || len(detail.VAs) != 1 {
		t.Fatalf("failed to fill gaps while retaining prior metadata: %+v, %v", detail, err)
	}
	res, err := index.Search("New voice", 20)
	if err != nil || len(res.Worknos) != 2 {
		t.Fatalf("enriched metadata missing from search: %+v %v", res, err)
	}
}

func TestMetadataRepairRejectsMismatchedSourceAndKeepsIncompleteVisible(t *testing.T) {
	store := asmr18TestStore(t)
	seedMetadataWorks(t, store, "RJ01760149", "RJ01756573")
	client := fakeMetadataSource{fetch: func(_ context.Context, wno string, _ bool) (*dlsite.Metadata, error) {
		if wno == "RJ01760149" {
			return completeTestMetadata("RJ01760000"), nil
		}
		return &dlsite.Metadata{Workno: wno, Title: "Incomplete"}, nil
	}}
	p, err := syncASMR18Metadata(context.Background(), &MetadataEnricher{store: store, client: client}, []string{"RJ01760149", "RJ01756573"}, MetadataOptions{Workers: 1}, nil)
	if err != nil || p.Failures != 1 || p.Enriched != 1 || p.Unresolved != 2 || p.DatesFilled != 0 {
		t.Fatalf("unexpected partial result %+v, %v", p, err)
	}
	detail, err := store.GetWorkDetail(context.Background(), "RJ01760149", 1)
	if err != nil || detail.ReleaseDate != "" {
		t.Fatalf("mismatched metadata was written: %+v %v", detail, err)
	}
}

func TestMetadataRepairConcurrentFetchAndCancellationPreserveCompleted(t *testing.T) {
	store := asmr18TestStore(t)
	worknos := []string{"RJ01760149", "RJ01756573", "RJ01755181", "RJ01754892"}
	seedMetadataWorks(t, store, worknos...)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var active, peak atomic.Int32
	var entered atomic.Int32
	gate := make(chan struct{})
	var closeGate sync.Once
	client := fakeMetadataSource{fetch: func(ctx context.Context, wno string, _ bool) (*dlsite.Metadata, error) {
		n := active.Add(1)
		defer active.Add(-1)
		for {
			old := peak.Load()
			if n <= old || peak.CompareAndSwap(old, n) {
				break
			}
		}
		if entered.Add(1) == 2 {
			closeGate.Do(func() { close(gate) })
		}
		select {
		case <-gate:
			return completeTestMetadata(wno), nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}}
	done := make(chan struct{})
	var p MetadataProgress
	var err error
	go func() {
		p, err = syncASMR18Metadata(ctx, &MetadataEnricher{store: store, client: client}, worknos, MetadataOptions{Workers: 2}, func(p MetadataProgress) {
			if p.Processed >= 1 {
				cancel()
			}
		})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("cancelled metadata workers did not stop")
	}
	if !errors.Is(err, context.Canceled) || p.Processed < 1 || p.Pending < 1 || peak.Load() != 2 || active.Load() != 0 {
		t.Fatalf("bad cancellation/concurrency result %+v, err=%v peak=%d active=%d", p, err, peak.Load(), active.Load())
	}
	var n int
	if err := store.DB().QueryRow("SELECT COUNT(*) FROM work WHERE release_date='2026-11-14'").Scan(&n); err != nil || n < 1 {
		t.Fatalf("completed repair not preserved: n=%d err=%v", n, err)
	}
}

func TestMetadataOptionsValidation(t *testing.T) {
	zero := 0.0
	interval := 0.5
	for _, opts := range []MetadataOptions{{MaxWorks: -1}, {Workers: 257}, {IntervalSec: &zero}, {Worknos: []string{"not a work"}}, {RequestsPerSec: math.NaN()}, {RequestsPerSec: math.Inf(1)}, {RequestsPerSec: -1}, {RequestsPerSec: 100, IntervalSec: &interval}} {
		if opts.Validate() == nil {
			t.Fatalf("accepted invalid options: %+v", opts)
		}
	}
	if err := (MetadataOptions{Workers: 256, RequestsPerSec: 100}).Validate(); err != nil {
		t.Fatalf("valid high-concurrency options rejected: %v", err)
	}
}

type metadataRoundTripFunc func(*http.Request) (*http.Response, error)

func (f metadataRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func metadataHTTPResponse(req *http.Request, status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: req}
}

func TestMetadataTransportShares100PerSecondBudgetAcrossHosts(t *testing.T) {
	var mu sync.Mutex
	var starts []time.Time
	var active, peak atomic.Int32
	base := metadataRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		mu.Lock()
		starts = append(starts, time.Now())
		mu.Unlock()
		n := active.Add(1)
		defer active.Add(-1)
		for {
			old := peak.Load()
			if n <= old || peak.CompareAndSwap(old, n) {
				break
			}
		}
		time.Sleep(35 * time.Millisecond)
		return metadataHTTPResponse(req, http.StatusOK, "ok"), nil
	})
	transport := newMetadataRateTransport(base, time.Second/100)
	var wg sync.WaitGroup
	for i := 0; i < 30; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			req, _ := http.NewRequest(http.MethodGet, fmt.Sprintf("https://source%d.test/metadata", i%3), nil)
			resp, err := transport.RoundTrip(req)
			if err != nil {
				t.Errorf("RoundTrip: %v", err)
				return
			}
			resp.Body.Close()
		}(i)
	}
	wg.Wait()
	sort.Slice(starts, func(i, j int) bool { return starts[i].Before(starts[j]) })
	if len(starts) != 30 || peak.Load() < 2 {
		t.Fatalf("requests=%d peak=%d: limiter must allow concurrent responses", len(starts), peak.Load())
	}
	for i := 1; i < len(starts); i++ {
		if gap := starts[i].Sub(starts[i-1]); gap < 9*time.Millisecond {
			t.Fatalf("cross-host requests exceeded shared 100/s budget: gap=%v", gap)
		}
	}
	if elapsed := starts[len(starts)-1].Sub(starts[0]); elapsed < 290*time.Millisecond-time.Millisecond {
		t.Fatalf("30 starts exceeded rate budget: elapsed=%v", elapsed)
	}
}

func TestMetadataTransportCancelsBothTimerAndGateWaiters(t *testing.T) {
	var calls atomic.Int32
	base := metadataRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		calls.Add(1)
		return metadataHTTPResponse(req, http.StatusOK, "ok"), nil
	})
	transport := newMetadataRateTransport(base, time.Second)
	first, _ := http.NewRequest(http.MethodGet, "https://source.test/first", nil)
	resp, err := transport.RoundTrip(first)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() {
			req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "https://other.test/wait", nil)
			_, err := transport.RoundTrip(req)
			done <- err
		}()
	}
	time.Sleep(20 * time.Millisecond)
	cancel()
	for i := 0; i < 2; i++ {
		select {
		case err := <-done:
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("waiter returned %v", err)
			}
		case <-time.After(200 * time.Millisecond):
			t.Fatal("cancelled HTTP limiter waiter did not stop")
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("cancelled requests reached upstream: calls=%d", calls.Load())
	}
}

func TestMetadataFactorySharesTransportForFallbackCoverRetryAndRedirect(t *testing.T) {
	cfg := config.Default(t.TempDir())
	cfg.DLsiteBaseURL = "https://dlsite.test"
	cfg.AsmrOneBaseURLs = []string{"https://asmrone.test"}
	var mu sync.Mutex
	var paths []string
	var starts []time.Time
	var productCalls int
	base := metadataRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		mu.Lock()
		paths = append(paths, req.URL.Host+req.URL.Path)
		starts = append(starts, time.Now())
		mu.Unlock()
		switch {
		case req.URL.Host == "dlsite.test" && strings.Contains(req.URL.Path, "product.json"):
			productCalls++
			if productCalls == 1 {
				return metadataHTTPResponse(req, http.StatusServiceUnavailable, "temporary"), nil
			}
			return metadataHTTPResponse(req, http.StatusOK, `[{"workno":"RJ01760149","work_name":"Work","maker_id":"RG12345","maker_name":"Circle","regist_date":"2026-11-14","age_category":3,"work_type":"SOU","price":100,"rate_average_2dp":4,"dl_count":1,"contents_file_size":1024,"image_main":{"url":"https://cover.test/cover.jpg"},"genres":[{"name":"Tag"}],"creaters":{"voice_by":[{"name":"Voice"}]}}]`), nil
		case req.URL.Host == "asmrone.test":
			return metadataHTTPResponse(req, http.StatusOK, `{"id":1760149,"source_id":"RJ01760149","title":"Work","duration":60,"release":"2026-11-14"}`), nil
		case req.URL.Host == "cover.test":
			resp := metadataHTTPResponse(req, http.StatusFound, "")
			resp.Header.Set("Location", "https://cdn.test/actual.jpg")
			return resp, nil
		case req.URL.Host == "cdn.test":
			return metadataHTTPResponse(req, http.StatusOK, "cover bytes"), nil
		default:
			return nil, fmt.Errorf("unexpected request %s", req.URL)
		}
	})
	transport := newMetadataRateTransport(base, time.Second/100)
	client, err := NewClientWithTransport(cfg, nil, 0, transport)
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	meta, err := client.Fetch(context.Background(), "RJ01760149", true)
	if err != nil || meta.Duration != 60 {
		t.Fatalf("metadata fallback failed: %+v %v", meta, err)
	}
	if _, err := client.DownloadCover(context.Background(), meta.Workno, meta.CoverURL); err != nil {
		t.Fatal(err)
	}
	// One normal retry backoff is expected; cfg's 3s/.5s intervals must not be
	// added on top of the supplied transport's shared limit.
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("per-source interval still active with global transport: elapsed=%v", elapsed)
	}
	if len(paths) != 5 || productCalls != 2 || paths[2] != "asmrone.test/api/workInfo/RJ01760149" || paths[3] != "cover.test/cover.jpg" || paths[4] != "cdn.test/actual.jpg" {
		t.Fatalf("fallback/retry/cover/redirect bypassed shared transport: %v", paths)
	}
	for i := 1; i < len(starts); i++ {
		if gap := starts[i].Sub(starts[i-1]); gap < 9*time.Millisecond {
			t.Fatalf("HTTP attempt bypassed limiter: gap=%v paths=%v", gap, paths)
		}
	}
}
