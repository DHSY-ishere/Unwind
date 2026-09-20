// Package ledger owns the SQLite database: the write-ahead record of every
// mutating call (sessions, intents, approvals). It is the only package that
// speaks SQL.
package ledger

import (
	"database/sql"
	"fmt"

	_ "modernc.org/sqlite"
)

// schema is applied on every open. Enum values in the comments are the
// authoritative set; see DECISIONS.md C and E for the additions beyond SPEC.md.
const schema = `
CREATE TABLE IF NOT EXISTS sessions (
    id          TEXT PRIMARY KEY,
    created_at  TIMESTAMP NOT NULL,
    policy_mode TEXT NOT NULL,   -- off | dryrun | enforce
    status      TEXT NOT NULL    -- active | rolled_back | partially_rolled_back
);

CREATE TABLE IF NOT EXISTS intents (
    id               TEXT PRIMARY KEY,
    session_id       TEXT NOT NULL REFERENCES sessions(id),
    seq              INTEGER NOT NULL,
    tool             TEXT NOT NULL,
    args_json        TEXT NOT NULL,
    idempotency_key  TEXT NOT NULL,
    reversibility    TEXT NOT NULL, -- reversible | partial | irreversible
                                    -- (read tools are never ledgered)
    status           TEXT NOT NULL, -- pending | committed | failed | blocked
                                    -- | awaiting_approval | denied
                                    -- | compensated | uncompensable
                                    -- | failed_compensation
    amount_minor     INTEGER NOT NULL DEFAULT 0,
    result_json      TEXT,
    compensation_json TEXT,
    created_at       TIMESTAMP NOT NULL,
    settled_at       TIMESTAMP,
    UNIQUE (session_id, idempotency_key),
    UNIQUE (session_id, seq)
);

CREATE INDEX IF NOT EXISTS idx_intents_session_seq ON intents(session_id, seq);

CREATE TABLE IF NOT EXISTS approvals (
    id         TEXT PRIMARY KEY,
    intent_id  TEXT NOT NULL REFERENCES intents(id),
    decision   TEXT NOT NULL, -- approved | denied
    decided_at TIMESTAMP NOT NULL
);
`

// Ledger is a handle on the database. There is exactly one implementation and
// no interface over it by design.
type Ledger struct {
	DB *sql.DB
}

// Open opens (creating if absent) the database at path and applies the schema.
// Connections are capped at one: every write in Unwind is serialized through a
// single writer, which keeps `seq` monotonic without extra locking.
func Open(path string) (*Ledger, error) {
	dsn := fmt.Sprintf("file:%s?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)", path)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	db.SetMaxOpenConns(1)
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("ping %s: %w", path, err)
	}
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("apply schema: %w", err)
	}
	return &Ledger{DB: db}, nil
}

func (l *Ledger) Close() error { return l.DB.Close() }
