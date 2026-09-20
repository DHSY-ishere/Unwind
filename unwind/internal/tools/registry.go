package tools

import (
	"context"
	"database/sql"
	"errors"
)

// Class is a tool's reversibility class. There is no "read" class -- see
// DECISIONS.md L: list_vendors and the read class were cut from this build.
type Class string

const (
	Reversible   Class = "reversible"
	Partial      Class = "partial"
	Irreversible Class = "irreversible"
)

// ErrUncompensable is returned by Compensate for irreversible tools. The
// engine catches it and marks the intent uncompensable rather than failing
// the rollback walk.
var ErrUncompensable = errors.New("tool: operation cannot be compensated")

// Tool is the compensation-registry interface from SPEC.md. Adding a tool is
// one file plus one Register call, zero changes to engine or api.
type Tool interface {
	Name() string
	Reversibility() Class
	// AmountMinor extracts the amount this call moves, in paise. 0 for
	// non-financial tools (cancel_subscription).
	AmountMinor(args map[string]any) int64
	Execute(ctx context.Context, args map[string]any) (result map[string]any, err error)
	Capture(ctx context.Context, args map[string]any) (compensation map[string]any, err error)
	Compensate(ctx context.Context, compensation map[string]any) error
}

// Registry maps tool name -> Tool.
type Registry struct {
	tools map[string]Tool
}

// NewRegistry builds the registry with the three mutating tools, all sharing
// the world state in db.
func NewRegistry(db *sql.DB) *Registry {
	r := &Registry{tools: map[string]Tool{}}
	for _, t := range []Tool{
		&cancelSubscriptionTool{db: db},
		&issueRefundTool{db: db},
		&transferFundsTool{db: db},
	} {
		r.tools[t.Name()] = t
	}
	return r
}

// NewEmptyRegistry and Register exist for tests: engine_test.go registers
// fake tools to control Compensate's outcome deterministically, without
// standing up the real seeded world.
func NewEmptyRegistry() *Registry { return &Registry{tools: map[string]Tool{}} }

func (r *Registry) Register(t Tool) { r.tools[t.Name()] = t }

func (r *Registry) Get(name string) (Tool, bool) {
	t, ok := r.tools[name]
	return t, ok
}

// Names returns every registered tool name (used by policy validation and the
// demo driver's own sanity checks).
func (r *Registry) Names() []string {
	names := make([]string, 0, len(r.tools))
	for n := range r.tools {
		names = append(names, n)
	}
	return names
}
