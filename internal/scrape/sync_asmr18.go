package scrape

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/SiIverAsh/otoha/internal/asmr18"
	"github.com/SiIverAsh/otoha/internal/config"
	"github.com/SiIverAsh/otoha/internal/db"
	"github.com/SiIverAsh/otoha/internal/search"
)

const DefaultASMR18Workers = 4

type ASMR18Options struct {
	URLs          []string `json:"urls"`
	Category      string   `json:"category"`
	StartPage     int      `json:"start_page"`
	MaxPages      int      `json:"max_pages"`
	MaxWorks      int      `json:"max_works"`
	DryRun        bool     `json:"dry_run"`
	Workers       int      `json:"workers"`                // 0 uses DefaultASMR18Workers
	IntervalSec   *float64 `json:"interval_sec,omitempty"` // nil uses asmr18.DefaultInterval
	ProbeDuration bool     `json:"probe_duration"`
	Refresh       bool     `json:"refresh"`
}

func (o ASMR18Options) Validate() error {
	if o.StartPage < 0 || o.MaxPages < 0 || o.MaxWorks < 0 {
		return fmt.Errorf("page and work limits cannot be negative")
	}
	if o.Workers < 0 || o.Workers > 16 {
		return fmt.Errorf("workers must be between 1 and 16 (0 uses the default)")
	}
	if o.IntervalSec != nil && (math.IsNaN(*o.IntervalSec) || math.IsInf(*o.IntervalSec, 0) || *o.IntervalSec < 0 || *o.IntervalSec > float64((1<<63-1)/int64(time.Second))) {
		return fmt.Errorf("interval must be a finite nonnegative number")
	}
	if len(o.URLs) > 0 {
		for _, u := range o.URLs {
			if _, err := asmr18.PageURL(u); err != nil {
				return err
			}
		}
		return nil
	}
	if o.Category != "" && o.Category != "allages" && o.Category != "boys" && o.Category != "girls" {
		return fmt.Errorf("invalid category")
	}
	return nil
}

type ASMR18Progress struct {
	Preview     *asmr18.Work `json:"preview,omitempty"`
	Processed   int          `json:"processed"`
	Inserted    int          `json:"inserted"`
	Merged      int          `json:"merged"`
	Skipped     int          `json:"skipped"`
	Tracks      int          `json:"tracks"`
	Failures    int          `json:"failures"`
	CurrentPage int          `json:"current_page"`
	WorksPerSec float64      `json:"works_per_sec"`
	Workno      string       `json:"workno"`
	Message     string       `json:"message"`
}

func blockingASMR18Error(err error) bool {
	var upstream *asmr18.HTTPError
	return errors.Is(err, asmr18.ErrChallenge) || (errors.As(err, &upstream) && (upstream.StatusCode == 403 || upstream.StatusCode == 429 || upstream.StatusCode == 503))
}

type asmr18Source interface {
	FetchWithOptions(context.Context, string, asmr18.FetchOptions) (*asmr18.Work, error)
	List(context.Context, string, int) ([]string, bool, error)
}

func SyncASMR18(ctx context.Context, store *db.Store, index *search.Engine, cfg *config.Config, opts ASMR18Options, progress func(ASMR18Progress)) (ASMR18Progress, error) {
	if err := opts.Validate(); err != nil {
		return ASMR18Progress{}, err
	}
	snap := cfg.Snapshot()
	interval := asmr18.DefaultInterval
	if opts.IntervalSec != nil {
		interval = time.Duration(*opts.IntervalSec * float64(time.Second))
	}
	client, err := asmr18.New(snap.ScraperProxy, interval)
	if err != nil {
		return ASMR18Progress{}, err
	}
	return syncASMR18(ctx, store, index, client, opts, progress)
}

func syncASMR18(ctx context.Context, store *db.Store, index *search.Engine, client asmr18Source, opts ASMR18Options, progress func(ASMR18Progress)) (ASMR18Progress, error) {
	var p ASMR18Progress
	if err := opts.Validate(); err != nil {
		return p, err
	}
	if !opts.DryRun && store == nil {
		return p, fmt.Errorf("ASMR18 import requires a store")
	}
	workers := opts.Workers
	if workers == 0 {
		workers = DefaultASMR18Workers
	}
	completed := map[string]bool{}
	if !opts.DryRun && !opts.Refresh {
		ids, err := store.ListASMR18Worknos(ctx, opts.ProbeDuration)
		if err != nil {
			return p, err
		}
		for _, id := range ids {
			completed[id] = true
		}
	}
	started := time.Now()
	seen := map[string]bool{}
	submitted := 0
	var failures []error
	notify := func() {
		elapsed := time.Since(started).Seconds()
		if elapsed > 0 {
			p.WorksPerSec = float64(p.Processed-p.Failures) / elapsed
		}
		if progress != nil {
			progress(p)
		}
	}
	type job struct{ url, workno string }
	type result struct {
		job  job
		work *asmr18.Work
		err  error
	}
	processBatch := func(rawURLs []string) error {
		var pending []job
		var skippedIDs []string
		skippedBefore := p.Skipped
		for _, raw := range rawURLs {
			if err := ctx.Err(); err != nil {
				return err
			}
			if opts.MaxWorks > 0 && submitted >= opts.MaxWorks {
				break
			}
			canonical, err := asmr18.PageURL(raw)
			if err != nil {
				return err
			}
			parts := strings.Split(strings.TrimRight(canonical, "/"), "/")
			id := strings.ToUpper(parts[len(parts)-1])
			if seen[id] {
				continue
			}
			seen[id] = true
			if completed[id] {
				skippedIDs = append(skippedIDs, id)
				p.Skipped++
				p.Workno = id
				continue
			}
			pending = append(pending, job{canonical, id})
			submitted++
		}
		// Source commits and search updates are separate. Repair skipped documents
		// locally as well, so a prior cancellation/index failure never hides a work.
		if index != nil && len(skippedIDs) > 0 {
			docs := make([]db.SearchDocument, 0, len(skippedIDs))
			for _, id := range skippedIDs {
				doc, err := store.GetSearchDocument(ctx, id)
				if err != nil {
					return err
				}
				docs = append(docs, *doc)
			}
			if err := index.IndexWorkBatch(docs); err != nil {
				return fmt.Errorf("index existing ASMR18 sources: %w", err)
			}
		}
		if p.Skipped != skippedBefore {
			notify()
		}
		if len(pending) == 0 {
			return nil
		}
		batchCtx, cancel := context.WithCancel(ctx)
		defer cancel()
		jobs := make(chan job, len(pending))
		results := make(chan result, workers)
		for _, j := range pending {
			jobs <- j
		}
		close(jobs)
		var wg sync.WaitGroup
		for i := 0; i < workers && i < len(pending); i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for {
					if batchCtx.Err() != nil {
						return
					}
					var j job
					var ok bool
					select {
					case <-batchCtx.Done():
						return
					case j, ok = <-jobs:
						if !ok {
							return
						}
					}
					if batchCtx.Err() != nil {
						return
					}
					w, err := client.FetchWithOptions(batchCtx, j.url, asmr18.FetchOptions{ProbeDuration: opts.ProbeDuration})
					if blockingASMR18Error(err) {
						cancel()
					}
					results <- result{j, w, err}
				}
			}()
		}
		go func() { wg.Wait(); close(results) }()
		var stopped error
		for r := range results {
			if ctx.Err() != nil {
				continue
			}
			if errors.Is(r.err, context.Canceled) && batchCtx.Err() != nil {
				continue
			}
			p.Processed++
			p.Workno = r.job.workno
			p.Preview = nil
			err := r.err
			if err == nil && r.work == nil {
				err = fmt.Errorf("ASMR18 returned no work")
			}
			if err == nil {
				w := r.work
				if w.Metadata.Workno != r.job.workno {
					err = fmt.Errorf("ASMR18 returned mismatched RJ %s for %s", w.Metadata.Workno, r.job.workno)
				} else {
					if opts.DryRun {
						p.Preview = w
					}
					inputs := make([]db.RemoteTrackInput, 0, len(w.Tracks))
					for i, tr := range w.Tracks {
						in := db.RemoteTrackInput{Title: tr.Title, RelPath: "ASMR18/" + tr.Key + ".m3u8", StreamURL: tr.URL, Ord: i + 1, Duration: tr.Duration}
						for _, ch := range tr.Chapters {
							in.Chapters = append(in.Chapters, db.Chapter{Title: ch.Title, Start: ch.Start})
						}
						inputs = append(inputs, in)
					}
					if !opts.DryRun {
						var inserted bool
						inserted, err = store.SaveASMR18Work(ctx, &w.Metadata, r.job.url, inputs)
						if err == nil {
							if inserted {
								p.Inserted++
							} else {
								p.Merged++
							}
							if index != nil {
								doc, e := store.GetSearchDocument(ctx, w.Metadata.Workno)
								if e == nil {
									e = index.IndexWork(*doc)
								}
								err = e
							}
						}
					}
					if err == nil {
						p.Tracks += len(inputs)
					}
				}
			}
			if err != nil {
				p.Failures++
				p.Message = err.Error()
				wrapped := fmt.Errorf("%s: %w", r.job.url, err)
				failures = append(failures, wrapped)
				if blockingASMR18Error(err) {
					if stopped == nil {
						stopped = wrapped
						cancel()
					}
				}
			} else {
				p.Message = ""
			}
			notify()
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		return stopped
	}
	if len(opts.URLs) > 0 {
		if err := processBatch(opts.URLs); err != nil {
			return p, err
		}
	} else {
		category := opts.Category
		if category == "" {
			category = "allages"
		}
		page := opts.StartPage
		if page == 0 {
			page = 1
		}
		for n := 0; opts.MaxPages == 0 || n < opts.MaxPages; n++ {
			if err := ctx.Err(); err != nil {
				return p, err
			}
			p.CurrentPage = page + n
			links, next, err := client.List(ctx, category, p.CurrentPage)
			if err != nil {
				return p, err
			}
			if len(links) == 0 {
				return p, fmt.Errorf("ASMR18 page %d has no work links; check site layout", p.CurrentPage)
			}
			if err := processBatch(links); err != nil {
				return p, err
			}
			if !next || (opts.MaxWorks > 0 && submitted >= opts.MaxWorks) {
				break
			}
		}
	}
	notify()
	return p, errors.Join(failures...)
}
