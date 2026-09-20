package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

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
}

func New(l *ledger.Ledger, r *tools.Registry, p *Policy) *Engine {
	return &Engine{Ledger: l, Tools: r, Policy: p}
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

	// SEAM: idempotency replay (landed in block 7) goes here, before the
	// intent row is written -- a replay must not consume a new seq.

	amount := tool.AmountMinor(args)

	// Step 1: write intent (pending) BEFORE execution.
	intent, err := e.Ledger.InsertPendingIntent(sessionID, toolName, argsJSON, idempotencyKey, string(tool.Reversibility()), amount)
	if err != nil {
		return nil, fmt.Errorf("insert intent: %w", err)
	}

	// SEAM: policy evaluation (landed in block 8) goes here. It runs against
	// `session` (for policy_mode) and `intent` (for reversibility/amount),
	// and on failure calls e.Ledger.MarkBlocked / MarkAwaitingApproval and
	// returns the intent immediately, before Capture/Execute ever run.
	_ = session

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
