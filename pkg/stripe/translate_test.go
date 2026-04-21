package stripe

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/EduardMaghakyan/gatr-cli/pkg/schema"
)

// intPtr is a tiny helper — schema.MeteredPrice.HardCap is *int.
func intPtr(v int) *int { return &v }

// strPtr is a tiny helper — schema.BillingInterval.StripePriceID is *string.
func strPtr(v string) *string { return &v }

func TestTranslateConfig_FullShape(t *testing.T) {
	cfg := &schema.Config{
		Version: schema.SupportedVersion,
		Project: "demo",
		Plans: []schema.Plan{
			{
				ID:   "free",
				Name: "Free",
				// no billing → entitlements-only, no prices generated
			},
			{
				ID:   "pro",
				Name: "Pro",
				Billing: &schema.Billing{
					Monthly: &schema.BillingInterval{
						AmountCents: 2900,
						Currency:    "usd",
					},
					Annual: &schema.BillingInterval{
						AmountCents:   29000,
						Currency:      "usd",
						StripePriceID: strPtr("price_existing_annual"),
					},
				},
			},
		},
		MeteredPrices: []schema.MeteredPrice{
			{
				ID:          "api_calls",
				Name:        "API calls",
				Unit:        "calls",
				UnitPrice:   0.01, // 1 cent per call — whole-cent path
				Period:      "month",
				Currency:    "usd",
				Aggregation: "sum",
				HardCap:     intPtr(1000),
			},
		},
	}
	ds, err := TranslateConfig(cfg)
	require.NoError(t, err)

	// Two plan products + one synthetic product for the metered price.
	productIDs := collectYamlIDs(ds.Products)
	require.ElementsMatch(t, []string{"free", "pro", "api_calls"}, productIDs)

	// pro monthly + pro annual + metered price.
	priceIDs := collectPriceYamlIDs(ds.Prices)
	require.ElementsMatch(t, []string{"pro_monthly", "pro_annual", "api_calls_metered"}, priceIDs)

	// Exactly one meter.
	require.Len(t, ds.Meters, 1)
	require.Equal(t, "api_calls", ds.Meters[0].YamlID)
	require.Equal(t, "sum", ds.Meters[0].Aggregation)

	// Cross-reference maps are populated so the apply engine can
	// resolve product_stripe_id / meter_stripe_id after create.
	require.Equal(t, "api_calls", ds.MeterYamlForPriceYaml["api_calls_metered"])
	require.Equal(t, "pro", ds.ProductYamlForPriceYaml["pro_monthly"])
	require.Equal(t, "api_calls", ds.ProductYamlForPriceYaml["api_calls_metered"])

	// Price shapes are correct.
	byID := map[string]PriceSpec{}
	for _, p := range ds.Prices {
		byID[p.YamlID] = p
	}
	require.EqualValues(t, 2900, byID["pro_monthly"].UnitAmount)
	require.Equal(t, "month", byID["pro_monthly"].Recurring.Interval)
	require.Equal(t, "licensed", byID["pro_monthly"].Recurring.UsageType)
	require.EqualValues(t, 29000, byID["pro_annual"].UnitAmount)
	require.Equal(t, "year", byID["pro_annual"].Recurring.Interval)

	// Whole-cent metered: UnitAmount = 1, no decimal-metadata carry.
	require.EqualValues(t, 1, byID["api_calls_metered"].UnitAmount)
	require.Equal(t, "metered", byID["api_calls_metered"].Recurring.UsageType)
	require.Nil(t, byID["api_calls_metered"].Metadata)
}

func TestTranslateConfig_SubCentMeteredPrice(t *testing.T) {
	// Sub-cent pricing (e.g. $0.001 per call = 0.1 cents) can't be
	// represented as a whole-cent unit_amount. TranslateConfig must
	// stash the decimal form in metadata so the upsert layer knows
	// to use unit_amount_decimal.
	cfg := &schema.Config{
		Version: schema.SupportedVersion,
		Project: "demo",
		MeteredPrices: []schema.MeteredPrice{
			{
				ID:          "api_calls",
				Name:        "API calls",
				Unit:        "calls",
				UnitPrice:   0.001,
				Period:      "month",
				Currency:    "usd",
				Aggregation: "sum",
			},
		},
	}
	ds, err := TranslateConfig(cfg)
	require.NoError(t, err)
	require.Len(t, ds.Prices, 1)

	meteredPrice := ds.Prices[0]
	require.EqualValues(t, 0, meteredPrice.UnitAmount, "sub-cent price → UnitAmount=0, decimal in metadata")
	require.Equal(t, "0.1", meteredPrice.Metadata[metaKeyUnitAmountDecimal])
}

func collectYamlIDs(ps []ProductSpec) []string {
	out := make([]string, len(ps))
	for i, p := range ps {
		out[i] = p.YamlID
	}
	return out
}

func collectPriceYamlIDs(ps []PriceSpec) []string {
	out := make([]string, len(ps))
	for i, p := range ps {
		out[i] = p.YamlID
	}
	return out
}
