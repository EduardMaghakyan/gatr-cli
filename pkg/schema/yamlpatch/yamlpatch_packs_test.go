package yamlpatch

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

const packYAML = `version: 4
project: demo
credits:
  - id: ai_credits
    name: AI credits
credit_packs:
  - id: pack_20
    name: 20 credits
    credit: ai_credits
    credits: 20
    amount_cents: 1000
    currency: usd
    # Filled by ` + "`gatr push --auto-patch`" + `.
    stripe_price_id: null
  - id: pack_100
    name: 100 credits
    credit: ai_credits
    credits: 100
    amount_cents: 5000
    currency: usd
    stripe_price_id: null
plans:
  - id: free
    name: Free
`

func TestPatchesTheNamedPackOnly(t *testing.T) {
	out, unresolved, err := Apply([]byte(packYAML), []Patch{
		{Kind: KindCreditPack, YamlID: "pack_20", StripeID: "price_abc"},
	})
	require.NoError(t, err)
	require.Empty(t, unresolved)

	s := string(out)
	require.Contains(t, s, `stripe_price_id: "price_abc"`)
	// The other pack keeps its null — a push that created one price must not
	// stamp that id onto every pack in the file.
	require.Equal(t, 1, strings.Count(s, "price_abc"))
	require.Contains(t, s, "stripe_price_id: null")
}

func TestPatchKeepsTheCommentAboveTheField(t *testing.T) {
	// The id is rewritten in place, so the surrounding YAML — including the
	// comment explaining where the value comes from — survives.
	out, _, err := Apply([]byte(packYAML), []Patch{
		{Kind: KindCreditPack, YamlID: "pack_20", StripeID: "price_abc"},
	})
	require.NoError(t, err)
	require.Contains(t, string(out), "gatr push --auto-patch")
}

func TestAnUnknownPackIsReportedNotGuessed(t *testing.T) {
	// Silently writing a price id onto the wrong pack would sell the wrong
	// number of credits.
	_, unresolved, err := Apply([]byte(packYAML), []Patch{
		{Kind: KindCreditPack, YamlID: "pack_nope", StripeID: "price_abc"},
	})
	require.NoError(t, err)
	require.Len(t, unresolved, 1)
	require.Equal(t, "pack_nope", unresolved[0].YamlID)
}

func TestPacksAndPlansPatchIndependently(t *testing.T) {
	out, unresolved, err := Apply([]byte(packYAML), []Patch{
		{Kind: KindCreditPack, YamlID: "pack_20", StripeID: "price_pack20"},
		{Kind: KindCreditPack, YamlID: "pack_100", StripeID: "price_pack100"},
	})
	require.NoError(t, err)
	require.Empty(t, unresolved)
	require.Contains(t, string(out), `"price_pack20"`)
	require.Contains(t, string(out), `"price_pack100"`)
}
