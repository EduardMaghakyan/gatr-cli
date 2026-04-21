package stripe

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
)

// testProjectID is a UUID-shaped string used across list tests. The
// hex form (no hyphens) is what meter event_names embed.
const testProjectID = "550e8400-e29b-41d4-a716-446655440000"

// fakeStripe is a minimal stand-in for api.stripe.com used by list
// tests. It serves precomputed JSON for the four list endpoints we
// care about and records hit counts so paging can be verified.
type fakeStripe struct {
	server   *httptest.Server
	requests int32 // atomic, total requests received

	// Per-endpoint payloads, indexed by URL path. The handler returns
	// payload[path] verbatim. Tests assemble these maps in the helper.
	payloads map[string][]byte

	// Optional per-path response status. Default 200.
	statuses map[string]int
}

func newFakeStripe(t *testing.T) *fakeStripe {
	t.Helper()
	fs := &fakeStripe{
		payloads: map[string][]byte{},
		statuses: map[string]int{},
	}
	fs.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&fs.requests, 1)
		path := r.URL.Path
		body, ok := fs.payloads[path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		status := fs.statuses[path]
		if status == 0 {
			status = http.StatusOK
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write(body)
	}))
	t.Cleanup(fs.server.Close)
	return fs
}

func (fs *fakeStripe) clientFor(t *testing.T, projectID string) *Client {
	t.Helper()
	c, err := NewClient(ClientOptions{
		SecretKey:  validKey,
		ProjectID:  projectID,
		BackendURL: fs.server.URL,
	})
	require.NoError(t, err)
	return c
}

// listResp builds a Stripe-shape list response.
func listResp(t *testing.T, items []map[string]any) []byte {
	t.Helper()
	b, err := json.Marshal(map[string]any{
		"object":   "list",
		"data":     items,
		"has_more": false,
		"url":      "/v1/anything",
	})
	require.NoError(t, err)
	return b
}

// productPayload builds a single product object for the fake server.
func productPayload(id, name string, active bool, metadata map[string]string) map[string]any {
	if metadata == nil {
		metadata = map[string]string{}
	}
	return map[string]any{
		"id":          id,
		"object":      "product",
		"name":        name,
		"description": "",
		"active":      active,
		"metadata":    metadata,
	}
}

func TestListManagedProducts_FiltersByMetadata(t *testing.T) {
	fs := newFakeStripe(t)
	other := "11111111-2222-3333-4444-555555555555"
	fs.payloads["/v1/products"] = listResp(t, []map[string]any{
		// gatr-managed in our project
		productPayload("prod_kept", "Pro plan", true, map[string]string{
			metaKeyManaged: "true",
			metaKeyGatrID:  gatrIDFor(testProjectID, "plan_pro"),
		}),
		// gatr-managed but a different project on the same Stripe account
		productPayload("prod_otherproj", "Other Project", true, map[string]string{
			metaKeyManaged: "true",
			metaKeyGatrID:  gatrIDFor(other, "plan_pro"),
		}),
		// operator-owned (no gatr metadata at all)
		productPayload("prod_operator", "Hand-made widget", true, nil),
		// gatr_managed=true but no namespaced gatr_id (defensive — should drop)
		productPayload("prod_partial", "Partial", true, map[string]string{
			metaKeyManaged: "true",
		}),
		// archived gatr-managed price — must still appear
		productPayload("prod_archived", "Old plan", false, map[string]string{
			metaKeyManaged: "true",
			metaKeyGatrID:  gatrIDFor(testProjectID, "plan_legacy"),
		}),
	})

	c := fs.clientFor(t, testProjectID)
	got, err := c.ListManagedProducts(context.Background())
	require.NoError(t, err)
	require.Len(t, got, 2, "only the two project-scoped managed products should survive")

	byYamlID := map[string]ManagedProduct{}
	for _, p := range got {
		byYamlID[p.YamlID] = p
	}
	require.Equal(t, "prod_kept", byYamlID["plan_pro"].StripeID)
	require.True(t, byYamlID["plan_pro"].Active)
	require.Equal(t, "prod_archived", byYamlID["plan_legacy"].StripeID)
	require.False(t, byYamlID["plan_legacy"].Active, "archived must round-trip")
}

func TestListManagedProducts_RequiresProjectID(t *testing.T) {
	c, err := NewClient(ClientOptions{SecretKey: validKey})
	require.NoError(t, err)
	_, err = c.ListManagedProducts(context.Background())
	require.Error(t, err)
	var sErr *Error
	require.True(t, errors.As(err, &sErr))
	require.Equal(t, ErrCodeMissingProjectID, sErr.Code)
}

func TestListManagedProducts_WrapsAPIError(t *testing.T) {
	fs := newFakeStripe(t)
	fs.payloads["/v1/products"] = []byte(`{
		"error": {
			"type": "invalid_request_error",
			"code": "api_key_expired",
			"message": "Your key has expired"
		}
	}`)
	fs.statuses["/v1/products"] = http.StatusUnauthorized

	c := fs.clientFor(t, testProjectID)
	_, err := c.ListManagedProducts(context.Background())
	require.Error(t, err)

	var sErr *Error
	require.True(t, errors.As(err, &sErr))
	require.Equal(t, ErrCodeStripeAPI, sErr.Code)
	require.Equal(t, "api_key_expired", sErr.Details["stripe_code"])
	require.Contains(t, sErr.Message, "list products")
}

func TestListManagedPrices_ProjectsRecurringAndOneTime(t *testing.T) {
	fs := newFakeStripe(t)
	fs.payloads["/v1/prices"] = listResp(t, []map[string]any{
		// recurring licensed (per-seat)
		{
			"id":          "price_seat",
			"object":      "price",
			"product":     "prod_team",
			"unit_amount": int64(1500),
			"currency":    "usd",
			"active":      true,
			"recurring": map[string]any{
				"interval":       "month",
				"interval_count": int64(1),
				"usage_type":     "licensed",
			},
			"metadata": map[string]string{
				metaKeyManaged: "true",
				metaKeyGatrID:  gatrIDFor(testProjectID, "team_seat"),
			},
		},
		// recurring metered
		{
			"id":          "price_meter",
			"object":      "price",
			"product":     "prod_api",
			"unit_amount": int64(0),
			"currency":    "usd",
			"active":      true,
			"recurring": map[string]any{
				"interval":   "month",
				"usage_type": "metered",
				"meter":      "mtr_abc",
			},
			"metadata": map[string]string{
				metaKeyManaged: "true",
				metaKeyGatrID:  gatrIDFor(testProjectID, "api_calls"),
			},
		},
		// one-time price (no recurring)
		{
			"id":          "price_onetime",
			"object":      "price",
			"product":     "prod_addon",
			"unit_amount": int64(2000),
			"currency":    "usd",
			"active":      true,
			"metadata": map[string]string{
				metaKeyManaged: "true",
				metaKeyGatrID:  gatrIDFor(testProjectID, "credit_pack_500"),
			},
		},
		// operator-owned — must be dropped
		{
			"id":          "price_outsider",
			"object":      "price",
			"product":     "prod_other",
			"unit_amount": int64(99),
			"currency":    "usd",
			"active":      true,
			"metadata":    map[string]string{},
		},
	})

	c := fs.clientFor(t, testProjectID)
	got, err := c.ListManagedPrices(context.Background())
	require.NoError(t, err)
	require.Len(t, got, 3)

	byYaml := map[string]ManagedPrice{}
	for _, p := range got {
		byYaml[p.YamlID] = p
	}

	seat := byYaml["team_seat"]
	require.Equal(t, "prod_team", seat.ProductStripeID)
	require.NotNil(t, seat.Recurring)
	require.Equal(t, "month", seat.Recurring.Interval)
	require.Equal(t, "licensed", seat.Recurring.UsageType)
	require.Empty(t, seat.Recurring.MeterID)

	metered := byYaml["api_calls"]
	require.NotNil(t, metered.Recurring)
	require.Equal(t, "metered", metered.Recurring.UsageType)
	require.Equal(t, "mtr_abc", metered.Recurring.MeterID)

	onetime := byYaml["credit_pack_500"]
	require.Nil(t, onetime.Recurring, "one-time price must surface Recurring=nil")
	require.EqualValues(t, 2000, onetime.UnitAmount)
}

func TestListManagedMeters_FiltersByEventNamePrefix(t *testing.T) {
	fs := newFakeStripe(t)
	other := "11111111-2222-3333-4444-555555555555"
	fs.payloads["/v1/billing/meters"] = listResp(t, []map[string]any{
		{
			"id":           "mtr_1",
			"object":       "billing.meter",
			"display_name": "API calls",
			"event_name":   meterEventNameFor(testProjectID, "api_calls"),
			"status":       "active",
			"default_aggregation": map[string]string{
				"formula": "sum",
			},
		},
		{
			"id":           "mtr_other",
			"object":       "billing.meter",
			"display_name": "From a different project",
			"event_name":   meterEventNameFor(other, "api_calls"),
			"status":       "active",
		},
		{
			"id":           "mtr_operator",
			"object":       "billing.meter",
			"display_name": "Hand-made by the operator",
			"event_name":   "raw_event_name",
			"status":       "active",
		},
		{
			"id":           "mtr_inactive",
			"object":       "billing.meter",
			"display_name": "Deactivated meter",
			"event_name":   meterEventNameFor(testProjectID, "old_meter"),
			"status":       "inactive",
		},
	})

	c := fs.clientFor(t, testProjectID)
	got, err := c.ListManagedMeters(context.Background())
	require.NoError(t, err)
	require.Len(t, got, 2)

	byYaml := map[string]ManagedMeter{}
	for _, m := range got {
		byYaml[m.YamlID] = m
	}
	require.Equal(t, "sum", byYaml["api_calls"].Aggregation)
	require.Equal(t, "active", byYaml["api_calls"].Status)
	require.Equal(t, "inactive", byYaml["old_meter"].Status)
	// Defensive: confirm the event_name we parsed back round-trips.
	require.True(t, strings.HasPrefix(byYaml["api_calls"].EventName, "gatr_"))
}

func TestListManagedMeters_RequiresProjectID(t *testing.T) {
	c, err := NewClient(ClientOptions{SecretKey: validKey})
	require.NoError(t, err)
	_, err = c.ListManagedMeters(context.Background())
	require.Error(t, err)
	require.True(t, errors.As(err, new(*Error)))
}

func TestListManagedProducts_FollowsPagingCursor(t *testing.T) {
	// Stand up a custom fake that returns has_more=true on the first
	// hit and has_more=false on subsequent ones. The iterator uses
	// starting_after to walk; we verify both pages flow through.
	const projectID = testProjectID
	page1 := []map[string]any{
		productPayload("prod_a", "A", true, map[string]string{
			metaKeyManaged: "true",
			metaKeyGatrID:  gatrIDFor(projectID, "yaml_a"),
		}),
	}
	page2 := []map[string]any{
		productPayload("prod_b", "B", true, map[string]string{
			metaKeyManaged: "true",
			metaKeyGatrID:  gatrIDFor(projectID, "yaml_b"),
		}),
	}
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&hits, 1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		var data []map[string]any
		hasMore := false
		switch n {
		case 1:
			data = page1
			hasMore = true
		default:
			data = page2
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"object":   "list",
			"data":     data,
			"has_more": hasMore,
			"url":      r.URL.Path,
		})
	}))
	defer srv.Close()

	c, err := NewClient(ClientOptions{
		SecretKey:  validKey,
		ProjectID:  projectID,
		BackendURL: srv.URL,
	})
	require.NoError(t, err)

	got, err := c.ListManagedProducts(context.Background())
	require.NoError(t, err)
	require.Len(t, got, 2, "iterator must follow has_more=true onto page 2")
	require.Equal(t, int32(2), atomic.LoadInt32(&hits), "iterator must make 2 HTTP requests")
}

func TestWrapStripeAPI_NonStripeError(t *testing.T) {
	// Network failure → not a *stripe.Error, but still wraps cleanly
	// with E500 + empty stripe_code.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hj, ok := w.(http.Hijacker)
		require.True(t, ok)
		conn, _, err := hj.Hijack()
		require.NoError(t, err)
		conn.Close() // abrupt close; client sees a network error
	}))
	defer srv.Close()

	c, err := NewClient(ClientOptions{
		SecretKey:  validKey,
		ProjectID:  testProjectID,
		BackendURL: srv.URL,
	})
	require.NoError(t, err)

	_, err = c.ListManagedProducts(context.Background())
	require.Error(t, err)
	var sErr *Error
	require.True(t, errors.As(err, &sErr))
	require.Equal(t, ErrCodeStripeAPI, sErr.Code)
	_, present := sErr.Details["stripe_code"]
	require.False(t, present, "non-stripe-error must omit stripe_code")
}

func TestNamespace_RoundTrip(t *testing.T) {
	// gatr_id round-trip
	v := gatrIDFor(testProjectID, "plan_pro")
	gotYaml, ok := parseGatrID(v, testProjectID)
	require.True(t, ok)
	require.Equal(t, "plan_pro", gotYaml)

	// Wrong project → not ok
	_, ok = parseGatrID(v, "00000000-0000-0000-0000-000000000000")
	require.False(t, ok)

	// Meter event_name round-trip; hex form drops hyphens
	en := meterEventNameFor(testProjectID, "api_calls")
	require.Equal(t, "gatr_550e8400e29b41d4a716446655440000_api_calls", en)
	yaml, ok := parseMeterEventName(en, testProjectID)
	require.True(t, ok)
	require.Equal(t, "api_calls", yaml)

	// Random event_name → not ok
	_, ok = parseMeterEventName("api_calls", testProjectID)
	require.False(t, ok)
}
