package schema

import "regexp"

// stripeKeyRE matches Stripe secret, restricted, webhook signing, and
// publishable keys. Kept intentionally narrow — only common prefixes and
// at least 16 base62 chars — so non-key strings like "pk_test_foo" short
// of the length threshold are left alone.
var stripeKeyRE = regexp.MustCompile(`\b(rk|sk|whsec|pk)_(test|live)_[A-Za-z0-9]{16,}\b`)

// Redact returns s with any recognizable Stripe key replaced by [REDACTED].
// Safe to call on arbitrary strings; pattern is anchored to word boundaries
// so unrelated content is unchanged.
func Redact(s string) string {
	return stripeKeyRE.ReplaceAllString(s, "[REDACTED]")
}
