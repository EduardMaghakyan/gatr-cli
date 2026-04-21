// Package stripe is the gatr-specific wrapper around the Stripe Go SDK.
// It is NOT a general-purpose Stripe client — it encodes the gatr
// conventions (gatr_managed=true, gatr_id=<project>:<yaml_id> metadata,
// idempotency-keyed writes, list-filter-by-metadata reads).
//
// The package is published as its own Go module so the CLI can vendor
// or fork it independently of the rest of the gatr ecosystem — see the
// M6+M7 plan Decision #11.
package stripe

import (
	"errors"
	"fmt"
)

// Error codes for the Stripe wrapper. Mirrors the E5xx block reserved
// for the CLI in docs/plan-v1.md; server-side webhook errors live in
// cmd/server/internal/server/errors.go under E100+.
const (
	// ErrCodeStripeAPI is returned when a Stripe API call fails. The
	// original Stripe error code (e.g. "invalid_request_error") is
	// propagated through the Details["stripe_code"] field.
	ErrCodeStripeAPI = "E500"

	// ErrCodeMissingCredentials is returned when no usable secret key
	// could be resolved from opts, env, or credfile — OR when a key
	// was resolved but is malformed. Details["reason"] disambiguates
	// ("missing" vs "malformed").
	ErrCodeMissingCredentials = "E501"

	// ErrCodeMissingProjectID is returned when neither --project-id,
	// the gatr.yaml top-level project_id, nor ~/.gatr/project_id is set.
	// Reserved for gatr push; the wrapper surfaces it for consistency.
	ErrCodeMissingProjectID = "E502"

	// ErrCodeGatrIDCollision is returned when two gatr.yaml objects
	// resolve to the same gatr_id metadata — guards against silent
	// data loss on push.
	ErrCodeGatrIDCollision = "E503"

	// ErrCodeApplyFailed is returned by gatr push when the diff apply
	// fails mid-flight. Partial state is recorded in ~/.gatr/audit.log
	// so a retry can complete safely.
	ErrCodeApplyFailed = "E504"

	// ErrCodeDirtyWorktree is returned when --auto-patch would
	// overwrite uncommitted edits in gatr.yaml. Override with --force.
	ErrCodeDirtyWorktree = "E505"
)

// Error is the wrapper's typed error. It carries a stable code plus an
// optional Details map so callers (CLI / server) can render structured
// output without inspecting wrapped Stripe errors.
//
// The embedded `cause` is for logging only — it is never serialized to
// a user-facing response (see pkg/schema.Redact).
type Error struct {
	Code    string
	Message string
	Details map[string]any
	cause   error
}

// Error renders the code + human-readable message. Stripe keys in the
// message are redacted via pkg/schema.Redact before returning — callers
// should not need to redact again.
func (e *Error) Error() string {
	if e.Code == "" {
		return e.Message
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

// Unwrap exposes the underlying cause for errors.Is / errors.As chains.
// The cause is deliberately NOT included in Error() output — serializing
// raw Stripe error bodies risks leaking keys or internal paths.
func (e *Error) Unwrap() error { return e.cause }

// newError is the internal constructor. Prefer the named helpers below.
func newError(code, msg string, cause error, details map[string]any) *Error {
	return &Error{Code: code, Message: msg, Details: details, cause: cause}
}

// ErrMissingCredentials returns an E501 error tagged with a reason.
// Reason should be one of: "missing", "malformed".
func ErrMissingCredentials(reason, msg string) *Error {
	return newError(ErrCodeMissingCredentials, msg, nil, map[string]any{"reason": reason})
}

// ErrStripeAPI wraps a stripe-go error with E500. The Stripe error code
// (from stripe.Error.Code) is lifted into Details["stripe_code"] when
// available.
func ErrStripeAPI(cause error, stripeCode, msg string) *Error {
	details := map[string]any{}
	if stripeCode != "" {
		details["stripe_code"] = stripeCode
	}
	return newError(ErrCodeStripeAPI, msg, cause, details)
}

// ErrMissingProjectID returns an E502 error. Surfaced when a method
// that requires a project-scoped client (e.g. List/Upsert) is called
// against a Client constructed without ClientOptions.ProjectID.
func ErrMissingProjectID(msg string) *Error {
	return newError(ErrCodeMissingProjectID, msg, nil, nil)
}

// IsMissingCredentials reports whether err is an E501 error.
// Convenience for callers that want to differentiate credential
// problems from network/API problems without typed assertions.
func IsMissingCredentials(err error) bool {
	var e *Error
	return errors.As(err, &e) && e.Code == ErrCodeMissingCredentials
}
