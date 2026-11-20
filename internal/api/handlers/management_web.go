package handlers

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/a-h/templ"

	"github.com/SiIverAsh/otoha/internal/db"
	"github.com/SiIverAsh/otoha/internal/history"
	"github.com/SiIverAsh/otoha/internal/scrape"
	"github.com/SiIverAsh/otoha/web/views"
)

const (
	loginFormMaxBytes = 4096
	webHistoryLimit   = 80
)

// WebLoginResult carries the login status and the form values needed for rendering.
type WebLoginResult struct {
	Status        int
	Username      string
	Message       string
	ResetPassword bool
	Next          string
}

// AuthenticateWeb signs in a browser using the same session logic as the JSON API.
func (a *API) AuthenticateWeb(w http.ResponseWriter, r *http.Request) WebLoginResult {
	r.Body = http.MaxBytesReader(w, r.Body, loginFormMaxBytes)
	if err := r.ParseForm(); err != nil {
		return WebLoginResult{Status: http.StatusBadRequest, Message: "无法读取登录表单", Next: "/"}
	}
	result := WebLoginResult{
		Status:   http.StatusOK,
		Username: strings.TrimSpace(r.PostForm.Get("username")),
		Next:     SafeWebNext(r.PostForm.Get("next")),
	}
	_, failure := a.login(w, r, loginRequest{Username: result.Username, Password: r.PostForm.Get("password")})
	if failure == nil {
		return result
	}
	result.Status = failure.status
	result.Message = failure.message
	result.ResetPassword = failure.resetPassword
	if failure.status >= http.StatusInternalServerError {
		slog.ErrorContext(r.Context(), "could not sign in", "error", failure.message)
		result.Message = "登录暂时不可用，请稍后重试"
	}
	return result
}

// SafeWebNext limits post-login redirects to Otoha's document routes.
func SafeWebNext(candidate string) string {
	parsed, err := url.ParseRequestURI(candidate)
	if err != nil || parsed.IsAbs() || parsed.Host != "" {
		return "/"
	}
	switch parsed.Path {
	case "/", "/people", "/tags", "/favorites", "/playlists", "/history", "/settings":
		query := parsed.Query()
		query.Del("token")
		return (&url.URL{Path: parsed.Path, RawQuery: query.Encode()}).RequestURI()
	default:
		return "/"
	}
}

// ManagementPage provides the account's history or the current server settings.
func (a *API) ManagementPage(r *http.Request, tab string) (templ.Component, error) {
	switch tab {
	case "history":
		return a.webHistory(r)
	case "settings":
		return a.webSettings(r)
	default:
		return nil, fmt.Errorf("unknown management page %q", tab)
	}
}

func (a *API) webHistory(r *http.Request) (templ.Component, error) {
	user := UserFromContext(r.Context())
	ageMode := a.effectiveAgeMode(r)
	entries, _, err := a.History.ListHistory(r.Context(), user.ID, ageMode, 1, webHistoryLimit)
	if err != nil {
		return nil, fmt.Errorf("list history entries: %w", err)
	}
	rows := make([]views.HistoryRow, 0, len(entries))
	for _, entry := range entries {
		resumeJSON, err := json.Marshal(entry)
		if err != nil {
			return nil, fmt.Errorf("encode history entry %d: %w", entry.ID, err)
		}
		rows = append(rows, views.HistoryRow{Entry: entry, ResumeJSON: string(resumeJSON)})
	}
	return views.History(ageMode, rows), nil
}

// DeleteWebHistory deletes the requested history and renders the refreshed list.
func (a *API) DeleteWebHistory(w http.ResponseWriter, r *http.Request) {
	user := UserFromContext(r.Context())
	ctx := r.Context()
	var err error
	if value := r.PathValue("id"); value != "" {
		id, parseErr := strconv.ParseInt(value, 10, 64)
		if parseErr != nil || id <= 0 {
			http.Error(w, "无效的收听记录编号", http.StatusBadRequest)
			return
		}
		err = a.History.DeleteEntry(ctx, user.ID, id)
	} else {
		var options history.ClearOptions
		query, parseErr := url.ParseQuery(r.URL.RawQuery)
		if parseErr != nil {
			http.Error(w, "无效的历史清理参数", http.StatusBadRequest)
			return
		}
		if values, ok := query["days"]; ok {
			if len(values) != 1 {
				http.Error(w, "无效的历史清理天数", http.StatusBadRequest)
				return
			}
			days, parseErr := strconv.ParseInt(values[0], 10, 16)
			if parseErr != nil || days <= 0 {
				http.Error(w, "无效的历史清理天数", http.StatusBadRequest)
				return
			}
			options.FromUnix = time.Now().Unix() - days*int64(24*time.Hour/time.Second)
		}
		_, err = a.History.Clear(ctx, user.ID, options)
	}
	if err != nil {
		slog.ErrorContext(ctx, "could not remove listening history", "error", err)
		http.Error(w, "无法清理收听历史", http.StatusInternalServerError)
		return
	}
	page, err := a.webHistory(r)
	if err != nil {
		slog.ErrorContext(ctx, "could not load listening history", "error", err)
		http.Error(w, "无法加载收听历史", http.StatusInternalServerError)
		return
	}
	templ.Handler(page).ServeHTTP(w, r)
}

func (a *API) webSettings(r *http.Request) (templ.Component, error) {
	ctx := r.Context()
	storages, err := a.Store.ListStorages(ctx)
	if err != nil {
		return nil, fmt.Errorf("list storage directories: %w", err)
	}
	user := UserFromContext(ctx)
	var users []db.User
	if user.IsAdmin {
		users, err = a.Store.ListUsers(ctx)
		if err != nil {
			return nil, fmt.Errorf("list user accounts: %w", err)
		}
	}
	task, err := a.settingsTask(r)
	if err != nil {
		return nil, err
	}
	return views.Settings(views.SettingsPage{
		Config:   a.Cfg.Snapshot(),
		Storages: storages,
		Users:    users,
		CanAdmin: user.IsAdmin,
		Task:     task,
	}), nil
}

// WebSettingsTask refreshes the status panels without replacing settings inputs.
func (a *API) WebSettingsTask(w http.ResponseWriter, r *http.Request) {
	task, err := a.settingsTask(r)
	if err != nil {
		slog.ErrorContext(r.Context(), "could not load sync progress", "error", err)
		http.Error(w, "无法加载同步进度", http.StatusInternalServerError)
		return
	}
	templ.Handler(views.SettingsTaskUpdate(task)).ServeHTTP(w, r)
}

func (a *API) settingsTask(r *http.Request) (views.SettingsTask, error) {
	checkpoint, preview := a.Tasks.SettingsTaskProgress()
	var task views.SettingsTask
	if checkpoint == nil {
		var err error
		checkpoint, err = scrape.LoadSyncCheckpoint(a.Cfg)
		if err != nil {
			slog.WarnContext(r.Context(), "could not read ASMR.one sync checkpoint", "error", err)
			task.CheckpointUnavailable = true
		}
	}
	task.Checkpoint = checkpoint
	if preview != nil {
		payload, err := json.MarshalIndent(preview, "", "  ")
		if err != nil {
			return views.SettingsTask{}, fmt.Errorf("encode ASMR18 preview: %w", err)
		}
		task.PreviewTitle = preview.Metadata.Title
		task.PreviewWorkno = preview.Metadata.Workno
		task.PreviewJSON = string(payload)
	}
	return task, nil
}
