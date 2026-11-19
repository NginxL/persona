// @vitest-environment jsdom
// PATH wrappers and external runtime launchers keep their existing Model providers Group, rows and launch checks (#10141).
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

vi.hoisted(() => {
  window.matchMedia = ((query: string) => ({ matches: false, media: query, onchange: null, addEventListener() {}, removeEventListener() {}, addListener() {}, removeListener() {}, dispatchEvent: () => false })) as typeof window.matchMedia;
  globalThis.ResizeObserver ??= class { observe() {} unobserve() {} disconnect() {} } as unknown as typeof ResizeObserver;
});
vi.mock("../lib/store", async (original) => ({
  ...(await original<typeof import("../lib/store")>()),
  useStore: (select: (state: { features: string[]; machines: { id: string; name: string; online: boolean }[]; conn: string; link: { networkUp: boolean; restarting: boolean }; hubName: string; mesh: unknown[] }) => unknown) => select({ features: [], machines: [{ id: "laptop", name: "Laptop", online: true }], conn: "online", link: { networkUp: true, restarting: false }, hubName: "", mesh: [] }),
}));
vi.mock("./settings/ModelConfiguration", () => ({ ModelConfiguration: () => null }));
vi.mock("./settings/ProviderImport", () => ({ ProviderImport: () => null }));
vi.mock("./settings/ProviderWizard", () => ({ ProviderWizard: () => null }));
vi.mock("./settings/LauncherQuota", () => ({ LauncherQuota: () => null }));
vi.mock("../lib/pwa", async (original) => ({ ...(await original<typeof import("../lib/pwa")>()), useInstall: () => "installed" }));

const WRAPPER = { name: "agent-wrapper", display_name: "Agent wrapper", source: "path", kind: "launcher", agent: "codex", machines: ["laptop"] };
const RUNTIME = { name: "antigravity", display_name: "Antigravity", source: "agy", kind: "launcher", agent: "agy", machines: ["laptop"] };
const LAUNCHERS = [WRAPPER, RUNTIME];
const calls = vi.hoisted(() => [] as string[]);
vi.mock("../lib/api", async (original) => ({
  ...(await original<typeof import("../lib/api")>()),
  api: (path: string, init?: { method?: string }) => {
    calls.push(`${init?.method ?? "GET"} ${path}`);
    if (path === "/launchers") return Promise.resolve({ launchers: LAUNCHERS });
    const launcher = LAUNCHERS.find((l) => path === `/launchers/${l.name}`);
    if (launcher) return Promise.resolve({ launcher });
    if (path.endsWith("/check?machine=laptop")) return Promise.resolve({ check: { ok: true, millis: 5 } });
    return Promise.resolve({});
  },
}));

import { t } from "../i18n";
import { resetManagerChoice } from "../lib/manager-choice";
import { resetRoster } from "../lib/managers";
import { Settings } from "./Settings";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
let host: HTMLDivElement;
let root: Root;
const flush = async () => { for (let i = 0; i < 6; i++) await act(async () => { await Promise.resolve(); }); };
const mount = async () => {
  history.replaceState(null, "", "/settings?tab=models");
  await act(async () => root.render(<Settings onToggleSidebar={() => {}} />));
  await flush();
};
const pathGroup = () => {
  const group = [...host.querySelectorAll<HTMLElement>("#settings-providers section")].find((s) => s.querySelector("h2")?.textContent === t("settings.path_title", { count: 2 }));
  expect(group).toBeDefined();
  return group!;
};

beforeEach(() => {
  calls.length = 0;
  localStorage.clear();
  resetManagerChoice();
  resetRoster();
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});
afterEach(() => {
  act(() => root.unmount());
  host.remove();
  document.body.innerHTML = "";
  resetManagerChoice();
  resetRoster();
});

describe("Model providers PATH launchers", () => {
  it("keeps PATH wrappers and external runtime launchers together in the existing Group footer", async () => {
    await mount();
    const group = pathGroup();
    expect(group.querySelector(":scope > p")?.textContent).toBe(t("settings.path_footer"));
    const rows = [...group.querySelectorAll("button")];
    expect(rows).toHaveLength(2);
    expect(rows[0].textContent).toContain(WRAPPER.display_name);
    expect(rows[1].textContent).toContain(RUNTIME.display_name);
    expect(rows[1].textContent).toContain("Antigravity Runtime");
  });

  it.each([WRAPPER, RUNTIME])("keeps the $display_name row and its launch check unchanged", async (launcher) => {
    await mount();
    const row = [...pathGroup().querySelectorAll("button")].find((b) => b.textContent?.includes(launcher.display_name));
    expect(row).toBeDefined();
    await act(async () => row!.click());
    await flush();

    const dialog = document.querySelector<HTMLElement>('[role="dialog"]')!;
    expect(dialog.textContent).toContain(launcher.display_name);
    const test = [...dialog.querySelectorAll("button")].find((b) => b.textContent === t("settings.test"));
    expect(test).toBeDefined();
    expect(test!.disabled).toBe(false);
    await act(async () => test!.click());
    await flush();
    expect(calls).toContain(`POST /launchers/${launcher.name}/check?machine=laptop`);
  });
});
