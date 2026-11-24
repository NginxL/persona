// @vitest-environment jsdom
// A parent's sidebar row whose Unread comes from a side session folded into it (#13299): the row names that side under
// its title ("+N" for the others), one click opens it in the parent's Side sessions panel, and once it is seen here the
// row's dot goes in the same update, with no frame of the hub's older rollup. A row with no unread side looks as before.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
vi.hoisted(() => {
  globalThis.ResizeObserver ??= class { observe() {} unobserve() {} disconnect() {} } as unknown as typeof ResizeObserver;
  window.matchMedia ??= ((query: string) => ({ matches: false, media: query, addEventListener() {}, removeEventListener() {} })) as unknown as typeof window.matchMedia;
});
import { applyCompleteSessions, getState, markSeenLocally, reset } from "../lib/store";
import { foldRows } from "../lib/folded-rows";
import { setSideFold } from "../lib/side-fold";
import { useSideAsk } from "../lib/side-panel";
import type { Route } from "../lib/router";
import type { Session } from "../lib/types";
import { SessionRow } from "./Sidebar";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const row = (id: string, extra: Partial<Session> = {}): Session => ({ machine_id: "mac", session_id: id, title: id, cwd: "/code", status: "idle", state: "idle", ...extra }) as Session;
// the parent was seen (its own last words are not new); two of its three sides have replies nobody has seen
const parent = (extra: Partial<Session> = {}) => row("review", { title: "Review PR 42", last_text: "Parent summary", seen_at: "2026-11-15T10:00:00Z", updated_at: "2026-11-15T09:59:00Z", children: { total: 3, sides: 3, sides_unread: 2 }, ...extra });
const side = (id: string, extra: Partial<Session> = {}) => row(id, { folded_under: "review", folded_as: "side", ...extra });
const newer = side("chat", { title: "Side chat", state: "unread", updated_at: "2026-11-15T10:09:00Z", seen_at: "2026-11-15T10:01:00Z" });
const older = side("btw", { title: "btw question", state: "unread", updated_at: "2026-11-15T10:05:00Z", seen_at: "2026-11-15T10:01:00Z" });
const seen = side("old", { title: "Seen side", updated_at: "2026-11-15T10:30:00Z" });

const to: Route = { name: "session", mid: "mac", sid: "review" };
let host: HTMLDivElement;
let root: Root;
let went: Route[];
let rowClicks: number;
const go = (r: Route) => (e: React.MouseEvent) => {
  e.preventDefault();
  went.push(r);
};
// what the parent's page would be asked to show
let ask: { side: string } | undefined;
function Asked() {
  ask = useSideAsk("mac", "review");
  return null;
}
const props = { k: "s:mac/review", reg: () => () => {}, hover: () => {}, active: false, to, go };
// the row as the store's lists draw it (folded with the side rows the list holds)
const listed = () => foldRows(getState().sessions.mac ?? []).find((s) => s.session_id === "review")!;
function draw(s: Session) {
  act(() => root.render(<div onClick={() => rowClicks++}><SessionRow {...props} s={s} /><Asked /></div>));
}
const mark = () => host.querySelector(".status-mark")?.getAttribute("data-state");
const sideLine = () => host.querySelector<HTMLElement>("[data-side-unread]");
const rowLine = () => host.querySelector("[data-row-line]")?.textContent;

beforeEach(() => {
  setSideFold(true);
  went = [];
  rowClicks = 0;
  ask = undefined;
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});
afterEach(() => {
  act(() => root.unmount());
  host.remove();
  reset();
  setSideFold(false);
});

describe("a parent row kept Unread by a side session (#13299)", () => {
  it("names the most recent unread side, +1 for the other, in place of the parent's own seen words", () => {
    act(() => applyCompleteSessions([parent(), older, newer, seen], [], { loaded: true }));
    draw(listed());
    expect(mark()).toBe("unread");
    expect(sideLine()?.textContent).toBe("分支新回复：Side chat+1");
    expect(sideLine()?.getAttribute("data-side-unread")).toBe("chat");
    expect(sideLine()?.getAttribute("role")).toBe("link");
    expect(sideLine()?.getAttribute("title")).toBe(["打开分支会话「Side chat」", "另有新回复：btw question"].join(String.fromCharCode(10)));
    expect(sideLine()?.getAttribute("aria-label")).toBe("分支会话「Side chat」有新回复. 打开分支会话「Side chat」. 另有新回复：btw question");
    // the parent's own reply was seen: its words do not stand under the side's green dot
    expect(rowLine()).toBeUndefined();
  });

  it("keeps the parent's own unread words beside the side's line when both are unread", () => {
    act(() => applyCompleteSessions([parent({ state: "unread", updated_at: "2026-11-15T10:02:00Z" }), newer, seen], [], { loaded: true }));
    draw(listed());
    expect(mark()).toBe("unread");
    expect(rowLine()).toBe("Parent summary");
    expect(sideLine()?.textContent).toBe("分支新回复：Side chat");
  });

  it("draws a row with no unread side as before", () => {
    act(() => applyCompleteSessions([parent({ children: { total: 1, sides: 1 } }), seen], [], { loaded: true }));
    draw(listed());
    expect(mark()).toBe("idle");
    expect(sideLine()).toBeNull();
    // nor a parent whose own reply is unread with only seen sides
    act(() => applyCompleteSessions([parent({ state: "unread", updated_at: "2026-11-15T10:02:00Z", children: { total: 1, sides: 1 } })]));
    draw(listed());
    expect(mark()).toBe("unread");
    expect(rowLine()).toBe("Parent summary");
    expect(sideLine()).toBeNull();
  });

  it("opens the side in its parent's page in one click, not the parent's row, and clears the dot once it is seen", () => {
    act(() => applyCompleteSessions([parent({ children: { total: 2, sides: 2, sides_unread: 1 } }), newer, seen], [], { loaded: true }));
    // a paged list's row, as the hub sent it: the hub still counts the side unread until it records the look
    const hubRow = getState().sessions.mac!.find((s) => s.session_id === "review")!;
    draw(hubRow);
    expect(mark()).toBe("unread");
    act(() => sideLine()!.click());
    expect(went).toEqual([to]);
    expect(rowClicks).toBe(0);
    expect(ask?.side).toBe("chat");
    // the side's chat is shown (viewSession marks it): the row is Seen in the same update, the hub's row unchanged
    const marks: (string | null | undefined)[] = [];
    act(() => markSeenLocally("mac", "chat"));
    marks.push(mark());
    draw(hubRow);
    marks.push(mark());
    expect(marks).toEqual(["idle", "idle"]);
    expect(sideLine()).toBeNull();
  });

  it("opens it from the keyboard too", () => {
    act(() => applyCompleteSessions([parent({ children: { total: 1, sides: 1, sides_unread: 1 } }), newer], [], { loaded: true }));
    draw(listed());
    act(() => {
      sideLine()!.focus();
      sideLine()!.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true }));
    });
    expect(went).toEqual([to]);
    expect(ask?.side).toBe("chat");
  });

  it("stays Unread when only the parent is seen here: its own mark does not clear a side's reply", () => {
    act(() => applyCompleteSessions([parent({ state: "unread", updated_at: "2026-11-15T10:02:00Z", children: { total: 1, sides: 1, sides_unread: 1 } }), newer], [], { loaded: true }));
    act(() => markSeenLocally("mac", "review"));
    draw(listed());
    expect(mark()).toBe("unread");
    expect(rowLine()).toBeUndefined();
    expect(sideLine()?.textContent).toBe("分支新回复：Side chat");
  });

  it("says how many sides have new replies before their rows are read", () => {
    // the store has not read its rows yet: no held side rows, only the hub's rollup
    act(() => applyCompleteSessions([parent()]));
    draw(getState().sessions.mac![0]!);
    expect(mark()).toBe("unread");
    expect(sideLine()?.textContent).toBe("2 个分支会话有新回复");
    expect(sideLine()?.getAttribute("role")).toBeNull();
  });
});
