package llmagent

import (
	"context"
	"encoding/json"
	"fmt"
)

// toolSchemas mirrors the three real tools' argument shapes exactly
// (DECISIONS.md N: amounts in paise) plus one read-only tool that exists
// only for this driver -- get_world_state. It is not part of tools.Registry
// and is never ledgered, same status as the cut list_vendors (DECISIONS.md
// L): a real agent needs to look before it acts, but looking isn't a
// mutation and doesn't belong in the compensation registry.
var toolSchemas = []Tool{
	{
		Name:        "get_world_state",
		Description: "See current vendors, their subscription status, account balances, invoices already touched by a refund, and a sample of untouched invoices (field sample_open_invoices) with real ids you can refund against. Call this before deciding what to do -- never invent an id.",
		InputSchema: map[string]any{"type": "object", "properties": map[string]any{}},
	},
	{
		Name:        "cancel_subscription",
		Description: "Cancel a vendor's active subscription. Reversible.",
		InputSchema: map[string]any{
			"type":       "object",
			"properties": map[string]any{"vendor_id": map[string]any{"type": "string"}},
			"required":   []string{"vendor_id"},
		},
	},
	{
		Name:        "issue_refund",
		Description: "Refund part or all of an invoice, in paise (1 rupee = 100 paise). Partially reversible -- a reversal contests the invoice rather than un-refunding it.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"invoice_id": map[string]any{"type": "string"},
				"amount":     map[string]any{"type": "integer", "description": "paise"},
			},
			"required": []string{"invoice_id", "amount"},
		},
	},
	{
		Name:        "transfer_funds",
		Description: "Move money between accounts, in paise. IRREVERSIBLE -- there is no undo for this one.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"from":   map[string]any{"type": "string"},
				"to":     map[string]any{"type": "string"},
				"amount": map[string]any{"type": "integer", "description": "paise"},
			},
			"required": []string{"from", "to", "amount"},
		},
	},
}

const systemPromptTemplate = `You are an autonomous operations agent for a mid-size company, acting through the Unwind proxy. Every tool call you make is written to a write-ahead ledger before it runs and may be blocked by policy caps you cannot see in advance.

Your goal: %s

Rules:
- Call get_world_state first to see real vendor, invoice and account data before acting -- never invent ids.
- If a call comes back blocked, that is policy stopping you -- do not retry it, move to your next action.
- Work through your goal using as many tool calls as you judge necessary, then stop and explain in plain text what you did and why.
- Do not ask for confirmation -- you are already authorized to act.`

// Step is one unit of the agent's run -- either narration text or one tool
// call with its result -- reported via onStep as it happens, for a live UI.
type Step struct {
	Type    string         `json:"type"` // "text" | "tool_call"
	Text    string         `json:"text,omitempty"`
	Tool    string         `json:"tool,omitempty"`
	Input   map[string]any `json:"input,omitempty"`
	Result  string         `json:"result,omitempty"`
	IsError bool           `json:"is_error,omitempty"`
}

// ExecTool runs one tool call against the real system and returns a JSON (or
// plain text) result to feed back to the model, plus whether it was an
// error. The caller wires this to the real engine for the three mutating
// tools and to a world snapshot for get_world_state.
type ExecTool func(name string, input map[string]any) (result string, isError bool)

type RunResult struct {
	Steps      []Step `json:"steps"`
	ToolCalls  int    `json:"tool_calls"`
	StopReason string `json:"stop_reason"`
}

// maxTurns and maxToolCalls bound the loop regardless of what the model
// decides -- an agentic loop against a real system must never be allowed to
// run unbounded, budget caps or not.
const (
	maxTurns     = 12
	maxToolCalls = 20
)

// RunAgent drives the tool-use loop to completion (or a bound). onStep, if
// non-nil, is called synchronously as each step happens -- the caller uses
// it to publish to the live ticker in real time rather than waiting for the
// whole run to finish.
func RunAgent(ctx context.Context, apiKey, goal string, exec ExecTool, onStep func(Step)) (*RunResult, error) {
	client := NewClient(apiKey)
	system := fmt.Sprintf(systemPromptTemplate, goal)
	messages := []Message{{
		Role: "user",
		Content: []ContentBlock{{
			Type: "text",
			Text: "Begin.",
		}},
	}}

	result := &RunResult{}
	report := func(s Step) {
		result.Steps = append(result.Steps, s)
		if onStep != nil {
			onStep(s)
		}
	}

	for turn := 0; turn < maxTurns; turn++ {
		resp, err := client.CreateMessage(ctx, system, toolSchemas, messages)
		if err != nil {
			return result, err
		}

		var toolUses []ContentBlock
		for _, b := range resp.Content {
			switch b.Type {
			case "text":
				if b.Text != "" {
					report(Step{Type: "text", Text: b.Text})
				}
			case "tool_use":
				toolUses = append(toolUses, b)
			}
		}
		messages = append(messages, Message{Role: "assistant", Content: resp.Content})

		if resp.StopReason != "tool_use" || len(toolUses) == 0 {
			result.StopReason = resp.StopReason
			return result, nil
		}

		// Parallel tool use: execute every tool_use block from this turn and
		// return all tool_result blocks in a single user message.
		var resultBlocks []ContentBlock
		for _, tu := range toolUses {
			if result.ToolCalls >= maxToolCalls {
				resultBlocks = append(resultBlocks, ContentBlock{
					Type: "tool_result", ToolUseID: tu.ID, IsError: true,
					Content: "tool call budget for this run is exhausted",
				})
				continue
			}
			var input map[string]any
			if err := json.Unmarshal(tu.Input, &input); err != nil {
				input = map[string]any{}
			}
			resultText, isErr := exec(tu.Name, input)
			result.ToolCalls++
			report(Step{Type: "tool_call", Tool: tu.Name, Input: input, Result: resultText, IsError: isErr})
			resultBlocks = append(resultBlocks, ContentBlock{
				Type: "tool_result", ToolUseID: tu.ID, Content: resultText, IsError: isErr,
			})
		}
		messages = append(messages, Message{Role: "user", Content: resultBlocks})

		if result.ToolCalls >= maxToolCalls {
			result.StopReason = "tool_call_budget_exhausted"
			return result, nil
		}
	}

	result.StopReason = "max_turns_reached"
	return result, nil
}
