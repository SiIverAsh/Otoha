package api

import (
	"io"
	"io/fs"
	"log"
	"mime"
	"net/http"
	"path"
	"path/filepath"
	"strings"

	"github.com/SiIverAsh/otoha/internal/api/handlers"
	"github.com/SiIverAsh/otoha/internal/config"
	"github.com/SiIverAsh/otoha/internal/db"
	"github.com/SiIverAsh/otoha/internal/history"
	"github.com/SiIverAsh/otoha/internal/search"
	"github.com/SiIverAsh/otoha/internal/storage"
)

// Dependencies bundles runtime services required by the HTTP server.
type Dependencies struct {
	Store    *db.Store
	Search   *search.Engine
	History  *history.Service
	Cfg      *config.Config
	Tasks    *TaskManager
	Logger   *log.Logger
	Frontend fs.FS
}

// NewRouter constructs the top-level Go 1.22+ HTTP handler for `/api/*`, `/dav/*`, and embedded SPA (§2 & §9).
func NewRouter(deps Dependencies) http.Handler {
	if deps.History == nil {
		deps.History = history.NewService(deps.Store, deps.Cfg)
	}
	if deps.Tasks == nil {
		deps.Tasks = NewTaskManager(deps.Store, deps.Search, deps.Cfg, deps.Logger)
	}

	h := &handlers.API{
		Store:   deps.Store,
		Search:  deps.Search,
		History: deps.History,
		Cfg:     deps.Cfg,
		Tasks:   deps.Tasks,
		Auth: handlers.AuthCallbacks{
			HashPassword:   HashPassword,
			VerifyPassword: VerifyPassword,
			IssueSessionCookie: func(w http.ResponseWriter, r *http.Request, userID int64) (string, error) {
				return IssueSessionCookie(r.Context(), w, deps.Store, userID)
			},
			CookieName: SessionCookieName,
		},
	}

	apiMux := http.NewServeMux()
	apiMux.HandleFunc("GET /api/users", h.ListUsers)
	apiMux.HandleFunc("POST /api/users", h.UpsertUser)

	apiMux.HandleFunc("GET /api/works", h.ListWorks)
	apiMux.HandleFunc("GET /api/works/facets", h.ListFacets)
	apiMux.HandleFunc("GET /api/works/{workno}", h.GetWork)
	apiMux.HandleFunc("PUT /api/works/{workno}/metadata", h.UpdateWorkMetadata)
	apiMux.HandleFunc("POST /api/works/{workno}/scrape", h.ScrapeSingleWork)
	apiMux.HandleFunc("POST /api/works/{workno}/sync-tracks", h.SyncWorkTracks)
	apiMux.HandleFunc("POST /api/works/{workno}/state", h.UpdateWorkState)
	apiMux.HandleFunc("POST /api/works/{workno}/tags", h.UpdateWorkTags)

	apiMux.HandleFunc("POST /api/asmrone/sync", h.TriggerAsmrOneSync)
	apiMux.HandleFunc("GET /api/asmrone/status", h.GetAsmrOneSyncStatus)

	apiMux.HandleFunc("GET /api/search", h.SearchWorks)
	apiMux.HandleFunc("POST /api/search/reindex", h.Reindex)

	apiMux.HandleFunc("GET /api/stream/{trackId}", h.StreamTrack)
	apiMux.HandleFunc("GET /api/cover/{workno}", h.GetCover)
	apiMux.HandleFunc("GET /api/subtitle/{trackId}", h.GetSubtitle)

	apiMux.HandleFunc("GET /api/storages", h.ListStorages)
	apiMux.HandleFunc("POST /api/storages", h.CreateStorage)
	apiMux.HandleFunc("DELETE /api/storages/{id}", h.DeleteStorage)

	apiMux.HandleFunc("POST /api/scan", h.TriggerScan)
	apiMux.HandleFunc("POST /api/tasks/cancel", h.CancelTask)
	apiMux.HandleFunc("GET /api/tasks", h.GetTasks)
	apiMux.HandleFunc("GET /api/tasks/events", h.StreamTaskEvents)

	apiMux.HandleFunc("POST /api/history/heartbeat", h.PostHeartbeat)
	apiMux.HandleFunc("GET /api/history", h.ListHistory)
	apiMux.HandleFunc("GET /api/history/recent", h.ListRecentHistory)
	apiMux.HandleFunc("DELETE /api/history/{id}", h.DeleteHistoryItem)
	apiMux.HandleFunc("DELETE /api/history", h.ClearHistory)

	apiMux.HandleFunc("GET /api/settings", h.GetSettings)
	apiMux.HandleFunc("PUT /api/settings", h.UpdateSettings)
	apiMux.HandleFunc("GET /api/export/metadata", h.ExportMetadata)
	apiMux.HandleFunc("POST /api/import/metadata", h.ImportMetadata)

	protectedAPI := AuthMiddleware(deps.Store, deps.Cfg, false, apiMux)

	davInner := storage.NewWebDAVHandler("/dav", deps.Store)
	davGuarded := AuthMiddleware(deps.Store, deps.Cfg, true, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !deps.Cfg.Snapshot().WebDAVEnabled {
			http.Error(w, "WebDAV disabled in settings", http.StatusNotFound)
			return
		}
		davInner.ServeHTTP(w, r)
	}))

	rootMux := http.NewServeMux()
	// Public auth bootstrap routes
	rootMux.HandleFunc("GET /api/auth/me", h.GetMe)
	rootMux.HandleFunc("POST /api/auth/login", h.Login)
	rootMux.HandleFunc("POST /api/auth/logout", h.Logout)

	// Protected API
	rootMux.Handle("/api/", protectedAPI)

	// Read-only WebDAV (§9)
	rootMux.HandleFunc("/dav", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/dav/", http.StatusMovedPermanently)
	})
	rootMux.Handle("/dav/", davGuarded)

	// Embedded Frontend SPA (§2)
	if deps.Frontend != nil {
		rootMux.Handle("/", serveEmbeddedSPA(deps.Frontend))
	} else {
		rootMux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			_, _ = io.WriteString(w, "Otoha server running")
		})
	}

	return rootMux
}

func serveEmbeddedSPA(distFS fs.FS) http.Handler {
	fileServer := http.FileServer(http.FS(distFS))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		clean := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if clean == "" || clean == "." {
			clean = "index.html"
		}
		if f, err := distFS.Open(clean); err == nil {
			_ = f.Close()
			if ext := filepath.Ext(clean); ext != "" {
				if ct := mime.TypeByExtension(ext); ct != "" {
					w.Header().Set("Content-Type", ct)
				}
			}
			fileServer.ServeHTTP(w, r)
			return
		}
		// Fallback to index.html for SPA routes
		raw, err := fs.ReadFile(distFS, "index.html")
		if err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(raw)
	})
}
