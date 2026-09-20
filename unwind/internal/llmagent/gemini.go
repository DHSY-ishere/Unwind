package llmagent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// geminiModel is a stable (non-preview) model confirmed against the live API
// to support generateContent + function calling. flash-lite specifically
// because it carries its own separate free-tier quota bucket from
// gemini-2.5-flash -- discovered the hard way when repeated demo/testing
// traffic exhausted flash's 20-request/day free tier during this same build
// session (DECISIONS.md ruling W).
const geminiModel = "gemini-2.5-flash-lite"

const geminiURLTemplate = "https://generativelanguage.googleapis.com/v1beta/models/%s:generateContent?key=%s"

// Gemini's REST API uses its own part/content shape (role "model"/"user",
// parts carrying text OR functionCall OR functionResponse) and an
// OpenAPI-flavored schema with UPPERCASE type names ("OBJECT", "STRING") --
// both confirmed against the live API before writing this, not assumed.

type geminiPart struct {
	Text             string                  `json:"text,omitempty"`
	FunctionCall     *geminiFunctionCall     `json:"functionCall,omitempty"`
	FunctionResponse *geminiFunctionResponse `json:"functionResponse,omitempty"`
}

type geminiFunctionCall struct {
	Name string          `json:"name"`
	Args json.RawMessage `json:"args"`
}

type geminiFunctionResponse struct {
	Name     string         `json:"name"`
	Response map[string]any `json:"response"`
}

type geminiContent struct {
	Role  string       `json:"role"`
	Parts []geminiPart `json:"parts"`
}

type geminiFunctionDeclaration struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
}

type geminiTool struct {
	FunctionDeclarations []geminiFunctionDeclaration `json:"functionDeclarations"`
}

type geminiSystemInstruction struct {
	Parts []geminiPart `json:"parts"`
}

type geminiRequest struct {
	SystemInstruction *geminiSystemInstruction `json:"system_instruction,omitempty"`
	Tools             []geminiTool             `json:"tools,omitempty"`
	Contents          []geminiContent          `json:"contents"`
}

type geminiCandidate struct {
	Content      geminiContent `json:"content"`
	FinishReason string        `json:"finishReason"`
}

type geminiAPIError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Status  string `json:"status"`
}

type geminiResponse struct {
	Candidates []geminiCandidate `json:"candidates"`
	Error      *geminiAPIError   `json:"error,omitempty"`
}

// GeminiClient is the Gemini counterpart to Client -- same "raw HTTP, one
// endpoint" shape, different wire format.
type GeminiClient struct {
	apiKey     string
	httpClient *http.Client
}

func NewGeminiClient(apiKey string) *GeminiClient {
	return &GeminiClient{apiKey: apiKey, httpClient: &http.Client{Timeout: 60 * time.Second}}
}

func (c *GeminiClient) generateContent(ctx context.Context, system string, tools []geminiTool, contents []geminiContent) (*geminiResponse, error) {
	reqBody, err := json.Marshal(geminiRequest{
		SystemInstruction: &geminiSystemInstruction{Parts: []geminiPart{{Text: system}}},
		Tools:             tools,
		Contents:          contents,
	})
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	url := fmt.Sprintf(geminiURLTemplate, geminiModel, c.apiKey)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(reqBody))
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("call Gemini API: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	var out geminiResponse
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("parse response (status %d): %w", resp.StatusCode, err)
	}
	if resp.StatusCode != http.StatusOK {
		if out.Error != nil {
			return nil, fmt.Errorf("Gemini API error (%d %s): %s", out.Error.Code, out.Error.Status, out.Error.Message)
		}
		return nil, fmt.Errorf("Gemini API returned status %d", resp.StatusCode)
	}
	return &out, nil
}

// geminiToolSchemas mirrors toolSchemas exactly (same names, same args,
// same get_world_state addition) in Gemini's uppercase-type schema dialect.
var geminiToolSchemas = []geminiTool{{FunctionDeclarations: []geminiFunctionDeclaration{
	{
		Name:        "get_world_state",
		Description: "See current vendors, their subscription status, account balances, invoices already touched by a refund, and a sample of untouched invoices (field sample_open_invoices) with real ids you can refund against. Call this before deciding what to do -- never invent an id.",
		Parameters:  map[string]any{"type": "OBJECT", "properties": map[string]any{}},
	},
	{
		Name:        "cancel_subscription",
		Description: "Cancel a vendor's active subscription. Reversible.",
		Parameters: map[string]any{
			"type":       "OBJECT",
			"properties": map[string]any{"vendor_id": map[string]any{"type": "STRING"}},
			"required":   []string{"vendor_id"},
		},
	},
	{
		Name:        "issue_refund",
		Description: "Refund part or all of an invoice, in paise (1 rupee = 100 paise). Partially reversible -- a reversal contests the invoice rather than un-refunding it.",
		Parameters: map[string]any{
			"type": "OBJECT",
			"properties": map[string]any{
				"invoice_id": map[string]any{"type": "STRING"},
				"amount":     map[string]any{"type": "INTEGER", "description": "paise"},
			},
			"required": []string{"invoice_id", "amount"},
		},
	},
	{
		Name:        "transfer_funds",
		Description: "Move money between accounts, in paise. IRREVERSIBLE -- there is no undo for this one.",
		Parameters: map[string]any{
			"type": "OBJECT",
			"properties": map[string]any{
				"from":   map[string]any{"type": "STRING"},
				"to":     map[string]any{"type": "STRING"},
				"amount": map[string]any{"type": "INTEGER", "description": "paise"},
			},
			"required": []string{"from", "to", "amount"},
		},
	},
}}}

// RunGeminiAgent is the Gemini counterpart to RunAgent -- identical contract
// (Step/RunResult/ExecTool, same system prompt, same maxTurns/maxToolCalls
// bounds), different wire format underneath. A finishReason of "STOP" is
// Gemini's normal finish for a function-call turn too, so whether to keep
// looping is decided by whether the turn actually contained a functionCall
// part, not by finishReason.
func RunGeminiAgent(ctx context.Context, apiKey, goal string, exec ExecTool, onStep func(Step)) (*RunResult, error) {
	client := NewGeminiClient(apiKey)
	system := fmt.Sprintf(systemPromptTemplate, goal)
	contents := []geminiContent{{Role: "user", Parts: []geminiPart{{Text: "Begin."}}}}

	result := &RunResult{}
	report := func(s Step) {
		result.Steps = append(result.Steps, s)
		if onStep != nil {
			onStep(s)
		}
	}

	for turn := 0; turn < maxTurns; turn++ {
		resp, err := client.generateContent(ctx, system, geminiToolSchemas, contents)
		if err != nil {
			return result, err
		}
		if len(resp.Candidates) == 0 {
			result.StopReason = "no_candidates"
			return result, nil
		}
		cand := resp.Candidates[0]

		var calls []geminiFunctionCall
		for _, p := range cand.Content.Parts {
			if p.Text != "" {
				report(Step{Type: "text", Text: p.Text})
			}
			if p.FunctionCall != nil {
				calls = append(calls, *p.FunctionCall)
			}
		}
		contents = append(contents, cand.Content)

		if len(calls) == 0 {
			result.StopReason = cand.FinishReason
			return result, nil
		}

		var responseParts []geminiPart
		for _, call := range calls {
			if result.ToolCalls >= maxToolCalls {
				responseParts = append(responseParts, geminiPart{FunctionResponse: &geminiFunctionResponse{
					Name: call.Name, Response: map[string]any{"error": "tool call budget for this run is exhausted"},
				}})
				continue
			}
			var input map[string]any
			if err := json.Unmarshal(call.Args, &input); err != nil {
				input = map[string]any{}
			}
			resultText, isErr := exec(call.Name, input)
			result.ToolCalls++
			report(Step{Type: "tool_call", Tool: call.Name, Input: input, Result: resultText, IsError: isErr})

			// functionResponse.response must be a JSON object -- unwrap
			// exec's JSON string if it parses as one, else wrap it plainly.
			respObj := map[string]any{}
			if json.Unmarshal([]byte(resultText), &respObj) != nil {
				respObj = map[string]any{"result": resultText}
			}
			if isErr {
				respObj["is_error"] = true
			}
			responseParts = append(responseParts, geminiPart{FunctionResponse: &geminiFunctionResponse{Name: call.Name, Response: respObj}})
		}
		contents = append(contents, geminiContent{Role: "user", Parts: responseParts})

		if result.ToolCalls >= maxToolCalls {
			result.StopReason = "tool_call_budget_exhausted"
			return result, nil
		}
	}

	result.StopReason = "max_turns_reached"
	return result, nil
}
