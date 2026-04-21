package stripe

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
)

// recordingAudit captures every AuditEntry in-memory — tests assert
// on the ordered slice instead of parsing JSONL.
type recordingAudit struct {
	mu      chan struct{}
	entries []AuditEntry
	// failAfter: return an error from Write after N successful writes.
	// 0 = never fail.
	failAfter int
	written   int32
}

func newRecordingAudit() *recordingAudit {
	return &recordingAudit{mu: make(chan struct{}, 1)}
}

func (r *recordingAudit) Write(e AuditEntry) error {
	r.mu <- struct{}{}
	defer func() { <-r.mu }()
	if r.failAfter > 0 && int(atomic.LoadInt32(&r.written)) >= r.failAfter {
		return errors.New("audit sink full")
	}
	r.entries = append(r.entries, e)
	atomic.AddInt32(&r.written, 1)
	return nil
}

// fixtureDesiredFull is a 1-product + 1-monthly-price + 1-meter +
// 1-metered-price + 1-synthetic-product layout — the minimum needed
// to exercise FK resolution across all three resource families.
func fixtureDesiredFull() DesiredState {
	return DesiredState{
		Products: []ProductSpec{
			{YamlID: "pro", Name: "Pro", Active: true},
			{YamlID: "api_calls", Name: "API calls", Description: "Metered: calls", Active: true},
		},
		Prices: []PriceSpec{
			{
				YamlID:     "pro_monthly",
				UnitAmount: 2900,
				Currency:   "usd",
				Active:     true,
				Recurring:  &RecurringInfo{Interval: "month", UsageType: "licensed"},
			},
			{
				YamlID:     "api_calls_metered",
				UnitAmount: 1,
				Currency:   "usd",
				Active:     true,
				Recurring:  &RecurringInfo{Interval: "month", UsageType: "metered"},
			},
		},
		Meters: []MeterSpec{
			{YamlID: "api_calls", DisplayName: "API calls", Aggregation: "sum"},
		},
		ProductYamlForPriceYaml: map[string]string{
			"pro_monthly":       "pro",
			"api_calls_metered": "api_calls",
		},
		MeterYamlForPriceYaml: map[string]string{
			"api_calls_metered": "api_calls",
		},
	}
}

// wireCleanApplyFixture seeds the fake Stripe with POST responses for
// each expected create. Returns the clients / plans / audit set up.
func wireCleanApplyFixture(t *testing.T) (*Client, DiffPlan, DesiredState, *recordingAudit, *recordingStripe) {
	t.Helper()
	rs := newRecordingStripe(t)
	rs.reply("POST", "/v1/products", map[string]any{
		// Stripe echoes the first create as this object. Subsequent
		// POSTs overwrite the response and return the second object.
		// Our tests rely on the fact that pkg/stripe's create path
		// projects the body Stripe returned, not the spec it sent —
		// so the test can tell create order by checking StripeIDs.
		"id": "prod_first", "object": "product", "name": "First",
		"metadata": map[string]string{
			metaKeyManaged: "true",
			metaKeyGatrID:  gatrIDFor(testProjectID, "pro"),
		},
	})
	// The fixture has TWO product creates back-to-back. A single
	// responses map wouldn't differentiate. Swap the handler to a
	// counter-aware dispatch.
	var productHits int32
	rs.mu.Lock()
	rs.server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rs.mu.Lock()
		rs.recorded = append(rs.recorded, recordedRequest{
			Method:         r.Method,
			Path:           r.URL.Path,
			IdempotencyKey: r.Header.Get("Idempotency-Key"),
		})
		rs.mu.Unlock()

		switch {
		case r.URL.Path == "/v1/products" && r.Method == "POST":
			n := atomic.AddInt32(&productHits, 1)
			id := "prod_pro"
			yamlID := "pro"
			if n == 2 {
				id = "prod_api"
				yamlID = "api_calls"
			}
			writeJSON(w, http.StatusOK, map[string]any{
				"id": id, "object": "product", "name": "Name",
				"metadata": map[string]string{
					metaKeyManaged: "true",
					metaKeyGatrID:  gatrIDFor(testProjectID, yamlID),
				},
			})

		case r.URL.Path == "/v1/billing/meters" && r.Method == "POST":
			writeJSON(w, http.StatusOK, map[string]any{
				"id": "mtr_api", "object": "billing.meter",
				"display_name": "API calls",
				"event_name":   meterEventNameFor(testProjectID, "api_calls"),
				"status":       "active",
				"default_aggregation": map[string]any{"formula": "sum"},
			})

		case r.URL.Path == "/v1/prices" && r.Method == "POST":
			writeJSON(w, http.StatusOK, map[string]any{
				"id": "price_new", "object": "price", "active": true,
				"unit_amount": 2900, "currency": "usd",
				"product": "prod_pro",
				"recurring": map[string]any{
					"interval": "month", "usage_type": "licensed",
				},
				"metadata": map[string]string{
					metaKeyManaged: "true",
					metaKeyGatrID:  gatrIDFor(testProjectID, "pro_monthly"),
				},
			})

		default:
			http.NotFound(w, r)
		}
	})
	rs.mu.Unlock()

	c, err := NewClient(ClientOptions{
		SecretKey: validKey, ProjectID: testProjectID, BackendURL: rs.server.URL,
	})
	require.NoError(t, err)

	desired := fixtureDesiredFull()
	plan := ComputeDiff(desired, CurrentState{})
	return c, plan, desired, newRecordingAudit(), rs
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

// ---- Test 1: clean apply --------------------------------------------------

func TestApplyPlan_CleanApply_WritesOneAuditRowPerOp(t *testing.T) {
	c, plan, desired, audit, _ := wireCleanApplyFixture(t)
	results, err := c.ApplyPlan(context.Background(), plan, desired, audit)
	require.NoError(t, err)

	// 2 product creates + 1 meter create + 2 price creates = 5 ops.
	require.Len(t, results, 5)
	for _, r := range results {
		require.NoError(t, r.Err)
		require.NotEmpty(t, r.StripeID)
	}
	require.Len(t, audit.entries, 5)

	// Audit rows carry the right shape: action, project_id, yaml_id.
	for _, e := range audit.entries {
		require.Equal(t, testProjectID, e.ProjectID)
		require.NotEmpty(t, e.Timestamp)
		require.Empty(t, e.Error, "clean apply must have no error rows")
	}

	// Execution order: products first, then meters, then prices.
	resources := []string{}
	for _, e := range audit.entries {
		resources = append(resources, e.Resource)
	}
	require.Equal(t, []string{"product", "product", "meter", "price", "price"}, resources)
}

// ---- Test 2: partial failure -----------------------------------------------

func TestApplyPlan_PartialFailure_AuditRecordsSuccessesThenError(t *testing.T) {
	rs := newRecordingStripe(t)
	// 1st product succeeds, 2nd product fails with a 500 — the
	// pipeline must halt at product 2 and surface a typed E504.
	var productHits int32
	rs.mu.Lock()
	rs.server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rs.mu.Lock()
		rs.recorded = append(rs.recorded, recordedRequest{Method: r.Method, Path: r.URL.Path})
		rs.mu.Unlock()
		if r.URL.Path == "/v1/products" && r.Method == "POST" {
			n := atomic.AddInt32(&productHits, 1)
			if n == 2 {
				writeJSON(w, http.StatusInternalServerError, map[string]any{
					"error": map[string]any{"type": "api_error", "message": "Stripe is down"},
				})
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{
				"id": "prod_ok", "object": "product", "name": "OK",
				"metadata": map[string]string{
					metaKeyManaged: "true",
					metaKeyGatrID:  gatrIDFor(testProjectID, "pro"),
				},
			})
			return
		}
		http.NotFound(w, r)
	})
	rs.mu.Unlock()

	c, err := NewClient(ClientOptions{
		SecretKey: validKey, ProjectID: testProjectID, BackendURL: rs.server.URL,
	})
	require.NoError(t, err)

	desired := fixtureDesiredFull()
	plan := ComputeDiff(desired, CurrentState{})
	audit := newRecordingAudit()

	results, err := c.ApplyPlan(context.Background(), plan, desired, audit)
	require.Error(t, err, "apply must halt and return an error on Stripe failure")

	var sErr *Error
	require.True(t, errors.As(err, &sErr))
	require.Equal(t, ErrCodeApplyFailed, sErr.Code)
	require.Equal(t, "product", sErr.Details["resource"])

	// Audit recorded the first success + the failing attempt. The
	// retry would see the first product as already-created (NoOp) and
	// pick up at the second — that's the whole point.
	require.Len(t, audit.entries, 2)
	require.Empty(t, audit.entries[0].Error)
	require.NotEmpty(t, audit.entries[1].Error)

	// Downstream phases didn't execute: no meter or price rows.
	for _, r := range results {
		require.Equal(t, ResourceProduct, r.Op.Resource)
	}
}

// ---- Test 3: idempotent re-run --------------------------------------------

func TestApplyPlan_IdempotentRerun_NoNewApiCalls(t *testing.T) {
	// First run creates; second run, with current state reflecting
	// the first run, computes an all-noop plan → zero API calls.
	current := CurrentState{
		Products: []ManagedProduct{
			{StripeID: "prod_pro", YamlID: "pro", Name: "Pro", Active: true},
			{StripeID: "prod_api", YamlID: "api_calls", Name: "API calls",
				Description: "Metered: calls", Active: true},
		},
		Prices: []ManagedPrice{
			{
				StripeID: "price_pro_m", YamlID: "pro_monthly",
				ProductStripeID: "prod_pro",
				UnitAmount:      2900, Currency: "usd", Active: true,
				Recurring: &RecurringInfo{Interval: "month", IntervalCount: 1, UsageType: "licensed"},
			},
			{
				StripeID: "price_api_m", YamlID: "api_calls_metered",
				ProductStripeID: "prod_api",
				UnitAmount:      1, Currency: "usd", Active: true,
				Recurring: &RecurringInfo{Interval: "month", IntervalCount: 1, UsageType: "metered", MeterID: "mtr_api"},
			},
		},
		Meters: []ManagedMeter{
			{
				StripeID: "mtr_api", YamlID: "api_calls",
				DisplayName: "API calls", Aggregation: "sum", Status: "active",
			},
		},
	}

	rs := newRecordingStripe(t)
	c, err := NewClient(ClientOptions{
		SecretKey: validKey, ProjectID: testProjectID, BackendURL: rs.server.URL,
	})
	require.NoError(t, err)

	desired := fixtureDesiredFull()
	plan := ComputeDiff(desired, current)
	require.False(t, plan.HasChanges(), "converged state must yield a no-change plan")

	audit := newRecordingAudit()
	results, err := c.ApplyPlan(context.Background(), plan, desired, audit)
	require.NoError(t, err)
	require.Empty(t, results, "no-op plan must produce zero ApplyResults")
	require.Empty(t, audit.entries, "no-op plan must not touch the audit log")
	require.Empty(t, rs.snapshot(), "no-op plan must not hit Stripe")
}

// ---- File-backed audit log tests ------------------------------------------

func TestFileAuditWriter_RoundTripsJSONL(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.log")
	w, err := NewFileAuditWriter(path)
	require.NoError(t, err)
	defer w.Close()

	entries := []AuditEntry{
		{Timestamp: "2026-04-20T10:00:00Z", ProjectID: "p", Resource: "product", Action: "created", YamlID: "pro", StripeID: "prod_1"},
		{Timestamp: "2026-04-20T10:00:01Z", ProjectID: "p", Resource: "price", Action: "created", YamlID: "pro_monthly", StripeID: "price_1"},
	}
	for _, e := range entries {
		require.NoError(t, w.Write(e))
	}

	// Read back — every entry round-trips bit-identical.
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, 2, strings.Count(string(raw), "\n"))

	got, err := ReadAuditLog(path)
	require.NoError(t, err)
	require.Equal(t, entries, got)
}

func TestApplyPlan_PriceReplace_ArchivesThenCreates(t *testing.T) {
	// Plan: one price with a hard-field change (amount) → Replace.
	// Apply expands this into archive(old) + create(new) on Stripe.
	rs := newRecordingStripe(t)
	rs.reply("POST", "/v1/prices/price_old", map[string]any{
		"id": "price_old", "object": "price", "active": false,
		"product": "prod_pro",
	})
	rs.reply("POST", "/v1/prices", map[string]any{
		"id": "price_new", "object": "price", "active": true,
		"unit_amount": 4900, "currency": "usd", "product": "prod_pro",
		"recurring": map[string]any{"interval": "month", "usage_type": "licensed"},
		"metadata": map[string]string{
			metaKeyManaged: "true",
			metaKeyGatrID:  gatrIDFor(testProjectID, "pro_monthly"),
		},
	})
	c, err := NewClient(ClientOptions{
		SecretKey: validKey, ProjectID: testProjectID, BackendURL: rs.server.URL,
	})
	require.NoError(t, err)

	plan := DiffPlan{
		PriceOps: []DiffOp{
			{
				Resource: ResourcePrice, Action: ActionReplaced,
				YamlID: "pro_monthly", StripeID: "price_old",
				Changes: []string{"amount"},
				PriceSpec: &PriceSpec{
					YamlID:     "pro_monthly",
					UnitAmount: 4900,
					Currency:   "usd",
					Active:     true,
					Recurring:  &RecurringInfo{Interval: "month", UsageType: "licensed"},
				},
			},
		},
	}
	desired := DesiredState{
		ProductYamlForPriceYaml: map[string]string{"pro_monthly": "pro"},
		MeterYamlForPriceYaml:   map[string]string{},
	}
	// Seed the product FK so the price can attach.
	plan.ProductOps = []DiffOp{
		{Resource: ResourceProduct, Action: ActionNoOp, YamlID: "pro", StripeID: "prod_pro"},
	}
	audit := newRecordingAudit()
	results, err := c.ApplyPlan(context.Background(), plan, desired, audit)
	require.NoError(t, err)
	require.Len(t, results, 1)
	require.Equal(t, "price_new", results[0].StripeID)

	// Replace = two HTTP calls (archive + create) but ONE DiffOp +
	// ONE audit row — the audit log records logical ops, not API calls.
	reqs := rs.snapshot()
	require.Len(t, reqs, 2)
	require.Equal(t, "/v1/prices/price_old", reqs[0].Path)
	require.Equal(t, "/v1/prices", reqs[1].Path)
	require.Len(t, audit.entries, 1)
	require.Equal(t, "replaced", audit.entries[0].Action)
}

func TestApplyPlan_RequiresProjectID(t *testing.T) {
	c, err := NewClient(ClientOptions{SecretKey: validKey})
	require.NoError(t, err)
	_, err = c.ApplyPlan(context.Background(), DiffPlan{}, DesiredState{}, nil)
	require.Error(t, err)
}

func TestReadAuditLog_MissingFile_ReturnsEmpty(t *testing.T) {
	got, err := ReadAuditLog("/nonexistent/audit.log")
	require.NoError(t, err)
	require.Nil(t, got)
}
