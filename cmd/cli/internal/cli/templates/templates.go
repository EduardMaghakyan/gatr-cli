// Package templates embeds the 5 init templates and renders them into a
// target directory.
package templates

import (
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/template"
)

//go:embed all:files
var Files embed.FS

// Available is the canonical, ordered list of template names shown to the user.
var Available = []string{
	"freemium-saas",
	"per-seat-team",
	"ai-credits",
	"usage-api",
	"hybrid-ai-saas",
}

// Descriptions is shown next to each template name in the picker.
var Descriptions = map[string]string{
	"freemium-saas":  "Free + paid plan, feature gates only. The classic SaaS shape.",
	"per-seat-team":  "Flat + per-seat pricing with seat enforcement. Team tools.",
	"ai-credits":     "Credit pool + costed operations. AI/LLM products.",
	"usage-api":      "Pure usage-based metering with included allowance. Pay-per-call APIs.",
	"hybrid-ai-saas": "Features + credits + metering composed. The full showcase.",
}

// Data is the substitution context passed to template files (those with .tmpl).
type Data struct {
	Project string
}

// Plan returns the full list of target file paths that Render would write,
// without writing or reading anything on disk. Used by callers to pre-check
// existing files and decide whether to overwrite (--force) atomically.
func Plan(name string, targetDir string) ([]string, error) {
	if !contains(Available, name) {
		return nil, fmt.Errorf("unknown template: %q (available: %s)", name, strings.Join(Available, ", "))
	}
	srcRoot := filepath.ToSlash(filepath.Join("files", name))
	var targets []string
	err := fs.WalkDir(Files, srcRoot, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(srcRoot, path)
		if err != nil {
			return err
		}
		outName := strings.TrimSuffix(rel, ".tmpl")
		targets = append(targets, filepath.Join(targetDir, outName))
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(targets)
	return targets, nil
}

// Render writes the named template into targetDir, substituting Data into any
// .tmpl files. It is non-destructive: it returns ErrFileExists if any target
// file already exists. Callers wanting atomic overwrite should first call
// Plan() and remove conflicts themselves.
func Render(name string, targetDir string, data Data) ([]string, error) {
	if !contains(Available, name) {
		return nil, fmt.Errorf("unknown template: %q (available: %s)", name, strings.Join(Available, ", "))
	}
	srcRoot := filepath.ToSlash(filepath.Join("files", name))

	var written []string
	err := fs.WalkDir(Files, srcRoot, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(srcRoot, path)
		if err != nil {
			return err
		}
		outName := strings.TrimSuffix(rel, ".tmpl")
		outPath := filepath.Join(targetDir, outName)

		if _, err := os.Stat(outPath); err == nil {
			return fmt.Errorf("%w: %s", ErrFileExists, outPath)
		}

		raw, err := fs.ReadFile(Files, path)
		if err != nil {
			return err
		}
		var content []byte
		if strings.HasSuffix(path, ".tmpl") {
			tmpl, err := template.New(rel).Parse(string(raw))
			if err != nil {
				return fmt.Errorf("parse %s: %w", rel, err)
			}
			var sb strings.Builder
			if err := tmpl.Execute(&sb, data); err != nil {
				return fmt.Errorf("execute %s: %w", rel, err)
			}
			content = []byte(sb.String())
		} else {
			content = raw
		}

		if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(outPath, content, 0o644); err != nil {
			return err
		}
		written = append(written, outPath)
		return nil
	})
	if err != nil {
		return written, err
	}
	sort.Strings(written)
	return written, nil
}

// ErrFileExists is returned by Render when a target file already exists.
var ErrFileExists = errors.New("file already exists")

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}
