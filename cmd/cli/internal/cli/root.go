// Package cli wires the gatr CLI commands.
package cli

import (
	"github.com/spf13/cobra"
)

// NewRoot returns the root cobra command.
func NewRoot(version string) *cobra.Command {
	root := &cobra.Command{
		Use:           "gatr",
		Short:         "Gatr CLI — entitlements, credits, and usage-based pricing for developers",
		Long:          rootLongHelp,
		Version:       version,
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.SetVersionTemplate("gatr {{.Version}}\n")
	root.AddCommand(newInitCmd())
	root.AddCommand(newValidateCmd())
	root.AddCommand(newTypegenCmd())
	root.AddCommand(newPushCmd())
	root.AddCommand(newImportCmd())
	return root
}

const rootLongHelp = `Gatr CLI — entitlements, credits, and usage-based pricing for developers.

Common commands:

  gatr init               Pick a template, scaffold gatr.yaml + sample SDK code
  gatr validate           Lint your gatr.yaml against the canonical schema
  gatr typegen --lang ts  Generate typed bindings for the SDK from gatr.yaml
  gatr push               Reconcile Stripe to match gatr.yaml
  gatr import             Read Stripe → emit a starter gatr.yaml

Run 'gatr <command> --help' for details. Docs: https://gatr.dev`
