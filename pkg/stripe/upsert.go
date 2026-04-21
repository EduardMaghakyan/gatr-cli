package stripe

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"maps"
	"slices"
	"strings"

	stripesdk "github.com/stripe/stripe-go/v82"
)

// ApplyAction is the result classification produced by an Upsert call.
// Created/Updated/Archived hit Stripe; NoOp does not. The CLI's diff
// renderer uses this to colour and summarise the apply.
type ApplyAction string

const (
	ActionCreated  ApplyAction = "created"
	ActionUpdated  ApplyAction = "updated"
	ActionNoOp     ApplyAction = "noop"
	ActionArchived ApplyAction = "archived"
	// ActionReplaced means "archive the current, then create a fresh
	// one with the desired shape" — the only way Stripe lets us change
	// a price's amount/currency/recurring fields, which are immutable
	// on the API. The apply engine expands a single Replace DiffOp
	// into two round-trips.
	ActionReplaced ApplyAction = "replaced"
)

// ProductSpec is the desired Stripe shape of a product, sourced from
// gatr.yaml. The wrapper attaches gatr_managed=true and the namespaced
// gatr_id metadata automatically — callers MUST NOT pre-populate those
// keys (Upsert refuses if they collide).
type ProductSpec struct {
	YamlID      string
	Name        string
	Description string
	// Active defaults to true at create time; pass false to create an
	// already-archived product (rare but valid for parity testing).
	Active bool
	// Extra metadata the user wants stamped on the product. Reserved
	// keys (gatr_managed, gatr_id) are forbidden.
	Metadata map[string]string
}

// PriceSpec captures the desired shape of a Stripe price. Most price
// fields are IMMUTABLE on Stripe — UnitAmount, Currency, and Recurring
// settings cannot be patched. UpsertPrice handles only the soft-update
// case (metadata, Active); the diff engine in T4 issues a separate
// "archive old + create new" pair when hard fields change.
type PriceSpec struct {
	YamlID          string
	ProductStripeID string // resolved from the product upsert step
	UnitAmount      int64
	Currency        string
	Recurring       *RecurringInfo
	Active          bool
	Metadata        map[string]string
}

// MeterSpec captures the desired shape of a Stripe billing meter. The
// EventName is derived automatically from (project, yamlID) — callers
// MUST NOT set it (Upsert refuses if they try).
type MeterSpec struct {
	YamlID      string
	DisplayName string
	// Aggregation maps to BillingMeter.DefaultAggregation.Formula.
	// Stripe accepts "sum", "count", "last".
	Aggregation string
}

// UpsertProduct creates / updates / no-ops a product. current==nil
// means "not in Stripe → create". When current!=nil, fields are
// compared and a PATCH is issued only if any differ (otherwise NoOp).
//
// Idempotency-Key on the underlying API call is derived from
// (operation, project, yaml_id, content_hash) so a CLI retry within
// 24h is a Stripe-side deduplication, not a duplicate object.
func (c *Client) UpsertProduct(ctx context.Context, spec ProductSpec, current *ManagedProduct) (ManagedProduct, ApplyAction, error) {
	if c.projectID == "" {
		return ManagedProduct{}, "", ErrMissingProjectID("UpsertProduct requires ClientOptions.ProjectID")
	}
	if err := validateSpecMetadata(spec.Metadata); err != nil {
		return ManagedProduct{}, "", err
	}
	if spec.YamlID == "" {
		return ManagedProduct{}, "", newError(ErrCodeApplyFailed, "ProductSpec.YamlID required", nil, nil)
	}

	stamped := stampedMetadata(spec.Metadata, c.projectID, spec.YamlID)
	hash := contentHashProduct(spec, stamped)

	if current == nil {
		params := &stripesdk.ProductParams{
			Name:     stripesdk.String(spec.Name),
			Active:   stripesdk.Bool(spec.Active),
			Metadata: stamped,
		}
		// Stripe rejects empty-string for Description on create —
		// "We assume empty values are an attempt to unset a parameter;
		// however 'description' cannot be unset." Only include the
		// field when the yaml actually supplies a price_display.
		if spec.Description != "" {
			params.Description = stripesdk.String(spec.Description)
		}
		params.Context = ctx
		params.SetIdempotencyKey(idemKey("create_product", c.projectID, spec.YamlID, hash))
		p, err := c.sc.Products.New(params)
		if err != nil {
			return ManagedProduct{}, "", wrapStripeAPI(err, "create product")
		}
		return projectProduct(p, spec.YamlID), ActionCreated, nil
	}

	if productEqual(spec, stamped, *current) {
		return *current, ActionNoOp, nil
	}

	params := &stripesdk.ProductParams{
		Name:     stripesdk.String(spec.Name),
		Active:   stripesdk.Bool(spec.Active),
		Metadata: stamped,
	}
	if spec.Description != "" {
		params.Description = stripesdk.String(spec.Description)
	}
	params.Context = ctx
	params.SetIdempotencyKey(idemKey("update_product", c.projectID, spec.YamlID, hash))
	p, err := c.sc.Products.Update(current.StripeID, params)
	if err != nil {
		return ManagedProduct{}, "", wrapStripeAPI(err, "update product")
	}
	return projectProduct(p, spec.YamlID), ActionUpdated, nil
}

// ArchiveProduct sets active=false on an existing product. Stripe does
// NOT permit deletion of products that have ever been used; archive is
// the only safe disposal — see the M6+M7 plan risk register.
func (c *Client) ArchiveProduct(ctx context.Context, productID string) error {
	params := &stripesdk.ProductParams{Active: stripesdk.Bool(false)}
	params.Context = ctx
	params.SetIdempotencyKey(idemKey("archive_product", c.projectID, productID, ""))
	if _, err := c.sc.Products.Update(productID, params); err != nil {
		return wrapStripeAPI(err, "archive product")
	}
	return nil
}

// UpsertPrice creates / updates (soft only) / no-ops a price. Hard
// fields (UnitAmount, Currency, Recurring) are IMMUTABLE on Stripe —
// callers must detect a hard-field change at the diff layer and call
// CreatePrice + ArchivePrice as a pair instead of UpsertPrice. If a
// hard change reaches Upsert, it returns an E504 error rather than
// silently no-op'ing.
func (c *Client) UpsertPrice(ctx context.Context, spec PriceSpec, current *ManagedPrice) (ManagedPrice, ApplyAction, error) {
	if c.projectID == "" {
		return ManagedPrice{}, "", ErrMissingProjectID("UpsertPrice requires ClientOptions.ProjectID")
	}
	if err := validateSpecMetadata(spec.Metadata); err != nil {
		return ManagedPrice{}, "", err
	}
	if spec.YamlID == "" || spec.ProductStripeID == "" {
		return ManagedPrice{}, "", newError(ErrCodeApplyFailed, "PriceSpec.YamlID and ProductStripeID required", nil, nil)
	}

	stamped := stampedMetadata(spec.Metadata, c.projectID, spec.YamlID)
	hash := contentHashPrice(spec, stamped)

	if current == nil {
		params := &stripesdk.PriceParams{
			Product:    stripesdk.String(spec.ProductStripeID),
			Currency:   stripesdk.String(spec.Currency),
			UnitAmount: stripesdk.Int64(spec.UnitAmount),
			Active:     stripesdk.Bool(spec.Active),
			Metadata:   stamped,
		}
		if spec.Recurring != nil {
			rp := &stripesdk.PriceRecurringParams{
				Interval:  stripesdk.String(spec.Recurring.Interval),
				UsageType: stripesdk.String(spec.Recurring.UsageType),
			}
			if spec.Recurring.IntervalCount > 0 {
				rp.IntervalCount = stripesdk.Int64(spec.Recurring.IntervalCount)
			}
			if spec.Recurring.MeterID != "" {
				rp.Meter = stripesdk.String(spec.Recurring.MeterID)
			}
			params.Recurring = rp
		}
		params.Context = ctx
		params.SetIdempotencyKey(idemKey("create_price", c.projectID, spec.YamlID, hash))
		p, err := c.sc.Prices.New(params)
		if err != nil {
			return ManagedPrice{}, "", wrapStripeAPI(err, "create price")
		}
		return projectPrice(p, spec.YamlID), ActionCreated, nil
	}

	if priceHardFieldsChanged(spec, *current) {
		return ManagedPrice{}, "", newError(ErrCodeApplyFailed,
			fmt.Sprintf("price %s: terms changed (amount/currency/recurring); diff engine must archive+recreate, not update", spec.YamlID),
			nil, map[string]any{"yaml_id": spec.YamlID})
	}
	if priceSoftEqual(spec, stamped, *current) {
		return *current, ActionNoOp, nil
	}

	// Soft fields only: metadata, active.
	params := &stripesdk.PriceParams{
		Active:   stripesdk.Bool(spec.Active),
		Metadata: stamped,
	}
	params.Context = ctx
	params.SetIdempotencyKey(idemKey("update_price", c.projectID, spec.YamlID, hash))
	p, err := c.sc.Prices.Update(current.StripeID, params)
	if err != nil {
		return ManagedPrice{}, "", wrapStripeAPI(err, "update price")
	}
	return projectPrice(p, spec.YamlID), ActionUpdated, nil
}

// ArchivePrice sets active=false on an existing price. Stripe does
// not allow deletion; only deactivation. Archived prices remain
// referenceable by historical subscriptions.
func (c *Client) ArchivePrice(ctx context.Context, priceID string) error {
	params := &stripesdk.PriceParams{Active: stripesdk.Bool(false)}
	params.Context = ctx
	params.SetIdempotencyKey(idemKey("archive_price", c.projectID, priceID, ""))
	if _, err := c.sc.Prices.Update(priceID, params); err != nil {
		return wrapStripeAPI(err, "archive price")
	}
	return nil
}

// UpsertMeter creates / updates (DisplayName only — meter event_name
// and aggregation are immutable) / no-ops a billing meter.
func (c *Client) UpsertMeter(ctx context.Context, spec MeterSpec, current *ManagedMeter) (ManagedMeter, ApplyAction, error) {
	if c.projectID == "" {
		return ManagedMeter{}, "", ErrMissingProjectID("UpsertMeter requires ClientOptions.ProjectID")
	}
	if spec.YamlID == "" {
		return ManagedMeter{}, "", newError(ErrCodeApplyFailed, "MeterSpec.YamlID required", nil, nil)
	}

	eventName := meterEventNameFor(c.projectID, spec.YamlID)
	hash := contentHashMeter(spec, eventName)

	if current == nil {
		if spec.Aggregation == "" {
			return ManagedMeter{}, "", newError(ErrCodeApplyFailed,
				"MeterSpec.Aggregation required on create (sum / count / last)", nil, nil)
		}
		params := &stripesdk.BillingMeterParams{
			DisplayName: stripesdk.String(spec.DisplayName),
			EventName:   stripesdk.String(eventName),
			DefaultAggregation: &stripesdk.BillingMeterDefaultAggregationParams{
				Formula: stripesdk.String(spec.Aggregation),
			},
		}
		params.Context = ctx
		params.SetIdempotencyKey(idemKey("create_meter", c.projectID, spec.YamlID, hash))
		m, err := c.sc.BillingMeters.New(params)
		if err != nil {
			return ManagedMeter{}, "", wrapStripeAPI(err, "create meter")
		}
		return projectMeter(m, spec.YamlID), ActionCreated, nil
	}

	if meterHardFieldsChanged(spec, eventName, *current) {
		return ManagedMeter{}, "", newError(ErrCodeApplyFailed,
			fmt.Sprintf("meter %s: event_name or aggregation changed (immutable on Stripe); recreate required", spec.YamlID),
			nil, map[string]any{"yaml_id": spec.YamlID})
	}
	if spec.DisplayName == current.DisplayName {
		return *current, ActionNoOp, nil
	}

	params := &stripesdk.BillingMeterParams{
		DisplayName: stripesdk.String(spec.DisplayName),
	}
	params.Context = ctx
	params.SetIdempotencyKey(idemKey("update_meter", c.projectID, spec.YamlID, hash))
	m, err := c.sc.BillingMeters.Update(current.StripeID, params)
	if err != nil {
		return ManagedMeter{}, "", wrapStripeAPI(err, "update meter")
	}
	return projectMeter(m, spec.YamlID), ActionUpdated, nil
}

// DeactivateMeter is the meter equivalent of ArchiveProduct/Price.
// Stripe meters are never deleted; a deactivated meter no longer
// accepts events but historical aggregations remain queryable.
func (c *Client) DeactivateMeter(ctx context.Context, meterID string) error {
	params := &stripesdk.BillingMeterDeactivateParams{}
	params.Context = ctx
	params.SetIdempotencyKey(idemKey("deactivate_meter", c.projectID, meterID, ""))
	if _, err := c.sc.BillingMeters.Deactivate(meterID, params); err != nil {
		return wrapStripeAPI(err, "deactivate meter")
	}
	return nil
}

// stampedMetadata returns a fresh map containing the user metadata
// PLUS the gatr-required keys. Returns a new map to avoid mutating
// the caller's slice; callers can keep treating their map as input.
func stampedMetadata(user map[string]string, projectID, yamlID string) map[string]string {
	out := map[string]string{
		metaKeyManaged: "true",
		metaKeyGatrID:  gatrIDFor(projectID, yamlID),
	}
	for k, v := range user {
		out[k] = v
	}
	return out
}

// validateSpecMetadata refuses caller-supplied gatr_managed / gatr_id
// keys. Allowing them would let a yaml override the namespacing and
// silently retarget another project's object — the worst kind of
// silent data corruption.
func validateSpecMetadata(meta map[string]string) error {
	for _, reserved := range []string{metaKeyManaged, metaKeyGatrID} {
		if _, present := meta[reserved]; present {
			return newError(ErrCodeApplyFailed,
				fmt.Sprintf("metadata key %q is reserved for gatr; remove it from the spec", reserved),
				nil, map[string]any{"key": reserved})
		}
	}
	return nil
}

// idemKey derives a stable Stripe Idempotency-Key for an operation.
// Format: gatr_<32-hex>. The 32-char prefix of the SHA-256 is more
// than enough collision resistance for our scale (< 1e9 ops/account).
func idemKey(op, projectID, yamlID, contentHash string) string {
	h := sha256.Sum256([]byte(strings.Join([]string{op, projectID, yamlID, contentHash}, "|")))
	return "gatr_" + hex.EncodeToString(h[:])[:32]
}

// contentHashProduct hashes the canonical fields of a product spec.
// Used only for idempotency-key derivation — equivalence checking is
// done field-by-field in productEqual.
func contentHashProduct(spec ProductSpec, stamped map[string]string) string {
	h := sha256.New()
	fmt.Fprintf(h, "name=%s|desc=%s|active=%v", spec.Name, spec.Description, spec.Active)
	for _, k := range slices.Sorted(maps.Keys(stamped)) {
		fmt.Fprintf(h, "|m[%s]=%s", k, stamped[k])
	}
	return hex.EncodeToString(h.Sum(nil))
}

func contentHashPrice(spec PriceSpec, stamped map[string]string) string {
	h := sha256.New()
	fmt.Fprintf(h, "prod=%s|amt=%d|cur=%s|active=%v", spec.ProductStripeID, spec.UnitAmount, spec.Currency, spec.Active)
	if spec.Recurring != nil {
		fmt.Fprintf(h, "|int=%s|cnt=%d|usage=%s|meter=%s",
			spec.Recurring.Interval, spec.Recurring.IntervalCount,
			spec.Recurring.UsageType, spec.Recurring.MeterID)
	}
	for _, k := range slices.Sorted(maps.Keys(stamped)) {
		fmt.Fprintf(h, "|m[%s]=%s", k, stamped[k])
	}
	return hex.EncodeToString(h.Sum(nil))
}

func contentHashMeter(spec MeterSpec, eventName string) string {
	h := sha256.New()
	fmt.Fprintf(h, "name=%s|event=%s|agg=%s", spec.DisplayName, eventName, spec.Aggregation)
	return hex.EncodeToString(h.Sum(nil))
}

// productEqual reports whether the spec already matches what's in
// Stripe — used to short-circuit no-op apply.
//
// An empty spec.Description is treated as "match anything" because
// yaml's price_display is optional; if the user doesn't supply it,
// we don't want to loop-update a Stripe product that has a description
// set through some other channel (dashboard edit, earlier yaml, etc.).
// Upsert never SENDS an empty Description, so idempotency is preserved:
// once current.Description is set, an empty-yaml spec leaves it alone.
func productEqual(spec ProductSpec, stamped map[string]string, current ManagedProduct) bool {
	descMatches := spec.Description == "" || spec.Description == current.Description
	return spec.Name == current.Name &&
		descMatches &&
		spec.Active == current.Active &&
		stringMapsEqual(stamped, current.Metadata)
}

// priceHardFieldsChanged reports whether spec disagrees with current
// on any IMMUTABLE field. Callers (T4 diff engine) react by emitting
// an archive+recreate pair instead of an update.
func priceHardFieldsChanged(spec PriceSpec, current ManagedPrice) bool {
	if spec.UnitAmount != current.UnitAmount {
		return true
	}
	if spec.Currency != current.Currency {
		return true
	}
	if spec.ProductStripeID != current.ProductStripeID {
		return true
	}
	if (spec.Recurring == nil) != (current.Recurring == nil) {
		return true
	}
	if spec.Recurring != nil {
		if spec.Recurring.Interval != current.Recurring.Interval {
			return true
		}
		// IntervalCount=0 is treated as 1 by Stripe; normalise before compare.
		specCount := spec.Recurring.IntervalCount
		if specCount == 0 {
			specCount = 1
		}
		curCount := current.Recurring.IntervalCount
		if curCount == 0 {
			curCount = 1
		}
		if specCount != curCount {
			return true
		}
		if spec.Recurring.UsageType != current.Recurring.UsageType {
			return true
		}
		if spec.Recurring.MeterID != current.Recurring.MeterID {
			return true
		}
	}
	return false
}

// priceSoftEqual reports whether the spec matches current on the
// updatable fields (active + metadata). Hard fields are assumed to
// match (caller must pre-check via priceHardFieldsChanged).
func priceSoftEqual(spec PriceSpec, stamped map[string]string, current ManagedPrice) bool {
	return spec.Active == current.Active && stringMapsEqual(stamped, current.Metadata)
}

// meterHardFieldsChanged reports event_name / aggregation drift.
// Both are immutable on Stripe — drift means we must recreate.
func meterHardFieldsChanged(spec MeterSpec, eventName string, current ManagedMeter) bool {
	if eventName != current.EventName {
		return true
	}
	if spec.Aggregation != "" && spec.Aggregation != current.Aggregation {
		return true
	}
	return false
}

// stringMapsEqual is a content-equality check for map[string]string,
// resilient to nil vs empty distinctions.
func stringMapsEqual(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if bv, ok := b[k]; !ok || bv != v {
			return false
		}
	}
	return true
}

func projectProduct(p *stripesdk.Product, yamlID string) ManagedProduct {
	return ManagedProduct{
		StripeID:    p.ID,
		YamlID:      yamlID,
		Name:        p.Name,
		Description: p.Description,
		Active:      p.Active,
		Metadata:    p.Metadata,
	}
}

func projectPrice(p *stripesdk.Price, yamlID string) ManagedPrice {
	mp := ManagedPrice{
		StripeID:   p.ID,
		YamlID:     yamlID,
		UnitAmount: p.UnitAmount,
		Currency:   string(p.Currency),
		Active:     p.Active,
		Metadata:   p.Metadata,
	}
	if p.Product != nil {
		mp.ProductStripeID = p.Product.ID
	}
	if p.Recurring != nil {
		mp.Recurring = &RecurringInfo{
			Interval:      string(p.Recurring.Interval),
			IntervalCount: p.Recurring.IntervalCount,
			UsageType:     string(p.Recurring.UsageType),
			MeterID:       p.Recurring.Meter,
		}
	}
	return mp
}

func projectMeter(m *stripesdk.BillingMeter, yamlID string) ManagedMeter {
	mm := ManagedMeter{
		StripeID:    m.ID,
		YamlID:      yamlID,
		DisplayName: m.DisplayName,
		EventName:   m.EventName,
		Status:      string(m.Status),
	}
	if m.DefaultAggregation != nil {
		mm.Aggregation = string(m.DefaultAggregation.Formula)
	}
	return mm
}
