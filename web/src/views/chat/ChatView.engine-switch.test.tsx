// @vitest-environment jsdom
// A conversation that moved between Claude Code and Codex (#5054): the live row names the sessions that ran it before
// (engine_history), and its chat reads them in order above its own, as one conversation. They load only once the live
// transcript has no older page, read only; each later stretch opens with its switch divider.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Item, Session } from "../../lib/types";

vi.hoisted(() => {
  const g = globalThis as Record<string, unknown>;
  g.matchMedia = () => ({ matches: false, addEventListener() {}, removeEventListener() {} });
  g.ResizeObserver = class { observe() {} unobserve() {} disconnect() {} };
  g.IntersectionObserver = class { observe() {} unobserve() {} disconnect() {} takeRecords() { return []; } };
  (g.CSS as { escape?: (s: string) => string } | undefined) ?? (g.CSS = {});
  (g.CSS as { escape?: (s: string) => string }).escape ??= (s: string) => s;
});

const transcript = vi.hoisted(() => ({
  items: [] as Item[], loading: false, error: "", hasMore: false, streaming: "", loadingEarlier: false,
  earlierPage: undefined as symbol | undefined, loadEarlier: async (): Promise<symbol | undefined> => undefined,
  expandFold: async () => {}, reload: () => {},
}));
vi.mock("./useTranscript", () => ({ useTranscript: () => transcript }));
vi.mock("../../lib/panel", async (importOriginal) => ({ ...await importOriginal<typeof import("../../lib/panel")>(), usePanel: () => null }));

const { ChatView } = await import("./ChatView");
const { getState, reset, upsertSession } = await import("../../lib/store");
const { forgetExpanded } = await import("./expanded");
(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const T0 = Date.parse("2026-11-12T09:00:00Z");
const at = (m: number) => new Date(T0 + m * 60_000).toISOString();
const block = (from: string, to: string, resumed = false) =>
  `<persona-context kind="engine-switch" title="Handoff" from="${from}" to="${to}" model="${to === "codex" ? "gpt-5.5" : "claude-opus-5-5"}"${resumed ? ' resumed="1"' : ""}>\nWhat happened so far.\n</persona-context>\n\n`;
const user = (sid: string, seq: number, m: number, text: string): Item => ({ id: `${sid}u${seq}#0`, seq, role: "user", kind: "text", text, ts: at(m) });
const said = (sid: string, seq: number, m: number, text: string): Item => ({ id: `${sid}a${seq}#0`, seq, role: "assistant", kind: "text", text, ts: at(m) });
const row = (sid: string, extra: Partial<Session> = {}): Session => ({ machine_id: "m", session_id: sid, title: "Login page", cwd: "/w", status: "idle", state: "idle", ...extra });

/** What the hub answers for another session's items, by session id: a page of the given items. */
let pages: Record<string, Item[]> = {};
let asked: string[] = [];

let host: HTMLDivElement;
let root: Root;
const flush = async () => {
  for (let i = 0; i < 10; i++) await act(async () => { await new Promise((r) => setTimeout(r, 0)); });
};
/** Until `ok` (the stretches' module loads with the chat; a loaded machine takes a few turns), at most about a second. */
const until = async (ok: () => boolean) => {
  for (let i = 0; i < 200 && !ok(); i++) await act(async () => { await new Promise((r) => setTimeout(r, 5)); });
};
/** Opens the chat as a page does: the transcript loading, then its first page in (the chat lands at the end first). */
async function open(sid: string, items: Item[]) {
  Object.assign(transcript, { items: [], loading: true });
  act(() => root.render(<ChatView mid="m" sid={sid} onToggleSidebar={() => {}} />));
  Object.assign(transcript, { items, loading: false });
  act(() => root.render(<ChatView mid="m" sid={sid} onToggleSidebar={() => {}} />));
  await flush();
}
/** The chat's words in the order drawn: user bubbles, replies and switch dividers. */
const reading = () =>
  [...host.querySelectorAll("[data-bubble-text], [data-answer], [data-engine-switch]")].map((el) =>
    el.hasAttribute("data-engine-switch") ? `⇄ ${el.getAttribute("data-engine-switch")}` : (el.textContent ?? "").trim(),
  );

beforeEach(() => {
  localStorage.clear();
  forgetExpanded();
  pages = {};
  asked = [];
  transcript.items = [];
  transcript.loading = false;
  transcript.hasMore = false;
  transcript.loadEarlier = async () => undefined;
  transcript.earlierPage = undefined;
  Object.assign(getState(), { loaded: true });
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
    const url = String(input);
    const m = /\/sessions\/m\/([^/]+)\/items\?/.exec(url);
    if (m && pages[m[1]]) {
      asked.push(`${m[1]}${url.includes("before_seq") ? " older" : ""}`);
      return new Response(JSON.stringify({ items: pages[m[1]], has_more: false }), { status: 200, headers: { "Content-Type": "application/json" } });
    }
    return new Response(JSON.stringify({ items: [] }), { status: 200, headers: { "Content-Type": "application/json" } });
  }));
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
  reset();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

describe("a conversation across engines", () => {
  it("A → B: reads A's stretch, then the divider and B's, once B has no older page", async () => {
    upsertSession(row("b", { agent: "codex", engine_history: [{ session_id: "a", agent: "claude", until: at(10) }] }));
    pages.a = [user("a", 1, 1, "Build a login page."), said("a", 2, 2, "The login page is in.")];
    let older = 0;
    transcript.hasMore = true;
    transcript.loadEarlier = async () => { older++; return undefined; };
    await open("b", [user("b", 1, 11, `${block("claude", "codex")}Now add the tests.`), said("b", 2, 12, "Tests added.")]);
    // B still has older pages: they come first, and nothing of A is asked for
    expect(older).toBeGreaterThan(0);
    expect(asked).toEqual([]);
    expect(reading()).toEqual(["⇄ summary", "Now add the tests.", "Tests added."]);
    // B is whole: A's stretch loads and reads above it
    transcript.hasMore = false;
    act(() => root.render(<ChatView mid="m" sid="b" onToggleSidebar={() => {}} />));
    await until(() => host.querySelector("[data-engine-stretch='a']") !== null);
    expect(asked).toEqual(["a"]);
    expect(reading()).toEqual(["Build a login page.", "The login page is in.", "⇄ summary", "Now add the tests.", "Tests added."]);
    expect(host.querySelector("[data-engine-stretch='a']")).not.toBeNull();
    // the block is no context chip, and the bubble keeps the user's words only
    expect(host.textContent).not.toContain("What happened so far.");
    expect(host.textContent).not.toContain("Handoff");
  });

  it("A → B → A: A's first stretch, B's, then A's own again, each later one after its divider", async () => {
    upsertSession(row("a", { agent: "claude", engine_history: [{ session_id: "a", agent: "claude", until: at(10) }, { session_id: "b", agent: "codex", until: at(20) }] }));
    pages.b = [user("b", 1, 11, `${block("claude", "codex")}Now add the tests.`), said("b", 2, 12, "Tests added.")];
    await open("a", [
      user("a", 1, 1, "Build a login page."), said("a", 2, 2, "The login page is in."),
      user("a", 3, 21, `${block("codex", "claude", true)}Review the tests.`), said("a", 4, 22, "They look right."),
    ]);
    await until(() => host.querySelector("[data-engine-stretch='a']") !== null);
    expect(asked).toEqual(["b"]);
    expect(reading()).toEqual(["Build a login page.", "The login page is in.", "⇄ summary", "Now add the tests.", "Tests added.", "⇄ resumed", "Review the tests.", "They look right."]);
    expect([...host.querySelectorAll("[data-engine-stretch]")].map((el) => el.getAttribute("data-engine-stretch"))).toEqual(["a", "b"]);
  });

  it("anchors an older A page when it renders above B without changing the live A front", async () => {
    upsertSession(row("a", { agent: "claude", engine_history: [
      { session_id: "a", agent: "claude", until: at(10) }, { session_id: "b", agent: "codex", until: at(20) },
    ] }));
    pages.b = [user("b", 1, 11, "Now add the tests."), said("b", 2, 12, "Tests added.")];
    let baseHeight = 800;
    const height = () => baseHeight + (host.querySelector("[data-engine-stretch='a'] [data-line*='au1']") ? 550 : 0);
    const firstPage = [user("a", 3, 3, "Build a login page."), user("a", 10, 21, "Review the tests.")];
    await open("a", firstPage);
    await until(() => host.querySelector("[data-engine-stretch='a']") !== null);
    expect(reading()).toContain("Build a login page.");
    const scroller = host.querySelector<HTMLElement>("[data-chat-scroll]")!;
    baseHeight = 1300;
    Object.defineProperties(scroller, {
      scrollHeight: { get: height },
      clientHeight: { value: 800 },
      scrollTop: { value: 250, writable: true },
    });
    // Leave following before the old page starts, like a reader scrolling away from the newest messages.
    act(() => scroller.dispatchEvent(new WheelEvent("wheel", { deltaY: -100, bubbles: true })));
    scroller.scrollTop = 250;

    let release!: (page: symbol) => void;
    const pending = new Promise<symbol>((resolve) => { release = resolve; });
    const loadEarlier = vi.fn(() => pending);
    Object.assign(transcript, { hasMore: true, loadEarlier });
    act(() => root.render(<ChatView mid="m" sid="a" onToggleSidebar={() => {}} />));
    await flush();
    act(() => scroller.dispatchEvent(new Event("scroll")));
    expect(loadEarlier).toHaveBeenCalled();

    const page = Symbol();
    await act(async () => { release(page); });
    // These older rows belong to A's first stretch, not the live stretch.
    Object.assign(transcript, { items: [user("a", 1, 1, "Start with the login form."), ...firstPage], earlierPage: page, hasMore: false });
    act(() => root.render(<ChatView mid="m" sid="a" onToggleSidebar={() => {}} />));
    expect(reading()).toContain("Start with the login form.");
    expect(scroller.scrollTop).toBe(800);
  });

  it("keeps A's early stretch back until B's is in, so nothing moves in between what is on screen", async () => {
    upsertSession(row("a", { agent: "claude", engine_history: [{ session_id: "a", agent: "claude", until: at(10) }, { session_id: "b", agent: "codex", until: at(20) }] }));
    // no page for b: it stays empty, as a stretch still loading does
    let hold: () => void = () => {};
    pages.b = [];
    vi.mocked(fetch).mockImplementation(() => new Promise((resolve) => { hold = () => resolve(new Response(JSON.stringify({ items: [] }), { status: 200 })); }));
    await open("a", [user("a", 1, 1, "Build a login page."), user("a", 3, 21, `${block("codex", "claude", true)}Review the tests.`)]);
    expect(reading()).toEqual(["⇄ resumed", "Review the tests."]);
    hold();
  });

  it("offers nothing that writes on an earlier stretch's messages", async () => {
    upsertSession(row("b", { agent: "codex", engine_history: [{ session_id: "a", agent: "claude", until: at(10) }] }));
    pages.a = [user("a", 1, 1, "Build a login page."), said("a", 2, 2, "The login page is in.")];
    await open("b", [user("b", 1, 11, `${block("claude", "codex")}Now add the tests.`), said("b", 2, 12, "Tests added.")]);
    await until(() => host.querySelector("[data-engine-stretch='a']") !== null);
    const earlier = host.querySelector("[data-engine-stretch='a']")!;
    const live = [...host.querySelectorAll("[data-user-bubble]")].find((el) => !earlier.contains(el))!;
    const labels = (el: Element) => [...el.querySelectorAll("button[aria-label]")].map((b) => b.getAttribute("aria-label"));
    expect(labels(live)).toContain("引用");
    expect(labels(earlier)).toContain("复制消息");
    expect(labels(earlier)).not.toContain("引用");
    expect(labels(earlier)).not.toContain("改后重发");
  });

  it("is the chat as it was for a conversation that never switched", async () => {
    upsertSession(row("b", { agent: "codex" }));
    await open("b", [user("b", 1, 11, "Hello."), said("b", 2, 12, "Hi.")]);
    expect(reading()).toEqual(["Hello.", "Hi."]);
    expect(asked).toEqual([]);
    expect(host.querySelector("[data-engine-stretch]")).toBeNull();
  });
});
