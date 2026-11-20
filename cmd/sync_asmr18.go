package cmd

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/SiIverAsh/otoha/internal/asmr18"
	"github.com/SiIverAsh/otoha/internal/config"
	"github.com/SiIverAsh/otoha/internal/db"
	"github.com/SiIverAsh/otoha/internal/scrape"
	"github.com/SiIverAsh/otoha/internal/search"
)

func runSyncASMR18(args []string) error {
	fs := flag.NewFlagSet("sync-asmr18", flag.ContinueOnError)
	data := fs.String("data", "data", "Data directory")
	proxy := fs.String("proxy", "", "HTTP/SOCKS5 proxy override")
	category := fs.String("category", "allages", "allages | boys | girls")
	start := fs.Int("start-page", 1, "Starting category page")
	pages := fs.Int("max-pages", 1, "Maximum category pages (0 = all)")
	limit := fs.Int("max-works", 24, "Maximum works (0 = all)")
	interval := fs.Float64("interval", asmr18.DefaultInterval.Seconds(), "Global minimum seconds between requests across all workers")
	dry := fs.Bool("dry-run", false, "Parse work pages without writing the library")
	workers := fs.Int("workers", scrape.DefaultASMR18Workers, "Concurrent work fetches (1..16; requests share one rate limit)")
	probe := fs.Bool("probe-duration", false, "Also fetch HLS manifests to validate sources and determine duration")
	refresh := fs.Bool("refresh", false, "Refresh already imported ASMR18 sources instead of skipping them")
	if err := fs.Parse(args); err != nil {
		return err
	}
	opts := scrape.ASMR18Options{URLs: fs.Args(), Category: *category, StartPage: *start, MaxPages: *pages, MaxWorks: *limit, DryRun: *dry, Workers: *workers, IntervalSec: interval, ProbeDuration: *probe, Refresh: *refresh}
	if err := opts.Validate(); err != nil {
		return err
	}
	cfg, err := config.Load(*data)
	if err != nil {
		return err
	}
	if *proxy != "" {
		cfg.ScraperProxy = *proxy
	}
	var store *db.Store
	var index *search.Engine
	if !*dry {
		store, err = db.Open(cfg.DBPath())
		if err != nil {
			return err
		}
		defer store.Close()
		index = search.Open(cfg.BlevePath())
		defer index.Close()
		if degraded, warning := index.IsDegraded(); degraded {
			return fmt.Errorf("search index unavailable; stop the running server or use its ASMR18 settings panel: %s", warning)
		}
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	_, err = scrape.SyncASMR18(ctx, store, index, cfg, opts, func(p scrape.ASMR18Progress) {
		if p.Preview != nil {
			raw, _ := json.MarshalIndent(p.Preview, "", "  ")
			fmt.Println(string(raw))
		}
		fmt.Printf("[ASMR18] %s page=%d processed=%d new=%d merged=%d skipped=%d tracks=%d failed=%d %.2f works/s %s\n", p.Workno, p.CurrentPage, p.Processed, p.Inserted, p.Merged, p.Skipped, p.Tracks, p.Failures, p.WorksPerSec, p.Message)
	})
	return err
}
