package gui

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/yetone/magpie/internal/agent"
)

// ompAdvisorRoutes are the omp row's Advisor (#5019): its settings at the
// user's level and a project's, what is in effect, and a project folder
// from the system's picker (agent/omp_advisor.go).
func ompAdvisorRoutes(mux *http.ServeMux, w Windows) {
	mux.HandleFunc("GET /api/agents/omp-advisor/{id}", func(rw http.ResponseWriter, r *http.Request) {
		a, err := agent.OmpAdvisorOf(r.PathValue("id"))
		if err != nil {
			ompAdvisorFail(rw, err)
			return
		}
		v, err := a.OmpAdvisorView(r.URL.Query().Get("project"))
		if err != nil {
			ompAdvisorFail(rw, err)
			return
		}
		writeJSON(rw, v)
	})
	mux.HandleFunc("POST /api/agents/omp-advisor/{id}", func(rw http.ResponseWriter, r *http.Request) {
		a, err := agent.OmpAdvisorOf(r.PathValue("id"))
		if err != nil {
			ompAdvisorFail(rw, err)
			return
		}
		var ch agent.OmpAdvisorChange
		dec := json.NewDecoder(r.Body)
		dec.DisallowUnknownFields()
		if err := dec.Decode(&ch); err != nil {
			ompAdvisorFail(rw, err)
			return
		}
		v, err := a.OmpAdvisorApply(ch)
		if err != nil {
			ompAdvisorFail(rw, err)
			return
		}
		writeJSON(rw, v)
	})
	// a project folder picked in the system's picker, as the agent spells
	// it; "" when the picker was cancelled
	mux.HandleFunc("POST /api/agents/omp-advisor/{id}/choose", func(rw http.ResponseWriter, r *http.Request) {
		a, err := agent.OmpAdvisorOf(r.PathValue("id"))
		if err != nil {
			ompAdvisorFail(rw, err)
			return
		}
		dir, err := w.ChooseFolder("Choose a project folder")
		if err != nil {
			ompAdvisorFail(rw, err)
			return
		}
		if dir != "" {
			dir = a.OmpAdvisorFolder(dir)
		}
		writeJSON(rw, map[string]string{"dir": dir})
	})
}

// ompAdvisorFail is fail for the Advisor's routes: what the agent said in
// words the window translates (agent.OmpSay) goes with the error as say,
// and a save over files changed since the window read them is a 409.
func ompAdvisorFail(rw http.ResponseWriter, err error) {
	var say agent.OmpSay
	if !errors.As(err, &say) {
		fail(rw, err)
		return
	}
	rw.Header().Set("Content-Type", "application/json")
	if say.Stale {
		rw.WriteHeader(http.StatusConflict)
	} else {
		rw.WriteHeader(http.StatusBadRequest)
	}
	json.NewEncoder(rw).Encode(map[string]any{"error": say.Error(), "say": say})
}
