package stripe

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"

	"github.com/BurntSushi/toml"
	"github.com/EduardMaghakyan/gatr-cli/pkg/schema"
)

// CredentialSource records WHERE a resolved key came from. Surfaced on
// Client and in the resolved-credential struct so the CLI can render
// "using key from $STRIPE_SECRET_KEY" diagnostics without re-resolving.
type CredentialSource string

const (
	CredSourceUnknown CredentialSource = ""
	CredSourceOptions CredentialSource = "options"   // ClientOptions.SecretKey set explicitly
	CredSourceEnv     CredentialSource = "env"       // STRIPE_SECRET_KEY env var
	CredSourceFile    CredentialSource = "credfile"  // ~/.gatr/credentials.toml
)

// envSecretKey is the environment variable consulted by ResolveSecretKey.
// Spelled out so tests can swap it via t.Setenv without magic strings.
const envSecretKey = "STRIPE_SECRET_KEY"

// secretKeyRE matches a Stripe SECRET key — sk_ (full secret) or rk_
// (restricted). Publishable (pk_) and webhook signing (whsec_) keys are
// deliberately rejected: the wrapper performs write operations, and pk_
// can't write while whsec_ belongs to a different code path entirely.
//
// The 16-char floor matches schema.Redact's regex so "looks like a key
// to the redactor" and "passes the wrapper's validator" stay aligned.
var secretKeyRE = regexp.MustCompile(`^(sk|rk)_(test|live)_[A-Za-z0-9]{16,}$`)

// ResolvedCredential is the output of ResolveSecretKey. Path is set
// only for CredSourceFile.
type ResolvedCredential struct {
	SecretKey string
	Source    CredentialSource
	Path      string
}

// credfileSection is the TOML schema for ~/.gatr/credentials.toml.
// Single section, single field — kept narrow on purpose so a future
// "[live]" or "[project_x]" section is an additive change.
type credfileSection struct {
	SecretKey string `toml:"secret_key"`
}

type credfileShape struct {
	Default credfileSection `toml:"default"`
}

// defaultCredfilePath returns ~/.gatr/credentials.toml (or "" if the
// home dir can't be resolved — rare in practice, but we don't want to
// panic on a sandboxed runtime).
func defaultCredfilePath() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, ".gatr", "credentials.toml")
}

// ResolveSecretKey applies the documented precedence:
//   1. opts.SecretKey (a flag, programmatic call)
//   2. $STRIPE_SECRET_KEY
//   3. ~/.gatr/credentials.toml → [default].secret_key
//
// On success the key is also format-validated; a malformed key returns
// E501 with details.reason="malformed". Empty everywhere returns E501
// with details.reason="missing".
//
// opts.CredentialFile overrides the default ~/.gatr/credentials.toml
// path — useful for tests and non-standard install layouts. An empty
// string means "use the default path."
func ResolveSecretKey(opts ClientOptions) (ResolvedCredential, error) {
	if opts.SecretKey != "" {
		if err := validateSecretKey(opts.SecretKey); err != nil {
			return ResolvedCredential{}, err
		}
		return ResolvedCredential{SecretKey: opts.SecretKey, Source: CredSourceOptions}, nil
	}

	if envKey := os.Getenv(envSecretKey); envKey != "" {
		if err := validateSecretKey(envKey); err != nil {
			return ResolvedCredential{}, err
		}
		return ResolvedCredential{SecretKey: envKey, Source: CredSourceEnv}, nil
	}

	path := opts.CredentialFile
	if path == "" {
		path = defaultCredfilePath()
	}
	if path != "" {
		key, err := readCredfile(path)
		if err == nil && key != "" {
			if verr := validateSecretKey(key); verr != nil {
				return ResolvedCredential{}, verr
			}
			return ResolvedCredential{SecretKey: key, Source: CredSourceFile, Path: path}, nil
		}
		// ENOENT (the common case — no credfile installed) falls through
		// to the "missing" error below. Other read errors propagate so
		// operators don't silently fall back when their TOML is broken.
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return ResolvedCredential{}, ErrMissingCredentials("malformed",
				fmt.Sprintf("could not read credentials file %s: %s", path, schema.Redact(err.Error())))
		}
	}

	return ResolvedCredential{}, ErrMissingCredentials("missing",
		"no Stripe secret key found (set --key, $STRIPE_SECRET_KEY, or ~/.gatr/credentials.toml)")
}

// readCredfile loads the TOML and returns [default].secret_key, or
// "" if the section is present but the key is empty. Returns an error
// for IO and parse failures.
func readCredfile(path string) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	var c credfileShape
	if _, err := toml.Decode(string(raw), &c); err != nil {
		return "", fmt.Errorf("parse credentials.toml: %w", err)
	}
	return c.Default.SecretKey, nil
}

// validateSecretKey enforces the secretKeyRE shape. Returns E501 with
// details.reason="malformed" on miss; nil on hit. Pure-function — no
// IO, no API calls.
func validateSecretKey(key string) error {
	if !secretKeyRE.MatchString(key) {
		return ErrMissingCredentials("malformed",
			"Stripe secret key has unrecognized format (expected sk_test_... / sk_live_... / rk_test_... / rk_live_...)")
	}
	return nil
}
