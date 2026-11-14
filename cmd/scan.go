package cmd

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/SiIverAsh/otoha/internal/config"
	"github.com/SiIverAsh/otoha/internal/db"
	"github.com/SiIverAsh/otoha/internal/dlsite"
	"github.com/SiIverAsh/otoha/internal/scanner"
	"github.com/SiIverAsh/otoha/internal/scrape"
	"github.com/SiIverAsh/otoha/internal/search"
)

func runScan(args []string) error {
	fs := flag.NewFlagSet("scan", flag.ContinueOnError)
	dataDir := fs.String("data", "data", "Data directory")
	rootDir := fs.String("root", "", "Optional local directory to add/scan immediately")
	storageName := fs.String("name", "", "Optional storage alias when --root is given")
	doScrape := fs.Bool("scrape", false, "Also fetch missing DLsite metadata (serially with rate limit & disk cache)")
	force := fs.Bool("force", false, "Force re-scrape even if cached")
	proxyURL := fs.String("proxy", "", "HTTP/SOCKS5 proxy URL (supports http://user:pass@host:port or socks5://user:pass@host:port)")
	locale := fs.String("locale", "", "DLsite metadata locale (zh_cn|ja_jp|zh_tw|en_us|ko_kr)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	cfg, err := config.Load(*dataDir)
	if err != nil {
		return err
	}
	if *proxyURL != "" {
		cfg.ScraperProxy = *proxyURL
	}
	if *locale != "" {
		cfg.ScraperLocale = *locale
	}

	store, err := db.Open(cfg.DBPath())
	if err != nil {
		return err
	}
	defer store.Close()

	searchEng := search.Open(cfg.BlevePath())
	defer searchEng.Close()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	logger := log.New(os.Stdout, "", log.LstdFlags)

	var targets []db.Storage
	if *rootDir != "" {
		abs, err := filepath.Abs(*rootDir)
		if err != nil {
			return err
		}
		alias := *storageName
		if alias == "" {
			alias = filepath.Base(abs)
		}
		st, err := store.UpsertStorage(ctx, "local", alias, abs, "")
		if err != nil {
			return err
		}
		targets = []db.Storage{*st}
	} else {
		targets, err = store.ListStorages(ctx)
		if err != nil {
			return err
		}
	}

	if len(targets) == 0 {
		return fmt.Errorf("no storages configured; pass --root <dir> or add a storage in Web UI")
	}

	totalWorks := 0
	totalTracks := 0
	totalSkipped := 0

	for _, st := range targets {
		if st.Kind != "local" {
			continue
		}
		logger.Printf("[scan] scanning storage %q (%s)...", st.Name, st.Root)
		stats, err := scanner.ScanRoot(ctx, st.Root, scanner.Options{
			MaxDepth:       cfg.ScanMaxDepth,
			IgnorePatterns: cfg.ScanIgnorePatterns,
			Logger:         logger,
		}, func(w scanner.DiscoveredWork) error {
			if err := store.UpsertScannedWork(ctx, st.ID, w.Workno, w.FallbackTitle, w.RelPath, w.AgeHint, w.LocalCover, w.Tracks); err != nil {
				return err
			}
			if doc, dErr := store.GetSearchDocument(ctx, w.Workno); dErr == nil {
				_ = searchEng.IndexWork(*doc)
			}
			return nil
		})
		if stats != nil {
			totalWorks += stats.WorksFound
			totalTracks += stats.TracksFound
			totalSkipped += len(stats.Skipped)
		}
		if errors.Is(err, context.Canceled) {
			logger.Printf("[scan] interrupted after %d works.", totalWorks)
			return nil
		}
		if err != nil {
			return err
		}
	}

	logger.Printf("[scan] completed: %d work folders, %d tracks, %d skipped entries.", totalWorks, totalTracks, totalSkipped)

	if *doScrape {
		worknos, err := store.ListUnscrapedWorknos(ctx, *force)
		if err != nil {
			return err
		}
		if len(worknos) == 0 {
			logger.Printf("[scrape] all works already scraped; nothing to fetch.")
			return nil
		}
		cache, err := dlsite.NewCache(cfg.CacheDir())
		if err != nil {
			return err
		}
		client, err := scrape.NewClient(cfg, cache, 0)
		if err != nil {
			return err
		}
		logger.Printf("[scrape] fetching metadata for %d works (interval=%.1fs, resumable)...", len(worknos), cfg.ScraperIntervalSec)
		_, err = client.ScrapeBatch(ctx, worknos, *force, func(p dlsite.BatchProgress) {
			if p.Meta != nil && !p.Meta.NotFound {
				coverPath, _ := client.DownloadCover(ctx, p.Workno, p.Meta.CoverURL)
				_ = store.SaveMetadata(ctx, p.Meta, coverPath, true)
				if doc, dErr := store.GetSearchDocument(ctx, p.Workno); dErr == nil {
					_ = searchEng.IndexWork(*doc)
				}
			}
			logger.Printf("[scrape] (%d/%d) %s -> %s", p.Current, p.Total, p.Workno, p.Status)
		})
		if errors.Is(err, context.Canceled) {
			logger.Printf("[scrape] interrupted; checkpoint saved for resume.")
			return nil
		}
		return err
	}

	return nil
}
