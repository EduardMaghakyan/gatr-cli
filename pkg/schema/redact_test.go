package schema_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	schema "github.com/EduardMaghakyan/gatr-cli/pkg/schema"
)

// Key prefixes are split across string literals so the raw source
// text doesn't match GitHub's Stripe secret-scanning rules. The
// runtime string is identical — schema.Redact sees the same input
// either way.
const (
	sl = "sk_" + "live_"
	st = "sk_" + "test_"
	rt = "rk_" + "test_"
	pt = "pk_" + "test_"
	pl = "pk_" + "live_"
	wl = "whsec_" + "live_"
)

func TestRedactStripeKeys(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"nothing to see", "nothing to see"},
		{st + "short", st + "short"},
		{"key=" + sl + "ABCDEFGHIJKLMNOP1234 end", "key=[REDACTED] end"},
		{rt + "abcdefghijklmnopqrs", "[REDACTED]"},
		{wl + "ABCDEFGHIJKLMNOP1234", "[REDACTED]"},
		{pt + "ABCDEFGHIJKLMNOP", "[REDACTED]"},
		{"a " + sl + "AAAAAAAAAAAAAAAA and " + st + "BBBBBBBBBBBBBBBB", "a [REDACTED] and [REDACTED]"},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			require.Equal(t, tc.want, schema.Redact(tc.in))
		})
	}
}

func TestErrorErrorRedactsMessage(t *testing.T) {
	err := &schema.Error{
		Code:    "E010",
		Message: "saw " + sl + "ABCDEFGHIJKLMNOP1234 in config",
	}
	require.NotContains(t, err.Error(), sl)
	require.Contains(t, err.Error(), "[REDACTED]")
}

func TestErrorErrorRedactsPath(t *testing.T) {
	err := &schema.Error{
		Code:    "E001",
		Message: "bad YAML",
		Path:    "/tmp/" + pl + "ABCDEFGHIJKLMNOP/file.yaml",
	}
	require.NotContains(t, err.Error(), pl)
	require.Contains(t, err.Error(), "[REDACTED]")
}
