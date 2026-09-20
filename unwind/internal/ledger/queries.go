package ledger

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"time"
)

// newID returns a short random id with the given prefix, e.g. "sn_a1b2c3d4".
func newID(prefix string) string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return prefix + "_" + hex.EncodeToString(b)
}

func now() string { return time.Now().UTC().Format(time.RFC3339Nano) }

// Session mirrors the sessions table.
type Session struct {
	ID          string `json:"id"`
	CreatedAt   string `json:"created_at"`
	PolicyMode  string `json:"policy_mode"`
	Status      string `json:"status"`
	IntentCount int    `json:"intent_count"` // only populated by ListSessions
}

// Intent mirrors the intents table. ResultJSON, CompensationJSON and
// SettledAt are empty strings when NULL.
type Intent struct {
	ID               string
	SessionID        string
	Seq              int
	Tool             string
	ArgsJSON         string
	IdempotencyKey   string
	Reversibility    string
	Status           string
	AmountMinor      int64
	ResultJSON       string
	CompensationJSON string
	CreatedAt        string
	SettledAt        string
}

func scanIntent(row interface{ Scan(...any) error }) (*Intent, error) {
	var i Intent
	var result, comp, settled sql.NullString
	if err := row.Scan(&i.ID, &i.SessionID, &i.Seq, &i.Tool, &i.ArgsJSON, &i.IdempotencyKey,
		&i.Reversibility, &i.Status, &i.AmountMinor, &result, &comp, &i.CreatedAt, &settled); err != nil {
		return nil, err
	}
	i.ResultJSON = result.String
	i.CompensationJSON = comp.String
	i.SettledAt = settled.String
	return &i, nil
}

const intentCols = `id, session_id, seq, tool, args_json, idempotency_key, reversibility, status, amount_minor, result_json, compensation_json, created_at, settled_at`

// GetOrCreateSession returns the session with id, creating it with
// defaultMode if it does not exist yet (DECISIONS.md I: the row wins over
// policy.yaml from creation onward).
func (l *Ledger) GetOrCreateSession(id, defaultMode string) (*Session, error) {
	s, err := l.GetSession(id)
	if err == nil {
		return s, nil
	}
	if err != sql.ErrNoRows {
		return nil, err
	}
	s = &Session{ID: id, CreatedAt: now(), PolicyMode: defaultMode, Status: "active"}
	_, err = l.DB.Exec(`INSERT INTO sessions (id, created_at, policy_mode, status) VALUES (?, ?, ?, ?)`,
		s.ID, s.CreatedAt, s.PolicyMode, s.Status)
	if err != nil {
		return nil, err
	}
	return s, nil
}

func (l *Ledger) GetSession(id string) (*Session, error) {
	row := l.DB.QueryRow(`SELECT id, created_at, policy_mode, status FROM sessions WHERE id = ?`, id)
	var s Session
	if err := row.Scan(&s.ID, &s.CreatedAt, &s.PolicyMode, &s.Status); err != nil {
		return nil, err
	}
	return &s, nil
}

// ListSessions returns every session, newest first, with its intent count
// (DECISIONS.md J).
func (l *Ledger) ListSessions() ([]*Session, error) {
	rows, err := l.DB.Query(`
		SELECT s.id, s.created_at, s.policy_mode, s.status, COUNT(i.id)
		FROM sessions s LEFT JOIN intents i ON i.session_id = s.id
		GROUP BY s.id ORDER BY s.created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Session
	for rows.Next() {
		var s Session
		if err := rows.Scan(&s.ID, &s.CreatedAt, &s.PolicyMode, &s.Status, &s.IntentCount); err != nil {
			return nil, err
		}
		out = append(out, &s)
	}
	return out, rows.Err()
}

func (l *Ledger) UpdateSessionStatus(id, status string) error {
	_, err := l.DB.Exec(`UPDATE sessions SET status = ? WHERE id = ?`, status, id)
	return err
}

// InsertPendingIntent writes intent step 1 of the pipeline: a durable record
// before any side effect, with a monotonic seq (guaranteed by the single
// writer connection -- see ledger.go's Open).
func (l *Ledger) InsertPendingIntent(sessionID, tool, argsJSON, idempotencyKey, reversibility string, amountMinor int64) (*Intent, error) {
	tx, err := l.DB.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	var seq int
	if err := tx.QueryRow(`SELECT COALESCE(MAX(seq), 0) + 1 FROM intents WHERE session_id = ?`, sessionID).Scan(&seq); err != nil {
		return nil, err
	}
	i := &Intent{
		ID: newID("int"), SessionID: sessionID, Seq: seq, Tool: tool, ArgsJSON: argsJSON,
		IdempotencyKey: idempotencyKey, Reversibility: reversibility, Status: "pending",
		AmountMinor: amountMinor, CreatedAt: now(),
	}
	_, err = tx.Exec(`
		INSERT INTO intents (id, session_id, seq, tool, args_json, idempotency_key, reversibility, status, amount_minor, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		i.ID, i.SessionID, i.Seq, i.Tool, i.ArgsJSON, i.IdempotencyKey, i.Reversibility, i.Status, i.AmountMinor, i.CreatedAt)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return i, nil
}

func (l *Ledger) GetIntent(id string) (*Intent, error) {
	row := l.DB.QueryRow(`SELECT `+intentCols+` FROM intents WHERE id = ?`, id)
	return scanIntent(row)
}

func (l *Ledger) FindByIdempotencyKey(sessionID, key string) (*Intent, error) {
	row := l.DB.QueryRow(`SELECT `+intentCols+` FROM intents WHERE session_id = ? AND idempotency_key = ?`, sessionID, key)
	return scanIntent(row)
}

func (l *Ledger) ListIntents(sessionID string) ([]*Intent, error) {
	return l.listIntents(sessionID, "ASC")
}

func (l *Ledger) ListIntentsDesc(sessionID string) ([]*Intent, error) {
	return l.listIntents(sessionID, "DESC")
}

func (l *Ledger) listIntents(sessionID, order string) ([]*Intent, error) {
	q := `SELECT ` + intentCols + ` FROM intents WHERE session_id = ? ORDER BY seq ` + order
	rows, err := l.DB.Query(q, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Intent
	for rows.Next() {
		i, err := scanIntent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, i)
	}
	return out, rows.Err()
}

// MarkCommitted flips pending -> committed, storing the execute result and
// the merged compensation record in the same statement (DECISIONS.md A).
func (l *Ledger) MarkCommitted(id, resultJSON, compensationJSON string) error {
	_, err := l.DB.Exec(`
		UPDATE intents SET status = 'committed', result_json = ?, compensation_json = ?, settled_at = ?
		WHERE id = ?`, resultJSON, compensationJSON, now(), id)
	return err
}

func (l *Ledger) MarkBlocked(id, reasonJSON string) error {
	_, err := l.DB.Exec(`UPDATE intents SET status = 'blocked', result_json = ?, settled_at = ? WHERE id = ?`, reasonJSON, now(), id)
	return err
}

func (l *Ledger) MarkFailed(id, reasonJSON string) error {
	_, err := l.DB.Exec(`UPDATE intents SET status = 'failed', result_json = ?, settled_at = ? WHERE id = ?`, reasonJSON, now(), id)
	return err
}

func (l *Ledger) MarkAwaitingApproval(id, reasonJSON string) error {
	_, err := l.DB.Exec(`UPDATE intents SET status = 'awaiting_approval', result_json = ? WHERE id = ?`, reasonJSON, id)
	return err
}

func (l *Ledger) MarkDenied(id, reasonJSON string) error {
	_, err := l.DB.Exec(`UPDATE intents SET status = 'denied', result_json = ?, settled_at = ? WHERE id = ?`, reasonJSON, now(), id)
	return err
}

// MarkCompensated, MarkUncompensable and MarkFailedCompensation are rollback
// outcomes. They deliberately leave settled_at untouched: it records when the
// real-world effect settled (commit time), not when it was later reversed.

func (l *Ledger) MarkCompensated(id string) error {
	_, err := l.DB.Exec(`UPDATE intents SET status = 'compensated' WHERE id = ?`, id)
	return err
}

func (l *Ledger) MarkUncompensable(id string) error {
	_, err := l.DB.Exec(`UPDATE intents SET status = 'uncompensable' WHERE id = ?`, id)
	return err
}

func (l *Ledger) MarkFailedCompensation(id, errMsg string) error {
	_, err := l.DB.Exec(`
		UPDATE intents SET status = 'failed_compensation',
		compensation_json = json_set(COALESCE(compensation_json, '{}'), '$.compensate_error', ?)
		WHERE id = ?`, errMsg, id)
	return err
}

// CountCommitted returns how many intents in the session are committed
// (DECISIONS.md D: mutation caps count committed only).
func (l *Ledger) CountCommitted(sessionID string) (int, error) {
	var n int
	err := l.DB.QueryRow(`SELECT COUNT(*) FROM intents WHERE session_id = ? AND status = 'committed'`, sessionID).Scan(&n)
	return n, err
}

func (l *Ledger) CountCommittedByTool(sessionID, tool string) (int, error) {
	var n int
	err := l.DB.QueryRow(`SELECT COUNT(*) FROM intents WHERE session_id = ? AND tool = ? AND status = 'committed'`, sessionID, tool).Scan(&n)
	return n, err
}

// SumReservedAmount returns the amount reserved against the cumulative cap:
// committed + awaiting_approval (DECISIONS.md D).
func (l *Ledger) SumReservedAmount(sessionID string) (int64, error) {
	var n int64
	err := l.DB.QueryRow(`
		SELECT COALESCE(SUM(amount_minor), 0) FROM intents
		WHERE session_id = ? AND status IN ('committed', 'awaiting_approval')`, sessionID).Scan(&n)
	return n, err
}

func (l *Ledger) InsertApproval(intentID, decision string) error {
	_, err := l.DB.Exec(`INSERT INTO approvals (id, intent_id, decision, decided_at) VALUES (?, ?, ?, ?)`,
		newID("apr"), intentID, decision, now())
	return err
}
