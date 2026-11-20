package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/SiIverAsh/otoha/internal/config"
	"github.com/SiIverAsh/otoha/internal/db"
	"github.com/SiIverAsh/otoha/internal/scrape"
	"github.com/SiIverAsh/otoha/internal/search"
)

const metadataIndexLockTimeout = time.Second

// runEnrichMetadata repairs catalog fields without fetching or replacing tracks.
// The server must release its search-index lock before this offline command runs.
func runEnrichMetadata(args []string) error {
	fs := flag.NewFlagSet("enrich-metadata", flag.ContinueOnError)
	dataDir := fs.String("data", "data", "Data directory containing the imported ASMR18 library")
	proxy := fs.String("proxy", "", "HTTP/SOCKS5 proxy override for this run")
	all := fs.Bool("all", false, "Fetch metadata for every imported ASMR18 work, including works with dates")
	dateOnly := fs.Bool("date-only", false, "Only fill missing release dates of imported ASMR18 works")
	workers := fs.Int("workers", 4, "Concurrent metadata workers (1..256)")
	rps := fs.Float64("rps", 2, "Global maximum HTTP requests per second across all workers and sources")
	limit := fs.Int("max-works", 0, "Maximum works to process (0 = unlimited)")
	reportPath := fs.String("report", "", "JSONL progress report (default: timestamped file under data/cache/metadata)")
	backup := fs.Bool("backup", true, "Save a consistent SQLite backup before updating metadata")
	if err := fs.Parse(args); err != nil {
		return err
	}
	opts := scrape.MetadataOptions{
		Worknos: fs.Args(), MaxWorks: *limit, Workers: *workers,
		RequestsPerSec: *rps, Refresh: *all, DateOnly: *dateOnly,
	}
	if *workers < 1 || *rps <= 0 {
		return fmt.Errorf("--workers and --rps must be positive")
	}
	if err := opts.Validate(); err != nil {
		return err
	}
	cfg, err := config.Load(*dataDir)
	if err != nil {
		return err
	}
	if *proxy != "" {
		cfg.ScraperProxy = *proxy
	}
	if _, err := os.Stat(cfg.DBPath()); err != nil {
		return fmt.Errorf("open existing library: %w", err)
	}
	var index *search.Engine
	// Release dates are read directly from SQLite and are not search-index fields.
	// Date-only updates can run alongside the server without opening its index.
	if !*dateOnly {
		index, err = search.OpenWithLockTimeout(cfg.BlevePath(), metadataIndexLockTimeout)
		if err != nil {
			return fmt.Errorf("could not open the search index (%w). If this data directory is in use, stop the running Otoha server before enriching it", err)
		}
		defer index.Close()
	}
	store, err := db.Open(cfg.DBPath())
	if err != nil {
		return err
	}
	defer store.Close()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	runDir := filepath.Join(cfg.DataDir, "cache", "metadata")
	stamp := time.Now().UTC().Format("20060102T150405.000000000Z")
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		return err
	}
	if *backup {
		backupPath := filepath.Join(runDir, "before-"+stamp+".db")
		if _, err := store.DB().ExecContext(ctx, "VACUUM INTO ?", backupPath); err != nil {
			return fmt.Errorf("backup library: %w", err)
		}
		fmt.Printf("Database backup: %s\n", backupPath)
	}
	if *reportPath == "" {
		*reportPath = filepath.Join(runDir, "enrich-"+stamp+".jsonl")
	}
	if err := os.MkdirAll(filepath.Dir(*reportPath), 0o755); err != nil {
		return err
	}
	report, err := os.OpenFile(*reportPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("create metadata report: %w", err)
	}
	defer report.Close()
	encoder := json.NewEncoder(report)
	var reportErr error
	started := time.Now()
	fmt.Printf("ASMR18 metadata: date_only=%v workers=%d global_rps=%g all=%v report=%s\n", *dateOnly, *workers, *rps, *all, *reportPath)
	p, syncErr := scrape.SyncASMR18Metadata(ctx, store, index, cfg, opts, func(p scrape.MetadataProgress) {
		if reportErr == nil {
			reportErr = encoder.Encode(p)
		}
		fmt.Printf("[metadata] %s %d/%d enriched=%d dates=%d not_found=%d failed=%d unresolved=%d %.2f works/s %s\n",
			p.Workno, p.Processed, p.Total, p.Enriched, p.DatesFilled, p.NotFound, p.Failures, p.Unresolved,
			float64(p.Processed)/time.Since(started).Seconds(), p.Message)
	})
	if err := report.Sync(); reportErr == nil {
		reportErr = err
	}
	fmt.Printf("Metadata run finished: processed=%d/%d dates_filled=%d not_found=%d failures=%d unresolved=%d pending=%d elapsed=%s\n",
		p.Processed, p.Total, p.DatesFilled, p.NotFound, p.Failures, p.Unresolved, p.Pending, time.Since(started).Round(time.Millisecond))
	if syncErr != nil || reportErr != nil {
		return errors.Join(syncErr, reportErr)
	}
	if p.Failures > 0 {
		return fmt.Errorf("metadata run had %d failures; completed updates were saved; see %s", p.Failures, *reportPath)
	}
	return nil
}
