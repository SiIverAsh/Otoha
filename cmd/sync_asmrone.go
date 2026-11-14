package cmd

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/SiIverAsh/otoha/internal/config"
	"github.com/SiIverAsh/otoha/internal/db"
	"github.com/SiIverAsh/otoha/internal/scrape"
	"github.com/SiIverAsh/otoha/internal/search"
)

func runSyncAsmrOne(args []string) error {
	fs := flag.NewFlagSet("sync-asmrone", flag.ContinueOnError)
	dataDir := fs.String("data", "data", "Data directory")
	pageSize := fs.Int("page-size", 100, "Page size (1..100)")
	startPage := fs.Int("start-page", 0, "Explicit starting page (0 = auto-resume from checkpoint)")
	maxPages := fs.Int("max-pages", 0, "Maximum pages to fetch in this run (0 = until end)")
	order := fs.String("order", "id", "Sort field (id | create_date | release | dl_count | rate_average_2dp)")
	sortDir := fs.String("sort", "asc", "Sort direction (asc | desc; asc keeps page numbers stable for resume)")
	subtitleOnly := fs.Bool("subtitle-only", false, "Only sync works marked with subtitles")
	includeTracks := fs.Bool("include-tracks", false, "Also fetch /api/tracks/{id} remote track tree for each work")
	resetCP := fs.Bool("reset-checkpoint", false, "Ignore saved checkpoint and start fresh")
	proxyURL := fs.String("proxy", "", "Optional HTTP/SOCKS5 proxy override")
	locale := fs.String("locale", "", "Tag i18n locale (zh_cn | ja_jp | en_us)")
	interval := fs.Float64("interval", 0, "Override request interval in seconds (default from config: 0.5s)")

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
	if *interval > 0 {
		cfg.AsmrOneIntervalSec = *interval
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

	cp, err := scrape.SyncAsmrOneCatalog(ctx, store, searchEng, cfg, scrape.SyncOptions{
		PageSize:        *pageSize,
		StartPage:       *startPage,
		MaxPages:        *maxPages,
		Order:           *order,
		Sort:            *sortDir,
		SubtitleOnly:    *subtitleOnly,
		IncludeTracks:   *includeTracks,
		ResetCheckpoint: *resetCP,
	}, func(p scrape.AsmrOneSyncCheckpoint) {
		fmt.Printf(
			"[sync-asmrone] page %d/%d | new=%d dup=%d fail=%d tracks=%d | completeness(circle=%.1f%% va=%.1f%% tags=%.1f%% cover=%.1f%%) | %.1f works/s ETA=%ds\n",
			p.CurrentPage, p.TotalPages, p.Inserted, p.Duplicates, p.Failures, p.TracksSynced,
			p.Completeness.CircleRatePct, p.Completeness.VARatePct, p.Completeness.TagsRatePct, p.Completeness.CoverRatePct,
			p.WorksPerSec, p.ETASeconds,
		)
	})
	if errors.Is(err, context.Canceled) {
		fmt.Printf("[sync-asmrone] interrupted at page %d; checkpoint saved (next_page=%d) for exact resume.\n", cp.CurrentPage, cp.NextPage)
		return nil
	}
	if err != nil {
		return err
	}
	fmt.Printf(
		"[sync-asmrone] done: processed=%d (new=%d, duplicates=%d, failures=%d, tracks=%d), next_page=%d.\n",
		cp.Processed, cp.Inserted, cp.Duplicates, cp.Failures, cp.TracksSynced, cp.NextPage,
	)
	return nil
}
