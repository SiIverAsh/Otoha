package cmd

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SiIverAsh/otoha/internal/config"
	"github.com/SiIverAsh/otoha/internal/db"
	"github.com/SiIverAsh/otoha/internal/dlsite"
	"github.com/SiIverAsh/otoha/internal/search"
)

func TestEnrichMetadataCommandPreservesAudioAndKnownFields(t *testing.T) {
	var productRequests atomic.Int32
	var pageRequests atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/maniax/api/=/product.json":
			productRequests.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[{"workno":"RJ01760149","work_name":"Official title","maker_id":"RG60273","maker_name":"Official circle","regist_date":"2026-11-08 00:00:00","age_category":3,"work_type":"SOU","price":1650,"contents_file_size":3208985788,"rate_average_2dp":4.8,"rate_count":20,"dl_count":30,"creaters":{"voice_by":[{"name":"Voice"}]},"genres":[{"name":"Tag"}]}]`))
		case "/maniax/work/=/product_id/RJ01760149.html":
			// Without a cover in product.json, the client checks the work page.
			pageRequests.Add(1)
			http.NotFound(w, r)
		default:
			t.Errorf("metadata-only command requested unexpected resource %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()
	cfg := config.Default(t.TempDir())
	cfg.DLsiteBaseURL = upstream.URL
	cfg.AsmrOneEnabled = false
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	store, err := db.Open(cfg.DBPath())
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.SaveASMR18Work(ctx, &dlsite.Metadata{Workno: "RJ01760149", Title: "Keep existing title", AgeCategory: 3},
		"https://asmr18.fans/boys/rj01760149/", []db.RemoteTrackInput{{Title: "Original track", RelPath: "ASMR18/file1.m3u8", StreamURL: "https://cdn3.cloudintech.net/file/RJ01760149/main.m3u8"}})
	if err != nil {
		t.Fatal(err)
	}
	before, err := store.GetWorkDetail(ctx, "RJ01760149", 1)
	if err != nil {
		t.Fatal(err)
	}
	beforeTracks, _ := json.Marshal(before.Tracks)
	store.Close()
	report := filepath.Join(cfg.DataDir, "report.jsonl")
	if err := runEnrichMetadata([]string{"--data", cfg.DataDir, "--all", "--workers", "128", "--rps", "100", "--report", report}); err != nil {
		t.Fatal(err)
	}
	store, err = db.Open(cfg.DBPath())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	after, err := store.GetWorkDetail(ctx, "RJ01760149", 1)
	if err != nil {
		t.Fatal(err)
	}
	afterTracks, _ := json.Marshal(after.Tracks)
	if string(beforeTracks) != string(afterTracks) || after.Title != before.Title || after.CreatedAt != before.CreatedAt {
		t.Fatalf("known metadata or audio changed: before=%+v after=%+v", before, after)
	}
	if after.ReleaseDate != "2026-11-08" || after.CircleID != "RG60273" || after.FileSize != 3208985788 || after.Price != 1650 || len(after.VAs) != 1 || len(after.Tags) != 1 {
		t.Fatalf("metadata gaps were not filled: %+v", after)
	}
	backups, _ := filepath.Glob(filepath.Join(cfg.DataDir, "cache", "metadata", "before-*.db"))
	if len(backups) != 1 || productRequests.Load() != 1 || pageRequests.Load() != 1 {
		t.Fatalf("expected one backup and one request per metadata endpoint, backups=%v product=%d page=%d", backups, productRequests.Load(), pageRequests.Load())
	}
	contents, err := os.ReadFile(report)
	if err != nil || !strings.Contains(string(contents), `"dates_filled":1`) {
		t.Fatalf("missing progress report: %s %v", contents, err)
	}
}

func TestEnrichMetadataCommandRefusesLockedSearchIndex(t *testing.T) {
	cfg := config.Default(t.TempDir())
	store, err := db.Open(cfg.DBPath())
	if err != nil {
		t.Fatal(err)
	}
	store.Close()
	index := search.Open(cfg.BlevePath())
	defer index.Close()
	if degraded, warning := index.IsDegraded(); degraded {
		t.Fatalf("could not hold the search-index lock: %s", warning)
	}
	// Release the lock if a regression would otherwise hang the test process.
	releaseLock := time.AfterFunc(5*time.Second, func() { _ = index.Close() })
	err = runEnrichMetadata([]string{"--data", cfg.DataDir, "--all", "--rps", "100"})
	if !releaseLock.Stop() {
		t.Fatal("metadata command waited for the index lock to be released")
	}
	if err == nil || !strings.Contains(err.Error(), "stop the running Otoha server") {
		t.Fatalf("expected a clear refusal for a running server, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(cfg.DataDir, "cache", "metadata")); !os.IsNotExist(err) {
		t.Fatalf("expected no backup or report while the index is locked, got %v", err)
	}
}
