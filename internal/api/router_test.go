package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SiIverAsh/otoha/internal/config"
	"github.com/SiIverAsh/otoha/internal/db"
	"github.com/SiIverAsh/otoha/internal/dlsite"
	"github.com/SiIverAsh/otoha/internal/search"
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

	router := NewRouter(Dependencies{
		Store:  store,
		Search: searchEng,
		Cfg:    cfg,
		Logger: log.New(io.Discard, "", 0),
	})
	srv := httptest.NewServer(router)
	defer srv.Close()

	// 1. The document includes catalog data before any browser JavaScript runs.
	document := performWebRequest(router, http.MethodGet, "/", false, nil)
	if document.Code != http.StatusOK ||
		!strings.Contains(document.Body.String(), "音葉テスト作品") ||
		!strings.Contains(document.Body.String(), `id="player-region"`) {
		t.Fatalf("GET / did not include document shell and works: %d", document.Code)
	}
	fragment := performWebRequest(router, http.MethodGet, "/?q="+url.QueryEscape("音葉テスト作品"), true, nil)
	if fragment.Code != http.StatusOK ||
		!strings.Contains(fragment.Body.String(), `data-work-card="RJ01234567"`) ||
		!strings.Contains(fragment.Body.String(), `hx-swap-oob="outerHTML"`) ||
		strings.Contains(fragment.Body.String(), `id="player-region"`) {
		t.Fatalf("HTMX search did not return a page fragment: %d", fragment.Code)
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

func TestWebLoginAndPageFragments(t *testing.T) {
	cfg := config.Default(t.TempDir())
	cfg.AuthEnabled = true
	store, err := db.Open(cfg.DBPath())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	})
	searchEngine := search.Open(cfg.BlevePath())
	t.Cleanup(func() { searchEngine.Close() })
	const password = "testing password"
	hash, err := HashPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpsertUser(t.Context(), "alice", hash, true, false); err != nil {
		t.Fatal(err)
	}
	router := NewRouter(Dependencies{Store: store, Search: searchEngine, Cfg: cfg, Logger: log.New(io.Discard, "", 0)})

	loginRedirect := performWebRequest(router, http.MethodGet, "/settings", false, nil)
	if loginRedirect.Code != http.StatusSeeOther || loginRedirect.Header().Get("Location") != "/login?next=%2Fsettings" {
		t.Fatalf("unauthenticated document redirect: %d %v", loginRedirect.Code, loginRedirect.Header())
	}
	htmxRedirect := performWebRequest(router, http.MethodGet, "/history", true, nil)
	if htmxRedirect.Code != http.StatusOK || htmxRedirect.Header().Get("HX-Redirect") != "/login?next=%2Fhistory" {
		t.Fatalf("unauthenticated fragment redirect: %d %v", htmxRedirect.Code, htmxRedirect.Header())
	}
	login := performWebRequest(router, http.MethodGet, loginRedirect.Header().Get("Location"), false, nil)
	if login.Code != http.StatusOK || !strings.Contains(login.Body.String(), `id="login-form"`) {
		t.Fatalf("login form was not rendered: %d", login.Code)
	}

	form := url.Values{"username": {"alice"}, "password": {"wrong password"}, "next": {"/settings"}}
	rejection := performWebRequest(router, http.MethodPost, "/login", true, form)
	if rejection.Code != http.StatusUnprocessableEntity ||
		!strings.Contains(rejection.Body.String(), "用户名或密码错误") ||
		strings.Contains(strings.ToLower(rejection.Body.String()), "<!doctype") ||
		len(rejection.Result().Cookies()) != 0 {
		t.Fatalf("invalid password response: %d %v", rejection.Code, rejection.Header())
	}
	form.Set("password", password)
	success := performWebRequest(router, http.MethodPost, "/login", true, form)
	cookies := success.Result().Cookies()
	if success.Code != http.StatusOK || success.Header().Get("HX-Redirect") != "/settings" ||
		len(cookies) != 1 || !cookies[0].HttpOnly {
		t.Fatalf("successful login response: %d %v", success.Code, success.Header())
	}

	document := performWebRequest(router, http.MethodGet, "/settings", false, nil, cookies[0])
	if document.Code != http.StatusOK ||
		!strings.Contains(document.Body.String(), `id="settings-asmr18"`) ||
		!strings.Contains(document.Body.String(), `id="settings-users"`) ||
		!strings.Contains(document.Body.String(), `id="player-region"`) {
		t.Fatalf("settings were not rendered in the document shell: %d", document.Code)
	}
	fragment := performWebRequest(router, http.MethodGet, "/history", true, nil, cookies[0])
	if fragment.Code != http.StatusOK ||
		!strings.Contains(fragment.Body.String(), "暂无收听记录") ||
		strings.Contains(fragment.Body.String(), `id="player-region"`) {
		t.Fatalf("history fragment did not render: %d", fragment.Code)
	}
	task := performWebRequest(router, http.MethodGet, "/ui/settings/task", true, nil, cookies[0])
	if task.Code != http.StatusOK ||
		!strings.Contains(task.Body.String(), `id="asmr18-preview-result" hx-swap-oob="innerHTML"`) {
		t.Fatalf("settings status did not include its out-of-band preview target: %d", task.Code)
	}

	crossSite := httptest.NewRequest(http.MethodDelete, "/ui/history", nil)
	crossSite.Header.Set("Sec-Fetch-Site", "cross-site")
	crossSite.AddCookie(cookies[0])
	response := httptest.NewRecorder()
	router.ServeHTTP(response, crossSite)
	if response.Code != http.StatusForbidden {
		t.Fatalf("cross-site history deletion was not blocked: %d", response.Code)
	}

	asset := performWebRequest(router, http.MethodGet, "/assets/styles.css", false, nil)
	if asset.Code != http.StatusOK || !strings.HasPrefix(asset.Header().Get("Content-Type"), "text/css") {
		t.Fatalf("embedded stylesheet response: %d %v", asset.Code, asset.Header())
	}
	missing := performWebRequest(router, http.MethodGet, "/missing", false, nil)
	if missing.Code != http.StatusNotFound {
		t.Fatalf("unknown document route status: %d", missing.Code)
	}
}

func performWebRequest(router http.Handler, method, path string, htmx bool, form url.Values, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, strings.NewReader(form.Encode()))
	if form != nil {
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if htmx {
		request.Header.Set("HX-Request", "true")
	}
	for _, cookie := range cookies {
		request.AddCookie(cookie)
	}
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	return recorder
}
