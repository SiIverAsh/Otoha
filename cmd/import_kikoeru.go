package cmd

import (
	"context"
	"flag"
	"fmt"
	"strings"

	"github.com/SiIverAsh/otoha/internal/config"
	"github.com/SiIverAsh/otoha/internal/db"
	"github.com/SiIverAsh/otoha/internal/kikoeru"
	"github.com/SiIverAsh/otoha/internal/search"
)

type kvMapFlag map[string]string

func (m *kvMapFlag) String() string {
	return fmt.Sprintf("%v", *m)
}

func (m *kvMapFlag) Set(val string) error {
	idx := strings.IndexByte(val, '=')
	if idx <= 0 {
		return fmt.Errorf("expected alias=path, got %q", val)
	}
	if *m == nil {
		*m = make(map[string]string)
	}
	(*m)[strings.TrimSpace(val[:idx])] = strings.TrimSpace(val[idx+1:])
	return nil
}

func runImportKikoeru(args []string) error {
	fs := flag.NewFlagSet("import-kikoeru", flag.ContinueOnError)
	srcDB := fs.String("db", "", "Path to kikoeru sqlite3 file (opened strictly read-only)")
	coversDir := fs.String("covers", "", "Optional kikoeru coverFolderDir to reuse local covers")
	dryRun := fs.Bool("dry-run", false, "Generate full conflict & migration report without writing to Otoha DB")
	reportFile := fs.String("report", "", "Output JSON report file path (default: kikoeru-import-report-<ts>.json)")
	dataDir := fs.String("data", "data", "Otoha data directory")
	var rootMap kvMapFlag
	fs.Var(&rootMap, "root-map", "Map kikoeru root_folder alias to local path (repeatable: --root-map Voices=D:\\VOICES)")

	if err := fs.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*srcDB) == "" {
		return fmt.Errorf("--db <path/to/kikoeru.sqlite3> is required")
	}

	cfg, err := config.Load(*dataDir)
	if err != nil {
		return err
	}

	var dstStore *db.Store
	if !*dryRun {
		dstStore, err = db.Open(cfg.DBPath())
		if err != nil {
			return err
		}
		defer dstStore.Close()
	}

	rep, err := kikoeru.RunImport(context.Background(), dstStore, kikoeru.ImportOptions{
		SourceDBPath:   *srcDB,
		CoversDir:      *coversDir,
		DestCoversDir:  cfg.CoversDir(),
		RootMap:        rootMap,
		DryRun:         *dryRun,
		ReportFilePath: *reportFile,
	})
	if err != nil {
		return err
	}

	if !*dryRun && dstStore != nil {
		eng := search.Open(cfg.BlevePath())
		_, _ = eng.ReindexFromStore(context.Background(), dstStore)
		_ = eng.Close()
	}

	mode := "COMMIT"
	if rep.DryRun {
		mode = "DRY-RUN (read-only)"
	}
	fmt.Printf("=== Kikoeru Import Summary [%s] ===\n", mode)
	fmt.Printf("Source DB           : %s\n", rep.SourceDB)
	fmt.Printf("Knex Migration      : %s\n", rep.KnexLatestMigration)
	fmt.Printf("Source Works        : %d\n", rep.SourceWorksTotal)
	fmt.Printf("Unique Works        : %d\n", rep.UniqueWorksImported)
	fmt.Printf("8-digit / ID Conflicts: %d\n", rep.ConflictsDetected)
	fmt.Printf("Duplicates Merged   : %d\n", rep.DuplicateCollapsed)
	fmt.Printf("Marked for Rescrape : %d\n", rep.NeedsRescrapeCount)
	fmt.Printf("Users Imported      : %d (password reset required on first login)\n", rep.UsersImported)
	fmt.Printf("Reviews Imported    : %d\n", rep.ReviewsImported)
	fmt.Printf("Azuse History Rows  : %d (table present=%v)\n", rep.HistoryImported, rep.AzuseHistoryFound)
	fmt.Printf("Report Saved To     : %s\n", rep.ReportPath)
	for _, w := range rep.Warnings {
		fmt.Printf("Warning             : %s\n", w)
	}
	return nil
}
