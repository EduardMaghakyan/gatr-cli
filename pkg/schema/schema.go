// Package schema is the Go consumer of the canonical gatr.yaml JSON Schema.
//
// The Zod schema in sdk/node/src/config/schema.ts is the source of truth.
// Run `pnpm schema:export` to regenerate schema/gatr.schema.json (which is
// embedded here via embedded/gatr.schema.json).
package schema

import "encoding/json"

const SupportedVersion = 4

// NumberOrUnlimited mirrors the TS union `number | "unlimited"`.
// Use the helper methods to distinguish between the two cases.
type NumberOrUnlimited struct {
	num       int
	unlimited bool
	set       bool
}

func (n NumberOrUnlimited) IsUnlimited() bool { return n.unlimited }
func (n NumberOrUnlimited) Int() int          { return n.num }
func (n NumberOrUnlimited) IsSet() bool       { return n.set }

func (n NumberOrUnlimited) MarshalJSON() ([]byte, error) {
	if !n.set {
		return []byte("null"), nil
	}
	if n.unlimited {
		return json.Marshal("unlimited")
	}
	return json.Marshal(n.num)
}

func (n *NumberOrUnlimited) UnmarshalJSON(b []byte) error {
	n.set = true
	if string(b) == `"unlimited"` {
		n.unlimited = true
		return nil
	}
	return json.Unmarshal(b, &n.num)
}

type Feature struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type Limit struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	Unit           string `json:"unit"`
	Period         string `json:"period"`
	PerSeatPricing bool   `json:"per_seat_pricing,omitempty"`
}

type Credit struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Rollover bool   `json:"rollover"`
}

type Operation struct {
	ID       string             `json:"id"`
	Consumes map[string]float64 `json:"consumes"`
}

type MeteredPrice struct {
	ID            string  `json:"id"`
	Name          string  `json:"name"`
	Unit          string  `json:"unit"`
	UnitPrice     float64 `json:"unit_price"`
	StripeMeterID *string `json:"stripe_meter_id"`
	Period        string  `json:"period"`
	HardCap       *int    `json:"hard_cap,omitempty"`
	Currency      string  `json:"currency"`
	Aggregation   string  `json:"aggregation"`
}

type BillingInterval struct {
	AmountCents   int     `json:"amount_cents"`
	Currency      string  `json:"currency"`
	StripePriceID *string `json:"stripe_price_id"`
	PriceDisplay  string  `json:"price_display,omitempty"`
}

type Billing struct {
	Monthly *BillingInterval `json:"monthly,omitempty"`
	Annual  *BillingInterval `json:"annual,omitempty"`
}

type Plan struct {
	ID            string                       `json:"id"`
	Name          string                       `json:"name"`
	PriceDisplay  string                       `json:"price_display,omitempty"`
	StripePriceID *string                      `json:"stripe_price_id,omitempty"`
	Billing       *Billing                     `json:"billing,omitempty"`
	TrialDays     int                          `json:"trial_days,omitempty"`
	CTA           string                       `json:"cta,omitempty"`
	CTAUrl        string                       `json:"cta_url,omitempty"`
	Features      []string                     `json:"features"`
	Limits        map[string]NumberOrUnlimited `json:"limits"`
	Grants        map[string]NumberOrUnlimited `json:"grants"`
	Includes      map[string]NumberOrUnlimited `json:"includes"`
	OveragePolicy string                       `json:"overage_policy,omitempty"`
}

// CreditPack is a one-off purchase of credits.
//
// Packs and plan grants fill the same pool, which is what makes rollover:true
// load-bearing: a renewal re-grant on a rollover:false credit hard-resets the
// balance and would delete a pack the customer had just bought.
type CreditPack struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	//: Which pool it fills. Refined against Config.Credits.
	Credit  string `json:"credit"`
	Credits int    `json:"credits"`
	//: Subunit integer. Also the anti-tamper check at grant time — the
	//: webhook compares it against the Checkout session's amount_total
	//: before topping anyone up.
	AmountCents   int     `json:"amount_cents"`
	Currency      string  `json:"currency"`
	StripePriceID *string `json:"stripe_price_id,omitempty"`
	PriceDisplay  string  `json:"price_display,omitempty"`
}

type Config struct {
	Version       int            `json:"version"`
	Project       string         `json:"project"`
	Features      []Feature      `json:"features"`
	Limits        []Limit        `json:"limits"`
	Credits       []Credit       `json:"credits"`
	Operations    []Operation    `json:"operations"`
	MeteredPrices []MeteredPrice `json:"metered_prices"`
	CreditPacks   []CreditPack   `json:"credit_packs"`
	Plans         []Plan         `json:"plans"`
}
