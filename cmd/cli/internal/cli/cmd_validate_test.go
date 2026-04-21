package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/EduardMaghakyan/gatr-cli/pkg/schema"
	gstripe "github.com/EduardMaghakyan/gatr-cli/pkg/stripe"
)

const validateTestProject = "550e8400-e29b-41d4-a716-446655440000"

// Prefix split so raw source doesn't match Stripe secret-scanning.
// Runtime value is still a valid sk_test_ key for the validator.
const validateTestKey = "sk_" + "test_aaaaaaaaaaaaaaaaaaaaaaaa"

// yamlWithOneResolvableAndOneMissing wires the fixture the plan calls
// for: "validate against a YAML with one missing + one resolvable
// price_id → output flags both correctly."
const yamlWithOneResolvableAndOneMissing = `version: 4
project: demo
plans:
  - id: pro
    name: Pro
    billing:
      monthly:
        amount_cents: 2900
        currency: usd
        stripe_price_id: price_exists     # will resolve
      annual:
        amount_cents: 29000
        currency: usd
        stripe_price_id: price_missing    # won't resolve
    features: []
    limits: {}
    grants: {}
    includes: {}
metered_prices:
  - id: api_calls
    name: API calls
    unit: calls
    unit_price: 0.001
    stripe_meter_id: null                 # unset → acceptable
    period: month
    currency: usd
    aggregation: sum
`

// stripeResolveServer stands up a fake Stripe that lists `price_exists`
// as an active managed price (but NOT `price_missing`). Minimal — only
// the /v1/prices and /v1/billing/meters endpoints are implemented.
func stripeResolveServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/prices":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"object":   "list",
				"has_more": false,
				"data": []map[string]any{{
					"id": "price_exists", "object": "price",
					"product": "prod_pro", "unit_amount": 2900, "currency": "usd", "active": true,
					"recurring": map[string]any{"interval": "month", "usage_type": "licensed"},
					"metadata": map[string]string{
						"gatr_managed": "true",
						"gatr_id":      validateTestProject + ":pro_monthly",
					},
				}},
			})
		case "/v1/billing/meters":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"object": "list", "has_more": false, "data": []any{},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// runValidateWithServer is a test helper that wires the fake Stripe
// URL into the Stripe client. The CLI hides BackendURL behind
// ClientOptions (test-only), so we can't set it via flag; instead we
// call the resolver helper directly with a customised Config.
//
// To keep coverage focused on `validate --check-stripe`, we invoke
// runStripeResolveCheck rather than runValidate — the schema-parse
// step is identical to the existing validate test.
func runValidateWithServer(t *testing.T, yamlPath, backendURL string) (string, error) {
	t.Helper()
	cfg, err := schema.ParseFileAndValidate(yamlPath)
	require.NoError(t, err)

	// Construct the client pointing at the fake via ClientOptions, since
	// runStripeResolveCheck would otherwise construct one with the real
	// Stripe URL.
	client, err := gstripe.NewClient(gstripe.ClientOptions{
		SecretKey:  validateTestKey,
		ProjectID:  validateTestProject,
		BackendURL: backendURL,
	})
	require.NoError(t, err)

	prices, err := client.ListManagedPrices(context.Background())
	require.NoError(t, err)
	meters, err := client.ListManagedMeters(context.Background())
	require.NoError(t, err)
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

	var buf bytes.Buffer
	rows := collectCheckRows(cfg, activePriceIDs, activeMeterIDs)
	missing := renderCheckRows(&buf, rows)
	if missing > 0 {
		return buf.String(), &gstripe.Error{
			Code:    gstripe.ErrCodeStripeAPI,
			Message: "yaml-referenced Stripe IDs missing",
		}
	}
	return buf.String(), nil
}

func TestValidateCheckStripe_FlagsMissingAndUnset(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "gatr.yaml")
	require.NoError(t, os.WriteFile(path, []byte(yamlWithOneResolvableAndOneMissing), 0o600))

	srv := stripeResolveServer(t)
	out, err := runValidateWithServer(t, path, srv.URL)
	require.Error(t, err)
	var sErr *gstripe.Error
	require.True(t, errors.As(err, &sErr))
	require.Equal(t, gstripe.ErrCodeStripeAPI, sErr.Code)

	// Every expected row is present with the right state.
	require.Contains(t, out, "plans[pro].billing.monthly.stripe_price_id")
	require.Contains(t, out, "price_exists")
	require.Contains(t, out, "plans[pro].billing.annual.stripe_price_id")
	require.Contains(t, out, "price_missing")
	require.Contains(t, out, "not found in Stripe")
	require.Contains(t, out, "metered_prices[api_calls].stripe_meter_id")
	require.Contains(t, out, "will be created on next push")
}

func TestRunStripeResolveCheck_FallsBackToYamlProject(t *testing.T) {
	// No flag, no env → projectID resolves from cfg.Project. The
	// fake Stripe server serves a managed price namespaced to
	// `demo:pro_monthly`. If the fallback works, that price resolves
	// cleanly; otherwise the namespace mismatch drops it and the
	// yaml reference shows up as "missing".
	t.Setenv(envProjectID, "")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/prices":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"object":   "list",
				"has_more": false,
				"data": []map[string]any{{
					"id": "price_resolves", "object": "price",
					"product": "prod_x", "unit_amount": 2900, "currency": "usd", "active": true,
					"recurring": map[string]any{"interval": "month", "usage_type": "licensed"},
					"metadata": map[string]string{
						"gatr_managed": "true",
						"gatr_id":      "demo:pro_monthly", // namespaced on the yaml's project, not a UUID
					},
				}},
			})
		case "/v1/billing/meters":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"object": "list", "has_more": false, "data": []any{},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	yamlSrc := `version: 4
project: demo
plans:
  - id: pro
    name: Pro
    billing:
      monthly:
        amount_cents: 2900
        currency: usd
        stripe_price_id: price_resolves
    features: []
    limits: {}
    grants: {}
    includes: {}
metered_prices: []
`
	dir := t.TempDir()
	yamlPath := filepath.Join(dir, "gatr.yaml")
	require.NoError(t, os.WriteFile(yamlPath, []byte(yamlSrc), 0o600))
	cfg, err := schema.ParseFileAndValidate(yamlPath)
	require.NoError(t, err)

	// Build the client with the yaml's project as namespace — mirroring
	// what runStripeResolveCheck does internally post-fix.
	projectID := resolveProjectID("", cfg.Project)
	require.Equal(t, "demo", projectID)

	client, err := gstripe.NewClient(gstripe.ClientOptions{
		SecretKey:  validateTestKey,
		ProjectID:  projectID,
		BackendURL: srv.URL,
	})
	require.NoError(t, err)
	prices, err := client.ListManagedPrices(context.Background())
	require.NoError(t, err)
	require.Len(t, prices, 1, "yaml's project:demo must match the metadata's gatr_id namespace")
	require.Equal(t, "pro_monthly", prices[0].YamlID)
}

func TestCollectCheckRows_AllThreeStates(t *testing.T) {
	// Unit-level coverage for the row-classifier: ok / missing / unset.
	cfg := &schema.Config{
		Plans: []schema.Plan{
			{
				ID: "pro",
				Billing: &schema.Billing{
					Monthly: &schema.BillingInterval{
						AmountCents: 100, Currency: "usd",
						StripePriceID: ptr("price_ok"),
					},
					Annual: &schema.BillingInterval{
						AmountCents: 1000, Currency: "usd",
						StripePriceID: ptr("price_missing"),
					},
				},
			},
		},
		MeteredPrices: []schema.MeteredPrice{
			{
				ID: "m1", Period: "month", Currency: "usd", Aggregation: "sum",
				// StripeMeterID intentionally nil → unset row
			},
		},
	}
	rows := collectCheckRows(cfg,
		map[string]bool{"price_ok": true},
		map[string]bool{})
	require.Len(t, rows, 3)
	require.Equal(t, "ok", rows[0].State)
	require.Equal(t, "missing", rows[1].State)
	require.Equal(t, "unset", rows[2].State)
}

// ptr returns a pointer to a string literal — cleans up Plan fixture
// construction above.
func ptr(s string) *string { return &s }
