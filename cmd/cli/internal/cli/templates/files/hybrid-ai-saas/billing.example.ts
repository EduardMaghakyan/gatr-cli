import { Gatr } from "@gatr/node";

const gatr = new Gatr({
  mode: "local",
  configPath: "./gatr.yaml",
  users: {
    workspace_42: {
      plan_id: "pro",
      billing_interval: "monthly",
      status: "active",
      limits_used: { seats: 3 },
    },
  },
});

// Feature gate (M1)
if (await gatr.can("workspace_42", "export_pdf")) {
  console.log("can export");
}

// Per-seat limit (M1)
const seats = await gatr.limit("workspace_42", "seats");
console.log(`seats: ${seats.used}/${seats.limit}`);

// Credits — `consume()` lands in M4
// const debit = await gatr.consume("workspace_42", "chat_advanced");

// Metered API — `track()` lands in M5
// await gatr.track("workspace_42", "api_calls", 1);

// Plan info (M1)
console.log(await gatr.plan("workspace_42"));
