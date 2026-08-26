package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/EduardMaghakyan/gatr-cli/pkg/schema"
)

// Where the gatr server and its key come from, in the same shape as the
// Stripe credentials: a flag beats the environment.
const (
	envGatrBaseURL = "GATR_BASE_URL"
	envGatrAPIKey  = "GATR_API_KEY"
)

// deployTimeout is generous: the server validates the config and reads every
// customer's plan before answering, and a push is a deliberate one-off rather
// than something on a hot path.
const deployTimeout = 30 * time.Second

type deployResult struct {
	ProjectID   string   `json:"project_id"`
	FromVersion int32    `json:"from_version"`
	ToVersion   int32    `json:"to_version"`
	DryRun      bool     `json:"dry_run"`
	PlansInUse  []string `json:"plans_in_use"`
}

// resolveDeployTarget returns the base URL and API key for the gatr server.
//
// Both are required together: a URL with no key cannot authenticate, and a key
// with no URL has nothing to authenticate to. Failing here with a named
// variable beats failing later with a 401 that looks like a bad key.
func resolveDeployTarget(baseURL, apiKey string) (string, string, error) {
	if baseURL == "" {
		baseURL = os.Getenv(envGatrBaseURL)
	}
	if apiKey == "" {
		apiKey = os.Getenv(envGatrAPIKey)
	}
	baseURL, apiKey = strings.TrimRight(strings.TrimSpace(baseURL), "/"), strings.TrimSpace(apiKey)

	var missing []string
	if baseURL == "" {
		missing = append(missing, "--gatr-url or $"+envGatrBaseURL)
	}
	if apiKey == "" {
		missing = append(missing, "--gatr-key or $"+envGatrAPIKey)
	}
	if len(missing) > 0 {
		return "", "", fmt.Errorf("--deploy needs %s", strings.Join(missing, " and "))
	}
	return baseURL, apiKey, nil
}

// deployConfig sends the yaml to the server's POST /v1/config.
//
// This is the whole point of --deploy: without it, the config a customer's app
// reads is changed by connecting to the database — which means an operator
// holding production credentials on their laptop, to change a price. The
// server identifies the project from the key, so there is no project to name
// and no way to point this at somebody else's pricing.
func deployConfig(
	ctx context.Context,
	baseURL, apiKey string,
	yaml []byte,
	dryRun bool,
) (*deployResult, error) {
	body, err := json.Marshal(map[string]any{
		"config":  string(yaml),
		"dry_run": dryRun,
	})
	if err != nil {
		return nil, fmt.Errorf("encode config: %w", err)
	}

	ctx, cancel := context.WithTimeout(ctx, deployTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(
		ctx, http.MethodPost, baseURL+"/v1/config", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("reach gatr server: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		// The server's message is the useful part — it names the plan and the
		// customer count when a push would strand somebody — so it is passed
		// through rather than replaced with the status code.
		return nil, fmt.Errorf("gatr server refused the config (%d): %s",
			resp.StatusCode, serverMessage(raw))
	}

	var out deployResult
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("parse response: %w", err)
	}
	return &out, nil
}

// checkPricesResolved refuses to deploy a config whose paid plans still have
// no Stripe price id.
//
// gatr matches an incoming subscription to a plan by that id. A config with
// nulls in it parses, validates and deploys perfectly happily, and then every
// subscription event silently fails to resolve to a plan — no error anywhere,
// the customer simply stops being tracked.
//
// It happens for a mundane reason: `push --deploy` where the yaml patch was
// declined at the prompt. The Stripe prices exist, the file still says null,
// and the deploy would carry that null to the server.
//
// Credit packs are deliberately not checked. gatr identifies a pack from the
// session metadata and the amount, never from the price id, so a pack without
// one is unsellable but not silently broken.
func checkPricesResolved(cfg *schema.Config) error {
	var missing []string
	for _, plan := range cfg.Plans {
		if plan.Billing == nil {
			continue
		}
		if m := plan.Billing.Monthly; m != nil && (m.StripePriceID == nil || *m.StripePriceID == "") {
			missing = append(missing, plan.ID+" (monthly)")
		}
		if a := plan.Billing.Annual; a != nil && (a.StripePriceID == nil || *a.StripePriceID == "") {
			missing = append(missing, plan.ID+" (annual)")
		}
	}
	if len(missing) == 0 {
		return nil
	}
	sort.Strings(missing)
	return fmt.Errorf(
		"refusing to deploy: %s still has no stripe_price_id.\n"+
			"gatr matches subscriptions to plans by that id, so deploying this would leave\n"+
			"subscription events resolving to no plan at all — silently. Re-run with\n"+
			"--auto-patch, or accept the patch prompt, so the ids reach the file first",
		strings.Join(missing, ", "))
}

// serverMessage digs the human-readable line out of gatr's error envelope,
// falling back to the raw body so an unexpected shape still says something.
func serverMessage(raw []byte) string {
	var env struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &env); err == nil && env.Error.Message != "" {
		return env.Error.Message
	}
	return strings.TrimSpace(string(raw))
}
