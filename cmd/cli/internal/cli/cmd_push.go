package cli

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	schema "github.com/EduardMaghakyan/gatr-cli/pkg/schema"
	"github.com/EduardMaghakyan/gatr-cli/pkg/schema/yamlpatch"
	gstripe "github.com/EduardMaghakyan/gatr-cli/pkg/stripe"
)

// pushOptions collects every tunable flag `gatr push` exposes. Kept
// as a struct so runPush is testable without touching cobra state.
type pushOptions struct {
	configPath string
	projectID  string
	secretKey  string
	credFile   string

	// auditLogPath overrides ~/.gatr/audit.log. For tests + non-HOME
	// deploys.
	auditLogPath string

	// autoPatch rewrites gatr.yaml in-place (preserving comments) to
	// carry the freshly-created Stripe IDs. Default: print-only — the
	// user copy-pastes. Opt-in because clobbering an uncommitted edit
	// is the kind of footgun OSS users won't forgive.
	autoPatch bool

	// force overrides the dirty-worktree guard on --auto-patch. Has
	// no effect without --auto-patch.
	force bool

	// dryRun prints the plan and exits — no prompt, no apply. Useful
	// in CI as a "show me what would change" step, or locally to eye
	// the diff before running an unadorned `gatr push`.
	dryRun bool

	// autoApprove skips the [y/N] apply confirmation. Required for
	// non-TTY runs that should execute (CI). v1 risk register flags
	// accidental Stripe pushes as a top-3 incident risk — so the
	// default is: TTY prompts, non-TTY refuses. --auto-approve is the
	// only way to bypass both.
	autoApprove bool

	// in is the reader the post-apply prompt reads from. nil falls
	// through to "no prompt, print only" — so tests don't have to
	// stub stdin every call.
	in io.Reader

	// interactive is true when stdin is a TTY. Auto-detected in the
	// cobra wiring; tests pass it explicitly. Gates two prompts: the
	// pre-apply [y/N] (see shouldApply) and the post-apply yaml-patch
	// [Y/n] (see decideAndPatch). Non-TTY runs skip patching silently
	// and refuse to apply unless --auto-approve is set.
	interactive bool
}

// envProjectID is the env var consulted when --project-id is unset.
// Matches the $STRIPE_SECRET_KEY ergonomics the push command already
// has via pkg/stripe.
const envProjectID = "GATR_PROJECT_ID"

// isTerminal reports whether f is a character device (a TTY). Pure
// stdlib — avoids pulling in golang.org/x/term for a one-line check
// that's portable enough for the CLI's use case.
func isTerminal(f *os.File) bool {
	info, err := f.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}

// resolveProjectID implements the documented precedence chain for the
// Stripe-metadata namespace: flag > env > yaml's `project:`. The yaml
// fallback removes CLI friction — the common case is one gatr project
// per repo, which the yaml already names.
//
// Shared between `gatr push` and `gatr validate --check-stripe` so
// both commands agree on what namespace to read from.
func resolveProjectID(flag, yamlProject string) string {
	if flag != "" {
		return flag
	}
	if env := os.Getenv(envProjectID); env != "" {
		return env
	}
	return yamlProject
}

func newPushCmd() *cobra.Command {
	opts := &pushOptions{}
	cmd := &cobra.Command{
		Use:   "push",
		Short: "Sync Stripe products / prices / meters to match gatr.yaml",
		Long: `gatr push — reconcile Stripe with your gatr.yaml.

Reads the YAML, lists the gatr-managed objects in your Stripe account,
computes a diff, prints it, and — after a [y/N] confirmation — applies
it. Idempotent: re-running a converged yaml against Stripe is a no-op.

Flags shape the flow:
  (none)          Print the plan and prompt for confirmation (TTY only).
  --dry-run       Print the plan and exit. No prompt, no apply.
  --auto-approve  Skip the prompt. Required for non-TTY runs (CI).

Every successful Stripe call is appended to ~/.gatr/audit.log so a
partial failure can be resumed safely.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			opts.in = cmd.InOrStdin()
			opts.interactive = isTerminal(os.Stdin)
			return runPush(cmd.Context(), cmd.OutOrStdout(), cmd.ErrOrStderr(), opts)
		},
	}
	cmd.Flags().StringVarP(&opts.configPath, "config", "c", "gatr.yaml", "Path to gatr.yaml")
	cmd.Flags().StringVar(&opts.projectID, "project-id", "", "Override the gatr project namespace (default: $"+envProjectID+", then gatr.yaml's `project:`)")
	cmd.Flags().StringVar(&opts.secretKey, "key", "", "Stripe secret key (defaults to $STRIPE_SECRET_KEY or ~/.gatr/credentials.toml)")
	cmd.Flags().StringVar(&opts.credFile, "credentials", "", "Override ~/.gatr/credentials.toml path")
	cmd.Flags().BoolVar(&opts.dryRun, "dry-run", false, "Print the plan and exit without applying")
	cmd.Flags().BoolVar(&opts.autoApprove, "auto-approve", false, "Skip the apply confirmation prompt (required for non-TTY runs)")
	cmd.Flags().StringVar(&opts.auditLogPath, "audit-log", "", "Override the default ~/.gatr/audit.log path")
	cmd.Flags().BoolVar(&opts.autoPatch, "auto-patch", false, "Rewrite gatr.yaml without prompting (CI default; interactive runs prompt instead)")
	cmd.Flags().BoolVar(&opts.force, "force", false, "Rewrite gatr.yaml even if it has uncommitted git changes (combine with --auto-patch or answer Y at the prompt)")
	return cmd
}

// runPush is the orchestrator. Keeping it decoupled from the cobra
// command makes the code path exercisable from tests via a hand-built
// pushOptions and a swappable ctx.
func runPush(ctx context.Context, out, errOut io.Writer, opts *pushOptions) error {
	if opts.dryRun && opts.autoApprove {
		printErr(errOut, "--dry-run and --auto-approve are mutually exclusive")
		return fmt.Errorf("--dry-run and --auto-approve are mutually exclusive")
	}

	cfg, err := schema.ParseFileAndValidate(opts.configPath)
	if err != nil {
		printErr(errOut, err.Error())
		return err
	}

	// Resolve project ID with three precedence levels:
	//   1. --project-id flag         (explicit override)
	//   2. $GATR_PROJECT_ID env      (CI / shell-scope override)
	//   3. cfg.Project from the yaml (the common case — schema requires
	//                                  project:min(1), so always set)
	// E502 is effectively unreachable below since the schema enforces
	// a non-empty `project`; kept as a defensive guard for any future
	// schema relaxation.
	projectID := resolveProjectID(opts.projectID, cfg.Project)
	if projectID == "" {
		printErr(errOut, "E502: no project namespace resolved")
		fmt.Fprintln(errOut, subtleStyle.Render("  Set --project-id, $"+envProjectID+", or `project:` in gatr.yaml."))
		return &gstripe.Error{
			Code:    gstripe.ErrCodeMissingProjectID,
			Message: "missing project namespace",
		}
	}

	client, err := gstripe.NewClient(gstripe.ClientOptions{
		SecretKey:      opts.secretKey,
		CredentialFile: opts.credFile,
		ProjectID:      projectID,
	})
	if err != nil {
		printErr(errOut, err.Error())
		return err
	}

	desired, err := gstripe.TranslateConfig(cfg)
	if err != nil {
		printErr(errOut, err.Error())
		return err
	}

	current, err := listCurrentState(ctx, client)
	if err != nil {
		printErr(errOut, err.Error())
		return err
	}

	plan := gstripe.ComputeDiff(desired, current)
	RenderDiffPlan(out, plan, projectID)

	if !plan.HasChanges() {
		return nil
	}
	proceed, err := shouldApply(out, errOut, opts)
	if err != nil {
		return err
	}
	if !proceed {
		return nil
	}

	audit, err := gstripe.NewFileAuditWriter(opts.auditLogPath)
	if err != nil {
		printErr(errOut, err.Error())
		return err
	}
	defer audit.Close()

	results, applyErr := client.ApplyPlan(ctx, plan, desired, audit)
	renderApplyResults(out, results, audit.Path())
	if applyErr != nil {
		printErr(errOut, applyErr.Error())
		fmt.Fprintln(errOut, subtleStyle.Render("  Partial results recorded in "+audit.Path()))
		fmt.Fprintln(errOut, subtleStyle.Render("  Re-run `gatr push` to resume — completed ops are idempotent."))
		return applyErr
	}

	return decideAndPatch(out, errOut, patchesFromResults(results), opts)
}

// shouldApply resolves the three-way gate between dry-run, auto-approve,
// and interactive confirmation. Returns (proceed, err):
//
//   - (false, nil)  → graceful skip (dry-run, or user answered N)
//   - (true,  nil)  → apply the plan
//   - (_,     err)  → refuse to apply (non-TTY without --auto-approve)
//
// Extracted from runPush so it's testable without standing up a fake
// Stripe client — the whole gate is reducible to pushOptions + writers.
func shouldApply(out, errOut io.Writer, opts *pushOptions) (bool, error) {
	if opts.dryRun {
		fmt.Fprintln(out)
		fmt.Fprintln(out, subtleStyle.Render("  Dry-run only. Re-run without --dry-run to apply."))
		return false, nil
	}
	if opts.autoApprove {
		return true, nil
	}
	if !opts.interactive || opts.in == nil {
		// Non-TTY (or a test that didn't wire stdin): refuse rather
		// than hang or silently drop. Stripe mutation is destructive;
		// the safe default in CI is to require an explicit opt-in.
		printErr(errOut, "refusing to apply in non-interactive environment without --auto-approve")
		fmt.Fprintln(errOut, subtleStyle.Render("  Pass --auto-approve to apply, or --dry-run to print only."))
		return false, fmt.Errorf("refusing to apply without --auto-approve in non-interactive environment")
	}
	fmt.Fprintln(out)
	if !confirm(opts.in, out, "Apply these changes to Stripe?", false /* defaultYes=false */) {
		fmt.Fprintln(out, subtleStyle.Render("  Aborted — no Stripe changes made."))
		return false, nil
	}
	return true, nil
}

// renderApplyResults prints one success/failure line per result. The
// apply pipeline already rendered the plan via RenderDiffPlan; this
// post-apply summary adds the actual Stripe IDs the user likely wants
// to paste back into gatr.yaml.
func renderApplyResults(out io.Writer, results []gstripe.ApplyResult, auditPath string) {
	if len(results) == 0 {
		return
	}
	fmt.Fprintln(out)
	fmt.Fprintln(out, titleStyle.Render("  Applied"))
	for _, r := range results {
		glyph := successStyle.Render("✓")
		if r.Err != nil {
			glyph = errorStyle.Render("✗")
		}
		line := fmt.Sprintf("    %s %s/%s %s",
			glyph, r.Op.Resource, r.Op.Action, codeStyle.Render(r.Op.YamlID))
		if r.StripeID != "" {
			line += " " + subtleStyle.Render("→ "+r.StripeID)
		}
		fmt.Fprintln(out, line)
	}
	fmt.Fprintln(out)
	fmt.Fprintln(out, subtleStyle.Render("  Audit log: "+auditPath))
}

// renderIDTable prints a machine-parseable table of newly-created
// Stripe IDs the user can paste back into gatr.yaml. Format is
// intentionally simple (no colour escapes in the table body) so it
// survives `| awk` and `| grep`:
//
//	  Paste these IDs back into <configPath>:
//	  <yaml_path>	<stripe_id>
//	  ...
//
// `showRerunHint` controls whether a trailing "or re-run with
// --auto-patch" line is printed — suppressed when an interactive
// prompt is about to fire (the prompt is the more direct option), or
// when --auto-patch is already in effect.
//
// Tests rely on the pipe-free separator; don't switch to lipgloss
// without updating diff_test.go.
func renderIDTable(w io.Writer, configPath string, patches []yamlpatch.Patch, showRerunHint bool) {
	if len(patches) == 0 {
		return
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, titleStyle.Render("  New Stripe IDs for "+configPath+":"))
	fmt.Fprintln(w)
	for _, p := range patches {
		fmt.Fprintf(w, "    %s\t%s\n", yamlPathFor(p), p.StripeID)
	}
	if showRerunHint {
		fmt.Fprintln(w)
		fmt.Fprintln(w, subtleStyle.Render("  Or re-run with --auto-patch to rewrite the file automatically."))
	}
}

// confirm prints prompt + "[Y/n]" (or "[y/N]" if defaultYes=false)
// and reads a line from in. Empty input → defaultYes. EOF / read
// error → defaultYes. Anything else: y/yes (case-insensitive) → true,
// everything else → false.
//
// Returning defaultYes on EOF is deliberate — the caller picks the
// safe default per prompt: the pre-apply Stripe prompt uses
// defaultYes=false (EOF → abort), the post-apply yaml-patch prompt
// uses defaultYes=true (user already consented, EOF → finish the job).
func confirm(in io.Reader, out io.Writer, prompt string, defaultYes bool) bool {
	suffix := "[Y/n]"
	if !defaultYes {
		suffix = "[y/N]"
	}
	fmt.Fprintf(out, "  %s %s ", prompt, suffix)

	r := bufio.NewReader(in)
	line, err := r.ReadString('\n')
	if err != nil && line == "" {
		fmt.Fprintln(out)
		return defaultYes
	}
	answer := strings.ToLower(strings.TrimSpace(line))
	if answer == "" {
		return defaultYes
	}
	return answer == "y" || answer == "yes"
}

// decideAndPatch is the post-apply branch extracted from runPush so
// it's testable without standing up a fake Stripe + ApplyPlan. Three
// cases:
//
//   • opts.autoPatch=true        → patch immediately, dirty-tree guard
//                                   still fires (--force overrides)
//   • opts.interactive=true and
//     opts.in != nil             → print table, prompt, patch on yes
//   • neither                    → print table + rerun hint, no patch
//
// In all cases, len(patches)==0 returns nil with no output.
func decideAndPatch(out, errOut io.Writer, patches []yamlpatch.Patch, opts *pushOptions) error {
	if len(patches) == 0 {
		return nil
	}
	willPrompt := !opts.autoPatch && opts.interactive && opts.in != nil
	willPatch := opts.autoPatch
	// Skip the "Or re-run with --auto-patch" hint when we're either
	// about to patch or about to prompt (both supersede the hint).
	renderIDTable(out, opts.configPath, patches, !willPrompt && !willPatch)

	if willPrompt {
		willPatch = confirm(opts.in, out, "Patch "+opts.configPath+" with these IDs?", true)
	}
	if !willPatch {
		return nil
	}

	unresolved, err := autoPatchConfig(opts.configPath, patches, opts.force)
	if err != nil {
		printErr(errOut, err.Error())
		return err
	}
	fmt.Fprintln(out)
	fmt.Fprintln(out, successStyle.Render("✓ Patched "+opts.configPath+" with "+fmt.Sprint(len(patches)-len(unresolved))+" new Stripe ID(s)."))
	for _, p := range unresolved {
		fmt.Fprintln(out, subtleStyle.Render(fmt.Sprintf("  (unresolved: %s %q — no matching yaml entry)", p.Kind, p.YamlID)))
	}
	return nil
}

// yamlPathFor is the human-readable path string for a patch — matches
// the shape a user would scroll to in their yaml. Doesn't quote the
// yaml_id because that's not how yaml is usually rendered.
func yamlPathFor(p yamlpatch.Patch) string {
	switch p.Kind {
	case yamlpatch.KindPlanMonthly:
		return "plans[" + p.YamlID + "].billing.monthly.stripe_price_id"
	case yamlpatch.KindPlanAnnual:
		return "plans[" + p.YamlID + "].billing.annual.stripe_price_id"
	case yamlpatch.KindMeteredPrice:
		return "metered_prices[" + p.YamlID + "].stripe_meter_id"
	}
	return strings.Join([]string{string(p.Kind), p.YamlID}, ":")
}

// listCurrentState runs the three ListManaged* calls and bundles them
// into a CurrentState for the diff engine. Three serial calls is fine
// at this scale — the Stripe list API caps at a few hundred objects
// per page and a mature gatr project tops out in the low dozens.
func listCurrentState(ctx context.Context, client *gstripe.Client) (gstripe.CurrentState, error) {
	products, err := client.ListManagedProducts(ctx)
	if err != nil {
		return gstripe.CurrentState{}, err
	}
	prices, err := client.ListManagedPrices(ctx)
	if err != nil {
		return gstripe.CurrentState{}, err
	}
	meters, err := client.ListManagedMeters(ctx)
	if err != nil {
		return gstripe.CurrentState{}, err
	}
	return gstripe.CurrentState{
		Products: products,
		Prices:   prices,
		Meters:   meters,
	}, nil
}
