package search

import (
	"context"
	"path/filepath"
	"slices"
	"testing"

	"github.com/SiIverAsh/otoha/internal/db"
	"github.com/SiIverAsh/otoha/internal/dlsite"
)

func TestBleveFuzzyKeywordSearch(t *testing.T) {
	indexPath := filepath.Join(t.TempDir(), "index.bleve")
	index := Open(indexPath)
	works := []db.SearchDocument{
		{Workno: "RJ123456", Title: "Moonlight 耳かき", VAs: []string{"Seraphina"}, Tags: []string{"Whispering"}},
		{Workno: "RJ12345678", Title: "Moonlight 朝", VAs: []string{"Seraphina"}, Tags: []string{"Rain"}},
		{Workno: "RJ123457", Title: "Moon", Tags: []string{"Rain"}},
	}
	if err := index.IndexWorkBatch(works); err != nil {
		t.Fatal(err)
	}
	if err := index.Close(); err != nil {
		t.Fatal(err)
	}

	index = Open(indexPath)
	t.Cleanup(func() {
		if err := index.Close(); err != nil {
			t.Error(err)
		}
	})

	for _, tt := range []struct {
		name    string
		keyword string
		want    []string
	}{
		{name: "partial title", keyword: "NLIGHT", want: []string{"RJ123456", "RJ12345678"}},
		{name: "multiple edits exceed typo tolerance", keyword: "Serafina", want: nil},
		{name: "single substitution", keyword: "Seraphena", want: []string{"RJ123456", "RJ12345678"}},
		{name: "single CJK character", keyword: "か", want: []string{"RJ123456"}},
		{name: "required terms across fields", keyword: "nlight hisper", want: []string{"RJ123456"}},
		{name: "exclusion is literal", keyword: "nlight -Whisper", want: []string{"RJ123456", "RJ12345678"}},
		{name: "matching exclusion removes result", keyword: "nlight -Rain", want: []string{"RJ123456"}},
		{name: "quoted keyword stays literal", keyword: `"nlight"`, want: nil},
		{name: "field query stays literal", keyword: "title:nlight", want: nil},
		{name: "explicit zero fuzziness stays literal", keyword: "nlight~0", want: nil},
		{name: "partial work number", keyword: "rj12345", want: []string{"RJ123456", "RJ12345678", "RJ123457"}},
		{name: "numeric fragment of work number", keyword: "2345", want: []string{"RJ123456", "RJ12345678", "RJ123457"}},
		{name: "full work number stays literal", keyword: "rj123456", want: []string{"RJ123456"}},
		{name: "short Latin keyword stays literal", keyword: "Mo", want: nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			result, err := index.SearchWithinWorks(t.Context(), tt.keyword, 20, nil)
			if err != nil {
				t.Fatal(err)
			}
			if result.Degraded {
				t.Fatalf("index degraded: %s", result.Warning)
			}
			slices.Sort(result.Worknos)
			if !slices.Equal(result.Worknos, tt.want) {
				t.Fatalf("search %q = %v, want %v", tt.keyword, result.Worknos, tt.want)
			}
		})
	}
}

func TestBleveCJKQuerySyntaxAndDegradedFallback(t *testing.T) {
	dir := t.TempDir()
	store, err := db.Open(filepath.Join(dir, "otoha.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	defer store.Close()

	eng := Open(filepath.Join(dir, "index.bleve"))
	defer eng.Close()

	ctx := context.Background()
	docs := []*dlsite.Metadata{
		{
			Workno:      "RJ01000001",
			Title:       "癒しの耳かきサロン 春風編",
			CircleName:  "音葉工房",
			SeriesName:  "四季の癒し",
			AgeCategory: dlsite.AgeGeneral,
			VAs:         []string{"花澤テスト"},
			Tags:        []string{"治愈", "耳かき", "バイノーラル"},
		},
		{
			Workno:      "RJ01000002",
			Title:       "癒しの耳かきサロン 夜空編",
			CircleName:  "月光堂",
			SeriesName:  "四季の癒し",
			AgeCategory: dlsite.AgeGeneral,
			VAs:         []string{"早見テスト"},
			Tags:        []string{"治愈", "安眠"},
		},
		{
			Workno:      "RJ01000003",
			Title:       "雨音と読書カフェ",
			CircleName:  "音葉工房",
			AgeCategory: dlsite.AgeGeneral,
			VAs:         []string{"上田テスト"},
			Tags:        []string{"環境音"},
		},
	}

	for _, m := range docs {
		if err := store.SaveMetadata(ctx, m, "", true); err != nil {
			t.Fatalf("SaveMetadata: %v", err)
		}
		sd, err := store.GetSearchDocument(ctx, m.Workno)
		if err != nil {
			t.Fatalf("GetSearchDocument: %v", err)
		}
		if err := eng.IndexWork(*sd); err != nil {
			t.Fatalf("IndexWork: %v", err)
		}
	}

	// 1. Japanese CJK substring search ("耳かき") -> matches RJ01000001 & RJ01000002
	res, err := eng.Search("耳かき", 20)
	if err != nil || len(res.Worknos) != 2 {
		t.Fatalf("search 耳かき = %+v, err=%v", res, err)
	}

	// 2. Space-separated multi-keyword INTERSECTION ("耳かき 花澤") -> ONLY RJ01000001 (§6)
	resAnd, err := eng.Search("耳かき 花澤", 20)
	if err != nil || len(resAnd.Worknos) != 1 || resAnd.Worknos[0] != "RJ01000001" {
		t.Fatalf("expected space-separated intersection to return ONLY [RJ01000001], got %+v (err=%v)", resAnd, err)
	}

	// 3. Exclusion `-关键词` ("耳かき -早見") -> ONLY RJ01000001 (§6)
	resExcl, err := eng.Search("耳かき -早見", 20)
	if err != nil || len(resExcl.Worknos) != 1 || resExcl.Worknos[0] != "RJ01000001" {
		t.Fatalf("expected exclusion query to return ONLY [RJ01000001], got %+v (err=%v)", resExcl, err)
	}

	// 4. Phrase query `"耳かきサロン 春風"` -> ONLY RJ01000001 (§6)
	resPhrase, err := eng.Search(`"耳かきサロン 春風"`, 20)
	if err != nil || len(resPhrase.Worknos) != 1 || resPhrase.Worknos[0] != "RJ01000001" {
		t.Fatalf("expected phrase query to return ONLY [RJ01000001], got %+v (err=%v)", resPhrase, err)
	}

	// 5. Simulate index corruption -> must degrade without failing and recover via ReindexFromStore (§6)
	eng.ForceDegradedForTest("simulated disk corruption")
	degRes, err := eng.Search("音葉工房", 20)
	if err != nil || !degRes.Degraded || !degRes.NeedsReindex {
		t.Fatalf("expected degraded response, got %+v err=%v", degRes, err)
	}
	fallbackWorks, total, err := store.ListWorks(ctx, db.ListWorksOptions{
		AgeMode:   "general",
		QueryLike: "音葉工房",
	})
	if err != nil || total != 2 || len(fallbackWorks) != 2 {
		t.Fatalf("SQL LIKE fallback failed: total=%d err=%v", total, err)
	}

	// 6. Full reindex restores Bleve from SQLite truth source
	n, err := eng.ReindexFromStore(ctx, store)
	if err != nil || n != 3 {
		t.Fatalf("ReindexFromStore: n=%d err=%v", n, err)
	}
	recRes, err := eng.Search("環境音", 20)
	if err != nil || recRes.Degraded || len(recRes.Worknos) != 1 || recRes.Worknos[0] != "RJ01000003" {
		t.Fatalf("search after reindex failed: %+v err=%v", recRes, err)
	}
}
