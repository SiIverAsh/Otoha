package views

import (
	"net/url"
	"strings"
	"testing"

	"github.com/PuerkitoBio/goquery"
	"github.com/SiIverAsh/otoha/internal/db"
)

func TestDrawerVoiceActorsRenderAsSeparateFilterTags(t *testing.T) {
	for _, testCase := range []struct {
		name   string
		actors []string
	}{
		{name: "no voice actors"},
		{name: "one voice actor", actors: []string{"柚木つばめ"}},
		{name: "multiple voice actors", actors: []string{"柚木つばめ", "梅木ちはる", "A & B / C"}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			detail := db.WorkDetail{Work: db.Work{Workno: "RJ000001", VAs: testCase.actors}}
			document := renderDrawerForNavigation(t, &detail)
			tags := document.Find(".drawer-voice-actors > a.tag-pill[data-action=filter-va]")
			if tags.Length() != len(testCase.actors) {
				t.Fatalf("found %d voice actor tags, want %d", tags.Length(), len(testCase.actors))
			}
			for index, actor := range testCase.actors {
				tag := tags.Eq(index)
				if tag.Text() != actor || tag.AttrOr("data-val", "") != actor {
					t.Errorf("tag %d should display and filter %q, got %q / %q",
						index, actor, tag.Text(), tag.AttrOr("data-val", ""))
				}
				href := tag.AttrOr("href", "")
				link, err := url.Parse(href)
				if err != nil || link.Path != "/" || link.Query().Get("va") != actor {
					t.Errorf("tag %d links to %q, want a catalog filter for %q", index, href, actor)
				}
			}
		})
	}
}

func TestDrawerExplicitSaveAndNonHLSFolders(t *testing.T) {
	hls := &db.TrackTreeNode{Type: "audio", Name: "WAV 01", Track: &db.Track{ID: 1, StreamURL: "https://example.test/01.m3u8"}}
	wav := &db.TrackTreeNode{Type: "audio", Name: "WAV 02", Track: &db.Track{ID: 2, RelPath: "02.wav"}}
	mp3 := &db.TrackTreeNode{Type: "audio", Name: "MP3 03", Track: &db.Track{ID: 3, RelPath: "03.mp3"}}
	for _, testCase := range []struct {
		name     string
		children []*db.TrackTreeNode
	}{
		{name: "mixed HLS and WAV", children: []*db.TrackTreeNode{hls, wav}},
		{name: "local WAV and MP3", children: []*db.TrackTreeNode{wav, mp3}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			detail := db.WorkDetail{
				Work: db.Work{Workno: "RJ000001"},
				Tree: []*db.TrackTreeNode{{Type: "folder", Name: "Audio", RelPath: "Audio", Children: testCase.children}},
			}
			document := renderDrawerForNavigation(t, &detail)
			if document.Find(".tree-folder .badge-format").Length() != 0 {
				t.Fatal("a mixed or local folder was incorrectly labeled HLS")
			}
			if document.Find("select#state-progress, select#state-rating").Length() != 2 ||
				document.Find("#state-progress[data-action], #state-rating[data-action]").Length() != 0 ||
				document.Find("button[data-action=save-state]").Length() != 1 {
				t.Fatal("changing a drawer field should require an explicit save")
			}
		})
	}
}

func renderDrawerForNavigation(t *testing.T, detail *db.WorkDetail) *goquery.Document {
	t.Helper()
	var result strings.Builder
	if err := WorkDrawer(detail, "{}", false).Render(t.Context(), &result); err != nil {
		t.Fatal(err)
	}
	document, err := goquery.NewDocumentFromReader(strings.NewReader(result.String()))
	if err != nil {
		t.Fatal(err)
	}
	return document
}
