package stripe

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/EduardMaghakyan/gatr-cli/pkg/schema"
)

func packConfig() *schema.Config {
	return &schema.Config{
		Version: schema.SupportedVersion,
		Project: "demo",
		Credits: []schema.Credit{{ID: "ai_credits", Name: "AI credits", Rollover: true}},
		CreditPacks: []schema.CreditPack{
			{ID: "pack_20", Name: "20 credits", Credit: "ai_credits", Credits: 20,
				AmountCents: 1000, Currency: "usd", PriceDisplay: "$10"},
			{ID: "pack_100", Name: "100 credits", Credit: "ai_credits", Credits: 100,
				AmountCents: 5000, Currency: "usd"},
		},
		Plans: []schema.Plan{{ID: "free", Name: "Free"}},
	}
}

func findPrice(t *testing.T, ds DesiredState, yamlID string) PriceSpec {
	t.Helper()
	for _, p := range ds.Prices {
		if p.YamlID == yamlID {
			return p
		}
	}
	t.Fatalf("no price with yaml id %q (have %+v)", yamlID, ds.Prices)
	return PriceSpec{}
}

func TestPackPriceIsOneTimeNotRecurring(t *testing.T) {
	// Recurring == nil is the entire difference between a pack and a
	// subscription: the upsert layer omits the recurring params, which is how
	// Stripe is told this is a single payment. A non-nil value here would
	// silently sign customers up to a monthly charge.
	ds, err := TranslateConfig(packConfig())
	require.NoError(t, err)

	price := findPrice(t, ds, "pack_20"+PriceYamlSuffixPack)
	require.Nil(t, price.Recurring, "a credit pack must never create a recurring price")
	require.Equal(t, int64(1000), price.UnitAmount)
	require.Equal(t, "usd", price.Currency)
	require.True(t, price.Active)
}

func TestEveryPackGetsItsOwnPrice(t *testing.T) {
	ds, err := TranslateConfig(packConfig())
	require.NoError(t, err)
	findPrice(t, ds, "pack_20"+PriceYamlSuffixPack)
	findPrice(t, ds, "pack_100"+PriceYamlSuffixPack)
}

func TestPackGetsItsOwnProduct(t *testing.T) {
	// Hanging pack prices off a plan's product would make a one-off purchase
	// look like part of that subscription in the dashboard and in every
	// revenue report built on it.
	ds, err := TranslateConfig(packConfig())
	require.NoError(t, err)

	var found bool
	for _, p := range ds.Products {
		if p.YamlID == "pack_20" {
			found = true
			require.Equal(t, "20 credits", p.Name)
			require.Equal(t, "$10", p.Description)
		}
	}
	require.True(t, found, "pack_20 must have its own product")
	require.Equal(t, "pack_20", ds.ProductYamlForPriceYaml["pack_20"+PriceYamlSuffixPack])
}

func TestPacksDoNotDisturbPlans(t *testing.T) {
	// The free plan still produces a product and no prices.
	ds, err := TranslateConfig(packConfig())
	require.NoError(t, err)

	for _, p := range ds.Prices {
		require.NotEqual(t, "free"+PriceYamlSuffixMonthly, p.YamlID)
	}
	var freeProduct bool
	for _, p := range ds.Products {
		if p.YamlID == "free" {
			freeProduct = true
		}
	}
	require.True(t, freeProduct)
}

func TestAConfigWithNoPacksIsUnchanged(t *testing.T) {
	cfg := packConfig()
	cfg.CreditPacks = nil
	ds, err := TranslateConfig(cfg)
	require.NoError(t, err)
	require.Empty(t, ds.Prices, "no packs, no plan billing → no prices")
	require.Len(t, ds.Products, 1, "just the free plan's product")
}
