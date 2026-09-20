// Package api exposes the Unwind HTTP surface. Handlers unmarshal, call the
// engine, and marshal back -- there is no service layer between them.
package api

import (
	"crypto/rand"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"

	"github.com/DHSY-ishere/unwind/internal/demo"
	"github.com/DHSY-ishere/unwind/internal/engine"
	"github.com/DHSY-ishere/unwind/internal/ledger"
	"github.com/DHSY-ishere/unwind/internal/tools"
)

//go:embed ui/index.html
var uiHTML []byte

type Server struct {
	Ledger *ledger.Ledger
	Engine *engine.Engine
}

// Routes returns the full mux. Endpoints not yet implemented answer 501 so the
// shape of the API is visible from the first slice.
func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /{$}", s.getUI)
	mux.HandleFunc("GET /v1/policy", s.getPolicy)
	mux.HandleFunc("POST /v1/policy", s.postPolicy)
	mux.HandleFunc("POST /v1/act", s.postAct)
	mux.HandleFunc("GET /v1/sessions", s.getSessions)
	mux.HandleFunc("GET /v1/sessions/{id}", s.getSession)
	mux.HandleFunc("GET /v1/world", s.getWorld)
	mux.HandleFunc("POST /v1/world/reset", s.postWorldReset)
	mux.HandleFunc("POST /v1/demo/run", s.postDemoRun)

	mux.HandleFunc("POST /v1/sessions/{id}/rollback", s.postRollback)

	// Approvals are designed but not built in this session -- see
	// DECISIONS.md O ("designed but not built in the hackathon window").
	mux.HandleFunc("POST /v1/approvals/{intent_id}", notImplemented)

	return logging(mux)
}

// getUI serves the embedded single-page dashboard (DECISIONS.md J).
func (s *Server) getUI(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(uiHTML)
}

// getPolicy serves the active policy in minor units (DECISIONS.md F). Mode
// and rules always reflect policy.yaml (DECISIONS.md I: a session's captured
// mode is separate and never reported here); caps reflect any live edit made
// through the Policy console (DECISIONS.md R).
func (s *Server) getPolicy(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.Engine.PolicySnapshot())
}

type updateCapsRequest struct {
	MaxMutationsPerSession int            `json:"max_mutations_per_session"`
	PerTool                map[string]int `json:"per_tool"`
}

// postPolicy is the Policy console's write path (DECISIONS.md R): it edits
// the live mutation caps for this running process only. It never touches
// policy.yaml, never touches Mode or Rules, and takes effect on the very
// next Act call -- there's no restart, no reload, nothing cached in between.
func (s *Server) postPolicy(w http.ResponseWriter, r *http.Request) {
	var req updateCapsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body: " + err.Error()})
		return
	}
	if req.MaxMutationsPerSession < 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "max_mutations_per_session must be >= 0"})
		return
	}
	for tool, capVal := range req.PerTool {
		if _, ok := s.Engine.Tools.Get(tool); !ok {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": fmt.Sprintf("unknown tool %q", tool)})
			return
		}
		if capVal < 0 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": fmt.Sprintf("per_tool.%s must be >= 0", tool)})
			return
		}
	}
	s.Engine.UpdateCaps(req.MaxMutationsPerSession, req.PerTool)
	writeJSON(w, http.StatusOK, s.Engine.PolicySnapshot())
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
		"id":            i.ID,
		"seq":           i.Seq,
		"tool":          i.Tool,
		"reversibility": i.Reversibility,
		"status":        i.Status,
		"amount_minor":  i.AmountMinor,
		"created_at":    i.CreatedAt,
		"settled_at":    i.SettledAt,
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

// postRollback is POST /v1/sessions/{id}/rollback.
func (s *Server) postRollback(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, err := s.Ledger.GetSession(id); err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "session not found"})
		return
	}
	summary, err := s.Engine.Rollback(r.Context(), id)
	if err != nil {
		log.Printf("rollback error: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	writeJSON(w, http.StatusOK, summary)
}

// getWorld is GET /v1/world: a read-only snapshot of the fake world state
// (accounts, vendor subscriptions, touched invoices) so the World screen can
// make a "committed" cancellation or refund visible as a real change, not
// just a status word. Like the list_vendors tool this build cut (DECISIONS.md
// L), it's a plain read -- never ledgered, never routed through Act.
func (s *Server) getWorld(w http.ResponseWriter, r *http.Request) {
	snap, err := tools.Snapshot(s.Ledger.DB)
	if err != nil {
		log.Printf("world snapshot: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	writeJSON(w, http.StatusOK, snap)
}

// postWorldReset is POST /v1/world/reset: reseeds the fake world back to its
// deterministic starting state, so "Launch Agent" can be run repeatedly in a
// live demo without the second run's cancellations legitimately failing on
// an already-cancelled subscription (see ResetWorld's doc comment).
func (s *Server) postWorldReset(w http.ResponseWriter, r *http.Request) {
	if err := tools.ResetWorld(s.Ledger.DB); err != nil {
		log.Printf("world reset: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	snap, err := tools.Snapshot(s.Ledger.DB)
	if err != nil {
		log.Printf("world snapshot after reset: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	writeJSON(w, http.StatusOK, snap)
}

func randomSessionID(prefix string) string {
	b := make([]byte, 3)
	_, _ = rand.Read(b)
	return prefix + "-" + hex.EncodeToString(b)
}

// postDemoRun is POST /v1/demo/run: the Control Room's "Launch Agent"
// button. It fires demo.RogueSequence() in-process against whatever caps are
// live right now -- the exact same 17 calls the CLI's `unwind demo` sends
// over real HTTP (DECISIONS.md P), just triggered from the browser instead of
// a terminal, and reactive to the Policy console's live edits
// (DECISIONS.md R) rather than to which policy.yaml the process booted with.
func (s *Server) postDemoRun(w http.ResponseWriter, r *http.Request) {
	sessionID := randomSessionID("run")
	actions := demo.RogueSequence()

	results := make([]map[string]any, 0, len(actions))
	committed, blocked, other := 0, 0, 0
	for i, a := range actions {
		idemKey := fmt.Sprintf("demo-%s-%02d", sessionID, i+1)
		intent, err := s.Engine.Act(r.Context(), sessionID, a.Tool, a.Args, idemKey)
		if err != nil {
			log.Printf("demo run act error: %v", err)
			other++
			results = append(results, map[string]any{"tool": a.Tool, "status": "error"})
			continue
		}
		switch intent.Status {
		case "committed":
			committed++
		case "blocked":
			blocked++
		default:
			other++
		}
		results = append(results, map[string]any{"tool": a.Tool, "status": intent.Status, "seq": intent.Seq})
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"session_id": sessionID,
		"committed":  committed,
		"blocked":    blocked,
		"other":      other,
		"results":    results,
	})
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
