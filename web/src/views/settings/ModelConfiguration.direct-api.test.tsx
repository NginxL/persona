// @vitest-environment jsdom
import { act, useState } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
vi.hoisted(() => {
  globalThis.ResizeObserver ??= class { observe() {} unobserve() {} disconnect() {} } as unknown as typeof ResizeObserver;
  window.matchMedia ??= ((query: string) => ({ matches: false, media: query, onchange: null, addEventListener() {}, removeEventListener() {}, addListener() {}, removeListener() {}, dispatchEvent: () => false })) as typeof window.matchMedia;
});
vi.mock("./ConfigurationManager", () => ({ ConfigurationManager: ({ children }: { children: (id: string) => unknown }) => children("test-manager") }));
vi.mock("./ModelProfiles", () => ({ ModelProfiles: () => null }));
vi.mock("../../lib/store", () => ({ useStore: (select: (state: { machines: []; features: [] }) => unknown) => select({ machines: [], features: [] }) }));
vi.mock("../../ui/atoms", async (original) => ({ ...(await original<typeof import("../../ui/atoms")>()), toast: vi.fn() }));
import { initI18n, t } from "../../i18n";
import { MANAGER_HEADER } from "../../lib/manager-choice";
import type { ModelProvider, ModelPurpose } from "../../lib/model-config";
import type { ProviderModel } from "../../lib/providers";
import { ModelConfiguration } from "./ModelConfiguration";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const providers: ModelProvider[] = [
  { id: "launcher.messages", name: "Messages provider", kind: "api", api: "messages", model: "messages-default" },
  { id: "launcher.responses", name: "Responses provider", kind: "api", api: "responses", model: "responses-default" },
  { id: "launcher.chat", name: "Chat provider", kind: "api", api: "chat", chat_effort: true, model: "chat-default" },
];
const models: Record<"messages" | "responses" | "chat", ProviderModel[]> = {
  messages: [
    { id: "messages-default", name: "Messages default", efforts: ["off", "low", "high"] },
    { id: "messages-new", name: "Friendly model", efforts: ["low", "high"] },
  ],
  responses: [
    { id: "responses-default", efforts: ["low", "medium", "high"] },
    { id: "responses-new", name: "Responses model", efforts: ["medium", "high", "xhigh"] },
  ],
  chat: [
    { id: "chat-default", efforts: ["high"] },
    { id: "chat-new", efforts: ["medium", "high"] },
    { id: "chat-none", efforts: [], no_effort: true },
  ],
};
type Call = { path: string; method: string; body?: Record<string, unknown>; headers: Headers };
let calls: Call[];
let current: ModelPurpose;
let result: Record<string, unknown> | undefined;
let root: Root;
let host: HTMLDivElement;
const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status });
const flush = async () => { for (let i = 0; i < 6; i++) await act(async () => { await Promise.resolve(); }); };
const dialog = () => document.querySelector<HTMLElement>('[role="dialog"]')!;
const button = (label: string, scope: ParentNode = document) => [...scope.querySelectorAll<HTMLButtonElement>("button")].find((node) => node.textContent === label)!;
const select = (label: string) => [...dialog().querySelectorAll<HTMLSelectElement>("select")].find((node) => node.closest("label")?.querySelector("span")?.textContent === label)!;
const modelInput = () => dialog().querySelector<HTMLInputElement>("input")!;
const options = (node: HTMLSelectElement) => [...node.options].map((option) => option.value);
const probes = () => calls.filter((call) => call.path.endsWith("/launchers/test"));
const writes = () => calls.filter((call) => ["PUT", "PATCH", "DELETE"].includes(call.method)
  || (call.method === "POST" && !["/api/v1/launchers/test", "/api/v1/launchers/discover"].includes(call.path)));

async function choose(node: HTMLSelectElement, value: string) {
  await act(async () => {
    node.value = value;
    node.dispatchEvent(new Event("change", { bubbles: true }));
  });
  await flush();
}
function typeModel(value: string) {
  act(() => {
    const input = modelInput();
    Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(input, value);
    input.dispatchEvent(new Event("input", { bubbles: true }));
  });
}
async function open() {
  await act(async () => [...host.querySelectorAll<HTMLButtonElement>("button")].find((node) => node.textContent?.includes(current.name))!.click());
  await flush();
}
async function testConnection() {
  await act(async () => button(t("settings-modelconfiguration.api_test"), dialog()).click());
  await flush();
}
async function render() {
  function Harness() {
    const [open, onOpen] = useState<string | null>(null);
    return <ModelConfiguration open={open} onOpen={onOpen} onProviders={() => {}} />;
  }
  await act(async () => root.render(<Harness />));
  await flush();
  await open();
}

beforeEach(async () => {
  await initI18n("zh-Hans");
  calls = [];
  result = undefined;
  providers[2].chat_effort = true;
  models.chat[2] = { id: "chat-none", efforts: [], no_effort: true };
  providers[2].models = models.chat.map((model) => ({ ...model }));
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const path = String(input).split("?")[0];
    const method = init?.method ?? "GET";
    const body = init?.body ? JSON.parse(String(init.body)) as Record<string, unknown> : undefined;
    calls.push({ path, method, body, headers: new Headers(init?.headers) });
    if (path.endsWith("/model-config")) return json({ purposes: [current], providers });
    if (path.endsWith("/purposes/assistant.supervise")) return json({ error: "not found" }, 404);
    if (path.endsWith(`/purposes/${current.id}`)) return json({ purpose: current, providers });
    if (path.endsWith("/launchers/discover")) return json({ models: models[body!.name as keyof typeof models] });
    if (path.endsWith("/launchers/test")) return json(result ?? { ok: true, ms: 42, model: body!.model, effort: body!.effort });
    throw new Error(`Unexpected request: ${method} ${path}`);
  }));
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});
afterEach(async () => {
  act(() => root.unmount());
  host.remove();
  vi.unstubAllGlobals();
  await initI18n("zh-Hans");
});

describe.each(["kb", "inbox.triage", "agent.delegate"])("%s Direct API editor", (id) => {
  beforeEach(() => {
    current = {
      id, name: `Purpose ${id}`, group: "Background", kind: "api", revision: 7,
      override: { provider: "launcher.messages", model: "saved-manual-model", effort: "provider-special" },
      effective: { provider: "launcher.messages", model: "saved-manual-model", effort: "provider-special" },
      fields: ["provider", "model", "effort"], provider_ids: providers.map((provider) => provider.id),
    };
  });

  it("replaces Unverified with the successful draft result instead of keeping contradictory badges", async () => {
    current.status = "未验证"; // i18n-ignore: stable protocol sentinel.
    await render();
    expect(document.body.textContent?.split(t("settings-modelconfiguration.api_test_unverified")).length).toBe(2);
    await testConnection();
    expect(document.body.textContent).toContain(t("settings-modelconfiguration.api_test_ok", { ms: 42 }));
    expect(document.body.textContent).not.toContain(t("settings-modelconfiguration.api_test_unverified"));
    expect(writes()).toEqual([]);
  });

  it("discovers models, keeps manual IDs and nonstandard effort, and follows model capability choices", async () => {
    await render();
    expect(calls.filter((call) => call.path.endsWith("/launchers/discover")).map((call) => call.body)).toEqual([{ name: "messages" }]);
    expect(options(select(t("settings-modelconfiguration.api_models")))).toEqual(["", "saved-manual-model", "messages-default", "messages-new"]);
    expect(modelInput().value).toBe("saved-manual-model");
    expect(select(t("settings-modelconfiguration.effort")).value).toBe("provider-special");
    await choose(select(t("settings-modelconfiguration.api_models")), "messages-new");
    expect(modelInput().value).toBe("messages-new");
    expect(options(select(t("settings-modelconfiguration.effort")))).toEqual(["", "provider-special", "low", "high"]);
    await choose(select(t("settings-modelconfiguration.effort")), "high");
    typeModel("private/manual-model");
    expect(select(t("settings-modelconfiguration.api_models")).value).toBe("private/manual-model");
    expect(select(t("settings-modelconfiguration.effort")).value).toBe("high");
    expect(probes()).toHaveLength(0);
    expect(writes()).toEqual([]);
  });

  it.each(["messages", "responses", "chat"])("offers and saves effort for %s when the original Chat override has no effort", async (api) => {
    // Older hubs omit effort for Chat; their switch to Messages or Responses must still offer it.
    if (api !== "chat") {
      current.fields = ["provider", "model"];
      delete providers[2].chat_effort;
    }
    current.override = { provider: "launcher.chat", model: "chat-default" };
    current.effective = { ...current.override };
    const revision = current.revision;
    await render();
    if (api !== "chat") expect(select(t("settings-modelconfiguration.effort"))).toBeUndefined();

    await choose(select(t("settings-modelconfiguration.model_provider")), `launcher.${api}`);
    const effort = select(t("settings-modelconfiguration.effort"));
    expect(effort).toBeDefined();
    expect(effort.disabled).toBe(false);
    expect(options(effort)).toContain("high");
    await choose(select(t("settings-modelconfiguration.api_models")), `${api}-new`);
    await choose(effort, "high");
    await testConnection();
    expect(probes().at(-1)?.body).toEqual({ name: api, model: `${api}-new`, effort: "high" });
    expect(writes()).toEqual([]);

    if (api === "chat") {
      current = {
        ...current, revision: revision + 1,
        override: { provider: "launcher.chat", model: "chat-new", effort: "high" },
        effective: { provider: "launcher.chat", model: "chat-new", effort: "high" },
      };
    }
    await act(async () => button(t("common.save"), dialog()).click());
    await flush();
    expect(writes()).toHaveLength(1);
    expect(writes()[0].body).toEqual({
      revision, provider: `launcher.${api}`, model: `${api}-new`, effort: "high",
    });

    if (api === "chat") {
      await open();
      expect(select(t("settings-modelconfiguration.effort")).value).toBe("high");
      await choose(select(t("settings-modelconfiguration.effort")), "");
      await act(async () => button(t("common.save"), dialog()).click());
      await flush();
      expect(writes()[1].body).toEqual({ revision: revision + 1, provider: "launcher.chat", model: "chat-new", effort: null });
    }
  });

  it("does not offer or save Chat effort after switching from Messages on an older hub", async () => {
    delete providers[2].chat_effort;
    current.override = { provider: "launcher.messages", model: "messages-default" };
    current.effective = { ...current.override };
    await render();
    await choose(select(t("settings-modelconfiguration.model_provider")), "launcher.chat");
    expect(select(t("settings-modelconfiguration.effort"))).toBeUndefined();
    await choose(select(t("settings-modelconfiguration.api_models")), "chat-new");
    await act(async () => button(t("common.save"), dialog()).click());
    await flush();
    expect(writes()).toHaveLength(1);
    expect(writes()[0].body).toEqual({ revision: current.revision, provider: "launcher.chat", model: "chat-new" });
  });

  it("tests the draft provider, raw model ID and effort without saving, including cancel/reopen", async () => {
    const saved = structuredClone(current);
    await render();
    await choose(select(t("settings-modelconfiguration.model_provider")), "launcher.responses");
    await choose(select(t("settings-modelconfiguration.api_models")), "responses-new");
    await choose(select(t("settings-modelconfiguration.effort")), "xhigh");
    await testConnection();
    expect(probes()).toHaveLength(1);
    expect(probes()[0].body).toEqual({ name: "responses", model: "responses-new", effort: "xhigh" });
    expect(probes()[0].headers.get(MANAGER_HEADER)).toBe("test-manager");
    expect(dialog().textContent).toContain(t("settings-modelconfiguration.api_test_ok", { ms: 42 }));
    expect(writes()).toEqual([]);
    expect(current).toEqual(saved);
    await act(async () => button(t("common.cancel"), dialog()).click());
    await open();
    expect(modelInput().value).toBe("saved-manual-model");
    expect(select(t("settings-modelconfiguration.effort")).value).toBe("provider-special");
    expect(dialog().textContent).not.toContain(t("settings-modelconfiguration.api_test_ok", { ms: 42 }));
  });

  it.each(["provider", "model", "effort"])("invalidates a verified result after a draft %s change", async (field) => {
    current.override = { ...current.override, model: "messages-new", effort: "high" };
    current.effective = { ...current.effective, model: "messages-new", effort: "high" };
    await render();
    await testConnection();
    expect(dialog().textContent).toContain(t("settings-modelconfiguration.api_test_ok", { ms: 42 }));
    if (field === "provider") await choose(select(t("settings-modelconfiguration.model_provider")), "launcher.responses");
    else if (field === "model") typeModel("another-model");
    else await choose(select(t("settings-modelconfiguration.effort")), "low");
    expect(dialog().textContent).not.toContain(t("settings-modelconfiguration.api_test_ok", { ms: 42 }));
    expect(writes()).toEqual([]);
  });

  it.each(["provider", "model", "effort"])("does not probe the old effective configuration when a saved %s override is cleared", async (field) => {
    await render();
    if (field === "provider") await choose(select(t("settings-modelconfiguration.model_provider")), "");
    else if (field === "model") typeModel("");
    else await choose(select(t("settings-modelconfiguration.effort")), "");
    expect(button(t("settings-modelconfiguration.api_test"), dialog()).disabled).toBe(true);
    expect(dialog().textContent).toContain(t("settings-modelconfiguration.api_test_inheritance"));
    await testConnection();
    expect(probes()).toEqual([]);
    expect(writes()).toEqual([]);
  });

  it.each([
    ["auth", "api_test_auth"], ["model_not_found", "api_test_model"], ["not_found", "api_test_model"],
    ["timeout", "api_test_timeout"], ["upstream_error", "api_test_other"],
    ["region_blocked", "api_test_other"], ["wrong_protocol", "api_test_other"],
  ])("shows the localized %s failure without modifying settings", async (code, key) => {
    result = { ok: false, ms: 15, code, error: code === "region_blocked" ? "Synthetic upstream detail: region refused (403)" : "Synthetic upstream detail" };
    const saved = structuredClone(current);
    await render();
    await testConnection();
    expect(dialog().querySelector('[role="alert"]')?.textContent).toContain(t(`settings-modelconfiguration.${key}`));
    expect(dialog().textContent).toContain("Synthetic upstream detail");
    expect(dialog().textContent).not.toContain(t("settings-modelconfiguration.api_test_ok", { ms: 15 }));
    expect(writes()).toEqual([]);
    expect(current).toEqual(saved);
  });

  it("tests the inherited Chat effort and does not verify a degraded result", async () => {
    current.override = { provider: "launcher.chat", model: "chat-default" };
    current.effective = { ...current.override, effort: "high" };
    result = { ok: true, ms: 20, model: "chat-default", effort: "" };
    await render();
    await testConnection();
    expect(probes()[0].body).toEqual({ name: "chat", model: "chat-default", effort: "high" });
    expect(dialog().querySelector('[role="alert"]')?.textContent).toContain(t("settings-modelconfiguration.api_test_effort"));
    expect(dialog().textContent).not.toContain(t("settings-modelconfiguration.api_test_ok", { ms: 20 }));
    expect(writes()).toEqual([]);
  });

  it("accepts deliberate suppression when the stored model does not support the inherited effort", async () => {
    current.override = { provider: "launcher.chat", model: "chat-default" };
    current.effective = { ...current.override, effort: "low" };
    result = { ok: true, ms: 20, model: "chat-default", effort: "" };
    await render();
    await testConnection();
    expect(probes()[0].body).toEqual({ name: "chat", model: "chat-default", effort: "low" });
    expect(dialog().textContent).toContain(t("settings-modelconfiguration.api_test_ok", { ms: 20 }));
    expect(dialog().textContent).not.toContain(t("settings-modelconfiguration.api_test_effort"));
    expect(writes()).toEqual([]);
  });

  it("accepts deliberate inherited-effort suppression from stored no_effort metadata", async () => {
    models.chat[2] = { ...models.chat[2], no_effort: false };
    current.override = { provider: "launcher.chat", model: "chat-none" };
    current.effective = { ...current.override, effort: "low" };
    result = { ok: true, ms: 20, model: "chat-none", effort: "" };
    await render();
    await testConnection();
    expect(probes()[0].body).toEqual({ name: "chat", model: "chat-none", effort: "low" });
    expect(dialog().textContent).toContain(t("settings-modelconfiguration.api_test_ok", { ms: 20 }));
    expect(dialog().textContent).not.toContain(t("settings-modelconfiguration.api_test_effort"));
    expect(writes()).toEqual([]);
  });

  it("accepts the inherited effort when the hub does not suppress it", async () => {
    current.override = { provider: "launcher.chat", model: "chat-default" };
    current.effective = { ...current.override, effort: "high" };
    result = { ok: true, ms: 20, model: "chat-default", effort: "high" };
    await render();
    await testConnection();
    expect(probes()[0].body).toEqual({ name: "chat", model: "chat-default", effort: "high" });
    expect(dialog().textContent).toContain(t("settings-modelconfiguration.api_test_ok", { ms: 20 }));
    expect(dialog().textContent).not.toContain(t("settings-modelconfiguration.api_test_effort"));
  });

  it("does not claim an effort was verified when an older hub ignores it", async () => {
    result = { ok: true, ms: 20, model: "saved-manual-model" };
    await render();
    await testConnection();
    expect(dialog().querySelector('[role="alert"]')?.textContent).toContain(t("settings-modelconfiguration.api_test_effort"));
    expect(dialog().textContent).not.toContain(t("settings-modelconfiguration.api_test_ok", { ms: 20 }));
    expect(writes()).toEqual([]);
  });
});
