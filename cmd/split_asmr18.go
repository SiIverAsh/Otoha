package cmd

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/SiIverAsh/otoha/internal/config"
	"github.com/SiIverAsh/otoha/internal/db"
	"github.com/SiIverAsh/otoha/internal/scrape"
)

// runSplitASMR18 checks single-stream works and expands verified track ranges.
func runSplitASMR18(args []string) error {
	fs := flag.NewFlagSet("split-asmr18", flag.ContinueOnError)
	dataDir := fs.String("data", "data", "Data directory containing the ASMR18 library")
	proxy := fs.String("proxy", "", "HTTP/SOCKS5 proxy override")
	chaptersOnly := fs.Bool("chapters-only", false, "Split existing ASMR18 chapters without network requests")
	dryRun := fs.Bool("dry-run", false, "Report track ranges without saving them")
	limit := fs.Int("max-works", 0, "Maximum works to check (0 = all)")
	if err := fs.Parse(args); err != nil {
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
	store, err := db.Open(cfg.DBPath())
	if err != nil {
		return err
	}
	defer store.Close()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	opts := scrape.ASMR18SplitOptions{
		Worknos: fs.Args(), MaxWorks: *limit, ChaptersOnly: *chaptersOnly, DryRun: *dryRun,
	}
	var checked, split, tracks, unresolved, failed int
	showTracks := len(opts.Worknos) > 0 || (opts.MaxWorks > 0 && opts.MaxWorks <= 20)
	err = scrape.SplitASMR18Tracks(ctx, store, cfg, opts, func(result scrape.ASMR18SplitResult) {
		checked++
		if result.Err != nil {
			failed++
			fmt.Fprintf(os.Stderr, "[%s] %s: %v\n", result.Source, result.Workno, result.Err)
			return
		}
		if len(result.Segments) == 0 {
			unresolved++
			if showTracks {
				fmt.Printf("[%s] %s: %s\n", result.Source, result.Workno, result.Message)
			}
			return
		}
		split++
		tracks += len(result.Segments)
		if showTracks {
			fmt.Printf("[%s] %s\n", result.Source, result.Workno)
			for i, segment := range result.Segments {
				end := "end"
				if segment.End != nil {
					end = fmt.Sprintf("%.2fs", *segment.End)
				}
				fmt.Printf("  %02d  %.2fs–%s  %s\n", i+1, segment.Start, end, segment.Title)
			}
		}
	})
	fmt.Printf("ASMR18 splits: checked=%d split=%d tracks=%d unresolved=%d failed=%d dry_run=%t\n",
		checked, split, tracks, unresolved, failed, *dryRun)
	return err
}
