package stripe

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/EduardMaghakyan/gatr-cli/pkg/schema"
)

// adoptionTestYAML returns a minimal *schema.Config with one plan
// pointing at a stripe_price_id, parameterised so each test can swap
// the ID. Using helpers keeps the test bodies focused on the
// classification logic.
func adoptionPlanCfg(stripePriceID string) *schema.Config {
	return &schema.Config{
		Version: 4, Project: "demo",
		Plans: []schema.Plan{{
			ID: "pro", Name: "Pro",
			Billing: &schema.Billing{
				Monthly: &schema.BillingInterval{
					AmountCents: 2900, Currency: "usd",
					StripePriceID: stringPtr(stripePriceID),
				},
			},
			Features: []string{},
			Limits:   map[string]schema.NumberOrUnlimited{},
			Grants:   map[string]schema.NumberOrUnlimited{},
			Includes: map[string]schema.NumberOrUnlimited{},
		}},
	}
}

// adoptionMeterCfg is the metered_price equivalent — used by the
// "meters cannot be adopted" test.
func adoptionMeterCfg(stripeMeterID string) *schema.Config {
	return &schema.Config{
		Version: 4, Project: "demo",
		Plans: []schema.Plan{{
			ID: "free", Name: "Free",
			Features: []string{},
			Limits:   map[string]schema.NumberOrUnlimited{},
			Grants:   map[string]schema.NumberOrUnlimited{},
			Includes: map[string]schema.NumberOrUnlimited{},
		}},
		MeteredPrices: []schema.MeteredPrice{{
			ID: "api_calls", Name: "API calls", Unit: "calls",
			UnitPrice: 0.001, Period: "month", Currency: "usd",
			Aggregation: "sum", StripeMeterID: stringPtr(stripeMeterID),
		}},
	}
}

// ---- ScanAdoptions -------------------------------------------------

func TestScanAdoptions_AlreadyManagedSkipped(t *testing.T) {
	rs := newRecordingStripe(t)
	// Stripe returns a price already stamped under THIS project at the
	// expected yaml_id (pro_monthly).
	rs.reply("GET", "/v1/prices/price_managed", map[string]any{
		"id":         "price_managed",
		"object":     "price",
		"unit_amount": 2900,
		"currency":   "usd",
		"product":    map[string]any{"id": "prod_managed"},
		"metadata": map[string]string{
			metaKeyManaged: "true",
			metaKeyGatrID:  gatrIDFor(testProjectID, "pro_monthly"),
		},
	})
	c := rs.client(t)

	plan, err := c.ScanAdoptions(context.Background(), adoptionPlanCfg("price_managed"))
	require.NoError(t, err)
	require.Empty(t, plan.Candidates)
	require.Empty(t, plan.Conflicts)
}

func TestScanAdoptions_UnmanagedBecomesCandidate(t *testing.T) {
	rs := newRecordingStripe(t)
	// Unmanaged price (no gatr metadata) → candidate. We also need to
	// reply for the parent product fetch.
	rs.reply("GET", "/v1/prices/price_new", map[string]any{
		"id":         "price_new",
		"object":     "price",
		"unit_amount": 2900,
		"currency":   "usd",
		"product":    map[string]any{"id": "prod_new", "name": "Pro"},
		"metadata":   map[string]string{}, // empty
	})
	rs.reply("GET", "/v1/products/prod_new", map[string]any{
		"id":       "prod_new",
		"object":   "product",
		"name":     "Pro",
		"active":   true,
		"metadata": map[string]string{}, // empty
	})
	c := rs.client(t)

	plan, err := c.ScanAdoptions(context.Background(), adoptionPlanCfg("price_new"))
	require.NoError(t, err)
	require.Empty(t, plan.Conflicts)
	require.Len(t, plan.Candidates, 2, "price + parent product")

	// One must be the price, one the product. Order is "price first,
	// then its parent" — see the loop in ScanAdoptions.
	require.Equal(t, ResourcePrice, plan.Candidates[0].Resource)
	require.Equal(t, "price_new", plan.Candidates[0].StripeID)
	require.Equal(t, "pro_monthly", plan.Candidates[0].YamlID)

	require.Equal(t, ResourceProduct, plan.Candidates[1].Resource)
	require.Equal(t, "prod_new", plan.Candidates[1].StripeID)
	require.Equal(t, "pro", plan.Candidates[1].YamlID)
}

func TestScanAdoptions_OwnedByOtherProjectErrors(t *testing.T) {
	rs := newRecordingStripe(t)
	otherProject := "other-project"
	rs.reply("GET", "/v1/prices/price_other", map[string]any{
		"id":         "price_other",
		"object":     "price",
		"unit_amount": 2900,
		"currency":   "usd",
		"metadata": map[string]string{
			metaKeyManaged: "true",
			metaKeyGatrID:  otherProject + ":pro_monthly",
		},
	})
	c := rs.client(t)

	plan, err := c.ScanAdoptions(context.Background(), adoptionPlanCfg("price_other"))
	require.NoError(t, err)
	require.Empty(t, plan.Candidates)
	require.Len(t, plan.Conflicts, 1)
	require.Equal(t, AdoptionOwnedByOtherProject, plan.Conflicts[0].Kind)
	require.Equal(t, ResourcePrice, plan.Conflicts[0].Resource)
	require.Contains(t, plan.Conflicts[0].Message, otherProject)
}

func TestScanAdoptions_ManagedHereButDifferentYamlID(t *testing.T) {
	// The price IS gatr-managed by this project, but under yaml_id
	// "legacy_monthly" instead of the yaml's "pro_monthly". This
	// usually means someone renamed the plan; gatr can't silently
	// rebind.
	rs := newRecordingStripe(t)
	rs.reply("GET", "/v1/prices/price_renamed", map[string]any{
		"id":         "price_renamed",
		"object":     "price",
		"unit_amount": 2900,
		"currency":   "usd",
		"metadata": map[string]string{
			metaKeyManaged: "true",
			metaKeyGatrID:  gatrIDFor(testProjectID, "legacy_monthly"),
		},
	})
	c := rs.client(t)

	plan, err := c.ScanAdoptions(context.Background(), adoptionPlanCfg("price_renamed"))
	require.NoError(t, err)
	require.Len(t, plan.Conflicts, 1)
	require.Equal(t, AdoptionOwnedByOtherProject, plan.Conflicts[0].Kind)
	require.Contains(t, plan.Conflicts[0].Message, "legacy_monthly")
}

func TestScanAdoptions_NotFoundIsConflict(t *testing.T) {
	rs := newRecordingStripe(t)
	rs.replyStatus("GET", "/v1/prices/price_gone", 404, map[string]any{
		"error": map[string]any{
			"type": "invalid_request_error",
			"code": "resource_missing",
			"message": "No such price: price_gone",
		},
	})
	c := rs.client(t)

	plan, err := c.ScanAdoptions(context.Background(), adoptionPlanCfg("price_gone"))
	require.NoError(t, err)
	require.Len(t, plan.Conflicts, 1)
	require.Equal(t, AdoptionStripeObjectMissing, plan.Conflicts[0].Kind)
}

func TestScanAdoptions_MeterAlwaysConflicts(t *testing.T) {
	rs := newRecordingStripe(t)
	// Even an "ordinary" Stripe meter returns as a conflict — gatr's
	// event_name namespacing can't be retrofitted.
	rs.reply("GET", "/v1/billing/meters/mtr_existing", map[string]any{
		"id":           "mtr_existing",
		"object":       "billing.meter",
		"event_name":   "user_picked_this_name",
		"display_name": "Existing meter",
		"status":       "active",
	})
	c := rs.client(t)

	plan, err := c.ScanAdoptions(context.Background(), adoptionMeterCfg("mtr_existing"))
	require.NoError(t, err)
	require.Empty(t, plan.Candidates)
	require.Len(t, plan.Conflicts, 1)
	require.Equal(t, AdoptionMeterNotAdoptable, plan.Conflicts[0].Kind)
	require.Contains(t, plan.Conflicts[0].Message, "immutable")
}

func TestScanAdoptions_NoStripeIDsIsNoOp(t *testing.T) {
	// yaml has only null stripe_price_ids → no fetches, empty plan.
	rs := newRecordingStripe(t)
	c := rs.client(t)

	cfg := &schema.Config{
		Version: 4, Project: "demo",
		Plans: []schema.Plan{{
			ID: "free", Name: "Free",
			Features: []string{}, Limits: map[string]schema.NumberOrUnlimited{},
			Grants: map[string]schema.NumberOrUnlimited{}, Includes: map[string]schema.NumberOrUnlimited{},
		}},
	}
	plan, err := c.ScanAdoptions(context.Background(), cfg)
	require.NoError(t, err)
	require.False(t, plan.HasWork())
	require.Empty(t, rs.snapshot(), "no stripe ids → zero Stripe calls")
}

// ---- AdoptProduct / AdoptPrice ------------------------------------

func TestAdoptProduct_StampsMetadata(t *testing.T) {
	rs := newRecordingStripe(t)
	rs.reply("POST", "/v1/products/prod_new", map[string]any{
		"id":     "prod_new",
		"object": "product",
		"name":   "Pro",
		"active": true,
		"metadata": map[string]string{
			metaKeyManaged: "true",
			metaKeyGatrID:  gatrIDFor(testProjectID, "pro"),
		},
	})
	c := rs.client(t)

	got, err := c.AdoptProduct(context.Background(), "prod_new", "pro")
	require.NoError(t, err)
	require.Equal(t, "prod_new", got.StripeID)
	require.Equal(t, "pro", got.YamlID)

	reqs := rs.snapshot()
	require.Len(t, reqs, 1)
	require.Equal(t, "POST", reqs[0].Method)
	require.Equal(t, "/v1/products/prod_new", reqs[0].Path)
	require.Equal(t, "true", reqs[0].Form.Get("metadata[gatr_managed]"))
	require.Equal(t, gatrIDFor(testProjectID, "pro"), reqs[0].Form.Get("metadata[gatr_id]"))
	require.NotEmpty(t, reqs[0].IdempotencyKey)
}

func TestAdoptPrice_StampsMetadata(t *testing.T) {
	rs := newRecordingStripe(t)
	rs.reply("POST", "/v1/prices/price_new", map[string]any{
		"id":         "price_new",
		"object":     "price",
		"unit_amount": 2900,
		"currency":   "usd",
		"metadata": map[string]string{
			metaKeyManaged: "true",
			metaKeyGatrID:  gatrIDFor(testProjectID, "pro_monthly"),
		},
	})
	c := rs.client(t)

	got, err := c.AdoptPrice(context.Background(), "price_new", "pro_monthly")
	require.NoError(t, err)
	require.Equal(t, "price_new", got.StripeID)
	require.Equal(t, "pro_monthly", got.YamlID)

	reqs := rs.snapshot()
	require.Len(t, reqs, 1)
	require.Equal(t, "/v1/prices/price_new", reqs[0].Path)
	require.Equal(t, gatrIDFor(testProjectID, "pro_monthly"), reqs[0].Form.Get("metadata[gatr_id]"))
}

// ---- helpers contract ----------------------------------------------

func TestProductYamlForPriceYaml(t *testing.T) {
	require.Equal(t, "pro", productYamlForPriceYaml("pro_monthly"))
	require.Equal(t, "pro", productYamlForPriceYaml("pro_annual"))
	require.Equal(t, "", productYamlForPriceYaml("api_calls_metered"))
	require.Equal(t, "", productYamlForPriceYaml(""))
}
