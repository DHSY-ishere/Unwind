// Package demo holds the scripted call sequence shared by the CLI driver
// (root demo.go, which fires it over real HTTP as an ordinary client) and the
// server's own POST /v1/demo/run (which fires it in-process for the Control
// Room UI). One sequence, two ways to trigger it.
package demo

import "fmt"

// Action is one call to fire at the act pipeline.
type Action struct {
	Tool string
	Args map[string]any
}

// RogueSequence is 12 cancel_subscription, 4 issue_refund and 1
// transfer_funds, interleaved so the resulting timeline doesn't read as three
// batched runs. Whether a run looks "rogue" (mostly commits) or "guarded"
// (blocks partway) now depends entirely on the live policy caps in effect
// when it runs, not on which variant was requested -- see DECISIONS.md R.
func RogueSequence() []Action {
	cancel := func(vendorNum int) Action {
		return Action{Tool: "cancel_subscription", Args: map[string]any{
			"vendor_id": fmt.Sprintf("vnd_%03d", vendorNum),
		}}
	}
	refund := func(invoiceNum int, amountRupees int64) Action {
		return Action{Tool: "issue_refund", Args: map[string]any{
			"invoice_id": fmt.Sprintf("inv_%04d", invoiceNum),
			"amount":     amountRupees * 100,
		}}
	}
	transfer := Action{Tool: "transfer_funds", Args: map[string]any{
		"from": "acc_main", "to": "acc_reserve", "amount": int64(200000) * 100,
	}}

	return []Action{
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
