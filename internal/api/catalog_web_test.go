package api

import (
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/PuerkitoBio/goquery"
	"github.com/SiIverAsh/otoha/internal/config"
	"github.com/SiIverAsh/otoha/internal/db"
	"github.com/SiIverAsh/otoha/internal/dlsite"
	"github.com/SiIverAsh/otoha/internal/history"
	"github.com/SiIverAsh/otoha/internal/search"
)

func TestCatalogHTMLNavigation(t *testing.T) {
	const title = "Moon <img src=x onerror=alert(1)>"
	const facet = "海岸 & <雨>"
	ctx := t.Context()
	cfg := config.Default(t.TempDir())
	store, err := db.Open(cfg.DBPath())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	})
	index := search.Open(cfg.BlevePath())
	t.Cleanup(func() {
		if err := index.Close(); err != nil {
			t.Error(err)
		}
	})

	for _, metadata := range []*dlsite.Metadata{
		{
			Workno: "RJ100001", Title: title, CircleName: facet,
			VAs: []string{facet}, Tags: []string{facet}, AsmrOneID: 100001, AgeCategory: dlsite.AgeGeneral,
		},
		{Workno: "RJ100002", Title: "Moon Second", AsmrOneID: 100002, AgeCategory: dlsite.AgeGeneral},
		{Workno: "RJ100003", Title: "Moon Excluded", AgeCategory: dlsite.AgeGeneral},
	} {
		if err := store.SaveMetadata(ctx, metadata, "", true); err != nil {
			t.Fatal(err)
		}
		document, err := store.GetSearchDocument(ctx, metadata.Workno)
		if err != nil {
			t.Fatal(err)
		}
		if err := index.IndexWork(*document); err != nil {
			t.Fatal(err)
		}
	}

	router := NewRouter(Dependencies{Store: store, Search: index, Cfg: cfg, Logger: log.New(io.Discard, "", 0)})

	getPage := func(t *testing.T, path string, fragment bool, cookies ...*http.Cookie) (*goquery.Document, string) {
		t.Helper()
		request := httptest.NewRequest(http.MethodGet, path, nil)
		if fragment {
			request.Header.Set("HX-Request", "true")
		}
		for _, cookie := range cookies {
			request.AddCookie(cookie)
		}
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("GET %s: status %d, body %s", path, response.Code, response.Body)
		}
		if contentType := response.Header().Get("Content-Type"); !strings.HasPrefix(contentType, "text/html") {
			t.Fatalf("GET %s: Content-Type = %q, want HTML", path, contentType)
		}
		body := response.Body.String()
		document, err := goquery.NewDocumentFromReader(strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		return document, body
	}

	t.Run("server-rendered search paginates through HTMX links", func(t *testing.T) {
		document, body := getPage(t, "/?q=Moon&source=asmrone&sort_by=workno&sort_desc=false&page_size=1", false)
		if !strings.HasPrefix(body, "<!doctype html>") && !strings.HasPrefix(body, "<!DOCTYPE html>") {
			t.Fatal("expected full HTML document")
		}
		if got := document.Find(".work-card-title").Text(); got != title {
			t.Fatalf("full response first work title = %q, want %q", got, title)
		}
		if document.Find("[onerror]").Length() != 0 {
			t.Fatal("unescaped metadata created an event handler in HTML")
		}
		var favorite db.Work
		if err := json.Unmarshal([]byte(document.Find(".card-favorite").AttrOr("data-work", "")), &favorite); err != nil {
			t.Fatal(err)
		}
		if favorite.Workno != "RJ100001" || favorite.Title != title {
			t.Fatalf("favorite payload = %#v", favorite)
		}
		assertCatalogExternalLink(t, document.Find(".work-card > a.dlsite-link"), "RJ100001")
		if document.Find(".work-card-open .dlsite-link, #topbar .quick-tag").Length() != 0 {
			t.Fatal("found an external link inside the card opener or tag in the toolbar")
		}
		next := document.Find(".pagination a")
		if next.AttrOr("hx-target", "") != "#page-content" || next.AttrOr("hx-boost", "") != "true" {
			t.Fatal("pagination must target the catalog region through HTMX")
		}
		nextURL, err := url.Parse(next.AttrOr("href", ""))
		if err != nil {
			t.Fatal(err)
		}
		for key, want := range map[string]string{"q": "Moon", "source": "asmrone", "page": "2", "page_size": "1", "sort_by": "workno", "sort_desc": "false"} {
			if got := nextURL.Query().Get(key); got != want {
				t.Fatalf("next page %s = %q, want %q", key, got, want)
			}
		}
		fragment, fragmentBody := getPage(t, nextURL.String(), true)
		if strings.Contains(strings.ToLower(fragmentBody), "<!doctype") || fragment.Find("#topbar[hx-swap-oob=outerHTML]").Length() != 1 {
			t.Fatal("HTMX response must contain the updated toolbar and no document wrapper")
		}
		if got := fragment.Find(".work-card-title").Text(); got != "Moon Second" {
			t.Fatalf("second work title = %q", got)
		}
	})

	t.Run("fuzzy keyword search honors filters in HTML and JSON", func(t *testing.T) {
		for _, keyword := range []string{"Moo", "Mooon"} {
			t.Run(keyword, func(t *testing.T) {
				params := url.Values{"q": {keyword + " -Second"}, "source": {"asmrone"}}
				document, _ := getPage(t, "/?"+params.Encode(), true)
				titles := document.Find(".work-card-title")
				if titles.Length() != 1 || titles.Text() != title {
					t.Errorf("HTML work titles = %q across %d cards, want only %q", titles.Text(), titles.Length(), title)
				}

				response := httptest.NewRecorder()
				router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/search?"+params.Encode(), nil))
				if response.Code != http.StatusOK {
					t.Fatalf("search API status = %d, body %s", response.Code, response.Body)
				}
				var results struct {
					Items    []db.Work `json:"items"`
					Total    int       `json:"total"`
					Degraded bool      `json:"degraded"`
				}
				if err := json.Unmarshal(response.Body.Bytes(), &results); err != nil {
					t.Fatal(err)
				}
				if results.Degraded || results.Total != 1 || len(results.Items) != 1 || results.Items[0].Workno != "RJ100001" {
					t.Fatalf("search API results = %+v, want one indexed hit for RJ100001", results)
				}
			})
		}
	})

	t.Run("native selects retain missing facet values and escape source labels", func(t *testing.T) {
		const missing = "旧选项 & <保留>"
		query := url.Values{"circle": {missing}, "va": {missing}, "tag": {missing}, "page": {"4"}, "view": {"table"}}
		document, _ := getPage(t, "/?"+query.Encode(), true)
		filters := document.Find("#catalog-filters")
		if filters.AttrOr("hx-get", "") != "/" || filters.AttrOr("hx-trigger", "") != "change, submit" {
			t.Fatal("filter form must request a fragment on change or submit")
		}
		if filters.Find("[name=page]").Length() != 0 {
			t.Fatal("changing a filter must return to the first page")
		}
		for key, label := range map[string]string{"circle": "全部社团", "va": "全部声优", "tag": "全部标签"} {
			selectInput := filters.Find("select[name=" + key + "]")
			defaultOption := selectInput.Find("option[value='']")
			if defaultOption.Text() != label || defaultOption.AttrOr("data-count", "") != "1" {
				t.Fatalf("default option for %s lost its label or count", key)
			}
			selected := selectInput.Find("option[selected]")
			if selected.Text() != missing || selected.AttrOr("value", "") != missing {
				t.Fatalf("missing %s selection was not preserved", key)
			}
			present := selectInput.Find("option").FilterFunction(func(_ int, option *goquery.Selection) bool {
				return option.Text() == facet && option.AttrOr("value", "") == facet
			})
			if present.AttrOr("data-count", "") != "1" {
				t.Fatalf("stored %s facet lost its count or escaped label", key)
			}
		}
	})

	t.Run("facet wall links preserve browsing options and set the selected facet", func(t *testing.T) {
		for _, testCase := range []struct {
			path     string
			selector string
			param    string
		}{
			{path: "/people", selector: ".facet-grid a", param: "va"},
			{path: "/tags", selector: ".tag-wall a", param: "tag"},
		} {
			document, _ := getPage(t, testCase.path+"?view=table&age=general&source=asmrone&page=4&q=Moon", false)
			link := document.Find(testCase.selector).First()
			if got := link.Find(".facet-name").Text(); got != facet {
				t.Fatalf("%s facet title = %q, want %q", testCase.path, got, facet)
			}
			href, err := url.Parse(link.AttrOr("href", ""))
			if err != nil {
				t.Fatal(err)
			}
			query := href.Query()
			if href.Path != "/" || query.Get(testCase.param) != facet || query.Get("view") != "table" ||
				query.Get("age") != "general" || query.Get("source") != "asmrone" || query.Has("page") || query.Has("q") {
				t.Fatalf("unexpected %s facet link %s", testCase.path, href)
			}
		}
	})

	t.Run("server honors table preference and explicit grid override", func(t *testing.T) {
		preference := &http.Cookie{Name: "otoha_view", Value: "table"}
		document, _ := getPage(t, "/?q=Moon&sort_by=workno&sort_desc=false", false, preference)
		table := document.Find(".works-table")
		if table.Length() != 1 {
			t.Fatal("view cookie did not select the compact table")
		}
		assertCatalogExternalLink(t, table.Find("tr[data-workno=RJ100001] .dlsite-link"), "RJ100001")
		explicit, _ := getPage(t, "/?view=grid", false, preference)
		if explicit.Find(".card-grid").Length() != 1 {
			t.Fatal("explicit view did not override table preference")
		}
	})

	t.Run("recent playback supplies the saved queue on the unfiltered first page", func(t *testing.T) {
		const workno = "RJ100001"
		const position = 42.5
		if err := store.SaveRemoteTracks(ctx, workno, []db.RemoteTrackInput{{
			RelPath: "Collection/Audio.m4a", Duration: 75, StreamURL: "https://media.example/audio.m4a",
		}}, ""); err != nil {
			t.Fatal(err)
		}
		work, err := store.GetWorkDetail(ctx, workno, 1)
		if err != nil {
			t.Fatal(err)
		}
		if len(work.Tracks) != 1 {
			t.Fatalf("test work has %d tracks, want 1", len(work.Tracks))
		}
		trackID := work.Tracks[0].ID
		if _, err := history.NewService(store, cfg).RecordHeartbeat(ctx, history.HeartbeatRequest{
			UserID: 1, Workno: workno, TrackID: trackID, Position: position,
			Queue: &history.QueueSnapshot{TrackIDs: []int64{trackID}, FolderScope: "Collection"},
		}); err != nil {
			t.Fatal(err)
		}

		document, _ := getPage(t, "/", false)
		resume := document.Find(".recent-chip[data-action=resume-recent]")
		var entry db.HistoryEntry
		if err := json.Unmarshal([]byte(resume.AttrOr("data-entry", "")), &entry); err != nil {
			t.Fatal(err)
		}
		var queue history.QueueSnapshot
		if err := json.Unmarshal([]byte(entry.QueueCtx), &queue); err != nil {
			t.Fatal(err)
		}
		if entry.Workno != workno || entry.TrackID != trackID || entry.Position != position ||
			queue.FolderScope != "Collection" || len(queue.TrackIDs) != 1 || queue.TrackIDs[0] != trackID {
			t.Fatalf("unexpected recent playback data: %+v, queue %+v", entry, queue)
		}

		filtered, _ := getPage(t, "/?source=asmrone", true)
		if filtered.Find(".recent-chip").Length() != 0 {
			t.Fatal("filtered results should not include the recent strip")
		}
	})
}

func assertCatalogExternalLink(t *testing.T, link *goquery.Selection, workno string) {
	t.Helper()
	if got := link.AttrOr("href", ""); got != "https://www.dlsite.com/home/work/=/product_id/"+workno+".html" {
		t.Fatalf("DLsite href = %q", got)
	}
	if link.AttrOr("target", "") != "_blank" || link.AttrOr("rel", "") != "noopener noreferrer" {
		t.Fatal("DLsite link is missing safe external navigation attributes")
	}
	if _, actionable := link.Attr("data-action"); actionable {
		t.Fatal("DLsite link should preserve native navigation")
	}
}
