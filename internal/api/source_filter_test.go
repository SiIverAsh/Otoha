package api

import (
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/SiIverAsh/otoha/internal/config"
	"github.com/SiIverAsh/otoha/internal/db"
	"github.com/SiIverAsh/otoha/internal/dlsite"
	"github.com/SiIverAsh/otoha/internal/search"
)

func TestWorkSourceFiltersListSearchAndFacets(t *testing.T) {
	ctx := t.Context()
	cfg := config.Default(t.TempDir())
	store, err := db.Open(cfg.DBPath())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	index := search.Open(cfg.BlevePath())
	defer index.Close()

	asmrOne := &dlsite.Metadata{Workno: "RJ100001", AsmrOneID: 100001, Title: "Moon", CircleName: "One", AgeCategory: dlsite.AgeGeneral}
	asmr18 := &dlsite.Metadata{Workno: "RJ100002", Title: "Moon", CircleName: "Eighteen", AgeCategory: dlsite.AgeGeneral}
	dual := &dlsite.Metadata{Workno: "RJ100003", AsmrOneID: 100003, Title: "Moon", CircleName: "Both", AgeCategory: dlsite.AgeGeneral}
	if _, _, err := store.SaveMetadataBatch(ctx, []*dlsite.Metadata{asmrOne, dual}); err != nil {
		t.Fatal(err)
	}
	tracks := []db.RemoteTrackInput{{RelPath: "audio.m3u8", StreamURL: "https://example.test/audio.m3u8"}}
	for _, metadata := range []*dlsite.Metadata{asmr18, dual} {
		if _, err := store.SaveASMR18Work(ctx, metadata, "https://asmr18.fans/boys/"+metadata.Workno, tracks); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.SaveMetadata(ctx, &dlsite.Metadata{
		Workno: "RJ100004", Title: "Moon", CircleName: "Other", AgeCategory: dlsite.AgeGeneral,
	}, "", true); err != nil {
		t.Fatal(err)
	}
	for _, workno := range []string{"RJ100001", "RJ100002", "RJ100003", "RJ100004"} {
		doc, err := store.GetSearchDocument(ctx, workno)
		if err != nil {
			t.Fatal(err)
		}
		if err := index.IndexWork(*doc); err != nil {
			t.Fatal(err)
		}
	}

	router := NewRouter(Dependencies{Store: store, Search: index, Cfg: cfg, Logger: log.New(io.Discard, "", 0)})
	checkResults := func(t *testing.T, path string, expected []string, total int) {
		t.Helper()
		res := httptest.NewRecorder()
		router.ServeHTTP(res, httptest.NewRequest(http.MethodGet, path, nil))
		if res.Code != http.StatusOK {
			t.Fatalf("GET %s: status=%d body=%s", path, res.Code, res.Body)
		}
		var results struct {
			Items []db.Work `json:"items"`
			Total int       `json:"total"`
		}
		if err := json.Unmarshal(res.Body.Bytes(), &results); err != nil {
			t.Fatal(err)
		}
		worknos := make([]string, 0, len(results.Items))
		for _, work := range results.Items {
			worknos = append(worknos, work.Workno)
		}
		slices.Sort(worknos)
		if !slices.Equal(worknos, expected) || results.Total != total {
			t.Fatalf("GET %s: worknos=%v total=%d, want worknos=%v total=%d", path, worknos, results.Total, expected, total)
		}
	}

	for _, path := range []string{"/api/works?age=all", "/api/search?age=all&q=Moon"} {
		checkResults(t, path, []string{"RJ100001", "RJ100002", "RJ100003", "RJ100004"}, 4)
		checkResults(t, path+"&source=all", []string{"RJ100001", "RJ100002", "RJ100003", "RJ100004"}, 4)
		checkResults(t, path+"&source=asmrone", []string{"RJ100001", "RJ100003"}, 2)
		checkResults(t, path+"&source=asmr18", []string{"RJ100002", "RJ100003"}, 2)
		checkResults(t, path+"&source=asmr18&page_size=1&page=2", []string{"RJ100002"}, 2)
		checkResults(t, path+"&source=asmr18&circle=Both", []string{"RJ100003"}, 1)
		checkResults(t, path+"&source=asmrone&circle=Eighteen", []string{}, 0)
	}
	checkResults(t, "/api/search?age=all&q=Moon+-Moon&source=asmrone", []string{}, 0)

	res := httptest.NewRecorder()
	router.ServeHTTP(res, httptest.NewRequest(http.MethodGet, "/api/works/facets?age=all&source=asmr18", nil))
	if res.Code != http.StatusOK {
		t.Fatalf("GET facets: status=%d body=%s", res.Code, res.Body)
	}
	var facets struct {
		Circles []db.FacetCount `json:"circles"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &facets); err != nil {
		t.Fatal(err)
	}
	if len(facets.Circles) != 2 || facets.Circles[0].Name != "Both" || facets.Circles[1].Name != "Eighteen" {
		t.Fatalf("expected source-filtered circles, got %v", facets.Circles)
	}

	for _, path := range []string{"/api/works?", "/api/search?q=Moon&", "/api/works/facets?"} {
		res := httptest.NewRecorder()
		router.ServeHTTP(res, httptest.NewRequest(http.MethodGet, path+"source=unknown", nil))
		if res.Code != http.StatusBadRequest {
			t.Fatalf("GET %s with unknown source: status=%d body=%s", path, res.Code, res.Body)
		}
	}

	index.ForceDegradedForTest("test degraded search")
	checkResults(t, "/api/search?age=all&q=Moon&source=asmrone", []string{"RJ100001", "RJ100003"}, 2)
	checkResults(t, "/api/search?age=all&q=Moon&source=asmr18", []string{"RJ100002", "RJ100003"}, 2)
}
