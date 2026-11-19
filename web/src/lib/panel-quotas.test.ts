import { describe, expect, it } from "vitest";
import { localLauncherProvider, localSubscriptionQuota } from "./panel-quotas";
import { weeklyQuota } from "./quota";
import type { UsageAccount } from "./usage-windows";

describe("localSubscriptionQuota", () => {
  const now = new Date(Date.now() - 60_000).toISOString();
  const future = new Date(Date.now() + 3 * 86400_000).toISOString();
  const sample: UsageAccount = {
    id: "codex-1",
    provider: "codex",
    name: "Codex Pro",
    plan: "pro",
    local: true,
    machines: ["MacBook"],
    sampled_at: now,
    windows: [{ label: "7d", window_minutes: 10080, used_percent: 75, resets_at: future }],
  };

  it("maps an unambiguous local subscription window to the model panel rail's weekly quota", () => {
    const q = localSubscriptionQuota([sample], "codex", "MacBook");
    expect(q?.quota?.gateway_name).toBe("Codex Pro");
    const w = weeklyQuota(q);
    expect(w).toMatchObject({ remaining: 25, tone: "orange" });
    expect(w?.window.label).toBe("7d");
  });

  it("matches named local launchers via their agent or agent_type", () => {
    const claude: UsageAccount = { ...sample, id: "claude-1", provider: "claude", name: "Claude Max" };
    const provider = localLauncherProvider("claude-work", { name: "claude-work", agent: "claude" });
    expect(provider).toBe("claude");
    expect(localSubscriptionQuota([claude], provider, "MacBook")?.quota?.gateway_name).toBe("Claude Max");
    expect(localLauncherProvider("work-codex", { name: "work-codex", agent_type: "Codex" })).toBe("codex");
  });

  it("refuses to guess when two local accounts match or the machine differs", () => {
    expect(localSubscriptionQuota([sample], "codex", "OtherMac")).toBeUndefined();
    expect(localSubscriptionQuota([sample, { ...sample, id: "codex-2" }], "codex", "MacBook")).toBeUndefined();
    expect(localSubscriptionQuota([{ ...sample, stale: true }], "codex", "MacBook")).toBeUndefined();
  });
});
