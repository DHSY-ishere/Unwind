// Command unwind is a transactional side-effect layer for AI agents: a
// write-ahead ledger plus a compensation engine sitting between an agent and
// the real tools it calls.
package main

import (
	"fmt"
	"log"
	"net/http"
	"os"

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
		stub("demo", "Drive the agent against a scenario", "slice 1.5"),
		stub("timeline", "Print the intent timeline for a session", "slice 1"),
		stub("rollback", "Compensate every committed intent in a session", "slice 4"),
		stub("approve", "Approve a held intent", "slice 5"),
		stub("deny", "Deny a held intent", "slice 5"),
	)

	if err := root.Execute(); err != nil {
		log.Fatalf("unwind: %v", err)
	}
}

func runServe(cmd *cobra.Command, args []string) error {
	pol, err := engine.LoadPolicy(policyPath)
	if err != nil {
		return err
	}
	led, err := ledger.Open(dbPath)
	if err != nil {
		return err
	}
	defer led.Close()

	srv := &api.Server{Ledger: led, Policy: pol}

	log.SetFlags(log.Ltime)
	log.Printf("ledger   %s", dbPath)
	log.Printf("policy   %s (mode=%s, cap=%d paise over %d mutations)",
		policyPath, pol.Mode, pol.Caps.MaxTotalAmountMinor, pol.Caps.MaxMutationsPerSession)
	log.Printf("listening on %s", addr)

	if err := http.ListenAndServe(addr, srv.Routes()); err != nil {
		return err
	}
	return nil
}

// stub registers a command that is not built yet, naming the slice that lands it.
func stub(name, short, slice string) *cobra.Command {
	return &cobra.Command{
		Use:   name,
		Short: short,
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Fprintf(os.Stderr, "unwind %s: not implemented yet (%s)\n", name, slice)
			return nil
		},
	}
}
