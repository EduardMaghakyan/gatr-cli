# Usage-based API template

Two plans with included allowance + per-call overage. Metering is post-paid
and pushed to Stripe Meters every 5 minutes (server-side, M7).

## Files
- `gatr.yaml` — one metered price (`api_calls` at $0.001 each)
- `billing.example.ts` — placeholder; metering API lands in M5

## Next steps
1. Create a Stripe Meter (Stripe → Billing → Meters) and paste its id into
   `stripe_meter_id`. Replace `REPLACE_WITH_STRIPE_PRICE_ID` with your base
   subscription price.
2. Decide your unit price (currently $0.001/call) and included allowance.
3. The `gatr.track()` and `gatr.usage()` APIs land in M5 of the v1 plan.
