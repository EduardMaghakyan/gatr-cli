package stripe

import (
	"context"
	"errors"
	"fmt"

	stripesdk "github.com/stripe/stripe-go/v82"

	"github.com/EduardMaghakyan/gatr-cli/pkg/schema"
)

// Adoption closes the "I already have products in Stripe" loop: when a
// yaml carries a non-null `stripe_price_id` / `stripe_meter_id` that
// points at an existing, non-gatr-managed Stripe object, `gatr push`
// stamps gatr_managed=true + gatr_id onto it instead of creating a
// duplicate. After the stamp, the object behaves identically to one
// gatr created — normal list+diff+apply takes over.
//
// Meters cannot be adopted: their event_name (which gatr uses as the
// namespacing signal) is immutable at create time. ScanAdoptions
// surfaces any meter-adoption attempt as a fatal AdoptionConflict so
// the CLI can refuse cleanly.

// AdoptionCandidate is one Stripe object the user referenced via yaml
// that isn't yet gatr-managed but CAN be stamped. Callers feed these
// into executeAdoption to write the metadata.
type AdoptionCandidate struct {
	Resource Resource
	YamlID   string // the yaml_id the stamped object will claim
	StripeID string
	Name     string // human label for the preview row
	// Current is the raw Stripe object (projected to ManagedXxx shape)
	// — lets the caller synthesise a CurrentState entry pre-stamp so
	// the diff engine sees the object as already-managed.
	ProductCurrent *ManagedProduct
	PriceCurrent   *ManagedPrice
}

// AdoptionConflict is a fatal classification: the referenced Stripe
// object cannot be adopted under this project. The push pipeline must
// render these and exit non-zero before any writes.
type AdoptionConflict struct {
	Kind     AdoptionConflictKind
	Resource Resource
	YamlID   string
	StripeID string
	Message  string
}

type AdoptionConflictKind string

const (
	// AdoptionOwnedByOtherProject: the Stripe object already carries a
	// gatr_id with a different project prefix — another gatr project
	// on the same Stripe account owns it.
	AdoptionOwnedByOtherProject AdoptionConflictKind = "owned_by_other_project"

	// AdoptionMeterNotAdoptable: the yaml references a non-gatr meter
	// by ID. Stripe meter event_names are immutable at create time, so
	// there's no way to retrofit gatr's namespacing. The user must
	// set stripe_meter_id: null and let gatr create a fresh meter.
	AdoptionMeterNotAdoptable AdoptionConflictKind = "meter_not_adoptable"

	// AdoptionStripeObjectMissing: the yaml has a stripe_price_id /
	// stripe_meter_id pointing at an object Stripe doesn't recognise.
	// Usually means the yaml was edited by hand or the object was
	// deleted out-of-band. Non-fatal (we could treat it as "fresh
	// create"), but safer to surface than silently ignore.
	AdoptionStripeObjectMissing AdoptionConflictKind = "stripe_object_missing"
)

// AdoptionPlan is the full classification result. Candidates are
// applied only if Conflicts is empty — any conflict aborts the push.
type AdoptionPlan struct {
	Candidates []AdoptionCandidate
	Conflicts  []AdoptionConflict
}

// HasWork reports whether the plan contains anything worth rendering
// — either a candidate to stamp or a conflict to surface.
func (p AdoptionPlan) HasWork() bool {
	return len(p.Candidates) > 0 || len(p.Conflicts) > 0
}

// ScanAdoptions walks the yaml's stripe_* hints and classifies each
// against current Stripe state. Pure-ish — issues Stripe GETs, no
// writes. The caller is responsible for enforcing the "abort if any
// conflict" rule (ScanAdoptions itself returns both candidates AND
// conflicts so the renderer can show them together).
//
// yamlIDForPrice resolves a yaml stripe_price_id to the yaml_id the
// push pipeline will attach to it — matches the translate.go mapping:
//   - plans[X].billing.monthly.stripe_price_id  → "X_monthly"
//   - plans[X].billing.annual.stripe_price_id   → "X_annual"
//   - metered_prices[X].stripe_meter_id         → meter yaml_id "X"
//
// We keep that mapping here (duplicated from translate.go) because
// adoption runs BEFORE TranslateConfig; inverting the translate
// function would be more complex than re-emitting the suffix rules.
func (c *Client) ScanAdoptions(ctx context.Context, cfg *schema.Config) (AdoptionPlan, error) {
	if c.projectID == "" {
		return AdoptionPlan{}, ErrMissingProjectID("ScanAdoptions requires ClientOptions.ProjectID")
	}

	type priceRef struct{ stripeID, yamlID string }
	type meterRef struct{ stripeID, yamlID string }

	var priceRefs []priceRef
	var meterRefs []meterRef

	for _, p := range cfg.Plans {
		if p.Billing == nil {
			continue
		}
		if p.Billing.Monthly != nil && p.Billing.Monthly.StripePriceID != nil && *p.Billing.Monthly.StripePriceID != "" {
			priceRefs = append(priceRefs, priceRef{*p.Billing.Monthly.StripePriceID, p.ID + PriceYamlSuffixMonthly})
		}
		if p.Billing.Annual != nil && p.Billing.Annual.StripePriceID != nil && *p.Billing.Annual.StripePriceID != "" {
			priceRefs = append(priceRefs, priceRef{*p.Billing.Annual.StripePriceID, p.ID + PriceYamlSuffixAnnual})
		}
	}
	for _, mp := range cfg.MeteredPrices {
		if mp.StripeMeterID != nil && *mp.StripeMeterID != "" {
			meterRefs = append(meterRefs, meterRef{*mp.StripeMeterID, mp.ID})
		}
	}

	var plan AdoptionPlan
	seenProducts := map[string]bool{} // dedupe in case two prices share a product

	for _, pr := range priceRefs {
		price, err := c.fetchPrice(ctx, pr.stripeID)
		if err != nil {
			if isNotFound(err) {
				plan.Conflicts = append(plan.Conflicts, AdoptionConflict{
					Kind: AdoptionStripeObjectMissing, Resource: ResourcePrice,
					YamlID: pr.yamlID, StripeID: pr.stripeID,
					Message: fmt.Sprintf("price %s referenced in yaml but not found in Stripe", pr.stripeID),
				})
				continue
			}
			return AdoptionPlan{}, err
		}

		// Classify price.
		yamlIDFromMeta, owned := isGatrManaged(price.Metadata, c.projectID)
		switch {
		case owned && yamlIDFromMeta == pr.yamlID:
			// Already managed by this project at this yaml_id —
			// nothing to do.
		case owned:
			// Managed by this project under a DIFFERENT yaml_id — the
			// user renamed things in yaml without running a Replace.
			// Treat as conflict; re-adopting would silently move the
			// managed-id under their feet.
			plan.Conflicts = append(plan.Conflicts, AdoptionConflict{
				Kind: AdoptionOwnedByOtherProject, Resource: ResourcePrice,
				YamlID: pr.yamlID, StripeID: pr.stripeID,
				Message: fmt.Sprintf("price %s is managed as %q in this project; yaml wants %q", pr.stripeID, yamlIDFromMeta, pr.yamlID),
			})
		case priceOwnedByOtherProject(price.Metadata, c.projectID):
			plan.Conflicts = append(plan.Conflicts, AdoptionConflict{
				Kind: AdoptionOwnedByOtherProject, Resource: ResourcePrice,
				YamlID: pr.yamlID, StripeID: pr.stripeID,
				Message: fmt.Sprintf("price %s is managed by another gatr project (metadata.gatr_id=%q)", pr.stripeID, price.Metadata[metaKeyGatrID]),
			})
		default:
			// Unmanaged — eligible for adoption.
			current := projectPrice(price, pr.yamlID)
			plan.Candidates = append(plan.Candidates, AdoptionCandidate{
				Resource: ResourcePrice,
				YamlID:   pr.yamlID, StripeID: pr.stripeID,
				Name:         price.Nickname,
				PriceCurrent: &current,
			})

			// Adopt the parent product as well if we haven't queued it
			// already (dedupe by Stripe product ID).
			if price.Product != nil && price.Product.ID != "" && !seenProducts[price.Product.ID] {
				seenProducts[price.Product.ID] = true
				productYaml := productYamlForPriceYaml(pr.yamlID)
				if productYaml != "" {
					productCandidate, conflict := c.classifyProductForAdoption(ctx, price.Product.ID, productYaml)
					if conflict != nil {
						plan.Conflicts = append(plan.Conflicts, *conflict)
					} else if productCandidate != nil {
						plan.Candidates = append(plan.Candidates, *productCandidate)
					}
				}
			}
		}
	}

	// Meters: always a conflict. event_name is immutable, so gatr's
	// prefix convention can never retroactively claim an existing meter.
	for _, mr := range meterRefs {
		// Fetch only to verify existence — the decision is the same
		// either way, but a "not found" is a different error shape.
		_, err := c.fetchMeter(ctx, mr.stripeID)
		if err != nil {
			if isNotFound(err) {
				plan.Conflicts = append(plan.Conflicts, AdoptionConflict{
					Kind: AdoptionStripeObjectMissing, Resource: ResourceMeter,
					YamlID: mr.yamlID, StripeID: mr.stripeID,
					Message: fmt.Sprintf("meter %s referenced in yaml but not found in Stripe", mr.stripeID),
				})
				continue
			}
			return AdoptionPlan{}, err
		}
		// Check if it's already gatr-managed via event_name prefix.
		meter, _ := c.fetchMeter(ctx, mr.stripeID)
		if yamlIDFromName, ok := parseMeterEventName(meter.EventName, c.projectID); ok {
			if yamlIDFromName == mr.yamlID {
				// Already managed in this project — no-op.
				continue
			}
		}
		plan.Conflicts = append(plan.Conflicts, AdoptionConflict{
			Kind: AdoptionMeterNotAdoptable, Resource: ResourceMeter,
			YamlID: mr.yamlID, StripeID: mr.stripeID,
			Message: fmt.Sprintf("meter %s cannot be adopted: Stripe meter event_name is immutable. Set stripe_meter_id: null in yaml and let gatr create a fresh meter (you'll need to rewire your event source to the new event_name).", mr.stripeID),
		})
	}

	return plan, nil
}

// classifyProductForAdoption runs the same owned/unowned dance as the
// price classification, but for a product reached via a price's
// Product.ID reference. Returns (candidate, nil) on success, (nil,
// conflict) on a fatal classification, or (nil, nil) on "already
// managed under the expected yaml_id — nothing to do".
func (c *Client) classifyProductForAdoption(ctx context.Context, stripeID, yamlID string) (*AdoptionCandidate, *AdoptionConflict) {
	prod, err := c.fetchProduct(ctx, stripeID)
	if err != nil {
		if isNotFound(err) {
			return nil, &AdoptionConflict{
				Kind: AdoptionStripeObjectMissing, Resource: ResourceProduct,
				YamlID: yamlID, StripeID: stripeID,
				Message: fmt.Sprintf("product %s referenced via a price but not found in Stripe", stripeID),
			}
		}
		// Transient errors surface via the main ScanAdoptions return
		// path — classify-helper swallows to keep the surface narrow.
		// Callers that need the error should use fetchProduct directly.
		return nil, &AdoptionConflict{
			Kind: AdoptionStripeObjectMissing, Resource: ResourceProduct,
			YamlID: yamlID, StripeID: stripeID,
			Message: fmt.Sprintf("product %s fetch failed: %s", stripeID, err.Error()),
		}
	}

	yamlIDFromMeta, owned := isGatrManaged(prod.Metadata, c.projectID)
	switch {
	case owned && yamlIDFromMeta == yamlID:
		return nil, nil // already managed, no action needed
	case owned:
		return nil, &AdoptionConflict{
			Kind: AdoptionOwnedByOtherProject, Resource: ResourceProduct,
			YamlID: yamlID, StripeID: stripeID,
			Message: fmt.Sprintf("product %s is managed as %q in this project; yaml wants %q", stripeID, yamlIDFromMeta, yamlID),
		}
	case priceOwnedByOtherProject(prod.Metadata, c.projectID):
		return nil, &AdoptionConflict{
			Kind: AdoptionOwnedByOtherProject, Resource: ResourceProduct,
			YamlID: yamlID, StripeID: stripeID,
			Message: fmt.Sprintf("product %s is managed by another gatr project (metadata.gatr_id=%q)", stripeID, prod.Metadata[metaKeyGatrID]),
		}
	default:
		current := projectProduct(prod, yamlID)
		return &AdoptionCandidate{
			Resource: ResourceProduct,
			YamlID:   yamlID, StripeID: stripeID,
			Name:           prod.Name,
			ProductCurrent: &current,
		}, nil
	}
}

// AdoptProduct stamps gatr_managed=true + gatr_id onto an existing
// Stripe product. Idempotent — re-stamping with the same yaml_id is a
// no-op (Stripe de-dupes metadata updates). Existing non-gatr metadata
// keys on the object are preserved (Stripe merges by default).
func (c *Client) AdoptProduct(ctx context.Context, stripeID, yamlID string) (ManagedProduct, error) {
	if c.projectID == "" {
		return ManagedProduct{}, ErrMissingProjectID("AdoptProduct requires ClientOptions.ProjectID")
	}
	params := &stripesdk.ProductParams{
		Metadata: map[string]string{
			metaKeyManaged: "true",
			metaKeyGatrID:  gatrIDFor(c.projectID, yamlID),
		},
	}
	params.Context = ctx
	params.SetIdempotencyKey(idemKey("adopt_product", c.projectID, yamlID, stripeID))
	p, err := c.sc.Products.Update(stripeID, params)
	if err != nil {
		return ManagedProduct{}, wrapStripeAPI(err, "adopt product")
	}
	return projectProduct(p, yamlID), nil
}

// AdoptPrice stamps gatr_managed=true + gatr_id onto an existing
// Stripe price. Same invariants as AdoptProduct.
func (c *Client) AdoptPrice(ctx context.Context, stripeID, yamlID string) (ManagedPrice, error) {
	if c.projectID == "" {
		return ManagedPrice{}, ErrMissingProjectID("AdoptPrice requires ClientOptions.ProjectID")
	}
	params := &stripesdk.PriceParams{
		Metadata: map[string]string{
			metaKeyManaged: "true",
			metaKeyGatrID:  gatrIDFor(c.projectID, yamlID),
		},
	}
	params.Context = ctx
	params.SetIdempotencyKey(idemKey("adopt_price", c.projectID, yamlID, stripeID))
	p, err := c.sc.Prices.Update(stripeID, params)
	if err != nil {
		return ManagedPrice{}, wrapStripeAPI(err, "adopt price")
	}
	return projectPrice(p, yamlID), nil
}

// ── helpers ────────────────────────────────────────────────────────

// fetchProduct wraps Stripe's single-object Get with gatr's error
// types. Returns the raw *stripesdk.Product so callers can read every
// field (we don't project here — adoption needs access to Metadata).
func (c *Client) fetchProduct(ctx context.Context, id string) (*stripesdk.Product, error) {
	params := &stripesdk.ProductParams{}
	params.Context = ctx
	p, err := c.sc.Products.Get(id, params)
	if err != nil {
		return nil, wrapStripeAPI(err, "get product")
	}
	return p, nil
}

func (c *Client) fetchPrice(ctx context.Context, id string) (*stripesdk.Price, error) {
	params := &stripesdk.PriceParams{}
	params.Context = ctx
	p, err := c.sc.Prices.Get(id, params)
	if err != nil {
		return nil, wrapStripeAPI(err, "get price")
	}
	return p, nil
}

func (c *Client) fetchMeter(ctx context.Context, id string) (*stripesdk.BillingMeter, error) {
	params := &stripesdk.BillingMeterParams{}
	params.Context = ctx
	m, err := c.sc.BillingMeters.Get(id, params)
	if err != nil {
		return nil, wrapStripeAPI(err, "get meter")
	}
	return m, nil
}

// isNotFound reports whether err is a Stripe 404 / resource_missing.
// The wrapStripeAPI path lifts Stripe codes into Details["stripe_code"];
// unwrap and check both.
func isNotFound(err error) bool {
	var gatrErr *Error
	if errors.As(err, &gatrErr) {
		if code, ok := gatrErr.Details["stripe_code"].(string); ok {
			return code == "resource_missing"
		}
	}
	var serr *stripesdk.Error
	if errors.As(err, &serr) {
		return serr.HTTPStatusCode == 404 || string(serr.Code) == "resource_missing"
	}
	return false
}

// priceOwnedByOtherProject returns true when metadata.gatr_managed is
// set but metadata.gatr_id belongs to a different project. Distinct
// from "no gatr metadata at all" (which is what makes an object
// adoption-eligible).
func priceOwnedByOtherProject(meta map[string]string, projectID string) bool {
	if meta[metaKeyManaged] != "true" {
		return false
	}
	_, thisProject := parseGatrID(meta[metaKeyGatrID], projectID)
	return !thisProject
}

// productYamlForPriceYaml reverses translate.go's "<plan_id>_monthly"
// / "<plan_id>_annual" convention to recover the parent plan id, which
// IS the yaml_id gatr wants on the Stripe product. Returns "" for
// non-plan price yaml_ids (e.g. "<metered_price_id>_metered"), which
// don't share a yaml product.
func productYamlForPriceYaml(priceYamlID string) string {
	for _, suffix := range []string{PriceYamlSuffixMonthly, PriceYamlSuffixAnnual} {
		if len(priceYamlID) > len(suffix) && priceYamlID[len(priceYamlID)-len(suffix):] == suffix {
			return priceYamlID[:len(priceYamlID)-len(suffix)]
		}
	}
	return ""
}
