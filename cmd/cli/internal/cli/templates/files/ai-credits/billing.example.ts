import { Gatr } from "@gatr/node";

const gatr = new Gatr({
  mode: "local",
  configPath: "./gatr.yaml",
  users: {
    user_42: { plan_id: "starter", status: "active", limits_used: {} },
  },
});

// `consume()` lands in M4. For M1 you can still introspect the plan:
const plan = await gatr.plan("user_42");
console.log(`user_42 is on ${plan.plan_id} (${plan.status})`);

// In M4+ you'll write:
//
//   const result = await gatr.consume("user_42", "chat_advanced");
//   if (!result.ok) {
//     return res.status(402).json({ reason: "out_of_credits", balance: result.balance });
//   }
