package cli

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/require"

	gstripe "github.com/EduardMaghakyan/gatr-cli/pkg/stripe"
)

// RenderDiffPlan's lipgloss output is non-deterministic at byte level
// (ANSI escape sequences vary by TERM). The assertions here target
// substrings that survive colour stripping: the header, the action
// labels ("create"), yaml IDs, and the action counts.

func TestRenderDiffPlan_EmptyPlan_SaysNoChanges(t *testing.T) {
	var buf bytes.Buffer
	RenderDiffPlan(&buf, gstripe.DiffPlan{}, "550e8400-e29b-41d4-a716-446655440000")
	out := buf.String()
	require.Contains(t, out, "gatr push")
	require.Contains(t, out, "550e8400")
	require.Contains(t, out, "no changes")
}

func TestRenderDiffPlan_AllCreates_ShowsCountAndYamlIDs(t *testing.T) {
	spec := gstripe.ProductSpec{YamlID: "pro", Name: "Pro", Active: true}
	plan := gstripe.DiffPlan{
		ProductOps: []gstripe.DiffOp{
			{Resource: gstripe.ResourceProduct, Action: gstripe.ActionCreated, YamlID: "free", ProductSpec: &gstripe.ProductSpec{YamlID: "free"}},
			{Resource: gstripe.ResourceProduct, Action: gstripe.ActionCreated, YamlID: "pro", ProductSpec: &spec},
		},
	}
	var buf bytes.Buffer
	RenderDiffPlan(&buf, plan, "test-project")
	out := buf.String()
	require.Contains(t, out, "Products")
	require.Contains(t, out, "create")
	require.Contains(t, out, "free")
	require.Contains(t, out, "pro")
	require.Contains(t, out, "2 to create")
}

func TestRenderDiffPlan_MixedActions_LabelsCorrect(t *testing.T) {
	// One of each action — ensures every branch of the renderer
	// produces output.
	plan := gstripe.DiffPlan{
		ProductOps: []gstripe.DiffOp{
			{Resource: gstripe.ResourceProduct, Action: gstripe.ActionCreated, YamlID: "create_me"},
			{Resource: gstripe.ResourceProduct, Action: gstripe.ActionUpdated, YamlID: "rename_me", StripeID: "prod_1", Changes: []string{"name"}},
			{Resource: gstripe.ResourceProduct, Action: gstripe.ActionArchived, YamlID: "retire_me", StripeID: "prod_2"},
			{Resource: gstripe.ResourceProduct, Action: gstripe.ActionNoOp, YamlID: "still_fine", StripeID: "prod_3"},
		},
		PriceOps: []gstripe.DiffOp{
			{Resource: gstripe.ResourcePrice, Action: gstripe.ActionReplaced, YamlID: "bump_me", StripeID: "price_1", Changes: []string{"amount"}},
		},
	}
	var buf bytes.Buffer
	RenderDiffPlan(&buf, plan, "demo")
	out := buf.String()

	require.Contains(t, out, "create")
	require.Contains(t, out, "update")
	require.Contains(t, out, "replace")
	require.Contains(t, out, "archive")
	// No-op rows are suppressed when there are other actions.
	require.NotContains(t, out, "still_fine")
	require.Contains(t, out, "rename_me")
	require.Contains(t, out, "amount", "replace op should surface the changed field")
	require.Contains(t, out, "1 no-op", "summary counts no-ops even when rows are suppressed")
}
