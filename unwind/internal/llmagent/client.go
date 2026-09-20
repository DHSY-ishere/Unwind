// Package llmagent is the real agent driver SPEC.md always wanted: a direct
// HTTP tool-use loop against the Anthropic Messages API, no SDK. It is the
// stretch goal DECISIONS.md K/O deferred -- the scripted and swarm drivers
// remain the primary, key-free demo path; this is an additional front door
// that requires ANTHROPIC_API_KEY.
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

const (
	messagesURL      = "https://api.anthropic.com/v1/messages"
	anthropicVersion = "2023-06-01"
	// Opus 5 is the current flagship; this is a low-volume agentic demo loop,
	// not a cost-sensitive high-throughput route, so there's no reason to
	// reach for a cheaper tier.
	model = "claude-opus-5"
)

// Client is a minimal raw-HTTP client for exactly what the agent loop needs
// -- one endpoint, one shape. No SDK, per SPEC.md.
type Client struct {
	apiKey     string
	httpClient *http.Client
}

func NewClient(apiKey string) *Client {
	return &Client{apiKey: apiKey, httpClient: &http.Client{Timeout: 60 * time.Second}}
}

// ContentBlock covers every block shape this loop sends or receives: text,
// tool_use (assistant) and tool_result (user). Fields irrelevant to a given
// type are simply omitted by omitempty.
type ContentBlock struct {
	Type string `json:"type"`

	// text
	Text string `json:"text,omitempty"`

	// tool_use (from the model)
	ID    string          `json:"id,omitempty"`
	Name  string          `json:"name,omitempty"`
	Input json.RawMessage `json:"input,omitempty"`

	// tool_result (to the model)
	ToolUseID string `json:"tool_use_id,omitempty"`
	Content   string `json:"content,omitempty"`
	IsError   bool   `json:"is_error,omitempty"`
}

type Message struct {
	Role    string         `json:"role"`
	Content []ContentBlock `json:"content"`
}

type Tool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"input_schema"`
}

type request struct {
	Model     string    `json:"model"`
	MaxTokens int       `json:"max_tokens"`
	System    string    `json:"system,omitempty"`
	Tools     []Tool    `json:"tools,omitempty"`
	Messages  []Message `json:"messages"`
}

type usage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

// Response is the subset of the Messages API response this loop reads.
type Response struct {
	Content    []ContentBlock `json:"content"`
	StopReason string         `json:"stop_reason"`
	Usage      usage          `json:"usage"`
	Error      *apiError      `json:"error,omitempty"`
}

type apiError struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

// CreateMessage sends one turn. It returns a descriptive error (including the
// API's own error message) rather than a bare HTTP status on failure, since
// this loop's caller has nowhere else to surface what went wrong.
func (c *Client) CreateMessage(ctx context.Context, system string, tools []Tool, messages []Message) (*Response, error) {
	body, err := json.Marshal(request{
		Model:     model,
		MaxTokens: 1536,
		System:    system,
		Tools:     tools,
		Messages:  messages,
	})
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, messagesURL, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", c.apiKey)
	req.Header.Set("anthropic-version", anthropicVersion)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("call Anthropic API: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	var out Response
	if err := json.Unmarshal(respBody, &out); err != nil {
		return nil, fmt.Errorf("parse response (status %d): %w", resp.StatusCode, err)
	}
	if resp.StatusCode != http.StatusOK {
		if out.Error != nil {
			return nil, fmt.Errorf("Anthropic API error (%d %s): %s", resp.StatusCode, out.Error.Type, out.Error.Message)
		}
		return nil, fmt.Errorf("Anthropic API returned status %d", resp.StatusCode)
	}
	return &out, nil
}
