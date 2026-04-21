package schema_test

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/stretchr/testify/require"

	schema "github.com/EduardMaghakyan/gatr-cli/pkg/schema"
)

func TestEmbedSchemaParses(t *testing.T) {
	require.NotEmpty(t, schema.SchemaJSON)
	var raw map[string]any
	require.NoError(t, json.Unmarshal(schema.SchemaJSON, &raw))
	require.Equal(t, "https://json-schema.org/draft/2020-12/schema", raw["$schema"])
}

func TestEmbedSchemaCompiles(t *testing.T) {
	c := jsonschema.NewCompiler()
	var raw any
	require.NoError(t, json.Unmarshal(schema.SchemaJSON, &raw))
	require.NoError(t, c.AddResource(schema.SchemaURI, raw))
	_, err := c.Compile(schema.SchemaURI)
	require.NoError(t, err)
}

func TestParseValidMinimal(t *testing.T) {
	cfg, err := schema.ParseFileAndValidate(fixture(t, "valid.minimal.yaml"))
	require.NoError(t, err)
	require.Equal(t, schema.SupportedVersion, cfg.Version)
	require.Equal(t, "minimal-app", cfg.Project)
	require.Len(t, cfg.Plans, 1)
	require.True(t, cfg.IDExists("plans", "free"))
}

func TestParseValidFull(t *testing.T) {
	cfg, err := schema.ParseFileAndValidate(fixture(t, "valid.full.yaml"))
	require.NoError(t, err)
	require.Equal(t, []string{"export_pdf", "custom_domain"}, featureIDs(cfg))
	require.Equal(t, []string{"free", "pro", "enterprise"}, planIDs(cfg))

	enterprise := findPlan(t, cfg, "enterprise")
	seats := enterprise.Limits["seats"]
	require.True(t, seats.IsUnlimited())

	pro := findPlan(t, cfg, "pro")
	require.NotNil(t, pro.Billing)
	require.NotNil(t, pro.Billing.Monthly)
	require.NotNil(t, pro.Billing.Annual)
	require.Equal(t, 14, pro.TrialDays)
}

func TestRejectInvalid(t *testing.T) {
	expectations := loadExpectations(t)
	for name, want := range expectations {
		t.Run(name, func(t *testing.T) {
			_, err := schema.ParseFileAndValidate(fixture(t, name))
			require.Error(t, err, "expected validation error")
			var gerr *schema.Error
			require.True(t, errors.As(err, &gerr), "expected *schema.Error, got %T", err)
			require.Equal(t, want.Code, gerr.Code,
				"fixture %s: want code %s, got %s (%s)", name, want.Code, gerr.Code, gerr.Message)
		})
	}
}

func TestNumberOrUnlimitedJSON(t *testing.T) {
	t.Run("unmarshal unlimited", func(t *testing.T) {
		var n schema.NumberOrUnlimited
		require.NoError(t, json.Unmarshal([]byte(`"unlimited"`), &n))
		require.True(t, n.IsUnlimited())
		require.True(t, n.IsSet())
	})
	t.Run("unmarshal number", func(t *testing.T) {
		var n schema.NumberOrUnlimited
		require.NoError(t, json.Unmarshal([]byte(`42`), &n))
		require.False(t, n.IsUnlimited())
		require.Equal(t, 42, n.Int())
	})
	t.Run("marshal unset is null", func(t *testing.T) {
		var n schema.NumberOrUnlimited
		b, err := json.Marshal(n)
		require.NoError(t, err)
		require.Equal(t, "null", string(b))
	})
	t.Run("marshal number", func(t *testing.T) {
		var n schema.NumberOrUnlimited
		require.NoError(t, json.Unmarshal([]byte(`5`), &n))
		b, err := json.Marshal(n)
		require.NoError(t, err)
		require.Equal(t, "5", string(b))
	})
	t.Run("marshal unlimited", func(t *testing.T) {
		var n schema.NumberOrUnlimited
		require.NoError(t, json.Unmarshal([]byte(`"unlimited"`), &n))
		b, err := json.Marshal(n)
		require.NoError(t, err)
		require.Equal(t, `"unlimited"`, string(b))
	})
}

func TestErrorFormatting(t *testing.T) {
	bare := &schema.Error{Code: "E021", Message: "no user"}
	require.Equal(t, "[E021] no user", bare.Error())

	with := &schema.Error{Code: "E001", Message: "broke", Path: "/foo"}
	require.Equal(t, "[E001] broke (path=/foo)", with.Error())
}

func TestParseFileMissing(t *testing.T) {
	_, err := schema.ParseFile("/no/such/file.yaml")
	require.Error(t, err)
	var gerr *schema.Error
	require.True(t, errors.As(err, &gerr))
	require.Equal(t, "E002", gerr.Code)
}

func TestParseFileAndValidateMissing(t *testing.T) {
	_, err := schema.ParseFileAndValidate("/no/such/file.yaml")
	require.Error(t, err)
	var gerr *schema.Error
	require.True(t, errors.As(err, &gerr))
	require.Equal(t, "E002", gerr.Code)
}

func TestIDExistsPanicsOnUnknownScope(t *testing.T) {
	cfg, err := schema.ParseFileAndValidate(fixture(t, "valid.minimal.yaml"))
	require.NoError(t, err)
	require.Panics(t, func() { _ = cfg.IDExists("nonsense", "x") })
}

// helpers

type expectation struct {
	Code string `json:"code"`
}

func loadExpectations(t *testing.T) map[string]expectation {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "error_expectations.json"))
	require.NoError(t, err)
	var out map[string]expectation
	require.NoError(t, json.Unmarshal(data, &out))
	return out
}

func fixture(t *testing.T, name string) string {
	t.Helper()
	return filepath.Join("testdata", name)
}

func featureIDs(cfg *schema.Config) []string {
	out := make([]string, 0, len(cfg.Features))
	for _, f := range cfg.Features {
		out = append(out, f.ID)
	}
	return out
}

func planIDs(cfg *schema.Config) []string {
	out := make([]string, 0, len(cfg.Plans))
	for _, p := range cfg.Plans {
		out = append(out, p.ID)
	}
	return out
}

func findPlan(t *testing.T, cfg *schema.Config, id string) *schema.Plan {
	t.Helper()
	for i := range cfg.Plans {
		if cfg.Plans[i].ID == id {
			return &cfg.Plans[i]
		}
	}
	t.Fatalf("plan %q not found", id)
	return nil
}
