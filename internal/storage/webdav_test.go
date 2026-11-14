package storage

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SiIverAsh/otoha/internal/db"
)

func TestWebDAVReadOnlyExport(t *testing.T) {
	libDir := t.TempDir()
	workDir := filepath.Join(libDir, "RJ01234567")
	_ = os.MkdirAll(workDir, 0o755)
	_ = os.WriteFile(filepath.Join(workDir, "01.wav"), []byte("RIFFWAVE_TEST_PAYLOAD"), 0o644)

	store, err := db.Open(filepath.Join(t.TempDir(), "otoha.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	defer store.Close()

	if _, err := store.UpsertStorage(context.Background(), "local", "NAS", libDir, ""); err != nil {
		t.Fatalf("UpsertStorage: %v", err)
	}

	srv := httptest.NewServer(NewWebDAVHandler("/dav", store))
	defer srv.Close()

	// 1. PROPFIND on /dav/ lists "NAS"
	propReq, _ := http.NewRequest("PROPFIND", srv.URL+"/dav/", nil)
	propReq.Header.Set("Depth", "1")
	propResp, err := http.DefaultClient.Do(propReq)
	if err != nil {
		t.Fatalf("PROPFIND: %v", err)
	}
	propBody, _ := io.ReadAll(propResp.Body)
	propResp.Body.Close()
	if propResp.StatusCode != http.StatusMultiStatus || !strings.Contains(string(propBody), "NAS") {
		t.Fatalf("expected 207 Multi-Status containing NAS, got %d body=%s", propResp.StatusCode, string(propBody))
	}

	// 2. GET /dav/NAS/RJ01234567/01.wav streams file
	getResp, err := http.Get(srv.URL + "/dav/NAS/RJ01234567/01.wav")
	if err != nil {
		t.Fatalf("GET file: %v", err)
	}
	fileBytes, _ := io.ReadAll(getResp.Body)
	getResp.Body.Close()
	if getResp.StatusCode != http.StatusOK || string(fileBytes) != "RIFFWAVE_TEST_PAYLOAD" {
		t.Fatalf("unexpected WebDAV GET: status=%d body=%q", getResp.StatusCode, string(fileBytes))
	}

	// 3. PUT / DELETE must be rejected (read-only WebDAV, §9)
	putReq, _ := http.NewRequest(http.MethodPut, srv.URL+"/dav/NAS/RJ01234567/evil.txt", strings.NewReader("nope"))
	putResp, err := http.DefaultClient.Do(putReq)
	if err != nil {
		t.Fatalf("PUT: %v", err)
	}
	putResp.Body.Close()
	if putResp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405 Method Not Allowed on WebDAV PUT, got %d", putResp.StatusCode)
	}
}
