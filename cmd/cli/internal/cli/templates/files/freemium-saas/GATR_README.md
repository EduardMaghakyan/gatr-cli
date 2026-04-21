# Freemium SaaS template

Free tier plus one paid plan, gated by features and a numeric project cap.

## Files
- `gatr.yaml` — your pricing config (canonical source of truth)
- `billing.example.ts` — a runnable example showing `can()`, `limit()`, `plan()`

## Next steps
1. Replace `REPLACE_WITH_STRIPE_PRICE_ID` with your real Stripe price id (`price_...`).
2. Wire up the SDK in your app code (see `billing.example.ts`).
3. When you're ready, run `gatr push` to scaffold the matching products in Stripe (M6 of the v1 plan).
