package stripe

// CurrentState is the projected Stripe-side snapshot `gatr push`
// consumes as the left-hand side of the diff. Built from the three
// ListManaged* methods; pure data — no methods.
type CurrentState struct {
	Products []ManagedProduct
	Prices   []ManagedPrice
	Meters   []ManagedMeter
}

// Resource categorises a DiffOp. Kept as a string enum rather than
// three parallel slices so the render layer can iterate one list.
type Resource string

const (
	ResourceProduct Resource = "product"
	ResourcePrice   Resource = "price"
	ResourceMeter   Resource = "meter"
)

// DiffOp is one row of the diff table. Fields populated conditionally:
//
//   - Create:   YamlID + Spec fields; StripeID empty.
//   - NoOp:     YamlID + StripeID.
//   - Update:   YamlID + StripeID + Spec + Changes.
//   - Archive:  YamlID + StripeID (Spec nil).
//   - Replace:  YamlID + StripeID (old) + Spec (new) + Changes.
//
// Only one of ProductSpec / PriceSpec / MeterSpec is non-nil per op —
// the matching one for Resource. Modelled as pointers so a nil
// pointer is an easy invariant check.
type DiffOp struct {
	Resource    Resource
	Action      ApplyAction
	YamlID      string
	StripeID    string
	Changes     []string
	ProductSpec *ProductSpec
	PriceSpec   *PriceSpec
	MeterSpec   *MeterSpec
}

// DiffPlan groups ops by resource so the renderer can print
// products-then-prices-then-meters in topological order — the same
// order the apply engine will execute them.
type DiffPlan struct {
	ProductOps []DiffOp
	PriceOps   []DiffOp
	MeterOps   []DiffOp
}

// HasChanges reports whether any op in the plan requires an API call.
// Pure NoOp plans let the CLI exit with a friendly "no changes" line
// and skip the apply confirmation prompt.
func (d DiffPlan) HasChanges() bool {
	for _, op := range d.ProductOps {
		if op.Action != ActionNoOp {
			return true
		}
	}
	for _, op := range d.PriceOps {
		if op.Action != ActionNoOp {
			return true
		}
	}
	for _, op := range d.MeterOps {
		if op.Action != ActionNoOp {
			return true
		}
	}
	return false
}

// Count returns the number of ops per action across the whole plan.
// Used by the renderer's summary line ("3 to create, 1 to archive").
func (d DiffPlan) Count(action ApplyAction) int {
	n := 0
	for _, ops := range [][]DiffOp{d.ProductOps, d.PriceOps, d.MeterOps} {
		for _, op := range ops {
			if op.Action == action {
				n++
			}
		}
	}
	return n
}

// ComputeDiff is the pure function at the heart of `gatr push`. It
// produces a fully-resolved DiffPlan from the desired state
// (TranslateConfig output) + the current Stripe snapshot.
//
// Matching is by yaml_id:
//   - desired ∧ current → NoOp | Update | Replace (per-resource rules)
//   - desired ∧ ¬current → Create
//   - ¬desired ∧ current → Archive
//
// For prices, a hard-field change (amount/currency/recurring) produces
// a Replace op (archive old + create new). Soft-only changes produce
// an Update. Meters distinguish analogously (event_name/aggregation
// are immutable).
//
// The caller MUST have resolved ProductStripeID on every desired price
// BEFORE calling ComputeDiff (via ResolveProductRefs after the product
// list is known). A PriceSpec with empty ProductStripeID forces a
// Create op even if a match exists, to avoid silently retargeting a
// price to the wrong product.
func ComputeDiff(desired DesiredState, current CurrentState) DiffPlan {
	var plan DiffPlan

	// -- Products ----------------------------------------------------
	curProductByYaml := map[string]ManagedProduct{}
	for _, p := range current.Products {
		curProductByYaml[p.YamlID] = p
	}
	seenProducts := map[string]bool{}
	for _, spec := range desired.Products {
		seenProducts[spec.YamlID] = true
		cur, exists := curProductByYaml[spec.YamlID]
		if !exists {
			s := spec
			plan.ProductOps = append(plan.ProductOps, DiffOp{
				Resource: ResourceProduct, Action: ActionCreated,
				YamlID: spec.YamlID, ProductSpec: &s,
			})
			continue
		}
		diffs := productDiffFields(spec, cur)
		if len(diffs) == 0 {
			plan.ProductOps = append(plan.ProductOps, DiffOp{
				Resource: ResourceProduct, Action: ActionNoOp,
				YamlID: spec.YamlID, StripeID: cur.StripeID,
			})
			continue
		}
		s := spec
		plan.ProductOps = append(plan.ProductOps, DiffOp{
			Resource: ResourceProduct, Action: ActionUpdated,
			YamlID: spec.YamlID, StripeID: cur.StripeID,
			Changes: diffs, ProductSpec: &s,
		})
	}
	for _, cur := range current.Products {
		if seenProducts[cur.YamlID] {
			continue
		}
		// In Stripe but not in yaml → archive. Skip if already archived
		// (active=false) to keep re-runs idempotent.
		if !cur.Active {
			continue
		}
		plan.ProductOps = append(plan.ProductOps, DiffOp{
			Resource: ResourceProduct, Action: ActionArchived,
			YamlID: cur.YamlID, StripeID: cur.StripeID,
		})
	}

	// -- Prices ------------------------------------------------------
	curPriceByYaml := map[string]ManagedPrice{}
	for _, p := range current.Prices {
		curPriceByYaml[p.YamlID] = p
	}
	seenPrices := map[string]bool{}
	for _, spec := range desired.Prices {
		seenPrices[spec.YamlID] = true
		cur, exists := curPriceByYaml[spec.YamlID]
		if !exists {
			s := spec
			plan.PriceOps = append(plan.PriceOps, DiffOp{
				Resource: ResourcePrice, Action: ActionCreated,
				YamlID: spec.YamlID, PriceSpec: &s,
			})
			continue
		}
		hard := priceHardDiffFields(spec, cur)
		if len(hard) > 0 {
			s := spec
			plan.PriceOps = append(plan.PriceOps, DiffOp{
				Resource: ResourcePrice, Action: ActionReplaced,
				YamlID: spec.YamlID, StripeID: cur.StripeID,
				Changes: hard, PriceSpec: &s,
			})
			continue
		}
		soft := priceSoftDiffFields(spec, cur)
		if len(soft) == 0 {
			plan.PriceOps = append(plan.PriceOps, DiffOp{
				Resource: ResourcePrice, Action: ActionNoOp,
				YamlID: spec.YamlID, StripeID: cur.StripeID,
			})
			continue
		}
		s := spec
		plan.PriceOps = append(plan.PriceOps, DiffOp{
			Resource: ResourcePrice, Action: ActionUpdated,
			YamlID: spec.YamlID, StripeID: cur.StripeID,
			Changes: soft, PriceSpec: &s,
		})
	}
	for _, cur := range current.Prices {
		if seenPrices[cur.YamlID] || !cur.Active {
			continue
		}
		plan.PriceOps = append(plan.PriceOps, DiffOp{
			Resource: ResourcePrice, Action: ActionArchived,
			YamlID: cur.YamlID, StripeID: cur.StripeID,
		})
	}

	// -- Meters ------------------------------------------------------
	curMeterByYaml := map[string]ManagedMeter{}
	for _, m := range current.Meters {
		curMeterByYaml[m.YamlID] = m
	}
	seenMeters := map[string]bool{}
	for _, spec := range desired.Meters {
		seenMeters[spec.YamlID] = true
		cur, exists := curMeterByYaml[spec.YamlID]
		if !exists {
			s := spec
			plan.MeterOps = append(plan.MeterOps, DiffOp{
				Resource: ResourceMeter, Action: ActionCreated,
				YamlID: spec.YamlID, MeterSpec: &s,
			})
			continue
		}
		// Aggregation + event_name immutable → drift = replace.
		// Event_name itself is derived from (projectID, yamlID), so a
		// yamlID match implies an event_name match; we still include
		// it in the check as defence-in-depth.
		hard := meterHardDiffFields(spec, cur)
		if len(hard) > 0 {
			s := spec
			plan.MeterOps = append(plan.MeterOps, DiffOp{
				Resource: ResourceMeter, Action: ActionReplaced,
				YamlID: spec.YamlID, StripeID: cur.StripeID,
				Changes: hard, MeterSpec: &s,
			})
			continue
		}
		soft := meterSoftDiffFields(spec, cur)
		if len(soft) == 0 {
			plan.MeterOps = append(plan.MeterOps, DiffOp{
				Resource: ResourceMeter, Action: ActionNoOp,
				YamlID: spec.YamlID, StripeID: cur.StripeID,
			})
			continue
		}
		s := spec
		plan.MeterOps = append(plan.MeterOps, DiffOp{
			Resource: ResourceMeter, Action: ActionUpdated,
			YamlID: spec.YamlID, StripeID: cur.StripeID,
			Changes: soft, MeterSpec: &s,
		})
	}
	for _, cur := range current.Meters {
		if seenMeters[cur.YamlID] || cur.Status == "inactive" {
			continue
		}
		plan.MeterOps = append(plan.MeterOps, DiffOp{
			Resource: ResourceMeter, Action: ActionArchived,
			YamlID: cur.YamlID, StripeID: cur.StripeID,
		})
	}

	return plan
}

// productDiffFields returns a human-readable list of field names that
// differ between spec and current. Empty slice = no change.
func productDiffFields(spec ProductSpec, cur ManagedProduct) []string {
	var out []string
	if spec.Name != cur.Name {
		out = append(out, "name")
	}
	if spec.Description != cur.Description {
		out = append(out, "description")
	}
	if spec.Active != cur.Active {
		out = append(out, "active")
	}
	return out
}

// priceHardDiffFields returns field names that are IMMUTABLE on
// Stripe and differ between spec and current → trigger Replace, not
// Update.
func priceHardDiffFields(spec PriceSpec, cur ManagedPrice) []string {
	var out []string
	if spec.UnitAmount != cur.UnitAmount {
		out = append(out, "amount")
	}
	if spec.Currency != cur.Currency {
		out = append(out, "currency")
	}
	if spec.ProductStripeID != "" && spec.ProductStripeID != cur.ProductStripeID {
		out = append(out, "product")
	}
	if (spec.Recurring == nil) != (cur.Recurring == nil) {
		out = append(out, "recurring")
		return out
	}
	if spec.Recurring != nil {
		if spec.Recurring.Interval != cur.Recurring.Interval {
			out = append(out, "interval")
		}
		specCount := spec.Recurring.IntervalCount
		if specCount == 0 {
			specCount = 1
		}
		curCount := cur.Recurring.IntervalCount
		if curCount == 0 {
			curCount = 1
		}
		if specCount != curCount {
			out = append(out, "interval_count")
		}
		if spec.Recurring.UsageType != cur.Recurring.UsageType {
			out = append(out, "usage_type")
		}
		if spec.Recurring.MeterID != "" && spec.Recurring.MeterID != cur.Recurring.MeterID {
			out = append(out, "meter")
		}
	}
	return out
}

// priceSoftDiffFields returns patchable fields that differ (active).
// Metadata equivalence is NOT checked — translate.go doesn't inject
// user metadata, so the only metadata differences come from the
// gatr_managed/gatr_id stamps, which are stable.
func priceSoftDiffFields(spec PriceSpec, cur ManagedPrice) []string {
	var out []string
	if spec.Active != cur.Active {
		out = append(out, "active")
	}
	return out
}

func meterHardDiffFields(spec MeterSpec, cur ManagedMeter) []string {
	var out []string
	if spec.Aggregation != "" && spec.Aggregation != cur.Aggregation {
		out = append(out, "aggregation")
	}
	return out
}

func meterSoftDiffFields(spec MeterSpec, cur ManagedMeter) []string {
	var out []string
	if spec.DisplayName != cur.DisplayName {
		out = append(out, "display_name")
	}
	return out
}
