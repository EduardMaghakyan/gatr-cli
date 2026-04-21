package stripe

import (
	"fmt"
	"strconv"

	"github.com/EduardMaghakyan/gatr-cli/pkg/schema"
)

// DesiredState is the full Specs set a `gatr push` would send to
// Stripe: one per gatr.yaml plan (product), one per plan×interval
// (price), plus one meter + one metered price per metered_price entry.
//
// Produced by TranslateConfig; consumed by the diff engine.
type DesiredState struct {
	Products []ProductSpec
	Prices   []PriceSpec
	Meters   []MeterSpec

	// MeterYamlForPriceYaml lets the diff engine resolve a metered
	// price's `recurring.meter` field to the yaml meter id after the
	// meter has been created. Keyed by price yaml_id.
	MeterYamlForPriceYaml map[string]string

	// ProductYamlForPriceYaml same idea — maps a price's yaml_id back
	// to the product yaml_id whose Stripe product ID it must reference.
	ProductYamlForPriceYaml map[string]string
}

// Yaml id suffixes for generated price entries. Keeping them as named
// constants (rather than "_monthly" inline strings) means a future
// period addition (`quarterly`, etc.) is a one-line change here.
const (
	PriceYamlSuffixMonthly = "_monthly"
	PriceYamlSuffixAnnual  = "_annual"
	PriceYamlSuffixMetered = "_metered"
)

// TranslateConfig turns a validated gatr.yaml into the Specs pkg/stripe
// consumes. The mapping is:
//
//   - Each Plan → one ProductSpec (yaml_id = plan.id).
//   - Each Plan.Billing.Monthly → one recurring licensed PriceSpec
//     (yaml_id = plan.id + "_monthly"). Same for Annual.
//   - Each MeteredPrice → one MeterSpec (yaml_id = metered_price.id)
//     AND one recurring metered PriceSpec (yaml_id = metered_price.id
//     + "_metered"). The price references the meter by yaml_id via
//     DesiredState.MeterYamlForPriceYaml; the diff/apply engine resolves
//     the Stripe meter ID after the meter is created.
//
// Plans without a `billing` block produce a product and zero prices —
// they're entitlements-only (the Free tier pattern).
func TranslateConfig(cfg *schema.Config) (DesiredState, error) {
	ds := DesiredState{
		MeterYamlForPriceYaml:   map[string]string{},
		ProductYamlForPriceYaml: map[string]string{},
	}

	for _, plan := range cfg.Plans {
		ds.Products = append(ds.Products, ProductSpec{
			YamlID:      plan.ID,
			Name:        plan.Name,
			Description: plan.PriceDisplay,
			Active:      true,
		})

		if plan.Billing == nil {
			continue
		}
		if plan.Billing.Monthly != nil {
			priceYaml := plan.ID + PriceYamlSuffixMonthly
			ds.Prices = append(ds.Prices, planPriceSpec(plan.ID, priceYaml, "month", plan.Billing.Monthly))
			ds.ProductYamlForPriceYaml[priceYaml] = plan.ID
		}
		if plan.Billing.Annual != nil {
			priceYaml := plan.ID + PriceYamlSuffixAnnual
			ds.Prices = append(ds.Prices, planPriceSpec(plan.ID, priceYaml, "year", plan.Billing.Annual))
			ds.ProductYamlForPriceYaml[priceYaml] = plan.ID
		}
	}

	for _, mp := range cfg.MeteredPrices {
		ds.Meters = append(ds.Meters, MeterSpec{
			YamlID:      mp.ID,
			DisplayName: mp.Name,
			Aggregation: mp.Aggregation,
		})
		meteredPrice, err := meteredPriceSpec(mp)
		if err != nil {
			return DesiredState{}, err
		}
		ds.Prices = append(ds.Prices, meteredPrice)
		ds.MeterYamlForPriceYaml[meteredPrice.YamlID] = mp.ID
		// Metered prices need a product to attach to; Stripe enforces
		// this. We emit a synthetic product per metered_price (yaml_id
		// = metered_price.id) so the surface is self-contained: no
		// manual "pick a product" step during push.
		ds.Products = append(ds.Products, ProductSpec{
			YamlID:      mp.ID,
			Name:        mp.Name,
			Description: fmt.Sprintf("Metered: %s", mp.Unit),
			Active:      true,
		})
		ds.ProductYamlForPriceYaml[meteredPrice.YamlID] = mp.ID
	}

	return ds, nil
}

func planPriceSpec(planYamlID, priceYamlID, interval string, interval_cfg *schema.BillingInterval) PriceSpec {
	return PriceSpec{
		YamlID:     priceYamlID,
		UnitAmount: int64(interval_cfg.AmountCents),
		Currency:   interval_cfg.Currency,
		Active:     true,
		Recurring: &RecurringInfo{
			Interval:  interval,
			UsageType: "licensed",
		},
	}
}

// meteredPriceSpec produces the PriceSpec for a Stripe metered Price
// (recurring + usage_type=metered). unit_price is serialised as a
// whole-cents integer where possible, falling back to the
// unit_amount_decimal fractional-cent form when the yaml value can't
// be expressed in whole cents without loss.
func meteredPriceSpec(mp schema.MeteredPrice) (PriceSpec, error) {
	// Dollars → cents. unit_price is a float to allow sub-cent (e.g.
	// 0.001 USD per call = 0.1 cents). For non-whole-cent values we
	// leave UnitAmount = 0 and stash the fractional-cent string in
	// metadata — the upsert layer will detect this and use
	// PriceParams.UnitAmountDecimal instead of UnitAmount.
	cents := mp.UnitPrice * 100
	whole := int64(cents)
	if float64(whole) != cents {
		// Sub-cent pricing → use the decimal form. We carry it in a
		// dedicated metadata key so the upsert layer doesn't have to
		// re-derive the float; downstream readers ignore non-string
		// keys.
		return PriceSpec{
			YamlID:     mp.ID + PriceYamlSuffixMetered,
			UnitAmount: 0,
			Currency:   mp.Currency,
			Active:     true,
			Recurring: &RecurringInfo{
				Interval:  mp.Period,
				UsageType: "metered",
			},
			Metadata: map[string]string{
				metaKeyUnitAmountDecimal: strconv.FormatFloat(cents, 'f', -1, 64),
			},
		}, nil
	}
	return PriceSpec{
		YamlID:     mp.ID + PriceYamlSuffixMetered,
		UnitAmount: whole,
		Currency:   mp.Currency,
		Active:     true,
		Recurring: &RecurringInfo{
			Interval:  mp.Period,
			UsageType: "metered",
		},
	}, nil
}

// metaKeyUnitAmountDecimal is an INTERNAL metadata key used to shuttle
// sub-cent prices from the translate layer to the upsert layer. Users
// MUST NOT set this key (validateSpecMetadata doesn't list it as
// reserved, but the key is unused outside this round-trip; the only
// cost of a collision is a confusing diff).
const metaKeyUnitAmountDecimal = "_gatr_unit_amount_decimal"
