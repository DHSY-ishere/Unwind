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
)

// action is one call the scripted driver fires at POST /v1/act.
type action struct {
	Tool string
	Args map[string]any
}

// rogueScenario is 12 cancel_subscription, 4 issue_refund and 1
// transfer_funds, interleaved so the resulting timeline doesn't read as three
// batched runs -- it should look like an agent going off the rails call by
// call. Both the "rogue" and "guarded" scenarios fire this exact sequence
// (DECISIONS.md P); only the server's policy differs.
func rogueScenario() []action {
	cancel := func(vendorNum int) action {
		return action{Tool: "cancel_subscription", Args: map[string]any{
			"vendor_id": fmt.Sprintf("vnd_%03d", vendorNum),
		}}
	}
	refund := func(invoiceNum int, amountRupees int64) action {
		return action{Tool: "issue_refund", Args: map[string]any{
			"invoice_id": fmt.Sprintf("inv_%04d", invoiceNum),
			"amount":     amountRupees * 100,
		}}
	}
	transfer := action{Tool: "transfer_funds", Args: map[string]any{
		"from": "acc_main", "to": "acc_reserve", "amount": int64(200000) * 100,
	}}

	return []action{
		cancel(1), cancel(2),
		refund(1, 5000),
		cancel(3), cancel(4), cancel(5),
		refund(2, 7500),
		transfer,
		cancel(6), cancel(7),
		refund(3, 6000),
		cancel(8), cancel(9), cancel(10),
		refund(4, 4500),
		cancel(11), cancel(12),
	}
}

func randomSuffix() string {
	b := make([]byte, 3)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

type actClient struct {
	server string
	client *http.Client
}

func (c *actClient) act(sessionID string, a action, idemKey string) (status string, body map[string]any, err error) {
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

			actions := rogueScenario()
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
