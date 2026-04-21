package schema_test

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	schema "github.com/EduardMaghakyan/gatr-cli/pkg/schema"
)

func TestParseDirectRejectsBadYAML(t *testing.T) {
	_, err := schema.Parse([]byte("version: 4\nproject: x\nplans: [unclosed"))
	require.Error(t, err)
	var gerr *schema.Error
	require.True(t, errors.As(err, &gerr))
	require.Equal(t, "E001", gerr.Code)
}

func TestParseDirectAcceptsValidBytes(t *testing.T) {
	cfg, err := schema.Parse([]byte("version: 4\nproject: x\nplans:\n  - id: free\n    name: Free\n"))
	require.NoError(t, err)
	require.Equal(t, "x", cfg.Project)
}

func TestValidateBytes(t *testing.T) {
	require.NoError(t, schema.Validate([]byte("version: 4\nproject: x\nplans:\n  - id: free\n    name: Free\n")))
}

func TestIDExistsAcrossAllScopes(t *testing.T) {
	cfg, err := schema.ParseFileAndValidate(fixture(t, "valid.full.yaml"))
	require.NoError(t, err)
	require.True(t, cfg.IDExists("features", "export_pdf"))
	require.False(t, cfg.IDExists("features", "missing"))
	require.True(t, cfg.IDExists("limits", "seats"))
	require.False(t, cfg.IDExists("limits", "missing"))
	require.True(t, cfg.IDExists("plans", "pro"))
	require.False(t, cfg.IDExists("plans", "missing"))
}
