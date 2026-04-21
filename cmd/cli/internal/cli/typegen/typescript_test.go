package typegen_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/EduardMaghakyan/gatr-cli/cmd/cli/internal/cli/typegen"
	schema "github.com/EduardMaghakyan/gatr-cli/pkg/schema"
)

func TestRenderTypeScriptShape(t *testing.T) {
	cfg := &schema.Config{
		Version: 4,
		Project: "demo",
		Features: []schema.Feature{
			{ID: "export_pdf", Name: "PDF export"},
			{ID: "custom_domain", Name: "Custom domain"},
		},
		Limits: []schema.Limit{
			{ID: "seats", Name: "Seats", Unit: "seats", Period: "never"},
		},
		Plans: []schema.Plan{
			{ID: "free", Name: "Free"},
			{ID: "pro", Name: "Pro"},
		},
	}
	out, err := typegen.RenderTypeScript(cfg)
	require.NoError(t, err)
	require.Contains(t, out, `// project: demo, schema version: 4`)
	require.Contains(t, out, `declare module "@gatr/node" {`)
	require.Contains(t, out, "interface GatrFeatureMap {")
	require.Contains(t, out, "custom_domain: true;")
	require.Contains(t, out, "export_pdf: true;")
	require.True(t, strings.Index(out, "custom_domain") < strings.Index(out, "export_pdf"),
		"ids should be sorted alphabetically")
}

func TestRenderTypeScriptQuotesNonIdentKeys(t *testing.T) {
	cfg := &schema.Config{
		Version: 4,
		Project: "x",
		Plans:   []schema.Plan{{ID: "free", Name: "Free"}},
		Features: []schema.Feature{
			{ID: "with-dash", Name: "Dash"},
			{ID: "starts9invalid", Name: "Starts with digit"},
		},
	}
	out, err := typegen.RenderTypeScript(cfg)
	require.NoError(t, err)
	require.Contains(t, out, `"with-dash": true;`)
	require.Contains(t, out, "starts9invalid: true;")
}

func TestRenderTypeScriptHandlesEmptyMaps(t *testing.T) {
	cfg := &schema.Config{
		Version: 4,
		Project: "empty",
		Plans:   []schema.Plan{{ID: "free", Name: "Free"}},
	}
	out, err := typegen.RenderTypeScript(cfg)
	require.NoError(t, err)
	require.Contains(t, out, "interface GatrFeatureMap {\n  }")
	require.Contains(t, out, "free: true;")
}
