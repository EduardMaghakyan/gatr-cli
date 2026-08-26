package cli

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/EduardMaghakyan/gatr-cli/pkg/schema"
)

func TestDeployTargetPrefersFlagsOverTheEnvironment(t *testing.T) {
	t.Setenv(envGatrBaseURL, "https://env.test")
	t.Setenv(envGatrAPIKey, "gatr_sk_env")

	url, key, err := resolveDeployTarget("https://flag.test", "gatr_sk_flag")
	require.NoError(t, err)
	require.Equal(t, "https://flag.test", url)
	require.Equal(t, "gatr_sk_flag", key)
}

func TestDeployTargetFallsBackToTheEnvironment(t *testing.T) {
	t.Setenv(envGatrBaseURL, "https://env.test")
	t.Setenv(envGatrAPIKey, "gatr_sk_env")

	url, key, err := resolveDeployTarget("", "")
	require.NoError(t, err)
	require.Equal(t, "https://env.test", url)
	require.Equal(t, "gatr_sk_env", key)
}

func TestDeployTargetTrimsATrailingSlash(t *testing.T) {
	// Otherwise the request goes to //v1/config, which some proxies 404.
	url, _, err := resolveDeployTarget("https://env.test/", "k")
	require.NoError(t, err)
	require.Equal(t, "https://env.test", url)
}

func TestDeployTargetNamesWhatIsMissing(t *testing.T) {
	// A URL with no key cannot authenticate and a key with no URL has nothing
	// to reach, so failing here beats a 401 that reads like a bad key.
	t.Setenv(envGatrBaseURL, "")
	t.Setenv(envGatrAPIKey, "")

	_, _, err := resolveDeployTarget("", "")
	require.Error(t, err)
	require.Contains(t, err.Error(), envGatrBaseURL)
	require.Contains(t, err.Error(), envGatrAPIKey)

	_, _, err = resolveDeployTarget("https://x.test", "")
	require.Error(t, err)
	require.Contains(t, err.Error(), envGatrAPIKey)
	require.NotContains(t, err.Error(), envGatrBaseURL)
}

func TestDeploySendsTheConfigAndTheKey(t *testing.T) {
	var gotAuth, gotPath string
	var gotBody map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth, gotPath = r.Header.Get("Authorization"), r.URL.Path
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &gotBody)
		_ = json.NewEncoder(w).Encode(deployResult{
			ProjectID: "p1", FromVersion: 3, ToVersion: 4,
		})
	}))
	defer srv.Close()

	out, err := deployConfig(context.Background(), srv.URL, "gatr_sk_abc",
		[]byte("version: 4\n"), false)
	require.NoError(t, err)

	require.Equal(t, "/v1/config", gotPath)
	require.Equal(t, "Bearer gatr_sk_abc", gotAuth)
	require.Equal(t, "version: 4\n", gotBody["config"])
	require.Equal(t, false, gotBody["dry_run"])
	require.Equal(t, int32(4), out.ToVersion)
}

func TestDeployPassesDryRunThrough(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &gotBody)
		_ = json.NewEncoder(w).Encode(deployResult{DryRun: true})
	}))
	defer srv.Close()

	_, err := deployConfig(context.Background(), srv.URL, "k", []byte("x"), true)
	require.NoError(t, err)
	require.Equal(t, true, gotBody["dry_run"])
}

func TestDeploySurfacesTheServersRefusal(t *testing.T) {
	// The server's message names the plan and the customer count when a push
	// would strand somebody. Replacing it with a status code throws away the
	// only part that says what to do next.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"code":"E020","message":"refusing to push: starter (3 customers) no longer declared"}}`))
	}))
	defer srv.Close()

	_, err := deployConfig(context.Background(), srv.URL, "k", []byte("x"), false)
	require.Error(t, err)
	require.Contains(t, err.Error(), "starter (3 customers)")
}

func TestDeployFallsBackToTheRawBodyOnAnUnexpectedShape(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte("upstream exploded"))
	}))
	defer srv.Close()

	_, err := deployConfig(context.Background(), srv.URL, "k", []byte("x"), false)
	require.Error(t, err)
	require.Contains(t, err.Error(), "upstream exploded")
}

func TestPushOffersDeployFlags(t *testing.T) {
	cmd := newPushCmd()
	for _, name := range []string{"deploy", "gatr-url", "gatr-key"} {
		require.NotNil(t, cmd.Flags().Lookup(name), "missing --%s", name)
	}
}

// ── the unresolved-price guard ─────────────────────────────────────

func strPtr(v string) *string { return &v }

func TestDeployRefusesAPlanWithNoStripePrice(t *testing.T) {
	// The silent failure this prevents: gatr matches subscriptions to plans by
	// price id, so a null deploys fine and then every subscription event
	// resolves to no plan, with nothing reported anywhere.
	err := checkPricesResolved(&schema.Config{
		Plans: []schema.Plan{
			{ID: "free"},
			{ID: "starter", Billing: &schema.Billing{
				Monthly: &schema.BillingInterval{AmountCents: 2500, Currency: "usd"},
			}},
		},
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "starter (monthly)")
	require.Contains(t, err.Error(), "--auto-patch", "must say how to fix it")
}

func TestDeployAllowsAFullyPatchedConfig(t *testing.T) {
	err := checkPricesResolved(&schema.Config{
		Plans: []schema.Plan{
			{ID: "free"},
			{ID: "starter", Billing: &schema.Billing{
				Monthly: &schema.BillingInterval{StripePriceID: strPtr("price_x")},
			}},
		},
	})
	require.NoError(t, err)
}

func TestDeployChecksBothIntervals(t *testing.T) {
	err := checkPricesResolved(&schema.Config{
		Plans: []schema.Plan{{ID: "pro", Billing: &schema.Billing{
			Monthly: &schema.BillingInterval{StripePriceID: strPtr("price_m")},
			Annual:  &schema.BillingInterval{},
		}}},
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "pro (annual)")
	require.NotContains(t, err.Error(), "pro (monthly)")
}

func TestDeployIgnoresCreditPacksWithoutPrices(t *testing.T) {
	// gatr identifies a pack from session metadata and the amount, never from
	// the price id, so a pack without one is unsellable but not silently
	// broken — which is a different problem with a different fix.
	err := checkPricesResolved(&schema.Config{
		CreditPacks: []schema.CreditPack{{ID: "pack_20"}},
		Plans:       []schema.Plan{{ID: "free"}},
	})
	require.NoError(t, err)
}

func TestDeployRefusalIsDeterministic(t *testing.T) {
	// An error message that reorders between runs is one nobody trusts.
	cfg := &schema.Config{Plans: []schema.Plan{
		{ID: "pro", Billing: &schema.Billing{Monthly: &schema.BillingInterval{}}},
		{ID: "starter", Billing: &schema.Billing{Monthly: &schema.BillingInterval{}}},
	}}
	require.Equal(t, checkPricesResolved(cfg).Error(), checkPricesResolved(cfg).Error())
}
