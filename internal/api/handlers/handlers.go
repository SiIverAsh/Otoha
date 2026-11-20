package handlers

import (
	"context"
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

	"github.com/SiIverAsh/otoha/internal/asmr18"
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
	TriggerAsmrOneSync(opts scrape.SyncOptions) (bool, error)
	TriggerASMR18Sync(opts scrape.ASMR18Options) (bool, error)
	Cancel() bool
	ServeSSE(w http.ResponseWriter, r *http.Request)
	SnapshotJSON() any
	SettingsTaskProgress() (*scrape.AsmrOneSyncCheckpoint, *asmr18.Work)
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

type loginRequest struct {
	Username    string `json:"username"`
	Password    string `json:"password"`
	NewPassword string `json:"new_password"`
}

type loginResponse struct {
	Token string   `json:"token"`
	User  *db.User `json:"user"`
}

type loginError struct {
	status        int
	message       string
	resetPassword bool
}

// Login authenticates a JSON API client and issues a session.
func (a *API) Login(w http.ResponseWriter, r *http.Request) {
	var req loginRequest
	if err := decodeBody(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	response, failure := a.login(w, r, req)
	if failure != nil {
		if failure.resetPassword {
			writeJSON(w, failure.status, struct {
				Error             string `json:"error"`
				MustResetPassword bool   `json:"must_reset_password"`
			}{Error: failure.message, MustResetPassword: true})
		} else {
			writeErr(w, failure.status, failure.message)
		}
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func (a *API) login(w http.ResponseWriter, r *http.Request, req loginRequest) (loginResponse, *loginError) {
	uname := strings.TrimSpace(req.Username)
	if uname == "" {
		return loginResponse{}, &loginError{status: http.StatusBadRequest, message: "username is required"}
	}

	u, err := a.Store.GetUserByUsername(r.Context(), uname)
	if err != nil {
		return loginResponse{}, &loginError{status: http.StatusUnauthorized, message: "用户名或密码错误"}
	}

	// §14.4: Migrated kikoeru user has empty password_hash & must_reset_password = true
	if u.MustResetPassword || u.PasswordHash == "" {
		targetPwd := req.NewPassword
		if targetPwd == "" {
			targetPwd = req.Password
		}
		if len(targetPwd) < 4 {
			return loginResponse{}, &loginError{
				status:        http.StatusForbidden,
				message:       "该账号需要首次重设密码（至少 4 位）",
				resetPassword: true,
			}
		}
		hash, err := a.Auth.HashPassword(targetPwd)
		if err != nil {
			return loginResponse{}, &loginError{status: http.StatusBadRequest, message: err.Error()}
		}
		if err := a.Store.UpdateUserPassword(r.Context(), u.ID, hash); err != nil {
			return loginResponse{}, &loginError{status: http.StatusInternalServerError, message: err.Error()}
		}
		u.MustResetPassword = false
	} else if !a.Auth.VerifyPassword(u.PasswordHash, req.Password) {
		return loginResponse{}, &loginError{status: http.StatusUnauthorized, message: "用户名或密码错误"}
	}

	tok, err := a.Auth.IssueSessionCookie(w, r, u.ID)
	if err != nil {
		return loginResponse{}, &loginError{status: http.StatusInternalServerError, message: err.Error()}
	}
	return loginResponse{Token: tok, User: u}, nil
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

func requestedWorkSource(w http.ResponseWriter, r *http.Request) (db.WorkSource, bool) {
	source, valid := parseWorkSource(r.URL.Query().Get("source"))
	if !valid {
		writeErr(w, http.StatusBadRequest, "source must be all, asmrone, or asmr18")
	}
	return source, valid
}

func parseWorkSource(value string) (db.WorkSource, bool) {
	switch source := db.WorkSource(value); source {
	case db.AllWorkSources, "all":
		return db.AllWorkSources, true
	case db.ASMRoneSource, db.ASMR18Source:
		return source, true
	default:
		return db.AllWorkSources, false
	}
}

func (a *API) listWorkOptions(r *http.Request, source db.WorkSource) db.ListWorksOptions {
	q := r.URL.Query()
	page, _ := strconv.Atoi(q.Get("page"))
	pageSize, _ := strconv.Atoi(q.Get("page_size"))
	storageID, _ := strconv.ParseInt(q.Get("storage_id"), 10, 64)
	sortDesc := q.Get("sort_desc") != "false" && q.Get("sort_desc") != "0"
	return db.ListWorksOptions{
		Page:         page,
		PageSize:     pageSize,
		SortBy:       q.Get("sort_by"),
		SortDesc:     sortDesc,
		AgeMode:      a.effectiveAgeMode(r),
		Source:       source,
		Circle:       q.Get("circle"),
		VA:           q.Get("va"),
		Tag:          q.Get("tag"),
		Progress:     q.Get("progress"),
		StorageID:    storageID,
		SubtitleOnly: q.Get("subtitle_only") == "1" || q.Get("subtitle_only") == "true",
	}
}

func (a *API) ListWorks(w http.ResponseWriter, r *http.Request) {
	source, valid := requestedWorkSource(w, r)
	if !valid {
		return
	}
	opts := a.listWorkOptions(r, source)
	opts.UserID = UserFromContext(r.Context()).ID
	items, total, err := a.Store.ListWorks(r.Context(), opts)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"items":     items,
		"total":     total,
		"page":      max(1, opts.Page),
		"page_size": max(1, opts.PageSize),
		"age_mode":  opts.AgeMode,
	})
}

func (a *API) ListFacets(w http.ResponseWriter, r *http.Request) {
	source, valid := requestedWorkSource(w, r)
	if !valid {
		return
	}
	ageMode := a.effectiveAgeMode(r)
	circles, vas, tags, err := a.Store.ListFacets(r.Context(), ageMode, source)
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

// GetWork returns work detail, playback positions and the track tree as JSON.
func (a *API) GetWork(w http.ResponseWriter, r *http.Request) {
	detail, err := a.loadWorkDetail(r)
	if errors.Is(err, db.ErrNotFound) {
		writeErr(w, http.StatusNotFound, "work not found")
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, detail)
}

func (a *API) loadWorkDetail(r *http.Request) (*db.WorkDetail, error) {
	ctx := r.Context()
	user := UserFromContext(ctx)
	workno := r.PathValue("workno")
	detail, err := a.Store.GetWorkDetail(ctx, workno, user.ID)
	if err != nil {
		return nil, err
	}

	// Lazy-load remote tracks from asmr.one on first open if work has 0 local/cached tracks
	if len(detail.Tracks) == 0 && a.Cfg.Snapshot().AsmrOneEnabled && r.URL.Query().Get("skip_remote_tracks") != "1" {
		if asmrClient, cErr := scrape.NewAsmrOneClient(a.Cfg); cErr == nil {
			if n, trErr := scrape.SyncWorkRemoteTracks(ctx, a.Store, asmrClient, detail.Workno, detail.AsmrOneID); trErr == nil && n > 0 {
				if refreshed, rErr := a.Store.GetWorkDetail(ctx, workno, user.ID); rErr == nil {
					detail = refreshed
				}
			}
		}
	}

	// Annotate HasSubtitle on local tracks & tree nodes (§8)
	storages, _ := a.Store.ListStorages(ctx)
	rootByID := make(map[int64]string, len(storages))
	for _, st := range storages {
		if st.Kind == "local" {
			rootByID[st.ID] = st.Root
		}
	}
	hasSubByTrackID := make(map[int64]bool, len(detail.Tracks))
	for i := range detail.Tracks {
		tr := &detail.Tracks[i]
		if tr.IsRemote {
			tr.HasSubtitle = tr.SubtitleURL != ""
			hasSubByTrackID[tr.ID] = tr.HasSubtitle
			continue
		}
		if stRoot := rootByID[tr.StorageID]; stRoot != "" {
			absAudio := filepath.Join(stRoot, filepath.FromSlash(tr.WorkRel), filepath.FromSlash(tr.RelPath))
			tr.HasSubtitle = subtitle.FindMatchingSubtitle(absAudio) != ""
			hasSubByTrackID[tr.ID] = tr.HasSubtitle
		}
	}
	annotateTreeSubtitles(detail.Tree, hasSubByTrackID)

	return detail, nil
}

func (a *API) SyncWorkTracks(w http.ResponseWriter, r *http.Request) {
	user := UserFromContext(r.Context())
	workno := dlsite.NormalizeWorkno(r.PathValue("workno"))
	if workno == "" {
		writeErr(w, http.StatusBadRequest, "invalid workno")
		return
	}
	detail, err := a.Store.GetWorkDetail(r.Context(), workno, user.ID)
	if err != nil {
		writeErr(w, http.StatusNotFound, "work not found")
		return
	}
	asmrClient, err := scrape.NewAsmrOneClient(a.Cfg)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if _, err := scrape.SyncWorkRemoteTracks(r.Context(), a.Store, asmrClient, workno, detail.AsmrOneID); err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	refreshed, _ := a.Store.GetWorkDetail(r.Context(), workno, user.ID)
	writeJSON(w, http.StatusOK, refreshed)
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
	qStr := strings.TrimSpace(r.URL.Query().Get("q"))
	if qStr == "" {
		a.ListWorks(w, r)
		return
	}

	source, valid := requestedWorkSource(w, r)
	if !valid {
		return
	}
	opts := a.listWorkOptions(r, source)
	opts.UserID = UserFromContext(r.Context()).ID
	items, total, result, err := a.searchWorks(r.Context(), qStr, opts)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	response := map[string]any{
		"items":         items,
		"total":         total,
		"degraded":      result.Degraded,
		"needs_reindex": result.NeedsReindex,
		"age_mode":      opts.AgeMode,
	}
	if result.Degraded {
		response["warning"] = result.Warning
	}
	writeJSON(w, http.StatusOK, response)
}

func (a *API) searchWorks(ctx context.Context, query string, opts db.ListWorksOptions) ([]db.Work, int, *search.SearchResult, error) {
	var worknos []string
	if opts.Source != db.AllWorkSources {
		var err error
		worknos, err = a.Store.ListMatchingWorknos(ctx, opts)
		if err != nil {
			return nil, 0, nil, err
		}
	}
	result, err := a.Search.SearchWithinWorks(ctx, query, 500, worknos)
	if err != nil {
		return nil, 0, nil, err
	}

	if result.Degraded {
		opts.QueryLike = query
	} else {
		opts.Worknos = result.Worknos
	}
	items, total, err := a.Store.ListWorks(ctx, opts)
	if err != nil {
		return nil, 0, nil, err
	}
	return items, total, result, nil
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
	if rt.IsRemote && rt.StreamURL != "" {
		if rt.StorageKind == "asmr18" {
			_ = stream.ProxyHLS(w, r, rt.StreamURL, a.Cfg.Snapshot().ScraperProxy)
			return
		}
		_ = stream.ProxyRemoteAudio(w, r, rt.StreamURL, rt.RelPath, a.Cfg.Snapshot().ScraperProxy)
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
	// On-demand remote cover fetch & local disk cache for synced catalog works
	if err == nil {
		remoteCover := detail.CoverURL
		if r.URL.Query().Get("thumb") == "1" && detail.ThumbnailCoverURL != "" {
			remoteCover = detail.ThumbnailCoverURL
		}
		if remoteCover != "" {
			if client, cErr := scrape.NewClient(a.Cfg, nil, time.Millisecond); cErr == nil {
				if savedPath, dErr := client.DownloadCover(r.Context(), workno, remoteCover); dErr == nil && savedPath != "" {
					_, _ = a.Store.DB().ExecContext(r.Context(), `UPDATE work SET cover_path = ? WHERE workno = ? AND COALESCE(cover_path, '') = ''`, savedPath, workno)
					http.ServeFile(w, r, savedPath)
					return
				}
			}
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
	if rt.IsRemote {
		if rt.SubtitleURL == "" {
			writeJSON(w, http.StatusOK, map[string]any{"found": false, "cues": []subtitle.Cue{}})
			return
		}
		raw, fErr := fetchRemoteBytes(r, rt.SubtitleURL, a.Cfg.Snapshot().ScraperProxy)
		if fErr != nil {
			writeErr(w, http.StatusBadGateway, fErr.Error())
			return
		}
		ext := strings.TrimPrefix(strings.ToLower(filepath.Ext(strings.Split(rt.SubtitleURL, "?")[0])), ".")
		if ext == "" {
			ext = "lrc"
		}
		parsed := subtitle.ParseBytes(raw, filepath.Base(rt.SubtitleURL), ext)
		writeJSON(w, http.StatusOK, map[string]any{
			"found":    true,
			"format":   parsed.Format,
			"encoding": parsed.Encoding,
			"file":     parsed.FileName,
			"cues":     parsed.Cues,
		})
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

func fetchRemoteBytes(r *http.Request, targetURL, proxyURL string) ([]byte, error) {
	tr, err := dlsite.BuildHTTPTransport(proxyURL)
	if err != nil {
		return nil, err
	}
	client := &http.Client{Transport: tr, Timeout: 20 * time.Second}
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, targetURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; Otoha/1.0; +https://github.com/SiIverAsh/Otoha)")
	req.Header.Set("Referer", "https://asmr.one/")
	req.Header.Set("Origin", "https://asmr.one")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("upstream HTTP %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 8<<20))
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

func (a *API) TriggerAsmrOneSync(w http.ResponseWriter, r *http.Request) {
	var req struct {
		PageSize        int    `json:"page_size"`
		StartPage       int    `json:"start_page"`
		MaxPages        int    `json:"max_pages"`
		Order           string `json:"order"`
		Sort            string `json:"sort"`
		SubtitleOnly    bool   `json:"subtitle_only"`
		IncludeTracks   bool   `json:"include_tracks"`
		ResetCheckpoint bool   `json:"reset_checkpoint"`
	}
	_ = decodeBody(r, &req)
	started, err := a.Tasks.TriggerAsmrOneSync(scrape.SyncOptions{
		PageSize:        req.PageSize,
		StartPage:       req.StartPage,
		MaxPages:        req.MaxPages,
		Order:           req.Order,
		Sort:            req.Sort,
		SubtitleOnly:    req.SubtitleOnly,
		IncludeTracks:   req.IncludeTracks,
		ResetCheckpoint: req.ResetCheckpoint,
	})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	cp, _ := scrape.LoadSyncCheckpoint(a.Cfg)
	writeJSON(w, http.StatusOK, map[string]any{
		"started":    started,
		"task":       a.Tasks.SnapshotJSON(),
		"checkpoint": cp,
	})
}

func (a *API) GetAsmrOneSyncStatus(w http.ResponseWriter, r *http.Request) {
	cp, _ := scrape.LoadSyncCheckpoint(a.Cfg)
	writeJSON(w, http.StatusOK, map[string]any{
		"checkpoint": cp,
		"task":       a.Tasks.SnapshotJSON(),
	})
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
		"items":    items,
		"total":    total,
		"age_mode": ageMode,
		"enabled":  a.Cfg.Snapshot().HistoryEnabled,
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
