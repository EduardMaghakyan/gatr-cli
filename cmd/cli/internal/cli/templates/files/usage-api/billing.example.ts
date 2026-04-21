import { Gatr } from "@gatr/node";

const gatr = new Gatr({
  mode: "local",
  configPath: "./gatr.yaml",
  users: {
    customer_42: { plan_id: "developer", status: "active", limits_used: {} },
  },
});

const plan = await gatr.plan("customer_42");
console.log(`customer_42 is on ${plan.plan_id}`);

// `track()` and `usage()` land in M5. For M1 you can validate the config and
// inspect plans. In M5+ you'll write:
//
//   await gatr.track("customer_42", "api_calls", 1);
//   const u = await gatr.usage("customer_42", "api_calls");
//   // → { used: 12345, included: 10000, overage: 2345, estimated_cost: 2.34, ... }
