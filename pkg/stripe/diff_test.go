package stripe

import (
	"sort"
	"testing"

	"github.com/stretchr/testify/require"
)

// fixtureDesired is the canonical "pro plan + api meter" yaml the
// diff tests drive against. Matches the shape TranslateConfig would
// produce from a 1-product + 2-interval + 1-meter gatr.yaml.
func fixtureDesired() DesiredState {
	return DesiredState{
		Products: []ProductSpec{
			{YamlID: "pro", Name: "Pro", Active: true},
			{YamlID: "api_calls", Name: "API calls", Description: "Metered: calls", Active: true},
		},
		Prices: []PriceSpec{
			{
				YamlID:     "pro_monthly",
				UnitAmount: 2900,
				Currency:   "usd",
				Active:     true,
				Recurring:  &RecurringInfo{Interval: "month", UsageType: "licensed"},
			},
			{
				YamlID:     "api_calls_metered",
				UnitAmount: 0,
				Currency:   "usd",
				Active:     true,
				Recurring:  &RecurringInfo{Interval: "month", UsageType: "metered"},
			},
		},
		Meters: []MeterSpec{
			{YamlID: "api_calls", DisplayName: "API calls", Aggregation: "sum"},
		},
	}
}

// ---- Test 1: empty Stripe → all creates ------------------------------------

func TestComputeDiff_EmptyStripe_AllCreates(t *testing.T) {
	plan := ComputeDiff(fixtureDesired(), CurrentState{})
	require.True(t, plan.HasChanges())
	require.Equal(t, 2+2+1, plan.Count(ActionCreated))
	require.Equal(t, 0, plan.Count(ActionNoOp))
	require.Equal(t, 0, plan.Count(ActionArchived))

	// Every op must be a Create and carry a Spec pointer of the
	// matching kind — the apply engine's invariant.
	for _, op := range plan.ProductOps {
		require.Equal(t, ActionCreated, op.Action)
		require.NotNil(t, op.ProductSpec)
		require.Empty(t, op.StripeID)
	}
	for _, op := range plan.PriceOps {
		require.Equal(t, ActionCreated, op.Action)
		require.NotNil(t, op.PriceSpec)
	}
	for _, op := range plan.MeterOps {
		require.Equal(t, ActionCreated, op.Action)
		require.NotNil(t, op.MeterSpec)
	}
}

// ---- Test 2: already-converged Stripe → all no-ops -------------------------

func TestComputeDiff_FullStripe_AllNoOps(t *testing.T) {
	current := CurrentState{
		Products: []ManagedProduct{
			{StripeID: "prod_pro", YamlID: "pro", Name: "Pro", Active: true},
			{StripeID: "prod_api", YamlID: "api_calls", Name: "API calls",
				Description: "Metered: calls", Active: true},
		},
		Prices: []ManagedPrice{
			{
				StripeID: "price_pro_m", YamlID: "pro_monthly",
				UnitAmount: 2900, Currency: "usd", Active: true,
				Recurring: &RecurringInfo{Interval: "month", IntervalCount: 1, UsageType: "licensed"},
			},
			{
				StripeID: "price_api_m", YamlID: "api_calls_metered",
				UnitAmount: 0, Currency: "usd", Active: true,
				Recurring: &RecurringInfo{Interval: "month", IntervalCount: 1, UsageType: "metered"},
			},
		},
		Meters: []ManagedMeter{
			{
				StripeID: "mtr_api", YamlID: "api_calls",
				DisplayName: "API calls", Aggregation: "sum", Status: "active",
			},
		},
	}
	plan := ComputeDiff(fixtureDesired(), current)
	require.False(t, plan.HasChanges())
	require.Equal(t, 2+2+1, plan.Count(ActionNoOp))
}

// ---- Test 3: mid-state mix (creates + noops + updates + archives) ----------

func TestComputeDiff_MidState_MixedOps(t *testing.T) {
	// Pro product exists with the correct name (no-op).
	// API-calls product exists with a STALE name (update).
	// A third "legacy_plan" product exists in Stripe but NOT in yaml
	//   → archive.
	// pro_monthly exists with a stale UnitAmount (replace — hard field).
	// api_calls_metered is missing entirely (create).
	// Meter api_calls exists but status=active, display_name stale (update).
	current := CurrentState{
		Products: []ManagedProduct{
			{StripeID: "prod_pro", YamlID: "pro", Name: "Pro", Active: true},
			{StripeID: "prod_api", YamlID: "api_calls", Name: "STALE", Active: true},
			{StripeID: "prod_legacy", YamlID: "legacy_plan", Name: "Legacy", Active: true},
		},
		Prices: []ManagedPrice{
			{
				StripeID: "price_pro_m_old", YamlID: "pro_monthly",
				UnitAmount: 1900, // stale: spec says 2900
				Currency:   "usd", Active: true,
				Recurring: &RecurringInfo{Interval: "month", IntervalCount: 1, UsageType: "licensed"},
			},
		},
		Meters: []ManagedMeter{
			{
				StripeID: "mtr_api", YamlID: "api_calls",
				DisplayName: "STALE", Aggregation: "sum", Status: "active",
			},
		},
	}
	plan := ComputeDiff(fixtureDesired(), current)

	require.True(t, plan.HasChanges())

	// Products: pro=noop, api_calls=update, legacy=archive, no new ones.
	productActions := collectActions(plan.ProductOps)
	require.Equal(t, map[string]ApplyAction{
		"pro":         ActionNoOp,
		"api_calls":   ActionUpdated,
		"legacy_plan": ActionArchived,
	}, productActions)

	// Prices: pro_monthly=replace (hard change), api_calls_metered=create.
	priceActions := collectActions(plan.PriceOps)
	require.Equal(t, map[string]ApplyAction{
		"pro_monthly":       ActionReplaced,
		"api_calls_metered": ActionCreated,
	}, priceActions)
	// The replace op must list "amount" in Changes so the CLI renders
	// it to the user.
	for _, op := range plan.PriceOps {
		if op.YamlID == "pro_monthly" {
			require.Contains(t, op.Changes, "amount")
		}
	}

	// Meters: display_name changed → update.
	meterActions := collectActions(plan.MeterOps)
	require.Equal(t, map[string]ApplyAction{"api_calls": ActionUpdated}, meterActions)
}

// ---- Test 4: archived-in-Stripe not re-archived (re-run idempotency) ------

func TestComputeDiff_AlreadyArchived_NoDoubleArchive(t *testing.T) {
	// An ex-plan in Stripe that's already archived must NOT produce
	// another archive op — re-runs would thrash the Stripe audit trail.
	current := CurrentState{
		Products: []ManagedProduct{
			{StripeID: "prod_dead", YamlID: "retired_plan", Name: "Retired", Active: false},
		},
	}
	plan := ComputeDiff(DesiredState{}, current)
	require.False(t, plan.HasChanges(), "archived product must not cause a new archive op")
}

// ---- Helper -----------------------------------------------------------------

func collectActions(ops []DiffOp) map[string]ApplyAction {
	out := make(map[string]ApplyAction, len(ops))
	for _, op := range ops {
		out[op.YamlID] = op.Action
	}
	// Ensure determinism for golden-style assertions (deterministic
	// map iteration is provided by Go — this is just for readability).
	keys := make([]string, 0, len(out))
	for k := range out {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return out
}
