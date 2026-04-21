package stripe

import (
	"context"
	"errors"

	stripesdk "github.com/stripe/stripe-go/v82"
)

// CheckRestrictedScope reports whether the active key has MORE
// permissions than `gatr push` actually needs. The probe is a one-row
// `subscriptions.list` — gatr push reads/writes products, prices, and
// meters, but never subscriptions, so a successful response signals
// over-scope.
//
// Returns:
//   • overScoped=true   — key is a full secret (sk_live / sk_test) or
//                         a restricted key with subscriptions:read.
//                         CLI emits a yellow warning suggesting the
//                         user mint a tighter restricted key.
//   • overScoped=false  — Stripe rejected the probe with permission_error
//                         (the key IS scoped tightly — good).
//   • err != nil        — anything else (network, API key invalid,
//                         unexpected status). Caller decides whether
//                         to fail the push or continue.
func (c *Client) CheckRestrictedScope(ctx context.Context) (overScoped bool, err error) {
	params := &stripesdk.SubscriptionListParams{}
	params.Limit = stripesdk.Int64(1)
	params.Context = ctx

	iter := c.sc.Subscriptions.List(params)
	// We only need to know whether the request was authorised. .Next()
	// triggers the underlying HTTP call; a permission error surfaces
	// via .Err() with no items yielded.
	_ = iter.Next()
	if iterErr := iter.Err(); iterErr != nil {
		var serr *stripesdk.Error
		// Stripe returns 403 with type=invalid_request_error when a
		// restricted key lacks a scope. 401 means the key itself is
		// invalid/expired — that's a real error, not a "scope OK"
		// signal. Discriminate on HTTPStatusCode rather than Code so
		// we don't get bitten by Stripe renaming error codes.
		if errors.As(iterErr, &serr) && serr.HTTPStatusCode == 403 {
			return false, nil
		}
		return false, wrapStripeAPI(iterErr, "scope check")
	}
	return true, nil
}
