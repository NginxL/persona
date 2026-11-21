import type { LeaderCounts } from "./direction-names";
import type { TaskPlan } from "./feed";

/** One pending plan adds one request to its source conversation; tasks within a plan are one decision. */
export function pendingPlanCounts(base: ReadonlyMap<string, LeaderCounts>, plans: Record<string, TaskPlan>, manager: string, owned: ReadonlySet<string>): Map<string, LeaderCounts> {
  const counts = new Map(base);
  for (const plan of Object.values(plans)) {
    if (plan.state !== "open" || !!plan.space || (plan.manager ? plan.manager !== manager : !owned.has(plan.conversation))) continue;
    const before = counts.get(plan.conversation) ?? { running: false, needs_you: 0 };
    counts.set(plan.conversation, { ...before, needs_you: before.needs_you + 1 });
  }
  return counts;
}
