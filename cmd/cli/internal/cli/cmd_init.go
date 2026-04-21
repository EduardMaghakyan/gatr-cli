package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/huh"
	"github.com/spf13/cobra"

	"github.com/EduardMaghakyan/gatr-cli/cmd/cli/internal/cli/templates"
)

type initOptions struct {
	dir      string
	template string
	project  string
	force    bool
	noPrompt bool
}

func newInitCmd() *cobra.Command {
	opts := &initOptions{}
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Pick a template, scaffold gatr.yaml + sample SDK code",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runInit(cmd, opts)
		},
	}
	cmd.Flags().StringVar(&opts.dir, "dir", ".", "Target directory")
	cmd.Flags().StringVar(&opts.template, "template", "", "Template name (skips picker if set)")
	cmd.Flags().StringVar(&opts.project, "project", "", "Project name (skips prompt if set)")
	cmd.Flags().BoolVar(&opts.force, "force", false, "Overwrite an existing gatr.yaml")
	cmd.Flags().BoolVar(&opts.noPrompt, "no-prompt", false, "Fail rather than prompt for missing flags (CI-friendly)")
	return cmd
}

func runInit(cmd *cobra.Command, opts *initOptions) error {
	out := cmd.OutOrStdout()
	if err := chooseTemplate(opts); err != nil {
		return err
	}
	if err := chooseProject(opts); err != nil {
		return err
	}
	if err := prepareTargets(opts); err != nil {
		return err
	}
	written, err := templates.Render(opts.template, opts.dir, templates.Data{Project: opts.project})
	if err != nil {
		return err
	}
	printSuccess(out, opts, written)
	return nil
}

func chooseTemplate(opts *initOptions) error {
	if opts.template != "" {
		if !contains(templates.Available, opts.template) {
			return fmt.Errorf("unknown template %q (available: %s)", opts.template, strings.Join(templates.Available, ", "))
		}
		return nil
	}
	if opts.noPrompt {
		return errors.New("--template is required when --no-prompt is set")
	}
	options := make([]huh.Option[string], 0, len(templates.Available))
	for _, name := range templates.Available {
		label := titleStyle.Render(name) + subtleStyle.Render("  "+templates.Descriptions[name])
		options = append(options, huh.NewOption(label, name))
	}
	return huh.NewForm(huh.NewGroup(
		huh.NewSelect[string]().
			Title("Pick a template").
			Description("Each template covers a common SaaS pricing shape.").
			Options(options...).
			Value(&opts.template),
	)).Run()
}

func chooseProject(opts *initOptions) error {
	if opts.project != "" {
		return validateProject(opts.project)
	}
	if opts.noPrompt {
		return errors.New("--project is required when --no-prompt is set")
	}
	def := defaultProjectName(opts.dir)
	opts.project = def
	return huh.NewForm(huh.NewGroup(
		huh.NewInput().
			Title("Project name").
			Description("Used as the `project:` field in gatr.yaml.").
			Validate(validateProject).
			Value(&opts.project),
	)).Run()
}

func validateProject(s string) error {
	s = strings.TrimSpace(s)
	if s == "" {
		return errors.New("project name cannot be empty")
	}
	for _, r := range s {
		if !(r == '-' || r == '_' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')) {
			return fmt.Errorf("project name may only contain letters, digits, '-', '_'; got %q", s)
		}
	}
	return nil
}

func defaultProjectName(dir string) string {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "my-app"
	}
	base := filepath.Base(abs)
	if base == "." || base == "/" {
		return "my-app"
	}
	return base
}

// prepareTargets enumerates every file the chosen template will write, checks
// for collisions, and either refuses (without --force) or removes all colliding
// files atomically before Render runs. Without this, a --force that collides
// with multiple files would leave a partial scaffold on the first write failure.
func prepareTargets(opts *initOptions) error {
	targets, err := templates.Plan(opts.template, opts.dir)
	if err != nil {
		return err
	}
	var conflicts []string
	for _, t := range targets {
		if _, err := os.Stat(t); err == nil {
			conflicts = append(conflicts, t)
		}
	}
	if len(conflicts) == 0 {
		return nil
	}
	if !opts.force {
		rel := make([]string, len(conflicts))
		for i, c := range conflicts {
			r, err := filepath.Rel(opts.dir, c)
			if err != nil {
				r = c
			}
			rel[i] = r
		}
		return fmt.Errorf("refuse to overwrite existing files (pass --force): %s", strings.Join(rel, ", "))
	}
	for _, c := range conflicts {
		if err := os.Remove(c); err != nil {
			return fmt.Errorf("remove %s: %w", c, err)
		}
	}
	return nil
}

func printSuccess(out io.Writer, opts *initOptions, written []string) {
	fmt.Fprintln(out)
	fmt.Fprintln(out, titleStyle.Render(fmt.Sprintf("✓ Scaffolded %s in %s", opts.template, opts.dir)))
	fmt.Fprintln(out)
	for _, p := range written {
		rel, err := filepath.Rel(opts.dir, p)
		if err != nil {
			rel = p
		}
		fmt.Fprintln(out, "  "+bullet(rel))
	}
	fmt.Fprintln(out)
	fmt.Fprintln(out, subtleStyle.Render("Next:"))
	fmt.Fprintln(out, "  "+codeStyle.Render("gatr validate")+subtleStyle.Render("                 # check the scaffolded gatr.yaml"))
	fmt.Fprintln(out, "  "+codeStyle.Render("gatr typegen --lang ts")+subtleStyle.Render("       # emit typed bindings for @gatr/node"))
	fmt.Fprintln(out, "  "+codeStyle.Render("pnpm add @gatr/node")+subtleStyle.Render("           # install the SDK"))
	fmt.Fprintln(out)
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}
