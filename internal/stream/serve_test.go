package stream

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestRangeStreaming206PartialContent(t *testing.T) {
	dir := t.TempDir()
	wavPath := filepath.Join(dir, "01 本編.wav")

	payload := make([]byte, 4096)
	for i := range payload {
		payload[i] = byte((i*31 + 7) & 0xFF)
	}
	if err := os.WriteFile(wavPath, payload, 0o644); err != nil {
		t.Fatalf("write wav: %v", err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = ServeAudioFile(w, r, wavPath)
	}))
	defer srv.Close()

	// §12: Range 100-200 must return 206 Partial Content and exact bytes [100:201]
	req, err := http.NewRequest(http.MethodGet, srv.URL+"/api/stream/1", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Range", "bytes=100-200")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusPartialContent {
		t.Fatalf("expected 206 Partial Content, got %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "audio/wav" {
		t.Fatalf("expected Content-Type audio/wav, got %q", ct)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	want := payload[100:201]
	if !bytes.Equal(body, want) {
		t.Fatalf("range bytes mismatch: got len=%d, want len=%d", len(body), len(want))
	}

	// Verify MIME map (§7)
	mimeCases := map[string]string{
		"a.wav":  "audio/wav",
		"a.flac": "audio/flac",
		"a.m4a":  "audio/mp4",
		"a.opus": "audio/ogg",
		"a.ogg":  "audio/ogg",
		"a.mp3":  "audio/mpeg",
	}
	for name, wantMime := range mimeCases {
		if got := ContentTypeForExt(name); got != wantMime {
			t.Errorf("ContentTypeForExt(%q) = %q, want %q", name, got, wantMime)
		}
	}
}
