import { describe, expect, it } from "vitest";
import { planEditsChanged, planEditsOf, planEditsToSend, planUltracodeToSend, type TaskPlan } from "./feed";

const plan: TaskPlan = {
  id: "p1",
  conversation: "c1",
  state: "open",
  tasks: [
    { title: "补测试", prompt: "给登录补测试", machine: "m1", cwd: "/work/app" },
    { title: "改文档", prompt: "更新文档", machine: "m2", cwd: "/work/docs", model: "opus", permission: "acceptEdits", ultracode: true },
  ],
};

describe("改一下 on a plan card", () => {
  it("starts from every task's fields, an unset permission as 默认权限 and ultracode as the assistant chose", () => {
    expect(planEditsOf(plan)).toEqual([
      { title: "补测试", machine: "m1", cwd: "/work/app", model: "", permission: "default", prompt: "给登录补测试", ultracode: false },
      { title: "改文档", machine: "m2", cwd: "/work/docs", model: "opus", permission: "acceptEdits", prompt: "更新文档", ultracode: true },
    ]);
  });

  it("starts from the card's own ultracode choice where it has one", () => {
    expect(planEditsOf(plan, [true, false]).map((e) => e.ultracode)).toEqual([true, false]);
  });

  it("knows when something changed since it opened", () => {
    const before = planEditsOf(plan);
    const edits = planEditsOf(plan);
    expect(planEditsChanged(before, edits)).toBe(false);
    edits[1] = { ...edits[1], cwd: "/work/site" };
    expect(planEditsChanged(before, edits)).toBe(true);
    const flipped = planEditsOf(plan);
    flipped[0] = { ...flipped[0], ultracode: true };
    expect(planEditsChanged(before, flipped)).toBe(true);
  });

  it("sends the edits, leaving out a 默认权限 the plan never set", () => {
    const edits = planEditsOf(plan);
    edits[0] = { ...edits[0], title: "补登录测试" };
    const sent = planEditsToSend(plan, edits);
    expect(sent[0]).toEqual({ title: "补登录测试", machine: "m1", cwd: "/work/app", model: "", prompt: "给登录补测试" });
    expect(sent[1].permission).toBe("acceptEdits");
    edits[0] = { ...edits[0], permission: "plan" };
    expect(planEditsToSend(plan, edits)[0].permission).toBe("plan");
  });

  it("sends ultracode only where it differs from the assistant's, so the hub keeps the rest", () => {
    const edits = planEditsOf(plan);
    expect(planEditsToSend(plan, edits).map((e) => "ultracode" in e)).toEqual([false, false]);
    edits[0] = { ...edits[0], ultracode: true };
    edits[1] = { ...edits[1], ultracode: false };
    expect(planEditsToSend(plan, edits).map((e) => e.ultracode)).toEqual([true, false]);
  });

  it("sends ultracode off for a task on a computer that can't run it, wherever the choice came from", () => {
    const old = (machine: string) => machine === "m2";
    const edits = planEditsOf(plan); // the assistant turned it on for the task on m2
    expect(planEditsToSend(plan, edits, old)[1].ultracode).toBe(false);
    edits[1] = { ...edits[1], machine: "m1" }; // moved to a computer that can: the assistant's again
    expect(planEditsToSend(plan, edits, old)[1]).not.toHaveProperty("ultracode");
    edits[0] = { ...edits[0], machine: "m2", ultracode: true }; // turned on where it can't: as the assistant left it
    expect(planEditsToSend(plan, edits, old)[0]).not.toHaveProperty("ultracode");
  });
});

describe("Start on a plan card", () => {
  it("starts the plan as it is while the card changed no ultracode", () => {
    expect(planUltracodeToSend(plan, [false, true])).toBeUndefined();
  });

  it("sends true / false for the tasks the card changed and nothing for the others", () => {
    expect(planUltracodeToSend(plan, [true, true])).toEqual([{ ultracode: true }, {}]);
    expect(planUltracodeToSend(plan, [false, false])).toEqual([{}, { ultracode: false }]);
  });

  it("sends it off for a task on a computer that can't run it", () => {
    expect(planUltracodeToSend(plan, [false, true], (machine) => machine === "m2")).toEqual([{}, { ultracode: false }]);
    expect(planUltracodeToSend(plan, [true, true], (machine) => machine === "m1")).toBeUndefined();
  });
});
