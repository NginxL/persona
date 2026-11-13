// @vitest-environment jsdom
import { act, useState } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
vi.hoisted(() => {
  globalThis.ResizeObserver ??= class { observe() {} unobserve() {} disconnect() {} } as unknown as typeof ResizeObserver;
  window.matchMedia ??= ((query: string) => ({ matches: false, media: query, onchange: null, addEventListener() {}, removeEventListener() {}, addListener() {}, removeListener() {}, dispatchEvent: () => false })) as typeof window.matchMedia;
});
vi.mock("./ConfigurationManager", () => ({ ConfigurationManager: ({ children }: { children: (id: string) => unknown }) => children("assistant") }));
vi.mock("./ModelProfiles", () => ({ ModelProfiles: () => null }));
vi.mock("../../lib/store", () => ({ useStore: (select: (s: { machines: [] }) => unknown) => select({ machines: [] }) }));
vi.mock("../../ui/atoms", async (original) => ({ ...(await original<typeof import("../../ui/atoms")>()), toast: vi.fn() }));
import { initI18n, t } from "../../i18n";
import type { ModelBinding, ModelProvider, ModelPurpose } from "../../lib/model-config";
import { ModelConfiguration } from "./ModelConfiguration";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

// The main chat's "New chats' Claude profile" (#4633): offered by the hub for Claude Code only, saved as config_dir.
const providers: ModelProvider[] = [
  { id: "launcher.claude", name: "Claude Code", kind: "launcher" },
  { id: "launcher.codex", name: "Codex", kind: "launcher", agent: "codex" },
];
let main: ModelPurpose;
let writes: Record<string, unknown>[];
const assistant = (effective: ModelBinding, offered = true): ModelPurpose => ({
  id: "assistant", category_id: "assistant", name: "主对话", group: "Default models", kind: "launcher", launch: "headless", revision: 4,
  override: {}, effective, fields: ["provider", "model", "effort", "runtime", ...(offered ? ["config_dir"] : [])],
  provider_ids: providers.map((p) => p.id), runtimes: ["cli", "agentsdk"], ...(offered ? { config_dirs: ["private", "legacy"] } : {}),
});
const json = (value: unknown, status = 200) => new Response(JSON.stringify(value), { status });
const hub = async (input: RequestInfo | URL, init?: RequestInit): Promise<Response> => {
  const path = String(input).split("?")[0];
  if (init?.method === "PATCH") {
    const body = JSON.parse(String(init.body)) as Record<string, unknown>;
    writes.push(body);
    const override: ModelBinding = typeof body.config_dir === "string" && body.config_dir !== "private" ? { config_dir: body.config_dir } : {};
    main = { ...main, override, effective: { ...main.effective, ...override }, revision: main.revision + 1 };
    return json({ purpose: main, providers });
  }
  if (path.endsWith("/model-config")) return json({ purposes: [main], providers });
  if (path.endsWith("/purposes/assistant.supervise")) return json({ error: "not found" }, 404);
  if (path.endsWith("/purposes/assistant")) return json({ purpose: main, providers });
  if (path.endsWith("/models")) return json({ error: "unavailable" }, 404);
  throw new Error(`Unexpected request: ${path}`);
};
let host: HTMLDivElement;
let root: Root;
const flush = async () => { for (let i = 0; i < 6; i++) await act(async () => { await Promise.resolve(); }); };
const dialog = () => document.querySelector<HTMLElement>('[role="dialog"]')!;
const profileChoice = () => dialog().querySelector<HTMLElement>(`[role="radiogroup"][aria-label="${t("settings-modelconfiguration.config_dir")}"]`);
async function openMain() {
  function Harness() {
    const [open, onOpen] = useState<string | null>(null);
    return <ModelConfiguration open={open} onOpen={onOpen} onProviders={() => {}} />;
  }
  await act(async () => root.render(<Harness />));
  await flush();
  const row = [...host.querySelectorAll("button")].find((b) => b.textContent?.includes("主对话"))!;
  await act(async () => row.click());
  await flush();
}
beforeEach(async () => {
  await initI18n("zh-Hans");
  writes = [];
  vi.stubGlobal("fetch", vi.fn(hub));
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});
afterEach(() => {
  act(() => root.unmount());
  host.remove();
  vi.unstubAllGlobals();
});

describe("main chat's profile for new conversations", () => {
  it("offers Persona's own and this computer's, and saves the choice", async () => {
    main = assistant({ provider: "launcher.claude" });
    await openMain();
    const choice = profileChoice();
    expect(choice).not.toBeNull();
    const radios = [...choice!.querySelectorAll<HTMLButtonElement>('[role="radio"]')];
    expect(radios.map((r) => r.textContent)).toEqual([t("settings-modelconfiguration.config_dir_private"), t("settings-modelconfiguration.config_dir_legacy")]);
    expect(radios[0].getAttribute("aria-checked")).toBe("true");
    expect(dialog().textContent).toContain(t("settings-modelconfiguration.config_dir_hint"));
    await act(async () => radios[1].click());
    const save = [...dialog().querySelectorAll("button")].find((b) => b.textContent === t("common.save"))!;
    await act(async () => save.click());
    await flush();
    expect(writes).toHaveLength(1);
    expect(writes[0]).toMatchObject({ revision: 4, config_dir: "legacy", runtime: "cli" });
  });

  it("shows the saved choice in what runs now", async () => {
    main = { ...assistant({ provider: "launcher.claude", config_dir: "legacy" }), override: { config_dir: "legacy" } };
    await openMain();
    expect(profileChoice()!.querySelector('[aria-checked="true"]')!.textContent).toBe(t("settings-modelconfiguration.config_dir_legacy"));
    expect(dialog().textContent).toContain(t("settings-modelconfiguration.config_dir"));
  });

  it("is not offered for another agent or by an older hub", async () => {
    main = assistant({ provider: "launcher.codex" });
    await openMain();
    expect(profileChoice()).toBeNull();
    act(() => root.unmount());
    root = createRoot(host);
    main = assistant({ provider: "launcher.claude" }, false);
    await openMain();
    expect(profileChoice()).toBeNull();
  });
});
