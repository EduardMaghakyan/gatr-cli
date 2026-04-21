package schema

import "fmt"

// Error is the Go counterpart to the TS GatrError. Carries a stable code and
// optional message/path so callers can map back to the canonical error catalog
// at https://gatr.dev/errors/<code>. Any Stripe key in Message/Path is
// redacted on emit.
type Error struct {
	Code    string
	Message string
	Path    string
}

func (e *Error) Error() string {
	if e.Path != "" {
		return fmt.Sprintf("[%s] %s (path=%s)", e.Code, Redact(e.Message), Redact(e.Path))
	}
	return fmt.Sprintf("[%s] %s", e.Code, Redact(e.Message))
}
