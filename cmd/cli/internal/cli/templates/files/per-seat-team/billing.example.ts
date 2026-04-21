import { Gatr } from "@gatr/node";

const gatr = new Gatr({
  mode: "local",
  configPath: "./gatr.yaml",
  users: {
    workspace_acme: {
      plan_id: "starter",
      status: "active",
      limits_used: { seats: 4, workspaces: 1 },
    },
  },
});

// Add a seat
const before = await gatr.limit("workspace_acme", "seats");
if (before.remaining > 0) {
  const after = await gatr.increment("workspace_acme", "seats");
  console.log(`seat added: ${after.used}/${after.limit}`);
} else {
  console.log("seat cap reached — upgrade to business");
}

// Remove a seat
await gatr.decrement("workspace_acme", "seats");
