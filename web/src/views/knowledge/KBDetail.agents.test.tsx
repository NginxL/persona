// @vitest-environment jsdom
// A knowledge project's "Agents on This Project" (#12055, the rule of #9971): a row opens the Agent's page on top of the
// project's, and the new entry names the project's address, so the page offers "Came from … · click to return" and the
// sidebar keeps Knowledge lit; ⌘-click still leaves it to the browser (a new tab). The hub is faked; the data made up.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { initI18n } from "../../i18n";
import { setToken } from "../../lib/api";
import { handoffFrom } from "../../lib/handoff";
import type { KBDetailResponse } from "../../lib/kb";
import { resetManagerChoice, setRosterDefault } from "../../lib/manager-choice";
import type { Session } from "../../lib/types";
import { EntryDetail } from "./KBDetail";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
let host: HTMLDivElement;
let root: Root;
const agent: Session = { machine_id: "mac", session_id: "s1", title: "Update the landing page", cwd: "/code/site", status: "idle", state: "idle" };
const detail: KBDetailResponse = { detail: { kind: "projects", id: "site", path: "", meta: { name: "Website" }, summary: "# Website" }, agents: [agent] };
const flush = async () => {
  for (let i = 0; i < 5; i++) await act(async () => { await Promise.resolve(); });
};
const row = () => host.querySelector<HTMLAnchorElement>('a[href="/s/mac/s1"]')!;

beforeEach(async () => {
  await initI18n("en");
  localStorage.clear();
  resetManagerChoice();
  setToken("synthetic-owner-a");
  setRosterDefault("assistant");
  vi.stubGlobal("fetch", vi.fn((url: string) => Promise.resolve(new Response(JSON.stringify(url.endsWith("/kb/projects/site") ? detail : {}), { status: 200, headers: { "Content-Type": "application/json" } }))));
  history.replaceState(null, "", "/kb/projects/site");
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  act(() => root.render(<EntryDetail kind="projects" id="site" reloadKey={0} onToggleSidebar={() => {}} onBack={() => {}} onOpen={() => {}} onSaved={() => {}} />));
  await flush();
});
afterEach(async () => {
  await act(async () => root.unmount());
  host.remove();
  resetManagerChoice();
  vi.unstubAllGlobals();
  history.replaceState(null, "", "/");
});

describe("a knowledge project's Agents (#12055)", () => {
  it("opens an Agent with the project named as where it came from", () => {
    expect(row().textContent).toContain("Update the landing page");
    const depth = history.length;
    act(() => row().click());
    expect(location.pathname).toBe("/s/mac/s1");
    expect(history.length).toBe(depth + 1);
    expect(handoffFrom()).toEqual({ path: "/kb/projects/site" });
  });

  it("leaves a ⌘-click to the browser", () => {
    const click = new MouseEvent("click", { bubbles: true, cancelable: true, metaKey: true });
    // what the browser would do with it (a new tab) is not this page's: jsdom is kept from following the link
    addEventListener("click", (e) => e.preventDefault(), { once: true });
    act(() => { row().dispatchEvent(click); });
    expect(location.pathname).toBe("/kb/projects/site");
    expect(handoffFrom()).toBeUndefined();
  });
});
