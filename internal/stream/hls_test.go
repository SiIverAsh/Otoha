package stream

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestHLSManifestVariantKeyAndRangeProxy(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/work/master.m3u8":
			io.WriteString(w, "#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=1000\nsub/audio.m3u8\n")
		case "/work/sub/audio.m3u8":
			io.WriteString(w, "#EXTM3U\n#EXT-X-KEY:METHOD=AES-128,URI=\"../key.bin\"\n#EXTINF:10,\na.ts\n#EXT-X-ENDLIST\n")
		case "/work/sub/a.ts":
			if r.Header.Get("Range") != "bytes=0-187" {
				t.Error("missing Range")
			}
			w.Header().Set("Content-Range", "bytes 0-187/1000")
			w.WriteHeader(206)
			w.Write(make([]byte, 188))
		case "/work/key.bin":
			w.Write([]byte("test-key"))
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()
	manifest := upstream.URL + "/work/master.m3u8"
	request := func(resource string, withRange bool) *httptest.ResponseRecorder {
		target := "/api/stream/42"
		if resource != "" {
			target += "?hls=" + url.QueryEscape(resource)
		}
		r := httptest.NewRequest("GET", target, nil)
		if withRange {
			r.Header.Set("Range", "bytes=0-187")
		}
		w := httptest.NewRecorder()
		if err := ProxyHLS(w, r, manifest, ""); err != nil {
			t.Fatal(err)
		}
		return w
	}
	master := request("", false)
	if master.Code != 200 || !strings.Contains(master.Header().Get("Content-Type"), "mpegurl") {
		t.Fatal(master)
	}
	var variant string
	for _, l := range strings.Split(master.Body.String(), "\n") {
		if strings.HasPrefix(l, "/api/") {
			u, _ := url.Parse(l)
			variant = u.Query().Get("hls")
		}
	}
	if variant != upstream.URL+"/work/sub/audio.m3u8" {
		t.Fatal(variant)
	}
	media := request(variant, false)
	body := media.Body.String()
	if !strings.Contains(body, "URI=\"/api/stream/42?hls=") || strings.Contains(body, "\na.ts\n") {
		t.Fatal(body)
	}
	segment := request(upstream.URL+"/work/sub/a.ts", true)
	if segment.Code != 206 || segment.Body.Len() != 188 || segment.Header().Get("Content-Range") == "" {
		t.Fatal(segment)
	}
}

func TestHLSRejectsCrossOriginAndTraversal(t *testing.T) {
	root := "https://cdn3.cloudintech.net/file/RJ123456/main.m3u8"
	for _, raw := range []string{"https://localhost/private", "https://cdn3.cloudintech.net/secret.ts", "../other/a.ts", "%2e%2e/other.ts", "https://user@cdn3.cloudintech.net/file/RJ123456/a.ts", "https://cdn3.cloudintech.net:443/file/RJ123456/a.ts"} {
		if _, err := ResolveHLSResource(root, raw); err == nil {
			t.Errorf("accepted %q", raw)
		}
	}
	if _, err := rewriteHLS("#EXTM3U\nhttps://evil.test/segment.ts\n", root, root, "/api/stream/1"); err == nil {
		t.Fatal("accepted cross-origin manifest resource")
	}
}
