package scrape

import (
	"context"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SiIverAsh/otoha/internal/asmr18"
	"github.com/SiIverAsh/otoha/internal/db"
	"github.com/SiIverAsh/otoha/internal/dlsite"
	"github.com/SiIverAsh/otoha/internal/search"
)

type fakeASMR18Source struct {
	mu     sync.Mutex
	calls  []string
	probes []bool
	fetch  func(context.Context, string, asmr18.FetchOptions) (*asmr18.Work, error)
	list   func(context.Context, string, int) ([]string, bool, error)
}

func (f *fakeASMR18Source) FetchWithOptions(ctx context.Context, raw string, opts asmr18.FetchOptions) (*asmr18.Work, error) {
	f.mu.Lock()
	f.calls = append(f.calls, raw)
	f.probes = append(f.probes, opts.ProbeDuration)
	f.mu.Unlock()
	if f.fetch != nil {
		return f.fetch(ctx, raw, opts)
	}
	w := fakeASMR18Work(raw)
	if opts.ProbeDuration {
		w.Tracks[0].Duration = 120
	}
	return w, nil
}
func (f *fakeASMR18Source) List(ctx context.Context, cat string, page int) ([]string, bool, error) {
	if f.list != nil {
		return f.list(ctx, cat, page)
	}
	return nil, false, fmt.Errorf("unexpected list request")
}
func fakeASMR18Work(raw string) *asmr18.Work {
	id := dlsite.ExtractWorknoFromPath(raw)
	return &asmr18.Work{Metadata: dlsite.Metadata{Workno: id, Title: "Work " + id, AgeCategory: 1}, Tracks: []asmr18.Track{{Key: "file1-1", Title: "Main", URL: "https://cdn3.cloudintech.net/file/" + id + "/main.m3u8", Chapters: []asmr18.Chapter{{Title: "Chapter", Start: 0}}}}}
}
func asmr18TestStore(t *testing.T) *db.Store {
	t.Helper()
	s, err := db.Open(filepath.Join(t.TempDir(), "library.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}
func asmr18TestURLs(n int) []string {
	var out []string
	for i := 0; i < n; i++ {
		out = append(out, fmt.Sprintf("https://asmr18.fans/allages/rj%06d/", i+1))
	}
	return out
}

func TestASMR18ConcurrentFetchSingleWriterAndRJDedup(t *testing.T) {
	s := asmr18TestStore(t)
	urls := asmr18TestURLs(6)
	urls = append(urls, urls[0], "https://asmr18.fans/boys/rj000001/")
	gate := make(chan struct{})
	entered := make(chan struct{}, 6)
	var active, peak atomic.Int32
	f := &fakeASMR18Source{fetch: func(ctx context.Context, u string, _ asmr18.FetchOptions) (*asmr18.Work, error) {
		n := active.Add(1)
		defer active.Add(-1)
		for {
			old := peak.Load()
			if n <= old || peak.CompareAndSwap(old, n) {
				break
			}
		}
		entered <- struct{}{}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-gate:
			return fakeASMR18Work(u), nil
		}
	}}
	type outcome struct {
		p   ASMR18Progress
		err error
	}
	done := make(chan outcome, 1)
	go func() {
		p, err := syncASMR18(context.Background(), s, nil, f, ASMR18Options{URLs: urls, Workers: 3}, nil)
		done <- outcome{p, err}
	}()
	for i := 0; i < 3; i++ {
		select {
		case <-entered:
		case <-time.After(3 * time.Second):
			t.Fatal("fetches did not overlap")
		}
	}
	if peak.Load() != 3 {
		t.Fatalf("peak concurrency=%d", peak.Load())
	}
	close(gate)
	select {
	case r := <-done:
		if r.err != nil || r.p.Inserted != 6 || r.p.Processed != 6 {
			t.Fatalf("%+v %v", r.p, r.err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("workers did not finish")
	}
	if active.Load() != 0 || peak.Load() > 3 || len(f.calls) != 6 {
		t.Fatalf("active=%d peak=%d calls=%d", active.Load(), peak.Load(), len(f.calls))
	}
	var count int
	s.DB().QueryRow(`SELECT COUNT(*) FROM work`).Scan(&count)
	if count != 6 {
		t.Fatal("duplicate RJ records")
	}
}

func TestASMR18SkipRefreshProbeAndDryRun(t *testing.T) {
	s := asmr18TestStore(t)
	ctx := context.Background()
	urls := asmr18TestURLs(1)
	f := &fakeASMR18Source{}
	if err := s.SaveMetadata(ctx, &dlsite.Metadata{Workno: "RJ000001", Title: "Keep existing metadata"}, "", true); err != nil {
		t.Fatal(err)
	}
	opts := ASMR18Options{URLs: urls}
	p, err := syncASMR18(ctx, s, nil, f, opts, nil)
	if err != nil || p.Merged != 1 || len(f.calls) != 1 {
		t.Fatalf("existing work from other sources must not skip: %+v %v", p, err)
	}
	d, err := s.GetWorkDetail(ctx, "RJ000001", 1)
	if err != nil {
		t.Fatal(err)
	}
	trackID := d.Tracks[0].ID
	p, err = syncASMR18(ctx, s, nil, f, opts, nil)
	if err != nil || p.Skipped != 1 || p.Processed != 0 || len(f.calls) != 1 {
		t.Fatalf("skip: %+v %v", p, err)
	}
	opts.ProbeDuration = true
	p, err = syncASMR18(ctx, s, nil, f, opts, nil)
	if err != nil || p.Skipped != 0 || p.Merged != 1 || len(f.calls) != 2 || !f.probes[1] {
		t.Fatalf("fill missing duration: %+v %v", p, err)
	}
	p, err = syncASMR18(ctx, s, nil, f, opts, nil)
	if err != nil || p.Skipped != 1 || len(f.calls) != 2 {
		t.Fatalf("known duration should skip: %+v %v", p, err)
	}
	opts.ProbeDuration = false
	opts.Refresh = true
	p, err = syncASMR18(ctx, s, nil, f, opts, nil)
	if err != nil || p.Merged != 1 || len(f.calls) != 3 {
		t.Fatalf("refresh: %+v %v", p, err)
	}
	d, err = s.GetWorkDetail(ctx, "RJ000001", 1)
	if err != nil || d.Title != "Keep existing metadata" || d.Tracks[0].ID != trackID || d.Tracks[0].Duration == nil || *d.Tracks[0].Duration != 120 {
		t.Fatalf("lost metadata, duration or track ID: %+v %v", d, err)
	}
	opts.DryRun = true
	opts.Refresh = false
	p, err = syncASMR18(ctx, nil, nil, f, opts, nil)
	if err != nil || p.Preview == nil || p.Processed != 1 || len(f.calls) != 4 {
		t.Fatalf("dry-run must fetch existing work without a store: %+v %v", p, err)
	}
}

func TestASMR18FetchBudgetAndPaging(t *testing.T) {
	s := asmr18TestStore(t)
	ctx := context.Background()
	urls := asmr18TestURLs(5)
	f := &fakeASMR18Source{}
	if _, err := syncASMR18(ctx, s, nil, f, ASMR18Options{URLs: urls[:1]}, nil); err != nil {
		t.Fatal(err)
	}
	f.calls = nil
	pages := 0
	f.list = func(_ context.Context, _ string, page int) ([]string, bool, error) {
		pages++
		if page == 1 {
			return urls[:2], true, nil
		}
		return urls[2:], true, nil
	}
	p, err := syncASMR18(ctx, s, nil, f, ASMR18Options{Category: "allages", MaxWorks: 2, Workers: 4}, nil)
	if err != nil || len(f.calls) != 2 || p.Skipped != 1 || p.Inserted != 2 || p.Processed != 2 || pages != 2 {
		t.Fatalf("budget overshoot: %+v calls=%d pages=%d err=%v", p, len(f.calls), pages, err)
	}
}

func TestASMR18CancellationJoinsWorkers(t *testing.T) {
	s := asmr18TestStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	entered := make(chan struct{}, 2)
	var active atomic.Int32
	f := &fakeASMR18Source{fetch: func(ctx context.Context, _ string, _ asmr18.FetchOptions) (*asmr18.Work, error) {
		active.Add(1)
		defer active.Add(-1)
		entered <- struct{}{}
		<-ctx.Done()
		return nil, ctx.Err()
	}}
	done := make(chan error, 1)
	go func() {
		_, err := syncASMR18(ctx, s, nil, f, ASMR18Options{URLs: asmr18TestURLs(8), Workers: 2}, nil)
		done <- err
	}()
	for i := 0; i < 2; i++ {
		select {
		case <-entered:
		case <-time.After(3 * time.Second):
			t.Fatal("missing worker")
		}
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("cancelled workers leaked")
	}
	if active.Load() != 0 {
		t.Fatal("workers still running")
	}
}

func TestASMR18StopsOnUpstreamRestrictions(t *testing.T) {
	for _, restriction := range []error{&asmr18.HTTPError{StatusCode: 429, RetryAfter: "30"}, &asmr18.HTTPError{StatusCode: 403}, &asmr18.HTTPError{StatusCode: 503}, asmr18.ErrChallenge} {
		t.Run(restriction.Error(), func(t *testing.T) {
			f := &fakeASMR18Source{fetch: func(context.Context, string, asmr18.FetchOptions) (*asmr18.Work, error) { return nil, restriction }}
			p, err := syncASMR18(context.Background(), nil, nil, f, ASMR18Options{URLs: asmr18TestURLs(8), Workers: 1, DryRun: true}, nil)
			if err == nil || p.Failures != 1 || len(f.calls) != 1 {
				t.Fatalf("did not stop: %+v calls=%d err=%v", p, len(f.calls), err)
			}
		})
	}
}

func TestASMR18OptionValidation(t *testing.T) {
	for _, opts := range []ASMR18Options{{Workers: 17}, {Workers: -1}, {IntervalSec: ptrASMR18Float(-1)}, {IntervalSec: ptrASMR18Float(math.NaN())}, {IntervalSec: ptrASMR18Float(math.Inf(1))}, {IntervalSec: ptrASMR18Float(1e100)}} {
		if err := opts.Validate(); err == nil {
			t.Fatalf("accepted %+v", opts)
		}
	}
}
func ptrASMR18Float(v float64) *float64 { return &v }

func TestASMR18SkipRepairsMissingSearchDocument(t *testing.T) {
	s := asmr18TestStore(t)
	ctx := context.Background()
	urls := asmr18TestURLs(1)
	f := &fakeASMR18Source{}
	if _, err := syncASMR18(ctx, s, nil, f, ASMR18Options{URLs: urls}, nil); err != nil {
		t.Fatal(err)
	}
	index := search.Open(filepath.Join(t.TempDir(), "index.bleve"))
	defer index.Close()
	if degraded, msg := index.IsDegraded(); degraded {
		t.Fatal(msg)
	}
	result, err := index.Search("RJ000001", 10)
	if err != nil || len(result.Worknos) != 0 {
		t.Fatalf("expected initially unindexed work: %+v %v", result, err)
	}
	p, err := syncASMR18(ctx, s, index, f, ASMR18Options{URLs: urls}, nil)
	if err != nil || p.Skipped != 1 || len(f.calls) != 1 {
		t.Fatalf("skip repair fetched remotely: %+v %v", p, err)
	}
	result, err = index.Search("RJ000001", 10)
	if err != nil || len(result.Worknos) != 1 || result.Worknos[0] != "RJ000001" {
		t.Fatalf("missing search repair: %+v %v", result, err)
	}
}
