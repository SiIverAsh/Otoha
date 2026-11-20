package views

import (
	"strings"
	"testing"

	"github.com/PuerkitoBio/goquery"
	"github.com/SiIverAsh/otoha/internal/db"
)

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
