package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/SiIverAsh/otoha/internal/config"
	"github.com/SiIverAsh/otoha/internal/db"
	"github.com/SiIverAsh/otoha/internal/dlsite"
	"github.com/SiIverAsh/otoha/internal/scanner"
	"github.com/SiIverAsh/otoha/internal/search"
)

// TaskSnapshot represents the current scan/scrape background job state for REST and SSE (§9).
type TaskSnapshot struct {
	Running       bool                  `json:"running"`
	Phase         string                `json:"phase"` // "idle" | "scanning" | "scraping" | "reindexing" | "done" | "cancelled" | "error"
	Message       string                `json:"message"`
	WorksScanned  int                   `json:"works_scanned"`
	TracksScanned int                   `json:"tracks_scanned"`
	ScrapeCurrent int                   `json:"scrape_current"`
	ScrapeTotal   int                   `json:"scrape_total"`
	CurrentWorkno string                `json:"current_workno,omitempty"`
	Skipped       []scanner.SkippedPath `json:"skipped,omitempty"`
	StartedAt     int64                 `json:"started_at,omitempty"`
	UpdatedAt     int64                 `json:"updated_at"`
}

// TaskManager coordinates background library scan, DLsite metadata scraping, and SSE broadcasting.
type TaskManager struct {
	mu          sync.Mutex
	store       *db.Store
	search      *search.Engine
	cfg         *config.Config
	logger      *log.Logger
	state       TaskSnapshot
	cancelFn    context.CancelFunc
	subscribers map[chan TaskSnapshot]struct{}
}

func NewTaskManager(store *db.Store, searchEng *search.Engine, cfg *config.Config, logger *log.Logger) *TaskManager {
	return &TaskManager{
		store:  store,
		search: searchEng,
		cfg:    cfg,
		logger: logger,
		state: TaskSnapshot{
			Phase:     "idle",
			Message:   "就绪",
			UpdatedAt: time.Now().Unix(),
		},
		subscribers: make(map[chan TaskSnapshot]struct{}),
	}
}

func (tm *TaskManager) Snapshot() TaskSnapshot {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	cp := tm.state
	cp.Skipped = append([]scanner.SkippedPath(nil), tm.state.Skipped...)
	return cp
}

func (tm *TaskManager) SnapshotJSON() any {
	return tm.Snapshot()
}

func (tm *TaskManager) publish(mut func(*TaskSnapshot)) {
	tm.mu.Lock()
	mut(&tm.state)
	tm.state.UpdatedAt = time.Now().Unix()
	snap := tm.state
	snap.Skipped = append([]scanner.SkippedPath(nil), tm.state.Skipped...)
	subs := make([]chan TaskSnapshot, 0, len(tm.subscribers))
	for ch := range tm.subscribers {
		subs = append(subs, ch)
	}
	tm.mu.Unlock()

	for _, ch := range subs {
		select {
		case ch <- snap:
		default:
		}
	}
}

// Cancel interrupts any running scan or scrape job cleanly (preserving checkpoint for resume, §3.3).
func (tm *TaskManager) Cancel() bool {
	tm.mu.Lock()
	cancel := tm.cancelFn
	running := tm.state.Running
	tm.mu.Unlock()
	if running && cancel != nil {
		cancel()
		return true
	}
	return false
}

// TriggerScan launches an asynchronous media scan (+ optional DLsite metadata scrape).
func (tm *TaskManager) TriggerScan(storageID int64, scrapeMetadata bool, forceScrape bool) (bool, error) {
	tm.mu.Lock()
	if tm.state.Running {
		tm.mu.Unlock()
		return false, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	tm.cancelFn = cancel
	tm.state = TaskSnapshot{
		Running:   true,
		Phase:     "scanning",
		Message:   "正在扫描本地媒体库...",
		StartedAt: time.Now().Unix(),
		UpdatedAt: time.Now().Unix(),
	}
	tm.mu.Unlock()

	go tm.runScanPipeline(ctx, storageID, scrapeMetadata, forceScrape)
	return true, nil
}

func (tm *TaskManager) runScanPipeline(ctx context.Context, onlyStorageID int64, scrapeMetadata, forceScrape bool) {
	defer func() {
		tm.mu.Lock()
		tm.state.Running = false
		tm.cancelFn = nil
		tm.mu.Unlock()
	}()

	storages, err := tm.store.ListStorages(ctx)
	if err != nil {
		tm.publish(func(s *TaskSnapshot) {
			s.Running = false
			s.Phase = "error"
			s.Message = "读取存储目录失败: " + err.Error()
		})
		return
	}

	cfgSnap := tm.cfg.Snapshot()
	totalWorks := 0
	totalTracks := 0
	var allSkipped []scanner.SkippedPath

	for _, st := range storages {
		if st.Kind != "local" {
			continue
		}
		if onlyStorageID > 0 && st.ID != onlyStorageID {
			continue
		}
		stats, scanErr := scanner.ScanRoot(ctx, st.Root, scanner.Options{
			MaxDepth:       cfgSnap.ScanMaxDepth,
			IgnorePatterns: cfgSnap.ScanIgnorePatterns,
			Logger:         tm.logger,
		}, func(w scanner.DiscoveredWork) error {
			if err := tm.store.UpsertScannedWork(ctx, st.ID, w.Workno, w.FallbackTitle, w.RelPath, w.AgeHint, w.LocalCover, w.Tracks); err != nil {
				return err
			}
			if doc, err := tm.store.GetSearchDocument(ctx, w.Workno); err == nil {
				_ = tm.search.IndexWork(*doc)
			}
			totalWorks++
			totalTracks += len(w.Tracks)
			tm.publish(func(s *TaskSnapshot) {
				s.WorksScanned = totalWorks
				s.TracksScanned = totalTracks
				s.CurrentWorkno = w.Workno
				s.Message = fmt.Sprintf("已扫描 %d 部作品 (%d 条音轨) · %s", totalWorks, totalTracks, w.Workno)
			})
			return nil
		})
		if stats != nil && len(stats.Skipped) > 0 {
			allSkipped = append(allSkipped, stats.Skipped...)
		}
		if errors.Is(scanErr, context.Canceled) {
			tm.publish(func(s *TaskSnapshot) {
				s.Running = false
				s.Phase = "cancelled"
				s.Skipped = allSkipped
				s.Message = fmt.Sprintf("扫描已中断（已完成 %d 部作品）", totalWorks)
			})
			return
		}
		if scanErr != nil {
			tm.publish(func(s *TaskSnapshot) {
				s.Running = false
				s.Phase = "error"
				s.Skipped = allSkipped
				s.Message = "扫描出错: " + scanErr.Error()
			})
			return
		}
	}

	if scrapeMetadata {
		unscraped, err := tm.store.ListUnscrapedWorknos(ctx, forceScrape)
		if err == nil && len(unscraped) > 0 {
			cache, cErr := dlsite.NewCache(tm.cfg.CacheDir())
			if cErr == nil {
				client, clErr := dlsite.NewClient(dlsite.ClientOptions{
					BaseURL:     cfgSnap.DLsiteBaseURL,
					Locale:      cfgSnap.ScraperLocale,
					UserAgent:   cfgSnap.ScraperUserAgent,
					ProxyURL:    cfgSnap.ScraperProxy,
					MinInterval: time.Duration(cfgSnap.ScraperIntervalSec * float64(time.Second)),
					Cache:       cache,
					CoversDir:   tm.cfg.CoversDir(),
				})
				if clErr == nil {
					tm.publish(func(s *TaskSnapshot) {
						s.Phase = "scraping"
						s.ScrapeTotal = len(unscraped)
						s.Message = fmt.Sprintf("正在抓取 DLsite 元数据 (0/%d)...", len(unscraped))
					})
					_, scrapeErr := client.ScrapeBatch(ctx, unscraped, forceScrape, func(p dlsite.BatchProgress) {
						if p.Meta != nil && !p.Meta.NotFound {
							coverPath, _ := client.DownloadCover(ctx, p.Workno, p.Meta.CoverURL)
							_ = tm.store.SaveMetadata(ctx, p.Meta, coverPath, true)
							if doc, dErr := tm.store.GetSearchDocument(ctx, p.Workno); dErr == nil {
								_ = tm.search.IndexWork(*doc)
							}
						}
						tm.publish(func(s *TaskSnapshot) {
							s.Phase = "scraping"
							s.ScrapeCurrent = p.Current
							s.ScrapeTotal = p.Total
							s.CurrentWorkno = p.Workno
							s.Message = fmt.Sprintf("抓取元数据 (%d/%d) · %s [%s]", p.Current, p.Total, p.Workno, p.Status)
						})
					})
					if errors.Is(scrapeErr, context.Canceled) {
						tm.publish(func(s *TaskSnapshot) {
							s.Running = false
							s.Phase = "cancelled"
							s.Skipped = allSkipped
							s.Message = "元数据抓取已中断，下次将继续断点续传"
						})
						return
					}
				}
			}
		}
	}

	tm.publish(func(s *TaskSnapshot) {
		s.Running = false
		s.Phase = "done"
		s.Skipped = allSkipped
		s.CurrentWorkno = ""
		s.Message = fmt.Sprintf("完成：共发现 %d 部作品，%d 条音轨（跳过 %d 项）", totalWorks, totalTracks, len(allSkipped))
	})
}

// ServeSSE streams real-time task progress events via Server-Sent Events (§9).
func (tm *TaskManager) ServeSSE(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	ch := make(chan TaskSnapshot, 16)
	tm.mu.Lock()
	tm.subscribers[ch] = struct{}{}
	initial := tm.state
	tm.mu.Unlock()

	defer func() {
		tm.mu.Lock()
		delete(tm.subscribers, ch)
		tm.mu.Unlock()
	}()

	writeEvent := func(snap TaskSnapshot) error {
		raw, err := json.Marshal(snap)
		if err != nil {
			return err
		}
		if _, err := fmt.Fprintf(w, "data: %s\n\n", raw); err != nil {
			return err
		}
		flusher.Flush()
		return nil
	}

	if err := writeEvent(initial); err != nil {
		return
	}

	keepAlive := time.NewTicker(20 * time.Second)
	defer keepAlive.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case snap := <-ch:
			if err := writeEvent(snap); err != nil {
				return
			}
		case <-keepAlive.C:
			if _, err := fmt.Fprint(w, ": ping\n\n"); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}
