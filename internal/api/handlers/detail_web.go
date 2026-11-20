package handlers

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/SiIverAsh/otoha/internal/db"
	"github.com/SiIverAsh/otoha/web/views"
)

// WorkDrawer renders the authenticated user's work detail and track tree as HTML.
func (a *API) WorkDrawer(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	detail, err := a.loadWorkDetail(r)
	if errors.Is(err, db.ErrNotFound) {
		http.Error(w, "未找到作品", http.StatusNotFound)
		return
	}
	if err != nil {
		slog.Error("load work drawer", "workno", r.PathValue("workno"), "error", err)
		http.Error(w, "无法加载作品详情", http.StatusInternalServerError)
		return
	}

	payload, err := json.Marshal(detail)
	if err != nil {
		slog.Error("encode work drawer playback data", "workno", detail.Workno, "error", err)
		http.Error(w, "无法加载作品详情", http.StatusInternalServerError)
		return
	}

	var output bytes.Buffer
	edit := r.URL.Query().Get("edit") == "1"
	if err := views.WorkDrawer(detail, string(payload), edit).Render(r.Context(), &output); err != nil {
		slog.Error("render work drawer", "workno", detail.Workno, "error", err)
		http.Error(w, "无法加载作品详情", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if _, err := output.WriteTo(w); err != nil {
		slog.Debug("write work drawer", "error", err)
	}
}
