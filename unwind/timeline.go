package main

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/mattn/go-isatty"
	"github.com/spf13/cobra"

	"github.com/DHSY-ishere/unwind/internal/ledger"
)

// ANSI color codes. Applied only when stdout is a real terminal (isTTY),
// so piping `unwind timeline` output stays clean.
const (
	ansiReset  = "\x1b[0m"
	ansiDim    = "\x1b[2m"
	ansiBold   = "\x1b[1m"
	ansiRed    = "\x1b[31m"
	ansiGreen  = "\x1b[32m"
	ansiYellow = "\x1b[33m"
	ansiCyan   = "\x1b[36m"
	ansiGray   = "\x1b[90m"
)

var isTTY = isatty.IsTerminal(os.Stdout.Fd())

func colorize(code, s string) string {
	if !isTTY {
		return s
	}
	return code + s + ansiReset
}

// glyphFor returns a status glyph and the color to render both it and the
// status word in.
func glyphFor(status string) (glyph, color string) {
	switch status {
	case "committed":
		return "✔", ansiGreen // ✔
	case "compensated":
		return "↺", ansiCyan // ↺
	case "uncompensable":
		return "⚠", ansiYellow // ⚠
	case "blocked":
		return "✘", ansiRed // ✘
	case "failed", "failed_compensation":
		return "✗", ansiRed // ✗
	case "denied":
		return "✘", ansiRed
	case "awaiting_approval":
		return "⏳", ansiYellow // ⏳
	case "pending":
		return "◌", ansiGray // ◌
	default:
		return "?", ansiGray
	}
}

// formatRupees renders paise as a comma-grouped rupee string, e.g.
// 150000000 -> "₹15,00,000" (Indian digit grouping).
func formatRupees(paise int64) string {
	neg := paise < 0
	if neg {
		paise = -paise
	}
	rupees := paise / 100
	s := fmt.Sprintf("%d", rupees)
	// Indian grouping: last 3 digits, then groups of 2.
	if len(s) > 3 {
		head := s[:len(s)-3]
		tail := s[len(s)-3:]
		var grouped []string
		for len(head) > 2 {
			grouped = append([]string{head[len(head)-2:]}, grouped...)
			head = head[:len(head)-2]
		}
		if head != "" {
			grouped = append([]string{head}, grouped...)
		}
		s = strings.Join(grouped, ",") + "," + tail
	}
	sign := ""
	if neg {
		sign = "-"
	}
	return sign + "₹" + s
}

// argsSummary renders an intent's args as a compact key=value list, sorted
// for stable output.
func argsSummary(argsJSON string) string {
	var m map[string]any
	if err := json.Unmarshal([]byte(argsJSON), &m); err != nil || len(m) == 0 {
		return ""
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		v := m[k]
		var vs string
		switch n := v.(type) {
		case float64:
			// amount-shaped args read better in rupees than as a raw paise float.
			if k == "amount" {
				vs = formatRupees(int64(n))
			} else {
				vs = fmt.Sprintf("%v", int64(n))
			}
		default:
			vs = fmt.Sprintf("%v", v)
		}
		parts = append(parts, k+"="+vs)
	}
	return strings.Join(parts, " ")
}

// RenderTimeline prints the session header and one line per intent, in seq
// order, as a colored terminal tree. Shared by `unwind timeline` and the
// tail of `unwind rollback`.
func RenderTimeline(sess *ledger.Session, intents []*ledger.Intent) {
	fmt.Printf("%s %s  %s  %s\n",
		colorize(ansiBold, "Session"),
		colorize(ansiBold, sess.ID),
		colorize(ansiDim, "["+sess.PolicyMode+"]"),
		sessionStatusColored(sess.Status),
	)
	if len(intents) == 0 {
		fmt.Println(colorize(ansiDim, "  (no intents yet)"))
		return
	}

	// Column widths for alignment.
	toolW, statusW := 0, 0
	for _, i := range intents {
		if len(i.Tool) > toolW {
			toolW = len(i.Tool)
		}
		if len(i.Status) > statusW {
			statusW = len(i.Status)
		}
	}

	for idx, i := range intents {
		branch := "├─"
		if idx == len(intents)-1 {
			branch = "└─"
		}
		glyph, color := glyphFor(i.Status)
		amount := formatRupees(i.AmountMinor)
		if i.Status == "compensated" {
			amount = colorize(ansiDim, strikethrough(amount))
		}
		args := argsSummary(i.ArgsJSON)
		revTag := ""
		if i.Reversibility == "irreversible" {
			revTag = colorize(ansiYellow, " (irreversible)")
		}

		fmt.Printf("%s #%-3d %s  %-*s  %-40s  %10s  %s %s%s\n",
			colorize(ansiGray, branch),
			i.Seq,
			colorize(color, glyph),
			toolW, i.Tool,
			truncate(args, 40),
			amount,
			colorize(color, fmt.Sprintf("%-*s", statusW, i.Status)),
			revTag,
			reasonSuffix(i),
		)
	}
}

func sessionStatusColored(status string) string {
	switch status {
	case "active":
		return colorize(ansiGreen, status)
	case "rolled_back":
		return colorize(ansiCyan, status)
	case "partially_rolled_back":
		return colorize(ansiYellow, status)
	default:
		return status
	}
}

func strikethrough(s string) string {
	var b strings.Builder
	for _, r := range s {
		b.WriteRune(r)
		b.WriteRune('̶')
	}
	return b.String()
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	if n <= 1 {
		return s[:n]
	}
	return s[:n-1] + "…"
}

func reasonSuffix(i *ledger.Intent) string {
	if i.Status != "blocked" && i.Status != "failed" || i.ResultJSON == "" {
		return ""
	}
	var m map[string]any
	if json.Unmarshal([]byte(i.ResultJSON), &m) != nil {
		return ""
	}
	if rule, ok := m["policy_rule"].(string); ok && rule != "" {
		return colorize(ansiDim, "  ["+rule+"]")
	}
	if errMsg, ok := m["error"].(string); ok && errMsg != "" {
		return colorize(ansiDim, "  ["+truncate(errMsg, 30)+"]")
	}
	return ""
}

func newRollbackCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "rollback <session_id>",
		Short: "Compensate every committed intent in a session, in descending seq",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			led, eng, err := openEngine()
			if err != nil {
				return err
			}
			defer led.Close()

			sessionID := args[0]
			if _, err := led.GetSession(sessionID); err != nil {
				return fmt.Errorf("session %s not found", sessionID)
			}

			summary, err := eng.Rollback(cmd.Context(), sessionID)
			if err != nil {
				return err
			}
			fmt.Printf("%s compensated=%d  %s uncompensable=%d  %s failed=%d\n\n",
				colorize(ansiGreen, "↺"), summary.Compensated,
				colorize(ansiYellow, "⚠"), summary.Uncompensable,
				colorize(ansiRed, "✗"), summary.Failed)

			sess, err := led.GetSession(sessionID)
			if err != nil {
				return err
			}
			intents, err := led.ListIntents(sessionID)
			if err != nil {
				return err
			}
			RenderTimeline(sess, intents)
			return nil
		},
	}
}

func newTimelineCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "timeline <session_id>",
		Short: "Print the intent timeline for a session",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			led, _, err := openEngine()
			if err != nil {
				return err
			}
			defer led.Close()
			sess, err := led.GetSession(args[0])
			if err != nil {
				return fmt.Errorf("session %s not found", args[0])
			}
			intents, err := led.ListIntents(args[0])
			if err != nil {
				return err
			}
			RenderTimeline(sess, intents)
			return nil
		},
	}
}
