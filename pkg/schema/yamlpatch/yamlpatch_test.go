package yamlpatch

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// sourceYAML mirrors a real gatr.yaml — with comments, quoted strings,
// and null placeholders in the spots Apply needs to fill in. These are
// the formatting choices that yamlpatch MUST preserve.
const sourceYAML = `version: 4
project: demo

# Plans section — defines product tiers.
plans:
  - id: pro
    name: "Pro"
    price_display: "$29 / month"
    billing:
      monthly:
        amount_cents: 2900
        currency: usd
        stripe_price_id: null  # ` + "`gatr push`" + ` fills this in
      annual:
        amount_cents: 29000
        currency: usd
        stripe_price_id: null
    features: []
    limits: {}
    grants: {}
    includes: {}

metered_prices:
  - id: api_calls
    name: "API calls"
    unit: "calls"
    unit_price: 0.001
    stripe_meter_id: null  # gatr push fills this in
    period: month
    currency: usd
    aggregation: sum
`

func TestApply_SetsAllThreeIDKinds(t *testing.T) {
	patches := []Patch{
		{Kind: KindPlanMonthly, YamlID: "pro", StripeID: "price_pro_m_live"},
		{Kind: KindPlanAnnual, YamlID: "pro", StripeID: "price_pro_a_live"},
		{Kind: KindMeteredPrice, YamlID: "api_calls", StripeID: "mtr_api_live"},
	}
	out, unresolved, err := Apply([]byte(sourceYAML), patches)
	require.NoError(t, err)
	require.Empty(t, unresolved)

	// The three new values round-trip as double-quoted strings at the
	// original line positions.
	got := string(out)
	require.Contains(t, got, `stripe_price_id: "price_pro_m_live"`)
	require.Contains(t, got, `stripe_price_id: "price_pro_a_live"`)
	require.Contains(t, got, `stripe_meter_id: "mtr_api_live"`)

	// Comments survive — the critical invariant. Specifically the
	// inline comments next to the patched lines.
	require.Contains(t, got, "# Plans section")
	require.Contains(t, got, "gatr push")

	// Ordering is preserved: "plans" comes before "metered_prices".
	plansIdx := strings.Index(got, "plans:")
	meteredIdx := strings.Index(got, "metered_prices:")
	require.Greater(t, meteredIdx, plansIdx, "section order must be preserved")
}

func TestApply_UnresolvedYamlIDsReturned(t *testing.T) {
	patches := []Patch{
		{Kind: KindPlanMonthly, YamlID: "nonexistent", StripeID: "price_x"},
	}
	_, unresolved, err := Apply([]byte(sourceYAML), patches)
	require.NoError(t, err)
	require.Len(t, unresolved, 1)
	require.Equal(t, "nonexistent", unresolved[0].YamlID)
}

func TestApply_RejectsEmptyStripeID(t *testing.T) {
	_, _, err := Apply([]byte(sourceYAML), []Patch{
		{Kind: KindPlanMonthly, YamlID: "pro", StripeID: ""},
	})
	require.Error(t, err)
}

func TestApply_RejectsMalformedYaml(t *testing.T) {
	_, _, err := Apply([]byte("::: not yaml at all :::"), nil)
	require.Error(t, err)
}

func TestApply_IdempotentReapply(t *testing.T) {
	// Running the same patch twice must be a no-op — the output of
	// the first run is a valid input for the second.
	patches := []Patch{
		{Kind: KindPlanMonthly, YamlID: "pro", StripeID: "price_pro_m_live"},
	}
	once, _, err := Apply([]byte(sourceYAML), patches)
	require.NoError(t, err)
	twice, _, err := Apply(once, patches)
	require.NoError(t, err)
	require.Equal(t, string(once), string(twice))
}

func TestApply_PreservesAmountAndCurrencyFields(t *testing.T) {
	// Regression guard: the patcher only touches the stripe_* scalars
	// — it must leave the sibling amount_cents + currency untouched.
	patches := []Patch{
		{Kind: KindPlanMonthly, YamlID: "pro", StripeID: "price_x"},
	}
	out, _, err := Apply([]byte(sourceYAML), patches)
	require.NoError(t, err)
	got := string(out)
	require.Contains(t, got, "amount_cents: 2900")
	require.Contains(t, got, "currency: usd")
}
