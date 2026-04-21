# AI credits template

Pre-paid credit pool with costed operations. Each model call debits credits
atomically. Designed for AI/LLM products.

## Files
- `gatr.yaml` — three operations (chat_basic, chat_advanced, image_gen)
- `billing.example.ts` — placeholder; full credits API lands in M4

## Next steps
1. Replace `REPLACE_WITH_STRIPE_PRICE_ID` with your subscription price ids.
2. Decide your credit costs per operation (currently 1 / 10 / 50). These should
   roughly track your underlying model costs.
3. The `gatr.consume()` API lands in M4 of the v1 plan; until then `gatr.yaml`
   is parseable and your plan grants are validated.
