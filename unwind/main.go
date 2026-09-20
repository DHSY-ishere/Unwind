// Command unwind is a transactional side-effect layer for AI agents: a
// write-ahead ledger plus a compensation engine sitting between an agent and
// the real tools it calls.
package main

import (
	"bufio"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/DHSY-ishere/unwind/internal/api"
	"github.com/DHSY-ishere/unwind/internal/engine"
	"github.com/DHSY-ishere/unwind/internal/ledger"
	"github.com/DHSY-ishere/unwind/internal/tools"
)

var (
	dbPath     string
	policyPath string
	addr       string
)

func main() {
	root := &cobra.Command{
		Use:           "unwind",
		Short:         "Transactional side effects for AI agents",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.PersistentFlags().StringVar(&dbPath, "db", "unwind.db", "path to the ledger database")
	root.PersistentFlags().StringVar(&policyPath, "policy", "policy.yaml", "path to the policy file")

	serve := &cobra.Command{
		Use:   "serve",
		Short: "Run the Unwind proxy server",
		RunE:  runServe,
	}
	serve.Flags().StringVar(&addr, "addr", ":8080", "listen address")

	root.AddCommand(
		serve,
		newDemoCmd(),
		newTimelineCmd(),
		newRollbackCmd(),
	)

	if err := root.Execute(); err != nil {
		log.Fatalf("unwind: %v", err)
	}
}

// openEngine wires the ledger, seeded world, policy and engine together --
// the same construction serve and every CLI subcommand shares, so there is
// exactly one place that assembles the app.
func openEngine() (*ledger.Ledger, *engine.Engine, error) {
	pol, err := engine.LoadPolicy(policyPath)
	if err != nil {
		return nil, nil, err
	}
	led, err := ledger.Open(dbPath)
	if err != nil {
		return nil, nil, err
	}
	if err := tools.EnsureWorld(led.DB); err != nil {
		led.Close()
		return nil, nil, err
	}
	registry := tools.NewRegistry(led.DB)
	eng := engine.New(led, registry, pol)
	return led, eng, nil
}

// loadDotEnv reads simple KEY=VALUE lines from .env (if present) into the
// process environment, without overwriting anything already set. It exists
// so the real-agent driver just works on this machine without the operator
// exporting anything first -- while keeping the key itself out of source
// control (.env is gitignored; a key committed to a public repo is a key
// that gets scraped).
func loadDotEnv(path string) {
	f, err := os.Open(path)
	if err != nil {
		return // no .env is the normal case, not an error
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, found := strings.Cut(line, "=")
		if !found {
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.Trim(strings.TrimSpace(value), `"'`)
		if key != "" && os.Getenv(key) == "" {
			os.Setenv(key, value)
		}
	}
}

func runServe(cmd *cobra.Command, args []string) error {
	loadDotEnv(".env")

	led, eng, err := openEngine()
	if err != nil {
		return err
	}
	defer led.Close()

	srv := &api.Server{Ledger: led, Engine: eng, Broker: api.NewBroker()}

	log.SetFlags(log.Ltime)
	log.Printf("ledger   %s", dbPath)
	log.Printf("policy   %s (mode=%s, cap=%d paise over %d mutations)",
		policyPath, eng.Policy.Mode, eng.Policy.Caps.MaxTotalAmountMinor, eng.Policy.Caps.MaxMutationsPerSession)
	log.Printf("listening on %s", addr)

	return http.ListenAndServe(addr, srv.Routes())
}

// stub registers a command that is not built yet, naming the block that lands it.
func stub(name, short, block string) *cobra.Command {
	return &cobra.Command{
		Use:   name,
		Short: short,
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Fprintf(os.Stderr, "unwind %s: not implemented yet (%s)\n", name, block)
			return nil
		},
	}
}
