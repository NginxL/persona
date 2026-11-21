// @vitest-environment jsdom
// An Agent's page opened from inside another page is a drill-down (#12055, the rule of #9971): the sidebar keeps the
// row of the page it was opened from lit, read off the Agent's history entry (lib/handoff.ts), and Back to that page
// lights it as before. An Agent picked in the sidebar, or reached by a link, lights its own row. The data is made up.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
vi.hoisted(() => {
  globalThis.ResizeObserver ??= class { observe() {} unobserve() {} disconnect() {} } as unknown as typeof ResizeObserver;
  window.matchMedia ??= ((query: string) => ({ matches: false, media: query, onchange: null, addEventListener() {}, removeEventListener() {}, addListener() {}, removeListener() {}, dispatchEvent: () => false })) as typeof window.matchMedia;
});
import { initI18n } from "../i18n";
import { openFromPage } from "../lib/handoff";
import { useRoute } from "../lib/router";
import { refresh, reset } from "../lib/store";
import type { Machine, Session } from "../lib/types";
import { Sidebar } from "./Sidebar";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const machines: Machine[] = [{ id: "mac", name: "MacBook", online: true }];
const sessions: Session[] = [
  { machine_id: "mac", session_id: "s1", title: "Fix the login page", cwd: "/code", status: "idle", state: "idle" },
  { machine_id: "mac", session_id: "s2", title: "Write the release notes", cwd: "/code", status: "idle", state: "idle" },
];
const answers: Record<string, unknown> = { "/api/v1/sessions": { sessions }, "/api/v1/machines": { machines } };

let host: HTMLDivElement;
let root: Root;
const flush = async () => {
  for (let i = 0; i < 5; i++) await act(async () => { await Promise.resolve(); });
};

beforeEach(async () => {
  await initI18n("en");
  localStorage.clear();
  vi.stubGlobal("fetch", vi.fn((url: string) => Promise.resolve(new Response(JSON.stringify(answers[new URL(url, "http://x").pathname] ?? {}), { headers: { "Content-Type": "application/json" } }))));
  history.replaceState(null, "", "/");
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});
afterEach(async () => {
  act(() => root.unmount());
  host.remove();
  reset();
  vi.unstubAllGlobals();
  history.replaceState(null, "", "/");
  await initI18n("zh-Hans");
});

/** The sidebar of the page at the address, drawn again on every move as the app draws it. */
function Shell() {
  return <Sidebar route={useRoute()} onSearch={() => {}} onNewAgent={() => {}} />;
}
const show = async () => {
  await act(() => refresh());
  act(() => root.render(<Shell />));
  await flush();
};
const agentRow = (sid: string) => host.querySelector<HTMLAnchorElement>(`[data-sidebar-rows] a[href$="/s/mac/${sid}"]`)!;
const pageRow = (path: string) => [...host.querySelectorAll<HTMLAnchorElement>(`a[href="${path}"]`)];
const lit = () => [...host.querySelectorAll<HTMLElement>('[aria-current="page"]')].map((el) => el.getAttribute("href"));
const back = () => act(() => new Promise<void>((done) => {
  addEventListener("popstate", () => setTimeout(done, 0), { once: true });
  history.back();
}));

describe("the sidebar on an Agent's page opened from inside a page (#12055)", () => {
  it.each([
    { from: "/todos?id=t1", row: "/todos", what: "a to-do's Agent row or hand-off" },
    { from: "/kb/projects/p1", row: "/kb", what: "a knowledge project's Agents" },
    { from: "/kb/recordings/r1", row: "/kb", what: "a meeting's hand-off" },
  ])("keeps the page's row lit: $what", async ({ from, row }) => {
    history.replaceState(null, "", from);
    await show();
    act(() => openFromPage({ name: "session", mid: "mac", sid: "s1" }));
    await flush();
    expect(location.pathname).toBe("/s/mac/s1");
    expect(agentRow("s1").getAttribute("aria-current")).toBeNull();
    expect(pageRow(row).some((a) => a.getAttribute("aria-current") === "page")).toBe(true);
    expect(lit()).toEqual([row]);
    // Back: the page, still lit
    await back();
    await flush();
    expect(location.pathname + location.search).toBe(from);
    expect(lit()).toEqual([row]);
  });

  it("keeps the Persona row lit for an Agent a Persona conversation's card opened as a page", async () => {
    history.replaceState({ handoff: { path: "/" } }, "", "/s/mac/s1");
    await show();
    expect(agentRow("s1").getAttribute("aria-current")).toBeNull();
    expect(lit()).toEqual(["/"]);
  });

  it("keeps the first page lit through an Agent opened from that Agent's page", async () => {
    history.replaceState(null, "", "/todos?id=t1");
    await show();
    act(() => openFromPage({ name: "session", mid: "mac", sid: "s1" }));
    await flush();
    act(() => openFromPage({ name: "session", mid: "mac", sid: "s2" }));
    await flush();
    expect(location.pathname).toBe("/s/mac/s2");
    expect(lit()).toEqual(["/todos"]);
  });

  it("lights the Agent's own row when it was picked in the sidebar or reached by a link", async () => {
    history.replaceState(null, "", "/s/mac/s1");
    await show();
    expect(agentRow("s1").getAttribute("aria-current")).toBe("page");
    expect(lit()).toEqual(["/s/mac/s1"]);
  });
});
