package engine

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"github.com/DHSY-ishere/unwind/internal/ledger"
	"github.com/DHSY-ishere/unwind/internal/tools"
)

// ErrUnknownTool is returned by Act when the requested tool isn't registered.
var ErrUnknownTool = errors.New("engine: unknown tool")

// ErrIdempotencyConflict is returned by Act when a repeated idempotency key
// is still mid-flight (status pending) -- DECISIONS.md H. Wired in block 7.
var ErrIdempotencyConflict = errors.New("engine: idempotency key is still pending from a previous attempt")

// Engine runs the act pipeline (SPEC.md's six steps), policy evaluation and
// the rollback walk. One concrete type, no interface -- there is exactly one
// engine in this system.
type Engine struct {
	Ledger *ledger.Ledger
	Tools  *tools.Registry
	Policy *Policy

	// capsMu guards Policy.Caps specifically. Mode and Rules are set once at
	// load and never mutated at runtime; Caps is -- the Control Room's policy
	// console (POST /v1/policy) edits it live so a viewer can drag a cap down
	// and rerun the scenario without restarting the server. Every read of
	// Caps in the act pipeline goes through capsSnapshot() rather than
	// touching e.Policy.Caps directly, so a concurrent edit is never a data
	// race, just a value that's current as of the moment it's read.
	capsMu sync.RWMutex
}

func New(l *ledger.Ledger, r *tools.Registry, p *Policy) *Engine {
	return &Engine{Ledger: l, Tools: r, Policy: p}
}

// CapsSnapshot returns a copy of the live caps -- safe to read concurrently
// with UpdateCaps.
func (e *Engine) CapsSnapshot() Caps {
	e.capsMu.RLock()
	defer e.capsMu.RUnlock()
	perTool := make(map[string]int, len(e.Policy.Caps.PerTool))
	for k, v := range e.Policy.Caps.PerTool {
		perTool[k] = v
	}
	return Caps{
		MaxMutationsPerSession: e.Policy.Caps.MaxMutationsPerSession,
		MaxTotalAmountMinor:    e.Policy.Caps.MaxTotalAmountMinor,
		PerTool:                perTool,
	}
}

// UpdateCaps replaces the live mutation caps -- the Policy console's write
// path. It never touches Mode or Rules, and it never touches policy.yaml on
// disk: this is a runtime override for the running process only, gone on
// restart (DECISIONS.md R).
func (e *Engine) UpdateCaps(maxMutations int, perTool map[string]int) {
	e.capsMu.Lock()
	defer e.capsMu.Unlock()
	e.Policy.Caps.MaxMutationsPerSession = maxMutations
	cp := make(map[string]int, len(perTool))
	for k, v := range perTool {
		cp[k] = v
	}
	e.Policy.Caps.PerTool = cp
}

// PolicySnapshot returns a copy of the full policy (live caps, static mode
// and rules) -- what GET /v1/policy actually serves.
func (e *Engine) PolicySnapshot() Policy {
	return Policy{
		Mode:  e.Policy.Mode,
		Caps:  e.CapsSnapshot(),
		Rules: e.Policy.Rules,
	}
}

func toJSON(v any) (string, error) {
	if v == nil {
		v = map[string]any{}
	}
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// Act runs the pipeline: write intent (pending) -> [idempotency] -> [policy]
// -> capture -> execute -> commit. It always returns an *ledger.Intent when
// the tool name and args are valid, even when that intent ends up blocked or
// failed -- api.go maps the resulting status to an HTTP code. It returns a Go
// error only for programmer/client errors that never reached the ledger
// (unknown tool, unmarshalable args).
func (e *Engine) Act(ctx context.Context, sessionID, toolName string, args map[string]any, idempotencyKey string) (*ledger.Intent, error) {
	tool, ok := e.Tools.Get(toolName)
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrUnknownTool, toolName)
	}

	session, err := e.Ledger.GetOrCreateSession(sessionID, e.Policy.Mode)
	if err != nil {
		return nil, fmt.Errorf("get or create session: %w", err)
	}

	argsJSON, err := toJSON(args)
	if err != nil {
		return nil, fmt.Errorf("marshal args: %w", err)
	}

	// Idempotency replay (DECISIONS.md H): a repeated (session_id,
	// idempotency_key) never consumes a new seq. Every terminal status
	// replays verbatim, status code included -- api.go's statusCodeFor maps
	// it the same way it would a fresh call. Only a still-`pending` row
	// (a previous attempt that crashed mid-flight) is a conflict.
	if existing, err := e.Ledger.FindByIdempotencyKey(sessionID, idempotencyKey); err == nil {
		if existing.Status == "pending" {
			return nil, fmt.Errorf("%w (intent %s)", ErrIdempotencyConflict, existing.ID)
		}
		return existing, nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("check idempotency key: %w", err)
	}

	amount := tool.AmountMinor(args)

	// Step 1: write intent (pending) BEFORE execution.
	intent, err := e.Ledger.InsertPendingIntent(sessionID, toolName, argsJSON, idempotencyKey, string(tool.Reversibility()), amount)
	if err != nil {
		return nil, fmt.Errorf("insert intent: %w", err)
	}

	// Step 3: evaluate policy. Only the blast-radius caps
	// (max_mutations_per_session, per_tool) are wired up -- see DECISIONS.md
	// O for what's deliberately not built (amount caps, approval rules,
	// dryrun). A blocked intent is still committed to the ledger as `blocked`
	// -- a blocked action must leave a trace, not vanish silently.
	if session.PolicyMode != ModeOff {
		if ruleName, blocked := e.checkCaps(sessionID, toolName); blocked {
			reasonJSON, _ := toJSON(map[string]any{
				"error":       "blocked by policy",
				"policy_rule": ruleName,
			})
			if err := e.Ledger.MarkBlocked(intent.ID, reasonJSON); err != nil {
				return nil, fmt.Errorf("mark blocked: %w", err)
			}
			return e.Ledger.GetIntent(intent.ID)
		}
	}

	// Step 4: capture compensation BEFORE execution (prior state only --
	// DECISIONS.md A; the execute result is merged in below, post-commit).
	prior, err := tool.Capture(ctx, args)
	if err != nil {
		return e.failIntent(intent, "capture", err)
	}

	// Step 5: execute against the real tool (the seeded world).
	result, err := tool.Execute(ctx, args)
	if err != nil {
		return e.failIntent(intent, "execute", err)
	}

	// Step 6: commit. The compensation record is {"prior": ..., "result": ...}
	// so Compensate always has both the state to restore and the identifiers
	// (refund id, etc.) that only existed after execution.
	compensation := map[string]any{"prior": prior, "result": result}
	compJSON, err := toJSON(compensation)
	if err != nil {
		return e.failIntent(intent, "marshal compensation", err)
	}
	resultJSON, err := toJSON(result)
	if err != nil {
		return e.failIntent(intent, "marshal result", err)
	}
	if err := e.Ledger.MarkCommitted(intent.ID, resultJSON, compJSON); err != nil {
		return nil, fmt.Errorf("mark committed: %w", err)
	}

	return e.Ledger.GetIntent(intent.ID)
}

// failIntent records a capture/execute failure against the intent (never a
// silent error) and returns the up-to-date row.
func (e *Engine) failIntent(intent *ledger.Intent, stage string, cause error) (*ledger.Intent, error) {
	reasonJSON, _ := toJSON(map[string]any{"stage": stage, "error": cause.Error()})
	if err := e.Ledger.MarkFailed(intent.ID, reasonJSON); err != nil {
		return nil, fmt.Errorf("mark failed: %w", err)
	}
	return e.Ledger.GetIntent(intent.ID)
}
