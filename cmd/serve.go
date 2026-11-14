package cmd

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/SiIverAsh/otoha/internal/api"
	"github.com/SiIverAsh/otoha/internal/config"
	"github.com/SiIverAsh/otoha/internal/db"
	"github.com/SiIverAsh/otoha/internal/history"
	"github.com/SiIverAsh/otoha/internal/search"
	"github.com/SiIverAsh/otoha/web"
)

func runServe(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	dataDir := fs.String("data", "data", "Data directory containing otoha.db, index.bleve, and cache")
	addr := fs.String("addr", "", "HTTP listen address (default from config or 127.0.0.1:8090)")
	authFlag := fs.Bool("auth", false, "Force enable user authentication")
	if err := fs.Parse(args); err != nil {
		return err
	}

	cfg, err := config.Load(*dataDir)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	if *addr != "" {
		cfg.ListenAddr = *addr
	}
	if *authFlag {
		cfg.AuthEnabled = true
	}

	logger := log.New(os.Stdout, "[otoha] ", log.LstdFlags)

	store, err := db.Open(cfg.DBPath())
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer store.Close()

	searchEng := search.Open(cfg.BlevePath())
	defer searchEng.Close()
	if deg, warn := searchEng.IsDegraded(); deg {
		logger.Printf("WARNING: %s", warn)
	}

	distFS, err := web.DistFS()
	if err != nil {
		return fmt.Errorf("load embedded frontend: %w", err)
	}

	histSvc := history.NewService(store, cfg)
	taskMgr := api.NewTaskManager(store, searchEng, cfg, logger)

	router := api.NewRouter(api.Dependencies{
		Store:    store,
		Search:   searchEng,
		History:  histSvc,
		Cfg:      cfg,
		Tasks:    taskMgr,
		Logger:   logger,
		Frontend: distFS,
	})

	srv := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           router,
		ReadHeaderTimeout: 15 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	errCh := make(chan error, 1)
	go func() {
		logger.Printf("listening on http://%s (data=%s, auth=%v, webdav=/dav/)", cfg.ListenAddr, cfg.DataDir, cfg.AuthEnabled)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
		close(errCh)
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		logger.Printf("shutting down gracefully...")
		taskMgr.Cancel()
		shutCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return srv.Shutdown(shutCtx)
	}
}
