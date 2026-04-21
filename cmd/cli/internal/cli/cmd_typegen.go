package cli

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/EduardMaghakyan/gatr-cli/cmd/cli/internal/cli/typegen"
	schema "github.com/EduardMaghakyan/gatr-cli/pkg/schema"
)

type typegenOptions struct {
	configPath string
	lang       string
	out        string
}

func newTypegenCmd() *cobra.Command {
	opts := &typegenOptions{}
	cmd := &cobra.Command{
		Use:   "typegen",
		Short: "Generate typed bindings for the SDK from gatr.yaml",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runTypegen(cmd, opts)
		},
	}
	cmd.Flags().StringVarP(&opts.configPath, "config", "c", "gatr.yaml", "Path to gatr.yaml")
	cmd.Flags().StringVarP(&opts.lang, "lang", "l", "ts", "Output language (currently: ts)")
	cmd.Flags().StringVarP(&opts.out, "out", "o", "src/gatr.generated.ts", "Output file path")
	return cmd
}

func runTypegen(cmd *cobra.Command, opts *typegenOptions) error {
	out := cmd.OutOrStdout()
	if opts.lang != "ts" {
		return fmt.Errorf("unsupported --lang %q (M2 supports: ts; Python/Ruby/PHP/Go in v1.1)", opts.lang)
	}
	cfg, err := schema.ParseFileAndValidate(opts.configPath)
	if err != nil {
		return err
	}
	rendered, err := typegen.RenderTypeScript(cfg)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(opts.out), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(opts.out, []byte(rendered), 0o644); err != nil {
		return err
	}
	fmt.Fprintln(out, successStyle.Render(fmt.Sprintf("✓ Wrote %s", opts.out)))
	fmt.Fprintln(out, subtleStyle.Render(
		fmt.Sprintf("  augments @gatr/node with %d features, %d limits, %d credits, %d operations, %d metered prices, %d plans",
			len(cfg.Features), len(cfg.Limits), len(cfg.Credits),
			len(cfg.Operations), len(cfg.MeteredPrices), len(cfg.Plans),
		),
	))
	fmt.Fprintln(out)
	fmt.Fprintln(out, subtleStyle.Render("Import once in your app to enable autocomplete + typo-fail-at-compile-time:"))
	fmt.Fprintln(out, "  "+codeStyle.Render(fmt.Sprintf(`import "%s";`, importPath(opts.out))))
	return nil
}

func importPath(out string) string {
	clean := filepath.ToSlash(filepath.Clean(out))
	for _, ext := range []string{".ts", ".tsx"} {
		if filepath.Ext(clean) == ext {
			clean = clean[:len(clean)-len(ext)]
		}
	}
	if clean == "" || clean == "." {
		return "./"
	}
	if clean[0] != '.' && clean[0] != '/' {
		clean = "./" + clean
	}
	return clean
}
