// lib/background-panel.ts (#5421): what /workflows opens at, and the chat page's actions the panel reads.
import { describe, expect, it } from "vitest";
import { backgroundChat, hasBackgroundWork, isWorkflowsCommand, latestWorkflowRun, provideBackgroundChat } from "./background-panel";
import type { Session, Workflow } from "./types";

const NOW = Date.parse("2026-11-12T10:00:00Z");
const ago = (h: number) => new Date(NOW - h * 3_600_000).toISOString();
const run = (task_id: string, over: Partial<Workflow>): Workflow => ({ task_id, status: "completed", running: false, ...over });
const session = (workflows: Workflow[]) => ({ machine_id: "m", session_id: "s", workflows }) as Session;

describe("latestWorkflowRun", () => {
  it("takes the latest running run, else the latest that started", () => {
    const runs = [run("a", { started_at: ago(3), ended_at: ago(2) }), run("b", { running: true, status: "running", started_at: ago(5) }), run("c", { started_at: ago(1), ended_at: ago(0.5) })];
    expect(latestWorkflowRun(session(runs), NOW)).toBe("b");
    expect(latestWorkflowRun(session(runs.filter((r) => r.task_id !== "b")), NOW)).toBe("c");
  });
  it("never a run the panel does not list (ended over a day ago)", () => {
    expect(latestWorkflowRun(session([run("old", { started_at: ago(80), ended_at: ago(72) })]), NOW)).toBeUndefined();
    expect(latestWorkflowRun(undefined, NOW)).toBeUndefined();
  });
});

describe("the chat page's actions", () => {
  it("answer with the page that came last, and none once every page has gone", () => {
    const one = { stopLoop: () => {} };
    const two = { stopLoop: () => {} };
    const takeOne = provideBackgroundChat("m", "s", one);
    const takeTwo = provideBackgroundChat("m", "s", two);
    expect(backgroundChat("m", "s")).toBe(two);
    takeTwo();
    expect(backgroundChat("m", "s")).toBe(one);
    takeOne();
    expect(backgroundChat("m", "s")).toBeUndefined();
  });
});

describe("the rest", () => {
  it("takes /workflows alone, without pictures", () => {
    expect(isWorkflowsCommand(" /Workflows ", 0)).toBe(true);
    expect(isWorkflowsCommand("/workflows", 1)).toBe(false);
    expect(isWorkflowsCommand("/workflows please", 0)).toBe(false);
  });
  it("has background work with a loop, a task or a recent run", () => {
    expect(hasBackgroundWork(undefined, NOW)).toBe(false);
    expect(hasBackgroundWork(session([]), NOW)).toBe(false);
    expect(hasBackgroundWork({ ...session([]), loop: { kind: "interval" } } as Session, NOW)).toBe(true);
    expect(hasBackgroundWork({ ...session([]), background: [{ id: "b", kind: "shell" }] } as Session, NOW)).toBe(true);
    expect(hasBackgroundWork({ ...session([]), background_ended: [{ id: "b", kind: "shell", status: "completed" }] } as Session, NOW)).toBe(true);
    expect(hasBackgroundWork(session([run("old", { started_at: ago(80), ended_at: ago(72) })]), NOW)).toBe(false);
  });
});
