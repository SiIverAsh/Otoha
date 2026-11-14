package cmd

import (
	"context"
	"flag"
	"fmt"
	"time"

	"github.com/SiIverAsh/otoha/internal/config"
	"github.com/SiIverAsh/otoha/internal/db"
	"github.com/SiIverAsh/otoha/internal/search"
)

func runReindex(args []string) error {
	fs := flag.NewFlagSet("reindex", flag.ContinueOnError)
	dataDir := fs.String("data", "data", "Data directory")
	if err := fs.Parse(args); err != nil {
		return err
	}

	cfg, err := config.Load(*dataDir)
	if err != nil {
		return err
	}
	store, err := db.Open(cfg.DBPath())
	if err != nil {
		return err
	}
	defer store.Close()

	eng := search.Open(cfg.BlevePath())
	defer eng.Close()

	start := time.Now()
	count, err := eng.ReindexFromStore(context.Background(), store)
	if err != nil {
		return err
	}
	fmt.Printf("Reindexed %d works into %s in %s.\n", count, cfg.BlevePath(), time.Since(start).Round(time.Millisecond))
	return nil
}
