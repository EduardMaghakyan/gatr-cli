package stripe

import (
	"fmt"
	"regexp"
	"strings"

	stripesdk "github.com/stripe/stripe-go/v82"

	"github.com/EduardMaghakyan/gatr-cli/pkg/schema"
)

// ImportResult is the output of TranslateFromStripe: a partial
// schema.Config plus per-item notes the CLI renders as yaml comments.
//
// The Config is "partial" because Stripe has no concept of features /
// limits / credits — those blocks come back empty; the operator fills
// them in after `gatr import`. Everything that CAN be derived from
// Stripe (plans, billing, metered_prices, stripe_* IDs) IS populated.
type ImportResult struct {
	Config schema.Config
	Notes  []ImportNote
}

// ImportNote is a single annotation surfaced to the user via yaml
// comments. Kept as structured data (not raw strings) so the renderer
// can group notes by Section and the tests can assert on shape without
// doing string matching.
type ImportNote struct {
	Kind    ImportNoteKind
	Section string // "plans" | "metered_prices" | ""
	Subject string // the Stripe ID + a short human handle
	Reason  string
}

type ImportNoteKind string

const (
	NoteSkipped      ImportNoteKind = "skipped"
	NotePerSeatHint  ImportNoteKind = "per_seat_hint"
	NoteIDCollision  ImportNoteKind = "id_collision"
	NoteMeterOrphan  ImportNoteKind = "meter_orphan"
	NoteArchivedSkip ImportNoteKind = "archived_skipped"
)

// TranslateFromStripe produces a starter gatr.yaml shape from a Stripe
// account snapshot. Pure function — the caller handles the ListAll*
// round-trip so tests can feed synthetic fixtures directly.
//
// Behaviour contract:
//   - Archived objects (Active=false) are skipped and noted.
//   - Non-recurring (one-time) prices are skipped.
//   - Tiered prices are skipped (gatr's unit_price is a flat rate).
//   - Recurring prices with intervals outside {month, year} and IntervalCount≠1 are skipped.
//   - Products with no surviving prices become entitlement-only plans.
//   - Per-seat is NOT auto-inferred; a hint note is emitted when a
//     licensed price has a `per_seat=true` metadata key on its product.
//   - ID collisions (two products that kebabify to the same slug) get
//     a `-2`, `-3`, … suffix.
func TranslateFromStripe(
	projectSlug string,
	products []*stripesdk.Product,
	prices []*stripesdk.Price,
	meters []*stripesdk.BillingMeter,
) ImportResult {
	result := ImportResult{
		Config: schema.Config{
			Version:       schema.SupportedVersion,
			Project:       projectSlug,
			Features:      []schema.Feature{},
			Limits:        []schema.Limit{},
			Credits:       []schema.Credit{},
			Operations:    []schema.Operation{},
			MeteredPrices: []schema.MeteredPrice{},
			Plans:         []schema.Plan{},
		},
	}

	// Group prices by product ID. Metered prices go into a side list:
	// they attach to meters, not plans.
	pricesByProduct := map[string][]*stripesdk.Price{}
	var meteredPrices []*stripesdk.Price
	for _, p := range prices {
		if !p.Active {
			result.Notes = append(result.Notes, ImportNote{
				Kind: NoteArchivedSkip, Section: "", Subject: p.ID,
				Reason: "archived price — not imported",
			})
			continue
		}
		if p.Recurring == nil {
			result.Notes = append(result.Notes, ImportNote{
				Kind: NoteSkipped, Section: "", Subject: p.ID,
				Reason: "one-time price — gatr only supports recurring",
			})
			continue
		}
		if p.BillingScheme == stripesdk.PriceBillingSchemeTiered || len(p.Tiers) > 0 {
			result.Notes = append(result.Notes, ImportNote{
				Kind: NoteSkipped, Section: "", Subject: p.ID,
				Reason: "tiered price — gatr's unit_price is a flat rate",
			})
			continue
		}
		if string(p.Recurring.UsageType) == "metered" {
			meteredPrices = append(meteredPrices, p)
			continue
		}
		// Licensed recurring price → pin to its product.
		if p.Product == nil || p.Product.ID == "" {
			result.Notes = append(result.Notes, ImportNote{
				Kind: NoteSkipped, Section: "", Subject: p.ID,
				Reason: "price has no product — Stripe orphan, cannot import",
			})
			continue
		}
		pricesByProduct[p.Product.ID] = append(pricesByProduct[p.Product.ID], p)
	}

	// Plans from products.
	usedSlugs := map[string]int{}
	for _, prod := range products {
		if !prod.Active {
			result.Notes = append(result.Notes, ImportNote{
				Kind: NoteArchivedSkip, Section: "plans", Subject: prod.ID,
				Reason: "archived product — not imported",
			})
			continue
		}
		slug := uniqueSlug(kebab(prod.Name, prod.ID), usedSlugs)
		if slug != kebab(prod.Name, prod.ID) {
			result.Notes = append(result.Notes, ImportNote{
				Kind: NoteIDCollision, Section: "plans", Subject: prod.ID,
				Reason: fmt.Sprintf("slug %q collided; used %q instead", kebab(prod.Name, prod.ID), slug),
			})
		}

		plan := schema.Plan{
			ID:       slug,
			Name:     prod.Name,
			Features: []string{},
			Limits:   map[string]schema.NumberOrUnlimited{},
			Grants:   map[string]schema.NumberOrUnlimited{},
			Includes: map[string]schema.NumberOrUnlimited{},
		}

		var billing schema.Billing
		var haveBilling bool
		for _, pr := range pricesByProduct[prod.ID] {
			if pr.Recurring.IntervalCount != 1 {
				result.Notes = append(result.Notes, ImportNote{
					Kind: NoteSkipped, Section: "plans", Subject: pr.ID,
					Reason: fmt.Sprintf("interval_count=%d — gatr supports 1/month, 1/year", pr.Recurring.IntervalCount),
				})
				continue
			}
			interval := schema.BillingInterval{
				AmountCents:   int(pr.UnitAmount),
				Currency:      string(pr.Currency),
				StripePriceID: stringPtr(pr.ID),
			}
			switch string(pr.Recurring.Interval) {
			case "month":
				if billing.Monthly != nil {
					result.Notes = append(result.Notes, ImportNote{
						Kind: NoteSkipped, Section: "plans", Subject: pr.ID,
						Reason: "duplicate monthly price on product — kept the first",
					})
					continue
				}
				billing.Monthly = &interval
				haveBilling = true
			case "year":
				if billing.Annual != nil {
					result.Notes = append(result.Notes, ImportNote{
						Kind: NoteSkipped, Section: "plans", Subject: pr.ID,
						Reason: "duplicate annual price on product — kept the first",
					})
					continue
				}
				billing.Annual = &interval
				haveBilling = true
			default:
				result.Notes = append(result.Notes, ImportNote{
					Kind: NoteSkipped, Section: "plans", Subject: pr.ID,
					Reason: fmt.Sprintf("interval=%s — gatr supports month/year", pr.Recurring.Interval),
				})
			}
		}
		if haveBilling {
			plan.Billing = &billing
		}

		// Per-seat hint: if any of the plan's prices is licensed AND
		// the product metadata marks per_seat=true, emit a note. We
		// don't touch limits — that's the user's call.
		if looksPerSeat(prod, pricesByProduct[prod.ID]) {
			result.Notes = append(result.Notes, ImportNote{
				Kind: NotePerSeatHint, Section: "plans", Subject: prod.ID,
				Reason: "product metadata hints at per-seat; consider adding a limit with per_seat_pricing: true",
			})
		}

		result.Config.Plans = append(result.Config.Plans, plan)
	}

	// Metered prices: pair each meter with its matching metered Price
	// (by recurring.meter = meter.id). Meters without a price become
	// "orphan" entries with unit_price=0.
	priceByMeterID := map[string]*stripesdk.Price{}
	for _, pr := range meteredPrices {
		if pr.Recurring != nil && pr.Recurring.Meter != "" {
			priceByMeterID[pr.Recurring.Meter] = pr
		}
	}
	for _, m := range meters {
		if string(m.Status) != "active" {
			result.Notes = append(result.Notes, ImportNote{
				Kind: NoteArchivedSkip, Section: "metered_prices", Subject: m.ID,
				Reason: fmt.Sprintf("meter status=%s — not imported", m.Status),
			})
			continue
		}
		mpID := uniqueSlug(kebab(m.DisplayName, m.ID), usedSlugs)
		entry := schema.MeteredPrice{
			ID:            mpID,
			Name:          m.DisplayName,
			Unit:          "",
			UnitPrice:     0,
			Period:        "month",
			Currency:      "usd",
			Aggregation:   "sum",
			StripeMeterID: stringPtr(m.ID),
		}
		if m.DefaultAggregation != nil && m.DefaultAggregation.Formula != "" {
			entry.Aggregation = string(m.DefaultAggregation.Formula)
		}

		pr, ok := priceByMeterID[m.ID]
		if ok {
			entry.Currency = string(pr.Currency)
			if pr.Recurring != nil && pr.Recurring.Interval != "" {
				entry.Period = string(pr.Recurring.Interval)
			}
			entry.UnitPrice = resolveUnitPrice(pr)
		} else {
			result.Notes = append(result.Notes, ImportNote{
				Kind: NoteMeterOrphan, Section: "metered_prices", Subject: m.ID,
				Reason: "meter has no matching metered Price — unit_price set to 0; fill in manually",
			})
		}

		result.Config.MeteredPrices = append(result.Config.MeteredPrices, entry)
	}

	return result
}

// resolveUnitPrice converts a Stripe Price's unit_amount (whole cents)
// or unit_amount_decimal (fractional cents) into gatr's float dollars.
// Prefers UnitAmountDecimal when it's non-zero and conflicts with the
// whole-cents value (the Stripe SDK populates both).
func resolveUnitPrice(p *stripesdk.Price) float64 {
	// Stripe returns unit_amount_decimal as a string parsed into a
	// float by the SDK. If it equals the int UnitAmount, we're in the
	// whole-cents case. Otherwise it's sub-cent, take the decimal.
	if p.UnitAmountDecimal != 0 && float64(p.UnitAmount) != p.UnitAmountDecimal {
		return p.UnitAmountDecimal / 100.0
	}
	return float64(p.UnitAmount) / 100.0
}

// kebab renders "Pro Plus" → "pro-plus". Falls back to fallback when
// the input is empty OR contains no [a-zA-Z0-9] characters. gatr yaml
// ids don't have a formal regex in the schema, but match CLI conventions:
// lower-case letters, digits, hyphens.
var kebabInvalidRE = regexp.MustCompile(`[^a-z0-9]+`)

func kebab(s, fallback string) string {
	lower := strings.ToLower(strings.TrimSpace(s))
	out := kebabInvalidRE.ReplaceAllString(lower, "-")
	out = strings.Trim(out, "-")
	if out == "" {
		return fallback
	}
	return out
}

// uniqueSlug suffixes `-2`, `-3`, ... to avoid collisions. Registers
// the chosen slug in used before returning.
func uniqueSlug(base string, used map[string]int) string {
	if _, taken := used[base]; !taken {
		used[base] = 1
		return base
	}
	for i := 2; ; i++ {
		cand := fmt.Sprintf("%s-%d", base, i)
		if _, taken := used[cand]; !taken {
			used[cand] = 1
			return cand
		}
	}
}

// looksPerSeat is a heuristic — returns true when the product metadata
// marks the product as per-seat (either key "per_seat" = "true" or
// "pricing_model" = "per_seat"). We do NOT look at price-side hints
// because Stripe doesn't carry a stable per-seat signal on prices.
func looksPerSeat(prod *stripesdk.Product, prices []*stripesdk.Price) bool {
	if prod.Metadata == nil {
		return false
	}
	if strings.EqualFold(prod.Metadata["per_seat"], "true") {
		return true
	}
	if strings.EqualFold(prod.Metadata["pricing_model"], "per_seat") {
		return true
	}
	return false
}

func stringPtr(s string) *string { return &s }
