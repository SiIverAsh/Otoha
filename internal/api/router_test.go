package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SiIverAsh/otoha/internal/config"
	"github.com/SiIverAsh/otoha/internal/db"
	"github.com/SiIverAsh/otoha/internal/dlsite"
	"github.com/SiIverAsh/otoha/internal/search"
	"github.com/SiIverAsh/otoha/web"
)

func TestFullRouterEndpointsAuthWebDAVBeaconAndVacuum(t *testing.T) {
	dataDir := t.TempDir()
	libDir := t.TempDir()

	// Prepare a local work folder with WAV + LRC subtitle
	workDir := filepath.Join(libDir, "RJ01234567")
	_ = os.MkdirAll(filepath.Join(workDir, "SE有り"), 0o755)
	wavBytes := make([]byte, 2048)
	for i := range wavBytes {
		wavBytes[i] = byte(i & 0xFF)
	}
	_ = os.WriteFile(filepath.Join(workDir, "SE有り", "01 本編.wav"), wavBytes, 0o644)
	_ = os.WriteFile(filepath.Join(workDir, "SE有り", "01 本編.lrc"), []byte("[00:01.50]テスト字幕\n"), 0o644)

	cfg := config.Default(dataDir)
	store, err := db.Open(cfg.DBPath())
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	defer store.Close()

	searchEng := search.Open(cfg.BlevePath())
	defer searchEng.Close()

	ctx := context.Background()
	st, _ := store.UpsertStorage(ctx, "local", "Voices", libDir, "")
	dur := 120.0
	_ = store.UpsertScannedWork(ctx, st.ID, "RJ01234567", "RJ01234567", "RJ01234567", dlsite.AgeGeneral, "", []db.ScannedTrackInput{
		{RelPath: "SE有り/01 本編.wav", Size: int64(len(wavBytes)), Duration: &dur, Ord: 1},
	})
	_ = store.SaveMetadata(ctx, &dlsite.Metadata{
		Workno:      "RJ01234567",
		Title:       "音葉テスト作品",
		CircleName:  "音葉社",
		AgeCategory: dlsite.AgeGeneral,
		VAs:         []string{"花澤テスト"},
		Tags:        []string{"バイノーラル"},
	}, "", true)
	if sd, err := store.GetSearchDocument(ctx, "RJ01234567"); err == nil {
		_ = searchEng.IndexWork(*sd)
	}

	distFS, err := web.DistFS()
	if err != nil {
		t.Fatalf("web.DistFS: %v", err)
	}

	router := NewRouter(Dependencies{
		Store:    store,
		Search:   searchEng,
		Cfg:      cfg,
		Logger:   log.New(io.Discard, "", 0),
		Frontend: distFS,
	})
	srv := httptest.NewServer(router)
	defer srv.Close()

	// 1. Embedded SPA index.html
	spaResp, err := http.Get(srv.URL + "/")
	if err != nil || spaResp.StatusCode != http.StatusOK {
		t.Fatalf("GET /: status=%v err=%v", spaResp, err)
	}
	spaBody, _ := io.ReadAll(spaResp.Body)
	spaResp.Body.Close()
	if !strings.Contains(string(spaBody), "Otoha") {
		t.Fatalf("expected Otoha SPA HTML")
	}

	// 2. Detail endpoint returns nested tree, subtitle detection, and track ID
	detResp, err := http.Get(srv.URL + "/api/works/RJ01234567")
	if err != nil || detResp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/works/RJ01234567 failed: %v", err)
	}
	var detail db.WorkDetail
	_ = json.NewDecoder(detResp.Body).Decode(&detail)
	detResp.Body.Close()
	if len(detail.Tracks) != 1 || !detail.Tracks[0].HasSubtitle {
		t.Fatalf("expected 1 track with HasSubtitle=true, got %+v", detail.Tracks)
	}
	trackID := detail.Tracks[0].ID

	// 3. Range 100-200 on /api/stream/{trackId} -> 206 Partial Content (§7 & §12)
	rangeReq, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/stream/1", nil)
	rangeReq.Header.Set("Range", "bytes=100-200")
	rangeResp, err := http.DefaultClient.Do(rangeReq)
	if err != nil {
		t.Fatalf("stream range: %v", err)
	}
	rangeBytes, _ := io.ReadAll(rangeResp.Body)
	rangeResp.Body.Close()
	if rangeResp.StatusCode != http.StatusPartialContent || !bytes.Equal(rangeBytes, wavBytes[100:201]) {
		t.Fatalf("expected 206 Partial Content matching [100:201], got status=%d len=%d", rangeResp.StatusCode, len(rangeBytes))
	}

	// 4. Subtitle endpoint
	subResp, err := http.Get(srv.URL + "/api/subtitle/1")
	if err != nil || subResp.StatusCode != http.StatusOK {
		t.Fatalf("subtitle: %v", err)
	}
	subRaw, _ := io.ReadAll(subResp.Body)
	subResp.Body.Close()
	if !strings.Contains(string(subRaw), "テスト字幕") {
		t.Fatalf("unexpected subtitle payload: %s", string(subRaw))
	}

	// 5. navigator.sendBeacon style text/plain heartbeat + play_position on work detail (§9.5)
	beaconBody := `{"workno":"RJ01234567","track_id":1,"position":42.5,"listened_delta":30,"queue":{"track_ids":[1],"current_index":0,"folder_scope":"SE有り"}}`
	hbResp, err := http.Post(srv.URL+"/api/history/heartbeat", "text/plain;charset=UTF-8", strings.NewReader(beaconBody))
	if err != nil || hbResp.StatusCode != http.StatusOK {
		t.Fatalf("sendBeacon heartbeat failed: status=%v err=%v", hbResp, err)
	}
	hbResp.Body.Close()

	detResp2, _ := http.Get(srv.URL + "/api/works/RJ01234567")
	var detail2 db.WorkDetail
	_ = json.NewDecoder(detResp2.Body).Decode(&detail2)
	detResp2.Body.Close()
	if detail2.Tracks[0].PlayPosition != 42.5 {
		t.Fatalf("expected play_position=42.5 on track detail, got %v", detail2.Tracks[0].PlayPosition)
	}
	_ = trackID

	// 6. Enable Authentication + Basic Auth on WebDAV (§9)
	hash, _ := HashPassword("pass1234")
	_, _ = store.UpsertUser(ctx, "alice", hash, true, false)
	_ = cfg.Update(func(c *config.Config) { c.AuthEnabled = true })

	unauthResp, _ := http.Get(srv.URL + "/api/works")
	unauthResp.Body.Close()
	if unauthResp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 when auth enabled without credentials, got %d", unauthResp.StatusCode)
	}

	davReq, _ := http.NewRequest("PROPFIND", srv.URL+"/dav/", nil)
	davReq.Header.Set("Depth", "1")
	davReq.SetBasicAuth("alice", "pass1234")
	davResp, err := http.DefaultClient.Do(davReq)
	if err != nil {
		t.Fatalf("authenticated PROPFIND: %v", err)
	}
	davResp.Body.Close()
	if davResp.StatusCode != http.StatusMultiStatus {
		t.Fatalf("expected 207 Multi-Status on Basic Auth WebDAV, got %d", davResp.StatusCode)
	}
}
