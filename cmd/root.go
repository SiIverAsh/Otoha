package cmd

import (
	"context"
	"flag"
	"fmt"
	"strings"

	"github.com/SiIverAsh/otoha/internal/api"
	"github.com/SiIverAsh/otoha/internal/config"
	"github.com/SiIverAsh/otoha/internal/db"
)

// Version is overridden with the release tag via go build -ldflags -X.
var Version = "0.1.0"

func Execute(args []string) error {
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		if len(args) > 0 && (args[0] == "-h" || args[0] == "--help") {
			printUsage()
			return nil
		}
		if len(args) > 0 && (args[0] == "-v" || args[0] == "--version") {
			fmt.Printf("otoha %s\n", Version)
			return nil
		}
		return runServe(args)
	}

	switch args[0] {
	case "serve":
		return runServe(args[1:])
	case "scan":
		return runScan(args[1:])
	case "reindex":
		return runReindex(args[1:])
	case "sync-asmr18":
		return runSyncASMR18(args[1:])
	case "split-asmr18":
		return runSplitASMR18(args[1:])
	case "enrich-metadata":
		return runEnrichMetadata(args[1:])
	case "sync-asmrone":
		return runSyncAsmrOne(args[1:])
	case "import-kikoeru":
		return runImportKikoeru(args[1:])
	case "user":
		return runUser(args[1:])
	case "service":
		return runService(args[1:])
	case "version":
		fmt.Printf("otoha %s\n", Version)
		return nil
	case "help":
		printUsage()
		return nil
	default:
		printUsage()
		return fmt.Errorf("unknown subcommand %q", args[0])
	}
}

func printUsage() {
	fmt.Print(`Otoha (音葉) — Open-source self-hosted streaming server for DLsite voice works

Usage:
  otoha [serve] [--addr 127.0.0.1:8090] [--data ./data]
  otoha scan [--data ./data] [--root <dir>] [--name <alias>] [--scrape] [--force]
  otoha sync-asmr18 [--data ./data] [--proxy URL] [--dry-run] [--category allages] [work-page-urls...]
  otoha split-asmr18 [--data ./data] [--chapters-only] [--dry-run] [--max-works N] [RJ-numbers...]
  otoha enrich-metadata [--data ./data] [--all | --date-only] [--workers 4] [--rps 2] [RJ-numbers...]
  otoha sync-asmrone [--data ./data] [--page-size 100] [--max-pages 0] [--include-tracks]
  otoha reindex [--data ./data]
  otoha import-kikoeru --db <path/to/kikoeru.sqlite3> [--covers <dir>] [--root-map alias=path] [--dry-run] [--report <file>]
  otoha user add --username <name> --password <pwd> [--admin] [--data ./data]
  otoha service <install|uninstall|systemd> [--addr 127.0.0.1:8090] [--data ./data]
  otoha version
`)
}

func runUser(args []string) error {
	if len(args) == 0 || args[0] != "add" {
		return fmt.Errorf("usage: otoha user add --username <name> --password <pwd> [--admin] [--data ./data]")
	}
	fs := flag.NewFlagSet("user add", flag.ContinueOnError)
	dataDir := fs.String("data", "data", "Data directory")
	username := fs.String("username", "", "Username")
	password := fs.String("password", "", "Password (>= 4 chars)")
	admin := fs.Bool("admin", true, "Grant administrator role")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if strings.TrimSpace(*username) == "" || *password == "" {
		return fmt.Errorf("--username and --password are required")
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

	hash, err := api.HashPassword(*password)
	if err != nil {
		return err
	}
	u, err := store.UpsertUser(context.Background(), strings.TrimSpace(*username), hash, *admin, false)
	if err != nil {
		return err
	}
	fmt.Printf("User %q (id=%d, admin=%v) saved.\n", u.Username, u.ID, u.IsAdmin)
	return nil
}
