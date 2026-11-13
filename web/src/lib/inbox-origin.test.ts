import { describe, expect, it } from "vitest";
import { inboxOriginForSession, type InboxOriginInput } from "./inbox-origin";
import type { InboxItem } from "./inbox";
import type { AssistantInfo, Session } from "./types";

const MID = "machine-a";
const SID = "current";

const session = (sid: string, extra: Partial<Session> = {}): Session => ({
  machine_id: MID,
  session_id: sid,
  title: `Session ${sid}`,
  cwd: "/work",
  status: "idle",
  ...extra,
});

const item = (id: string, extra: Partial<InboxItem> = {}): InboxItem => ({
  id,
  source: "connector",
  kind: "dm",
  account: "account",
  account_name: "Account",
  title: `Title ${id}`,
  preview: "",
  count: 1,
  ...extra,
});

const assistant = (conversations: Session[]): AssistantInfo => ({ machine_id: MID, conversations });

const resolve = (current: Session | undefined, extra: Partial<InboxOriginInput> = {}) =>
  inboxOriginForSession(current?.machine_id ?? MID, current?.session_id ?? SID, {
    session: current,
    sessions: {},
    assistant: null,
    items: [],
    history: [],
    ...extra,
  });

const chainItem = (id: string, mid = MID, sid = SID, extra: Partial<InboxItem> = {}): InboxItem => item(id, {
  ...extra,
  chain: [{ id: `${id}-link`, kind: "session", text: "", link: { type: "session", machine_id: mid, session_id: sid } }],
});

const handlingItem = (id: string, mid = MID, sid = SID, extra: Partial<InboxItem> = {}): InboxItem => item(id, {
  ...extra,
  handling: { state: "agent", machine_id: mid, session_id: sid },
});

describe("inboxOriginForSession", () => {
  it("uses a direct inbox origin and prefers a duplicate item with the session manager", () => {
    const current = session(SID, { origin: "inbox", origin_ref: "matter", manager: "manager-a" });
    const items = [
      item("matter", { manager: "manager-b", title: "Other manager" }),
      item("matter", { manager: "manager-a", title: "Preferred title" }),
    ];
    expect(resolve(current, { items })).toEqual({ id: "matter", manager: "manager-a", title: "Preferred title" });
  });

  it("treats empty and missing managers as the default for duplicate item and parent ids", () => {
    const direct = session(SID, { origin: "inbox", origin_ref: "matter" });
    const items = [
      item("matter", { manager: "manager-b", title: "Other manager" }),
      item("matter", { manager: "", title: "Default manager" }),
    ];
    expect(resolve(direct, { items })).toEqual({ id: "matter", manager: "", title: "Default manager" });

    const spawned = session(SID, { origin: "spawned", origin_ref: "parent", manager: "" });
    const parents = [
      session("parent", { manager: "manager-b", origin: "inbox", origin_ref: "other-manager" }),
      session("parent", { origin: "inbox", origin_ref: "default-manager" }),
    ];
    expect(resolve(spawned, { sessions: { [MID]: parents } })).toEqual({
      id: "default-manager", manager: undefined, title: undefined,
    });
  });

  it("takes a direct origin's title from done history", () => {
    const current = session(SID, { origin: "inbox", origin_ref: "done", manager: "manager-a" });
    const history = [item("done", { manager: "manager-a", title: "Done title" })];
    expect(resolve(current, { history })).toEqual({ id: "done", manager: "manager-a", title: "Done title" });
  });

  it("keeps a direct origin when its item is not loaded, without borrowing a session title", () => {
    const current = session(SID, { origin: "inbox", origin_ref: "unloaded", manager: "manager-a", title: "Session title" });
    expect(resolve(current)).toEqual({ id: "unloaded", manager: "manager-a", title: undefined });
  });

  it("ignores empty and malformed origin references", () => {
    for (const origin_ref of ["", "   ", 7 as unknown as string]) {
      expect(resolve(session(SID, { origin: "inbox", origin_ref }))).toBeUndefined();
      expect(resolve(session(SID, { origin: "spawned", origin_ref }))).toBeUndefined();
    }
  });

  it("resolves a spawned chat through a parent in assistant conversations", () => {
    const current = session(SID, { origin: "spawned", origin_ref: "parent", manager: "manager-a" });
    const parent = session("parent", { origin: "inbox", origin_ref: "matter", manager: "manager-a" });
    const items = [item("matter", { manager: "manager-a", title: "Parent item" })];
    expect(resolve(current, { assistant: assistant([parent]), items })).toEqual({
      id: "matter", manager: "manager-a", title: "Parent item",
    });
  });

  it("prefers the current machine and same manager for duplicate parent session ids", () => {
    const current = session(SID, { origin: "spawned", origin_ref: "parent", manager: "manager-a" });
    const parents = [
      session("parent", { machine_id: "machine-b", manager: "manager-a", origin: "inbox", origin_ref: "other-machine" }),
      session("parent", { manager: "manager-b", origin: "inbox", origin_ref: "other-manager" }),
      session("parent", { manager: "manager-a", origin: "inbox", origin_ref: "preferred" }),
    ];
    expect(resolve(current, { sessions: { [MID]: parents.slice(1), "machine-b": parents.slice(0, 1) } })).toEqual({
      id: "preferred", manager: "manager-a", title: undefined,
    });
  });

  it("uses the current runtime row instead of a listed row with the same identity", () => {
    const current = session(SID, { origin: "inbox", origin_ref: "runtime" });
    const listed = session(SID, { origin: "inbox", origin_ref: "listed" });
    const items = [item("runtime", { title: "Runtime" }), item("listed", { title: "Listed" })];
    expect(resolve(current, { sessions: { [MID]: [listed] }, items })).toEqual({
      id: "runtime", manager: undefined, title: "Runtime",
    });
  });

  it("follows three parent links", () => {
    const current = session(SID, { origin: "spawned", origin_ref: "parent-1" });
    const parent1 = session("parent-1", { origin: "spawned", origin_ref: "parent-2" });
    const parent2 = session("parent-2", { origin: "spawned", origin_ref: "parent-3" });
    const parent3 = session("parent-3", { origin: "inbox", origin_ref: "root-item" });
    expect(resolve(current, {
      sessions: { [MID]: [parent1, parent2] },
      assistant: assistant([parent3]),
      items: [item("root-item")],
    })).toEqual({ id: "root-item", manager: undefined, title: "Title root-item" });
  });

  it("does not follow a fourth parent link", () => {
    const current = session(SID, { origin: "spawned", origin_ref: "parent-1" });
    const parents = [
      session("parent-1", { origin: "spawned", origin_ref: "parent-2" }),
      session("parent-2", { origin: "spawned", origin_ref: "parent-3" }),
      session("parent-3", { origin: "spawned", origin_ref: "parent-4" }),
      session("parent-4", { origin: "inbox", origin_ref: "too-far" }),
    ];
    expect(resolve(current, { sessions: { [MID]: parents } })).toBeUndefined();
  });

  it("stops safely at parent loops and missing parents", () => {
    const current = session(SID, { origin: "spawned", origin_ref: "parent-1" });
    const parents = [
      session("parent-1", { origin: "spawned", origin_ref: "parent-2" }),
      session("parent-2", { origin: "spawned", origin_ref: "parent-1" }),
    ];
    expect(resolve(current, { sessions: { [MID]: parents } })).toBeUndefined();
    expect(resolve(session(SID, { origin: "spawned", origin_ref: "missing" }))).toBeUndefined();
  });

  it("does not apply chain or handling fallback to an ancestor", () => {
    const current = session(SID, { origin: "spawned", origin_ref: "parent" });
    const parent = session("parent");
    expect(resolve(current, {
      sessions: { [MID]: [parent] },
      items: [chainItem("ancestor-chain", MID, "parent"), handlingItem("ancestor-handling", MID, "parent")],
    })).toBeUndefined();
  });

  it("requires both machine and session id for a chain session link", () => {
    const current = session(SID);
    const items = [
      chainItem("wrong-machine", "machine-b", SID, { updated_at: "2026-11-13T12:00:00Z" }),
      chainItem("wrong-session", MID, "other", { updated_at: "2026-11-13T12:00:00Z" }),
      chainItem("exact", MID, SID, { updated_at: "2026-11-13T10:00:00Z" }),
    ];
    expect(resolve(current, { items })).toEqual({ id: "exact", manager: undefined, title: "Title exact" });
  });

  it("matches an item's handling machine and session id", () => {
    const current = session(SID);
    const items = [handlingItem("wrong", "machine-b", SID), handlingItem("handled")];
    expect(resolve(current, { items })).toEqual({ id: "handled", manager: undefined, title: "Title handled" });
  });

  it("chooses the newest updated_at among matching open and historical items", () => {
    const current = session(SID);
    const items = [
      handlingItem("old", MID, SID, { updated_at: "2026-11-13T10:00:00Z" }),
      chainItem("newest", MID, SID, { updated_at: "2026-11-13T12:00:00Z" }),
      handlingItem("invalid", MID, SID, { updated_at: "not-a-date" }),
      handlingItem("missing-date"),
    ];
    const history = [handlingItem("middle", MID, SID, { updated_at: "2026-11-13T11:00:00Z" })];
    expect(resolve(current, { items, history })?.id).toBe("newest");
  });

  it("treats invalid dates as oldest and keeps input order for ties", () => {
    const current = session(SID);
    const invalid = [handlingItem("first-invalid", MID, SID, { updated_at: "bad" }), handlingItem("first-missing")];
    expect(resolve(current, { items: invalid })?.id).toBe("first-invalid");
    const tied = [
      handlingItem("first-tied", MID, SID, { updated_at: "2026-11-13T10:00:00Z" }),
      chainItem("second-tied", MID, SID, { updated_at: "2026-11-13T10:00:00Z" }),
    ];
    expect(resolve(current, { items: tied })?.id).toBe("first-tied");
  });

  it("keeps chain and handling matches within a stamped session's manager", () => {
    const current = session(SID, { manager: "manager-a" });
    const own = handlingItem("own", MID, SID, { manager: "manager-a", updated_at: "2026-11-13T10:00:00Z" });
    const other = chainItem("other", MID, SID, { manager: "manager-b", updated_at: "2026-11-13T12:00:00Z" });
    expect(resolve(current, { items: [own, other] })?.id).toBe("own");
    expect(resolve(current, { items: [other] })).toBeUndefined();
  });

  it("prefers a default-manager linked item for an unstamped session without losing exact-link compatibility", () => {
    const current = session(SID);
    const fallback = handlingItem("default", MID, SID, { manager: "", updated_at: "2026-11-13T10:00:00Z" });
    const other = chainItem("other", MID, SID, { manager: "manager-b", updated_at: "2026-11-13T12:00:00Z" });
    expect(resolve(current, { items: [fallback, other] })?.id).toBe("default");
    // An older hub may not stamp the session's manager: the exact handling link is still authoritative on its own.
    expect(resolve(current, { items: [other] })?.id).toBe("other");
  });

  it("returns an undefined title when the matching item has none", () => {
    const current = session(SID);
    const items = [handlingItem("untitled", MID, SID, { title: "", manager: "manager-a" })];
    expect(resolve(current, { items })).toEqual({ id: "untitled", manager: "manager-a", title: undefined });
  });

  it("prioritizes direct and spawned origins over chain fallback", () => {
    const direct = session(SID, { origin: "inbox", origin_ref: "direct" });
    expect(resolve(direct, { items: [item("direct"), chainItem("chain")] })?.id).toBe("direct");

    const spawned = session(SID, { origin: "spawned", origin_ref: "parent" });
    const parent = session("parent", { origin: "inbox", origin_ref: "spawned-parent" });
    expect(resolve(spawned, {
      sessions: { [MID]: [parent] },
      items: [item("spawned-parent"), chainItem("chain")],
    })?.id).toBe("spawned-parent");
  });

  it("does not match handoffs", () => {
    const current = session(SID);
    const items = [item("handoff", { handoffs: [{ machine_id: MID, session_id: SID }] })];
    expect(resolve(current, { items })).toBeUndefined();
  });

  it("returns undefined when no rule matches", () => {
    expect(resolve(session(SID), { items: [item("unrelated")] })).toBeUndefined();
  });
});
