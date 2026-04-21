package templates_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/EduardMaghakyan/gatr-cli/cmd/cli/internal/cli/templates"
)

func TestRenderEverywhere(t *testing.T) {
	for _, name := range templates.Available {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			files, err := templates.Render(name, dir, templates.Data{Project: "alpha"})
			require.NoError(t, err)
			require.NotEmpty(t, files)

			yaml, err := os.ReadFile(filepath.Join(dir, "gatr.yaml"))
			require.NoError(t, err)
			require.Contains(t, string(yaml), "project: alpha")
		})
	}
}

func TestRenderUnknown(t *testing.T) {
	_, err := templates.Render("nope", t.TempDir(), templates.Data{Project: "x"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "unknown template")
}

func TestRenderRefusesExisting(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "gatr.yaml"), []byte("x"), 0o644))
	_, err := templates.Render("freemium-saas", dir, templates.Data{Project: "x"})
	require.Error(t, err)
	require.True(t, errors.Is(err, templates.ErrFileExists))
}

func TestDescriptionsCoverAllTemplates(t *testing.T) {
	for _, name := range templates.Available {
		require.NotEmpty(t, templates.Descriptions[name], "missing description for %s", name)
	}
}
