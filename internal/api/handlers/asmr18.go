package handlers

import (
	"github.com/SiIverAsh/otoha/internal/scrape"
	"net/http"
)

func (a *API) TriggerASMR18Sync(w http.ResponseWriter, r *http.Request) {
	if !UserFromContext(r.Context()).IsAdmin {
		writeErr(w, 403, "admin required")
		return
	}
	opts := scrape.ASMR18Options{Category: "allages", MaxPages: 1, MaxWorks: 24}
	if err := decodeBody(r, &opts); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	if err := opts.Validate(); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	started, err := a.Tasks.TriggerASMR18Sync(opts)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	if !started {
		writeErr(w, 409, "another task is running")
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]bool{"started": true})
}
