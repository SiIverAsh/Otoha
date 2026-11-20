package asmr18

import (
	"os"
	"strings"
	"testing"
)

func TestParseRealPackedPlayer(t *testing.T) {
	raw, err := os.ReadFile("testdata/work.html")
	if err != nil {
		t.Fatal(err)
	}
	w, err := Parse(BaseURL+"/allages/rj01752865/", string(raw))
	if err != nil {
		t.Fatal(err)
	}
	m := w.Metadata
	if m.Workno != "RJ01752865" || m.Title != "理容室こころ" || m.CircleName != "Crescendo" || m.AgeCategory != 1 || len(m.VAs) != 1 || m.VAs[0] != "中井みのる" || len(m.Tags) != 5 {
		t.Fatalf("metadata: %+v", m)
	}
	if m.ReleaseDate != "" || m.CreateDate != "" || m.CircleID != "" || m.Rating != 0 || m.Price != 0 {
		t.Fatalf("invented metadata: %+v", m)
	}
	if len(w.Tracks) != 1 {
		t.Fatalf("tracks: %+v", w.Tracks)
	}
	tr := w.Tracks[0]
	if tr.URL != "https://cdn3.cloudintech.net/file/RJ01752865/RJ01752865.m3u8" || len(tr.Chapters) != 5 || tr.Chapters[1].Start != 1825 || tr.Chapters[1].Title != "02もこもこシャンプー" {
		t.Fatalf("track: %+v", tr)
	}
}

func TestMultiplePlayersKeepSeparateChapters(t *testing.T) {
	html := `<h1>Multi</h1><div class="tab_content" id="file1_content"><script>const src='https://cdn3.cloudintech.net/file/RJ123456/1.m3u8';</script><div id="chapter"><a data-value="0">One<span>00:00</span></a></div></div><div class="tab_content" id="file2_content"><script>const src='https://cdn3.cloudintech.net/file/RJ123456/2.m3u8';</script><div id="chapter"><a data-value="10">Two<span>00:10</span></a></div></div>`
	w, err := Parse(BaseURL+"/boys/rj123456/", html)
	if err != nil {
		t.Fatal(err)
	}
	if len(w.Tracks) != 2 || w.Tracks[0].Key == w.Tracks[1].Key || len(w.Tracks[1].Chapters) != 1 || w.Tracks[1].Chapters[0].Start != 10 || w.Metadata.AgeCategory != 3 {
		t.Fatalf("%+v", w)
	}
}

func TestRejectUnsupportedSourcesAndPages(t *testing.T) {
	for _, u := range []string{"http://asmr18.fans/allages/rj123456/", "https://other.test/allages/rj123456/", BaseURL + "/allages/rj1234567/", BaseURL + "/allages/rj123456/extra/"} {
		if _, err := PageURL(u); err == nil {
			t.Errorf("accepted %s", u)
		}
	}
	for _, u := range []string{"http://cdn3.cloudintech.net/file/a.m3u8", "https://localhost/file/a.m3u8", "https://cdn3.cloudintech.net.evil.test/file/a.m3u8", "https://cdn3.cloudintech.net:8443/file/a.m3u8"} {
		if ValidMediaURL(u) {
			t.Errorf("accepted %s", u)
		}
	}
	_, err := Parse(BaseURL+"/allages/rj123456/", `<h1>Missing</h1><script>https://evil.test/a.m3u8</script>`)
	if err == nil || !strings.Contains(err.Error(), "no supported HLS") {
		t.Fatal(err)
	}
}
