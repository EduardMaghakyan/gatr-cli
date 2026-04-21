package cli

import (
	"fmt"
	"io"
	"strings"

	"github.com/charmbracelet/lipgloss"

	gstripe "github.com/EduardMaghakyan/gatr-cli/pkg/stripe"
)

// Diff rendering colours. Kept separate from the general CLI palette
// in style.go because `gatr push` has action-specific semantics
// (create = green-add, archive = red-strike, etc.) that don't belong
// in the shared palette.
var (
	diffCreateStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("#3DD68C")).Bold(true) // green
	diffUpdateStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("#FFB454")).Bold(true) // amber
	diffReplaceStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#FF6B6B")).Bold(true) // red (destructive)
	diffArchiveStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#FF6B6B"))            // red
	diffNoopStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("241"))                // grey
)

// actionInfo is the single source of truth for how each ApplyAction
// renders: glyph (used in per-row prefixes), label (used in row text +
// summary counts), and style (colour). Adding a new ApplyAction is a
// one-line entry here; previously the same enum was switched-on in
// THREE places (actionGlyph, actionLabel, renderSummary's struct).
type actionInfo struct {
	glyph string
	label string
	style lipgloss.Style
}

var actionInfos = map[gstripe.ApplyAction]actionInfo{
	gstripe.ActionCreated:  {"+", "create", diffCreateStyle},
	gstripe.ActionUpdated:  {"~", "update", diffUpdateStyle},
	gstripe.ActionReplaced: {"↻", "replace", diffReplaceStyle},
	gstripe.ActionArchived: {"-", "archive", diffArchiveStyle},
	gstripe.ActionNoOp:     {"=", "no-op", diffNoopStyle},
}

// summaryOrder is the explicit display order for the trailing summary
// line ("3 to create, 1 to update, ..."). Map iteration is random in
// Go, so the order can't come from actionInfos. NoOp is handled
// separately below — it's always the last column when present.
var summaryOrder = []gstripe.ApplyAction{
	gstripe.ActionCreated,
	gstripe.ActionUpdated,
	gstripe.ActionReplaced,
	gstripe.ActionArchived,
}

// RenderDiffPlan writes a human-readable summary of the plan to w.
// Format: one line per op, grouped by resource (products → prices →
// meters). Trailing summary line counts each action.
//
// Noop rows are rendered only if there are no other rows — on a
// non-trivial plan they're noise. The full "=" list is reachable via
// --verbose (not implemented in T4).
func RenderDiffPlan(w io.Writer, plan gstripe.DiffPlan, projectID string) {
	fmt.Fprintln(w, titleStyle.Render(fmt.Sprintf("gatr push — planned changes for project %s", projectID)))
	fmt.Fprintln(w)

	if !plan.HasChanges() {
		fmt.Fprintln(w, subtleStyle.Render("  (no changes — Stripe is already in sync with gatr.yaml)"))
		return
	}

	renderSection(w, "Products", plan.ProductOps)
	renderSection(w, "Prices", plan.PriceOps)
	renderSection(w, "Meters", plan.MeterOps)

	fmt.Fprintln(w)
	fmt.Fprintln(w, renderSummary(plan))
}

func renderSection(w io.Writer, heading string, ops []gstripe.DiffOp) {
	// Count actionable rows so we don't print an empty header for a
	// section where everything no-op'd.
	actionable := 0
	for _, op := range ops {
		if op.Action != gstripe.ActionNoOp {
			actionable++
		}
	}
	if actionable == 0 {
		return
	}
	fmt.Fprintln(w, titleStyle.Render("  "+heading))
	for _, op := range ops {
		if op.Action == gstripe.ActionNoOp {
			continue
		}
		fmt.Fprintln(w, "    "+formatOp(op))
	}
}

func formatOp(op gstripe.DiffOp) string {
	info := actionInfos[op.Action]
	parts := []string{
		info.style.Render(info.glyph),
		info.style.Render(info.label),
		codeStyle.Render(op.YamlID),
	}
	if op.StripeID != "" {
		parts = append(parts, subtleStyle.Render("("+op.StripeID+")"))
	}
	if len(op.Changes) > 0 {
		parts = append(parts, subtleStyle.Render("— "+strings.Join(op.Changes, ", ")))
	}
	return strings.Join(parts, " ")
}

func renderSummary(plan gstripe.DiffPlan) string {
	var parts []string
	for _, action := range summaryOrder {
		n := plan.Count(action)
		if n == 0 {
			continue
		}
		info := actionInfos[action]
		parts = append(parts, info.style.Render(fmt.Sprintf("%d to %s", n, info.label)))
	}
	if noops := plan.Count(gstripe.ActionNoOp); noops > 0 {
		info := actionInfos[gstripe.ActionNoOp]
		parts = append(parts, info.style.Render(fmt.Sprintf("%d no-op", noops)))
	}
	return "  " + strings.Join(parts, subtleStyle.Render(", "))
}
