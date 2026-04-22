# gatr

A standalone CLI that scaffolds Stripe products + prices + meters from a single `gatr.yaml`, validates pricing configs, generates typed bindings, and keeps Stripe in lockstep with your YAML — idempotently, with a dry-run safety net.

You bring a `gatr.yaml` and a Stripe restricted key. That's the whole input contract — no server, no database, no gatr account.

## Install

```bash
# Go install — single binary in $GOBIN
go install github.com/EduardMaghakyan/gatr-cli/cmd/cli@latest
gatr --version

# Docker — no Go toolchain needed
docker run --rm -v $PWD:/work ghcr.io/eduardmaghakyan/gatr-cli:latest --version

# Pre-built binaries
# Download from https://github.com/EduardMaghakyan/gatr-cli/releases
```

## Quickstart

```bash
# Scaffold a new gatr.yaml from a template (interactive picker)
gatr init

# Lint the YAML against the canonical schema
gatr validate

# Generate typed TypeScript bindings
gatr typegen --lang ts --out ./src/gatr.gen.ts
```

```bash
export STRIPE_SECRET_KEY=sk_test_...

# Print the diff, confirm [y/N], then apply.
gatr push

# Print the diff and exit — no prompt, no apply.
gatr push --dry-run

# CI: skip both prompts.
gatr push --auto-approve --auto-patch
```

Already have products/prices in Stripe? Start with the reverse:

```bash
# Read Stripe → emit a starter gatr.yaml (refuses to overwrite)
gatr import --project my-app

# Review the file, fill in features/limits/credits (Stripe has no
# equivalent), then push to confirm the round-trip is a no-op.
gatr validate
gatr push --dry-run
```

## Commands

| Command | What it does |
|---|---|
| `gatr init` | Pick a template, scaffold `gatr.yaml` + sample SDK code |
| `gatr validate` | Lint `gatr.yaml` against the canonical schema |
| `gatr validate --check-stripe` | Verify every `stripe_price_id` / `stripe_meter_id` resolves in Stripe |
| `gatr typegen` | Generate typed TS/Go bindings from `gatr.yaml` |
| `gatr push` | Reconcile Stripe with `gatr.yaml` (idempotent diff + apply) |
| `gatr import` | Read an existing Stripe account → emit a starter `gatr.yaml` |

Run `gatr <command> --help` for full option lists.

## Stripe credentials

The CLI reads `STRIPE_SECRET_KEY` from (in order of precedence):

1. `--key` CLI flag
2. `STRIPE_SECRET_KEY` environment variable
3. `~/.gatr/credentials.toml` → `[default].secret_key`

A restricted-scope key is strongly recommended: write access on Products + Prices + Meters only. `gatr push` warns if your key has more permissions than required.

## Layout

```
cmd/cli/       # the `gatr` binary
pkg/schema/    # Go adapter over the canonical JSON schema
pkg/stripe/    # Stripe client + diff/apply engine
schema/        # gatr.schema.json (frozen from the upstream Zod source)
```

Three Go modules unified via `go.work` for local dev; each publishable independently. The schema JSON is the source of truth for both this Go code and the TypeScript SDK in the ecosystem repo.

## Build from source

```bash
git clone https://github.com/EduardMaghakyan/gatr-cli
cd gatr-cli
( cd cmd/cli && go build -o /tmp/gatr . )
/tmp/gatr --version
```

Run tests: `go test ./...` from each module directory (or use the CI workflow).

### Scenario tests against real Stripe

The `cmd/cli/internal/cli` package ships a 12-test scenario suite (`TestScenario_*`) that exercises `gatr push` end-to-end against a real Stripe **test** account — covering init/validate, idempotent re-runs, plan type mixes, soft updates, hard replaces, deletes, audit log integrity, and the "what happens to existing subscribers when the price changes" case.

They skip silently when `STRIPE_SECRET_KEY` is unset (so `go test ./...` stays green on dev machines and per-PR CI). To run them:

```bash
export STRIPE_SECRET_KEY=sk_test_...   # test-mode key, restricted to Products+Prices+Meters+BillingMeters recommended

# All 12 scenarios (~6 min; each test cleans up after itself)
go test ./cmd/cli/internal/cli/ -run TestScenario -v -timeout=10m

# One scenario
go test ./cmd/cli/internal/cli/ -run TestScenario_HardReplace -v
```

Each test uses a unique `gatrtest-<hex>` project namespace, so tests don't collide with each other or with real gatr projects in the same Stripe account. Cleanup archives every gatr-managed object under that namespace via the same diff engine `gatr push` uses. Stray objects (from interrupted tests) all carry unique project IDs and are inert — safe to ignore or sweep manually.

## License

MIT. See [`LICENSE`](LICENSE). No CLA.

## Related

This CLI is part of the broader [gatr ecosystem](https://github.com/gatr-dev/gatr) — a self-hostable server + SDK for runtime entitlements, credits, and metered-usage pricing. Both layers are optional; this CLI works standalone.
