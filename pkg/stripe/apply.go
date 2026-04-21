package stripe

import (
	"context"
	"fmt"
	"time"
)

// ApplyResult records the outcome of one logical DiffOp. Replace ops
// internally produce two Stripe API calls (archive + create); those
// surface as a single ApplyResult here, but TWO audit-log entries
// (see AuditWriter in audit.go).
//
// On error, StripeID holds the id of whatever was produced BEFORE the
// failure — useful for the CLI's "rerun to resume" messaging.
type ApplyResult struct {
	Op       DiffOp
	StripeID string
	Err      error
}

// ApplyPlan executes the diff in topological order:
//
//  1. Products: creates + updates (archives deferred)
//  2. Meters:   creates + updates (archives deferred)
//  3. Prices:   creates + updates + replaces (needs product + meter IDs)
//  4. Archives: prices first, then meters, then products (reverse of create)
//
// Every successful Stripe call is immediately written to `audit` before
// the next call is issued — a process crash between step N and N+1
// still yields a durable trail of what landed.
//
// First error short-circuits. The returned slice contains results up
// to (and including) the failing op so the caller can render progress
// accurately. Returned error is always an *Error with code E504 when
// execution halted mid-flight.
//
// Idempotency: each underlying Upsert/Archive call carries a stable
// Idempotency-Key (per T3). Re-running after a partial failure replays
// completed ops as Stripe-side no-ops, then picks up where it stopped.
func (c *Client) ApplyPlan(ctx context.Context, plan DiffPlan, desired DesiredState, audit AuditWriter) ([]ApplyResult, error) {
	if c.projectID == "" {
		return nil, ErrMissingProjectID("ApplyPlan requires ClientOptions.ProjectID")
	}

	results := make([]ApplyResult, 0, len(plan.ProductOps)+len(plan.PriceOps)+len(plan.MeterOps))

	// Tracks product yaml_id → stripe_id for downstream price FK
	// resolution. Seeded with NoOp ops (their StripeID is known from
	// the current-state list).
	productStripeIDs := map[string]string{}
	for _, op := range plan.ProductOps {
		if op.Action == ActionNoOp || op.Action == ActionUpdated {
			productStripeIDs[op.YamlID] = op.StripeID
		}
	}
	meterStripeIDs := map[string]string{}
	for _, op := range plan.MeterOps {
		if op.Action == ActionNoOp || op.Action == ActionUpdated {
			meterStripeIDs[op.YamlID] = op.StripeID
		}
	}

	// -- Phase 1: products (non-archive) -----------------------------
	for _, op := range plan.ProductOps {
		if op.Action == ActionNoOp || op.Action == ActionArchived {
			continue
		}
		res := c.applyProductOp(ctx, op)
		results = append(results, res)
		if audit != nil {
			if werr := audit.Write(auditEntryFor(c.projectID, res)); werr != nil {
				return results, fmt.Errorf("audit write: %w", werr)
			}
		}
		if res.Err != nil {
			return results, wrapApplyHalted(op, res.Err)
		}
		productStripeIDs[op.YamlID] = res.StripeID
	}

	// -- Phase 2: meters (non-archive) -------------------------------
	for _, op := range plan.MeterOps {
		if op.Action == ActionNoOp || op.Action == ActionArchived {
			continue
		}
		res := c.applyMeterOp(ctx, op)
		results = append(results, res)
		if audit != nil {
			if werr := audit.Write(auditEntryFor(c.projectID, res)); werr != nil {
				return results, fmt.Errorf("audit write: %w", werr)
			}
		}
		if res.Err != nil {
			return results, wrapApplyHalted(op, res.Err)
		}
		meterStripeIDs[op.YamlID] = res.StripeID
	}

	// -- Phase 3: prices (non-archive) -------------------------------
	for _, op := range plan.PriceOps {
		if op.Action == ActionNoOp || op.Action == ActionArchived {
			continue
		}
		if op.PriceSpec == nil {
			return results, &Error{Code: ErrCodeApplyFailed, Message: "price op missing PriceSpec"}
		}
		// Resolve FKs from the yaml_id cross-ref maps populated by
		// TranslateConfig. Missing → bail: we'd otherwise create a
		// price with no product, which Stripe rejects with 400.
		productYaml, ok := desired.ProductYamlForPriceYaml[op.YamlID]
		if !ok {
			return results, &Error{Code: ErrCodeApplyFailed,
				Message:  fmt.Sprintf("price %s: no product mapping in DesiredState", op.YamlID),
				Details:  map[string]any{"yaml_id": op.YamlID}}
		}
		productStripeID, ok := productStripeIDs[productYaml]
		if !ok {
			return results, &Error{Code: ErrCodeApplyFailed,
				Message:  fmt.Sprintf("price %s: product %s not yet applied", op.YamlID, productYaml),
				Details:  map[string]any{"yaml_id": op.YamlID, "product_yaml": productYaml}}
		}
		op.PriceSpec.ProductStripeID = productStripeID

		if meterYaml, hasMeter := desired.MeterYamlForPriceYaml[op.YamlID]; hasMeter && op.PriceSpec.Recurring != nil {
			meterStripeID, ok := meterStripeIDs[meterYaml]
			if !ok {
				return results, &Error{Code: ErrCodeApplyFailed,
					Message:  fmt.Sprintf("price %s: meter %s not yet applied", op.YamlID, meterYaml),
					Details:  map[string]any{"yaml_id": op.YamlID, "meter_yaml": meterYaml}}
			}
			op.PriceSpec.Recurring.MeterID = meterStripeID
		}

		res := c.applyPriceOp(ctx, op)
		results = append(results, res)
		if audit != nil {
			if werr := audit.Write(auditEntryFor(c.projectID, res)); werr != nil {
				return results, fmt.Errorf("audit write: %w", werr)
			}
		}
		if res.Err != nil {
			return results, wrapApplyHalted(op, res.Err)
		}
	}

	// -- Phase 4: archives (reverse dependency order) ----------------
	// Prices first — so the parent product is still active at the
	// moment we archive its prices. Then meters. Then products.
	for _, op := range plan.PriceOps {
		if op.Action != ActionArchived {
			continue
		}
		res := ApplyResult{Op: op, StripeID: op.StripeID}
		if err := c.ArchivePrice(ctx, op.StripeID); err != nil {
			res.Err = err
		}
		results = append(results, res)
		if audit != nil {
			if werr := audit.Write(auditEntryFor(c.projectID, res)); werr != nil {
				return results, fmt.Errorf("audit write: %w", werr)
			}
		}
		if res.Err != nil {
			return results, wrapApplyHalted(op, res.Err)
		}
	}
	for _, op := range plan.MeterOps {
		if op.Action != ActionArchived {
			continue
		}
		res := ApplyResult{Op: op, StripeID: op.StripeID}
		if err := c.DeactivateMeter(ctx, op.StripeID); err != nil {
			res.Err = err
		}
		results = append(results, res)
		if audit != nil {
			if werr := audit.Write(auditEntryFor(c.projectID, res)); werr != nil {
				return results, fmt.Errorf("audit write: %w", werr)
			}
		}
		if res.Err != nil {
			return results, wrapApplyHalted(op, res.Err)
		}
	}
	for _, op := range plan.ProductOps {
		if op.Action != ActionArchived {
			continue
		}
		res := ApplyResult{Op: op, StripeID: op.StripeID}
		if err := c.ArchiveProduct(ctx, op.StripeID); err != nil {
			res.Err = err
		}
		results = append(results, res)
		if audit != nil {
			if werr := audit.Write(auditEntryFor(c.projectID, res)); werr != nil {
				return results, fmt.Errorf("audit write: %w", werr)
			}
		}
		if res.Err != nil {
			return results, wrapApplyHalted(op, res.Err)
		}
	}

	return results, nil
}

// applyProductOp executes one non-archive product op, returning an
// ApplyResult. The op's Action must be Created or Updated — NoOp is
// filtered upstream, Archive uses a dedicated phase.
func (c *Client) applyProductOp(ctx context.Context, op DiffOp) ApplyResult {
	if op.ProductSpec == nil {
		return ApplyResult{Op: op, Err: &Error{Code: ErrCodeApplyFailed, Message: "product op missing ProductSpec"}}
	}
	var current *ManagedProduct
	if op.Action == ActionUpdated {
		current = &ManagedProduct{StripeID: op.StripeID, YamlID: op.YamlID}
	}
	got, _, err := c.UpsertProduct(ctx, *op.ProductSpec, current)
	if err != nil {
		return ApplyResult{Op: op, StripeID: op.StripeID, Err: err}
	}
	return ApplyResult{Op: op, StripeID: got.StripeID}
}

func (c *Client) applyMeterOp(ctx context.Context, op DiffOp) ApplyResult {
	if op.MeterSpec == nil {
		return ApplyResult{Op: op, Err: &Error{Code: ErrCodeApplyFailed, Message: "meter op missing MeterSpec"}}
	}
	var current *ManagedMeter
	if op.Action == ActionUpdated {
		current = &ManagedMeter{
			StripeID:    op.StripeID,
			YamlID:      op.YamlID,
			EventName:   meterEventNameFor(c.projectID, op.YamlID),
			Aggregation: op.MeterSpec.Aggregation,
		}
	}
	got, _, err := c.UpsertMeter(ctx, *op.MeterSpec, current)
	if err != nil {
		return ApplyResult{Op: op, StripeID: op.StripeID, Err: err}
	}
	return ApplyResult{Op: op, StripeID: got.StripeID}
}

// applyPriceOp covers Create / Update / Replace. Replace expands into
// ArchivePrice(old) + UpsertPrice(new, nil). If the archive succeeds
// but the create fails, the caller sees the create error; the old
// price stays archived (Stripe's ground truth), which is still a
// better state than "old active + new active" duplicates.
func (c *Client) applyPriceOp(ctx context.Context, op DiffOp) ApplyResult {
	if op.Action == ActionReplaced {
		if err := c.ArchivePrice(ctx, op.StripeID); err != nil {
			return ApplyResult{Op: op, StripeID: op.StripeID, Err: err}
		}
		// Now create the new one from the same spec.
		got, _, err := c.UpsertPrice(ctx, *op.PriceSpec, nil)
		if err != nil {
			return ApplyResult{Op: op, StripeID: op.StripeID, Err: err}
		}
		return ApplyResult{Op: op, StripeID: got.StripeID}
	}

	var current *ManagedPrice
	if op.Action == ActionUpdated {
		current = &ManagedPrice{StripeID: op.StripeID, YamlID: op.YamlID}
	}
	got, _, err := c.UpsertPrice(ctx, *op.PriceSpec, current)
	if err != nil {
		return ApplyResult{Op: op, StripeID: op.StripeID, Err: err}
	}
	return ApplyResult{Op: op, StripeID: got.StripeID}
}

// wrapApplyHalted turns a per-op error into the top-level E504 apply
// failure so the CLI can render a single coherent message and exit
// non-zero. The original cause is on the Unwrap chain.
func wrapApplyHalted(op DiffOp, cause error) *Error {
	return &Error{
		Code:    ErrCodeApplyFailed,
		Message: fmt.Sprintf("apply halted at %s/%s (%s)", op.Resource, op.YamlID, op.Action),
		Details: map[string]any{
			"resource": string(op.Resource),
			"yaml_id":  op.YamlID,
			"action":   string(op.Action),
		},
		cause: cause,
	}
}

// auditEntryFor projects an ApplyResult into a stable audit-log row.
// Timestamp is UTC RFC3339Nano so sorting and deduplication across
// machines are straightforward.
func auditEntryFor(projectID string, r ApplyResult) AuditEntry {
	entry := AuditEntry{
		Timestamp: time.Now().UTC().Format(time.RFC3339Nano),
		ProjectID: projectID,
		Resource:  string(r.Op.Resource),
		Action:    string(r.Op.Action),
		YamlID:    r.Op.YamlID,
		StripeID:  r.StripeID,
		Changes:   r.Op.Changes,
	}
	if r.Err != nil {
		entry.Error = r.Err.Error()
	}
	return entry
}
