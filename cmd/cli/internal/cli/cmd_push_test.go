package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	gstripe "github.com/EduardMaghakyan/gatr-cli/pkg/stripe"
)

// Tests in this file exercise runPush end-to-end against a stubbed
// Stripe backend. We don't test the happy-path stdout rendering here —
// that's covered by diff_test.go; these tests focus on the error paths
// where runPush has to bail cleanly with the right code.

func TestRunPush_ResolvesProjectIDFromYAML(t *testing.T) {
	// No --project-id flag, no env var — yaml's `project: demo` must
	// be picked up. The run will fail downstream (bogus Stripe key),
	// but the error code must NOT be E502 — yaml fallback worked.
	t.Setenv(envProjectID, "")
	dir := t.TempDir()
	yamlPath := filepath.Join(dir, "gatr.yaml")
	require.NoError(t, os.WriteFile(yamlPath, []byte(minimalValidYAML), 0o600))

	var out, errOut bytes.Buffer
	err := runPush(context.Background(), &out, &errOut, &pushOptions{
		configPath: yamlPath,
		secretKey:  "not-a-real-key", // forces a later failure
		credFile:   "/nonexistent/credentials.toml",
	})
	require.Error(t, err, "run should fail on bogus key, not on missing project id")
	var sErr *gstripe.Error
	require.True(t, errors.As(err, &sErr))
	require.NotEqual(t, gstripe.ErrCodeMissingProjectID, sErr.Code,
		"yaml `project:` must satisfy the project-id requirement")
	require.Equal(t, gstripe.ErrCodeMissingCredentials, sErr.Code)
}

func TestResolveProjectID_Precedence(t *testing.T) {
	// Flag beats env beats yaml. Pure-function unit test so the
	// precedence contract is locked independent of runPush plumbing.
	t.Run("flag wins over env + yaml", func(t *testing.T) {
		t.Setenv(envProjectID, "from_env")
		require.Equal(t, "from_flag", resolveProjectID("from_flag", "from_yaml"))
	})
	t.Run("env wins over yaml when flag empty", func(t *testing.T) {
		t.Setenv(envProjectID, "from_env")
		require.Equal(t, "from_env", resolveProjectID("", "from_yaml"))
	})
	t.Run("yaml used when flag + env empty", func(t *testing.T) {
		t.Setenv(envProjectID, "")
		require.Equal(t, "from_yaml", resolveProjectID("", "from_yaml"))
	})
	t.Run("all empty → empty", func(t *testing.T) {
		t.Setenv(envProjectID, "")
		require.Empty(t, resolveProjectID("", ""))
	})
}

func TestRunPush_InvalidYAML_PropagatesSchemaError(t *testing.T) {
	dir := t.TempDir()
	yamlPath := filepath.Join(dir, "gatr.yaml")
	// Invalid: missing required fields on a plan.
	require.NoError(t, os.WriteFile(yamlPath, []byte("version: 4\nproject: demo\nplans:\n  - id: pro\n"), 0o600))

	var out, errOut bytes.Buffer
	err := runPush(context.Background(), &out, &errOut, &pushOptions{
		configPath: yamlPath,
		projectID:  "550e8400-e29b-41d4-a716-446655440000",
		secretKey:  "sk_" + "test_aaaaaaaaaaaaaaaaaaaaaaaa",
	})
	require.Error(t, err, "schema validation failure must surface as an error")
	require.NotEmpty(t, errOut.String())
}

func TestRunPush_BogusStripeKey_ReturnsE501(t *testing.T) {
	dir := t.TempDir()
	yamlPath := filepath.Join(dir, "gatr.yaml")
	require.NoError(t, os.WriteFile(yamlPath, []byte(minimalValidYAML), 0o600))

	var out, errOut bytes.Buffer
	err := runPush(context.Background(), &out, &errOut, &pushOptions{
		configPath: yamlPath,
		projectID:  "550e8400-e29b-41d4-a716-446655440000",
		secretKey:  "not-a-real-key",
		credFile:   "/nonexistent/credentials.toml",
	})
	require.Error(t, err)
	var sErr *gstripe.Error
	require.True(t, errors.As(err, &sErr))
	require.Equal(t, gstripe.ErrCodeMissingCredentials, sErr.Code)
}

// TestRunPush_DryRunAndAutoApproveAreMutuallyExclusive locks the mutex
// at the very top of runPush. The error must fire before any Stripe
// work kicks off — so we don't need to stub yaml or credentials.
func TestRunPush_DryRunAndAutoApproveAreMutuallyExclusive(t *testing.T) {
	var out, errOut bytes.Buffer
	err := runPush(context.Background(), &out, &errOut, &pushOptions{
		configPath:  "/dev/null", // unreachable — mutex fires first
		dryRun:      true,
		autoApprove: true,
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "mutually exclusive")
	require.Contains(t, errOut.String(), "mutually exclusive")
	require.Empty(t, out.String(), "mutex must short-circuit before any plan rendering")
}

// ---- shouldApply: plan→prompt→apply gate ---------------------------------
//
// shouldApply is a pure decision function — no Stripe, no filesystem,
// no cobra. That's deliberate: the gate's branches (dry-run, auto-
// approve, non-TTY refusal, interactive Y/N) are the user-visible
// contract of the new Terraform-style flow. Testing this directly is
// the cheapest way to pin that contract.

func TestShouldApply_DryRunPrintsHintAndSkips(t *testing.T) {
	var out, errOut bytes.Buffer
	proceed, err := shouldApply(&out, &errOut, &pushOptions{dryRun: true})
	require.NoError(t, err)
	require.False(t, proceed)
	require.Contains(t, out.String(), "Dry-run only")
	require.Empty(t, errOut.String())
}

func TestShouldApply_AutoApproveProceedsSilently(t *testing.T) {
	var out, errOut bytes.Buffer
	proceed, err := shouldApply(&out, &errOut, &pushOptions{autoApprove: true})
	require.NoError(t, err)
	require.True(t, proceed)
	require.Empty(t, out.String(), "auto-approve must not print a prompt or banner")
	require.Empty(t, errOut.String())
}

func TestShouldApply_NonInteractiveRefusesWithoutAutoApprove(t *testing.T) {
	// CI / piped stdin / test without `in` wired → refuse cleanly.
	// The alternative (hanging on a never-arriving y/n, or silently
	// skipping) would be a worse default for a destructive op.
	var out, errOut bytes.Buffer
	proceed, err := shouldApply(&out, &errOut, &pushOptions{interactive: false})
	require.Error(t, err)
	require.False(t, proceed)
	require.Contains(t, err.Error(), "refusing to apply")
	require.Contains(t, errOut.String(), "--auto-approve")
	require.Contains(t, errOut.String(), "--dry-run", "hint must mention both escape hatches")
}

func TestShouldApply_InteractiveNoAnswerAborts(t *testing.T) {
	var out, errOut bytes.Buffer
	proceed, err := shouldApply(&out, &errOut, &pushOptions{
		interactive: true,
		in:          strings.NewReader("n\n"),
	})
	require.NoError(t, err)
	require.False(t, proceed)
	require.Contains(t, out.String(), "[y/N]", "default-No suffix — Stripe mutation is destructive")
	require.Contains(t, out.String(), "Aborted")
}

func TestShouldApply_InteractiveEmptyAnswerDefaultsToNo(t *testing.T) {
	// Empty input (user hit Enter) → defaultYes=false → abort. This
	// is the key differentiator from the yaml-patch prompt, which
	// defaults to Yes. A barefoot Enter on the apply prompt must NOT
	// silently execute.
	var out, errOut bytes.Buffer
	proceed, err := shouldApply(&out, &errOut, &pushOptions{
		interactive: true,
		in:          strings.NewReader("\n"),
	})
	require.NoError(t, err)
	require.False(t, proceed)
	require.Contains(t, out.String(), "Aborted")
}

func TestShouldApply_InteractiveYesProceeds(t *testing.T) {
	var out, errOut bytes.Buffer
	proceed, err := shouldApply(&out, &errOut, &pushOptions{
		interactive: true,
		in:          strings.NewReader("y\n"),
	})
	require.NoError(t, err)
	require.True(t, proceed)
	require.Contains(t, out.String(), "Apply these changes to Stripe?")
	require.NotContains(t, out.String(), "Aborted")
}

// minimalValidYAML is the smallest gatr.yaml that passes schema
// validation — just enough to let runPush reach the Stripe step.
const minimalValidYAML = `version: 4
project: demo
features: []
limits: []
credits: []
operations: []
metered_prices: []
plans:
  - id: free
    name: Free
    features: []
    limits: {}
    grants: {}
    includes: {}
`
