// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { t } from "../../i18n";
import { api } from "../../lib/api";
import { SessionList, type ListCounts } from "../../lib/session-list";
import { PagedSessionList } from "./PagedSessionList";

vi.mock("../../lib/api", async (original) => ({ ...await original<typeof import("../../lib/api")>(), api: vi.fn() }));
vi.mock("../../lib/store", () => ({ onHubEvent: () => () => {} }));
// jsdom lays nothing out, so the virtualizer would draw no row: draw every item, in order
vi.mock("@tanstack/react-virtual", () => ({
  useVirtualizer: ({ count, getItemKey }: { count: number; getItemKey: (i: number) => string }) => ({
    getVirtualItems: () => Array.from({ length: count }, (_, index) => ({ index, key: getItemKey(index), start: index * 32 })),
    getTotalSize: () => count * 32,
    measureElement: () => {},
  }),
}));
(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

type Row = { id: string; updated_at: string; state: string; parent?: string };
const NOW = Date.UTC(2026, 10, 13, 12);
const HOUR = 3_600_000;
const row = (id: string, hoursAgo: number, state = "idle", parent?: string): Row =>
  ({ id, updated_at: new Date(NOW - hoursAgo * HOUR).toISOString(), state, parent });
// newest first, as the hub sends them: two side chats of "main" (one waiting on the user), main itself two days old
const rows = [row("btw-1", 1, "idle", "main"), row("other", 2), row("btw-2", 5, "needs_you", "main"), row("main", 48), row("old", 200)];
const identity = (r: Row) => r.id;
const compare = (a: Row, b: Row) => Date.parse(b.updated_at) - Date.parse(a.updated_at);
const activeAt = (r: Row) => Date.parse(r.updated_at);
const needsYou = (r: Row) => r.state === "needs_you";
const parentOf = (r: Row) => r.parent;
const counts: ListCounts = { total: 5, state: { needs_you: 1, idle: 4 }, machine: {}, project: {}, group: {}, revision: "r" };

let host: HTMLDivElement;
let root: Root;
let list: SessionList<Row>;

beforeEach(() => {
  vi.useFakeTimers({ now: NOW });
  localStorage.clear();
  vi.mocked(api).mockImplementation((path) => {
    const params = new URL(path, "https://example.com").searchParams;
    return Promise.resolve(params.has("counts") ? counts
      : { sessions: rows, remove: [], has_more: false, total: 5, revision: "r" }) as ReturnType<typeof api>;
  });
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  list = new SessionList<Row>("/sessions", identity, compare);
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
  document.body.innerHTML = "";
  vi.useRealTimers();
});

async function show(withParents = true, stable = false) {
  await list.load();
  if (stable) list.setPresentation({ compare: (a, b) => a.id.localeCompare(b.id), parentOf });
  await act(async () => root.render(<PagedSessionList root={list} path="/sessions" identity={identity} compare={compare} event="sessions_list"
    activeAt={activeAt} needsYou={needsYou} parentOf={withParents ? parentOf : undefined} renderRow={(r) => <span>{r.id}</span>} />));
}
const shown = () => [...host.querySelectorAll("[data-session-row]")].map((el) => el.textContent);
const nested = () => [...host.querySelectorAll("[data-nested-session]")].map((el) => el.textContent);
const total = () => host.querySelector("[data-session-total]")?.textContent;
const chip = (state: string) => host.querySelector(`[role="radiogroup"] button[aria-label^="${state}"]`)?.getAttribute("aria-label");

describe("side sessions in the paged list (#5935)", () => {
  it("draws each under its parent, a level in, the group where its newest member was", async () => {
    await show();
    expect(shown()).toEqual(["main", "btw-1", "btw-2", "other", "old"]);
    expect(nested()).toEqual(["btw-1", "btw-2"]);
  });

  it("keeps every row and count: nesting only moves rows", async () => {
    await show(false);
    const flat = { total: total(), needs: chip(t("lib-state-labels.needs_you")), rows: [...shown()].sort() };
    expect(nested()).toEqual([]);
    act(() => root.unmount());
    root = createRoot(host);
    await show();
    expect(total()).toBe(flat.total);
    expect(total()).toBe(t("paged-agents.count", { count: 5 }));
    expect(chip(t("lib-state-labels.needs_you"))).toBe(flat.needs);
    expect(flat.needs).toBe(`${t("lib-state-labels.needs_you")} 1`);
    expect([...shown()].sort()).toEqual(flat.rows);
    // the side session that waits on the user is a row of its own, in view without opening anything
    expect(shown()).toContain("btw-2");
  });

  it("never splits a group with Earlier after structural ordering: its newest side member still leads its activity", async () => {
    localStorage.setItem("persona.agentActiveWindow", "1d");
    await show(true, true);
    // main is two days old and sorts before its side chats, but its side chat moved an hour ago: the group stays recent
    expect(shown()).toEqual(["main", "btw-1", "btw-2", "other"]);
    const header = host.querySelector<HTMLElement>("[data-earlier] button");
    expect(header?.textContent).toBe(`${t("paged-agents.earlier")}1`);
  });

  it("counts an interleaved parent and side as recent, with only the unrelated rows Earlier (#11801)", async () => {
    localStorage.setItem("persona.agentActiveWindow", "1d");
    const interleaved = [row("side", 1, "idle", "parent"), row("unrelated", 30, "needs_you"), row("parent", 40, "needs_you"), row("old", 200)];
    const interleavedCounts: ListCounts = { total: 4, state: { needs_you: 2, idle: 2 }, machine: {}, project: {}, group: {}, revision: "r" };
    vi.mocked(api).mockImplementation((path) => {
      const params = new URL(path, "https://example.com").searchParams;
      return Promise.resolve(params.has("counts") ? interleavedCounts
        : { sessions: interleaved, remove: [], has_more: false, total: 4, revision: "r" }) as ReturnType<typeof api>;
    });
    await show(true, true);

    expect(shown()).toEqual(["parent", "side"]);
    const header = host.querySelector<HTMLElement>("[data-earlier]")!;
    expect(header.querySelector("button")?.textContent).toBe(`${t("paged-agents.earlier")}2`);
    expect(header.querySelector("button + span")?.textContent).toBe(`1 ${t("sidebar.needs_you_suffix", { count: 1 })}`);

    await act(async () => header.querySelector("button")!.click());
    const items = [...host.querySelectorAll("[data-index]")].map((el) => el.hasAttribute("data-earlier") ? "earlier" : el.textContent);
    expect(items).toEqual(["parent", "side", "earlier", "old", "unrelated"]);
  });
});
