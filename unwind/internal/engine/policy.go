// Package engine holds the act pipeline, policy evaluation and the rollback
// walk. Slice 0 implements policy loading only.
package engine

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// Mode values for policy.mode.
const (
	ModeOff     = "off"
	ModeDryrun  = "dryrun"
	ModeEnforce = "enforce"
)

// Policy is the in-memory policy, denominated entirely in minor units (paise).
// No rupee-valued number survives the loader -- see DECISIONS.md F.
type Policy struct {
	Mode  string `json:"mode"`
	Caps  Caps   `json:"caps"`
	Rules []Rule `json:"rules"`
}

type Caps struct {
	MaxMutationsPerSession int            `json:"max_mutations_per_session"`
	MaxTotalAmountMinor    int64          `json:"max_total_amount_minor"`
	PerTool                map[string]int `json:"per_tool"`
}

type Rule struct {
	Tool string `json:"tool"`
	// RequireApproval is "always" or empty.
	RequireApproval string `json:"require_approval,omitempty"`
	// RequireApprovalAboveMinor is a threshold in paise; 0 means unset.
	RequireApprovalAboveMinor int64 `json:"require_approval_above_minor,omitempty"`
}

// rawPolicy mirrors policy.yaml exactly, rupees and all. It exists so that the
// conversion to minor units happens in exactly one place.
type rawPolicy struct {
	Mode string `yaml:"mode"`
	Caps struct {
		MaxMutationsPerSession int            `yaml:"max_mutations_per_session"`
		MaxTotalAmountINR      int64          `yaml:"max_total_amount_inr"`
		PerTool                map[string]int `yaml:"per_tool"`
	} `yaml:"caps"`
	Rules []struct {
		Tool                    string `yaml:"tool"`
		RequireApproval         string `yaml:"require_approval"`
		RequireApprovalAboveINR int64  `yaml:"require_approval_above_inr"`
	} `yaml:"rules"`
}

// minorPerUnit is paise per rupee.
const minorPerUnit = 100

// LoadPolicy reads and validates policy.yaml, converting every monetary value
// to minor units.
func LoadPolicy(path string) (*Policy, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read policy: %w", err)
	}
	var raw rawPolicy
	if err := yaml.Unmarshal(b, &raw); err != nil {
		return nil, fmt.Errorf("parse policy: %w", err)
	}

	switch raw.Mode {
	case ModeOff, ModeDryrun, ModeEnforce:
	default:
		return nil, fmt.Errorf("parse policy: mode %q must be one of off|dryrun|enforce", raw.Mode)
	}

	p := &Policy{
		Mode: raw.Mode,
		Caps: Caps{
			MaxMutationsPerSession: raw.Caps.MaxMutationsPerSession,
			MaxTotalAmountMinor:    raw.Caps.MaxTotalAmountINR * minorPerUnit,
			PerTool:                raw.Caps.PerTool,
		},
	}
	if p.Caps.PerTool == nil {
		p.Caps.PerTool = map[string]int{}
	}
	for _, r := range raw.Rules {
		if r.RequireApproval != "" && r.RequireApproval != "always" {
			return nil, fmt.Errorf("parse policy: rule for %q: require_approval must be \"always\"", r.Tool)
		}
		p.Rules = append(p.Rules, Rule{
			Tool:                      r.Tool,
			RequireApproval:           r.RequireApproval,
			RequireApprovalAboveMinor: r.RequireApprovalAboveINR * minorPerUnit,
		})
	}
	return p, nil
}
