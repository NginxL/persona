// @vitest-environment jsdom
import { act, useState } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
vi.hoisted(() => {
  globalThis.ResizeObserver ??= class { observe() {} unobserve() {} disconnect() {} } as unknown as typeof ResizeObserver;
  window.matchMedia ??= (() => ({ matches: false, media: "", onchange: null, addEventListener() {}, removeEventListener() {}, addListener() {}, removeListener() {}, dispatchEvent: () => false })) as typeof window.matchMedia;
});
vi.mock("./ConfigurationManager", () => ({ ConfigurationManager: ({ children }: { children: (id: string) => unknown }) => children("assistant") }));
vi.mock("./ModelProfiles", () => ({ ModelProfiles: () => null }));
vi.mock("../../lib/store", () => ({ useStore: (select: (s: { machines: []; features: [] }) => unknown) => select({ machines: [], features: [] }) }));
import { initI18n, t } from "../../i18n";
import type { ModelBinding, ModelPurpose } from "../../lib/model-config";
import { ModelConfiguration } from "./ModelConfiguration";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
const providers = [
  { id: "api.screening", name: "Screening", kind: "api", api: "messages", model: "screening-model" },
  { id: "api.translation", name: "Translation", kind: "api", api: "messages", model: "translation-default" },
];
const purpose = (id: string, revision = 1, override: ModelBinding = {}): ModelPurpose => ({
  id, category_id: id, name: id === "translate" ? "Text and mail translation" : id,
  group: "Default models", kind: "api", revision, override,
  effective: { provider: id === "inbox.triage" ? "api.screening" : "api.translation", model: "default-model", ...override },
  fields: ["provider", "model", "effort"], provider_ids: providers.map((provider) => provider.id),
});
let all: ModelPurpose[];
let writes: { path: string; body: Record<string, unknown> }[];
let host: HTMLDivElement;
let root: Root;
const response = (value: unknown) => new Response(JSON.stringify(value));
const api = async (input: RequestInfo | URL, init?: RequestInit) => {
  const path = String(input).split("?")[0];
  if (init?.method === "PATCH") {
    const body = JSON.parse(String(init.body)) as Record<string, unknown>;
    writes.push({ path, body });
    const id = path.split("/").at(-1)!;
    all = all.map((p) => p.id === id ? {
      ...p,
      revision: p.revision + 1,
      override: { provider: String(body.provider), model: String(body.model) },
      effective: { ...p.effective, provider: String(body.provider), model: String(body.model) },
    } : p);
    return response({ purpose: all.find((p) => p.id === id), providers });
  }
  if (path.endsWith("/model-config")) return response({ purposes: all, providers });
  if (path.endsWith("/launchers/discover")) return response({ models: [{ id: "translation-default", name: "Translation default" }] });
  if (path.endsWith("/models")) return response({ agent: "claude", models: [{ id: "translation-default", name: "Translation default" }] });
  if (path.includes("/purposes/")) return response({ purpose: all.find((p) => path.endsWith(`/${p.id}`)), providers });
  throw new Error(`Unexpected request: ${path}`);
};
const flush = async () => { for (let i = 0; i < 6; i++) await act(async () => { await Promise.resolve(); }); };
const dialog = () => document.querySelector<HTMLElement>('[role="dialog"]')!;
const button = (label: string, scope: ParentNode = document) => [...scope.querySelectorAll("button")].find((b) => b.textContent === label)!;

beforeEach(async () => {
  await initI18n("en");
  all = [purpose("assistant"), purpose("agent.new"), purpose("kb"), purpose("inbox.triage"), purpose("agent.delegate"), purpose("translate")];
  writes = [];
  vi.stubGlobal("fetch", vi.fn(api));
  host = document.createElement("div"); document.body.append(host); root = createRoot(host);
});
afterEach(async () => {
  act(() => root.unmount()); host.remove(); vi.unstubAllGlobals(); await initI18n("en");
});

describe("independent translation model setting", () => {
  it("shows a translation row and saves its provider and model to translate only", async () => {
    function Harness() {
      const [open, onOpen] = useState<string | null>(null);
      return <ModelConfiguration open={open} onOpen={onOpen} onProviders={() => {}} />;
    }
    await act(async () => root.render(<Harness />)); await flush();
    const translationRow = host.querySelector<HTMLElement>('[data-model-purpose="translate"]');
    expect(translationRow).not.toBeNull();
    await act(async () => translationRow!.querySelector("button")!.click()); await flush();
    const provider = dialog().querySelector<HTMLSelectElement>("select")!;
    await act(async () => { provider.value = "api.translation"; provider.dispatchEvent(new Event("change", { bubbles: true })); }); await flush();
    const label = [...dialog().querySelectorAll("label")].find((node) => node.textContent?.includes(t("settings-modelconfiguration.api_model_id")));
    const model = label?.querySelector<HTMLInputElement>("input");
    expect(model).toBeInstanceOf(HTMLInputElement);
    act(() => {
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(model, "chosen-translation-model");
      model!.dispatchEvent(new Event("input", { bubbles: true }));
    });
    await act(async () => button(t("common.save"), dialog()).click()); await flush();
    expect(writes).toEqual([{ path: "/api/v1/model-config/purposes/translate", body: {
      revision: 1, provider: "api.translation", model: "chosen-translation-model", effort: null,
    } }]);
    expect(all.find((p) => p.id === "inbox.triage")?.override).toEqual({});
    expect(all.find((p) => p.id === "kb")?.override).toEqual({});
    expect(all.find((p) => p.id === "assistant")?.override).toEqual({});
  });
});
