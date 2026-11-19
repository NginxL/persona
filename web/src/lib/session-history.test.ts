import { describe, expect, it } from "vitest";
import { historyGroups, historySessions } from "./session-history";
import type { Session } from "./types";
const row = (session_id: string, extra: Partial<Session> = {}): Session => ({ machine_id: "computer", session_id, status: "offline", state: "offline", ...extra } as Session);
describe("History (#10240)", () => {
  it("uses the folded sidebar state and excludes running, pending, health, deleted and assistant rows", () => {
    const rows = [row("stopped"), row("running", { status: "idle", state: "idle" }), row("working", { state: "working" }),
      row("pending", { state: "needs_you" }), row("parent", { children: { working: 1 } as Session["children"] }),
      row("health", { space: "health" }), row("deleted", { deleted: true }), row("assistant", { assistant: true })];
    expect(historySessions(rows).map(s => s.session_id)).toEqual(["stopped"]);
  });
  it("folds children before filtering, so a child never becomes a second history row", () => {
    expect(historySessions([row("parent"), row("child", { folded_under: "parent" })]).map(s => s.session_id)).toEqual(["parent"]);
  });
  it("filters Computer and Agent before making day counts, with stable newest-first ordering", () => {
    const rows = [row("old", { updated_at: "2026-11-15T12:00:00Z", agent: "codex" }), row("new", { updated_at: "2026-11-16T12:00:00Z", agent: "codex" }),
      row("other-agent", { updated_at: "2026-11-16T12:01:00Z", agent: "claude" }), row("other-computer", { machine_id: "other", agent: "codex" })];
    expect(historyGroups(historySessions(rows, { machine: "computer", agent: "codex" })).map(g => g.sessions.map(s => s.session_id))).toEqual([["new"], ["old"]]);
  });
  it("uses local calendar dates across a UTC midnight and keeps unknown dates last", () => {
    const groups = historyGroups([row("unknown"), row("earlier", { updated_at: new Date(2026, 10, 15, 23, 55).toISOString() }), row("local", { updated_at: new Date(2026, 10, 16, 0, 5).toISOString() })]);
    expect(groups.map(g => g.key)).toEqual(["2026-11-16", "2026-11-15", "unknown"]);
  });
  it("a resumed authoritative row leaves History immediately", () => {
    expect(historySessions([row("resumed", { status: "idle", state: "idle" })])).toEqual([]);
  });
});
