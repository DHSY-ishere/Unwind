package main

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/spf13/cobra"

	"github.com/DHSY-ishere/unwind/internal/demo"
)

func randomSuffix() string {
	b := make([]byte, 3)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

type actClient struct {
	server string
	client *http.Client
}

func (c *actClient) act(sessionID string, a demo.Action, idemKey string) (status string, body map[string]any, err error) {
	payload, _ := json.Marshal(map[string]any{
		"session_id":      sessionID,
		"tool":            a.Tool,
		"args":            a.Args,
		"idempotency_key": idemKey,
	})
	resp, err := c.client.Post(c.server+"/v1/act", "application/json", bytes.NewReader(payload))
	if err != nil {
		return "", nil, err
	}
	defer resp.Body.Close()
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", nil, err
	}
	if s, ok := out["status"].(string); ok {
		status = s
	} else {
		status = fmt.Sprintf("http_%d", resp.StatusCode)
	}
	return status, out, nil
}

// newDemoCmd is the CLI driver: fires demo.RogueSequence() over real HTTP as
// an ordinary client (DECISIONS.md P). The web Control Room's
// POST /v1/demo/run fires the identical sequence in-process instead
// (DECISIONS.md R) -- same story, two front doors.
func newDemoCmd() *cobra.Command {
	var scenario, server string
	cmd := &cobra.Command{
		Use:   "demo",
		Short: "Drive a scripted scenario against a running 'unwind serve' over HTTP",
		RunE: func(cmd *cobra.Command, args []string) error {
			if scenario != "rogue" && scenario != "guarded" {
				return fmt.Errorf("unknown --scenario %q (want rogue or guarded)", scenario)
			}
			sessionID := fmt.Sprintf("%s-%s", scenario, randomSuffix())
			client := &actClient{server: server, client: &http.Client{Timeout: 10 * time.Second}}

			actions := demo.RogueSequence()
			blocked, committed, failed := 0, 0, 0

			fmt.Printf("driving %d calls against %s  (session %s)\n\n", len(actions), server, sessionID)
			for i, a := range actions {
				idemKey := fmt.Sprintf("demo-%s-%02d", sessionID, i+1)
				status, body, err := client.act(sessionID, a, idemKey)
				if err != nil {
					return fmt.Errorf("call %d (%s): %w -- is 'unwind serve' running at %s?", i+1, a.Tool, err, server)
				}
				switch status {
				case "committed":
					committed++
					fmt.Printf("  [%2d] %-20s committed\n", i+1, a.Tool)
				case "blocked":
					blocked++
					rule, _ := body["policy_rule"].(string)
					reason, _ := body["reason"].(map[string]any)
					if rule == "" && reason != nil {
						if r, ok := reason["rule"].(string); ok {
							rule = r
						}
					}
					fmt.Printf("  [%2d] %-20s BLOCKED  (%s)\n", i+1, a.Tool, rule)
				default:
					failed++
					fmt.Printf("  [%2d] %-20s %s\n", i+1, a.Tool, status)
				}
			}

			fmt.Println()
			fmt.Printf("committed=%d blocked=%d other=%d\n", committed, blocked, failed)
			fmt.Println(sessionID)
			return nil
		},
	}
	cmd.Flags().StringVar(&scenario, "scenario", "rogue", "rogue | guarded")
	cmd.Flags().StringVar(&server, "server", "http://localhost:8080", "base URL of a running 'unwind serve'")
	return cmd
}
