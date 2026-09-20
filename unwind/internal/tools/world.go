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
