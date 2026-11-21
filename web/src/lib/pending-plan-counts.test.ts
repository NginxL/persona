import { expect, it } from "vitest";
import { pendingPlanCounts } from "./pending-plan-counts";
import { totalCounts } from "./direction-names";
import type { TaskPlan } from "./feed";

it("counts each open plan once, including unlisted routine sources, within its manager", () => {
  const plan = (id: string, conversation: string, manager?: string, state: TaskPlan["state"] = "open"): TaskPlan => ({ id, conversation, manager, state, tasks: [] });
  const base = new Map([["lead", { running: true, needs_you: 2 }]]);
  const plans = { lead: plan("lead-plan", "lead", "home"), routine: plan("routine-plan", "unlisted-routine", "home"),
    other: plan("other", "work-chat", "work"), legacy: plan("legacy", "known"), unknown: plan("unknown", "unknown"),
    health: { ...plan("health", "health-chat", "home"), space: "health" },
    done: plan("done", "lead", "home", "dismissed") };
  const got = pendingPlanCounts(base, plans, "home", new Set(["lead", "known"]));
  expect(got.get("lead")).toEqual({ running: true, needs_you: 3 });
  expect(got.get("unlisted-routine")?.needs_you).toBe(1);
  expect(got.get("known")?.needs_you).toBe(1);
  expect(got.has("work-chat")).toBe(false);
  expect(got.has("unknown")).toBe(false);
  expect(got.has("health-chat")).toBe(false);
  expect(totalCounts(got, [...got.keys()])).toEqual({ running: true, needs_you: 5 });
  expect(base.get("lead")?.needs_you).toBe(2);
});
