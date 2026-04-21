import { Gatr } from "@gatr/node";

const gatr = new Gatr({
  mode: "local",
  configPath: "./gatr.yaml",
  users: {
    user_alice: { plan_id: "free", status: "active", limits_used: { projects: 1 } },
    user_bob: { plan_id: "pro", status: "active", limits_used: { projects: 5 } },
  },
});

// Feature gate
if (await gatr.can("user_alice", "export_pdf")) {
  console.log("alice can export");
} else {
  console.log("alice cannot export — upgrade to pro");
}

// Numeric limit
const alicesProjects = await gatr.limit("user_alice", "projects");
if (alicesProjects.remaining > 0) {
  await gatr.increment("user_alice", "projects");
  console.log("alice created a new project");
}

// Plan info
console.log(await gatr.plan("user_bob"));
