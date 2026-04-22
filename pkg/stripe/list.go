package stripe

import (
	"context"
	"errors"
	"fmt"

	stripesdk "github.com/stripe/stripe-go/v82"
)

// pageSize is the per-request limit handed to Stripe. 100 is the API
// max; the iterator follows starting_after cursors automatically.
const pageSize = 100

// ListManagedProducts returns every gatr-managed product on the bound
// Stripe account that belongs to this client's project. Includes BOTH
// active and archived products — the diff engine needs to know about
// archived ones so it doesn't recreate them on every push.
func (c *Client) ListManagedProducts(ctx context.Context) ([]ManagedProduct, error) {
	if c.projectID == "" {
		return nil, ErrMissingProjectID("ListManagedProducts requires ClientOptions.ProjectID")
	}
	params := &stripesdk.ProductListParams{}
	params.Limit = stripesdk.Int64(pageSize)
	params.Context = ctx
	// Active is left nil → Stripe returns both active and archived
	// products. We need the archived ones to render correct diffs.

	var out []ManagedProduct
	iter := c.sc.Products.List(params)
	for iter.Next() {
		p := iter.Product()
		yamlID, ok := isGatrManaged(p.Metadata, c.projectID)
		if !ok {
			continue
		}
		out = append(out, ManagedProduct{
			StripeID:    p.ID,
			YamlID:      yamlID,
			Name:        p.Name,
			Description: p.Description,
			Active:      p.Active,
			Metadata:    p.Metadata,
		})
	}
	if err := iter.Err(); err != nil {
		return nil, wrapStripeAPI(err, "list products")
	}
	return out, nil
}

// ListManagedPrices returns every gatr-managed price scoped to this
// project. Recurring metadata is projected onto RecurringInfo; one-time
// prices come back with Recurring=nil.
func (c *Client) ListManagedPrices(ctx context.Context) ([]ManagedPrice, error) {
	if c.projectID == "" {
		return nil, ErrMissingProjectID("ListManagedPrices requires ClientOptions.ProjectID")
	}
	params := &stripesdk.PriceListParams{}
	params.Limit = stripesdk.Int64(pageSize)
	params.Context = ctx

	var out []ManagedPrice
	iter := c.sc.Prices.List(params)
	for iter.Next() {
		p := iter.Price()
		yamlID, ok := isGatrManaged(p.Metadata, c.projectID)
		if !ok {
			continue
		}
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
		out = append(out, mp)
	}
	if err := iter.Err(); err != nil {
		return nil, wrapStripeAPI(err, "list prices")
	}
	return out, nil
}

// ListManagedMeters returns every gatr-managed meter scoped to this
// project. Identification is by event_name prefix (Stripe meters have
// no metadata field — see managed.go for the namespacing scheme).
func (c *Client) ListManagedMeters(ctx context.Context) ([]ManagedMeter, error) {
	if c.projectID == "" {
		return nil, ErrMissingProjectID("ListManagedMeters requires ClientOptions.ProjectID")
	}
	params := &stripesdk.BillingMeterListParams{}
	params.Limit = stripesdk.Int64(pageSize)
	params.Context = ctx

	var out []ManagedMeter
	iter := c.sc.BillingMeters.List(params)
	for iter.Next() {
		m := iter.BillingMeter()
		yamlID, ok := parseMeterEventName(m.EventName, c.projectID)
		if !ok {
			continue
		}
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
		out = append(out, mm)
	}
	if err := iter.Err(); err != nil {
		return nil, wrapStripeAPI(err, "list meters")
	}
	return out, nil
}

// ListAllProducts returns every product on the account — managed AND
// operator-owned, active AND archived. Counterpart to ListManagedProducts
// for the `gatr import` path, which needs to see what's already in
// Stripe without any gatr-scoped filtering. The caller is responsible
// for interpreting metadata / deciding what to do with each row.
func (c *Client) ListAllProducts(ctx context.Context) ([]*stripesdk.Product, error) {
	params := &stripesdk.ProductListParams{}
	params.Limit = stripesdk.Int64(pageSize)
	params.Context = ctx

	var out []*stripesdk.Product
	iter := c.sc.Products.List(params)
	for iter.Next() {
		out = append(out, iter.Product())
	}
	if err := iter.Err(); err != nil {
		return nil, wrapStripeAPI(err, "list all products")
	}
	return out, nil
}

// ListAllPrices returns every price on the account — unfiltered. See
// ListAllProducts. Needed by import for BillingScheme / Tiers /
// UnitAmountDecimal which the Managed* projection doesn't carry.
func (c *Client) ListAllPrices(ctx context.Context) ([]*stripesdk.Price, error) {
	params := &stripesdk.PriceListParams{}
	params.Limit = stripesdk.Int64(pageSize)
	params.Context = ctx

	var out []*stripesdk.Price
	iter := c.sc.Prices.List(params)
	for iter.Next() {
		out = append(out, iter.Price())
	}
	if err := iter.Err(); err != nil {
		return nil, wrapStripeAPI(err, "list all prices")
	}
	return out, nil
}

// ListAllMeters returns every billing meter on the account — including
// the ones gatr didn't create. Import uses this to translate meters
// regardless of event_name naming convention.
func (c *Client) ListAllMeters(ctx context.Context) ([]*stripesdk.BillingMeter, error) {
	params := &stripesdk.BillingMeterListParams{}
	params.Limit = stripesdk.Int64(pageSize)
	params.Context = ctx

	var out []*stripesdk.BillingMeter
	iter := c.sc.BillingMeters.List(params)
	for iter.Next() {
		out = append(out, iter.BillingMeter())
	}
	if err := iter.Err(); err != nil {
		return nil, wrapStripeAPI(err, "list all meters")
	}
	return out, nil
}

// wrapStripeAPI converts a stripe-go error into the wrapper's typed
// *Error. The Stripe-side error code (e.g. "api_key_expired") is
// lifted into Details["stripe_code"] so callers can render it without
// inspecting the chain.
func wrapStripeAPI(err error, op string) error {
	var serr *stripesdk.Error
	if errors.As(err, &serr) {
		code := string(serr.Code)
		if code == "" {
			code = string(serr.Type)
		}
		return ErrStripeAPI(err, code, fmt.Sprintf("%s: %s", op, serr.Msg))
	}
	return ErrStripeAPI(err, "", fmt.Sprintf("%s: %s", op, err.Error()))
}
