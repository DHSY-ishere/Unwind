package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/DHSY-ishere/unwind/internal/tools"
)

// RollbackSummary is the result of a rollback walk -- the exact shape
// POST /v1/sessions/{id}/rollback and `unwind rollback` report.
type RollbackSummary struct {
	Compensated   int `json:"compensated"`
	Uncompensable int `json:"uncompensable"`
	Failed        int `json:"failed"`
}

// Rollback is the thesis of this project: walk a session's intents in
// descending seq and compensate every committed one.
//
// DECISIONS.md E is the single rule this whole walk rests on: only intents in
// status `committed` are candidates for compensation. Every other status
// (pending, blocked, failed, awaiting_approval, denied, and -- critically --
// already compensated/uncompensable/failed_compensation from a prior
// rollback) is skipped by definition. That one rule is what makes rerunning
// rollback on an already-rolled-back session a true no-op: there is nothing
// left in status `committed` to walk.
func (e *Engine) Rollback(ctx context.Context, sessionID string) (*RollbackSummary, error) {
	session, err := e.Ledger.GetSession(sessionID)
	if err != nil {
		return nil, fmt.Errorf("get session: %w", err)
	}

	// Descending seq -- the walk undoes the most recent action first, which
	// matters when later actions depend on earlier ones (e.g. a transfer out
	// of an account a subscription cancellation later funded).
	intents, err := e.Ledger.ListIntentsDesc(sessionID)
	if err != nil {
		return nil, fmt.Errorf("list intents: %w", err)
	}

	summary := &RollbackSummary{}
	for _, intent := range intents {
		if intent.Status != "committed" {
			continue // ruling E: not a candidate, by definition
		}

		tool, ok := e.Tools.Get(intent.Tool)
		if !ok {
			// A tool that once ran but is no longer registered. Never seen in
			// this build (the registry is fixed at three tools), but never
			// abort the walk over it either.
			if err := e.Ledger.MarkFailedCompensation(intent.ID, "tool no longer registered: "+intent.Tool); err != nil {
				return nil, fmt.Errorf("mark failed_compensation: %w", err)
			}
			summary.Failed++
			continue
		}

		var compensation map[string]any
		if intent.CompensationJSON != "" {
			if err := json.Unmarshal([]byte(intent.CompensationJSON), &compensation); err != nil {
				if err := e.Ledger.MarkFailedCompensation(intent.ID, "corrupt compensation record: "+err.Error()); err != nil {
					return nil, fmt.Errorf("mark failed_compensation: %w", err)
				}
				summary.Failed++
				continue
			}
		}

		compErr := tool.Compensate(ctx, compensation)
		switch {
		case compErr == nil:
			if err := e.Ledger.MarkCompensated(intent.ID); err != nil {
				return nil, fmt.Errorf("mark compensated: %w", err)
			}
			summary.Compensated++
		case errors.Is(compErr, tools.ErrUncompensable):
			if err := e.Ledger.MarkUncompensable(intent.ID); err != nil {
				return nil, fmt.Errorf("mark uncompensable: %w", err)
			}
			summary.Uncompensable++
		default:
			// Individual compensation failure: record it and keep walking.
			// Never abort the whole rollback on one failure.
			if err := e.Ledger.MarkFailedCompensation(intent.ID, compErr.Error()); err != nil {
				return nil, fmt.Errorf("mark failed_compensation: %w", err)
			}
			summary.Failed++
		}
	}

	// Only the first rollback transitions the session's status -- a rerun
	// (which walks zero committed intents and so cannot change the counts)
	// must never downgrade partially_rolled_back back to rolled_back.
	if session.Status == "active" {
		status := "rolled_back"
		if summary.Failed > 0 {
			status = "partially_rolled_back"
		}
		if err := e.Ledger.UpdateSessionStatus(sessionID, status); err != nil {
			return nil, fmt.Errorf("update session status: %w", err)
		}
	}

	return summary, nil
}
