package stripe

import (
	"testing"

	"github.com/stretchr/testify/require"
	stripesdk "github.com/stripe/stripe-go/v82"
)

// Fixture shortcuts. Keep them tiny and focused — each test builds
// exactly what it needs rather than sharing elaborate setups.

func prod(id, name string, active bool, metadata map[string]string) *stripesdk.Product {
	return &stripesdk.Product{
		ID:       id,
		Name:     name,
		Active:   active,
		Metadata: metadata,
	}
}

func priceRecurring(id, productID, interval, usageType string, amount int64, active bool) *stripesdk.Price {
	return &stripesdk.Price{
		ID:            id,
		Active:        active,
		Currency:      "usd",
		BillingScheme: stripesdk.PriceBillingSchemePerUnit,
		UnitAmount:    amount,
		Product:       &stripesdk.Product{ID: productID},
		Type:          stripesdk.PriceTypeRecurring,
		Recurring: &stripesdk.PriceRecurring{
			Interval:      stripesdk.PriceRecurringInterval(interval),
			IntervalCount: 1,
			UsageType:     stripesdk.PriceRecurringUsageType(usageType),
		},
	}
}

func meter(id, displayName, formula, status string) *stripesdk.BillingMeter {
	m := &stripesdk.BillingMeter{
		ID:          id,
		DisplayName: displayName,
		Status:      stripesdk.BillingMeterStatus(status),
	}
	if formula != "" {
		m.DefaultAggregation = &stripesdk.BillingMeterDefaultAggregation{
			Formula: stripesdk.BillingMeterDefaultAggregationFormula(formula),
		}
	}
	return m
}

// ---- Empty account -------------------------------------------------

func TestTranslateFromStripe_EmptyAccount(t *testing.T) {
	got := TranslateFromStripe("demo", nil, nil, nil)
	require.Equal(t, 4, got.Config.Version)
	require.Equal(t, "demo", got.Config.Project)
	require.Empty(t, got.Config.Plans)
	require.Empty(t, got.Config.MeteredPrices)
	require.Empty(t, got.Notes)
	// Non-nil empty slices so the renderer emits `[]`, not `null`.
	require.NotNil(t, got.Config.Features)
	require.NotNil(t, got.Config.Limits)
	require.NotNil(t, got.Config.Credits)
	require.NotNil(t, got.Config.Operations)
}

// ---- Plans + billing -----------------------------------------------

func TestTranslateFromStripe_MonthlyAndAnnualPair(t *testing.T) {
	products := []*stripesdk.Product{prod("prod_A", "Pro", true, nil)}
	prices := []*stripesdk.Price{
		priceRecurring("price_m", "prod_A", "month", "licensed", 2900, true),
		priceRecurring("price_y", "prod_A", "year", "licensed", 29000, true),
	}
	got := TranslateFromStripe("demo", products, prices, nil)
	require.Len(t, got.Config.Plans, 1)
	p := got.Config.Plans[0]
	require.Equal(t, "pro", p.ID)
	require.Equal(t, "Pro", p.Name)
	require.NotNil(t, p.Billing)
	require.NotNil(t, p.Billing.Monthly)
	require.Equal(t, 2900, p.Billing.Monthly.AmountCents)
	require.Equal(t, "price_m", *p.Billing.Monthly.StripePriceID)
	require.NotNil(t, p.Billing.Annual)
	require.Equal(t, 29000, p.Billing.Annual.AmountCents)
	require.Equal(t, "price_y", *p.Billing.Annual.StripePriceID)
}

func TestTranslateFromStripe_EntitlementOnlyProduct(t *testing.T) {
	// Product with zero prices → plan exists with no billing block.
	// This matches the "Free tier" pattern gatr's templates use.
	products := []*stripesdk.Product{prod("prod_F", "Free", true, nil)}
	got := TranslateFromStripe("demo", products, nil, nil)
	require.Len(t, got.Config.Plans, 1)
	require.Nil(t, got.Config.Plans[0].Billing)
}

// ---- Skip paths ----------------------------------------------------

func TestTranslateFromStripe_OneTimePriceSkipped(t *testing.T) {
	products := []*stripesdk.Product{prod("prod_A", "Pro", true, nil)}
	onetime := &stripesdk.Price{
		ID: "price_once", Active: true, Currency: "usd",
		BillingScheme: stripesdk.PriceBillingSchemePerUnit,
		UnitAmount:    5000,
		Product:       &stripesdk.Product{ID: "prod_A"},
		Type:          stripesdk.PriceTypeOneTime,
		// Recurring is nil — that's what makes it one-time.
	}
	got := TranslateFromStripe("demo", products, []*stripesdk.Price{onetime}, nil)
	require.Len(t, got.Config.Plans, 1)
	require.Nil(t, got.Config.Plans[0].Billing, "one-time price must not produce a billing block")
	require.NotEmpty(t, got.Notes)
	require.Equal(t, NoteSkipped, got.Notes[0].Kind)
	require.Contains(t, got.Notes[0].Reason, "one-time")
}

func TestTranslateFromStripe_TieredPriceSkipped(t *testing.T) {
	products := []*stripesdk.Product{prod("prod_A", "Pro", true, nil)}
	tiered := &stripesdk.Price{
		ID: "price_t", Active: true, Currency: "usd",
		BillingScheme: stripesdk.PriceBillingSchemeTiered,
		Tiers: []*stripesdk.PriceTier{
			{UpTo: 100, UnitAmount: 10},
			{UpTo: 0, UnitAmount: 5}, // UpTo=0 means "infinity" in Stripe
		},
		Product: &stripesdk.Product{ID: "prod_A"},
		Type:    stripesdk.PriceTypeRecurring,
		Recurring: &stripesdk.PriceRecurring{
			Interval: "month", IntervalCount: 1, UsageType: "licensed",
		},
	}
	got := TranslateFromStripe("demo", products, []*stripesdk.Price{tiered}, nil)
	require.Len(t, got.Config.Plans, 1)
	require.Nil(t, got.Config.Plans[0].Billing)
	require.NotEmpty(t, got.Notes)
	require.Contains(t, got.Notes[0].Reason, "tiered")
}

func TestTranslateFromStripe_ArchivedProductSkipped(t *testing.T) {
	products := []*stripesdk.Product{
		prod("prod_A", "Pro", true, nil),
		prod("prod_old", "Legacy", false, nil), // archived
	}
	got := TranslateFromStripe("demo", products, nil, nil)
	require.Len(t, got.Config.Plans, 1)
	require.Equal(t, "pro", got.Config.Plans[0].ID)
	// An archived_skipped note for the legacy product.
	var found bool
	for _, n := range got.Notes {
		if n.Kind == NoteArchivedSkip && n.Subject == "prod_old" {
			found = true
		}
	}
	require.True(t, found, "expected an archived-skipped note for prod_old")
}

func TestTranslateFromStripe_ArchivedPriceSkipped(t *testing.T) {
	products := []*stripesdk.Product{prod("prod_A", "Pro", true, nil)}
	prices := []*stripesdk.Price{
		priceRecurring("price_old", "prod_A", "month", "licensed", 1900, false), // archived
		priceRecurring("price_m", "prod_A", "month", "licensed", 2900, true),
	}
	got := TranslateFromStripe("demo", products, prices, nil)
	require.Len(t, got.Config.Plans, 1)
	require.Equal(t, "price_m", *got.Config.Plans[0].Billing.Monthly.StripePriceID)
}

func TestTranslateFromStripe_IntervalCountRejected(t *testing.T) {
	// Stripe allows interval_count>1 (e.g. every 3 months). gatr
	// doesn't — it only models 1 month / 1 year. Skip + note.
	products := []*stripesdk.Product{prod("prod_A", "Pro", true, nil)}
	pr := priceRecurring("price_q", "prod_A", "month", "licensed", 7900, true)
	pr.Recurring.IntervalCount = 3
	got := TranslateFromStripe("demo", products, []*stripesdk.Price{pr}, nil)
	require.Len(t, got.Config.Plans, 1)
	require.Nil(t, got.Config.Plans[0].Billing)
	require.NotEmpty(t, got.Notes)
	require.Contains(t, got.Notes[0].Reason, "interval_count=3")
}

// ---- Metered prices ------------------------------------------------

func TestTranslateFromStripe_MeterPairedWithPrice(t *testing.T) {
	meters := []*stripesdk.BillingMeter{meter("mtr_A", "API calls", "sum", "active")}
	mp := priceRecurring("price_mtr", "prod_syn", "month", "metered", 0, true)
	mp.UnitAmountDecimal = 0.1 // sub-cent: $0.001 per unit
	mp.Recurring.Meter = "mtr_A"
	got := TranslateFromStripe("demo", nil, []*stripesdk.Price{mp}, meters)
	require.Len(t, got.Config.MeteredPrices, 1)
	entry := got.Config.MeteredPrices[0]
	require.Equal(t, "api-calls", entry.ID)
	require.Equal(t, "API calls", entry.Name)
	require.Equal(t, "month", entry.Period)
	require.Equal(t, "sum", entry.Aggregation)
	require.Equal(t, "mtr_A", *entry.StripeMeterID)
	// 0.1 cents / 100 = 0.001 dollars
	require.InDelta(t, 0.001, entry.UnitPrice, 1e-9)
}

func TestTranslateFromStripe_MeterOrphan_NoPrice(t *testing.T) {
	meters := []*stripesdk.BillingMeter{meter("mtr_A", "Tokens", "sum", "active")}
	got := TranslateFromStripe("demo", nil, nil, meters)
	require.Len(t, got.Config.MeteredPrices, 1)
	require.EqualValues(t, 0, got.Config.MeteredPrices[0].UnitPrice)

	var found bool
	for _, n := range got.Notes {
		if n.Kind == NoteMeterOrphan && n.Subject == "mtr_A" {
			found = true
		}
	}
	require.True(t, found, "expected an orphan note for mtr_A")
}

func TestTranslateFromStripe_InactiveMeterSkipped(t *testing.T) {
	meters := []*stripesdk.BillingMeter{meter("mtr_A", "Tokens", "sum", "inactive")}
	got := TranslateFromStripe("demo", nil, nil, meters)
	require.Empty(t, got.Config.MeteredPrices)
	require.NotEmpty(t, got.Notes)
}

// ---- ID collisions + per-seat hints --------------------------------

func TestTranslateFromStripe_KebabCollision(t *testing.T) {
	products := []*stripesdk.Product{
		prod("prod_A", "Pro", true, nil),
		prod("prod_B", "PRO", true, nil), // kebab collision: "pro"
	}
	got := TranslateFromStripe("demo", products, nil, nil)
	require.Len(t, got.Config.Plans, 2)
	require.Equal(t, "pro", got.Config.Plans[0].ID)
	require.Equal(t, "pro-2", got.Config.Plans[1].ID)

	var found bool
	for _, n := range got.Notes {
		if n.Kind == NoteIDCollision {
			found = true
		}
	}
	require.True(t, found, "expected an id-collision note")
}

func TestTranslateFromStripe_PerSeatHint(t *testing.T) {
	products := []*stripesdk.Product{
		prod("prod_A", "Team", true, map[string]string{"per_seat": "true"}),
	}
	prices := []*stripesdk.Price{
		priceRecurring("price_m", "prod_A", "month", "licensed", 1500, true),
	}
	got := TranslateFromStripe("demo", products, prices, nil)
	require.Len(t, got.Config.Plans, 1)

	var found bool
	for _, n := range got.Notes {
		if n.Kind == NotePerSeatHint {
			found = true
			require.Contains(t, n.Reason, "per_seat_pricing")
		}
	}
	require.True(t, found, "expected a per-seat hint note")
}

// ---- Helpers -------------------------------------------------------

func TestKebab(t *testing.T) {
	cases := []struct{ in, fb, want string }{
		{"Pro Plus", "x", "pro-plus"},
		{"My  App", "x", "my-app"},
		{"  ", "fallback_id", "fallback_id"},
		{"", "fallback_id", "fallback_id"},
		{"!@#$%^", "x", "x"},
		{"alpha123", "x", "alpha123"},
		{"TRIM-ME-", "x", "trim-me"},
	}
	for _, c := range cases {
		require.Equal(t, c.want, kebab(c.in, c.fb), "kebab(%q, %q)", c.in, c.fb)
	}
}

func TestUniqueSlug(t *testing.T) {
	used := map[string]int{}
	require.Equal(t, "a", uniqueSlug("a", used))
	require.Equal(t, "a-2", uniqueSlug("a", used))
	require.Equal(t, "a-3", uniqueSlug("a", used))
	require.Equal(t, "b", uniqueSlug("b", used))
}
