import { describe, expect, it } from "vitest";
import { buildMachineTree, machineSessionOrder, machineSessionPresentation } from "./herdr-tree";
import { catalogGroups, countGroups, type SessionCatalog } from "./session-groups";
import { mergeStableRows } from "./side-sessions";
import type { ListCounts } from "./session-list";
import type { Machine, Session } from "./types";

const machines: Machine[] = [
  { id: "mac", name: "Mac", online: true },
  { id: "pc", name: "PC", online: true },
];

function counts(mac: number, pc: number): ListCounts {
  return { total: mac + pc, revision: "r", machine: { mac, pc }, project: {}, group: {}, state: {} };
}

const agent = (id: string, machine_id: string, space_id: string, slot_id: string, slot_index: number, hour: number): Session => ({
  machine_id, session_id: id, title: id, cwd: `/${space_id}`, status: "idle", state: "idle",
  space_id, space_name: space_id, space_index: space_id === "alpha" ? 0 : 1,
  slot_id, slot_index, updated_at: `2026-01-01T${String(hour).padStart(2, "0")}:00:00Z`,
});

const initial = [
  agent("a1", "mac", "alpha", "a1", 0, 1),
  agent("a2", "mac", "alpha", "a2", 1, 2),
  agent("b1", "mac", "beta", "b1", 0, 3),
  agent("c1", "pc", "gamma", "c1", 0, 4),
];
const refreshed = initial.map((s) => s.session_id === "a1" ? { ...s, updated_at: "2026-01-01T10:00:00Z" } : s);

function treeOrder(sessions: Session[]) {
  return buildMachineTree(machines, sessions).map((machine) => ({
    machine: machine.machine.id,
    workspaces: machine.spaces.map((space) => ({ workspace: space.name, tabs: space.tabs.map((tab) => tab.slotId) })),
  }));
}

describe("stable sidebar order (#11801)", () => {
  it("does not move paged machine groups when activity changes their counts", () => {
    const before = countGroups(counts(2, 1), "machine").map((group) => group.key);
    const after = countGroups(counts(2, 3), "machine").map((group) => group.key);
    expect(after).toEqual(before);
  });

  it("does not move legacy machines, workspaces or tabs when an Agent updates", () => {
    expect(treeOrder(refreshed)).toEqual(treeOrder(initial));
  });

  it("gives paged rows the same herdr order after activity and a reversed transport reload", () => {
    const order = machineSessionOrder(machines);
    const ids = (sessions: Session[]) => [...sessions].sort(order).map((s) => s.session_id);
    expect(ids(initial)).toEqual(["a1", "a2", "b1", "c1"]);
    expect(ids(refreshed)).toEqual(ids(initial));
    expect(ids([...initial].reverse())).toEqual(ids(initial));
  });

  it("puts an unindexed workspace before Other in both paged and legacy placement", () => {
    const base = { machine_id: "mac", title: "", cwd: "/tmp", status: "idle" as const, state: "idle" as const };
    const spaced: Session = { ...base, session_id: "spaced", space_kind: "paseo", space_id: "prj_app", space_name: "app" };
    const loose: Session = { ...base, session_id: "loose" };
    const flat = [spaced, loose].sort(machineSessionOrder(machines)).map((s) => s.session_id);
    const tree = buildMachineTree(machines, [loose, spaced])[0]!;
    const legacy = [...tree.spaces.flatMap((space) => space.sessions), ...tree.other].map((s) => s.session_id);
    expect(flat).toEqual(["spaced", "loose"]);
    expect(legacy).toEqual(flat);
  });

  it("puts an unindexed tab before loose sessions in its workspace in both paged and legacy placement", () => {
    const base = { machine_id: "mac", title: "", cwd: "/tmp", status: "idle" as const, state: "idle" as const, space_id: "alpha", space_name: "alpha" };
    const tab: Session = { ...base, session_id: "tab", slot_id: "tab" };
    const loose: Session = { ...base, session_id: "loose" };
    const flat = [loose, tab].sort(machineSessionOrder(machines)).map((s) => s.session_id);
    const tree = buildMachineTree(machines, [loose, tab])[0]!;
    const legacy = tree.spaces[0]!.sessions.map((s) => s.session_id);
    expect(flat).toEqual(["tab", "loose"]);
    expect(legacy).toEqual(flat);
  });

  it("reconciles real herdr slot and pane moves, created tabs and reloads without following activity", () => {
    const rows = [
      { ...agent("a1", "mac", "alpha", "tab-a", 0, 1), pane_index: 0 },
      { ...agent("a2", "mac", "alpha", "tab-a", 0, 2), pane_index: 1 },
      { ...agent("b1", "mac", "alpha", "tab-b", 1, 3), pane_index: 0 },
    ];
    const order = machineSessionPresentation(machines);
    const key = (row: Session) => row.session_id;
    const ids = (rows: readonly Session[]) => rows.map(key);
    const merge = (previous: readonly Session[], current: Session[], added?: ReadonlySet<string>) => mergeStableRows(
      previous, [...current].reverse(), new Map(current.map((row) => [key(row), row])), key, order, undefined, added);

    const initialPresentation = merge([], rows);
    expect(ids(initialPresentation)).toEqual(["a1", "a2", "b1"]);
    const active = rows.map((row) => row.session_id === "a1" ? { ...row, state: "working" as const, status: "working", updated_at: "2026-01-01T10:00:00Z" } : row);
    const afterActivity = merge(initialPresentation, active);
    expect(ids(afterActivity)).toEqual(["a1", "a2", "b1"]);
    expect(afterActivity[0]!.state).toBe("working");

    const moved = afterActivity.map((row) => row.slot_id === "tab-a"
      ? { ...row, slot_index: 1, pane_index: row.session_id === "a1" ? 1 : 0 }
      : { ...row, slot_index: 0 });
    const afterMove = merge(afterActivity, moved);
    expect(ids(afterMove)).toEqual(["b1", "a2", "a1"]);
    expect(ids(merge([], moved))).toEqual(ids(afterMove));

    const created = { ...agent("first", "mac", "alpha", "aa-new", 0, 4), pane_index: 0 };
    const withFirst = [...moved, created];
    expect(ids(merge(afterMove, withFirst, new Set(["first"])))).toEqual(["first", "b1", "a2", "a1"]);
    expect(ids(merge([], withFirst))).toEqual(["first", "b1", "a2", "a1"]);
  });

  it("follows a real machine-order change instead of previous row positions", () => {
    const rows = [agent("mac-row", "mac", "alpha", "tab", 0, 1), agent("pc-row", "pc", "gamma", "tab", 0, 2)];
    const key = (row: Session) => row.session_id;
    const candidates = new Map(rows.map((row) => [key(row), row]));
    const previous = mergeStableRows([], rows, candidates, key, machineSessionPresentation(machines));
    expect(previous.map(key)).toEqual(["mac-row", "pc-row"]);

    const reversedMachines = [...machines].reverse();
    const order = machineSessionPresentation(reversedMachines);
    const after = mergeStableRows(previous, [...rows].reverse(), candidates, key, order, undefined, new Set());
    expect(after.map(key)).toEqual(["pc-row", "mac-row"]);
    expect(mergeStableRows([], rows, candidates, key, order).map(key)).toEqual(after.map(key));
  });

  it("does not move catalog projects or workspaces when their counts or hub order changes", () => {
    const catalog = (busy: boolean): SessionCatalog => ({
      revision: "r", total: 10,
      projects: [
        { id: "z", name: "Zulu", total: busy ? 9 : 1, places: [{ machine_id: "mac", path: "/z", total: busy ? 9 : 1 }] },
        { id: "a", name: "Alpha", total: busy ? 1 : 9, places: [{ machine_id: "mac", path: "/a", total: busy ? 1 : 9 }] },
      ],
      spaces: [
        { machine_id: "mac", id: "z", name: "Zulu", total: busy ? 9 : 1 },
        { machine_id: "mac", id: "a", name: "Alpha", total: busy ? 1 : 9 },
      ],
    });
    for (const dimension of ["project", "group"] as const) {
      const before = catalogGroups(catalog(false), dimension).map((g) => g.key);
      const reversed = catalog(false);
      reversed.projects.reverse();
      reversed.spaces.reverse();
      expect(catalogGroups(reversed, dimension).map((g) => g.key)).toEqual(before);
      expect(catalogGroups(catalog(true), dimension).map((g) => g.key)).toEqual(before);
      expect(before).toEqual(["a", "z"].map((id) => dimension === "project" ? id : JSON.stringify(["mac", id])));
    }
  });
});
