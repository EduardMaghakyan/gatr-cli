package cli

// Scenario tests that exercise `gatr push` against a real Stripe test
// account. Each scenario:
//   1. Generates a unique gatrtest-<hex> project id.
//   2. Writes a gatr.yaml in a t.TempDir().
//   3. Invokes runPush (and re-invokes with edited yaml where relevant).
//   4. Asserts on CLI output + Stripe-side state via ListManaged*.
//   5. Auto-cleans via the t.Cleanup registered by newTestProject.
//
// All scenarios skip (not fail) when STRIPE_SECRET_KEY is unset — so
// `go test ./...` stays green on dev machines / per-PR CI that don't
// have the secret.
//
// Running one scenario:
//   STRIPE_SECRET_KEY=sk_test_... go test \
//     ./cmd/cli/internal/cli/ -run TestScenario_HardReplace -v
//
// Running all (~6 min total):
//   STRIPE_SECRET_KEY=sk_test_... go test \
//     ./cmd/cli/internal/cli/ -run TestScenario -v -timeout=10m

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	stripesdk "github.com/stripe/stripe-go/v82"

	gstripe "github.com/EduardMaghakyan/gatr-cli/pkg/stripe"
)

// ── Baseline flow ───────────────────────────────────────────────────

// TestScenario_InitValidatePushAllCreates is the canonical happy
// path: scaffold → validate → push against an empty Stripe account.
// Every resource in the template ends up created; validate
// --check-stripe round-trips every ID.
func TestScenario_InitValidatePushAllCreates(t *testing.T) {
	key := requireStripeKey(t)
	projectID := newTestProject(t, key)

	// `gatr init` into a tempdir using the hybrid-ai-saas template —
	// it's the richest template, exercising plans + meters + metered
	// prices in one push.
	dir := t.TempDir()
	root := NewRoot("test")
	var initOut bytes.Buffer
	root.SetOut(&initOut)
	root.SetErr(&initOut)
	root.SetArgs([]string{"init",
		"--template", "hybrid-ai-saas",
		"--project", projectID,
		"--no-prompt",
		"--dir", dir,
	})
	require.NoError(t, root.Execute(), "init failed: %s", initOut.String())

	yamlPath := filepath.Join(dir, "gatr.yaml")
	require.FileExists(t, yamlPath)

	// `gatr validate` on the scaffolded yaml (schema-only, no Stripe).
	root2 := NewRoot("test")
	var valOut bytes.Buffer
	root2.SetOut(&valOut)
	root2.SetErr(&valOut)
	root2.SetArgs([]string{"validate", "--config", yamlPath})
	require.NoError(t, root2.Execute(), "validate failed: %s", valOut.String())
	require.Contains(t, valOut.String(), "is valid")

	// `gatr push --auto-approve` — the real Stripe hit.
	opts := pushOpts(t, projectID, yamlPath, key)
	out, errOut, err := runPushAndCapture(t, opts)
	require.NoError(t, err, "push failed.\nstdout:\n%s\nstderr:\n%s", out, errOut)
	require.Contains(t, out, "Applied")

	// Validate --check-stripe also goes green: every non-null yaml ID
	// resolves, every null one reports "will be created on next push"
	// (there shouldn't be any nulls left since --auto-patch is off;
	// the user still sees a round-trip of "ok" + "unset" rows).
	root3 := NewRoot("test")
	var checkOut bytes.Buffer
	root3.SetOut(&checkOut)
	root3.SetErr(&checkOut)
	root3.SetArgs([]string{"validate", "--config", yamlPath,
		"--check-stripe", "--project-id", projectID, "--key", key,
	})
	require.NoError(t, root3.Execute(), "check-stripe failed: %s", checkOut.String())
}

// TestScenario_IdempotentRerun is the core M6 acceptance criterion —
// a second push against the same yaml makes zero mutating API calls.
func TestScenario_IdempotentRerun(t *testing.T) {
	key := requireStripeKey(t)
	projectID := newTestProject(t, key)
	yamlPath := writeYAML(t, minimalFreemium(projectID))

	opts := pushOpts(t, projectID, yamlPath, key)
	_, _, err := runPushAndCapture(t, opts)
	require.NoError(t, err, "first push must succeed")

	// Fresh audit log path so we can count the second run's entries
	// in isolation.
	opts2 := pushOpts(t, projectID, yamlPath, key)
	out, _, err := runPushAndCapture(t, opts2)
	require.NoError(t, err, "second push must succeed")
	require.Contains(t, out, "no changes")

	entries, err := gstripe.ReadAuditLog(opts2.auditLogPath)
	require.NoError(t, err)
	require.Empty(t, entries, "second push must write zero audit entries")
}

// ── Plan type coverage ──────────────────────────────────────────────

// TestScenario_PlanWithMonthlyAndAnnual creates a single plan with
// both billing intervals. Expected: one Product, two Prices, both
// linked to the same product.
func TestScenario_PlanWithMonthlyAndAnnual(t *testing.T) {
	key := requireStripeKey(t)
	projectID := newTestProject(t, key)
	yamlPath := writeYAML(t, fmt.Sprintf(`version: 4
project: %s
plans:
  - id: pro
    name: "Pro"
    billing:
      monthly:
        amount_cents: 2900
        currency: usd
        stripe_price_id: null
      annual:
        amount_cents: 29000
        currency: usd
        stripe_price_id: null
    features: []
    limits: {}
    grants: {}
    includes: {}
`, projectID))

	opts := pushOpts(t, projectID, yamlPath, key)
	_, _, err := runPushAndCapture(t, opts)
	require.NoError(t, err)

	client := mustClient(t, key, projectID)
	products, _ := client.ListManagedProducts(context.Background())
	prices, _ := client.ListManagedPrices(context.Background())

	require.Len(t, products, 1, "one plan → one product")
	require.Len(t, prices, 2, "monthly + annual → two prices")

	productID := products[0].StripeID
	for _, p := range prices {
		require.Equal(t, productID, p.ProductStripeID, "both prices must reference the plan's product")
	}
}

// TestScenario_MeteredPriceWithMeter exercises the metered_price
// branch: one synthetic product + one Stripe meter + one metered
// price, all wired through gatr_managed metadata.
func TestScenario_MeteredPriceWithMeter(t *testing.T) {
	key := requireStripeKey(t)
	projectID := newTestProject(t, key)
	yamlPath := writeYAML(t, fmt.Sprintf(`version: 4
project: %s
plans:
  - id: free
    name: "Free"
    features: []
    limits: {}
    grants: {}
    includes: {}
metered_prices:
  - id: api_calls
    name: "API calls"
    amount_cents: 1
    currency: usd
    aggregation: sum
    stripe_meter_id: null
`, projectID))

	opts := pushOpts(t, projectID, yamlPath, key)
	_, _, err := runPushAndCapture(t, opts)
	require.NoError(t, err)

	client := mustClient(t, key, projectID)
	meters, _ := client.ListManagedMeters(context.Background())
	prices, _ := client.ListManagedPrices(context.Background())

	require.Len(t, meters, 1, "one metered_price → one meter")
	require.Equal(t, "api_calls", meters[0].YamlID)
	require.Equal(t, "sum", meters[0].Aggregation)

	// Exactly one metered price (yaml id api_calls_metered).
	var meteredFound bool
	for _, p := range prices {
		if p.YamlID == "api_calls_metered" {
			meteredFound = true
			require.NotNil(t, p.Recurring, "metered price must be recurring")
			require.Equal(t, "metered", p.Recurring.UsageType)
		}
	}
	require.True(t, meteredFound, "api_calls_metered price must exist")
}

// TestScenario_MixedPlanAndMetered combines plan billing + a metered
// price to check the topological apply order (products → meters →
// prices) doesn't regress.
func TestScenario_MixedPlanAndMetered(t *testing.T) {
	key := requireStripeKey(t)
	projectID := newTestProject(t, key)
	yamlPath := writeYAML(t, fmt.Sprintf(`version: 4
project: %s
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
metered_prices:
  - id: tokens
    name: "Tokens"
    amount_cents: 1
    currency: usd
    aggregation: sum
    stripe_meter_id: null
`, projectID))

	opts := pushOpts(t, projectID, yamlPath, key)
	out, _, err := runPushAndCapture(t, opts)
	require.NoError(t, err, "mixed push: %s", out)

	client := mustClient(t, key, projectID)
	products, _ := client.ListManagedProducts(context.Background())
	prices, _ := client.ListManagedPrices(context.Background())
	meters, _ := client.ListManagedMeters(context.Background())

	// Expected: 2 products (pro + synthetic for tokens), 2 prices
	// (pro_monthly + tokens_metered), 1 meter.
	require.Len(t, products, 2)
	require.Len(t, prices, 2)
	require.Len(t, meters, 1)
}

// ── Edit flows ──────────────────────────────────────────────────────

// TestScenario_SoftUpdate_PlanName renames a plan between pushes.
// Expected: one ActionUpdated on the product, no prices touched.
// The product's Stripe ID must be unchanged (not archived+recreated).
func TestScenario_SoftUpdate_PlanName(t *testing.T) {
	key := requireStripeKey(t)
	projectID := newTestProject(t, key)

	v1 := simpleProPlan(projectID, `"Pro"`, 2900)
	yamlPath := writeYAML(t, v1)

	opts := pushOpts(t, projectID, yamlPath, key)
	_, _, err := runPushAndCapture(t, opts)
	require.NoError(t, err)

	client := mustClient(t, key, projectID)
	productsBefore, _ := client.ListManagedProducts(context.Background())
	require.Len(t, productsBefore, 1)
	idBefore := productsBefore[0].StripeID

	// Edit name only.
	v2 := simpleProPlan(projectID, `"Pro Plus"`, 2900)
	require.NoError(t, os.WriteFile(yamlPath, []byte(v2), 0o600))

	opts2 := pushOpts(t, projectID, yamlPath, key)
	out, _, err := runPushAndCapture(t, opts2)
	require.NoError(t, err)
	require.Contains(t, out, "update")

	productsAfter, _ := client.ListManagedProducts(context.Background())
	require.Len(t, productsAfter, 1)
	require.Equal(t, idBefore, productsAfter[0].StripeID, "soft update must keep the same product ID")
	require.Equal(t, "Pro Plus", productsAfter[0].Name)
}

// TestScenario_HardReplace_AmountCents changes amount_cents, which is
// immutable on a Stripe Price — triggering Replace (archive old +
// create new). The old Price becomes active=false and a new one
// appears with the new amount.
func TestScenario_HardReplace_AmountCents(t *testing.T) {
	key := requireStripeKey(t)
	projectID := newTestProject(t, key)

	yamlPath := writeYAML(t, simpleProPlan(projectID, `"Pro"`, 2900))
	_, _, err := runPushAndCapture(t, pushOpts(t, projectID, yamlPath, key))
	require.NoError(t, err)

	client := mustClient(t, key, projectID)
	pricesBefore, _ := client.ListManagedPrices(context.Background())
	require.Len(t, pricesBefore, 1)
	oldPriceID := pricesBefore[0].StripeID
	require.True(t, pricesBefore[0].Active)

	// Bump to 3900.
	require.NoError(t, os.WriteFile(yamlPath, []byte(simpleProPlan(projectID, `"Pro"`, 3900)), 0o600))
	out, _, err := runPushAndCapture(t, pushOpts(t, projectID, yamlPath, key))
	require.NoError(t, err, "replace push: %s", out)
	require.Contains(t, out, "replace")

	pricesAfter, _ := client.ListManagedPrices(context.Background())
	require.Len(t, pricesAfter, 2, "old + new price — Stripe pins immutable fields, never reuses a Price ID")

	var oldActive, newActive bool
	var newAmount int64
	for _, p := range pricesAfter {
		if p.StripeID == oldPriceID {
			oldActive = p.Active
		} else {
			newActive = p.Active
			newAmount = p.UnitAmount
		}
	}
	require.False(t, oldActive, "old price must be archived")
	require.True(t, newActive, "new price must be active")
	require.EqualValues(t, 3900, newAmount, "new price must carry the new amount")
}

// TestScenario_DeletePlan_Archives removes the plan block and pushes
// again. Expected: product + price archived (active=false).
func TestScenario_DeletePlan_Archives(t *testing.T) {
	key := requireStripeKey(t)
	projectID := newTestProject(t, key)

	yamlPath := writeYAML(t, simpleProPlan(projectID, `"Pro"`, 2900))
	_, _, err := runPushAndCapture(t, pushOpts(t, projectID, yamlPath, key))
	require.NoError(t, err)

	// Remove the plan. Schema requires at least one plan? No — the
	// schema allows zero plans; features/limits/etc are all optional.
	// We fall back to a single free-tier plan so the yaml is still
	// semantically meaningful.
	empty := fmt.Sprintf(`version: 4
project: %s
plans:
  - id: free
    name: "Free"
    features: []
    limits: {}
    grants: {}
    includes: {}
`, projectID)
	require.NoError(t, os.WriteFile(yamlPath, []byte(empty), 0o600))

	out, _, err := runPushAndCapture(t, pushOpts(t, projectID, yamlPath, key))
	require.NoError(t, err, "archive push: %s", out)
	require.Contains(t, out, "archive")

	client := mustClient(t, key, projectID)
	products, _ := client.ListManagedProducts(context.Background())
	prices, _ := client.ListManagedPrices(context.Background())

	// Pro's product + price archived; the new Free product is active
	// (no price for free). So: 2 products total (pro inactive, free
	// active), 1 price (pro_monthly inactive).
	var proProduct, freeProduct *gstripe.ManagedProduct
	for i, p := range products {
		switch p.YamlID {
		case "pro":
			proProduct = &products[i]
		case "free":
			freeProduct = &products[i]
		}
	}
	require.NotNil(t, proProduct, "pro product must still exist (archived)")
	require.NotNil(t, freeProduct, "free product must exist (active)")
	require.False(t, proProduct.Active, "pro product must be archived")
	require.True(t, freeProduct.Active, "free product must be active")

	require.Len(t, prices, 1)
	require.False(t, prices[0].Active, "pro_monthly must be archived")
}

// ── Error paths / 'can't be done' ──────────────────────────────────

// TestScenario_InvalidKey_SurfacesCleanly reconstructs the classic
// "can't even reach Stripe" error path. We don't test restricted-key
// scope here (it requires a second prepared key in env); instead
// we prove the credential-malformed error has the right shape —
// there's no half-applied state.
func TestScenario_InvalidKey_SurfacesCleanly(t *testing.T) {
	requireStripeKey(t) // at least gate on the env var presence — skip on dev
	projectID := "gatrtest-bogus"
	// Note: no newTestProject — no cleanup needed because the push
	// never reaches Stripe.

	yamlPath := writeYAML(t, minimalFreemium(projectID))
	opts := pushOpts(t, projectID, yamlPath, "sk_"+"test_not_a_real_key_xxxxxxxxxx")

	_, errOut, err := runPushAndCapture(t, opts)
	require.Error(t, err, "push with garbage key must fail")
	// Error surface is E501 (missing/malformed credentials) OR E504
	// (Stripe API refused — authentication). Both acceptable.
	require.True(t,
		strings.Contains(errOut, "E501") || strings.Contains(errOut, "E504"),
		"expected E501 or E504, got: %s", errOut,
	)
}

// TestScenario_MeterArchiveConstraint is the "delete can't be done"
// scenario the user asked about. Stripe's model: meter deactivation
// ALWAYS succeeds (events remain queryable). We prove it — a meter
// that has ingested an event still archives cleanly.
func TestScenario_MeterArchiveConstraint(t *testing.T) {
	key := requireStripeKey(t)
	projectID := newTestProject(t, key)

	yamlPath := writeYAML(t, fmt.Sprintf(`version: 4
project: %s
plans:
  - id: free
    name: "Free"
    features: []
    limits: {}
    grants: {}
    includes: {}
metered_prices:
  - id: api_calls
    name: "API calls"
    amount_cents: 1
    currency: usd
    aggregation: sum
    stripe_meter_id: null
`, projectID))

	_, _, err := runPushAndCapture(t, pushOpts(t, projectID, yamlPath, key))
	require.NoError(t, err)

	// Post a meter event directly via stripe-go — simulates production
	// traffic against the meter.
	client := mustClient(t, key, projectID)
	meters, _ := client.ListManagedMeters(context.Background())
	require.Len(t, meters, 1)
	eventName := meters[0].EventName

	// Need a customer for the event payload.
	raw := newRawStripeClient(key)
	cust, err := raw.Customers.New(&stripesdk.CustomerParams{
		Email: stripesdk.String("meter-test+" + projectID + "@example.com"),
	})
	require.NoError(t, err, "create customer")
	t.Cleanup(func() {
		_, _ = raw.Customers.Del(cust.ID, nil)
	})

	// Fire one meter event. Stripe accepts the payload asynchronously;
	// the archive path doesn't wait for ingestion either — it's a
	// control-plane op, not a data-plane one.
	evtParams := &stripesdk.BillingMeterEventParams{
		EventName: stripesdk.String(eventName),
		Payload: map[string]string{
			"stripe_customer_id": cust.ID,
			"value":              "1",
		},
	}
	_, err = raw.BillingMeterEvents.New(evtParams)
	require.NoError(t, err, "post meter event")

	// Now archive by removing the metered_price from yaml.
	empty := fmt.Sprintf(`version: 4
project: %s
plans:
  - id: free
    name: "Free"
    features: []
    limits: {}
    grants: {}
    includes: {}
metered_prices: []
`, projectID)
	require.NoError(t, os.WriteFile(yamlPath, []byte(empty), 0o600))

	out, _, err := runPushAndCapture(t, pushOpts(t, projectID, yamlPath, key))
	require.NoError(t, err, "archive-with-events must succeed: %s", out)

	// Meter should now be inactive.
	metersAfter, _ := client.ListManagedMeters(context.Background())
	require.Len(t, metersAfter, 1)
	require.Equal(t, "inactive", metersAfter[0].Status,
		"meter status must be inactive after archive; events remain queryable")
}

// ── Subscriber behaviour (documentation test) ──────────────────────

// TestScenario_PriceChangeWithActiveSubscription is the scenario the
// user flagged: "what if users already have subscriptions and we want
// to change the price?"
//
// Documented behaviour: gatr archives the old Price and creates a new
// Price. Existing subscriptions remain PINNED to the archived Price
// ID and keep billing at the old rate. gatr does NOT auto-migrate.
//
// This test is a regression guard for that behaviour. The followup
// migration story is a separate future feature (gatr migrate-subs).
func TestScenario_PriceChangeWithActiveSubscription(t *testing.T) {
	key := requireStripeKey(t)
	projectID := newTestProject(t, key)

	// v1: $29/mo.
	yamlPath := writeYAML(t, simpleProPlan(projectID, `"Pro"`, 2900))
	_, _, err := runPushAndCapture(t, pushOpts(t, projectID, yamlPath, key))
	require.NoError(t, err)

	client := mustClient(t, key, projectID)
	prices, _ := client.ListManagedPrices(context.Background())
	require.Len(t, prices, 1)
	oldPriceID := prices[0].StripeID

	// Create a test customer + subscription pinned to the old price.
	raw := newRawStripeClient(key)
	cust, err := raw.Customers.New(&stripesdk.CustomerParams{
		Email:         stripesdk.String("subtest+" + projectID + "@example.com"),
		PaymentMethod: stripesdk.String("pm_card_visa"),
		InvoiceSettings: &stripesdk.CustomerInvoiceSettingsParams{
			DefaultPaymentMethod: stripesdk.String("pm_card_visa"),
		},
	})
	require.NoError(t, err, "create customer")

	sub, err := raw.Subscriptions.New(&stripesdk.SubscriptionParams{
		Customer: stripesdk.String(cust.ID),
		Items: []*stripesdk.SubscriptionItemsParams{
			{Price: stripesdk.String(oldPriceID)},
		},
	})
	require.NoError(t, err, "create subscription")

	// Cleanup order: cancel the sub + delete the customer BEFORE
	// the project-cleanup archives prices. Registered after
	// newTestProject so LIFO makes it run first.
	t.Cleanup(func() {
		_, _ = raw.Subscriptions.Cancel(sub.ID, &stripesdk.SubscriptionCancelParams{
			InvoiceNow: stripesdk.Bool(false),
			Prorate:    stripesdk.Bool(false),
		})
		_, _ = raw.Customers.Del(cust.ID, nil)
	})

	// v2: $39/mo → Replace.
	require.NoError(t, os.WriteFile(yamlPath, []byte(simpleProPlan(projectID, `"Pro"`, 3900)), 0o600))
	_, _, err = runPushAndCapture(t, pushOpts(t, projectID, yamlPath, key))
	require.NoError(t, err)

	pricesAfter, _ := client.ListManagedPrices(context.Background())
	require.Len(t, pricesAfter, 2)

	// The documented behaviour: old price archived, new price active,
	// subscription still references the old price.
	var newPriceID string
	for _, p := range pricesAfter {
		if p.StripeID == oldPriceID {
			require.False(t, p.Active, "old price must be archived")
		} else {
			require.True(t, p.Active)
			require.EqualValues(t, 3900, p.UnitAmount)
			newPriceID = p.StripeID
		}
	}
	require.NotEmpty(t, newPriceID)

	// Re-fetch the subscription from Stripe. It must still be pinned
	// to the ARCHIVED price — this is the documented gap.
	subAfter, err := raw.Subscriptions.Get(sub.ID, nil)
	require.NoError(t, err)
	require.Equal(t, "active", string(subAfter.Status))
	require.NotEmpty(t, subAfter.Items.Data)
	require.Equal(t, oldPriceID, subAfter.Items.Data[0].Price.ID,
		"Stripe pins subscriptions to Price IDs — gatr does NOT auto-migrate")

	// Pause 1s before cleanup to let Stripe's propagation settle,
	// otherwise the project-cleanup's archive-price call can race
	// with Stripe's internal sub↔price indexing.
	time.Sleep(time.Second)
}

// ── Audit log integrity ─────────────────────────────────────────────

// TestScenario_AuditLogCompleteness checks every action surfaces as
// a properly-shaped JSONL entry. Replace ops produce TWO entries
// (archive + create), per the documented contract in audit.go.
func TestScenario_AuditLogCompleteness(t *testing.T) {
	key := requireStripeKey(t)
	projectID := newTestProject(t, key)

	yamlPath := writeYAML(t, simpleProPlan(projectID, `"Pro"`, 2900))
	opts := pushOpts(t, projectID, yamlPath, key)
	_, _, err := runPushAndCapture(t, opts)
	require.NoError(t, err)

	entries, err := gstripe.ReadAuditLog(opts.auditLogPath)
	require.NoError(t, err)
	require.NotEmpty(t, entries)

	for _, e := range entries {
		require.Equal(t, projectID, e.ProjectID)
		require.NotEmpty(t, e.Resource)
		require.NotEmpty(t, e.Action)
		require.NotEmpty(t, e.YamlID)
		require.Empty(t, e.Error, "no entry should carry an error on a successful push")
		require.NotEmpty(t, e.StripeID, "successful ops must have a stripe_id")
	}
}

// ── fixtures ────────────────────────────────────────────────────────

// minimalFreemium is the smallest yaml that passes schema validation
// and exercises the plan-create path — one free + one paid tier.
func minimalFreemium(projectID string) string {
	return fmt.Sprintf(`version: 4
project: %s
plans:
  - id: free
    name: "Free"
    features: []
    limits: {}
    grants: {}
    includes: {}
  - id: pro
    name: "Pro"
    billing:
      monthly:
        amount_cents: 1900
        currency: usd
        stripe_price_id: null
    features: []
    limits: {}
    grants: {}
    includes: {}
`, projectID)
}

// simpleProPlan is a single-plan yaml parameterised by name + amount.
// Used by the edit-flow scenarios that mutate one field at a time.
func simpleProPlan(projectID, quotedName string, amountCents int) string {
	return fmt.Sprintf(`version: 4
project: %s
plans:
  - id: pro
    name: %s
    billing:
      monthly:
        amount_cents: %d
        currency: usd
        stripe_price_id: null
    features: []
    limits: {}
    grants: {}
    includes: {}
`, projectID, quotedName, amountCents)
}

// mustClient builds a pkg/stripe client bound to (key, projectID) and
// fails the test on error. Thin wrapper — worth it because every
// scenario needs one for post-push assertions.
func mustClient(t *testing.T, key, projectID string) *gstripe.Client {
	t.Helper()
	c, err := gstripe.NewClient(gstripe.ClientOptions{
		SecretKey: key,
		ProjectID: projectID,
	})
	require.NoError(t, err)
	return c
}
