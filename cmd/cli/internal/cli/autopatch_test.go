package cli

import (
	"bytes"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/EduardMaghakyan/gatr-cli/pkg/schema/yamlpatch"
	gstripe "github.com/EduardMaghakyan/gatr-cli/pkg/stripe"
)

// ---- Test (a): print-only default emits a parseable table ------------------

func TestRenderIDTable_EmitsPathAndStripeID(t *testing.T) {
	patches := []yamlpatch.Patch{
		{Kind: yamlpatch.KindPlanMonthly, YamlID: "pro", StripeID: "price_pro_m"},
		{Kind: yamlpatch.KindMeteredPrice, YamlID: "api_calls", StripeID: "mtr_api"},
	}
	var buf bytes.Buffer
	renderIDTable(&buf, "gatr.yaml", patches, true)
	out := buf.String()
	require.Contains(t, out, "New Stripe IDs for gatr.yaml")
	require.Contains(t, out, "plans[pro].billing.monthly.stripe_price_id")
	require.Contains(t, out, "price_pro_m")
	require.Contains(t, out, "metered_prices[api_calls].stripe_meter_id")
	require.Contains(t, out, "mtr_api")
	// Tab separator is how a pipe-parseable table works. Strip ANSI
	// style reset escapes before asserting.
	require.Contains(t, stripAnsi(out), "\tprice_pro_m")
	require.Contains(t, out, "Or re-run with --auto-patch", "rerun hint when showRerunHint=true")
}

func TestRenderIDTable_SuppressesRerunHintWhenFalse(t *testing.T) {
	// When the caller is about to prompt or already auto-patching,
	// the "rerun with --auto-patch" trailer is noise — suppress it.
	patches := []yamlpatch.Patch{
		{Kind: yamlpatch.KindPlanMonthly, YamlID: "pro", StripeID: "price_pro_m"},
	}
	var buf bytes.Buffer
	renderIDTable(&buf, "gatr.yaml", patches, false)
	out := buf.String()
	require.Contains(t, out, "price_pro_m", "table still prints")
	require.NotContains(t, out, "Or re-run with --auto-patch")
}

// ---- Test (b): --auto-patch round-trips comments byte-for-byte ------------

func TestAutoPatch_PreservesCommentsAndNeighbourFields(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "gatr.yaml")
	src := `version: 4
project: demo

# keep this comment
plans:
  - id: pro
    name: "Pro"
    billing:
      monthly:
        amount_cents: 2900
        currency: usd
        stripe_price_id: null  # inline comment
    features: []
    limits: {}
    grants: {}
    includes: {}
metered_prices: []
`
	require.NoError(t, os.WriteFile(path, []byte(src), 0o600))

	patches := []yamlpatch.Patch{
		{Kind: yamlpatch.KindPlanMonthly, YamlID: "pro", StripeID: "price_live_42"},
	}
	// Force=true bypasses the git guard; this tempdir is not a repo.
	unresolved, err := autoPatchConfig(path, patches, true)
	require.NoError(t, err)
	require.Empty(t, unresolved)

	got, err := os.ReadFile(path)
	require.NoError(t, err)
	out := string(got)

	require.Contains(t, out, "# keep this comment", "top-of-section comment must survive")
	require.Contains(t, out, "# inline comment", "inline comment must survive")
	require.Contains(t, out, `stripe_price_id: "price_live_42"`)
	require.Contains(t, out, "amount_cents: 2900", "sibling field must not be touched")
	require.Contains(t, out, "currency: usd")
}

// ---- Test (c): --auto-patch on a dirty git tree refuses with E505 ---------

func TestAutoPatch_DirtyWorktree_RefusesWithE505(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed; skipping dirty-worktree test")
	}

	dir := t.TempDir()
	// Init a real git repo so `git status --porcelain` yields output.
	runGit(t, dir, "init", "-q", "-b", "main")
	runGit(t, dir, "config", "user.email", "test@example.com")
	runGit(t, dir, "config", "user.name", "test")

	path := filepath.Join(dir, "gatr.yaml")
	clean := "version: 4\nproject: demo\nplans: []\nmetered_prices: []\n"
	require.NoError(t, os.WriteFile(path, []byte(clean), 0o600))
	runGit(t, dir, "add", "gatr.yaml")
	runGit(t, dir, "commit", "-q", "-m", "initial")

	// Dirty it.
	dirty := clean + "# uncommitted edit\n"
	require.NoError(t, os.WriteFile(path, []byte(dirty), 0o600))

	patches := []yamlpatch.Patch{
		{Kind: yamlpatch.KindPlanMonthly, YamlID: "pro", StripeID: "price_x"},
	}
	_, err := autoPatchConfig(path, patches, false /* no --force */)
	require.Error(t, err)

	var sErr *gstripe.Error
	require.True(t, errors.As(err, &sErr))
	require.Equal(t, gstripe.ErrCodeDirtyWorktree, sErr.Code)

	// File contents should be untouched.
	after, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, dirty, string(after))
}

// ---- Test (d): --auto-patch --force overrides the dirty-tree guard --------

func TestAutoPatch_Force_RewritesDirtyFile(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed; skipping force test")
	}

	dir := t.TempDir()
	runGit(t, dir, "init", "-q", "-b", "main")
	runGit(t, dir, "config", "user.email", "test@example.com")
	runGit(t, dir, "config", "user.name", "test")

	path := filepath.Join(dir, "gatr.yaml")
	src := `version: 4
project: demo
plans:
  - id: pro
    name: "Pro"
    billing:
      monthly:
        amount_cents: 2900
        currency: usd
        stripe_price_id: null
    features: []
    limits: {}
    grants: {}
    includes: {}
metered_prices: []
`
	require.NoError(t, os.WriteFile(path, []byte(src), 0o600))
	runGit(t, dir, "add", "gatr.yaml")
	runGit(t, dir, "commit", "-q", "-m", "initial")

	// Dirty it (append a trailing comment).
	require.NoError(t, os.WriteFile(path, []byte(src+"# dirty\n"), 0o600))

	patches := []yamlpatch.Patch{
		{Kind: yamlpatch.KindPlanMonthly, YamlID: "pro", StripeID: "price_forced"},
	}
	_, err := autoPatchConfig(path, patches, true /* --force */)
	require.NoError(t, err)

	got, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Contains(t, string(got), `stripe_price_id: "price_forced"`)
}

// ---- Test bonus: autoPatchConfig in a non-git dir is a no-op guard -------

func TestAutoPatch_NonGitDir_SkipsGuard(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "gatr.yaml")
	src := `version: 4
project: demo
plans:
  - id: pro
    name: "Pro"
    billing:
      monthly:
        amount_cents: 2900
        currency: usd
        stripe_price_id: null
    features: []
    limits: {}
    grants: {}
    includes: {}
metered_prices: []
`
	require.NoError(t, os.WriteFile(path, []byte(src), 0o600))

	// No git init — the guard should skip (not a repo).
	_, err := autoPatchConfig(path, []yamlpatch.Patch{
		{Kind: yamlpatch.KindPlanMonthly, YamlID: "pro", StripeID: "price_nogit"},
	}, false)
	require.NoError(t, err, "non-git dir must not trigger the dirty-tree guard")
}

// ---- Test bonus: patchesFromResults extraction -----------------------------

func TestPatchesFromResults_MapsCorrectly(t *testing.T) {
	results := []gstripe.ApplyResult{
		{Op: gstripe.DiffOp{Resource: gstripe.ResourcePrice, Action: gstripe.ActionCreated, YamlID: "pro_monthly"}, StripeID: "price_m"},
		{Op: gstripe.DiffOp{Resource: gstripe.ResourcePrice, Action: gstripe.ActionCreated, YamlID: "pro_annual"}, StripeID: "price_a"},
		{Op: gstripe.DiffOp{Resource: gstripe.ResourcePrice, Action: gstripe.ActionCreated, YamlID: "api_calls_metered"}, StripeID: "price_metered"},
		{Op: gstripe.DiffOp{Resource: gstripe.ResourceMeter, Action: gstripe.ActionCreated, YamlID: "api_calls"}, StripeID: "mtr_api"},
		{Op: gstripe.DiffOp{Resource: gstripe.ResourceProduct, Action: gstripe.ActionCreated, YamlID: "pro"}, StripeID: "prod_pro"},
		// Errored result must be skipped.
		{Op: gstripe.DiffOp{Resource: gstripe.ResourceMeter, Action: gstripe.ActionCreated, YamlID: "errored"}, Err: errors.New("boom")},
		// Non-create actions must be skipped.
		{Op: gstripe.DiffOp{Resource: gstripe.ResourceProduct, Action: gstripe.ActionUpdated, YamlID: "pro"}, StripeID: "prod_pro"},
	}
	patches := patchesFromResults(results)
	require.Len(t, patches, 3, "monthly + annual + meter; metered-price and product/errored are skipped")
	byKind := map[yamlpatch.Kind]yamlpatch.Patch{}
	for _, p := range patches {
		byKind[p.Kind] = p
	}
	require.Equal(t, "price_m", byKind[yamlpatch.KindPlanMonthly].StripeID)
	require.Equal(t, "price_a", byKind[yamlpatch.KindPlanAnnual].StripeID)
	require.Equal(t, "mtr_api", byKind[yamlpatch.KindMeteredPrice].StripeID)
}

// ---- Interactive prompt tests ----------------------------------------------

func TestConfirm_AcceptsYesVariants(t *testing.T) {
	// Each input → expected return. Default is Y so empty / unrecognised
	// input still flows through the "yes" branch.
	cases := map[string]bool{
		"y\n":       true,
		"Y\n":       true,
		"yes\n":     true,
		"YES\n":     true,
		"\n":        true,  // empty → defaultYes
		"n\n":       false,
		"N\n":       false,
		"no\n":      false,
		"garbage\n": false, // non-y/yes is a no
	}
	for input, expected := range cases {
		t.Run("input="+strings.TrimSpace(input), func(t *testing.T) {
			var out bytes.Buffer
			got := confirm(strings.NewReader(input), &out, "Patch?", true /* defaultYes */)
			require.Equal(t, expected, got)
			require.Contains(t, out.String(), "[Y/n]", "default-yes prompt suffix")
		})
	}
}

func TestConfirm_DefaultNoFlipsSuffix(t *testing.T) {
	var out bytes.Buffer
	got := confirm(strings.NewReader("\n"), &out, "Delete?", false /* defaultYes=false */)
	require.False(t, got)
	require.Contains(t, out.String(), "[y/N]")
}

func TestConfirm_EOFReturnsDefault(t *testing.T) {
	// Disconnected pipe / closed stdin → default. defaultYes=true here
	// because this is the yaml-patch prompt, which fires AFTER the
	// user already confirmed the apply prompt — intent was already
	// expressed, so EOF finishes the job rather than aborts.
	var out bytes.Buffer
	got := confirm(strings.NewReader(""), &out, "Patch?", true)
	require.True(t, got, "EOF should fall through to defaultYes")
}

// ---- decideAndPatch: 4 modes -----------------------------------------------

func TestDecideAndPatch_NoPatches_NoOp(t *testing.T) {
	var out, errOut bytes.Buffer
	require.NoError(t, decideAndPatch(&out, &errOut, nil, &pushOptions{}))
	require.Empty(t, out.String())
	require.Empty(t, errOut.String())
}

func TestDecideAndPatch_AutoPatch_RewritesWithoutPrompt(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "gatr.yaml")
	require.NoError(t, os.WriteFile(path, []byte(autopatchTestYAML), 0o600))

	var out, errOut bytes.Buffer
	patches := []yamlpatch.Patch{
		{Kind: yamlpatch.KindPlanMonthly, YamlID: "pro", StripeID: "price_auto"},
	}
	err := decideAndPatch(&out, &errOut, patches, &pushOptions{
		configPath:  path,
		autoPatch:   true,
		force:       true, // tempdir isn't a git repo, so this is moot — kept for clarity
		interactive: false,
		// in: nil — must not be read
	})
	require.NoError(t, err)
	require.Contains(t, out.String(), "✓ Patched")
	require.NotContains(t, out.String(), "[Y/n]", "auto-patch must skip the prompt")

	got, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Contains(t, string(got), `stripe_price_id: "price_auto"`)
}

func TestDecideAndPatch_InteractiveYes_PromptsAndPatches(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "gatr.yaml")
	require.NoError(t, os.WriteFile(path, []byte(autopatchTestYAML), 0o600))

	var out, errOut bytes.Buffer
	patches := []yamlpatch.Patch{
		{Kind: yamlpatch.KindPlanMonthly, YamlID: "pro", StripeID: "price_yes"},
	}
	err := decideAndPatch(&out, &errOut, patches, &pushOptions{
		configPath:  path,
		autoPatch:   false,
		force:       true,
		interactive: true,
		in:          strings.NewReader("y\n"),
	})
	require.NoError(t, err)
	require.Contains(t, out.String(), "[Y/n]", "interactive mode must prompt")
	require.Contains(t, out.String(), "✓ Patched")

	got, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Contains(t, string(got), `stripe_price_id: "price_yes"`)
}

func TestDecideAndPatch_InteractiveNo_PromptsAndSkips(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "gatr.yaml")
	original := autopatchTestYAML
	require.NoError(t, os.WriteFile(path, []byte(original), 0o600))

	var out, errOut bytes.Buffer
	patches := []yamlpatch.Patch{
		{Kind: yamlpatch.KindPlanMonthly, YamlID: "pro", StripeID: "price_no"},
	}
	err := decideAndPatch(&out, &errOut, patches, &pushOptions{
		configPath:  path,
		autoPatch:   false,
		interactive: true,
		in:          strings.NewReader("n\n"),
	})
	require.NoError(t, err)
	require.Contains(t, out.String(), "[Y/n]", "interactive mode must prompt")
	require.NotContains(t, out.String(), "✓ Patched", "no patch when user says no")

	got, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, original, string(got), "yaml must be untouched after a no")
}

func TestDecideAndPatch_NonInteractive_PrintsTableWithRerunHint(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "gatr.yaml")
	original := autopatchTestYAML
	require.NoError(t, os.WriteFile(path, []byte(original), 0o600))

	var out, errOut bytes.Buffer
	patches := []yamlpatch.Patch{
		{Kind: yamlpatch.KindPlanMonthly, YamlID: "pro", StripeID: "price_pipe"},
	}
	err := decideAndPatch(&out, &errOut, patches, &pushOptions{
		configPath:  path,
		autoPatch:   false,
		interactive: false, // CI / piped — must not hang on stdin
		in:          nil,
	})
	require.NoError(t, err)
	out_s := out.String()
	require.Contains(t, out_s, "price_pipe", "table prints")
	require.Contains(t, out_s, "Or re-run with --auto-patch", "rerun hint when no other path")
	require.NotContains(t, out_s, "[Y/n]", "no prompt in non-interactive mode")

	got, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, original, string(got), "yaml untouched without prompt or auto-patch")
}

// autopatchTestYAML is a minimal valid yaml with one patchable
// monthly stripe_price_id slot. Shared across the decideAndPatch
// tests so each test only differs in the prompt setup.
const autopatchTestYAML = `version: 4
project: demo
plans:
  - id: pro
    name: "Pro"
    billing:
      monthly:
        amount_cents: 2900
        currency: usd
        stripe_price_id: null
    features: []
    limits: {}
    grants: {}
    includes: {}
metered_prices: []
`

// ---- helpers --------------------------------------------------------------

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	require.NoError(t, cmd.Run(), "git %v", args)
}

// stripAnsi removes SGR escape sequences (colour codes) so assertions
// work regardless of the terminal lipgloss detects.
func stripAnsi(s string) string {
	// Cheap state machine — enough for our generated output.
	var out strings.Builder
	in := false
	for _, r := range s {
		if in {
			if r == 'm' {
				in = false
			}
			continue
		}
		if r == 0x1b {
			in = true
			continue
		}
		out.WriteRune(r)
	}
	return out.String()
}
