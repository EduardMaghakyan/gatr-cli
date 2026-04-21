package cli

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/spf13/cobra"

	schema "github.com/EduardMaghakyan/gatr-cli/pkg/schema"
	gstripe "github.com/EduardMaghakyan/gatr-cli/pkg/stripe"
)

type validateOptions struct {
	configPath  string
	checkStripe bool

	// The Stripe-facing inputs mirror cmd_push.go so operators can
	// wire `validate --check-stripe` into CI with the same env vars.
	projectID string
	secretKey string
	credFile  string
}

func newValidateCmd() *cobra.Command {
	opts := &validateOptions{}
	cmd := &cobra.Command{
		Use:   "validate",
		Short: "Lint your gatr.yaml against the canonical schema",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runValidate(cmd.Context(), cmd.OutOrStdout(), opts)
		},
	}
	cmd.Flags().StringVarP(&opts.configPath, "config", "c", "gatr.yaml", "Path to gatr.yaml")
	cmd.Flags().BoolVar(&opts.checkStripe, "check-stripe", false, "Additionally verify every stripe_price_id / stripe_meter_id resolves in Stripe")
	cmd.Flags().StringVar(&opts.projectID, "project-id", "", "Override the gatr project namespace (default: $"+envProjectID+", then gatr.yaml's `project:`)")
	cmd.Flags().StringVar(&opts.secretKey, "key", "", "Stripe secret key (defaults to $STRIPE_SECRET_KEY or ~/.gatr/credentials.toml)")
	cmd.Flags().StringVar(&opts.credFile, "credentials", "", "Override ~/.gatr/credentials.toml path")
	return cmd
}

func runValidate(ctx context.Context, out io.Writer, opts *validateOptions) error {
	cfg, err := schema.ParseFileAndValidate(opts.configPath)
	if err != nil {
		var gerr *schema.Error
		if errors.As(err, &gerr) {
			printErr(out, fmt.Sprintf("%s — %s", gerr.Code, gerr.Message))
			fmt.Fprintln(out, subtleStyle.Render("  see https://gatr.dev/errors/"+gerr.Code))
		} else {
			printErr(out, err.Error())
		}
		return err
	}
	fmt.Fprintln(out, successStyle.Render(fmt.Sprintf(
		"✓ %s is valid — %d features, %d limits, %d credits, %d operations, %d metered prices, %d plans",
		opts.configPath,
		len(cfg.Features), len(cfg.Limits), len(cfg.Credits),
		len(cfg.Operations), len(cfg.MeteredPrices), len(cfg.Plans),
	)))

	if !opts.checkStripe {
		return nil
	}
	return runStripeResolveCheck(ctx, out, cfg, opts)
}

// stripeCheckRow is one line of the --check-stripe report. Kept in
// a struct so tests can assert on shape instead of stdout scraping.
type stripeCheckRow struct {
	// YamlPath is like "plans[pro].billing.monthly.stripe_price_id".
	YamlPath string
	// State is "ok" | "missing" | "unset". Unset means the yaml
	// value is null → `gatr push` will create it.
	State   string
	Message string
}

// runStripeResolveCheck lists the gatr-managed Stripe objects and
// verifies every non-null yaml `stripe_price_id` / `stripe_meter_id`
// resolves to one of them. Missing IDs are reported as failures; null
// placeholders are reported as "will be created on next push".
//
// Returns a non-nil error iff any yaml-referenced ID is missing in
// Stripe OR the Stripe client itself fails. Null-placeholders alone
// do NOT fail the check — they're the expected state pre-push.
func runStripeResolveCheck(ctx context.Context, out io.Writer, cfg *schema.Config, opts *validateOptions) error {
	projectID := resolveProjectID(opts.projectID, cfg.Project)
	if projectID == "" {
		printErr(out, "E502: no project namespace resolved")
		return &gstripe.Error{Code: gstripe.ErrCodeMissingProjectID, Message: "missing project namespace"}
	}
	client, err := gstripe.NewClient(gstripe.ClientOptions{
		SecretKey:      opts.secretKey,
		CredentialFile: opts.credFile,
		ProjectID:      projectID,
	})
	if err != nil {
		printErr(out, err.Error())
		return err
	}

	prices, err := client.ListManagedPrices(ctx)
	if err != nil {
		printErr(out, err.Error())
		return err
	}
	meters, err := client.ListManagedMeters(ctx)
	if err != nil {
		printErr(out, err.Error())
		return err
	}
	activePriceIDs := map[string]bool{}
	for _, p := range prices {
		if p.Active {
			activePriceIDs[p.StripeID] = true
		}
	}
	activeMeterIDs := map[string]bool{}
	for _, m := range meters {
		if m.Status == "active" {
			activeMeterIDs[m.StripeID] = true
		}
	}

	rows := collectCheckRows(cfg, activePriceIDs, activeMeterIDs)
	missing := renderCheckRows(out, rows)
	if missing > 0 {
		printErr(out, fmt.Sprintf("%d Stripe ID(s) referenced in yaml but not found in Stripe", missing))
		return &gstripe.Error{
			Code:    gstripe.ErrCodeStripeAPI,
			Message: fmt.Sprintf("%d yaml-referenced Stripe IDs missing", missing),
		}
	}
	return nil
}

// collectCheckRows walks the yaml and emits one row per stripe_* field.
// Returns rows in yaml-order so `--check-stripe` output matches the
// operator's mental map of the file.
func collectCheckRows(cfg *schema.Config, activePriceIDs, activeMeterIDs map[string]bool) []stripeCheckRow {
	var rows []stripeCheckRow
	for _, plan := range cfg.Plans {
		if plan.Billing == nil {
			continue
		}
		if plan.Billing.Monthly != nil {
			rows = append(rows, checkRow(
				fmt.Sprintf("plans[%s].billing.monthly.stripe_price_id", plan.ID),
				plan.Billing.Monthly.StripePriceID, activePriceIDs,
			))
		}
		if plan.Billing.Annual != nil {
			rows = append(rows, checkRow(
				fmt.Sprintf("plans[%s].billing.annual.stripe_price_id", plan.ID),
				plan.Billing.Annual.StripePriceID, activePriceIDs,
			))
		}
	}
	for _, mp := range cfg.MeteredPrices {
		rows = append(rows, checkRow(
			fmt.Sprintf("metered_prices[%s].stripe_meter_id", mp.ID),
			mp.StripeMeterID, activeMeterIDs,
		))
	}
	return rows
}

func checkRow(path string, yamlID *string, active map[string]bool) stripeCheckRow {
	if yamlID == nil || *yamlID == "" {
		return stripeCheckRow{YamlPath: path, State: "unset", Message: "→ will be created on next push"}
	}
	if active[*yamlID] {
		return stripeCheckRow{YamlPath: path, State: "ok", Message: *yamlID}
	}
	return stripeCheckRow{YamlPath: path, State: "missing", Message: *yamlID + " — not found in Stripe (or archived)"}
}

// renderCheckRows writes each row to out with a status glyph; returns
// the count of "missing" rows so the caller can error appropriately.
func renderCheckRows(out io.Writer, rows []stripeCheckRow) (missing int) {
	for _, r := range rows {
		var glyph string
		switch r.State {
		case "ok":
			glyph = successStyle.Render("✓")
		case "unset":
			glyph = subtleStyle.Render("·")
		case "missing":
			glyph = errorStyle.Render("✗")
			missing++
		}
		fmt.Fprintf(out, "  %s %s %s\n", glyph, codeStyle.Render(r.YamlPath), subtleStyle.Render(r.Message))
	}
	return missing
}
