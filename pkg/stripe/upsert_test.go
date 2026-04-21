package stripe

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

// recordedRequest is one HTTP exchange against the fake Stripe server.
// Tests assert on Method, Path, the parsed form body, and the
// Idempotency-Key header — those four cover every gatr invariant.
type recordedRequest struct {
	Method         string
	Path           string
	Form           url.Values
	IdempotencyKey string
}

// recordingStripe is the shared test fixture: an httptest.Server that
// records every request and serves canned responses keyed on
// (METHOD, path). Per-test setup adds responses; per-test asserts pull
// from the recorded slice.
type recordingStripe struct {
	t      *testing.T
	server *httptest.Server

	mu        sync.Mutex
	recorded  []recordedRequest
	responses map[string][]byte // key = "METHOD path"
	statuses  map[string]int    // key = "METHOD path", default 200
}

func newRecordingStripe(t *testing.T) *recordingStripe {
	t.Helper()
	rs := &recordingStripe{
		t:         t,
		responses: map[string][]byte{},
		statuses:  map[string]int{},
	}
	rs.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		form, _ := url.ParseQuery(string(body))
		rs.mu.Lock()
		rs.recorded = append(rs.recorded, recordedRequest{
			Method:         r.Method,
			Path:           r.URL.Path,
			Form:           form,
			IdempotencyKey: r.Header.Get("Idempotency-Key"),
		})
		key := r.Method + " " + r.URL.Path
		body2, ok := rs.responses[key]
		status := rs.statuses[key]
		rs.mu.Unlock()

		if !ok {
			http.NotFound(w, r)
			return
		}
		if status == 0 {
			status = http.StatusOK
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write(body2)
	}))
	t.Cleanup(rs.server.Close)
	return rs
}

func (rs *recordingStripe) reply(method, path string, payload any) {
	rs.t.Helper()
	b, err := json.Marshal(payload)
	require.NoError(rs.t, err)
	rs.responses[method+" "+path] = b
}

func (rs *recordingStripe) replyStatus(method, path string, status int, payload any) {
	rs.t.Helper()
	rs.reply(method, path, payload)
	rs.statuses[method+" "+path] = status
}

func (rs *recordingStripe) snapshot() []recordedRequest {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	out := make([]recordedRequest, len(rs.recorded))
	copy(out, rs.recorded)
	return out
}

func (rs *recordingStripe) client(t *testing.T) *Client {
	t.Helper()
	c, err := NewClient(ClientOptions{
		SecretKey:  validKey,
		ProjectID:  testProjectID,
		BackendURL: rs.server.URL,
	})
	require.NoError(t, err)
	return c
}

// ----- Test 1: create -------------------------------------------------------

func TestUpsertProduct_Creates(t *testing.T) {
	rs := newRecordingStripe(t)
	rs.reply("POST", "/v1/products", map[string]any{
		"id":          "prod_new",
		"object":      "product",
		"name":        "Pro plan",
		"description": "All features",
		"active":      true,
		"metadata": map[string]string{
			metaKeyManaged: "true",
			metaKeyGatrID:  gatrIDFor(testProjectID, "plan_pro"),
		},
	})

	c := rs.client(t)
	got, action, err := c.UpsertProduct(context.Background(), ProductSpec{
		YamlID:      "plan_pro",
		Name:        "Pro plan",
		Description: "All features",
		Active:      true,
	}, nil)
	require.NoError(t, err)
	require.Equal(t, ActionCreated, action)
	require.Equal(t, "prod_new", got.StripeID)
	require.Equal(t, "plan_pro", got.YamlID)

	reqs := rs.snapshot()
	require.Len(t, reqs, 1)
	require.Equal(t, "POST", reqs[0].Method)
	require.Equal(t, "/v1/products", reqs[0].Path)
	require.Equal(t, "Pro plan", reqs[0].Form.Get("name"))
	// Critical: gatr metadata stamped on every create — operator-owned
	// objects must never be mistakable for gatr-managed.
	require.Equal(t, "true", reqs[0].Form.Get("metadata[gatr_managed]"))
	require.Equal(t, gatrIDFor(testProjectID, "plan_pro"), reqs[0].Form.Get("metadata[gatr_id]"))
}

// ----- Test 2: no-op (current matches spec) ---------------------------------

func TestUpsertProduct_NoOp(t *testing.T) {
	rs := newRecordingStripe(t)
	c := rs.client(t)

	stamped := stampedMetadata(nil, testProjectID, "plan_pro")
	current := &ManagedProduct{
		StripeID:    "prod_existing",
		YamlID:      "plan_pro",
		Name:        "Pro plan",
		Description: "All features",
		Active:      true,
		Metadata:    stamped,
	}
	got, action, err := c.UpsertProduct(context.Background(), ProductSpec{
		YamlID:      "plan_pro",
		Name:        "Pro plan",
		Description: "All features",
		Active:      true,
	}, current)
	require.NoError(t, err)
	require.Equal(t, ActionNoOp, action)
	require.Equal(t, *current, got)
	require.Empty(t, rs.snapshot(), "no-op must not hit Stripe")
}

// ----- Test 3: update (soft change) -----------------------------------------

func TestUpsertProduct_UpdatesChangedField(t *testing.T) {
	rs := newRecordingStripe(t)
	rs.reply("POST", "/v1/products/prod_existing", map[string]any{
		"id":          "prod_existing",
		"object":      "product",
		"name":        "Pro plan v2",
		"description": "All features",
		"active":      true,
		"metadata": map[string]string{
			metaKeyManaged: "true",
			metaKeyGatrID:  gatrIDFor(testProjectID, "plan_pro"),
		},
	})

	current := &ManagedProduct{
		StripeID:    "prod_existing",
		YamlID:      "plan_pro",
		Name:        "Pro plan",
		Description: "All features",
		Active:      true,
		Metadata:    stampedMetadata(nil, testProjectID, "plan_pro"),
	}
	c := rs.client(t)
	_, action, err := c.UpsertProduct(context.Background(), ProductSpec{
		YamlID:      "plan_pro",
		Name:        "Pro plan v2", // only field that changed
		Description: "All features",
		Active:      true,
	}, current)
	require.NoError(t, err)
	require.Equal(t, ActionUpdated, action)

	reqs := rs.snapshot()
	require.Len(t, reqs, 1)
	require.Equal(t, "/v1/products/prod_existing", reqs[0].Path)
	require.Equal(t, "Pro plan v2", reqs[0].Form.Get("name"))
}

// ----- Test 4: archive (never DELETE) ---------------------------------------

func TestArchiveProduct_NeverDeletes(t *testing.T) {
	rs := newRecordingStripe(t)
	rs.reply("POST", "/v1/products/prod_old", map[string]any{
		"id":     "prod_old",
		"object": "product",
		"active": false,
	})
	c := rs.client(t)
	require.NoError(t, c.ArchiveProduct(context.Background(), "prod_old"))

	reqs := rs.snapshot()
	require.Len(t, reqs, 1)
	require.Equal(t, "POST", reqs[0].Method, "archive must POST active=false, never DELETE")
	require.Equal(t, "/v1/products/prod_old", reqs[0].Path)
	require.Equal(t, "false", reqs[0].Form.Get("active"))
}

// ----- Test 5: idempotency-key on every write -------------------------------

func TestUpsert_IdempotencyKeyOnEveryWrite(t *testing.T) {
	rs := newRecordingStripe(t)
	rs.reply("POST", "/v1/products", map[string]any{
		"id": "prod_x", "object": "product", "name": "X", "active": true,
		"metadata": map[string]string{
			metaKeyManaged: "true",
			metaKeyGatrID:  gatrIDFor(testProjectID, "plan_x"),
		},
	})
	rs.reply("POST", "/v1/products/prod_x", map[string]any{
		"id": "prod_x", "object": "product", "name": "X v2", "active": false,
		"metadata": map[string]string{
			metaKeyManaged: "true",
			metaKeyGatrID:  gatrIDFor(testProjectID, "plan_x"),
		},
	})

	c := rs.client(t)
	created, _, err := c.UpsertProduct(context.Background(), ProductSpec{
		YamlID: "plan_x", Name: "X", Active: true,
	}, nil)
	require.NoError(t, err)
	require.NoError(t, c.ArchiveProduct(context.Background(), created.StripeID))

	reqs := rs.snapshot()
	require.Len(t, reqs, 2)
	for _, r := range reqs {
		require.NotEmpty(t, r.IdempotencyKey, "every write must carry Idempotency-Key")
		require.True(t, strings.HasPrefix(r.IdempotencyKey, "gatr_"),
			"key must be gatr-namespaced: %q", r.IdempotencyKey)
	}
	require.NotEqual(t, reqs[0].IdempotencyKey, reqs[1].IdempotencyKey,
		"create vs archive must produce distinct keys (different op)")

	// Re-running an identical create must produce the SAME key — the
	// retry-safety guarantee per Decision in M6+M7 plan.
	keyA := idemKey("create_product", testProjectID, "plan_x", "h1")
	keyB := idemKey("create_product", testProjectID, "plan_x", "h1")
	require.Equal(t, keyA, keyB, "identical inputs must produce identical idempotency keys")
}

// ----- Test 6: list-filter discipline (Upsert can't bypass metadata stamp) -

func TestUpsertProduct_RefusesReservedMetadata(t *testing.T) {
	rs := newRecordingStripe(t)
	c := rs.client(t)

	for _, badKey := range []string{metaKeyManaged, metaKeyGatrID} {
		_, _, err := c.UpsertProduct(context.Background(), ProductSpec{
			YamlID: "plan_x",
			Name:   "X",
			Metadata: map[string]string{
				badKey: "attacker_value",
			},
		}, nil)
		require.Error(t, err, "spec metadata containing %s must be rejected", badKey)
		var sErr *Error
		require.True(t, errors.As(err, &sErr))
		require.Equal(t, ErrCodeApplyFailed, sErr.Code)
		require.Equal(t, badKey, sErr.Details["key"])
	}
	require.Empty(t, rs.snapshot(), "rejection must happen before any HTTP call")
}

// ----- Test 7: scope check ---------------------------------------------------

func TestCheckRestrictedScope_OverScopedAndRestricted(t *testing.T) {
	t.Run("over-scoped: subscriptions list returns 200", func(t *testing.T) {
		rs := newRecordingStripe(t)
		rs.reply("GET", "/v1/subscriptions", map[string]any{
			"object": "list", "data": []any{}, "has_more": false,
		})
		c := rs.client(t)
		over, err := c.CheckRestrictedScope(context.Background())
		require.NoError(t, err)
		require.True(t, over, "200 OK on subscriptions.list → over-scoped")
	})

	t.Run("properly restricted: 403 on subscriptions list", func(t *testing.T) {
		rs := newRecordingStripe(t)
		rs.replyStatus("GET", "/v1/subscriptions", http.StatusForbidden, map[string]any{
			"error": map[string]any{
				"type":    "invalid_request_error",
				"code":    "secret_key_required",
				"message": "Restricted key does not have access to subscriptions",
			},
		})
		c := rs.client(t)
		over, err := c.CheckRestrictedScope(context.Background())
		require.NoError(t, err, "403 means scope is correctly tight, not an error")
		require.False(t, over)
	})

	t.Run("invalid key: 401 propagates", func(t *testing.T) {
		rs := newRecordingStripe(t)
		rs.replyStatus("GET", "/v1/subscriptions", http.StatusUnauthorized, map[string]any{
			"error": map[string]any{
				"type":    "invalid_request_error",
				"code":    "api_key_expired",
				"message": "Your key has expired",
			},
		})
		c := rs.client(t)
		_, err := c.CheckRestrictedScope(context.Background())
		require.Error(t, err, "401 is a real failure, not a scope signal")
		var sErr *Error
		require.True(t, errors.As(err, &sErr))
		require.Equal(t, ErrCodeStripeAPI, sErr.Code)
	})
}

// ----- Bonus coverage: price + meter immutability guards --------------------

func TestUpsertPrice_RefusesHardFieldChange(t *testing.T) {
	rs := newRecordingStripe(t)
	c := rs.client(t)
	current := &ManagedPrice{
		StripeID:        "price_existing",
		YamlID:          "plan_pro_monthly",
		ProductStripeID: "prod_pro",
		UnitAmount:      1500,
		Currency:        "usd",
		Active:          true,
		Metadata:        stampedMetadata(nil, testProjectID, "plan_pro_monthly"),
		Recurring: &RecurringInfo{
			Interval: "month", IntervalCount: 1, UsageType: "licensed",
		},
	}
	_, _, err := c.UpsertPrice(context.Background(), PriceSpec{
		YamlID:          "plan_pro_monthly",
		ProductStripeID: "prod_pro",
		UnitAmount:      1900, // changed!
		Currency:        "usd",
		Active:          true,
		Recurring: &RecurringInfo{
			Interval: "month", UsageType: "licensed",
		},
	}, current)
	require.Error(t, err)
	var sErr *Error
	require.True(t, errors.As(err, &sErr))
	require.Equal(t, ErrCodeApplyFailed, sErr.Code)
	require.Empty(t, rs.snapshot(), "must reject before HTTP — the diff engine handles archive+recreate")
}

func TestUpsertPrice_CreatesAndStampsMetadata(t *testing.T) {
	rs := newRecordingStripe(t)
	rs.reply("POST", "/v1/prices", map[string]any{
		"id":          "price_new",
		"object":      "price",
		"product":     "prod_pro",
		"unit_amount": int64(1500),
		"currency":    "usd",
		"active":      true,
		"recurring": map[string]any{
			"interval":   "month",
			"usage_type": "licensed",
		},
		"metadata": map[string]string{
			metaKeyManaged: "true",
			metaKeyGatrID:  gatrIDFor(testProjectID, "plan_pro_monthly"),
		},
	})
	c := rs.client(t)
	_, action, err := c.UpsertPrice(context.Background(), PriceSpec{
		YamlID:          "plan_pro_monthly",
		ProductStripeID: "prod_pro",
		UnitAmount:      1500,
		Currency:        "usd",
		Active:          true,
		Recurring: &RecurringInfo{
			Interval: "month", UsageType: "licensed",
		},
	}, nil)
	require.NoError(t, err)
	require.Equal(t, ActionCreated, action)
	reqs := rs.snapshot()
	require.Len(t, reqs, 1)
	require.Equal(t, "1500", reqs[0].Form.Get("unit_amount"))
	require.Equal(t, "month", reqs[0].Form.Get("recurring[interval]"))
	require.Equal(t, "true", reqs[0].Form.Get("metadata[gatr_managed]"))
}

func TestUpsertMeter_CreatesWithEventNameAndAggregation(t *testing.T) {
	rs := newRecordingStripe(t)
	rs.reply("POST", "/v1/billing/meters", map[string]any{
		"id":           "mtr_new",
		"object":       "billing.meter",
		"display_name": "API calls",
		"event_name":   meterEventNameFor(testProjectID, "api_calls"),
		"status":       "active",
		"default_aggregation": map[string]any{
			"formula": "sum",
		},
	})
	c := rs.client(t)
	got, action, err := c.UpsertMeter(context.Background(), MeterSpec{
		YamlID:      "api_calls",
		DisplayName: "API calls",
		Aggregation: "sum",
	}, nil)
	require.NoError(t, err)
	require.Equal(t, ActionCreated, action)
	require.Equal(t, "api_calls", got.YamlID)

	reqs := rs.snapshot()
	require.Len(t, reqs, 1)
	require.Equal(t, meterEventNameFor(testProjectID, "api_calls"), reqs[0].Form.Get("event_name"))
	require.Equal(t, "sum", reqs[0].Form.Get("default_aggregation[formula]"))
}

// ----- Coverage backfill: noop / soft update / archive / immutability ------

func TestUpsertPrice_NoOp(t *testing.T) {
	rs := newRecordingStripe(t)
	c := rs.client(t)
	stamped := stampedMetadata(nil, testProjectID, "plan_pro_monthly")
	current := &ManagedPrice{
		StripeID:        "price_existing",
		YamlID:          "plan_pro_monthly",
		ProductStripeID: "prod_pro",
		UnitAmount:      1500,
		Currency:        "usd",
		Active:          true,
		Metadata:        stamped,
		Recurring: &RecurringInfo{
			Interval: "month", IntervalCount: 1, UsageType: "licensed",
		},
	}
	_, action, err := c.UpsertPrice(context.Background(), PriceSpec{
		YamlID:          "plan_pro_monthly",
		ProductStripeID: "prod_pro",
		UnitAmount:      1500,
		Currency:        "usd",
		Active:          true,
		Recurring: &RecurringInfo{
			Interval: "month", UsageType: "licensed", // IntervalCount=0 normalises to 1
		},
	}, current)
	require.NoError(t, err)
	require.Equal(t, ActionNoOp, action)
	require.Empty(t, rs.snapshot())
}

func TestUpsertPrice_SoftUpdate_OnlySendsActiveAndMetadata(t *testing.T) {
	rs := newRecordingStripe(t)
	rs.reply("POST", "/v1/prices/price_existing", map[string]any{
		"id": "price_existing", "object": "price",
		"product": "prod_pro", "unit_amount": int64(1500), "currency": "usd",
		"active": false,
		"recurring": map[string]any{"interval": "month", "usage_type": "licensed"},
		"metadata": map[string]string{
			metaKeyManaged: "true",
			metaKeyGatrID:  gatrIDFor(testProjectID, "plan_pro_monthly"),
		},
	})
	c := rs.client(t)
	current := &ManagedPrice{
		StripeID: "price_existing", YamlID: "plan_pro_monthly",
		ProductStripeID: "prod_pro", UnitAmount: 1500, Currency: "usd",
		Active:   true,
		Metadata: stampedMetadata(nil, testProjectID, "plan_pro_monthly"),
		Recurring: &RecurringInfo{
			Interval: "month", IntervalCount: 1, UsageType: "licensed",
		},
	}
	_, action, err := c.UpsertPrice(context.Background(), PriceSpec{
		YamlID: "plan_pro_monthly", ProductStripeID: "prod_pro",
		UnitAmount: 1500, Currency: "usd",
		Active: false, // only soft change
		Recurring: &RecurringInfo{
			Interval: "month", UsageType: "licensed",
		},
	}, current)
	require.NoError(t, err)
	require.Equal(t, ActionUpdated, action)
	reqs := rs.snapshot()
	require.Len(t, reqs, 1)
	// Hard fields must NOT be in the body — Stripe would reject them.
	require.Empty(t, reqs[0].Form.Get("unit_amount"))
	require.Empty(t, reqs[0].Form.Get("currency"))
	require.Equal(t, "false", reqs[0].Form.Get("active"))
}

func TestUpsertPrice_HardFieldVariants(t *testing.T) {
	c, err := NewClient(ClientOptions{SecretKey: validKey, ProjectID: testProjectID})
	require.NoError(t, err)
	base := &ManagedPrice{
		StripeID:        "price_x",
		YamlID:          "p",
		ProductStripeID: "prod_a",
		UnitAmount:      100,
		Currency:        "usd",
		Active:          true,
		Metadata:        stampedMetadata(nil, testProjectID, "p"),
		Recurring: &RecurringInfo{
			Interval: "month", IntervalCount: 1, UsageType: "licensed",
		},
	}
	mkSpec := func() PriceSpec {
		return PriceSpec{
			YamlID:          "p",
			ProductStripeID: "prod_a",
			UnitAmount:      100,
			Currency:        "usd",
			Active:          true,
			Recurring: &RecurringInfo{
				Interval: "month", UsageType: "licensed",
			},
		}
	}
	cases := map[string]func(s *PriceSpec){
		"currency change":       func(s *PriceSpec) { s.Currency = "eur" },
		"product change":        func(s *PriceSpec) { s.ProductStripeID = "prod_b" },
		"interval change":       func(s *PriceSpec) { s.Recurring.Interval = "year" },
		"interval-count change": func(s *PriceSpec) { s.Recurring.IntervalCount = 2 },
		"usage_type change":     func(s *PriceSpec) { s.Recurring.UsageType = "metered" },
		"meter change":          func(s *PriceSpec) { s.Recurring.MeterID = "mtr_x" },
		"recurring → one-time":  func(s *PriceSpec) { s.Recurring = nil },
	}
	for name, mut := range cases {
		t.Run(name, func(t *testing.T) {
			spec := mkSpec()
			mut(&spec)
			_, _, err := c.UpsertPrice(context.Background(), spec, base)
			require.Error(t, err, "%s should trigger hard-field rejection", name)
		})
	}
}

func TestArchivePrice_PostsActiveFalse(t *testing.T) {
	rs := newRecordingStripe(t)
	rs.reply("POST", "/v1/prices/price_old", map[string]any{
		"id": "price_old", "object": "price", "active": false,
	})
	c := rs.client(t)
	require.NoError(t, c.ArchivePrice(context.Background(), "price_old"))

	reqs := rs.snapshot()
	require.Len(t, reqs, 1)
	require.Equal(t, "POST", reqs[0].Method)
	require.Equal(t, "/v1/prices/price_old", reqs[0].Path)
	require.Equal(t, "false", reqs[0].Form.Get("active"))
	require.NotEmpty(t, reqs[0].IdempotencyKey)
}

func TestUpsertMeter_NoOpAndSoftUpdate(t *testing.T) {
	rs := newRecordingStripe(t)
	c := rs.client(t)
	en := meterEventNameFor(testProjectID, "api_calls")
	current := &ManagedMeter{
		StripeID: "mtr_x", YamlID: "api_calls",
		DisplayName: "API calls", EventName: en, Aggregation: "sum",
	}

	// No-op: same display_name.
	_, action, err := c.UpsertMeter(context.Background(), MeterSpec{
		YamlID: "api_calls", DisplayName: "API calls", Aggregation: "sum",
	}, current)
	require.NoError(t, err)
	require.Equal(t, ActionNoOp, action)
	require.Empty(t, rs.snapshot())

	// Soft update: only display_name changes.
	rs.reply("POST", "/v1/billing/meters/mtr_x", map[string]any{
		"id": "mtr_x", "object": "billing.meter",
		"display_name": "API requests", "event_name": en, "status": "active",
		"default_aggregation": map[string]any{"formula": "sum"},
	})
	_, action, err = c.UpsertMeter(context.Background(), MeterSpec{
		YamlID: "api_calls", DisplayName: "API requests", Aggregation: "sum",
	}, current)
	require.NoError(t, err)
	require.Equal(t, ActionUpdated, action)

	reqs := rs.snapshot()
	require.Len(t, reqs, 1)
	require.Equal(t, "API requests", reqs[0].Form.Get("display_name"))
	// event_name + aggregation must NOT be in the update body — Stripe
	// rejects attempts to mutate those.
	require.Empty(t, reqs[0].Form.Get("event_name"))
}

func TestUpsertMeter_RequiresAggregationOnCreate(t *testing.T) {
	c, err := NewClient(ClientOptions{SecretKey: validKey, ProjectID: testProjectID})
	require.NoError(t, err)
	_, _, err = c.UpsertMeter(context.Background(), MeterSpec{
		YamlID: "api_calls", DisplayName: "API",
		// Aggregation deliberately missing
	}, nil)
	require.Error(t, err)
}

func TestDeactivateMeter_PostsToDeactivateEndpoint(t *testing.T) {
	rs := newRecordingStripe(t)
	rs.reply("POST", "/v1/billing/meters/mtr_dead/deactivate", map[string]any{
		"id": "mtr_dead", "object": "billing.meter", "status": "inactive",
	})
	c := rs.client(t)
	require.NoError(t, c.DeactivateMeter(context.Background(), "mtr_dead"))

	reqs := rs.snapshot()
	require.Len(t, reqs, 1)
	require.Equal(t, "POST", reqs[0].Method)
	require.Equal(t, "/v1/billing/meters/mtr_dead/deactivate", reqs[0].Path)
	require.NotEmpty(t, reqs[0].IdempotencyKey)
}

func TestUpsert_RequiresProjectID(t *testing.T) {
	c, err := NewClient(ClientOptions{SecretKey: validKey}) // no ProjectID
	require.NoError(t, err)

	_, _, err = c.UpsertProduct(context.Background(), ProductSpec{YamlID: "x"}, nil)
	require.Error(t, err)
	_, _, err = c.UpsertPrice(context.Background(), PriceSpec{YamlID: "x", ProductStripeID: "prod_a"}, nil)
	require.Error(t, err)
	_, _, err = c.UpsertMeter(context.Background(), MeterSpec{YamlID: "x"}, nil)
	require.Error(t, err)
}

func TestUpsertMeter_RejectsAggregationDrift(t *testing.T) {
	rs := newRecordingStripe(t)
	c := rs.client(t)
	en := meterEventNameFor(testProjectID, "api_calls")
	current := &ManagedMeter{
		StripeID:    "mtr_existing",
		YamlID:      "api_calls",
		DisplayName: "API calls",
		EventName:   en,
		Aggregation: "sum",
	}
	_, _, err := c.UpsertMeter(context.Background(), MeterSpec{
		YamlID:      "api_calls",
		DisplayName: "API calls",
		Aggregation: "count", // immutable on Stripe
	}, current)
	require.Error(t, err)
	require.Empty(t, rs.snapshot())
}
