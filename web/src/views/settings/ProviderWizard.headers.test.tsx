// @vitest-environment jsdom
// #11075: provider request headers belong on Connect, before the checks that send them. These are mounted acceptance
// tests through the real form and HTTP client: add/edit, automatic/manual test and discovery, saved-name-only reads,
// replacement/removal, Chat Completions, and the runtime support or limitation said before a check.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi, type Mock } from "vitest";
vi.hoisted(() => {
  globalThis.ResizeObserver ??= class { observe() {} unobserve() {} disconnect() {} } as unknown as typeof ResizeObserver;
  window.matchMedia ??= ((media: string) => ({ matches: false, media, onchange: null, addEventListener() {}, removeEventListener() {}, addListener() {}, removeListener() {}, dispatchEvent: () => false })) as typeof window.matchMedia;
});
import { t } from "../../i18n";
import type { ProviderAPI, ProviderView } from "../../lib/providers";
import { resetReadiness } from "../../lib/readiness";
import { getState } from "../../lib/store";
import { ProviderWizard } from "./ProviderWizard";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

type Call = { method: string; path: string; body?: Record<string, unknown> };
type Pending = Call & { resolve: (value: unknown, status?: number) => void; reject: (error: Error) => void };
type Editor = ProviderView | "new" | null;
type Runtime = {
  id: string;
  label: string;
  kind: "launcher" | "api";
  agent?: string;
  api: ProviderAPI;
  hint: string;
};

const RUNTIMES: Runtime[] = [
  { id: "claude", label: "Claude Code", kind: "launcher", agent: "claude", api: "messages", hint: "headers_runtime_claude" },
  { id: "codex", label: "Codex", kind: "launcher", agent: "codex", api: "messages", hint: "headers_runtime_codex" },
  { id: "agy", label: "Antigravity", kind: "launcher", agent: "agy", api: "messages", hint: "headers_runtime_agy" },
  { id: "omp", label: "omp", kind: "launcher", agent: "omp", api: "messages", hint: "headers_runtime_omp" },
  { id: "messages", label: "Anthropic Messages", kind: "api", api: "messages", hint: "headers_runtime_direct" },
  { id: "responses", label: "OpenAI Responses", kind: "api", api: "responses", hint: "headers_runtime_direct" },
  { id: "chat", label: "Chat Completions", kind: "api", api: "chat", hint: "headers_runtime_direct" },
];
// Codex is supported through env_http_headers; Antigravity has no arbitrary-header launch channel.
const CONNECT_CASES = RUNTIMES.filter((r) => r.id === "claude" || r.id === "codex" || r.kind === "api");
const HEADER = "X-Custom-Header: example-value";
const REPLACEMENT = "X-Custom-Header: replacement-value\nX-Team: blue";
const AUTO = 700;

let calls: Call[];
let pending: Pending[];
let host: HTMLDivElement;
let root: Root;
let saved: Mock<() => void>;
const json = (value: unknown, status = 200) => new Response(JSON.stringify(value), { status, headers: { "Content-Type": "application/json" } });
const flush = async () => { for (let i = 0; i < 6; i++) await act(async () => { await Promise.resolve(); }); };
const text = () => document.body.textContent ?? "";
function button(key: string) {
  const found = [...document.body.querySelectorAll("button")].find((el) => el.textContent?.trim().startsWith(t(key)));
  expect(found, key).toBeDefined();
  return found!;
}
function click(key: string) { act(() => button(key).click()); }
function clickText(label: string) {
  const found = [...document.body.querySelectorAll("button")].find((el) => el.textContent?.trim() === label);
  expect(found, label).toBeDefined();
  act(() => found!.click());
}
function input(selector: string) {
  const found = document.body.querySelector<HTMLInputElement>(selector);
  expect(found, selector).not.toBeNull();
  return found!;
}
function type(el: HTMLInputElement | HTMLTextAreaElement, value: string) {
  const prototype = el instanceof HTMLTextAreaElement ? HTMLTextAreaElement.prototype : HTMLInputElement.prototype;
  act(() => {
    Object.getOwnPropertyDescriptor(prototype, "value")!.set!.call(el, value);
    el.dispatchEvent(new Event("input", { bubbles: true }));
  });
}
function headerLabel() {
  const found = [...document.body.querySelectorAll("label")].find((el) => el.textContent?.trim() === t("settings-providerwizard.headers"));
  expect(found, "Request headers on Connect").toBeDefined();
  return found!;
}
function headersBox() {
  const label = headerLabel();
  const found = document.getElementById(label.htmlFor);
  expect(found, "Request headers textarea").toBeInstanceOf(HTMLTextAreaElement);
  return found as HTMLTextAreaElement;
}
function headersText() {
  const label = headerLabel();
  return (label.closest("[data-provider-field]") ?? label.parentElement)?.textContent ?? "";
}
function typeHeaders(value: string) { type(headersBox(), value); }
const urlInput = () => input('input[inputmode="url"]');
const keyInput = () => input('input[type="password"]');
const onConnect = () => expect(document.querySelector('[aria-current="step"]')?.textContent).toContain(t("settings-providerwizard.step_connect"));
const probes = (kind: "test" | "discover") => pending.filter((r) => r.path.endsWith(`/launchers/${kind}`));
const saves = () => calls.filter((c) => (c.method === "POST" && c.path === "/api/v1/launchers") || c.method === "PUT");

function render(existing: Editor) {
  root.render(<ProviderWizard existing={existing} onClose={() => {}} onSaved={() => saved()} />);
}
async function draw(existing: Editor) {
  act(() => render(existing));
  await flush();
}
async function advance(ms: number) {
  await act(async () => { await vi.advanceTimersByTimeAsync(ms); });
  await flush();
}
async function resolve(request: Pending, value?: unknown) {
  const answer = value ?? (request.path.endsWith("/test")
    ? { ok: true, ms: 123, model: String(request.body?.model || "m1") }
    : { models: [{ id: "m1" }] });
  await act(async () => request.resolve(answer));
  await flush();
}
function leave(el: HTMLInputElement) { act(() => { el.focus(); el.blur(); }); }

function existingFor(runtime: Runtime, withHeaders = false): ProviderView {
  return {
    name: `${runtime.id}-gw`,
    kind: runtime.kind,
    ...(runtime.kind === "launcher" ? { agent: runtime.agent } : {}),
    api: runtime.api,
    base_url: "https://gw.example.com/v1",
    has_key: true,
    auth: runtime.kind === "api" && runtime.api !== "messages" ? "api_key" : "bearer",
    model: "m1",
    models: [{ id: "m1" }],
    ...(withHeaders ? { has_headers: true, header_names: ["X-Custom-Header", "X-Team"] } : {}),
  } as ProviderView;
}
async function choose(runtime: Runtime) {
  if (runtime.kind === "api") {
    click("settings-providerwizard.direct_api");
    if (runtime.api !== "messages") clickText(runtime.label);
  } else if (runtime.agent !== "claude") clickText(runtime.label);
}
async function startNew(runtime: Runtime, connect = true) {
  await draw("new");
  click("settings-providerwizard.custom");
  await choose(runtime);
  if (!connect) return;
  type(urlInput(), "https://gw.example.com/v1");
  leave(urlInput());
  type(keyInput(), "sk-example-123456");
}
async function saveEdit() {
  click("common.save");
  await flush();
  const call = saves().at(-1);
  expect(call?.method).toBe("PUT");
  return call!.body!;
}

beforeEach(() => {
  // Only timers are faked: promises, fetch and React scheduling keep running for real.
  vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
  calls = [];
  pending = [];
  saved = vi.fn();
  Object.assign(getState(), { features: ["builtin_presets", "launcher_auto_name", "launcher_auth_auto"], machines: [] });
  resetReadiness();
  vi.stubGlobal("fetch", vi.fn((url: RequestInfo | URL, init?: RequestInit) => {
    const call: Call = { method: init?.method ?? "GET", path: String(url).split("?")[0]!, body: init?.body ? JSON.parse(String(init.body)) : undefined };
    calls.push(call);
    if (/\/launchers\/(discover|test)$/.test(call.path)) {
      return new Promise<Response>((resolve, reject) => pending.push({ ...call, resolve: (value, status) => resolve(json(value, status)), reject }));
    }
    if (call.path.endsWith("/launchers/presets")) return Promise.resolve(json({ presets: [] }));
    if (call.path.endsWith("/assistant/readiness")) return Promise.resolve(json({ error: "not found" }, 404));
    if (call.path === "/api/v1/launchers" && call.method === "POST") return Promise.resolve(json({ launcher: { name: "new-gw" } }));
    if (call.method === "PUT") return Promise.resolve(json({ launcher: { name: call.path.split("/").at(-1) } }));
    return Promise.resolve(json({}));
  }));
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});
afterEach(() => {
  act(() => root.unmount());
  host.remove();
  vi.unstubAllGlobals();
  vi.useRealTimers();
});

describe("request headers on Connect (#11075)", () => {
  it.each(CONNECT_CASES)("add $label: headers precede and are sent by automatic and manual checks and Save", async (runtime) => {
    await startNew(runtime);
    onConnect();
    typeHeaders(HEADER);

    // The editor was available before the automatic model discovery and its follow-up inference check.
    await advance(AUTO);
    expect(probes("discover")).toHaveLength(1);
    expect(probes("discover")[0]!.body).toMatchObject({ headers: HEADER, api: runtime.api });
    await resolve(probes("discover")[0]!);
    expect(probes("test")).toHaveLength(1);
    expect(probes("test")[0]!.body).toMatchObject({ headers: HEADER, model: runtime.id === "codex" ? "gpt-5.4" : "m1" });
    await resolve(probes("test")[0]!);

    // The same draft, not a stale pre-header connection, is used by both manual operations too.
    click("settings-providerwizard.test");
    click("settings-providerwizard.discover");
    expect(probes("test")[1]!.body).toMatchObject({ headers: HEADER });
    expect(probes("discover")[1]!.body).toMatchObject({ headers: HEADER });
    await resolve(probes("test")[1]!);
    await resolve(probes("discover")[1]!);

    click("settings-providerwizard.next");
    click("common.save");
    await flush();
    expect(saved).toHaveBeenCalledOnce();
    expect(saves().at(-1)?.body).toMatchObject({ kind: runtime.kind, api: runtime.api, headers: HEADER });
  });

  it.each(CONNECT_CASES)("edit $label: replacement is used by manual test, discovery and Save", async (runtime) => {
    await draw(existingFor(runtime, true));
    await advance(AUTO * 3);
    expect(pending).toHaveLength(0);
    onConnect();
    expect(headersBox().value).toBe("");
    expect(headersText()).toContain("X-Custom-Header");
    expect(headersText()).toContain("X-Team");

    typeHeaders(REPLACEMENT);
    click("settings-providerwizard.test");
    click("settings-providerwizard.discover");
    expect(probes("test")[0]!.body).toMatchObject({ name: `${runtime.id}-gw`, headers: REPLACEMENT });
    expect(probes("discover")[0]!.body).toMatchObject({ name: `${runtime.id}-gw`, headers: REPLACEMENT });
    await resolve(probes("test")[0]!);
    await resolve(probes("discover")[0]!);
    expect(await saveEdit()).toMatchObject({ headers: REPLACEMENT });
  });

  it.each(CONNECT_CASES)("edit $label: removal is explicit and reaches manual test, discovery and Save", async (runtime) => {
    await draw(existingFor(runtime, true));
    onConnect();
    click("settings-providerwizard.headers_clear");
    expect(headersText()).toContain(t("settings-providerwizard.headers_cleared"));
    expect(headersBox().value).toBe("");

    click("settings-providerwizard.test");
    click("settings-providerwizard.discover");
    expect(probes("test")[0]!.body).toHaveProperty("headers", "");
    expect(probes("discover")[0]!.body).toHaveProperty("headers", "");
    await resolve(probes("test")[0]!);
    await resolve(probes("discover")[0]!);
    expect(await saveEdit()).toHaveProperty("headers", "");
  });

  it("a reopened editor shows configured names but never saved values, and an unchanged Save keeps them", async () => {
    // Be defensive even if an older or buggy hub includes values: the reopened form must not put them in the DOM.
    const read = {
      ...existingFor(RUNTIMES[0]!, true),
      headers: "X-Custom-Header: saved-header-secret\nX-Team: other-saved-secret",
    } as unknown as ProviderView;
    await draw(read);
    onConnect();
    expect(headersBox().value).toBe("");
    expect(headersText()).toContain("X-Custom-Header");
    expect(headersText()).toContain("X-Team");
    expect(text()).not.toContain("saved-header-secret");
    expect(text()).not.toContain("other-saved-secret");
    expect(await saveEdit()).not.toHaveProperty("headers");
  });

  it("a saved provider automatically checks replacement headers, and a later edit retires earlier results", async () => {
    await draw(existingFor(RUNTIMES[0]!, true));
    typeHeaders("X-Custom-Header: first-value");
    click("settings-providerwizard.test");
    click("settings-providerwizard.discover");
    expect(probes("test")[0]!.body).toHaveProperty("headers", "X-Custom-Header: first-value");
    expect(probes("discover")[0]!.body).toHaveProperty("headers", "X-Custom-Header: first-value");

    typeHeaders("X-Custom-Header: current-value");
    await resolve(probes("test")[0]!);
    await resolve(probes("discover")[0]!);
    expect(text()).not.toContain(t("lib-providers.test_ok", { ms: 123 }));
    expect(text()).not.toContain(t("settings-providerwizard.discover_found", { count: 1 }));

    await advance(AUTO);
    expect(probes("discover")).toHaveLength(2);
    expect(probes("discover")[1]!.body).toHaveProperty("headers", "X-Custom-Header: current-value");
    await resolve(probes("discover")[1]!);
    expect(probes("test")).toHaveLength(2);
    expect(probes("test")[1]!.body).toHaveProperty("headers", "X-Custom-Header: current-value");
    await resolve(probes("test")[1]!);
    expect(text()).toContain(t("lib-providers.test_ok", { ms: 123 }));
  });

  it("rejects a reserved header before checks or Next can send it", async () => {
    await startNew(RUNTIMES[0]!);
    typeHeaders("Authorization: protocol-managed");
    expect(headersBox().getAttribute("aria-invalid")).toBe("true");
    expect(button("settings-providerwizard.test").disabled).toBe(true);
    expect(button("settings-providerwizard.discover").disabled).toBe(true);
    click("settings-providerwizard.next");
    onConnect();
    await advance(AUTO * 3);
    expect(pending).toHaveLength(0);
    expect(saves()).toHaveLength(0);
  });
});

describe("runtime header support is explained before checks (#11075)", () => {
  it.each(RUNTIMES)("add and edit $label show their support or limitation on Connect", async (runtime) => {
    await startNew(runtime, false);
    onConnect();
    expect(headersText()).toContain(t(`settings-providerwizard.${runtime.hint}`));
    expect(pending).toHaveLength(0);

    await draw(null);
    await draw(existingFor(runtime));
    onConnect();
    expect(headersText()).toContain(t(`settings-providerwizard.${runtime.hint}`));
    await advance(AUTO * 3);
    expect(pending).toHaveLength(0);
  });

  it.each(RUNTIMES.filter((r) => r.id === "agy"))("$label cannot report a misleading successful header-dependent model check", async (runtime) => {
    await startNew(runtime);
    typeHeaders(HEADER);
    expect(headersText()).toContain(t(`settings-providerwizard.${runtime.hint}`));
    expect(button("settings-providerwizard.test").disabled).toBe(true);
    await advance(AUTO * 3);
    expect(pending).toHaveLength(0);
  });

  it("Antigravity keeps saved names and the explicit removal action visible beside its limitation", async () => {
    const runtime = RUNTIMES.find((r) => r.id === "agy")!;
    await draw(existingFor(runtime, true));
    expect(headersText()).toContain(t("settings-providerwizard.headers_runtime_agy"));
    expect(headersText()).toContain("X-Custom-Header");
    expect(headersText()).toContain("X-Team");

    click("settings-providerwizard.headers_clear");
    expect(headersText()).toContain(t("settings-providerwizard.headers_cleared"));
    click("settings-providerwizard.test");
    click("settings-providerwizard.discover");
    expect(probes("test")[0]!.body).toHaveProperty("headers", "");
    expect(probes("discover")[0]!.body).toHaveProperty("headers", "");
    await resolve(probes("test")[0]!);
    await resolve(probes("discover")[0]!);
    expect(await saveEdit()).toHaveProperty("headers", "");
  });
});
