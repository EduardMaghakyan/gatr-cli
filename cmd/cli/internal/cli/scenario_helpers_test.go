package cli

// Helpers for the real-Stripe scenario tests in scenario_test.go.
// See that file's package-level doc for the overall test strategy.

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	stripeclient "github.com/stripe/stripe-go/v82/client"

	gstripe "github.com/EduardMaghakyan/gatr-cli/pkg/stripe"
)

// stripeKeyEnv is the env var every scenario test consults. Kept as
// a named const so a future rename (or a fallback to a second var)
// is a one-line change.
const stripeKeyEnv = "STRIPE_SECRET_KEY"

// requireStripeKey gates every scenario test. Returns the key or
// t.Skip()s. Unlike build-tag gating, this lets `go test ./...` run
// cleanly (scenarios show as SKIP) so CI without a key is still green.
func requireStripeKey(t *testing.T) string {
	t.Helper()
	key := os.Getenv(stripeKeyEnv)
	if key == "" {
		t.Skipf("%s not set — skipping real-Stripe scenario", stripeKeyEnv)
	}
	return key
}

// newTestProject returns a unique `gatrtest-<8-hex>` namespace and
// registers a t.Cleanup that archives every gatr-managed object
// written under it. The prefix doubles as a convention: real gatr
// projects must not use it, so a janitor sweep can safely archive
// anything matching in a shared test account.
func newTestProject(t *testing.T, secretKey string) string {
	t.Helper()
	buf := make([]byte, 4)
	_, err := rand.Read(buf)
	require.NoError(t, err, "crypto/rand")
	projectID := "gatrtest-" + hex.EncodeToString(buf)

	t.Cleanup(func() {
		// Cleanup runs even on failure. Use a fresh context with a
		// generous timeout — the enclosing test's ctx may already be
		// cancelled by this point.
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		cleanupProject(t, ctx, projectID, secretKey)
	})
	return projectID
}

// cleanupProject archives every Stripe object gatr has tagged under
// projectID by driving an empty DesiredState through the diff+apply
// pipeline. Best-effort: logs failures via t.Logf instead of failing
// the test, because cleanup errors on a live external service are
// diagnostics, not assertions.
func cleanupProject(t *testing.T, ctx context.Context, projectID, secretKey string) {
	t.Helper()
	client, err := gstripe.NewClient(gstripe.ClientOptions{
		SecretKey: secretKey,
		ProjectID: projectID,
	})
	if err != nil {
		t.Logf("cleanup: NewClient failed: %v", err)
		return
	}

	products, err := client.ListManagedProducts(ctx)
	if err != nil {
		t.Logf("cleanup: list products: %v", err)
		return
	}
	prices, err := client.ListManagedPrices(ctx)
	if err != nil {
		t.Logf("cleanup: list prices: %v", err)
		return
	}
	meters, err := client.ListManagedMeters(ctx)
	if err != nil {
		t.Logf("cleanup: list meters: %v", err)
		return
	}
	current := gstripe.CurrentState{Products: products, Prices: prices, Meters: meters}

	plan := gstripe.ComputeDiff(gstripe.DesiredState{}, current)
	if !plan.HasChanges() {
		return
	}
	// nil audit writer: cleanup output is logged, not persisted.
	// ApplyPlan tolerates a nil AuditWriter (it's the "no-audit"
	// path used in a few unit tests).
	if _, err := client.ApplyPlan(ctx, plan, gstripe.DesiredState{}, nil); err != nil {
		t.Logf("cleanup: ApplyPlan: %v", err)
	}
}

// writeYAML drops body into a temp gatr.yaml and returns its path.
// Exists purely to keep scenario bodies readable.
func writeYAML(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "gatr.yaml")
	require.NoError(t, os.WriteFile(path, []byte(body), 0o600))
	return path
}

// pushOpts is the default pushOptions for a scenario. autoApprove is
// ON because scenarios never interact; the audit log lives in a
// t.TempDir so concurrent tests don't collide on ~/.gatr/audit.log.
func pushOpts(t *testing.T, projectID, yamlPath, secretKey string) *pushOptions {
	t.Helper()
	return &pushOptions{
		configPath:   yamlPath,
		projectID:    projectID,
		secretKey:    secretKey,
		auditLogPath: filepath.Join(t.TempDir(), "audit.log"),
		autoApprove:  true,
		interactive:  false,
	}
}

// runPushAndCapture invokes runPush with buffered writers and a
// fresh context. Returns stdout+stderr strings and the runPush
// return value. The timeout guards against a hung network call
// hanging the whole suite.
func runPushAndCapture(t *testing.T, opts *pushOptions) (string, string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	var out, errOut bytes.Buffer
	err := runPush(ctx, &out, &errOut, opts)
	return out.String(), errOut.String(), err
}

// newRawStripeClient is for tests that need to poke Stripe directly
// (creating a Customer + Subscription in the subscriber-behaviour
// scenario). The gatr pkg/stripe.Client doesn't expose subscription
// endpoints — those aren't gatr's responsibility — so we spin up a
// vanilla stripe-go client alongside it.
func newRawStripeClient(secretKey string) *stripeclient.API {
	sc := &stripeclient.API{}
	sc.Init(secretKey, nil)
	return sc
}

