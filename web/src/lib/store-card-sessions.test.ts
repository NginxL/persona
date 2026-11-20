// A pending card on a paging hub: child sessions (#5120) and off-page root sessions (#6432) need one exact lookup,
// so capability checks work wherever the card appears, not only after opening the Agent.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { connect, getSession, getState, reset } from "./store";
import { directoryVisibleCards, stateCounts } from "./sessions";
import { runtimeSessions } from "./runtime-list";
import { interactionCanAnswer } from "./format";
import type { Interaction, Session } from "./types";

class Socket {
  static OPEN = 1;
  static last: Socket;
  readyState = 1;
  onopen?: () => void;
  onmessage?: (event: { data: string }) => void;
  constructor(public url: string) { Socket.last = this; }
  send() {}
  close() { this.readyState = 3; }
  push(value: unknown) { this.onmessage?.({ data: JSON.stringify(value) }); }
}
const child: Session = { machine_id: "demo", session_id: "kid", title: "Add a remember-me option", cwd: "/demo", status: "blocked", state: "needs_you", updated_at: "2026-01-01T00:00:00Z", source_kind: "runtime", capabilities: { read: true, approve: true }, folded_under: "lead" };
const card: Interaction = { id: "ix1", machine_id: "demo", session_id: "kid", kind: "permission", tool_name: "Bash", folded_under: "lead" };
const rootSession: Session = { ...child, session_id: "off-page", folded_under: undefined };
const rootCard: Interaction = { id: "ix-root", machine_id: "demo", session_id: "off-page", kind: "question", questions: [{ question: "Which port?", options: null }] };
let lookups: URL[];
let exactRows: Session[];
// The paging list (`/sessions` without session_id): the first page, then each `since` read's changes.
let listRows: Session[] | null;
let listDelta: { sessions: Session[]; remove: string[] };
let listRevision: number;
let lookupFailures: number;
// Exact lookups held open until the test answers them (with the rows the hub had when each was asked).
let holdLookups: number;
let heldLookups: ((rows: Session[]) => void)[];

beforeEach(() => {
  vi.useFakeTimers();
  lookups = [];
  exactRows = [child, rootSession];
  listRows = null;
  listDelta = { sessions: [], remove: [] };
  listRevision = 1;
  lookupFailures = 0;
  holdLookups = 0;
  heldLookups = [];
  vi.stubGlobal("WebSocket", Socket);
  vi.stubGlobal("location", { protocol: "http:", host: "demo" });
  vi.stubGlobal("localStorage", { getItem: () => "demo-token" });
  vi.stubGlobal("window", { setTimeout, clearTimeout });
  vi.stubGlobal("fetch", vi.fn(async (path: string) => {
    const url = new URL(path, "http://demo");
    if (url.pathname === "/api/v1/sessions" && url.searchParams.has("session_id")) {
      lookups.push(url);
      if (lookupFailures > 0) { lookupFailures--; return Response.json({ error: "busy" }, { status: 500 }); }
      if (holdLookups > 0) {
        holdLookups--;
        return new Promise<Response>((resolve) => heldLookups.push((sessions) => resolve(Response.json({ sessions, remove: [], total: sessions.length, has_more: false, revision: "r1" }))));
      }
      const sessions = exactRows.filter((s) => s.machine_id === url.searchParams.get("machine_id") && s.session_id === url.searchParams.get("session_id"));
      return Response.json({ sessions, remove: [], total: sessions.length, has_more: false, revision: "r1" });
    }
    if (url.pathname === "/api/v1/sessions") {
      if (url.searchParams.has("counts")) return Response.json({ total: 1, machine: {}, project: {}, group: {}, state: {}, revision: "r1" });
      if (listRows && url.searchParams.has("since")) {
        const delta = listDelta;
        listDelta = { sessions: [], remove: [] };
        return Response.json({ ...delta, total: listRows.length, has_more: false, revision: `r${++listRevision}` });
      }
      if (listRows) return Response.json({ sessions: listRows, remove: [], total: listRows.length, has_more: false, revision: `r${listRevision}` });
      return Response.json({ sessions: [{ machine_id: "demo", session_id: "lead", title: "lead", cwd: "/demo", status: "idle", state: "idle", updated_at: "2026-01-01T00:00:00Z", waiting: false, children: { total: 1, needs_you: 1 } }], remove: [], total: 1, has_more: false, revision: "r1" });
    }
    return Response.json({});
  }));
});
afterEach(() => { reset(); vi.useRealTimers(); vi.unstubAllGlobals(); });
// a push reaches the listeners once per frame, or after 100 ms without one (store.ts notifySoon)
const settle = () => vi.advanceTimersByTimeAsync(200);

describe("pending-card sessions on a paging hub", () => {
  it("looks the card's session up once, so it can be answered outside its parent's page", async () => {
    connect();
    Socket.last.push({ type: "hello", features: ["sessions_list", "sessions_lookup", "sessions_hierarchy"] });
    await settle();
    expect(getSession("demo", "kid")).toBeUndefined();
    Socket.last.push({ type: "interaction", interaction: card });
    await settle();
    expect(lookups).toHaveLength(1);
    expect(lookups[0].searchParams.get("machine_id")).toBe("demo");
    expect(interactionCanAnswer(card, getSession("demo", "kid"))).toBe(true);
    // the same card again asks nothing more
    Socket.last.push({ type: "interaction", interaction: { ...card, summary: "npm run migrate" } });
    await settle();
    expect(lookups).toHaveLength(1);
  });

  it("asks nothing for a card whose session is listed, or without exact lookups", async () => {
    connect();
    Socket.last.push({ type: "hello", features: ["sessions_list"] });
    await settle();
    Socket.last.push({ type: "interaction", interaction: card });
    await settle();
    expect(lookups).toHaveLength(0);
  });

  it("hydrates an ordinary off-page question before enabling its answer, once per session", async () => {
    connect();
    Socket.last.push({ type: "hello", features: ["sessions_list", "sessions_lookup"] });
    await settle();
    expect(interactionCanAnswer(rootCard, getSession("demo", "off-page"))).toBe(false);
    Socket.last.push({ type: "interaction", interaction: rootCard });
    await settle();
    expect(lookups).toHaveLength(1);
    expect(lookups[0].searchParams.get("session_id")).toBe("off-page");
    expect(interactionCanAnswer(rootCard, getSession("demo", "off-page"))).toBe(true);
    Socket.last.push({ type: "interaction", interaction: { ...rootCard, id: "ix-next" } });
    await settle();
    expect(lookups).toHaveLength(1);
  });

  it.each(["read-only", "missing"] as const)("keeps an off-page question disabled when its exact session is %s", async (kind) => {
    const session_id = `off-page-${kind}`;
    const ix = { ...rootCard, session_id };
    exactRows = kind === "missing" ? [] : [{ ...rootSession, session_id, capabilities: { read: true, approve: false } }];
    connect();
    Socket.last.push({ type: "hello", features: ["sessions_list", "sessions_lookup"] });
    await settle();
    Socket.last.push({ type: "interaction", interaction: ix });
    await settle();
    expect(lookups).toHaveLength(1);
    expect(interactionCanAnswer(ix, getSession("demo", session_id))).toBe(false);
  });
});


it("retains a hidden pending card and restores its actionable projection after a visibility replay", async () => {
  connect();
  Socket.last.push({ type: "hello", features: [] });
  await settle();
  const hidden = { ...rootSession, hidden_by_directory: true };
  Socket.last.push({ type: "sessions", machine_id: "demo", sessions: [hidden] });
  Socket.last.push({ type: "interaction", interaction: rootCard });
  await settle();
  expect(getState().interactions.filter((ix) => ix.id === rootCard.id)).toHaveLength(1);
  expect(stateCounts([getSession("demo", "off-page")!]).badge).toBe(0);
  Socket.last.push({ type: "sessions", machine_id: "demo", sessions: [rootSession] });
  Socket.last.push({ type: "interaction", interaction: rootCard });
  await settle();
  expect(getState().interactions.filter((ix) => ix.id === rootCard.id)).toHaveLength(1);
  expect(interactionCanAnswer(rootCard, getSession("demo", "off-page"))).toBe(true);
  expect(stateCounts([getSession("demo", "off-page")!]).badge).toBe(1);
});

// #8578: a hidden session's card stays in the store (answerable on its own page) but leaves every card list and count.
it("leaves a directory-hidden session's card out of the card lists until its folder is shown again", async () => {
  connect();
  Socket.last.push({ type: "hello", features: [] });
  await settle();
  Socket.last.push({ type: "sessions", machine_id: "demo", sessions: [{ ...rootSession, hidden_by_directory: true }] });
  Socket.last.push({ type: "interaction", interaction: rootCard });
  await settle();
  expect(directoryVisibleCards(getState().interactions)).toEqual([]);
  Socket.last.push({ type: "sessions", machine_id: "demo", sessions: [rootSession] });
  await settle();
  expect(directoryVisibleCards(getState().interactions).map((ix) => ix.id)).toEqual([rootCard.id]);
});

it("leaves out a hidden off-page session's card once its exact lookup says it is hidden", async () => {
  exactRows = [{ ...rootSession, hidden_by_directory: true }];
  connect();
  Socket.last.push({ type: "hello", features: ["sessions_list", "sessions_lookup"] });
  await settle();
  Socket.last.push({ type: "interaction", interaction: rootCard });
  await settle();
  expect(lookups).toHaveLength(1);
  expect(getState().interactions.map((ix) => ix.id)).toEqual([rootCard.id]);
  expect(directoryVisibleCards(getState().interactions)).toEqual([]);
});

// #8584: a paging hub's list and exact lookup say whether a card's session is hidden; a chat detail kept from an
// opened chat is not refreshed by summary deltas and must not decide it.
describe("directory-hidden cards on a paging hub", () => {
  const offPage = JSON.stringify(["demo", "off-page"]);
  const summary = (s: Session): Session => ({ ...s, waiting: true });
  const visibleIds = () => directoryVisibleCards(getState().interactions).map((ix) => ix.id);
  // the list reads hints in 2 s windows (session-list.ts invalidate)
  const listChanged = async () => { Socket.last.push({ type: "sessions_list" }); await vi.advanceTimersByTimeAsync(2_500); };
  async function pagingHub(rows: Session[]) {
    listRows = rows;
    connect();
    Socket.last.push({ type: "hello", features: ["sessions_list", "sessions_lookup"] });
    await settle();
    await runtimeSessions.load();
    await settle();
  }

  it("hides and shows a card again past an opened chat's retained detail", async () => {
    await pagingHub([summary(rootSession)]);
    Socket.last.push({ type: "session", session: rootSession }); // the chat was opened: a complete row
    Socket.last.push({ type: "interaction", interaction: rootCard });
    await settle();
    expect(visibleIds()).toEqual([rootCard.id]);

    // hide: the row leaves the list, its exact lookup says hidden, the hub replays the pending cards
    exactRows = [{ ...rootSession, hidden_by_directory: true }];
    listDelta = { sessions: [], remove: [offPage] };
    await listChanged();
    Socket.last.push({ type: "interaction", interaction: rootCard });
    await settle();
    expect(visibleIds()).toEqual([]);
    expect(lookups.map((u) => u.searchParams.get("session_id"))).toContain("off-page");
    expect(getState().interactions.map((ix) => ix.id)).toEqual([rootCard.id]); // still answerable on its own page

    // unhide: the list sends the row again and the lookup says visible
    exactRows = [rootSession];
    listDelta = { sessions: [summary(rootSession)], remove: [] };
    await listChanged();
    Socket.last.push({ type: "interaction", interaction: rootCard });
    await settle();
    expect(visibleIds()).toEqual([rootCard.id]);
  });

  it("shows a card again after unhide when the retained detail and an earlier lookup said hidden", async () => {
    exactRows = [{ ...rootSession, hidden_by_directory: true }];
    await pagingHub([]);
    Socket.last.push({ type: "session", session: { ...rootSession, hidden_by_directory: true } });
    Socket.last.push({ type: "interaction", interaction: rootCard });
    await settle();
    expect(visibleIds()).toEqual([]);

    exactRows = [rootSession];
    listDelta = { sessions: [summary(rootSession)], remove: [] };
    await listChanged();
    Socket.last.push({ type: "interaction", interaction: rootCard });
    await settle();
    expect(visibleIds()).toEqual([rootCard.id]);
  });

  it("looks up every off-page session of a large replay until all are known", async () => {
    const ids = Array.from({ length: 25 }, (_, i) => `hidden-${i}`);
    exactRows = ids.map((session_id) => ({ ...rootSession, session_id, hidden_by_directory: true }));
    await pagingHub([]);
    for (const session_id of ids) Socket.last.push({ type: "interaction", interaction: { ...rootCard, id: `ix-${session_id}`, session_id } });
    await settle();
    await vi.advanceTimersByTimeAsync(1_000);
    expect(new Set(lookups.map((u) => u.searchParams.get("session_id"))).size).toBe(25);
    expect(getState().interactions).toHaveLength(25);
    expect(visibleIds()).toEqual([]);
  });

  // #8584: an exact lookup asked before a list change of its session can answer after it; that older answer must not
  // stand over the change, or the card keeps the visibility it had before (hide / unhide) until a reconnect.
  it("does not let a lookup asked before a hide stand over the list's removal", async () => {
    exactRows = [rootSession];
    holdLookups = 1;
    await pagingHub([]);
    Socket.last.push({ type: "interaction", interaction: rootCard });
    await settle();
    expect(heldLookups).toHaveLength(1);

    exactRows = [{ ...rootSession, hidden_by_directory: true }];
    listDelta = { sessions: [], remove: [offPage] };
    await listChanged();
    heldLookups[0]([rootSession]); // the answer from before the hide lands now
    await settle();
    expect(lookups).toHaveLength(2); // asked again: the answer predates the change
    expect(visibleIds()).toEqual([]);
  });

  it("does not let a lookup asked before an unhide stand over the list's newer row", async () => {
    // fifty newer rows fill the list's first page, so the session's row stays off it
    const newer = Array.from({ length: 50 }, (_, i) => summary({ ...rootSession, session_id: `newer-${i}`, updated_at: "2026-02-01T00:00:00Z" }));
    exactRows = [{ ...rootSession, hidden_by_directory: true }];
    holdLookups = 1;
    await pagingHub(newer);
    Socket.last.push({ type: "interaction", interaction: rootCard });
    await settle();
    expect(heldLookups).toHaveLength(1);

    exactRows = [rootSession];
    listDelta = { sessions: [summary(rootSession)], remove: [] };
    await listChanged();
    heldLookups[0]([{ ...rootSession, hidden_by_directory: true }]); // the answer from before the unhide
    await settle();
    expect(visibleIds()).toEqual([rootCard.id]);
  });

  it("retries a failed lookup with backoff, a bounded number of times", async () => {
    exactRows = [{ ...rootSession, hidden_by_directory: true }];
    lookupFailures = 2;
    await pagingHub([]);
    Socket.last.push({ type: "interaction", interaction: rootCard });
    await settle();
    expect(lookups).toHaveLength(1);
    expect(visibleIds()).toEqual([rootCard.id]); // unknown: shown until known
    await vi.advanceTimersByTimeAsync(60_000);
    expect(lookups).toHaveLength(3);
    expect(visibleIds()).toEqual([]);
  });

  it("gives up after a few failures and stops retrying a card that is gone", async () => {
    lookupFailures = Infinity;
    await pagingHub([]);
    Socket.last.push({ type: "interaction", interaction: rootCard });
    await settle();
    await vi.advanceTimersByTimeAsync(10 * 60_000);
    const tries = lookups.length;
    expect(tries).toBeGreaterThan(1);
    expect(tries).toBeLessThanOrEqual(5);
    expect(visibleIds()).toEqual([rootCard.id]); // a card never disappears for want of an answer
    await vi.advanceTimersByTimeAsync(10 * 60_000);
    expect(lookups).toHaveLength(tries);

    const other = { ...rootCard, id: "ix-gone", session_id: "gone" };
    Socket.last.push({ type: "interaction", interaction: other });
    await settle();
    const before = lookups.length;
    Socket.last.push({ type: "interaction_resolved", id: other.id });
    await settle();
    await vi.advanceTimersByTimeAsync(10 * 60_000);
    expect(lookups.length).toBe(before);
  });
});
