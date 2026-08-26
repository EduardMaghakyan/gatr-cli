package cli

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/EduardMaghakyan/gatr-cli/pkg/schema/yamlpatch"
	gstripe "github.com/EduardMaghakyan/gatr-cli/pkg/stripe"
)

func priceResult(yamlID, stripeID string, action gstripe.ApplyAction) gstripe.ApplyResult {
	return gstripe.ApplyResult{
		Op:       gstripe.DiffOp{Resource: gstripe.ResourcePrice, YamlID: yamlID, Action: action},
		StripeID: stripeID,
	}
}

func TestCreatedPackPriceWritesItsIDBack(t *testing.T) {
	// Without this the id never reaches gatr.yaml, so the app has no price to
	// check out against and the pack is unbuyable.
	got := patchesFromResults([]gstripe.ApplyResult{
		priceResult("pack_20"+gstripe.PriceYamlSuffixPack, "price_abc", gstripe.ActionCreated),
	})
	require.Equal(t, []yamlpatch.Patch{
		{Kind: yamlpatch.KindCreditPack, YamlID: "pack_20", StripeID: "price_abc"},
	}, got)
}

func TestPackSuffixIsNotConfusedWithAPlanInterval(t *testing.T) {
	// "_pack" and "_monthly" are both suffixes on the same yaml_id space, so
	// a pack must not be patched as a plan or vice versa.
	got := patchesFromResults([]gstripe.ApplyResult{
		priceResult("pro"+gstripe.PriceYamlSuffixMonthly, "price_m", gstripe.ActionCreated),
		priceResult("pack_20"+gstripe.PriceYamlSuffixPack, "price_p", gstripe.ActionCreated),
	})
	require.Len(t, got, 2)
	require.Equal(t, yamlpatch.KindPlanMonthly, got[0].Kind)
	require.Equal(t, "pro", got[0].YamlID)
	require.Equal(t, yamlpatch.KindCreditPack, got[1].Kind)
	require.Equal(t, "pack_20", got[1].YamlID)
}

func TestAPackPriceThatAlreadyExistedIsNotRepatched(t *testing.T) {
	// Same rule as plans: only creates (and replacements) carry a new id.
	got := patchesFromResults([]gstripe.ApplyResult{
		priceResult("pack_20"+gstripe.PriceYamlSuffixPack, "price_abc", gstripe.ActionNoOp),
	})
	require.Empty(t, got)
}

func TestPackPathIsShownInTheIDTable(t *testing.T) {
	// The operator reads this to check the right field was written.
	got := yamlPathFor(yamlpatch.Patch{Kind: yamlpatch.KindCreditPack, YamlID: "pack_20"})
	require.Equal(t, "credit_packs[pack_20].stripe_price_id", got)
}
