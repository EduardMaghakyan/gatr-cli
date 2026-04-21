package cli

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/EduardMaghakyan/gatr-cli/pkg/schema/yamlpatch"
	gstripe "github.com/EduardMaghakyan/gatr-cli/pkg/stripe"
)

// patchesFromResults turns the apply results into yamlpatch Patches.
// Only Create ops produce patches (existing objects already have the
// id in yaml, presumably — and Update/Replace don't change the
// stripe_id for the user's reference).
//
// Recognised yaml_id suffixes (set by TranslateConfig):
//   - "<plan_id>_monthly"    → KindPlanMonthly  / YamlID=<plan_id>
//   - "<plan_id>_annual"     → KindPlanAnnual   / YamlID=<plan_id>
//   - "<meter_id>_metered"   → skipped (the metered PRICE has no yaml
//                              field to patch — the METER does)
//
// Meter creates → KindMeteredPrice.
func patchesFromResults(results []gstripe.ApplyResult) []yamlpatch.Patch {
	var out []yamlpatch.Patch
	for _, r := range results {
		if r.Err != nil {
			continue
		}
		if r.Op.Action != gstripe.ActionCreated && r.Op.Action != gstripe.ActionReplaced {
			continue
		}
		switch r.Op.Resource {
		case gstripe.ResourcePrice:
			if planID, ok := strings.CutSuffix(r.Op.YamlID, gstripe.PriceYamlSuffixMonthly); ok {
				out = append(out, yamlpatch.Patch{
					Kind: yamlpatch.KindPlanMonthly, YamlID: planID, StripeID: r.StripeID,
				})
			} else if planID, ok := strings.CutSuffix(r.Op.YamlID, gstripe.PriceYamlSuffixAnnual); ok {
				out = append(out, yamlpatch.Patch{
					Kind: yamlpatch.KindPlanAnnual, YamlID: planID, StripeID: r.StripeID,
				})
			}
			// Metered-price results intentionally don't patch — the
			// metered price is keyed on the meter id which lives in
			// metered_prices[].stripe_meter_id (handled by the meter
			// branch below).
		case gstripe.ResourceMeter:
			out = append(out, yamlpatch.Patch{
				Kind: yamlpatch.KindMeteredPrice, YamlID: r.Op.YamlID, StripeID: r.StripeID,
			})
		}
	}
	return out
}

// autoPatchConfig rewrites the yaml file in place, subject to the
// dirty-worktree guard. Returns (unresolved patches, error).
// --force skips the git check.
func autoPatchConfig(configPath string, patches []yamlpatch.Patch, force bool) ([]yamlpatch.Patch, error) {
	if len(patches) == 0 {
		return nil, nil
	}
	if !force {
		dirty, err := isDirtyInGit(configPath)
		if err != nil {
			return nil, err
		}
		if dirty {
			return nil, &gstripe.Error{
				Code:    gstripe.ErrCodeDirtyWorktree,
				Message: fmt.Sprintf("refusing to rewrite %s: it has uncommitted changes (pass --force to override)", configPath),
				Details: map[string]any{"path": configPath},
			}
		}
	}

	src, err := os.ReadFile(configPath)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", configPath, err)
	}
	out, unresolved, err := yamlpatch.Apply(src, patches)
	if err != nil {
		return nil, fmt.Errorf("patch %s: %w", configPath, err)
	}
	// Write atomically: rename-over guarantees a crash mid-write
	// doesn't leave a half-yaml on disk.
	tmp, err := os.CreateTemp(filepath.Dir(configPath), ".gatr-autopatch-*.yaml")
	if err != nil {
		return nil, fmt.Errorf("create temp: %w", err)
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(out); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return nil, fmt.Errorf("write temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return nil, fmt.Errorf("close temp: %w", err)
	}
	if err := os.Rename(tmpPath, configPath); err != nil {
		os.Remove(tmpPath)
		return nil, fmt.Errorf("rename temp: %w", err)
	}
	return unresolved, nil
}

// isDirtyInGit reports whether `path` has uncommitted changes in git.
// If the file isn't in a git repo (or `git` isn't installed), the
// guard is skipped — returns (false, nil) so --auto-patch still works
// outside a repo. Any OTHER git error (e.g. corrupted index) returns
// an error so the operator can investigate.
func isDirtyInGit(path string) (bool, error) {
	// git status --porcelain returns non-empty output for any change
	// (modified, staged, untracked). -- <path> scopes the check to
	// just the file we're about to rewrite — unrelated uncommitted
	// changes elsewhere don't trigger the guard.
	cmd := exec.Command("git", "status", "--porcelain", "--", path)
	cmd.Dir = filepath.Dir(path)
	out, err := cmd.Output()
	if err != nil {
		// git exited non-zero. Most common cause: "not a git repo"
		// (exit 128). Anything else is unexpected.
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 128 {
			return false, nil // not a repo → no guard
		}
		// `git` not on $PATH at all → treat as "no repo".
		if errors.Is(err, exec.ErrNotFound) {
			return false, nil
		}
		return false, fmt.Errorf("git status: %w", err)
	}
	return len(bytes.TrimSpace(out)) > 0, nil
}
