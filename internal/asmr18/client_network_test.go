package asmr18

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func responseFor(r *http.Request, status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: r}
}

func TestFetchCanOmitDurationProbe(t *testing.T) {
	fixture, err := os.ReadFile("testdata/work.html")
	if err != nil {
		t.Fatal(err)
	}
	for _, probe := range []bool{false, true} {
		t.Run(map[bool]string{false: "page-only", true: "with-duration"}[probe], func(t *testing.T) {
			var pageRequests, manifestRequests int
			c := &Client{http: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				if r.URL.Host == "asmr18.fans" {
					pageRequests++
					return responseFor(r, 200, string(fixture)), nil
				}
				if r.URL.Host == "cdn3.cloudintech.net" {
					manifestRequests++
					return responseFor(r, 200, "#EXTM3U\n#EXTINF:10.5,\nfirst.ts\n#EXTINF:21,\nsecond.ts\n#EXT-X-ENDLIST\n"), nil
				}
				t.Fatalf("unexpected request %s", r.URL)
				return nil, nil
			})}}
			var w *Work
			var err error
			if probe {
				w, err = c.Fetch(context.Background(), BaseURL+"/allages/rj01752865/")
			} else {
				w, err = c.FetchWithOptions(context.Background(), BaseURL+"/allages/rj01752865/", FetchOptions{})
			}
			if err != nil {
				t.Fatal(err)
			}
			if pageRequests != 1 || len(w.Tracks) != 1 || len(w.Tracks[0].Chapters) != 5 {
				t.Fatalf("lost page data or repeated page request: requests=%d work=%+v", pageRequests, w)
			}
			if probe {
				if manifestRequests != 1 || w.Tracks[0].Duration != 31.5 {
					t.Fatalf("probe: requests=%d duration=%f", manifestRequests, w.Tracks[0].Duration)
				}
			} else if manifestRequests != 0 || w.Tracks[0].Duration != 0 {
				t.Fatalf("unexpected probe: requests=%d duration=%f", manifestRequests, w.Tracks[0].Duration)
			}
		})
	}
}

func TestProbeMediaDurationDoesNotUseAnUnfinishedPlaylist(t *testing.T) {
	client := &Client{http: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return responseFor(r, http.StatusOK, "#EXTM3U\n#EXTINF:12.5,\nfirst.ts\n"), nil
	})}}
	duration, err := client.ProbeMediaDuration(context.Background(), "https://cdn3.cloudintech.net/file/RJ01752865/main.m3u8")
	if err != nil || duration != 0 {
		t.Fatalf("unfinished playlist returned duration %g, error %v", duration, err)
	}
}

func TestConcurrentFetchAndListShareGlobalRequestSpacing(t *testing.T) {
	const interval = 25 * time.Millisecond
	const latency = 90 * time.Millisecond
	const requests = 6
	var mu sync.Mutex
	var starts []time.Time
	active, maxActive := 0, 0
	c := &Client{interval: interval, http: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		mu.Lock()
		starts = append(starts, time.Now())
		active++
		if active > maxActive {
			maxActive = active
		}
		mu.Unlock()
		defer func() { mu.Lock(); active--; mu.Unlock() }()
		timer := time.NewTimer(latency)
		defer timer.Stop()
		select {
		case <-r.Context().Done():
			return nil, r.Context().Err()
		case <-timer.C:
		}
		if strings.Contains(r.URL.Path, "/rj") {
			return responseFor(r, 200, `<h1>Work</h1><script>const src='https://cdn3.cloudintech.net/file/RJ123456/1.m3u8';</script>`), nil
		}
		return responseFor(r, 200, `<div id="main"><a href="https://asmr18.fans/allages/rj123456/">Work</a></div>`), nil
	})}}
	var wg sync.WaitGroup
	errs := make(chan error, requests)
	ready := make(chan struct{})
	for i := 0; i < requests; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-ready
			if i%2 == 0 {
				_, err := c.FetchWithOptions(context.Background(), BaseURL+"/allages/rj123456/", FetchOptions{})
				errs <- err
			} else {
				_, _, err := c.List(context.Background(), "allages", 1)
				errs <- err
			}
		}(i)
	}
	close(ready)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(starts) != requests {
		t.Fatalf("sent %d requests", len(starts))
	}
	sort.Slice(starts, func(i, j int) bool { return starts[i].Before(starts[j]) })
	if elapsed := starts[len(starts)-1].Sub(starts[0]); elapsed < (requests-1)*interval-10*time.Millisecond {
		t.Fatalf("request starts were not globally paced: %s", elapsed)
	}
	for i := 1; i < len(starts); i++ {
		if gap := starts[i].Sub(starts[i-1]); gap < interval-5*time.Millisecond {
			t.Fatalf("burst between request starts: %s", gap)
		}
	}
	if maxActive < 2 {
		t.Fatal("rate limiter serialized responses instead of allowing concurrent requests")
	}
}

func TestRateWaitCancellationDoesNotWaitForOtherCallers(t *testing.T) {
	var requests atomic.Int32
	c := &Client{interval: time.Second, http: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) { requests.Add(1); return responseFor(r, 200, "ok"), nil })}}
	if _, err := c.get(context.Background(), BaseURL+"/"); err != nil {
		t.Fatal(err)
	}
	otherCtx, stopOther := context.WithCancel(context.Background())
	defer stopOther()
	otherDone := make(chan error, 1)
	go func() { _, err := c.get(otherCtx, BaseURL+"/"); otherDone <- err }()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err := c.get(ctx, BaseURL+"/")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected cancellation, got %v", err)
	}
	if elapsed := time.Since(started); elapsed > 300*time.Millisecond {
		t.Fatalf("cancellation waited for unrelated rate waiter: %s", elapsed)
	}
	stopOther()
	select {
	case err := <-otherDone:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(300 * time.Millisecond):
		t.Fatal("other caller did not cancel")
	}
	if requests.Load() != 1 {
		t.Fatalf("canceled callers issued requests: %d", requests.Load())
	}
}

func TestHTTPErrorKeepsStatusAndRetryAfterThroughProbe(t *testing.T) {
	var manifestRequests int
	c := &Client{http: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host == "asmr18.fans" {
			return responseFor(r, 200, `<h1>Work</h1><script>const src='https://cdn3.cloudintech.net/file/RJ123456/1.m3u8';</script>`), nil
		}
		manifestRequests++
		res := responseFor(r, 429, "wait")
		res.Header.Set("Retry-After", "12")
		return res, nil
	})}}
	_, err := c.Fetch(context.Background(), BaseURL+"/allages/rj123456/")
	var upstream *HTTPError
	if !errors.As(err, &upstream) || upstream.StatusCode != 429 || upstream.RetryAfter != "12" {
		t.Fatalf("lost upstream rate response: %v", err)
	}
	if manifestRequests != 1 {
		t.Fatalf("unexpected automatic retry: %d", manifestRequests)
	}
}

func TestSuccessfulChallengeResponseIsNotParsedAsWork(t *testing.T) {
	for _, body := range []string{`<title>Just a moment...</title>`, `<script src="/cdn-cgi/challenge-platform/test.js"></script>`} {
		c := &Client{http: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) { return responseFor(r, 200, body), nil })}}
		_, err := c.FetchWithOptions(context.Background(), BaseURL+"/allages/rj123456/", FetchOptions{})
		if !errors.Is(err, ErrChallenge) {
			t.Fatalf("expected challenge signal, got %v", err)
		}
	}
	c := &Client{http: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		res := responseFor(r, 200, "challenge")
		res.Header.Set("CF-Mitigated", "challenge")
		return res, nil
	})}}
	if _, err := c.get(context.Background(), BaseURL+"/"); !errors.Is(err, ErrChallenge) {
		t.Fatalf("expected challenge-header signal, got %v", err)
	}
}
