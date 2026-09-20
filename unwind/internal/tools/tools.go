package tools

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"time"
)

// newID returns a short random id with the given prefix, e.g. "rfd_a1b2c3d4".
func newID(prefix string) string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return prefix + "_" + hex.EncodeToString(b)
}

func argString(args map[string]any, key string) (string, error) {
	v, ok := args[key]
	if !ok {
		return "", fmt.Errorf("missing required arg %q", key)
	}
	s, ok := v.(string)
	if !ok || s == "" {
		return "", fmt.Errorf("arg %q must be a non-empty string", key)
	}
	return s, nil
}

// argAmountMinor reads an "amount" arg as an integer number of paise.
// JSON numbers decode as float64, hence the conversion here rather than at
// every call site.
func argAmountMinor(args map[string]any, key string) (int64, error) {
	v, ok := args[key]
	if !ok {
		return 0, fmt.Errorf("missing required arg %q", key)
	}
	switch n := v.(type) {
	case float64:
		return int64(n), nil
	case int64:
		return n, nil
	case int:
		return int64(n), nil
	default:
		return 0, fmt.Errorf("arg %q must be a number", key)
	}
}

// ---------------------------------------------------------------------------
// cancel_subscription -- reversible.
//
// Args: {"vendor_id": string}. DECISIONS.md M: a vendor is assumed to have at
// most one active subscription, so vendor_id alone identifies the row to
// cancel (SPEC.md's own signature -- cancel_subscription(vendor_id) -- has no
// room for a subscription_id, so this is the reading that keeps the tool
// callable as specified).
// ---------------------------------------------------------------------------

type cancelSubscriptionTool struct{ db *sql.DB }

func (t *cancelSubscriptionTool) Name() string          { return "cancel_subscription" }
func (t *cancelSubscriptionTool) Reversibility() Class   { return Reversible }
func (t *cancelSubscriptionTool) AmountMinor(map[string]any) int64 { return 0 }

func (t *cancelSubscriptionTool) findActiveSubscription(vendorID string) (map[string]any, error) {
	row := t.db.QueryRow(`
		SELECT id, vendor_id, account_id, plan, amount_minor, status, started_at, cancelled_at
		FROM subscriptions WHERE vendor_id = ? AND status = 'active' LIMIT 1`, vendorID)
	var id, vid, accID, plan, status string
	var amount int64
	var startedAt string
	var cancelledAt sql.NullString
	if err := row.Scan(&id, &vid, &accID, &plan, &amount, &status, &startedAt, &cancelledAt); err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("no active subscription for vendor %s", vendorID)
		}
		return nil, err
	}
	m := map[string]any{
		"id": id, "vendor_id": vid, "account_id": accID, "plan": plan,
		"amount_minor": amount, "status": status, "started_at": startedAt,
	}
	if cancelledAt.Valid {
		m["cancelled_at"] = cancelledAt.String
	} else {
		m["cancelled_at"] = nil
	}
	return m, nil
}

func (t *cancelSubscriptionTool) Capture(ctx context.Context, args map[string]any) (map[string]any, error) {
	vendorID, err := argString(args, "vendor_id")
	if err != nil {
		return nil, err
	}
	return t.findActiveSubscription(vendorID)
}

func (t *cancelSubscriptionTool) Execute(ctx context.Context, args map[string]any) (map[string]any, error) {
	vendorID, err := argString(args, "vendor_id")
	if err != nil {
		return nil, err
	}
	prior, err := t.findActiveSubscription(vendorID)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC().Format(time.RFC3339)
	res, err := t.db.ExecContext(ctx, `
		UPDATE subscriptions SET status = 'cancelled', cancelled_at = ?
		WHERE id = ? AND status = 'active'`, now, prior["id"])
	if err != nil {
		return nil, err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return nil, fmt.Errorf("subscription %s was not active", prior["id"])
	}
	return map[string]any{
		"vendor_id":       vendorID,
		"subscription_id": prior["id"],
		"status":          "cancelled",
		"cancelled_at":    now,
	}, nil
}

func (t *cancelSubscriptionTool) Compensate(ctx context.Context, comp map[string]any) error {
	prior, ok := comp["prior"].(map[string]any)
	if !ok {
		return fmt.Errorf("compensation missing prior state")
	}
	id, _ := prior["id"].(string)
	if id == "" {
		return fmt.Errorf("compensation prior state missing subscription id")
	}
	var cancelledAt any
	cancelledAt = prior["cancelled_at"]
	_, err := t.db.ExecContext(ctx, `
		UPDATE subscriptions SET status = 'active', cancelled_at = ?
		WHERE id = ?`, cancelledAt, id)
	return err
}

// ---------------------------------------------------------------------------
// issue_refund -- partial.
//
// Args: {"invoice_id": string, "amount": number (paise)}.
// ---------------------------------------------------------------------------

type issueRefundTool struct{ db *sql.DB }

func (t *issueRefundTool) Name() string        { return "issue_refund" }
func (t *issueRefundTool) Reversibility() Class { return Partial }
func (t *issueRefundTool) AmountMinor(args map[string]any) int64 {
	amt, _ := argAmountMinor(args, "amount")
	return amt
}

func (t *issueRefundTool) findInvoice(invoiceID string) (map[string]any, error) {
	row := t.db.QueryRow(`
		SELECT id, vendor_id, amount_minor, status, refunded_amount_minor, issued_at
		FROM invoices WHERE id = ?`, invoiceID)
	var id, vendorID, status, issuedAt string
	var amount, refunded int64
	if err := row.Scan(&id, &vendorID, &amount, &status, &refunded, &issuedAt); err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("invoice %s not found", invoiceID)
		}
		return nil, err
	}
	return map[string]any{
		"id": id, "vendor_id": vendorID, "amount_minor": amount,
		"status": status, "refunded_amount_minor": refunded, "issued_at": issuedAt,
	}, nil
}

func (t *issueRefundTool) Capture(ctx context.Context, args map[string]any) (map[string]any, error) {
	invoiceID, err := argString(args, "invoice_id")
	if err != nil {
		return nil, err
	}
	return t.findInvoice(invoiceID)
}

const refundSettlementAccount = "acc_vendor_settlement"

func (t *issueRefundTool) Execute(ctx context.Context, args map[string]any) (map[string]any, error) {
	invoiceID, err := argString(args, "invoice_id")
	if err != nil {
		return nil, err
	}
	amount, err := argAmountMinor(args, "amount")
	if err != nil {
		return nil, err
	}
	if amount <= 0 {
		return nil, fmt.Errorf("refund amount must be positive")
	}
	prior, err := t.findInvoice(invoiceID)
	if err != nil {
		return nil, err
	}
	refundID := newID("rfd")
	now := time.Now().UTC().Format(time.RFC3339)

	tx, err := t.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `
		UPDATE invoices SET status = 'refunded', refunded_amount_minor = refunded_amount_minor + ?
		WHERE id = ?`, amount, invoiceID); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE accounts SET balance_minor = balance_minor - ? WHERE id = ?`, amount, refundSettlementAccount); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO journal_entries (id, account_id, amount_minor, kind, ref, created_at)
		VALUES (?, ?, ?, 'refund', ?, ?)`, newID("je"), refundSettlementAccount, -amount, refundID, now); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}

	_ = prior
	return map[string]any{
		"refund_id":    refundID,
		"invoice_id":   invoiceID,
		"amount_minor": amount,
	}, nil
}

func (t *issueRefundTool) Compensate(ctx context.Context, comp map[string]any) error {
	prior, _ := comp["prior"].(map[string]any)
	result, _ := comp["result"].(map[string]any)
	if prior == nil || result == nil {
		return fmt.Errorf("compensation missing prior state or execute result")
	}
	invoiceID, _ := prior["id"].(string)
	refundID, _ := result["refund_id"].(string)
	amount, err := argAmountMinor(result, "amount_minor")
	if err != nil {
		return fmt.Errorf("compensation result missing amount_minor: %w", err)
	}
	now := time.Now().UTC().Format(time.RFC3339)

	tx, err := t.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// Post the reversing journal entry -- money flows back into settlement.
	if _, err := tx.ExecContext(ctx, `
		UPDATE accounts SET balance_minor = balance_minor + ? WHERE id = ?`, amount, refundSettlementAccount); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO journal_entries (id, account_id, amount_minor, kind, ref, created_at)
		VALUES (?, ?, ?, 'reversal', ?, ?)`, newID("je"), refundSettlementAccount, amount, refundID, now); err != nil {
		return err
	}
	// Flag the invoice as contested rather than silently reverting it -- a
	// reversed refund is a dispute, not an undo (SPEC.md's own wording).
	if _, err := tx.ExecContext(ctx, `
		UPDATE invoices SET status = 'contested' WHERE id = ?`, invoiceID); err != nil {
		return err
	}
	return tx.Commit()
}

// ---------------------------------------------------------------------------
// transfer_funds -- irreversible. Exists to prove the policy gate, not the
// compensation engine, is the answer for this class.
//
// Args: {"from": account id, "to": account id, "amount": number (paise)}.
// ---------------------------------------------------------------------------

type transferFundsTool struct{ db *sql.DB }

func (t *transferFundsTool) Name() string        { return "transfer_funds" }
func (t *transferFundsTool) Reversibility() Class { return Irreversible }
func (t *transferFundsTool) AmountMinor(args map[string]any) int64 {
	amt, _ := argAmountMinor(args, "amount")
	return amt
}

// Capture is a no-op: there is nothing to reverse for an irreversible
// transfer (SPEC.md is explicit about this).
func (t *transferFundsTool) Capture(ctx context.Context, args map[string]any) (map[string]any, error) {
	return map[string]any{}, nil
}

func (t *transferFundsTool) Execute(ctx context.Context, args map[string]any) (map[string]any, error) {
	from, err := argString(args, "from")
	if err != nil {
		return nil, err
	}
	to, err := argString(args, "to")
	if err != nil {
		return nil, err
	}
	amount, err := argAmountMinor(args, "amount")
	if err != nil {
		return nil, err
	}
	if amount <= 0 {
		return nil, fmt.Errorf("transfer amount must be positive")
	}

	tx, err := t.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	var balance int64
	if err := tx.QueryRowContext(ctx, `SELECT balance_minor FROM accounts WHERE id = ?`, from).Scan(&balance); err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("account %s not found", from)
		}
		return nil, err
	}
	if balance < amount {
		return nil, fmt.Errorf("insufficient funds in %s: have %d, need %d", from, balance, amount)
	}

	transferID := newID("txf")
	now := time.Now().UTC().Format(time.RFC3339)

	if _, err := tx.ExecContext(ctx, `UPDATE accounts SET balance_minor = balance_minor - ? WHERE id = ?`, amount, from); err != nil {
		return nil, err
	}
	if res, err := tx.ExecContext(ctx, `UPDATE accounts SET balance_minor = balance_minor + ? WHERE id = ?`, amount, to); err != nil {
		return nil, err
	} else if n, _ := res.RowsAffected(); n == 0 {
		return nil, fmt.Errorf("account %s not found", to)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO journal_entries (id, account_id, amount_minor, kind, ref, created_at) VALUES (?, ?, ?, 'transfer', ?, ?)`,
		newID("je"), from, -amount, transferID, now); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO journal_entries (id, account_id, amount_minor, kind, ref, created_at) VALUES (?, ?, ?, 'transfer', ?, ?)`,
		newID("je"), to, amount, transferID, now); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}

	return map[string]any{
		"transfer_id":  transferID,
		"from":         from,
		"to":           to,
		"amount_minor": amount,
	}, nil
}

// Compensate always fails: transfer_funds is irreversible by design. The
// engine catches ErrUncompensable and marks the intent uncompensable rather
// than treating this as a rollback failure.
func (t *transferFundsTool) Compensate(ctx context.Context, comp map[string]any) error {
	return ErrUncompensable
}
