package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/PuerkitoBio/goquery"

	"github.com/SiIverAsh/otoha/internal/config"
	"github.com/SiIverAsh/otoha/internal/db"
	"github.com/SiIverAsh/otoha/internal/dlsite"
)

func TestWorkDrawerRendersTrackTreeAndPlaybackData(t *testing.T) {
	cfg := config.Default(t.TempDir())
	cfg.AsmrOneEnabled = false
	store, err := db.Open(cfg.DBPath())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	})

	const workno = "RJ01234567"
	const title = `音葉 "A" & <B>`
	ctx := t.Context()
	if err := store.SaveMetadata(ctx, &dlsite.Metadata{
		Workno: workno, Title: title, AgeCategory: dlsite.AgeGeneral,
		LanguageEditionsJSON: `[{"workno":"RJ01234567","lang":"JPN","label":"日本語"}]`,
	}, "", false); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveRemoteTracks(ctx, workno, []db.RemoteTrackInput{{
		RelPath:     "Collection/Scene.m4a",
		Duration:    90,
		StreamURL:   "https://media.example/scene.m3u8?quality=high",
		SubtitleURL: "https://media.example/scene.lrc",
	}}, `[{"rel_path":"Notes.txt","type":"text","download_url":"https://media.example/notes.txt"}]`); err != nil {
		t.Fatal(err)
	}
	handler := API{Store: store, Cfg: cfg}

	jsonResponse := httptest.NewRecorder()
	jsonRequest := httptest.NewRequest(http.MethodGet, "/api/works/"+workno, nil)
	jsonRequest.SetPathValue("workno", workno)
	handler.GetWork(jsonResponse, jsonRequest)
	var apiDetail db.WorkDetail
	if err := json.Unmarshal(jsonResponse.Body.Bytes(), &apiDetail); err != nil {
		t.Fatal(err)
	}

	for _, testCase := range []struct {
		name    string
		query   string
		editing bool
	}{
		{name: "view"},
		{name: "edit", query: "?edit=1", editing: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/ui/works/"+workno+testCase.query, nil)
			req.SetPathValue("workno", workno)
			response := httptest.NewRecorder()
			handler.WorkDrawer(response, req)

			if response.Code != http.StatusOK {
				t.Fatalf("drawer status %d, body %q", response.Code, response.Body)
			}
			if response.Header().Get("Content-Type") != "text/html; charset=utf-8" || response.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("drawer headers = %v", response.Header())
			}
			document, err := goquery.NewDocumentFromReader(response.Body)
			if err != nil {
				t.Fatal(err)
			}
			drawer := document.Find("aside.drawer")
			payload, ok := drawer.Attr("data-detail")
			if !ok {
				t.Fatal("drawer playback payload is missing")
			}
			var detail db.WorkDetail
			if err := json.Unmarshal([]byte(payload), &detail); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(detail, apiDetail) {
				t.Fatal("drawer and API returned different work details")
			}
			titleNode := drawer.Find(".drawer-title")
			if titleNode.Text() != title || titleNode.Children().Length() != 0 {
				t.Fatalf("incorrectly escaped title %q", titleNode.Text())
			}
			if titleInput := drawer.Find("#meta-title"); testCase.editing != (titleInput.Length() > 0) {
				t.Fatal("metadata editor visibility did not match requested mode")
			}
			folder := drawer.Find(".tree-folder")
			if folder.Find(".badge-format").Text() != "HLS" || folder.Find(".tree-track-meta .badge").Text() != "字幕" {
				t.Fatal("missing HLS or subtitle badge")
			}
			play := folder.Find("[data-action=play-track]")
			if path, _ := play.Attr("data-folder-rel"); path != "Collection" {
				t.Fatalf("playback folder scope = %q", path)
			}
			if drawer.Find(".drawer-edition-language").Text() != "日本語" ||
				drawer.Find("a[href='https://media.example/notes.txt']").Length() != 1 {
				t.Fatal("missing edition or attachment")
			}
		})
	}

	req := httptest.NewRequest(http.MethodGet, "/ui/works/RJ000001", nil)
	req.SetPathValue("workno", "RJ000001")
	response := httptest.NewRecorder()
	handler.WorkDrawer(response, req)
	if response.Code != http.StatusNotFound {
		t.Fatalf("missing work drawer status = %d", response.Code)
	}
}
