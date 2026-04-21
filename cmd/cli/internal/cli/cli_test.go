package cli_test

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/EduardMaghakyan/gatr-cli/cmd/cli/internal/cli"
)

func runCmd(t *testing.T, args ...string) (string, error) {
	t.Helper()
	root := cli.NewRoot("test")
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs(args)
	err := root.Execute()
	return out.String(), err
}

func TestRootHelp(t *testing.T) {
	out, err := runCmd(t, "--help")
	require.NoError(t, err)
	require.Contains(t, out, "gatr init")
	require.Contains(t, out, "gatr validate")
	require.Contains(t, out, "gatr typegen")
}

func TestVersion(t *testing.T) {
	out, err := runCmd(t, "--version")
	require.NoError(t, err)
	require.Contains(t, out, "gatr test")
}

func TestInitScaffoldsAllTemplates(t *testing.T) {
	for _, tpl := range []string{
		"freemium-saas", "per-seat-team", "ai-credits", "usage-api", "hybrid-ai-saas",
	} {
		t.Run(tpl, func(t *testing.T) {
			dir := t.TempDir()
			out, err := runCmd(t, "init",
				"--template", tpl,
				"--project", "demo_app",
				"--no-prompt",
				"--dir", dir,
			)
			require.NoError(t, err, out)
			require.FileExists(t, filepath.Join(dir, "gatr.yaml"))
			require.FileExists(t, filepath.Join(dir, "billing.example.ts"))
			require.FileExists(t, filepath.Join(dir, "GATR_README.md"))

			yaml, err := os.ReadFile(filepath.Join(dir, "gatr.yaml"))
			require.NoError(t, err)
			require.Contains(t, string(yaml), "project: demo_app")
		})
	}
}

func TestInitRefusesIfGatrYAMLExists(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "gatr.yaml"), []byte("preexisting"), 0o644))
	out, err := runCmd(t, "init",
		"--template", "freemium-saas",
		"--project", "x",
		"--no-prompt",
		"--dir", dir,
	)
	require.Error(t, err)
	require.Contains(t, err.Error(), "refuse to overwrite")
	require.Contains(t, err.Error(), "gatr.yaml")
	_ = out
}

func TestInitRefuseListsAllConflicts(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "gatr.yaml"), []byte("x"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "GATR_README.md"), []byte("x"), 0o644))
	_, err := runCmd(t, "init",
		"--template", "freemium-saas",
		"--project", "demo",
		"--no-prompt",
		"--dir", dir,
	)
	require.Error(t, err)
	require.Contains(t, err.Error(), "gatr.yaml")
	require.Contains(t, err.Error(), "GATR_README.md")
}

func TestInitForceAtomicOverwritesMultipleFiles(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "gatr.yaml"), []byte("pre"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "GATR_README.md"), []byte("pre"), 0o644))
	_, err := runCmd(t, "init",
		"--template", "freemium-saas",
		"--project", "demo",
		"--no-prompt",
		"--force",
		"--dir", dir,
	)
	require.NoError(t, err)
	yaml, err := os.ReadFile(filepath.Join(dir, "gatr.yaml"))
	require.NoError(t, err)
	require.NotContains(t, string(yaml), "pre")
	readme, err := os.ReadFile(filepath.Join(dir, "GATR_README.md"))
	require.NoError(t, err)
	require.NotContains(t, string(readme), "pre")
}

// TestInitLeavesProjectREADMEAlone is the regression guard for the
// rename: `gatr init` MUST NOT touch a user's existing README.md, even
// when --force is set. The whole reason we ship as GATR_README.md is
// to be safe to drop into a populated repo without clobbering anything.
func TestInitLeavesProjectREADMEAlone(t *testing.T) {
	dir := t.TempDir()
	const userReadme = "# My existing project\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "README.md"), []byte(userReadme), 0o644))

	_, err := runCmd(t, "init",
		"--template", "freemium-saas",
		"--project", "demo",
		"--no-prompt",
		"--force", // even with --force
		"--dir", dir,
	)
	require.NoError(t, err)

	got, err := os.ReadFile(filepath.Join(dir, "README.md"))
	require.NoError(t, err)
	require.Equal(t, userReadme, string(got), "user's README.md must be left exactly as written")
	require.FileExists(t, filepath.Join(dir, "GATR_README.md"))
}

func TestInitForceOverwrites(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "gatr.yaml"), []byte("preexisting"), 0o644))
	_, err := runCmd(t, "init",
		"--template", "freemium-saas",
		"--project", "x",
		"--no-prompt",
		"--force",
		"--dir", dir,
	)
	require.NoError(t, err)
	yaml, err := os.ReadFile(filepath.Join(dir, "gatr.yaml"))
	require.NoError(t, err)
	require.NotContains(t, string(yaml), "preexisting")
}

func TestInitRequiresTemplateWhenNoPrompt(t *testing.T) {
	_, err := runCmd(t, "init", "--project", "x", "--no-prompt", "--dir", t.TempDir())
	require.Error(t, err)
	require.Contains(t, err.Error(), "--template")
}

func TestInitRequiresProjectWhenNoPrompt(t *testing.T) {
	_, err := runCmd(t, "init",
		"--template", "freemium-saas",
		"--no-prompt",
		"--dir", t.TempDir(),
	)
	require.Error(t, err)
	require.Contains(t, err.Error(), "--project")
}

func TestInitRejectsBadProjectName(t *testing.T) {
	_, err := runCmd(t, "init",
		"--template", "freemium-saas",
		"--project", "bad name!",
		"--no-prompt",
		"--dir", t.TempDir(),
	)
	require.Error(t, err)
	require.Contains(t, err.Error(), "may only contain")
}

func TestInitRejectsEmptyProject(t *testing.T) {
	_, err := runCmd(t, "init",
		"--template", "freemium-saas",
		"--project", "  ",
		"--no-prompt",
		"--dir", t.TempDir(),
	)
	require.Error(t, err)
	require.Contains(t, err.Error(), "cannot be empty")
}

func TestInitRejectsUnknownTemplate(t *testing.T) {
	_, err := runCmd(t, "init",
		"--template", "no-such-template",
		"--project", "x",
		"--no-prompt",
		"--dir", t.TempDir(),
	)
	require.Error(t, err)
	require.Contains(t, err.Error(), "unknown template")
}

func TestValidateSucceedsOnScaffoldedConfig(t *testing.T) {
	dir := t.TempDir()
	_, err := runCmd(t, "init",
		"--template", "hybrid-ai-saas",
		"--project", "demo",
		"--no-prompt",
		"--dir", dir,
	)
	require.NoError(t, err)
	out, err := runCmd(t, "validate", "--config", filepath.Join(dir, "gatr.yaml"))
	require.NoError(t, err, out)
	require.Contains(t, out, "is valid")
}

func TestValidateFailsOnMissingFile(t *testing.T) {
	out, err := runCmd(t, "validate", "--config", "/no/such/file.yaml")
	require.Error(t, err)
	require.Contains(t, out, "E002")
}

func TestValidateFailsOnInvalidConfig(t *testing.T) {
	dir := t.TempDir()
	bad := filepath.Join(dir, "gatr.yaml")
	require.NoError(t, os.WriteFile(bad, []byte("version: 99\nproject: x\nplans: [{id: free, name: Free}]\n"), 0o644))
	out, err := runCmd(t, "validate", "--config", bad)
	require.Error(t, err)
	require.Contains(t, out, "E003")
}

func TestTypegenWritesFile(t *testing.T) {
	dir := t.TempDir()
	_, err := runCmd(t, "init",
		"--template", "hybrid-ai-saas",
		"--project", "demo",
		"--no-prompt",
		"--dir", dir,
	)
	require.NoError(t, err)
	outPath := filepath.Join(dir, "src", "gatr.generated.ts")
	out, err := runCmd(t, "typegen",
		"--config", filepath.Join(dir, "gatr.yaml"),
		"--out", outPath,
	)
	require.NoError(t, err, out)
	require.FileExists(t, outPath)

	body, err := os.ReadFile(outPath)
	require.NoError(t, err)
	s := string(body)
	require.Contains(t, s, `declare module "@gatr/node"`)
	require.Contains(t, s, "interface GatrFeatureMap")
	require.Contains(t, s, "export_pdf: true")
	require.Contains(t, s, "interface GatrLimitMap")
	require.Contains(t, s, "seats: true")
	require.Contains(t, s, "interface GatrPlanMap")
	require.Contains(t, s, "pro: true")
	require.True(t, strings.HasSuffix(strings.TrimSpace(s), "export {};"))
}

func TestTypegenRejectsUnsupportedLang(t *testing.T) {
	dir := t.TempDir()
	_, err := runCmd(t, "init",
		"--template", "freemium-saas",
		"--project", "demo",
		"--no-prompt",
		"--dir", dir,
	)
	require.NoError(t, err)
	_, err = runCmd(t, "typegen",
		"--config", filepath.Join(dir, "gatr.yaml"),
		"--lang", "py",
	)
	require.Error(t, err)
	require.Contains(t, err.Error(), "unsupported --lang")
}

func TestTypegenImportPathHandlesEdgeCases(t *testing.T) {
	dir := t.TempDir()
	_, err := runCmd(t, "init",
		"--template", "freemium-saas",
		"--project", "demo",
		"--no-prompt",
		"--dir", dir,
	)
	require.NoError(t, err)
	for _, out := range []string{
		filepath.Join(dir, ".ts"),
		filepath.Join(dir, "plain"),
		filepath.Join(dir, "src/gatr.generated.ts"),
	} {
		_, err := runCmd(t, "typegen",
			"--config", filepath.Join(dir, "gatr.yaml"),
			"--out", out,
		)
		require.NoError(t, err, "--out %q must not panic", out)
	}
}

func TestTypegenFailsOnInvalidConfig(t *testing.T) {
	dir := t.TempDir()
	bad := filepath.Join(dir, "gatr.yaml")
	require.NoError(t, os.WriteFile(bad, []byte("version: 99\nproject: x\nplans: [{id: free, name: Free}]\n"), 0o644))
	_, err := runCmd(t, "typegen", "--config", bad, "--out", filepath.Join(dir, "out.ts"))
	require.Error(t, err)
}
