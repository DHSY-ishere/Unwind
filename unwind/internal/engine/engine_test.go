package engine

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/DHSY-ishere/unwind/internal/ledger"
	"github.com/DHSY-ishere/unwind/internal/tools"
)

// fakeTool is a minimal tools.Tool for exercising the rollback walk without
// the real seeded world -- it lets each test dictate exactly what Compensate
// does and records the order Compensate was called in.
type fakeTool struct {
	name          string
	compensateErr error     // returned by every Compensate call
	calls         *[]string // append tool name here on each Compensate call
}

func (f *fakeTool) Name() string                     { return f.name }
func (f *fakeTool) Reversibility() tools.Class       { return tools.Reversible }
func (f *fakeTool) AmountMinor(map[string]any) int64 { return 0 }
func (f *fakeTool) Execute(context.Context, map[string]any) (map[string]any, error) {
	return map[string]any{"ok": true}, nil
}
func (f *fakeTool) Capture(context.Context, map[string]any) (map[string]any, error) {
	return map[string]any{"prior": f.name}, nil
}
func (f *fakeTool) Compensate(ctx context.Context, comp map[string]any) error {
	if f.calls != nil {
		*f.calls = append(*f.calls, f.name)
	}
	return f.compensateErr
}

// testEngine builds an Engine over a fresh temp-file SQLite ledger and an
// empty tool registry the caller populates with fakeTool instances.
func testEngine(t *testing.T) (*Engine, *[]string) {
	t.Helper()
	led, err := ledger.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open ledger: %v", err)
	}
	t.Cleanup(func() { led.Close() })

	registry := tools.NewEmptyRegistry()
	calls := &[]string{}
	for _, name := range []string{"toolA", "toolB", "toolC"} {
		registry.Register(&fakeTool{name: name, calls: calls})
	}

	pol := &Policy{Mode: ModeEnforce, Caps: Caps{PerTool: map[string]int{}}}
	return New(led, registry, pol), calls
}

// act is a small helper: fire one Act call and fail the test if it doesn't
// commit (every case here needs committed intents to roll back).
func act(t *testing.T, e *Engine, sessionID, tool string) *ledger.Intent {
	t.Helper()
	intent, err := e.Act(context.Background(), sessionID, tool, map[string]any{}, tool+"-"+sessionID)
	if err != nil {
		t.Fatalf("act(%s): %v", tool, err)
	}
	if intent.Status != "committed" {
		t.Fatalf("act(%s): got status %q, want committed", tool, intent.Status)
	}
	return intent
}

// registerWith swaps out one tool in e's registry for a fresh fakeTool with
// the given Compensate error, so a single test can control just one tool's
// rollback outcome.
func registerWith(e *Engine, name string, compensateErr error, calls *[]string) {
	e.Tools.Register(&fakeTool{name: name, compensateErr: compensateErr, calls: calls})
}

func TestRollback_DescendingSeqOrder(t *testing.T) {
	e, calls := testEngine(t)
	ctx := context.Background()
	sessionID := "s-order"

	act(t, e, sessionID, "toolA") // seq 1
	act(t, e, sessionID, "toolB") // seq 2
	act(t, e, sessionID, "toolC") // seq 3

	summary, err := e.Rollback(ctx, sessionID)
	if err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if summary.Compensated != 3 || summary.Uncompensable != 0 || summary.Failed != 0 {
		t.Fatalf("summary = %+v, want {3 0 0}", summary)
	}

	want := []string{"toolC", "toolB", "toolA"} // descending seq
	got := *calls
	if len(got) != len(want) {
		t.Fatalf("compensate call order = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("compensate call order = %v, want %v", got, want)
		}
	}

	sess, err := e.Ledger.GetSession(sessionID)
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	if sess.Status != "rolled_back" {
		t.Fatalf("session status = %q, want rolled_back", sess.Status)
	}
}

func TestRollback_RerunIsANoOp(t *testing.T) {
	e, calls := testEngine(t)
	ctx := context.Background()
	sessionID := "s-rerun"

	act(t, e, sessionID, "toolA")
	act(t, e, sessionID, "toolB")

	first, err := e.Rollback(ctx, sessionID)
	if err != nil {
		t.Fatalf("first rollback: %v", err)
	}
	if first.Compensated != 2 {
		t.Fatalf("first rollback compensated = %d, want 2", first.Compensated)
	}
	callsAfterFirst := len(*calls)

	second, err := e.Rollback(ctx, sessionID)
	if err != nil {
		t.Fatalf("second rollback: %v", err)
	}
	if second.Compensated != 0 || second.Uncompensable != 0 || second.Failed != 0 {
		t.Fatalf("second rollback = %+v, want {0 0 0}", second)
	}
	if len(*calls) != callsAfterFirst {
		t.Fatalf("second rollback invoked Compensate again: %d calls before, %d after", callsAfterFirst, len(*calls))
	}

	sess, err := e.Ledger.GetSession(sessionID)
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	if sess.Status != "rolled_back" {
		t.Fatalf("session status after rerun = %q, want rolled_back (unchanged)", sess.Status)
	}
}

func TestRollback_IrreversibleReportsUncompensableAndContinues(t *testing.T) {
	e, calls := testEngine(t)
	ctx := context.Background()
	sessionID := "s-uncompensable"

	act(t, e, sessionID, "toolA") // seq 1, will compensate fine
	registerWith(e, "toolB", tools.ErrUncompensable, calls)
	act(t, e, sessionID, "toolB") // seq 2, irreversible
	act(t, e, sessionID, "toolC") // seq 3, will compensate fine

	summary, err := e.Rollback(ctx, sessionID)
	if err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if summary.Uncompensable != 1 {
		t.Fatalf("uncompensable = %d, want 1", summary.Uncompensable)
	}
	// The walk must not have aborted at the uncompensable intent: both
	// neighbours (seq 1 and seq 3) still got compensated.
	if summary.Compensated != 2 {
		t.Fatalf("compensated = %d, want 2 (walk must continue past the uncompensable intent)", summary.Compensated)
	}
	if summary.Failed != 0 {
		t.Fatalf("failed = %d, want 0", summary.Failed)
	}

	intents, err := e.Ledger.ListIntents(sessionID)
	if err != nil {
		t.Fatalf("list intents: %v", err)
	}
	statuses := map[int]string{}
	for _, i := range intents {
		statuses[i.Seq] = i.Status
	}
	if statuses[1] != "compensated" || statuses[2] != "uncompensable" || statuses[3] != "compensated" {
		t.Fatalf("intent statuses by seq = %+v, want {1:compensated 2:uncompensable 3:compensated}", statuses)
	}

	// An uncompensable intent (as opposed to a failed one) does not, on its
	// own, downgrade the session below a clean rollback.
	sess, err := e.Ledger.GetSession(sessionID)
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	if sess.Status != "rolled_back" {
		t.Fatalf("session status = %q, want rolled_back", sess.Status)
	}
}

func TestRollback_CompensateFailureDoesNotAbortTheWalk(t *testing.T) {
	e, calls := testEngine(t)
	ctx := context.Background()
	sessionID := "s-failure"
	boom := errors.New("boom: downstream reversal rejected")

	act(t, e, sessionID, "toolA") // seq 1, fine
	registerWith(e, "toolB", boom, calls)
	act(t, e, sessionID, "toolB") // seq 2, fails to compensate
	act(t, e, sessionID, "toolC") // seq 3, fine

	summary, err := e.Rollback(ctx, sessionID)
	if err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if summary.Failed != 1 {
		t.Fatalf("failed = %d, want 1", summary.Failed)
	}
	// The walk must continue past the failure: both other intents still
	// compensated rather than the whole rollback aborting.
	if summary.Compensated != 2 {
		t.Fatalf("compensated = %d, want 2 (walk must continue past the failed compensation)", summary.Compensated)
	}

	intents, err := e.Ledger.ListIntents(sessionID)
	if err != nil {
		t.Fatalf("list intents: %v", err)
	}
	statuses := map[int]string{}
	for _, i := range intents {
		statuses[i.Seq] = i.Status
	}
	if statuses[1] != "compensated" || statuses[2] != "failed_compensation" || statuses[3] != "compensated" {
		t.Fatalf("intent statuses by seq = %+v, want {1:compensated 2:failed_compensation 3:compensated}", statuses)
	}

	// A real compensation failure DOES downgrade the session.
	sess, err := e.Ledger.GetSession(sessionID)
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	if sess.Status != "partially_rolled_back" {
		t.Fatalf("session status = %q, want partially_rolled_back", sess.Status)
	}
}
