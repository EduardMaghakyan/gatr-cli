# Hybrid AI SaaS template

Every Gatr primitive composed in one config: features + per-seat limits +
credits + metered API. The full showcase.

## Files
- `gatr.yaml` — three plans (free / pro / business) with all four primitives
- `billing.example.ts` — `can()`, `limit()`, `plan()` (M1) plus stubs for `consume()` (M4) and `track()` (M5)

## Next steps
1. Replace every `REPLACE_WITH_*` placeholder with your real Stripe ids.
2. Decide your per-plan grants and includes — these are the levers you'll
   tune as you learn how customers use your product.
3. Land the SDK calls milestone by milestone:
   - M1: `can()`, `limit()`, `increment()`, `decrement()`, `plan()` (works today)
   - M4: `consume()`, `topup()`, `balance()`
   - M5: `track()`, `usage()`
   - M7: per-seat sync to Stripe + metered push to Stripe Meters
