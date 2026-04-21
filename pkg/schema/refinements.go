package schema

// codePriority matches sdk/node/src/config/load.ts CODE_PRIORITY. If both
// sides diverge, the multi-violation drift test will fail.
var codePriority = []string{
	"E003",
	"E011",
	"E012",
	"E015",
	"E016",
	"E017",
	"E018",
	"E013",
	"E014",
	"E010",
}

// validateRefinements runs the cross-field semantic checks that JSON Schema
// cannot express (these correspond 1:1 with the Zod superRefine + refine
// blocks in sdk/node/src/config/schema.ts).
//
// All checks run; the highest-priority violation (per codePriority) is
// returned, so both parsers pick the same code for the same input.
func validateRefinements(c *Config) *Error {
	all := []*Error{
		checkUniqueIDs(c),
		checkPlanFeatureRefs(c),
		checkOperationCreditRefs(c),
		checkPlanGrantRefs(c),
		checkPlanLimitRefs(c),
		checkPlanIncludeRefs(c),
		checkLimitPerSeatPeriod(c),
		checkBillingHasInterval(c),
	}
	return pickByPriority(all)
}

func pickByPriority(errs []*Error) *Error {
	nonNil := errs[:0:0]
	for _, e := range errs {
		if e != nil {
			nonNil = append(nonNil, e)
		}
	}
	if len(nonNil) == 0 {
		return nil
	}
	for _, code := range codePriority {
		for _, e := range nonNil {
			if e.Code == code {
				return e
			}
		}
	}
	return nonNil[0]
}

func checkUniqueIDs(c *Config) *Error {
	checks := []struct {
		scope string
		ids   []string
	}{
		{"features", featureIDs(c.Features)},
		{"limits", limitIDs(c.Limits)},
		{"credits", creditIDs(c.Credits)},
		{"operations", operationIDs(c.Operations)},
		{"metered_prices", meteredIDs(c.MeteredPrices)},
		{"plans", planIDs(c.Plans)},
	}
	for _, ch := range checks {
		seen := make(map[string]struct{}, len(ch.ids))
		for _, id := range ch.ids {
			if _, dup := seen[id]; dup {
				return &Error{Code: "E011", Message: "duplicate id in " + ch.scope + ": " + id}
			}
			seen[id] = struct{}{}
		}
	}
	return nil
}

func checkPlanFeatureRefs(c *Config) *Error {
	declared := idSet(featureIDs(c.Features))
	for _, p := range c.Plans {
		for _, fid := range p.Features {
			if _, ok := declared[fid]; !ok {
				return &Error{
					Code:    "E012",
					Message: "plan '" + p.ID + "' references undeclared feature '" + fid + "'",
				}
			}
		}
	}
	return nil
}

func checkOperationCreditRefs(c *Config) *Error {
	declared := idSet(creditIDs(c.Credits))
	for _, op := range c.Operations {
		for key := range op.Consumes {
			if _, ok := declared[key]; !ok {
				return &Error{
					Code:    "E015",
					Message: "operation '" + op.ID + "' consumes undeclared credit '" + key + "'",
				}
			}
		}
	}
	return nil
}

func checkPlanGrantRefs(c *Config) *Error {
	declared := idSet(creditIDs(c.Credits))
	for _, p := range c.Plans {
		for key := range p.Grants {
			if _, ok := declared[key]; !ok {
				return &Error{
					Code:    "E016",
					Message: "plan '" + p.ID + "' grants undeclared credit '" + key + "'",
				}
			}
		}
	}
	return nil
}

func checkPlanLimitRefs(c *Config) *Error {
	declared := idSet(limitIDs(c.Limits))
	for _, p := range c.Plans {
		for key := range p.Limits {
			if _, ok := declared[key]; !ok {
				return &Error{
					Code:    "E017",
					Message: "plan '" + p.ID + "' sets undeclared limit '" + key + "'",
				}
			}
		}
	}
	return nil
}

func checkPlanIncludeRefs(c *Config) *Error {
	declared := idSet(meteredIDs(c.MeteredPrices))
	for _, p := range c.Plans {
		for key := range p.Includes {
			if _, ok := declared[key]; !ok {
				return &Error{
					Code:    "E018",
					Message: "plan '" + p.ID + "' includes undeclared metered_price '" + key + "'",
				}
			}
		}
	}
	return nil
}

func checkLimitPerSeatPeriod(c *Config) *Error {
	for _, l := range c.Limits {
		if l.PerSeatPricing && l.Period != "never" {
			return &Error{
				Code:    "E014",
				Message: "limit '" + l.ID + "' has per_seat_pricing but period is " + l.Period,
			}
		}
	}
	return nil
}

func checkBillingHasInterval(c *Config) *Error {
	for _, p := range c.Plans {
		if p.Billing == nil {
			continue
		}
		if p.Billing.Monthly == nil && p.Billing.Annual == nil {
			return &Error{
				Code:    "E010",
				Message: "plan '" + p.ID + "' has empty billing block",
			}
		}
	}
	return nil
}

func idSet(ids []string) map[string]struct{} {
	out := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		out[id] = struct{}{}
	}
	return out
}

func featureIDs(xs []Feature) []string {
	out := make([]string, len(xs))
	for i, x := range xs {
		out[i] = x.ID
	}
	return out
}
func limitIDs(xs []Limit) []string {
	out := make([]string, len(xs))
	for i, x := range xs {
		out[i] = x.ID
	}
	return out
}
func creditIDs(xs []Credit) []string {
	out := make([]string, len(xs))
	for i, x := range xs {
		out[i] = x.ID
	}
	return out
}
func operationIDs(xs []Operation) []string {
	out := make([]string, len(xs))
	for i, x := range xs {
		out[i] = x.ID
	}
	return out
}
func meteredIDs(xs []MeteredPrice) []string {
	out := make([]string, len(xs))
	for i, x := range xs {
		out[i] = x.ID
	}
	return out
}
func planIDs(xs []Plan) []string {
	out := make([]string, len(xs))
	for i, x := range xs {
		out[i] = x.ID
	}
	return out
}
