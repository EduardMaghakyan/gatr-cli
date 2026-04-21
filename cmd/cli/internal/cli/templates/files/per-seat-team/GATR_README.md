# Per-seat team template

Two plans: capped seats on Starter, unlimited on Business. Per-seat pricing
syncs the seat count to Stripe (server-side, M7).

## Files
- `gatr.yaml` — your pricing config
- `billing.example.ts` — `limit()`, `increment()`, `decrement()` for seats

## Next steps
1. Replace `REPLACE_WITH_STRIPE_PRICE_ID` with your per-seat Stripe price ids.
2. In Stripe, mark these prices as recurring with `usage_type=licensed` and
   `interval=month` so quantity-based billing works.
3. Wire `gatr.increment("seats")` into your "invite teammate" flow.
