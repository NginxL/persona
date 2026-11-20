// @vitest-environment jsdom
import { act, useState } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
vi.hoisted(() => {
  globalThis.ResizeObserver ??= class { observe() {} unobserve() {} disconnect() {} } as unknown as typeof ResizeObserver;
  window.matchMedia ??= ((query: string) => ({ matches: false, media: query, addEventListener() {}, removeEventListener() {}, addListener() {}, removeListener() {} })) as typeof window.matchMedia;
});
vi.mock("./ConfigurationManager", () => ({ ConfigurationManager: ({ children }: { children: (id: string) => unknown }) => children("manager-b") }));
vi.mock("./ModelProfiles", () => ({ ModelProfiles: () => null }));
vi.mock("../../lib/store", () => ({ useStore: (select: (s: { machines: []; features: [] }) => unknown) => select({ machines: [], features: [] }) }));
vi.mock("../../ui/atoms", async (original) => ({ ...(await original<typeof import("../../ui/atoms")>()), toast: vi.fn() }));
import { formatDate, initI18n, t } from "../../i18n";
import type { ModelProvider, ModelPurpose } from "../../lib/model-config";
import { MANAGER_HEADER } from "../../lib/manager-choice";
import { ModelConfiguration } from "./ModelConfiguration";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
const providers: ModelProvider[] = [
  { id: "launcher.primary", name: "North", kind: "api", has_key: true, model: "default-a" },
  { id: "launcher.messages", name: "South", kind: "launcher", api: "messages", has_key: true },
  { id: "launcher.cli", name: "On this computer", kind: "launcher", api: "responses", has_key: true },
  { id: "legacy.ai", name: "Legacy", kind: "api", has_key: true },
  { id: "launcher.no-key", name: "No key", kind: "api" },
  { id: "launcher.hidden", name: "Hidden", kind: "api", has_key: true, hidden: true },
  { id: "launcher.other", name: "Another purpose", kind: "api", has_key: true },
];
const last = "2026-09-14T12:30:00Z";
let current: ModelPurpose, root: Root, host: HTMLDivElement;
let writes: { body: Record<string, unknown>; manager: string | null }[];
const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status });
const dialog = () => document.querySelector<HTMLElement>('[role="dialog"]')!;
const tr = (key: string, index = 1) => t(`settings-modelconfiguration.candidate_${key}`, { index });
const control = <T extends HTMLElement,>(label: string): T => Array.from(dialog().querySelectorAll<T>("[aria-label]")).find((e) => e.getAttribute("aria-label") === label)!;
const mode = () => control<HTMLSelectElement>(tr("title"));
const provider = (i: number) => control<HTMLSelectElement>(tr("provider", i));
const model = (i: number) => control<HTMLInputElement>(tr("model", i));
const action = (name: string) => [...dialog().querySelectorAll("button")].find((b) => b.textContent === name || b.getAttribute("aria-label") === name)!;
const click = async (node: HTMLElement) => act(async () => node.click());
const choose = async (el: HTMLSelectElement, value: string) => act(async () => { el.value = value; el.dispatchEvent(new Event("change", { bubbles: true })); });
const type = async (el: HTMLInputElement, value: string) => act(async () => {
  Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(el, value);
  el.dispatchEvent(new Event("input", { bubbles: true }));
});
const render = async () => {
  function Harness() {
    const [open, setOpen] = useState<string | null>(current.id);
    return <ModelConfiguration open={open} onOpen={setOpen} onProviders={() => {}} />;
  }
  await act(async () => root.render(<Harness />));
};
beforeEach(async () => {
  await initI18n("en");
  current = { id: "extension.library", name: "Library", group: "Extensions", kind: "api", category_id: "kb", parent: "kb",
    revision: 7, fields: ["provider", "model"], override: {}, effective: { provider: "launcher.primary", model: "default-a" }, provider_ids: providers.slice(0, -1).map((p) => p.id),
    candidates: { mode: "extension", modes: ["extension", "off", "custom"], most: 2, custom: [], direct_provider_ids: ["launcher.primary", "launcher.messages", "launcher.other"],
      requested: [{ last_used: last, models: [{ model: "first", provider: "launcher.primary", provider_name: "North", available: true }, { model: "missing", available: false, problem: "No configured provider offers this model" }] }] } };
  writes = [];
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const path = String(input).split("?")[0];
    if (path.endsWith("/model-config")) return json({ purposes: [current], providers });
    if (path.endsWith("/purposes/assistant.supervise")) return json({}, 404);
    if (path.endsWith(`/purposes/${current.id}`)) {
      if (init?.method === "PATCH") {
        const body = JSON.parse(String(init.body)); writes.push({ body, manager: new Headers(init.headers).get(MANAGER_HEADER) });
        if (body.revision !== current.revision) return json({ error: "revision conflict" }, 409);
      }
      return json({ purpose: current, providers });
    }
    throw Error(`Unexpected request: ${path}`);
  }));
  host = document.createElement("div"); document.body.append(host); root = createRoot(host);
});
afterEach(async () => { act(() => root.unmount()); host.remove(); vi.unstubAllGlobals(); await initI18n("en"); });

describe("extension candidate settings", () => {
  it.each(["en", "zh-Hans", "ja"])("shows ordered requests, resolution, problems and last use in %s", async (lang) => {
    await initI18n(lang); await render();
    expect(mode().value).toBe("extension");
    expect([...mode().options].map((o) => o.text)).toEqual([tr("extension"), tr("off"), tr("custom")]);
    const history = dialog().querySelector("details")!;
    expect(history.open).toBe(false); await click(history.querySelector("summary")!);
    expect([...history.querySelectorAll("li")].map((li) => li.textContent)).toEqual(["firstNorth", `missing${tr("unavailable")} · No configured provider offers this model`]);
    expect(history.querySelector("time")?.textContent).toContain(formatDate(last, { dateStyle: "medium", timeStyle: "short" }));
    expect(writes).toEqual([]);
  });

  it("edits, caps, reorders and removes pairs, including a provider default, in one sparse revisioned write", async () => {
    await render(); await choose(mode(), "custom");
    expect(action(t("common.save")).disabled).toBe(true);
    expect([...provider(1).options].map((o) => o.value)).toEqual(["", "launcher.primary", "launcher.messages"]);
    await choose(provider(1), "launcher.primary"); await type(model(1), "manual/model"); await click(action(tr("add")));
    await choose(provider(2), "launcher.primary"); await type(model(2), " manual/model ");
    expect(action(tr("add")).disabled).toBe(true); expect(action(t("common.save")).disabled).toBe(true);
    expect(dialog().textContent).toContain(tr("duplicate"));
    await choose(provider(2), "launcher.messages"); expect(model(2).value).toBe("");
    await click(action(tr("up", 2))); expect(provider(1).value).toBe("launcher.messages");
    await click(action(tr("down", 1))); await click(action(tr("remove", 1))); expect(provider(1).value).toBe("launcher.messages");
    await click(action(tr("add"))); await choose(provider(2), "launcher.primary"); await type(model(2), " model-b ");
    await click(action(t("common.save")));
    expect(writes).toEqual([{ manager: "manager-b", body: { revision: 7, candidates: { mode: "custom", custom: [{ provider: "launcher.messages", model: "" }, { provider: "launcher.primary", model: "model-b" }] } } }]);
  });

  it("preserves a saved provider default and leaves candidates out of an ordinary binding edit", async () => {
    current.candidates!.mode = "custom";
    current.candidates!.custom = [{ provider: "launcher.primary", model: "default-a", configured_model: "", available: true }];
    await render(); expect(model(1).value).toBe("");
    await type(control<HTMLInputElement>(t("settings-modelconfiguration.specific_model")), "binding-only");
    await click(action(t("common.save"))); expect(writes[0].body).toMatchObject({ revision: 7, model: "binding-only" });
    expect(writes[0].body).not.toHaveProperty("candidates");
  });

  it("retains a deleted connection for repair and keeps empty configured_model when reordering", async () => {
    current.candidates!.mode = "custom";
    current.candidates!.custom = [
      { provider: "launcher.retired", provider_name: "Retired", model: "old-model", available: false, problem: "Connection removed" },
      { provider: "launcher.primary", model: "default-a", configured_model: "", available: true },
    ];
    await render(); expect(provider(1).selectedOptions[0].text).toBe("Retired");
    expect(dialog().textContent).toContain("Connection removed"); expect(model(2).value).toBe("");
    await click(action(tr("up", 2))); expect(action(t("common.save")).disabled).toBe(true);
    expect(action(t("settings-modelconfiguration.restore")).disabled).toBe(true);
    await choose(provider(2), "launcher.messages"); await type(model(2), "replacement");
    await click(action(t("common.save")));
    expect(writes[0].body).toEqual({ revision: 7, candidates: { mode: "custom", custom: [
      { provider: "launcher.primary", model: "" }, { provider: "launcher.messages", model: "replacement" },
    ] } });
  });

  it("uses the manager's authoritative list even when old provider metadata is missing", async () => {
    current.candidates!.modes = ["off", "custom"]; current.candidates!.mode = "off";
    current.candidates!.direct_provider_ids = ["launcher.no-key", "launcher.other"];
    await render(); expect([...mode().options].map((option) => option.value)).toEqual(["off", "custom"]);
    await choose(mode(), "custom");
    expect(provider(1).value).toBe("launcher.no-key");
    expect([...provider(1).options].map((option) => option.value)).toEqual(["", "launcher.no-key"]);
    await click(action(t("common.save")));
    expect(writes[0].body).toEqual({ revision: 7, candidates: { mode: "custom", custom: [{ provider: "launcher.no-key", model: "" }] } });
  });

  it("keeps unsaved rows on a 409 and requires explicit reload before using the newer revision", async () => {
    await render(); await choose(mode(), "custom"); await choose(provider(1), "launcher.primary"); await type(model(1), "my-pending");
    current = { ...current, revision: 8, candidates: { ...current.candidates!, mode: "off" } };
    await click(action(t("common.save")));
    expect(model(1).value).toBe("my-pending"); expect(action(t("common.save")).disabled).toBe(true);
    await click(action(t("common.save"))); expect(writes).toHaveLength(1);
    await click(action(t("settings-modelconfiguration.reload_discard"))); expect(mode().value).toBe("off");
    await choose(mode(), "extension"); await click(action(t("common.save")));
    expect(writes[1].body).toEqual({ revision: 8, candidates: { mode: "extension" } });
  });

  it("saves off and a changed ordinary binding atomically", async () => {
    await render(); await choose(mode(), "off"); await type(control<HTMLInputElement>(t("settings-modelconfiguration.specific_model")), "shared-draft");
    await click(action(t("common.save")));
    expect(writes).toHaveLength(1); expect(writes[0].body).toMatchObject({ revision: 7, model: "shared-draft", candidates: { mode: "off" } });
  });

  it("respects an empty direct list even when keyed providers exist", async () => {
    current.candidates!.direct_provider_ids = []; current.candidates!.requested = [];
    await render(); expect(dialog().textContent).toContain(tr("no_requests")); await choose(mode(), "custom");
    expect(action(tr("add")).disabled).toBe(true); expect(action(t("common.save")).disabled).toBe(true); expect(dialog().textContent).toContain(tr("no_providers"));
    await choose(mode(), "off"); await click(action(t("common.save"))); expect(writes[0].body).toEqual({ revision: 7, candidates: { mode: "off" } });
  });

  it("filters older hub providers conservatively, preserving its original model spelling", async () => {
    delete current.candidates!.direct_provider_ids; current.candidates!.mode = "custom";
    current.candidates!.custom = [{ model: "old/choice", provider: "launcher.primary", available: true }]; await render();
    expect([...provider(1).options].map((o) => o.value)).toEqual(["", "launcher.primary", "launcher.messages"]); expect(model(1).value).toBe("old/choice");
    await type(model(1), "bad model"); expect(action(t("common.save")).disabled).toBe(true);
    await type(model(1), "old/choice"); await click(action(t("common.save"))); expect(writes[0].body).not.toHaveProperty("candidates");
  });

  it.each(["builtin", "old-extension"])("keeps the ordinary %s editor", async (kind) => {
    if (kind === "builtin") current.id = "kb"; else delete current.candidates;
    await render(); expect(mode()).toBeUndefined(); expect(action(t("common.save"))).toBeDefined();
  });
});
