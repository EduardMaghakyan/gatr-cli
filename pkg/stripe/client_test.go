package stripe

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNewClient_BogusKey_ReturnsParseableError(t *testing.T) {
	_, err := NewClient(ClientOptions{SecretKey: "not-a-real-key"})
	require.Error(t, err)
	var sErr *Error
	require.True(t, errors.As(err, &sErr), "expected typed *stripe.Error")
	require.Equal(t, ErrCodeMissingCredentials, sErr.Code)
	require.Equal(t, "malformed", sErr.Details["reason"])
}

func TestNewClient_EmptyEverywhere_ReturnsMissing(t *testing.T) {
	t.Setenv(envSecretKey, "")
	// Point at a non-existent credfile so we don't pick up the real ~/.gatr.
	_, err := NewClient(ClientOptions{CredentialFile: "/nonexistent/credentials.toml"})
	require.Error(t, err)
	require.True(t, IsMissingCredentials(err))
}

func TestNewClient_ValidKey_NoAPICall(t *testing.T) {
	// Stand up a fake "Stripe" server that fails the test if it's hit.
	// NewClient must not touch the network — only method calls do.
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	c, err := NewClient(ClientOptions{
		SecretKey:  validKey,
		HTTPClient: srv.Client(),
	})
	require.NoError(t, err)
	require.NotNil(t, c)
	require.Equal(t, CredSourceOptions, c.CredentialSource())
	require.Empty(t, c.CredentialPath(), "options source should not carry a path")
	require.Empty(t, c.ProjectID())
	require.Equal(t, int32(0), atomic.LoadInt32(&hits), "NewClient must not call Stripe")
}

func TestNewClient_PreservesProjectID(t *testing.T) {
	c, err := NewClient(ClientOptions{
		SecretKey: validKey,
		ProjectID: "550e8400-e29b-41d4-a716-446655440000",
	})
	require.NoError(t, err)
	require.Equal(t, "550e8400-e29b-41d4-a716-446655440000", c.ProjectID())
}

func TestError_FormatsCodeAndMessage(t *testing.T) {
	e := ErrMissingCredentials("missing", "no key")
	require.Equal(t, "E501: no key", e.Error())

	// No code → just message (defensive; never produced by the helpers).
	bare := &Error{Message: "raw"}
	require.Equal(t, "raw", bare.Error())
}

func TestError_UnwrapExposesCause(t *testing.T) {
	cause := errors.New("network down")
	e := ErrStripeAPI(cause, "card_declined", "could not list products")
	require.Equal(t, "card_declined", e.Details["stripe_code"])
	require.Equal(t, ErrCodeStripeAPI, e.Code)
	require.True(t, errors.Is(e, cause), "errors.Is should follow Unwrap")
}

func TestErrStripeAPI_OmitsEmptyStripeCode(t *testing.T) {
	e := ErrStripeAPI(errors.New("boom"), "", "wrapped")
	_, present := e.Details["stripe_code"]
	require.False(t, present, "no stripe_code → key absent, not empty string")
}
