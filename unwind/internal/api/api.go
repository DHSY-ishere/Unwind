// Package api exposes the Unwind HTTP surface. Handlers unmarshal, call the
// engine, and marshal back -- there is no service layer between them.
package api

import (
	"encoding/json"
	"log"
	"net/http"

	"github.com/DHSY-ishere/unwind/internal/engine"
	"github.com/DHSY-ishere/unwind/internal/ledger"
)

type Server struct {
	Ledger *ledger.Ledger
	Policy *engine.Policy
}

// Routes returns the full mux. Endpoints not yet implemented answer 501 so the
// shape of the API is visible from the first slice.
func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /v1/policy", s.getPolicy)

	// Implemented in later slices.
	mux.HandleFunc("POST /v1/act", notImplemented)                    // slice 1
	mux.HandleFunc("GET /v1/sessions", notImplemented)                // slice 6
	mux.HandleFunc("GET /v1/sessions/{id}", notImplemented)           // slice 1
	mux.HandleFunc("POST /v1/sessions/{id}/rollback", notImplemented) // slice 4
	mux.HandleFunc("POST /v1/approvals/{intent_id}", notImplemented)  // slice 5

	return logging(mux)
}

// getPolicy serves the active policy in minor units (DECISIONS.md F). It always
// reports policy.yaml, never a session's captured mode (DECISIONS.md I).
func (s *Server) getPolicy(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.Policy)
}

func notImplemented(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusNotImplemented, map[string]string{
		"error": "not implemented yet",
	})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		log.Printf("write response: %v", err)
	}
}

func logging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.Printf("%s %s", r.Method, r.URL.Path)
		next.ServeHTTP(w, r)
	})
}
