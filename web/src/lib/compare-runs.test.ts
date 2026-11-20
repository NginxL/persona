// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import {
  addGroup, compareMenu, compareRunsKey, getGroup, groupOfSession, loadGroups, MAX_GROUPS, resetGroupsCache, runBranches, runSession, runStatus, subscribeGroups, updateRun,
  type CompareGroup, type RunContext,
} from "./compare-runs";
import type { AgentLaunch } from "./new-agent";
import type { Session } from "./types";
import { t } from "../i18n";

const session = (over: Partial<Session> = {}): Session => ({ machine_id: "computer", session_id: "s1", title: "Synthetic run", cwd: "/projects/demo-1", status: "idle", ...over }) as Session;
const group = (over: Partial<CompareGroup> = {}): CompareGroup => ({
  id: "g1", at: 1, mid: "computer", cwd: "/projects/demo", spec: { launcher: "claude" }, attachments: false,
  runs: [{ n: 1, branch: "persona/task-1", phase: "waiting" }, { n: 2, branch: "persona/task-2", phase: "waiting" }],
  ...over,
});
const ctx = (over: Partial<RunContext> = {}): RunContext => ({ sessions: {}, launches: {}, active: false, holds: false, ...over });
const launch = (over: Partial<AgentLaunch>): AgentLaunch => ({ id: "local-1", machineId: "computer", cwd: "/projects/demo-1", launcher: "claude", text: "", attachments: [], phase: "creating", uploaded: {}, startOpId: "a", startPromptOpId: "b", messageId: "c", ...over });

beforeEach(() => {
  localStorage.clear();
  resetGroupsCache();
});
afterEach(() => resetGroupsCache());

describe("runBranches", () => {
  it("numbers the stem once per run and never leaves a doubled separator", () => {
    expect(runBranches("persona/fix-login", 3)).toEqual(["persona/fix-login-1", "persona/fix-login-2", "persona/fix-login-3"]);
    expect(runBranches(" feat/x- ", 2)).toEqual(["feat/x-1", "feat/x-2"]);
  });
});

describe("the list in this browser", () => {
  it("keeps only a group's own fields, never the task's words", () => {
    localStorage.setItem(compareRunsKey(), JSON.stringify([
      { ...group(), text: "Secret task words", runs: [{ n: 1, branch: "b-1", phase: "launched", sid: "s1", prompt: "x" }, { n: 2, branch: "b-2", phase: "failed", error: "Synthetic" }] },
      { id: "broken" },
      { ...group({ id: "one-run" }), runs: [{ n: 1, branch: "b-1", phase: "waiting" }] },
      { ...group({ id: "bad-phase" }), runs: [{ n: 1, branch: "b-1", phase: "nope" }, { n: 2, branch: "b-2", phase: "waiting" }] },
    ]));
    const list = loadGroups();
    expect(list.map((g) => g.id)).toEqual(["g1"]);
    expect(JSON.stringify(list)).not.toContain("Secret task words");
    expect(list[0].runs[0]).toEqual({ n: 1, branch: "b-1", phase: "launched", sid: "s1" });
  });

  it("puts a new group first, keeps the latest MAX_GROUPS, and changes one run in place", () => {
    for (let i = 0; i < MAX_GROUPS + 3; i++) addGroup(group({ id: `g${i}` }));
    resetGroupsCache();
    const list = loadGroups();
    expect(list).toHaveLength(MAX_GROUPS);
    expect(list[0].id).toBe(`g${MAX_GROUPS + 2}`);
    updateRun(`g${MAX_GROUPS + 2}`, 2, { phase: "failed", error: "Synthetic failure" });
    updateRun(`g${MAX_GROUPS + 2}`, 2, { error: undefined });
    resetGroupsCache();
    expect(getGroup(`g${MAX_GROUPS + 2}`)?.runs[1]).toEqual({ n: 2, branch: "persona/task-2", phase: "failed" });
  });

  it("keeps agy's native-effort flag when another tab updates a comparison", () => {
    const spec = { launcher: "agy", launcherDefaultEffort: true };
    addGroup(group({ spec }));
    const key = compareRunsKey();
    const updated = JSON.parse(localStorage.getItem(key)!) as CompareGroup[];
    expect(updated[0].spec).toEqual(spec);
    updated[0].runs[0].phase = "worktree";
    const newValue = JSON.stringify(updated);
    const unsubscribe = subscribeGroups(() => undefined);
    try {
      localStorage.setItem(key, newValue);
      window.dispatchEvent(new StorageEvent("storage", { key, newValue }));
      expect(getGroup("g1")?.runs[0].phase).toBe("worktree");
      expect(getGroup("g1")?.spec).toEqual(spec);
      updateRun("g1", 1, { phase: "launched" });
      expect(loadGroups()[0].spec).toEqual(spec);
    } finally {
      unsubscribe();
    }
  });

  it("finds the comparison a session is a run of, by its id or by its worktree", () => {
    addGroup(group({ runs: [{ n: 1, branch: "b-1", phase: "launched", sid: "s1" }, { n: 2, branch: "b-2", phase: "launched", path: "/projects/demo-2/" }] }));
    expect(groupOfSession(session())?.id).toBe("g1");
    expect(groupOfSession(session({ session_id: "s9", cwd: "/projects/demo-2" }))?.id).toBe("g1");
    expect(groupOfSession(session({ session_id: "s9", cwd: "/projects/elsewhere" }))).toBeUndefined();
    expect(groupOfSession(session({ machine_id: "other" }))).toBeUndefined();
  });

  it("gives the chat menu its item, in the page's own words", () => {
    addGroup(group({ runs: [{ n: 1, branch: "b-1", phase: "launched", sid: "s1" }, { n: 2, branch: "b-2", phase: "waiting" }] }));
    expect(compareMenu(session())).toEqual({ id: "g1", label: t("compare-runs.title") });
    expect(compareMenu(session({ session_id: "s9" }))).toBeUndefined();
  });
});

describe("runSession", () => {
  it("prefers the recorded session, then the computer's session in the run's worktree", () => {
    const sessions = { computer: [session({ session_id: "s2", cwd: "/projects/demo-2", worktree: "/projects/demo-2" }), session()] };
    expect(runSession({ n: 1, branch: "b", phase: "launched", sid: "s1" }, "computer", sessions)?.session_id).toBe("s1");
    expect(runSession({ n: 2, branch: "b", phase: "launched", path: "/projects/demo-2" }, "computer", sessions)?.session_id).toBe("s2");
    expect(runSession({ n: 2, branch: "b", phase: "launched", path: "/projects/demo-2" }, "other", sessions)).toBeUndefined();
  });
});

describe("runStatus", () => {
  const g = group();
  it("says how far a run got while this tab works through it", () => {
    expect(runStatus({ n: 1, branch: "b", phase: "waiting" }, g, ctx({ active: true, holds: true })).kind).toBe("waiting");
    expect(runStatus({ n: 1, branch: "b", phase: "worktree" }, g, ctx({ active: true, holds: true })).kind).toBe("worktree");
  });

  it("follows the start through its launch, and a session found by its folder wins", () => {
    const run = { n: 1, branch: "b", phase: "launched" as const, path: "/projects/demo-1", launch: "local-1" };
    expect(runStatus(run, g, ctx({ launches: { "local-1": launch({ phase: "uploading" }) } })).kind).toBe("starting");
    const ready = runStatus(run, g, ctx({ launches: { "local-1": launch({ phase: "ready", session: session() }) } }));
    expect(ready).toMatchObject({ kind: "started", session: { session_id: "s1" } });
    expect(runStatus(run, g, ctx({ sessions: { computer: [session()] } })).kind).toBe("started");
  });

  it("retries a failed start under its own op ids, but leaves an uncertain one to its page", () => {
    const run = { n: 1, branch: "b", phase: "launched" as const, launch: "local-1" };
    expect(runStatus(run, g, ctx({ launches: { "local-1": launch({ phase: "failed", error: "Refused" }) } }))).toEqual({ kind: "failed", error: "Refused", retry: "launch" });
    expect(runStatus(run, g, ctx({ launches: { "local-1": launch({ phase: "failed", error: "?", uncertain: true }) } }))).toEqual({ kind: "failed", error: "?", retry: null, uncertain: true });
  });

  it("after a reload: a start sent waits for its session, a run not begun is stopped, retried only while the tab holds the task", () => {
    expect(runStatus({ n: 1, branch: "b", phase: "launched", launch: "local-gone" }, g, ctx()).kind).toBe("elsewhere");
    expect(runStatus({ n: 1, branch: "b", phase: "worktree" }, g, ctx())).toEqual({ kind: "stopped", retry: false });
    expect(runStatus({ n: 1, branch: "b", phase: "waiting" }, g, ctx({ holds: true }))).toEqual({ kind: "stopped", retry: true });
    expect(runStatus({ n: 1, branch: "b", phase: "failed", error: "No repo" }, g, ctx())).toEqual({ kind: "failed", error: "No repo", retry: null });
    expect(runStatus({ n: 1, branch: "b", phase: "failed", error: "No repo" }, g, ctx({ holds: true }))).toEqual({ kind: "failed", error: "No repo", retry: "run" });
  });
});
