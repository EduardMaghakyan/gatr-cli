package stripe

import (
	"log/slog"
	"net/http"

	stripesdk "github.com/stripe/stripe-go/v82"
	stripeclient "github.com/stripe/stripe-go/v82/client"
)

// ClientOptions configures NewClient. All fields are optional; the
// zero-value form (`stripe.NewClient(stripe.ClientOptions{})`) attempts
// env+credfile resolution and uses sensible defaults.
type ClientOptions struct {
	// SecretKey, if non-empty, is used directly and short-circuits env
	// + credfile lookup. Validated against the Stripe key format
	// regardless of source.
	SecretKey string

	// CredentialFile overrides ~/.gatr/credentials.toml. Empty = default.
	// Honoured only when SecretKey + $STRIPE_SECRET_KEY are both empty.
	CredentialFile string

	// HTTPClient lets callers (mostly tests) inject a custom transport
	// to capture or mock Stripe HTTP traffic. nil = stripe-go's default.
	HTTPClient *http.Client

	// BackendURL overrides the Stripe API endpoint. For tests only —
	// production code should not set this. Empty = stripe-go's default
	// (https://api.stripe.com).
	BackendURL string

	// Logger receives wrapper-level diagnostics. nil = slog.Default().
	Logger *slog.Logger

	// ProjectID is the gatr project UUID this client is bound to. Used
	// to namespace gatr_id metadata as "<project>:<yaml_id>" so a
	// single Stripe account can host multiple gatr projects (Decision #3).
	// Optional in T1; the upsert layer (T3) requires it.
	ProjectID string
}

// Client is the gatr-specific Stripe wrapper. Construct with NewClient.
// Methods on Client filter list responses by gatr_managed=true and
// scope by ProjectID, so reads never see operator-owned objects and
// writes never collide across projects.
type Client struct {
	sc        *stripeclient.API
	logger    *slog.Logger
	projectID string
	credSrc   CredentialSource
	credPath  string
}

// NewClient resolves credentials, validates the key shape, and
// initialises the underlying Stripe SDK client. **No network call is
// made** — the SDK is purely client-side until a method is invoked.
//
// Returns an *Error on credential / format problems so callers can
// match by code (errors.As) without wrapping.
func NewClient(opts ClientOptions) (*Client, error) {
	resolved, err := ResolveSecretKey(opts)
	if err != nil {
		return nil, err
	}

	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}

	sc := &stripeclient.API{}
	cfg := &stripesdk.BackendConfig{}
	if opts.HTTPClient != nil {
		cfg.HTTPClient = opts.HTTPClient
	}
	if opts.BackendURL != "" {
		cfg.URL = stripesdk.String(opts.BackendURL)
	}
	backends := &stripesdk.Backends{
		API:     stripesdk.GetBackendWithConfig(stripesdk.APIBackend, cfg),
		Uploads: stripesdk.GetBackendWithConfig(stripesdk.UploadsBackend, cfg),
		Connect: stripesdk.GetBackendWithConfig(stripesdk.ConnectBackend, cfg),
	}
	sc.Init(resolved.SecretKey, backends)

	return &Client{
		sc:        sc,
		logger:    logger,
		projectID: opts.ProjectID,
		credSrc:   resolved.Source,
		credPath:  resolved.Path,
	}, nil
}

// CredentialSource reports where the active key was loaded from.
// Useful for "using key from $STRIPE_SECRET_KEY" diagnostics in the CLI.
func (c *Client) CredentialSource() CredentialSource { return c.credSrc }

// CredentialPath returns the credfile path when CredentialSource is
// CredSourceFile, otherwise "".
func (c *Client) CredentialPath() string { return c.credPath }

// ProjectID returns the gatr project UUID this client is bound to,
// or "" if unbound.
func (c *Client) ProjectID() string { return c.projectID }
