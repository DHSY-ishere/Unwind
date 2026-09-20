// Package api exposes the Unwind HTTP surface. Handlers unmarshal, call the
// engine, and marshal back -- there is no service layer between them.
package api

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"

	"github.com/DHSY-ishere/unwind/internal/engine"
	"github.com/DHSY-ishere/unwind/internal/ledger"
)

type Server struct {
	Ledger *ledger.Ledger
	Engine *engine.Engine
	Policy *engine.Policy
}

// Routes returns the full mux. Endpoints not yet implemented answer 501 so the
// shape of the API is visible from the first slice.
func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /v1/policy", s.getPolicy)
	mux.HandleFunc("POST /v1/act", s.postAct)
	mux.HandleFunc("GET /v1/sessions", s.getSessions)
	mux.HandleFunc("GET /v1/sessions/{id}", s.getSession)

	// Implemented in later blocks.
	mux.HandleFunc("POST /v1/sessions/{id}/rollback", notImplemented) // block 5
	mux.HandleFunc("POST /v1/approvals/{intent_id}", notImplemented)  // block 5 (approvals were descoped -- see DECISIONS.md O)

	return logging(mux)
}

// getPolicy serves the active policy in minor units (DECISIONS.md F). It always
// reports policy.yaml, never a session's captured mode (DECISIONS.md I).
func (s *Server) getPolicy(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.Policy)
}

type actRequest struct {
	SessionID      string         `json:"session_id"`
	Tool           string         `json:"tool"`
	Args           map[string]any `json:"args"`
	IdempotencyKey string         `json:"idempotency_key"`
}

// postAct is POST /v1/act. It runs the full pipeline and maps the resulting
// intent status to the HTTP code SPEC.md defines (plus 409 for a crashed
// idempotency replay -- DECISIONS.md H).
func (s *Server) postAct(w http.ResponseWriter, r *http.Request) {
	var req actRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body: " + err.Error()})
		return
	}
	if req.SessionID == "" || req.Tool == "" || req.IdempotencyKey == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "session_id, tool and idempotency_key are required"})
		return
	}

	intent, err := s.Engine.Act(r.Context(), req.SessionID, req.Tool, req.Args, req.IdempotencyKey)
	if err != nil {
		if errors.Is(err, engine.ErrUnknownTool) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		if errors.Is(err, engine.ErrIdempotencyConflict) {
			writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
			return
		}
		log.Printf("act error: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}

	writeJSON(w, statusCodeFor(intent.Status), intentResponse(intent))
}

func statusCodeFor(status string) int {
	switch status {
	case "committed", "denied":
		return http.StatusOK
	case "awaiting_approval":
		return http.StatusAccepted
	case "blocked":
		return http.StatusForbidden
	default: // pending (shouldn't escape Act), failed
		return http.StatusOK
	}
}

func intentResponse(i *ledger.Intent) map[string]any {
	resp := map[string]any{
		"intent_id": i.ID,
		"status":    i.Status,
		"seq":       i.Seq,
		"tool":      i.Tool,
	}
	if i.ResultJSON != "" {
		var v any
		if json.Unmarshal([]byte(i.ResultJSON), &v) == nil {
			switch i.Status {
			case "blocked", "failed", "denied":
				resp["reason"] = v
				if m, ok := v.(map[string]any); ok {
					if rule, ok := m["policy_rule"]; ok {
						resp["policy_rule"] = rule
					}
				}
			default:
				resp["result"] = v
			}
		}
	}
	return resp
}

// getSession is GET /v1/sessions/{id}: the session plus its ordered intents.
func (s *Server) getSession(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	sess, err := s.Ledger.GetSession(id)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "session not found"})
		return
	}
	intents, err := s.Ledger.ListIntents(id)
	if err != nil {
		log.Printf("list intents: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	out := make([]map[string]any, 0, len(intents))
	for _, i := range intents {
		out = append(out, fullIntentResponse(i))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"session": sess,
		"intents": out,
	})
}

func fullIntentResponse(i *ledger.Intent) map[string]any {
	m := map[string]any{
		"id":             i.ID,
		"seq":            i.Seq,
		"tool":           i.Tool,
		"reversibility":  i.Reversibility,
		"status":         i.Status,
		"amount_minor":   i.AmountMinor,
		"created_at":     i.CreatedAt,
		"settled_at":     i.SettledAt,
	}
	var args any
	if json.Unmarshal([]byte(i.ArgsJSON), &args) == nil {
		m["args"] = args
	}
	if i.ResultJSON != "" {
		var v any
		if json.Unmarshal([]byte(i.ResultJSON), &v) == nil {
			m["result"] = v
		}
	}
	return m
}

// getSessions is GET /v1/sessions (DECISIONS.md J).
func (s *Server) getSessions(w http.ResponseWriter, r *http.Request) {
	sessions, err := s.Ledger.ListSessions()
	if err != nil {
		log.Printf("list sessions: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	writeJSON(w, http.StatusOK, sessions)
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
