package stripe

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/EduardMaghakyan/gatr-cli/pkg/schema"
)

func starterDesired(pinned string) DesiredState {
	ds := DesiredState{
		Products: []ProductSpec{{YamlID: "starter", Name: "Starter", Active: true}},
		Prices: []PriceSpec{{
			YamlID: "starter_monthly", UnitAmount: 3500, Currency: "usd", Active: true,
			Recurring: &RecurringInfo{Interval: "month", UsageType: "licensed"},
		}},
		PinnedPriceByYaml: map[string]string{},
	}
	if pinned != "" {
		ds.PinnedPriceByYaml["starter_monthly"] = pinned
	}
	return ds
}

func starterPrice(stripeID string, amount int64, active bool) ManagedPrice {
	return ManagedPrice{
		StripeID: stripeID, YamlID: "starter_monthly", UnitAmount: amount, Currency: "usd", Active: active,
		Recurring: &RecurringInfo{Interval: "month", UsageType: "licensed"},
	}
}

func starterStripe(newestFirst ...ManagedPrice) CurrentState {
	return CurrentState{
		Products: []ManagedProduct{{StripeID: "prod_starter", YamlID: "starter", Name: "Starter", Active: true}},
		Prices:   newestFirst,
	}
}

func onlyPriceOp(t *testing.T, plan DiffPlan) DiffOp {
	t.Helper()
	require.Len(t, plan.PriceOps, 1)
	return plan.PriceOps[0]
}

func TestComputeDiff_AnArchivedOlderPriceDoesNotShadowTheLiveOne(t *testing.T) {
	plan := ComputeDiff(starterDesired(""), starterStripe(
		starterPrice("price_live_35", 3500, true),
		starterPrice("price_archived_25", 2500, false),
	))

	op := onlyPriceOp(t, plan)
	require.Equal(t, ActionNoOp, op.Action)
	require.Equal(t, "price_live_35", op.StripeID)
	require.False(t, plan.HasChanges())
}

func TestComputeDiff_TheLivePriceWinsWhicheverOrderStripeListsThem(t *testing.T) {
	plan := ComputeDiff(starterDesired(""), starterStripe(
		starterPrice("price_archived_25", 2500, false),
		starterPrice("price_live_35", 3500, true),
	))

	op := onlyPriceOp(t, plan)
	require.Equal(t, ActionNoOp, op.Action)
	require.Equal(t, "price_live_35", op.StripeID)
}

func TestComputeDiff_ThePinnedPriceIsComparedWhenSeveralAreLive(t *testing.T) {
	plan := ComputeDiff(starterDesired("price_pinned"), starterStripe(
		starterPrice("price_newer_duplicate", 3500, true),
		starterPrice("price_pinned", 3500, true),
		starterPrice("price_archived_25", 2500, false),
	))

	op := onlyPriceOp(t, plan)
	require.Equal(t, ActionNoOp, op.Action)
	require.Equal(t, "price_pinned", op.StripeID)
}

func TestComputeDiff_AnArchivedPinnedPriceIsReactivatedNotReplaced(t *testing.T) {
	plan := ComputeDiff(starterDesired("price_pinned"), starterStripe(
		starterPrice("price_other_live", 3500, true),
		starterPrice("price_pinned", 3500, false),
	))

	op := onlyPriceOp(t, plan)
	require.Equal(t, ActionUpdated, op.Action)
	require.Equal(t, "price_pinned", op.StripeID)
	require.Equal(t, []string{"active"}, op.Changes)
}

func TestComputeDiff_ARealPriceChangeStillReplacesThePinnedPrice(t *testing.T) {
	desired := starterDesired("price_pinned")
	desired.Prices[0].UnitAmount = 4000

	plan := ComputeDiff(desired, starterStripe(
		starterPrice("price_pinned", 3500, true),
		starterPrice("price_archived_25", 2500, false),
	))

	op := onlyPriceOp(t, plan)
	require.Equal(t, ActionReplaced, op.Action)
	require.Equal(t, "price_pinned", op.StripeID)
	require.Equal(t, []string{"amount"}, op.Changes)
}

func TestTranslateConfig_CarriesThePriceIdsTheYamlPins(t *testing.T) {
	monthly, annual, pack := "price_starter_m", "price_starter_y", "price_pack_20"
	cfg := &schema.Config{
		Version: schema.SupportedVersion,
		Project: "demo",
		Credits: []schema.Credit{{ID: "ai_credits", Name: "AI credits", Rollover: true}},
		Plans: []schema.Plan{
			{ID: "free", Name: "Free"},
			{ID: "starter", Name: "Starter", Billing: &schema.Billing{
				Monthly: &schema.BillingInterval{AmountCents: 3500, Currency: "usd", StripePriceID: &monthly},
				Annual:  &schema.BillingInterval{AmountCents: 35000, Currency: "usd", StripePriceID: &annual},
			}},
			{ID: "advanced", Name: "Advanced", Billing: &schema.Billing{
				Monthly: &schema.BillingInterval{AmountCents: 6500, Currency: "usd"},
			}},
		},
		CreditPacks: []schema.CreditPack{
			{ID: "pack_20", Name: "20 credits", Credit: "ai_credits", Credits: 20,
				AmountCents: 1000, Currency: "usd", StripePriceID: &pack},
		},
	}

	ds, err := TranslateConfig(cfg)
	require.NoError(t, err)

	require.Equal(t, map[string]string{
		"starter_monthly": monthly,
		"starter_annual":  annual,
		"pack_20_pack":    pack,
	}, ds.PinnedPriceByYaml)
}
