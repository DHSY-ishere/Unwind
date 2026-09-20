// Package tools implements the Tool interface, the registry, and the fake
// "real world" (vendors, subscriptions, invoices, accounts) that the tools
// mutate. The world lives in the same SQLite file as the ledger, in separate
// tables, so a demo reset is "delete the one file".
package tools

import (
	"database/sql"
	_ "embed"
	"fmt"
)

//go:embed seed.sql
var seedSQL string

const worldSchema = `
CREATE TABLE IF NOT EXISTS vendors (
    id         TEXT PRIMARY KEY,
    name       TEXT NOT NULL,
    category   TEXT NOT NULL,
    created_at TIMESTAMP NOT NULL
);

CREATE TABLE IF NOT EXISTS accounts (
    id            TEXT PRIMARY KEY,
    name          TEXT NOT NULL,
    balance_minor INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS subscriptions (
    id           TEXT PRIMARY KEY,
    vendor_id    TEXT NOT NULL REFERENCES vendors(id),
    account_id   TEXT NOT NULL REFERENCES accounts(id),
    plan         TEXT NOT NULL,
    amount_minor INTEGER NOT NULL,
    status       TEXT NOT NULL, -- active | cancelled
    started_at   TIMESTAMP NOT NULL,
    cancelled_at TIMESTAMP
);

CREATE TABLE IF NOT EXISTS invoices (
    id                    TEXT PRIMARY KEY,
    vendor_id             TEXT NOT NULL REFERENCES vendors(id),
    amount_minor          INTEGER NOT NULL,
    status                TEXT NOT NULL, -- open | paid | refunded | contested
    refunded_amount_minor INTEGER NOT NULL DEFAULT 0,
    issued_at             TIMESTAMP NOT NULL
);

CREATE TABLE IF NOT EXISTS journal_entries (
    id           TEXT PRIMARY KEY,
    account_id   TEXT NOT NULL REFERENCES accounts(id),
    amount_minor INTEGER NOT NULL, -- signed: debit negative, credit positive
    kind         TEXT NOT NULL,    -- transfer | refund | reversal
    ref          TEXT NOT NULL,    -- intent id or refund id this entry belongs to
    created_at   TIMESTAMP NOT NULL
);
`

// Account, VendorStatus and Invoice are the read model for GET /v1/world --
// enough of the fake world's real state for the dashboard's World screen to
// make "committed" and "compensated" visible as an actual balance moving,
// not just a status word.
type Account struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	BalanceMinor int64  `json:"balance_minor"`
}

type VendorStatus struct {
	VendorID       string `json:"vendor_id"`
	Name           string `json:"name"`
	Category       string `json:"category"`
	SubscriptionID string `json:"subscription_id,omitempty"`
	Plan           string `json:"plan,omitempty"`
	AmountMinor    int64  `json:"amount_minor,omitempty"`
	Status         string `json:"status"` // active | cancelled | none (no subscription)
}

type Invoice struct {
	ID                  string `json:"id"`
	VendorID            string `json:"vendor_id"`
	AmountMinor         int64  `json:"amount_minor"`
	Status              string `json:"status"`
	RefundedAmountMinor int64  `json:"refunded_amount_minor"`
}

// WorldSnapshot is the full response body for GET /v1/world.
type WorldSnapshot struct {
	Accounts       []Account      `json:"accounts"`
	Vendors        []VendorStatus `json:"vendors"`
	Invoices       []Invoice      `json:"invoices"`        // only invoices touched by a refund -- the World screen's "what changed" view
	InvoiceSummary map[string]int `json:"invoice_summary"` // status -> count, across all 120
	// SampleOpenInvoices is a capped sample of untouched (open/paid)
	// invoices with real ids -- without it, a caller (the real LLM agent,
	// in particular) has no way to discover a valid invoice_id to refund
	// against on a fresh world, since Invoices above only lists ones
	// already refunded/contested. Not used by the World screen.
	SampleOpenInvoices []Invoice `json:"sample_open_invoices"`
}

// Snapshot reads the current world state for the dashboard. It's a plain
// read -- never ledgered, never routed through Act -- exactly like the
// list_vendors tool this build cut (DECISIONS.md L); the difference is this
// one exists to make the World screen possible, not as a fourth tool.
func Snapshot(db *sql.DB) (*WorldSnapshot, error) {
	snap := &WorldSnapshot{InvoiceSummary: map[string]int{}}

	rows, err := db.Query(`SELECT id, name, balance_minor FROM accounts ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("query accounts: %w", err)
	}
	for rows.Next() {
		var a Account
		if err := rows.Scan(&a.ID, &a.Name, &a.BalanceMinor); err != nil {
			rows.Close()
			return nil, err
		}
		snap.Accounts = append(snap.Accounts, a)
	}
	rows.Close()

	rows, err = db.Query(`
		SELECT v.id, v.name, v.category,
		       COALESCE(s.id, ''), COALESCE(s.plan, ''), COALESCE(s.amount_minor, 0),
		       CASE WHEN s.id IS NULL THEN 'none' ELSE s.status END
		FROM vendors v
		LEFT JOIN subscriptions s ON s.vendor_id = v.id
		ORDER BY v.id`)
	if err != nil {
		return nil, fmt.Errorf("query vendors: %w", err)
	}
	for rows.Next() {
		var v VendorStatus
		if err := rows.Scan(&v.VendorID, &v.Name, &v.Category, &v.SubscriptionID, &v.Plan, &v.AmountMinor, &v.Status); err != nil {
			rows.Close()
			return nil, err
		}
		snap.Vendors = append(snap.Vendors, v)
	}
	rows.Close()

	rows, err = db.Query(`SELECT status, COUNT(*) FROM invoices GROUP BY status`)
	if err != nil {
		return nil, fmt.Errorf("query invoice summary: %w", err)
	}
	for rows.Next() {
		var status string
		var count int
		if err := rows.Scan(&status, &count); err != nil {
			rows.Close()
			return nil, err
		}
		snap.InvoiceSummary[status] = count
	}
	rows.Close()

	rows, err = db.Query(`
		SELECT id, vendor_id, amount_minor, status, refunded_amount_minor
		FROM invoices WHERE status IN ('refunded', 'contested') ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("query touched invoices: %w", err)
	}
	for rows.Next() {
		var inv Invoice
		if err := rows.Scan(&inv.ID, &inv.VendorID, &inv.AmountMinor, &inv.Status, &inv.RefundedAmountMinor); err != nil {
			rows.Close()
			return nil, err
		}
		snap.Invoices = append(snap.Invoices, inv)
	}
	rows.Close()

	rows, err = db.Query(`
		SELECT id, vendor_id, amount_minor, status, refunded_amount_minor
		FROM invoices WHERE status IN ('open', 'paid') ORDER BY id LIMIT 15`)
	if err != nil {
		return nil, fmt.Errorf("query sample open invoices: %w", err)
	}
	for rows.Next() {
		var inv Invoice
		if err := rows.Scan(&inv.ID, &inv.VendorID, &inv.AmountMinor, &inv.Status, &inv.RefundedAmountMinor); err != nil {
			rows.Close()
			return nil, err
		}
		snap.SampleOpenInvoices = append(snap.SampleOpenInvoices, inv)
	}
	rows.Close()

	return snap, rows.Err()
}

// EnsureWorld applies the world schema and seeds it exactly once (guarded by
// the vendors table being empty). Safe to call on every boot.
func EnsureWorld(db *sql.DB) error {
	if _, err := db.Exec(worldSchema); err != nil {
		return fmt.Errorf("apply world schema: %w", err)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM vendors`).Scan(&count); err != nil {
		return fmt.Errorf("check seed state: %w", err)
	}
	if count > 0 {
		return nil
	}
	if _, err := db.Exec(seedSQL); err != nil {
		return fmt.Errorf("seed world: %w", err)
	}
	return nil
}

// ResetWorld wipes the fake world back to its deterministic seed (same
// vendor/invoice/account ids every time -- seed.sql has a fixed RNG seed).
// It exists because the world is shared across every session: run the demo
// scenario twice in a row without this and the second run's
// cancel_subscription calls legitimately fail (nothing active left to
// cancel), not "block" -- which looks like a bug on camera when you're trying
// to demo the policy gate, not real exhaustion of fake data. It never touches
// the ledger (sessions/intents/approvals) -- past runs keep their history,
// they just now refer to a freshly-reset world.
func ResetWorld(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("reset world: begin: %w", err)
	}
	defer tx.Rollback()

	// Children before parents, so SQLite's foreign-key checks don't trip.
	for _, table := range []string{"journal_entries", "invoices", "subscriptions", "vendors", "accounts"} {
		if _, err := tx.Exec("DELETE FROM " + table); err != nil {
			return fmt.Errorf("reset world: clear %s: %w", table, err)
		}
	}
	if _, err := tx.Exec(seedSQL); err != nil {
		return fmt.Errorf("reset world: reseed: %w", err)
	}
	return tx.Commit()
}
