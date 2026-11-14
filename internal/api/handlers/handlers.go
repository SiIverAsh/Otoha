package handlers

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/SiIverAsh/otoha/internal/config"
	"github.com/SiIverAsh/otoha/internal/db"
	"github.com/SiIverAsh/otoha/internal/dlsite"
	"github.com/SiIverAsh/otoha/internal/history"
	"github.com/SiIverAsh/otoha/internal/scrape"
	"github.com/SiIverAsh/otoha/internal/search"
	"github.com/SiIverAsh/otoha/internal/stream"
	"github.com/SiIverAsh/otoha/internal/subtitle"
)

// TaskRunner abstracts background scan/scrape orchestration for API handlers.
type TaskRunner interface {
	TriggerScan(storageID int64, scrapeMetadata bool, forceScrape bool) (bool, error)
	Cancel() bool
	ServeSSE(w http.ResponseWriter, r *http.Request)
	SnapshotJSON() any
}

// AuthCallbacks provides password/session helpers without cyclic imports.
type AuthCallbacks struct {
	HashPassword       func(plain string) (string, error)
	VerifyPassword     func(hash, plain string) bool
	IssueSessionCookie func(w http.ResponseWriter, r *http.Request, userID int64) (string, error)
	CookieName         string
}

// API bundles all REST endpoint handlers (§9).
type API struct {
	Store   *db.Store
	Search  *search.Engine
	History *history.Service
	Cfg     *config.Config
	Tasks   TaskRunner
	Auth    AuthCallbacks
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

func decodeBody(r *http.Request, dst any) error {
	defer r.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(r.Body, 16<<20))
	if err != nil {
		return err
	}
	if len(strings.TrimSpace(string(raw))) == 0 {
		return nil
	}
	return json.Unmarshal(raw, dst)
}

func (a *API) effectiveAgeMode(r *http.Request) string {
	if explicit := strings.TrimSpace(r.URL.Query().Get("age")); explicit != "" {
		return explicit
	}
	if a.Cfg.Snapshot().DefaultHideNSFW {
		return "general"
	}
	return "all"
}

// --- Auth & Users ---

func (a *API) GetMe(w http.ResponseWriter, r *http.Request) {
	snap := a.Cfg.Snapshot()
	degraded, warn := a.Search.IsDegraded()
	var user *db.User
	if !snap.AuthEnabled {
		user = &db.User{ID: 1, Username: "default", IsAdmin: true}
	} else {
		if c, err := r.Cookie(a.Auth.CookieName); err == nil && c.Value != "" {
			user, _ = a.Store.GetUserBySession(r.Context(), c.Value)
		}
		if user == nil {
			authHdr := r.Header.Get("Authorization")
			if strings.HasPrefix(strings.ToLower(authHdr), "bearer ") {
				user, _ = a.Store.GetUserBySession(r.Context(), strings.TrimSpace(authHdr[7:]))
			}
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"authenticated":          user != nil,
		"auth_enabled":           snap.AuthEnabled,
		"user":                   user,
		"default_hide_nsfw":      snap.DefaultHideNSFW,
		"history_enabled":        snap.HistoryEnabled,
		"heartbeat_interval_sec": snap.HeartbeatIntervalSec,
		"webdav_enabled":         snap.WebDAVEnabled,
		"search_degraded":        degraded,
		"search_warning":         warn,
	})
}

func (a *API) Login(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Username    string `json:"username"`
		Password    string `json:"password"`
		NewPassword string `json:"new_password"`
	}
	if err := decodeBody(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	uname := strings.TrimSpace(req.Username)
	if uname == "" {
		writeErr(w, http.StatusBadRequest, "username is required")
		return
	}

	u, err := a.Store.GetUserByUsername(r.Context(), uname)
	if err != nil {
		writeErr(w, http.StatusUnauthorized, "用户名或密码错误")
		return
	}

	// §14.4: Migrated kikoeru user has empty password_hash & must_reset_password = true
	if u.MustResetPassword || u.PasswordHash == "" {
		targetPwd := req.NewPassword
		if targetPwd == "" {
			targetPwd = req.Password
		}
		if len(targetPwd) < 4 {
			writeJSON(w, http.StatusForbidden, map[string]any{
				"error":               "该账号需要首次重设密码（至少 4 位）",
				"must_reset_password": true,
			})
			return
		}
		hash, err := a.Auth.HashPassword(targetPwd)
		if err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		if err := a.Store.UpdateUserPassword(r.Context(), u.ID, hash); err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		u.MustResetPassword = false
	} else {
		if !a.Auth.VerifyPassword(u.PasswordHash, req.Password) {
			writeErr(w, http.StatusUnauthorized, "用户名或密码错误")
			return
		}
	}

	tok, err := a.Auth.IssueSessionCookie(w, r, u.ID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"token": tok,
		"user":  u,
	})
}

func (a *API) Logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(a.Auth.CookieName); err == nil && c.Value != "" {
		_ = a.Store.DeleteSession(r.Context(), c.Value)
	}
	http.SetCookie(w, &http.Cookie{
		Name:     a.Auth.CookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
	})
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (a *API) ListUsers(w http.ResponseWriter, r *http.Request) {
	u := UserFromContext(r.Context())
	if !u.IsAdmin {
		writeErr(w, http.StatusForbidden, "admin required")
		return
	}
	users, err := a.Store.ListUsers(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": users})
}

func (a *API) UpsertUser(w http.ResponseWriter, r *http.Request) {
	caller := UserFromContext(r.Context())
	if !caller.IsAdmin {
		writeErr(w, http.StatusForbidden, "admin required")
		return
	}
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
		IsAdmin  bool   `json:"is_admin"`
	}
	if err := decodeBody(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	uname := strings.TrimSpace(req.Username)
	if uname == "" {
		writeErr(w, http.StatusBadRequest, "username is required")
		return
	}
	hash, err := a.Auth.HashPassword(req.Password)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	created, err := a.Store.UpsertUser(r.Context(), uname, hash, req.IsAdmin, false)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, created)
}

// --- Works, Detail, State, Tags, Manual Edit ---

func (a *API) ListWorks(w http.ResponseWriter, r *http.Request) {
	user := UserFromContext(r.Context())
	q := r.URL.Query()
	page, _ := strconv.Atoi(q.Get("page"))
	pageSize, _ := strconv.Atoi(q.Get("page_size"))
	storageID, _ := strconv.ParseInt(q.Get("storage_id"), 10, 64)
	sortDesc := q.Get("sort_desc") != "false" && q.Get("sort_desc") != "0"
	ageMode := a.effectiveAgeMode(r)

	items, total, err := a.Store.ListWorks(r.Context(), db.ListWorksOptions{
		UserID:    user.ID,
		Page:      page,
		PageSize:  pageSize,
		SortBy:    q.Get("sort_by"),
		SortDesc:  sortDesc,
		AgeMode:   ageMode,
		Circle:    q.Get("circle"),
		VA:        q.Get("va"),
		Tag:       q.Get("tag"),
		Progress:  q.Get("progress"),
		StorageID: storageID,
	})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"items":     items,
		"total":     total,
		"page":      max(1, page),
		"page_size": max(1, pageSize),
		"age_mode":  ageMode,
	})
}

func (a *API) ListFacets(w http.ResponseWriter, r *http.Request) {
	ageMode := a.effectiveAgeMode(r)
	circles, vas, tags, err := a.Store.ListFacets(r.Context(), ageMode)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"circles": circles,
		"vas":     vas,
		"tags":    tags,
	})
}

func (a *API) GetWork(w http.ResponseWriter, r *http.Request) {
	user := UserFromContext(r.Context())
	workno := r.PathValue("workno")
	detail, err := a.Store.GetWorkDetail(r.Context(), workno, user.ID)
	if errors.Is(err, db.ErrNotFound) {
		writeErr(w, http.StatusNotFound, "work not found")
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Annotate HasSubtitle on tracks & tree nodes (§8)
	storages, _ := a.Store.ListStorages(r.Context())
	rootByID := make(map[int64]string, len(storages))
	for _, st := range storages {
		rootByID[st.ID] = st.Root
	}
	hasSubByTrackID := make(map[int64]bool, len(detail.Tracks))
	for i := range detail.Tracks {
		tr := &detail.Tracks[i]
		if stRoot := rootByID[tr.StorageID]; stRoot != "" {
			absAudio := filepath.Join(stRoot, filepath.FromSlash(tr.WorkRel), filepath.FromSlash(tr.RelPath))
			tr.HasSubtitle = subtitle.FindMatchingSubtitle(absAudio) != ""
			hasSubByTrackID[tr.ID] = tr.HasSubtitle
		}
	}
	annotateTreeSubtitles(detail.Tree, hasSubByTrackID)

	writeJSON(w, http.StatusOK, detail)
}

func annotateTreeSubtitles(nodes []*db.TrackTreeNode, subMap map[int64]bool) {
	for _, n := range nodes {
		if n.Track != nil {
			n.Track.HasSubtitle = subMap[n.Track.ID]
		}
		if len(n.Children) > 0 {
			annotateTreeSubtitles(n.Children, subMap)
		}
	}
}

func (a *API) UpdateWorkMetadata(w http.ResponseWriter, r *http.Request) {
	workno := dlsite.NormalizeWorkno(r.PathValue("workno"))
	if workno == "" {
		writeErr(w, http.StatusBadRequest, "invalid workno")
		return
	}
	var meta dlsite.Metadata
	if err := decodeBody(r, &meta); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	meta.Workno = workno
	meta.Source = "manual"
	meta.FetchedAt = time.Now().Unix()
	if err := a.Store.SaveMetadata(r.Context(), &meta, "", true); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if doc, err := a.Store.GetSearchDocument(r.Context(), workno); err == nil {
		_ = a.Search.IndexWork(*doc)
	}
	user := UserFromContext(r.Context())
	detail, _ := a.Store.GetWorkDetail(r.Context(), workno, user.ID)
	writeJSON(w, http.StatusOK, detail)
}

func (a *API) ScrapeSingleWork(w http.ResponseWriter, r *http.Request) {
	workno := dlsite.NormalizeWorkno(r.PathValue("workno"))
	if workno == "" {
		writeErr(w, http.StatusBadRequest, "invalid workno")
		return
	}
	cache, err := dlsite.NewCache(a.Cfg.CacheDir())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	client, err := scrape.NewClient(a.Cfg, cache, 100*time.Millisecond)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	meta, err := client.Fetch(r.Context(), workno, true)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	coverPath, _ := client.DownloadCover(r.Context(), workno, meta.CoverURL)
	if err := a.Store.SaveMetadata(r.Context(), meta, coverPath, true); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if doc, err := a.Store.GetSearchDocument(r.Context(), workno); err == nil {
		_ = a.Search.IndexWork(*doc)
	}
	user := UserFromContext(r.Context())
	detail, _ := a.Store.GetWorkDetail(r.Context(), workno, user.ID)
	writeJSON(w, http.StatusOK, detail)
}

func (a *API) UpdateWorkState(w http.ResponseWriter, r *http.Request) {
	user := UserFromContext(r.Context())
	workno := r.PathValue("workno")
	var req struct {
		Rating   *int    `json:"rating"`
		Review   *string `json:"review"`
		Progress *string `json:"progress"`
	}
	if err := decodeBody(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	st, err := a.Store.UpsertUserState(r.Context(), user.ID, workno, req.Rating, req.Review, req.Progress)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, st)
}

func (a *API) UpdateWorkTags(w http.ResponseWriter, r *http.Request) {
	workno := r.PathValue("workno")
	var req struct {
		Tags []string `json:"tags"`
	}
	if err := decodeBody(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	tags, err := a.Store.SetWorkUserTags(r.Context(), workno, req.Tags)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if doc, err := a.Store.GetSearchDocument(r.Context(), strings.ToUpper(strings.TrimSpace(workno))); err == nil {
		_ = a.Search.IndexWork(*doc)
	}
	writeJSON(w, http.StatusOK, map[string]any{"tags": tags})
}

// --- Search & Reindex ---

func (a *API) SearchWorks(w http.ResponseWriter, r *http.Request) {
	user := UserFromContext(r.Context())
	qStr := strings.TrimSpace(r.URL.Query().Get("q"))
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	pageSize, _ := strconv.Atoi(r.URL.Query().Get("page_size"))
	ageMode := a.effectiveAgeMode(r)

	if qStr == "" {
		a.ListWorks(w, r)
		return
	}

	sres, _ := a.Search.Search(qStr, 500)
	if sres != nil && sres.Degraded {
		items, total, err := a.Store.ListWorks(r.Context(), db.ListWorksOptions{
			UserID:    user.ID,
			Page:      page,
			PageSize:  pageSize,
			AgeMode:   ageMode,
			QueryLike: qStr,
			SortDesc:  true,
		})
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"items":         items,
			"total":         total,
			"degraded":      true,
			"needs_reindex": true,
			"warning":       sres.Warning,
			"age_mode":      ageMode,
		})
		return
	}

	items, total, err := a.Store.ListWorks(r.Context(), db.ListWorksOptions{
		UserID:   user.ID,
		Page:     page,
		PageSize: pageSize,
		AgeMode:  ageMode,
		Worknos:  sres.Worknos,
		SortDesc: true,
	})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items":         items,
		"total":         total,
		"degraded":      false,
		"needs_reindex": false,
		"age_mode":      ageMode,
	})
}

func (a *API) Reindex(w http.ResponseWriter, r *http.Request) {
	count, err := a.Search.ReindexFromStore(r.Context(), a.Store)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"indexed": count})
}

// --- Stream, Cover, Subtitle ---

func (a *API) StreamTrack(w http.ResponseWriter, r *http.Request) {
	trackID, err := strconv.ParseInt(r.PathValue("trackId"), 10, 64)
	if err != nil || trackID <= 0 {
		writeErr(w, http.StatusBadRequest, "invalid trackId")
		return
	}
	rt, err := a.Store.ResolveTrack(r.Context(), trackID)
	if errors.Is(err, db.ErrNotFound) {
		writeErr(w, http.StatusNotFound, "track not found")
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	_ = stream.ServeAudioFile(w, r, rt.AbsFilePath())
}

func (a *API) GetCover(w http.ResponseWriter, r *http.Request) {
	workno := strings.ToUpper(strings.TrimSpace(r.PathValue("workno")))
	user := UserFromContext(r.Context())
	detail, err := a.Store.GetWorkDetail(r.Context(), workno, user.ID)
	if err == nil && detail.CoverPath != "" {
		if st, statErr := os.Stat(detail.CoverPath); statErr == nil && !st.IsDir() {
			http.ServeFile(w, r, detail.CoverPath)
			return
		}
	}
	// Clean minimal SVG placeholder when offline / no cover image present
	w.Header().Set("Content-Type", "image/svg+xml; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=300")
	_, _ = fmt.Fprintf(w, `<svg xmlns="http://www.w3.org/2000/svg" width="320" height="240" viewBox="0 0 320 240"><rect width="320" height="240" fill="#18181b"/><text x="160" y="116" fill="#71717a" font-family="monospace" font-size="18" text-anchor="middle">%s</text><text x="160" y="140" fill="#52525b" font-family="sans-serif" font-size="12" text-anchor="middle">OTOHA</text></svg>`, workno)
}

func (a *API) GetSubtitle(w http.ResponseWriter, r *http.Request) {
	trackID, err := strconv.ParseInt(r.PathValue("trackId"), 10, 64)
	if err != nil || trackID <= 0 {
		writeErr(w, http.StatusBadRequest, "invalid trackId")
		return
	}
	rt, err := a.Store.ResolveTrack(r.Context(), trackID)
	if errors.Is(err, db.ErrNotFound) {
		writeErr(w, http.StatusNotFound, "track not found")
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	subPath := subtitle.FindMatchingSubtitle(rt.AbsFilePath())
	if subPath == "" {
		writeJSON(w, http.StatusOK, map[string]any{
			"found": false,
			"cues":  []subtitle.Cue{},
		})
		return
	}
	parsed, err := subtitle.LoadAndParse(subPath)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"found":    true,
		"format":   parsed.Format,
		"encoding": parsed.Encoding,
		"file":     parsed.FileName,
		"cues":     parsed.Cues,
	})
}

// --- Storages, Scan, Tasks ---

func (a *API) ListStorages(w http.ResponseWriter, r *http.Request) {
	items, err := a.Store.ListStorages(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (a *API) CreateStorage(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Kind   string `json:"kind"`
		Name   string `json:"name"`
		Root   string `json:"root"`
		Config string `json:"config"`
	}
	if err := decodeBody(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	name := strings.TrimSpace(req.Name)
	root := strings.TrimSpace(req.Root)
	if name == "" || root == "" {
		writeErr(w, http.StatusBadRequest, "name and root are required")
		return
	}
	st, err := a.Store.UpsertStorage(r.Context(), req.Kind, name, root, req.Config)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, st)
}

func (a *API) DeleteStorage(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeErr(w, http.StatusBadRequest, "invalid storage id")
		return
	}
	if err := a.Store.DeleteStorage(r.Context(), id); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (a *API) TriggerScan(w http.ResponseWriter, r *http.Request) {
	var req struct {
		StorageID      int64 `json:"storage_id"`
		ScrapeMetadata bool  `json:"scrape_metadata"`
		ForceScrape    bool  `json:"force_scrape"`
	}
	_ = decodeBody(r, &req)
	started, err := a.Tasks.TriggerScan(req.StorageID, req.ScrapeMetadata, req.ForceScrape)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"started": started,
		"task":    a.Tasks.SnapshotJSON(),
	})
}

func (a *API) CancelTask(w http.ResponseWriter, r *http.Request) {
	cancelled := a.Tasks.Cancel()
	writeJSON(w, http.StatusOK, map[string]bool{"cancelled": cancelled})
}

func (a *API) GetTasks(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, a.Tasks.SnapshotJSON())
}

func (a *API) StreamTaskEvents(w http.ResponseWriter, r *http.Request) {
	a.Tasks.ServeSSE(w, r)
}

// --- Listen History & Privacy (§9.5 & §9.6) ---

func (a *API) PostHeartbeat(w http.ResponseWriter, r *http.Request) {
	user := UserFromContext(r.Context())
	var req history.HeartbeatRequest
	if err := decodeBody(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid heartbeat payload")
		return
	}
	req.UserID = user.ID
	res, err := a.History.RecordHeartbeat(r.Context(), req)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (a *API) ListHistory(w http.ResponseWriter, r *http.Request) {
	user := UserFromContext(r.Context())
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	pageSize, _ := strconv.Atoi(r.URL.Query().Get("page_size"))
	ageMode := a.effectiveAgeMode(r)

	items, total, err := a.History.ListHistory(r.Context(), user.ID, ageMode, page, pageSize)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items":     items,
		"total":     total,
		"age_mode":  ageMode,
		"enabled":   a.Cfg.Snapshot().HistoryEnabled,
	})
}

func (a *API) ListRecentHistory(w http.ResponseWriter, r *http.Request) {
	user := UserFromContext(r.Context())
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	ageMode := a.effectiveAgeMode(r)

	items, err := a.History.ListRecent(r.Context(), user.ID, ageMode, limit)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items":    items,
		"age_mode": ageMode,
		"enabled":  a.Cfg.Snapshot().HistoryEnabled,
	})
}

func (a *API) DeleteHistoryItem(w http.ResponseWriter, r *http.Request) {
	user := UserFromContext(r.Context())
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeErr(w, http.StatusBadRequest, "invalid history id")
		return
	}
	if err := a.History.DeleteEntry(r.Context(), user.ID, id); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (a *API) ClearHistory(w http.ResponseWriter, r *http.Request) {
	user := UserFromContext(r.Context())
	q := r.URL.Query()
	var opts history.ClearOptions
	opts.FromUnix, _ = strconv.ParseInt(q.Get("from"), 10, 64)
	opts.ToUnix, _ = strconv.ParseInt(q.Get("to"), 10, 64)
	if days, err := strconv.Atoi(q.Get("days")); err == nil && days > 0 {
		opts.FromUnix = time.Now().Unix() - int64(days)*86400
	}
	deleted, err := a.History.Clear(r.Context(), user.ID, opts)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"deleted":  deleted,
		"vacuumed": true,
	})
}

// --- Settings & Metadata Export/Import (§3.2, §9, §9.6) ---

func (a *API) GetSettings(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, a.Cfg.Snapshot())
}

func (a *API) UpdateSettings(w http.ResponseWriter, r *http.Request) {
	var incoming config.Config
	if err := decodeBody(r, &incoming); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON")
		return
	}

	// Validate proxy URL syntax before saving (§9)
	if strings.TrimSpace(incoming.ScraperProxy) != "" {
		if _, err := dlsite.BuildHTTPTransport(incoming.ScraperProxy); err != nil {
			writeErr(w, http.StatusBadRequest, "无效代理配置: "+err.Error())
			return
		}
	}

	// Prevent accidental lockout if enabling AuthEnabled without any password-configured account
	if incoming.AuthEnabled {
		var countWithPwd int
		_ = a.Store.DB().QueryRowContext(r.Context(), `SELECT COUNT(*) FROM app_user WHERE password_hash != ''`).Scan(&countWithPwd)
		if countWithPwd == 0 {
			writeErr(w, http.StatusBadRequest, "开启认证前请先在用户管理中创建带密码的管理员账号")
			return
		}
	}

	err := a.Cfg.Update(func(c *config.Config) {
		c.AuthEnabled = incoming.AuthEnabled
		c.DefaultHideNSFW = incoming.DefaultHideNSFW
		if incoming.ScraperLocale != "" {
			c.ScraperLocale = dlsite.FormatLocaleParam(incoming.ScraperLocale)
		}
		if incoming.ScraperIntervalSec >= 0.5 {
			c.ScraperIntervalSec = incoming.ScraperIntervalSec
		}
		c.ScraperProxy = strings.TrimSpace(incoming.ScraperProxy)
		c.AsmrOneEnabled = incoming.AsmrOneEnabled
		if incoming.AsmrOneIntervalSec > 0 {
			c.AsmrOneIntervalSec = incoming.AsmrOneIntervalSec
		}
		if len(incoming.AsmrOneBaseURLs) > 0 {
			c.AsmrOneBaseURLs = incoming.AsmrOneBaseURLs
		}
		if incoming.ScanMaxDepth > 0 {
			c.ScanMaxDepth = incoming.ScanMaxDepth
		}
		if len(incoming.ScanIgnorePatterns) > 0 {
			c.ScanIgnorePatterns = incoming.ScanIgnorePatterns
		}
		c.HistoryEnabled = incoming.HistoryEnabled
		if incoming.HistoryMaxDays >= 0 {
			c.HistoryMaxDays = incoming.HistoryMaxDays
		}
		if incoming.HistoryMaxEntries >= 0 {
			c.HistoryMaxEntries = incoming.HistoryMaxEntries
		}
		if incoming.HeartbeatIntervalSec >= 5 {
			c.HeartbeatIntervalSec = incoming.HeartbeatIntervalSec
		}
		c.WebDAVEnabled = incoming.WebDAVEnabled
	})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, a.Cfg.Snapshot())
}

type exportedWorkBundle struct {
	Metadata dlsite.Metadata `json:"metadata"`
	UserTags []string        `json:"user_tags,omitempty"`
	State    *db.UserState   `json:"state,omitempty"`
}

func (a *API) ExportMetadata(w http.ResponseWriter, r *http.Request) {
	user := UserFromContext(r.Context())
	works, _, err := a.Store.ListWorks(r.Context(), db.ListWorksOptions{
		UserID:   user.ID,
		Page:     1,
		PageSize: 100000,
		AgeMode:  "all",
	})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := make([]exportedWorkBundle, 0, len(works))
	for _, wk := range works {
		det, err := a.Store.GetWorkDetail(r.Context(), wk.Workno, user.ID)
		if err != nil {
			continue
		}
		var dlsiteTags, userTags []string
		for _, tg := range det.Tags {
			if tg.Source == "user" {
				userTags = append(userTags, tg.Name)
			} else {
				dlsiteTags = append(dlsiteTags, tg.Name)
			}
		}
		out = append(out, exportedWorkBundle{
			Metadata: dlsite.Metadata{
				Workno:      det.Workno,
				Title:       det.Title,
				CircleID:    det.CircleID,
				CircleName:  det.CircleName,
				ReleaseDate: det.ReleaseDate,
				AgeCategory: det.AgeCategory,
				WorkType:    det.WorkType,
				Price:       det.Price,
				Rating:      det.Rating,
				RateCount:   det.RateCount,
				ReviewCount: det.ReviewCount,
				DLCount:     det.DLCount,
				FileSize:    det.FileSize,
				SeriesID:    det.SeriesID,
				SeriesName:  det.SeriesName,
				CoverURL:    det.CoverURL,
				VAs:         det.VAs,
				Scenarios:   det.Scenarios,
				Illusts:     det.Illusts,
				Musics:      det.Musics,
				Tags:        dlsiteTags,
				FetchedAt:   det.ScrapedAt,
			},
			UserTags: userTags,
			State:    det.State,
		})
	}
	w.Header().Set("Content-Disposition", `attachment; filename="otoha-metadata-backup.json"`)
	writeJSON(w, http.StatusOK, out)
}

func (a *API) ImportMetadata(w http.ResponseWriter, r *http.Request) {
	user := UserFromContext(r.Context())
	var bundles []exportedWorkBundle
	if err := decodeBody(r, &bundles); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid metadata backup JSON")
		return
	}
	imported := 0
	for i := range bundles {
		b := &bundles[i]
		if dlsite.NormalizeWorkno(b.Metadata.Workno) == "" {
			continue
		}
		if err := a.Store.SaveMetadata(r.Context(), &b.Metadata, "", true); err != nil {
			continue
		}
		if len(b.UserTags) > 0 {
			_, _ = a.Store.SetWorkUserTags(r.Context(), b.Metadata.Workno, b.UserTags)
		}
		if b.State != nil {
			prog := b.State.Progress
			rev := b.State.Review
			_, _ = a.Store.UpsertUserState(r.Context(), user.ID, b.Metadata.Workno, b.State.Rating, &rev, &prog)
		}
		if doc, err := a.Store.GetSearchDocument(r.Context(), b.Metadata.Workno); err == nil {
			_ = a.Search.IndexWork(*doc)
		}
		imported++
	}
	writeJSON(w, http.StatusOK, map[string]int{"imported": imported})
}
