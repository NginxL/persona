// @vitest-environment jsdom
// Model settings lists the two meeting uses set apart from their categories (#7980, docs/assistant/model-config.md
// "Meeting minutes and the in-meeting conversation"): the In-meeting conversation right after Main chat and the
// background supervisor, Meeting minutes right after Information processing, each saying it follows its category by the
// category's name or that it is separately configured, with a reset to follow it. The room's editor offers only the
// hub's providers, no runtime, and says it runs on Claude Code only. A hub before #7980 lists neither and is asked
// nothing for them; every other row stays as it was.
import { act, useState } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
vi.hoisted(() => {
  globalThis.ResizeObserver ??= class { observe() {} unobserve() {} disconnect() {} } as unknown as typeof ResizeObserver;
  window.matchMedia ??= ((query: string) => ({ matches: false, media: query, onchange: null, addEventListener() {}, removeEventListener() {}, addListener() {}, removeListener() {}, dispatchEvent: () => false })) as typeof window.matchMedia;
});
vi.mock("./ConfigurationManager", () => ({ ConfigurationManager: ({ children }: { children: (id: string) => unknown }) => children("assistant") }));
vi.mock("./ModelProfiles", () => ({ ModelProfiles: () => null }));
vi.mock("../../lib/store", () => ({ useStore: (select: (s: { machines: []; features: [] }) => unknown) => select({ machines: [], features: [] }) }));
vi.mock("../../ui/atoms", async (original) => ({ ...(await original<typeof import("../../ui/atoms")>()), toast: vi.fn() }));
import { initI18n, t } from "../../i18n";
import type { ModelBinding, ModelProvider, ModelPurpose } from "../../lib/model-config";
import { effortTitle } from "../../lib/session-actions";
import { ModelConfiguration } from "./ModelConfiguration";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const providers: ModelProvider[] = [
  { id: "launcher.claude", name: "Claude Code", kind: "launcher", agent: "claude" },
  { id: "launcher.codex", name: "Codex", kind: "launcher", agent: "codex" },
  { id: "api.messages", name: "Messages gateway", kind: "api", api: "messages", model: "api-default", has_key: true },
  { id: "api.chat", name: "Chat gateway", kind: "api", api: "chat", chat_effort: true, model: "chat-default", has_key: true },
];
/** The catalog's names in the hub's language: the page's words take the category's name from there. */
const NAMES: Record<string, Record<string, string>> = {
  en: { assistant: "Main chat", kb: "Information processing", room: "In-meeting conversation", minutes: "Meeting minutes" },
  "zh-Hans": { assistant: "主对话", kb: "信息处理", room: "会中对话", minutes: "会议纪要" },
  ja: { assistant: "メインチャット", kb: "情報処理", room: "会議中の会話", minutes: "議事録" },
};
const ROOM_DESCRIPTION = "Persona in a recorded meeting: answers the questions asked there. Runs only on Claude Code.";
/** What the two categories run on: what each use follows while its record is empty. */
const MAIN: ModelBinding = { provider: "launcher.claude", model: "claude-model" };
const KB: ModelBinding = { provider: "api.messages", model: "kb-model" };
const categoryBinding = (id: string) => id === "meeting.room" ? MAIN : KB;
/** Claude Code's /model list on the launcher (#943), with its own default. */
const CLAUDE_MODELS = { agent: "claude", models: [
  { id: "default", name: "Default", default: true, resolved: "launcher-default-model", efforts: ["low", "medium", "high"] },
  { id: "claude-model", name: "Claude model", efforts: ["low", "medium", "high"] },
  { id: "claude-small", name: "Claude small", efforts: ["low"] },
] };
let lang: string;
const name = (key: string) => NAMES[lang][key];
const fields = ["provider", "model", "effort"];
const category = (id: string, kind: ModelPurpose["kind"], launch: ModelPurpose["launch"], effective: ModelBinding, extra: Partial<ModelPurpose> = {}): ModelPurpose => ({
  id, category_id: id, name: NAMES[lang][id] ?? id, group: "Default models", kind, launch, revision: 1, override: {}, effective,
  fields, uses: [{ name: "a use" }], provider_ids: providers.filter((p) => p.kind === kind || (kind === "launcher" && p.api === "messages")).map((p) => p.id), ...extra,
});
const catalog = (): ModelPurpose[] => {
  const main = category("assistant", "launcher", "headless", MAIN, { runtimes: ["cli", "agentsdk"], fields: [...fields, "runtime"] });
  const agents = category("agent.new", "launcher", "engine", { provider: "launcher.claude", model: "claude-model" }, { name: "Agents" });
  const kb = category("kb", "api", undefined, KB);
  const triage = category("inbox.triage", "api", undefined, { provider: "api.messages", model: "api-default" }, { name: "Information screening" });
  const use = (id: string, parent: ModelPurpose, extra: Partial<ModelPurpose>): ModelPurpose => ({
    ...parent, id, parent: parent.id, category_id: parent.id, uses: undefined, runtimes: undefined, fields, revision: 0, inherited_from: parent.name, ...extra,
  });
  const lark = use("lark.agent", agents, { name: "Lark /agent", launch: "headless", inherited_from: "Agents" });
  const supervise = use("assistant.supervise", main, { name: "Background supervisor", description: "Follows up the tasks Persona hands out." });
  const extension = use("extension.notes", kb, { name: "Notes extension", fields: ["provider", "model"], ...stored["extension.notes"] });
  const hubs = [main, agents, kb, triage, lark, supervise];
  if (meeting) {
    hubs.push(
      use("meeting.room", main, {
        name: name("room"), description: ROOM_DESCRIPTION, group: "Main chat's other uses", ...stored["meeting.room"], provider_ids: ["launcher.claude", "api.messages"],
      }),
      use("meeting.minutes", kb, { name: name("minutes"), description: "Writes a recorded meeting's minutes.", group: "Information processing's other uses", ...stored["meeting.minutes"] }),
    );
  }
  return [...hubs, extension];
};
/** What the hub holds for each meeting use (and the extension's purpose, once set): its own record and revision. */
let stored: Record<string, Pick<ModelPurpose, "override" | "effective" | "revision" | "inherited_from" | "fields">>;
const follows = (id: string, effective: ModelBinding, revision = 0): void => {
  stored[id] = { override: {}, effective, revision, inherited_from: name(id === "meeting.room" ? "assistant" : "kb"), fields };
};
/** What runs with a record of its own: a provider of its own with what it was given, else the category's with that. */
const setApart = (id: string, override: ModelBinding, revision: number, own: string[] = fields): void => {
  stored[id] = { override, effective: override.provider ? override : { ...categoryBinding(id), ...override }, revision, inherited_from: undefined, fields: own };
};
let meeting: boolean;
let conflict: boolean;
/** The hub lists Claude Code's models on its launcher; off, the editor has the plain field (a list it could not read). */
let lists: boolean;
/** The hub's catalog GET fails (the page's error), so the editor is reached only by its link. */
let catalogFails: boolean;
/** Held answers: the next catalog GET's (read when it was asked), and each save's. */
let catalogGate: Promise<void> | undefined;
let patchGate: Promise<void> | undefined;
let parentFailures: number;
let parentGated: boolean;
let parentReleases: (() => void)[];
let patches: { path: string; body: Record<string, unknown> }[];
const json = (value: unknown, status = 200) => new Response(JSON.stringify(value), { status });
/** The paths the page asked the hub for, in order. */
const requested = () => vi.mocked(fetch).mock.calls.map(([input]) => String(input).split("?")[0]);
const hub = async (input: RequestInfo | URL, init?: RequestInit): Promise<Response> => {
  const path = String(input).split("?")[0];
  const id = decodeURIComponent(path.split("/").pop() ?? "");
  if (init?.method === "PATCH") {
    const body = JSON.parse(String(init.body)) as Record<string, unknown>;
    patches.push({ path, body });
    if (patchGate) await patchGate;
    if (conflict || body.revision !== stored[id]?.revision) return json({ error: "revision conflict" }, 409);
    const override: ModelBinding = { ...stored[id].override };
    for (const field of ["provider", "model", "effort"] as const) {
      if (body[field] === null) delete override[field];
      else if (typeof body[field] === "string") override[field] = body[field];
    }
    if (Object.keys(override).length) setApart(id, override, stored[id].revision + 1);
    else follows(id, categoryBinding(id), stored[id].revision + 1);
    return json({ purpose: catalog().find((p) => p.id === id), providers });
  }
  if (path.endsWith("/model-config")) {
    if (catalogFails) return json({ error: "hub busy" }, 503);
    const body = { purposes: catalog(), providers };
    const gate = catalogGate;
    catalogGate = undefined;
    if (gate) await gate;
    return json(body);
  }
  const listed = catalog().find((p) => path.endsWith(`/purposes/${p.id}`));
  if (listed) {
    if (listed.id === "kb" && parentFailures > 0) {
      parentFailures--;
      return json({ error: "parent busy" }, 503);
    }
    const response = json({ purpose: listed, providers });
    if (listed.id === "kb" && parentGated) await new Promise<void>((resolve) => parentReleases.push(resolve));
    return response;
  }
  if (path.includes("/purposes/")) return json({ error: "not found" }, 404);
  if (path.endsWith("/models")) return lists && path.endsWith("/providers/launcher.claude/models") ? json(CLAUDE_MODELS) : json({ error: "unavailable" }, 404);
  throw new Error(`Unexpected request: ${path}`);
};
let host: HTMLDivElement;
let root: Root;
const flush = async () => { for (let i = 0; i < 8; i++) await act(async () => { await Promise.resolve(); }); };
const rows = () => [...host.querySelectorAll<HTMLElement>("[data-model-purpose]")];
const row = (id: string) => host.querySelector<HTMLElement>(`[data-model-purpose="${id}"]`)!;
const dialog = () => document.querySelector<HTMLElement>('[role="dialog"]')!;
const button = (label: string, scope: ParentNode = document) => [...scope.querySelectorAll("button")].find((b) => b.textContent === label);
const effortSelect = () => [...dialog().querySelectorAll<HTMLSelectElement>("select")]
  .find((node) => node.closest("label")?.querySelector("span")?.textContent === t("settings-modelconfiguration.effort"));
const modelField = () => dialog().querySelector<HTMLInputElement>(`input[aria-label="${t("settings-modelconfiguration.specific_model")}"]`);
/** The launcher list's picker (its trigger), and one of its rows. */
const picker = () => dialog().querySelector<HTMLButtonElement>("button[aria-expanded]");
const option = (name: string) => [...dialog().querySelectorAll<HTMLButtonElement>('[role="option"]')].find((b) => b.textContent?.startsWith(name));
/** The page, with the editor of `opened` open from the start (a link to it). */
async function render(opened: string | null = null) {
  function Harness() {
    const [open, onOpen] = useState<string | null>(opened);
    return <ModelConfiguration open={open} onOpen={onOpen} onProviders={() => {}} />;
  }
  await act(async () => root.render(<Harness />));
  await flush();
}
async function open(id: string) {
  await act(async () => row(id).querySelector("button")!.click());
  await flush();
}
async function choose(value: string) {
  await act(async () => {
    const select = dialog().querySelector("select")!;
    select.value = value;
    select.dispatchEvent(new Event("change", { bubbles: true }));
  });
  await flush();
}
function type(input: HTMLInputElement, value: string) {
  act(() => {
    Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(input, value);
    input.dispatchEvent(new Event("input", { bubbles: true }));
  });
}
async function useLanguage(next: string) {
  lang = next;
  await initI18n(next);
}
beforeEach(async () => {
  await useLanguage("en");
  meeting = true;
  conflict = false;
  lists = false;
  catalogFails = false;
  catalogGate = undefined;
  patchGate = undefined;
  parentFailures = 0;
  parentGated = false;
  parentReleases = [];
  patches = [];
  providers.length = 4;
  Object.assign(KB, { provider: "api.messages", model: "kb-model" });
  delete KB.effort;
  providers[3].chat_effort = true;
  providers[3].models = undefined;
  providers[3].model_aliases = undefined;
  stored = {};
  follows("meeting.room", MAIN);
  follows("meeting.minutes", KB);
  vi.stubGlobal("fetch", vi.fn(hub));
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

describe("the meeting uses' models (#7980)", () => {
  it("lists the room after Main chat and the background supervisor, the minutes after Information processing, each in its category's group", async () => {
    await render();
    expect(rows().map((r) => r.dataset.modelPurpose)).toEqual([
      "assistant", "assistant.supervise", "meeting.room", "lark.agent", "agent.new", "kb", "meeting.minutes", "inbox.triage", "extension.notes",
    ]);
    expect(row("meeting.room").closest("section")).toBe(row("assistant").closest("section"));
    expect(row("meeting.minutes").closest("section")).toBe(row("kb").closest("section"));
    expect(row("meeting.room").querySelector('[data-testid="purpose-description"]')?.textContent).toBe(ROOM_DESCRIPTION);
    expect(row("meeting.room").textContent).toContain("Background process · claude -p");
    expect(row("meeting.minutes").textContent).toContain("Direct API · Messages");
    // nothing asked by their ids: the catalog has them
    expect(requested().filter((p) => p.includes("/purposes/"))).toEqual([]);
  });

  it.each([
    ["en", "Follows Main chat", "Follows Information processing", "Separately configured", "Follow Main chat"],
    // the words the background supervisor's row has had (`follows`)
    ["zh-Hans", "跟随 主对话", "跟随 信息处理", "单独设置", "跟随 主对话"],
    ["ja", "メインチャットに従う", "情報処理に従う", "個別設定", "メインチャットに従う"],
  ])("says what each follows by the category's name, or that it is set apart, in %s", async (language, room, minutes, separate, reset) => {
    await useLanguage(language);
    await render();
    expect(row("meeting.room").textContent).toContain(room);
    expect(row("meeting.minutes").textContent).toContain(minutes);
    expect(row("meeting.room").textContent).not.toContain(separate);
    setApart("meeting.room", { model: "claude-small" }, 3); // a model of its own is enough
    setApart("meeting.minutes", { effort: "low" }, 2);
    await act(async () => host.querySelector<HTMLButtonElement>(`button[aria-label="${t("settings-modelconfiguration.refresh")}"]`)!.click());
    await flush();
    expect(row("meeting.room").textContent).toContain(separate);
    expect(row("meeting.room").textContent).not.toContain(room);
    expect(row("meeting.minutes").textContent).toContain(separate);
    await open("meeting.room");
    expect(dialog().textContent).toContain(separate);
    expect(button(reset, dialog())).toBeDefined();
    expect(dialog().querySelector('[data-testid="claude-code-only"]')?.textContent).toBe(t("settings-modelconfiguration.claude_code_only"));
  });

  it("the room's editor offers only the hub's providers, no runtime, and says it runs on Claude Code only", async () => {
    await render();
    await open("meeting.room");
    expect(dialog().textContent).toContain("In-meeting conversation");
    expect(dialog().textContent).toContain("Follows Main chat");
    const options = [...dialog().querySelector("select")!.options];
    expect(options.map((o) => o.value)).toEqual(["", "launcher.claude", "api.messages"]);
    expect(options[0].textContent).toBe("Follow Main chat");
    const only = dialog().querySelector<HTMLElement>('[data-testid="claude-code-only"]')!;
    expect(only.textContent).toBe("Runs on Claude Code only: pick a Claude Code launcher or a Messages API connection.");
    expect(dialog().querySelector("select")!.getAttribute("aria-describedby")).toBe(only.id);
    expect(dialog().textContent).not.toContain(t("settings-modelconfiguration.runtime"));
    expect(dialog().querySelector('[role="radiogroup"]')).toBeNull();
    expect(effortSelect()).toBeDefined();
    await choose("api.messages");
    type(modelField()!, "api-model");
    await act(async () => {
      effortSelect()!.value = "high";
      effortSelect()!.dispatchEvent(new Event("change", { bubbles: true }));
    });
    await act(async () => button("Save", dialog())!.click());
    await flush();
    expect(patches).toEqual([{ path: "/api/v1/model-config/purposes/meeting.room", body: { revision: 0, provider: "api.messages", model: "api-model", effort: "high" } }]);
    expect(row("meeting.room").textContent).toContain("Separately configured");
    // its category is left as it was
    expect(row("assistant").textContent).toContain("claude-model");
  });

  it("the minutes' editor shows effort only where the provider takes one, and no Claude Code line", async () => {
    await render();
    await open("meeting.minutes");
    expect(dialog().querySelector('[data-testid="claude-code-only"]')).toBeNull();
    expect([...dialog().querySelectorAll<HTMLOptionElement>("select option")][0].textContent).toBe("Follow Information processing");
    expect(effortSelect()).toBeDefined();
    await choose("api.chat");
    expect(effortSelect()).toBeDefined();
    expect(dialog().textContent).not.toContain("Test connection");
    await choose("api.messages");
    expect(effortSelect()).toBeDefined();
  });

  it("uses the selected Chat model's exact capabilities and saves only a supported effort", async () => {
    providers[3].models = [
      { id: "chat-exact", efforts: ["low"], all_efforts: ["none", "low"] },
      { id: "chat-none", efforts: [], no_effort: true },
    ];
    setApart("meeting.minutes", { provider: "api.chat", model: "chat-exact" }, 4);
    await render();
    await open("meeting.minutes");
    expect([...effortSelect()!.options].map((option) => option.value)).toEqual(["", "off", "low"]);
    expect(effortSelect()!.disabled).toBe(false);
    type(modelField()!, "chat-none");
    await flush();
    expect(effortSelect()!.disabled).toBe(true);
    expect([...effortSelect()!.options].map((option) => option.value)).toEqual([""]);
    type(modelField()!, "chat-exact");
    await flush();
    await act(async () => {
      effortSelect()!.value = "low";
      effortSelect()!.dispatchEvent(new Event("change", { bubbles: true }));
    });
    await act(async () => button("Save", dialog())!.click());
    await flush();
    expect(patches).toEqual([{ path: "/api/v1/model-config/purposes/meeting.minutes", body: { revision: 4, provider: "api.chat", model: "chat-exact", effort: "low" } }]);
  });

  it("uses the parent model and effort when an own record keeps the same Chat provider", async () => {
    Object.assign(KB, { provider: "api.chat", model: "parent-model", effort: "high" });
    providers[3].models = [
      { id: "parent-model", efforts: ["low"], all_efforts: ["none", "low"] },
      { id: "chat-default", efforts: ["high"], all_efforts: ["none", "high"] },
    ];
    stored["meeting.minutes"] = {
      override: { provider: "api.chat", effort: "low" },
      effective: { provider: "api.chat", model: "parent-model", effort: "low" },
      revision: 4, fields,
    };
    await render();
    await open("meeting.minutes");
    expect([...effortSelect()!.options].map((option) => option.value)).toEqual(["", "off", "low"]);
    await act(async () => {
      effortSelect()!.value = "";
      effortSelect()!.dispatchEvent(new Event("change", { bubbles: true }));
    });
    expect(effortSelect()!.options[0].textContent).toBe(t("settings-modelconfiguration.api_effort_inherit", { effort: effortTitle("high") }));
  });

  it("uses effective inheritance for a same-provider Chat record when the catalog is unavailable", async () => {
    catalogFails = true;
    Object.assign(KB, { provider: "api.chat", model: "parent-model", effort: "high" });
    providers[3].models = [
      { id: "parent-model", efforts: ["low"], all_efforts: ["none", "low"] },
      { id: "chat-default", efforts: ["high"], all_efforts: ["none", "high"] },
    ];
    stored["meeting.minutes"] = {
      override: { provider: "api.chat" },
      effective: { provider: "api.chat", model: "parent-model", effort: "high" },
      revision: 4, fields,
    };
    await render("meeting.minutes");
    expect([...effortSelect()!.options].map((option) => option.value)).toEqual(["", "off", "low"]);
    expect(effortSelect()!.options[0].textContent).toBe(t("settings-modelconfiguration.api_effort_inherit", { effort: effortTitle("high") }));
    await act(async () => {
      effortSelect()!.value = "low";
      effortSelect()!.dispatchEvent(new Event("change", { bubbles: true }));
    });
    await act(async () => button("Save", dialog())!.click());
    await flush();
    expect(patches).toEqual([{ path: "/api/v1/model-config/purposes/meeting.minutes", body: { revision: 4, provider: "api.chat", model: null, effort: "low" } }]);
  });

  it("uses the linked parent after clearing a model override without the catalog", async () => {
    catalogFails = true;
    Object.assign(KB, { provider: "api.chat", model: "parent-model", effort: "high" });
    providers[3].models = [
      { id: "parent-model", efforts: ["low"], all_efforts: ["none", "low"] },
      { id: "child-model", efforts: ["high"], all_efforts: ["none", "high"] },
      { id: "chat-default", efforts: ["high"], all_efforts: ["none", "high"] },
    ];
    setApart("meeting.minutes", { model: "child-model" }, 4);
    await render("meeting.minutes");
    expect(requested()).toContain("/api/v1/model-config/purposes/kb");
    type(modelField()!, "");
    await flush();
    expect([...effortSelect()!.options].map((option) => option.value)).toEqual(["", "off", "low"]);
    await act(async () => {
      effortSelect()!.value = "low";
      effortSelect()!.dispatchEvent(new Event("change", { bubbles: true }));
    });
    await act(async () => button("Save", dialog())!.click());
    await flush();
    expect(patches).toEqual([{ path: "/api/v1/model-config/purposes/meeting.minutes", body: { revision: 4, provider: null, model: null, effort: "low" } }]);
  });

  it("ignores an older retry after the newest linked parent loads", async () => {
    catalogFails = true;
    parentFailures = 1;
    parentGated = true;
    Object.assign(KB, { provider: "api.chat", model: "old-parent", effort: "high" });
    providers[3].models = [
      { id: "old-parent", efforts: ["low"], all_efforts: ["none", "low"] },
      { id: "new-parent", efforts: ["medium"], all_efforts: ["none", "medium"] },
      { id: "child-model", efforts: ["high"], all_efforts: ["none", "high"] },
      { id: "chat-default", efforts: ["high"], all_efforts: ["none", "high"] },
    ];
    setApart("meeting.minutes", { model: "child-model" }, 4);
    await render("meeting.minutes");
    expect(button("Retry", dialog())).toBeDefined();
    await act(async () => button("Retry", dialog())!.click());
    await flush();
    expect(parentReleases).toHaveLength(1);
    KB.model = "new-parent";
    await act(async () => button("Retry", dialog())!.click());
    await flush();
    expect(parentReleases).toHaveLength(2);
    await act(async () => parentReleases[1]());
    await flush();
    await act(async () => parentReleases[0]());
    await flush();
    type(modelField()!, "");
    await flush();
    expect([...effortSelect()!.options].map((option) => option.value)).toEqual(["", "off", "medium"]);
    await act(async () => {
      effortSelect()!.value = "medium";
      effortSelect()!.dispatchEvent(new Event("change", { bubbles: true }));
    });
    await act(async () => button("Save", dialog())!.click());
    await flush();
    expect(patches).toEqual([{ path: "/api/v1/model-config/purposes/meeting.minutes", body: { revision: 4, provider: null, model: null, effort: "medium" } }]);
  });

  it("keeps Chat effort when an older hub's explicit Chat binding switches to another Chat provider", async () => {
    delete providers[3].chat_effort;
    providers.push({ id: "api.chat-b", name: "Chat B", kind: "api", api: "chat", model: "chat-b-default", has_key: true });
    setApart("meeting.minutes", { provider: "api.chat", model: "chat-a", effort: "low" }, 4);
    await render();
    await open("meeting.minutes");
    await choose("api.chat-b");
    expect(effortSelect()).toBeDefined();
  });

  it("restores Messages effort from another provider's current-hub mark when the effective provider is missing", async () => {
    stored["meeting.minutes"] = {
      override: {}, effective: { provider: "api.missing" }, revision: 4, fields: ["provider", "model"],
    };
    await render();
    await open("meeting.minutes");
    await choose("api.messages");
    expect(effortSelect()).toBeDefined();
  });

  it("the minutes' editor hides and omits Chat effort after a Messages switch on an older hub", async () => {
    delete providers[3].chat_effort;
    setApart("meeting.minutes", { provider: "api.messages", model: "api-model" }, 4);
    await render();
    await open("meeting.minutes");
    expect(effortSelect()).toBeDefined();
    await choose("api.chat");
    expect(effortSelect()).toBeUndefined();
    type(modelField()!, "chat-model");
    await act(async () => button("Save", dialog())!.click());
    await flush();
    expect(patches).toEqual([{ path: "/api/v1/model-config/purposes/meeting.minutes", body: { revision: 4, provider: "api.chat", model: "chat-model" } }]);
  });

  it.each([
    ["meeting.room", "Follow Main chat", "Follows Main chat"],
    ["meeting.minutes", "Follow Information processing", "Follows Information processing"],
  ])("%s goes back to following with explicit nulls and its own revision", async (id, reset, following) => {
    // on a Chat Completions connection the minutes take no effort, and the reset still clears one
    setApart(id, { provider: id === "meeting.room" ? "launcher.claude" : "api.chat", model: "own-model" }, 5, id === "meeting.room" ? fields : ["provider", "model"]);
    await render();
    await open(id);
    // its reset is longer than "Inherit": a line of its own above Cancel and Save where the three do not fit, which
    // web/e2e/meeting-models.mjs measures at 320 and 360 px; here, that Cancel and Save move together
    const footer = dialog().querySelector<HTMLElement>("[data-editor-footer]")!;
    expect(button(reset, footer)!.parentElement).toBe(footer);
    expect(button("Cancel", footer)!.parentElement).toBe(button("Save", footer)!.parentElement);
    await act(async () => button(reset, dialog())!.click());
    await flush();
    expect(patches).toEqual([{ path: `/api/v1/model-config/purposes/${id}`, body: { revision: 5, provider: null, model: null, effort: null } }]);
    expect(stored[id].override).toEqual({});
    expect(row(id).textContent).toContain(following);
    expect(document.querySelector('[role="dialog"]')).toBeNull();
  });

  it("keeps the draft and blocks saving and the reset after a conflict, until reloaded", async () => {
    await render();
    await open("meeting.room");
    const model = () => modelField()!;
    type(model(), "draft-model");
    setApart("meeting.room", { model: "elsewhere" }, 1); // saved on another device meanwhile
    await act(async () => button("Save", dialog())!.click());
    await flush();
    expect(patches.map((p) => p.body.revision)).toEqual([0]);
    expect(model().value).toBe("draft-model");
    expect(dialog().textContent).toContain(t("settings-modelconfiguration.conflict"));
    expect(button("Save", dialog())!.disabled).toBe(true);
    expect(button("Follow Main chat", dialog())!.disabled).toBe(true);
    await act(async () => button(t("settings-modelconfiguration.reload_discard"), dialog())!.click());
    await flush();
    expect(model().value).toBe("elsewhere");
    await act(async () => button("Follow Main chat", dialog())!.click());
    await flush();
    expect(patches.map((p) => p.body)).toEqual([
      { revision: 0, provider: null, model: "draft-model", effort: null },
      { revision: 1, provider: null, model: null, effort: null },
    ]);
  });

  it("the room, set back to following, previews Main chat's launcher and its list, with Main chat's model as Default", async () => {
    lists = true;
    setApart("meeting.room", { provider: "api.messages", model: "api-model" }, 2);
    await render();
    await open("meeting.room");
    // on its own Messages connection: the plain field, and no launcher's list asked for
    expect(picker()).toBeNull();
    expect(modelField()!.value).toBe("api-model");
    expect(requested().filter((p) => p.endsWith("/models"))).toEqual([]);
    await choose("");
    // what it runs on once saved: Main chat's launcher, not the connection it is leaving
    expect(requested().filter((p) => p.endsWith("/models"))).toEqual(["/api/v1/model-config/providers/launcher.claude/models"]);
    expect(dialog().querySelector("[data-execution-preview]")!.textContent).toBe("Background process · claude -p");
    expect(picker()!.textContent).toContain("claude-model");
    expect(picker()!.textContent).not.toContain("launcher-default-model");
  });

  it("the room with a model of its own: the list's Default is Main chat's model, and picking it follows Main chat", async () => {
    lists = true;
    setApart("meeting.room", { model: "claude-small" }, 2);
    await render();
    await open("meeting.room");
    expect(picker()!.textContent).toContain("Claude small");
    await act(async () => picker()!.click());
    await flush();
    // not the launcher's own default: with nothing of its own it runs Main chat's model
    expect(option("Default")!.textContent).toBe("Defaultclaude-model");
    await act(async () => option("Default")!.click());
    await flush();
    expect(picker()!.textContent).toContain("claude-model");
    await act(async () => button("Save", dialog())!.click());
    await flush();
    expect(patches.map((p) => p.body)).toEqual([{ revision: 2, provider: null, model: null, effort: null }]);
    expect(row("meeting.room").textContent).toContain("Follows Main chat");
  });

  it("the minutes on an older hub's Chat Completions connection offer effort again once the draft runs on one that takes it", async () => {
    // an older hub lists no effort while they run on a Chat Completions connection
    delete providers[3].chat_effort;
    setApart("meeting.minutes", { provider: "api.chat", model: "chat-model" }, 4, ["provider", "model"]);
    await render();
    await open("meeting.minutes");
    expect(effortSelect()).toBeUndefined();
    expect(dialog().querySelector("[data-execution-preview]")!.textContent).toBe("Direct API · Chat");
    // following Information processing again: its Messages connection, which takes one
    await choose("");
    expect(dialog().querySelector("[data-execution-preview]")!.textContent).toBe("Direct API · Messages");
    expect(effortSelect()).toBeDefined();
    expect(modelField()!.placeholder).toBe("kb-model"); // its model, not the connection's default (api-default)
    await choose("api.chat");
    expect(effortSelect()).toBeUndefined();
    await choose("api.messages");
    await act(async () => {
      effortSelect()!.value = "high";
      effortSelect()!.dispatchEvent(new Event("change", { bubbles: true }));
    });
    await act(async () => button("Save", dialog())!.click());
    await flush();
    expect(patches.map((p) => p.body)).toEqual([{ revision: 4, provider: "api.messages", model: null, effort: "high" }]);
  });

  it("resets all three by the purpose's own data, also when the page's catalog did not load", async () => {
    catalogFails = true;
    setApart("meeting.minutes", { model: "own-model", effort: "low" }, 3, ["provider", "model"]);
    await render("meeting.minutes"); // opened by its link
    expect(host.querySelector('[role="alert"]')?.textContent).toContain("hub busy");
    // without the catalog the editor cannot name the category: the plain reset, which still clears the effort
    await act(async () => button(t("settings-modelconfiguration.restore"), dialog())!.click());
    await flush();
    expect(patches).toEqual([{ path: "/api/v1/model-config/purposes/meeting.minutes", body: { revision: 3, provider: null, model: null, effort: null } }]);
    expect(stored["meeting.minutes"].override).toEqual({});
  });

  it("an extension's purpose resets all three too", async () => {
    setApart("extension.notes", { effort: "low" }, 2, ["provider", "model"]);
    await render();
    await open("extension.notes");
    await act(async () => button(t("settings-modelconfiguration.restore"), dialog())!.click());
    await flush();
    expect(patches).toEqual([{ path: "/api/v1/model-config/purposes/extension.notes", body: { revision: 2, provider: null, model: null, effort: null } }]);
  });

  it("holds the launcher's list still while a save is on its way", async () => {
    lists = true;
    let release!: () => void;
    patchGate = new Promise((done) => { release = done; });
    await render();
    await open("meeting.room");
    expect(picker()!.parentElement!.hasAttribute("inert")).toBe(false);
    await act(async () => button("Save", dialog())!.click());
    await flush();
    expect(patches).toHaveLength(1);
    expect(picker()!.parentElement!.hasAttribute("inert")).toBe(true);
    release();
    await flush();
    expect(document.querySelector('[role="dialog"]')).toBeNull();
  });

  it("an older catalog answer does not put back what a save's reload replaced", async () => {
    let release!: () => void;
    catalogGate = new Promise((done) => { release = done; }); // the page's first GET answers late
    await render("meeting.room");
    expect(rows()).toEqual([]);
    type(modelField()!, "own-model");
    await act(async () => button("Save", dialog())!.click());
    await flush();
    expect(row("meeting.room").textContent).toContain("Separately configured");
    release(); // the GET from before the save, at last
    await flush();
    expect(row("meeting.room").textContent).toContain("Separately configured");
    expect(row("meeting.room").textContent).not.toContain("Follows Main chat");
  });

  it("leaves the background supervisor, Lark /agent and the extension's purpose as they were", async () => {
    await render();
    expect(row("assistant.supervise").textContent).toContain("Follows Main chat");
    expect(row("lark.agent").textContent).toContain(t("settings-modelconfiguration.lark_headless_inheritance"));
    expect(row("extension.notes").textContent).toContain("Follows Information processing");
    await open("assistant.supervise");
    expect(button(t("settings-modelconfiguration.restore"), dialog())).toBeDefined();
    expect(button("Follow Main chat", dialog())).toBeUndefined();
    expect([...dialog().querySelectorAll<HTMLOptionElement>("select option")][0].textContent).toBe(t("settings-modelconfiguration.follow_default"));
    expect(dialog().querySelector('[data-testid="claude-code-only"]')).toBeNull();
    expect(dialog().querySelector("[data-editor-footer]")).toBeNull();
  });

  it("against a hub before #7980: no rows, no requests by their ids, the rest as before", async () => {
    meeting = false;
    await render();
    expect(rows().map((r) => r.dataset.modelPurpose)).toEqual(["assistant", "assistant.supervise", "lark.agent", "agent.new", "kb", "inbox.triage", "extension.notes"]);
    expect(requested().filter((p) => p.includes("meeting."))).toEqual([]);
    expect(requested()).toEqual(["/api/v1/model-config"]);
    expect(host.querySelector('[role="alert"]')).toBeNull();
  });
});
