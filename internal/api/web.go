package api

import (
	"bytes"
	"log"
	"net/http"
	"net/url"

	"github.com/SiIverAsh/otoha/internal/api/handlers"
	"github.com/SiIverAsh/otoha/internal/db"
	"github.com/SiIverAsh/otoha/web"
	"github.com/SiIverAsh/otoha/web/views"

	"github.com/a-h/templ"
)

type webPages struct {
	api    *handlers.API
	logger *log.Logger
}

func webRouter(deps Dependencies, api *handlers.API) http.Handler {
	logger := deps.Logger
	if logger == nil {
		logger = log.Default()
	}
	pages := webPages{api: api, logger: logger}

	mux := http.NewServeMux()
	mux.Handle("/assets/", web.Assets())
	mux.HandleFunc("GET /login", pages.loginPage)
	mux.HandleFunc("POST /login", pages.login)
	mux.Handle("GET /{$}", pages.requireUser(pages.page("works")))
	mux.Handle("GET /people", pages.requireUser(pages.page("people")))
	mux.Handle("GET /tags", pages.requireUser(pages.page("tags")))
	mux.Handle("GET /favorites", pages.requireUser(pages.page("favorites")))
	mux.Handle("GET /playlists", pages.requireUser(pages.page("playlists")))
	mux.Handle("GET /history", pages.requireUser(pages.page("history")))
	mux.Handle("GET /settings", pages.requireUser(pages.page("settings")))
	mux.Handle("GET /ui/works/{workno}", pages.requireUser(api.WorkDrawer))
	mux.Handle("DELETE /ui/history", pages.requireUser(api.DeleteWebHistory))
	mux.Handle("DELETE /ui/history/{id}", pages.requireUser(api.DeleteWebHistory))
	mux.Handle("GET /ui/settings/task", pages.requireUser(api.WebSettingsTask))
	return http.NewCrossOriginProtection().Handler(mux)
}

func webUser(r *http.Request, api *handlers.API) *db.User {
	if !api.Cfg.Snapshot().AuthEnabled {
		return &db.User{ID: 1, Username: "default", IsAdmin: true}
	}
	cookie, err := r.Cookie(SessionCookieName)
	if err != nil || cookie.Value == "" {
		return nil
	}
	user, err := api.Store.GetUserBySession(r.Context(), cookie.Value)
	if err != nil {
		return nil
	}
	return user
}

func isHTMX(r *http.Request) bool {
	return r.Header.Get("HX-Request") == "true"
}

func redirectWeb(w http.ResponseWriter, r *http.Request, destination string) {
	if isHTMX(r) {
		w.Header().Set("HX-Redirect", destination)
		w.WriteHeader(http.StatusOK)
		return
	}
	http.Redirect(w, r, destination, http.StatusSeeOther)
}

func (pages webPages) requireUser(next http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		user := webUser(r, pages.api)
		if user == nil {
			redirectWeb(w, r, "/login?"+url.Values{"next": {handlers.SafeWebNext(r.URL.RequestURI())}}.Encode())
			return
		}
		next(w, r.WithContext(handlers.WithUser(r.Context(), user)))
	})
}

func (pages webPages) page(tab string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Vary", "HX-Request, HX-History-Restore-Request")
		var content templ.Component
		var err error
		switch tab {
		case "works", "people", "tags":
			content, err = pages.api.CatalogPage(r, tab)
		case "history", "settings":
			content, err = pages.api.ManagementPage(r, tab)
		case "favorites", "playlists":
			content = views.BrowserLibrary()
		}

		status := http.StatusOK
		if err != nil {
			pages.logger.Printf("render %s page: %v", tab, err)
			content = views.ErrorMessage("无法加载页面")
			status = http.StatusInternalServerError
		}

		snapshot := pages.api.Cfg.Snapshot()
		shell := views.Shell{
			Tab:               tab,
			Query:             pages.api.CatalogQuery(r),
			User:              UserFromContext(r.Context()),
			AuthEnabled:       snapshot.AuthEnabled,
			HeartbeatInterval: snapshot.HeartbeatIntervalSec,
		}
		if isHTMX(r) && r.Header.Get("HX-History-Restore-Request") != "true" {
			if r.Header.Get("X-Otoha-Navigate") == "1" {
				w.Header().Set("HX-Push-Url", r.URL.RequestURI())
			}
			pages.render(w, r, status, views.PageFragment(shell, content))
			return
		}
		pages.render(w, r, status, views.Document(views.Workspace(shell, content)))
	}
}

func (pages webPages) loginPage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	next := handlers.SafeWebNext(r.URL.Query().Get("next"))
	if webUser(r, pages.api) != nil {
		redirectWeb(w, r, next)
		return
	}
	pages.render(w, r, http.StatusOK, views.Document(views.Login(next, "", "", false)))
}

func (pages webPages) login(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	result := pages.api.AuthenticateWeb(w, r)
	if result.Status == http.StatusOK {
		redirectWeb(w, r, result.Next)
		return
	}
	content := views.Login(result.Next, result.Username, result.Message, result.ResetPassword)
	status := result.Status
	if !isHTMX(r) {
		content = views.Document(content)
	} else if status >= 400 && status < 500 {
		status = http.StatusUnprocessableEntity
	}
	pages.render(w, r, status, content)
}

func (pages webPages) render(w http.ResponseWriter, r *http.Request, status int, component templ.Component) {
	var body bytes.Buffer
	if err := component.Render(r.Context(), &body); err != nil {
		pages.logger.Printf("render HTML: %v", err)
		http.Error(w, "无法渲染页面", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	if _, err := body.WriteTo(w); err != nil && r.Context().Err() == nil {
		pages.logger.Printf("write HTML response: %v", err)
	}
}
