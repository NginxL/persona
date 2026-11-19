// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, expect, it, vi } from "vitest";

vi.hoisted(() => { window.matchMedia ??= ((query: string) => ({ matches: false, media: query, onchange: null, addEventListener() {}, removeEventListener() {} })) as typeof window.matchMedia; });
import { initI18n } from "../../i18n";
import { credentialOwner, handleUnauthorized, setToken } from "../../lib/api";
import { Toasts } from "../../ui/atoms";
import { Repositories } from "./Repositories";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
type Call = { path: string; init: RequestInit; answered?: boolean; answer: (body: unknown, status?: number) => void };
let root: Root, host: HTMLDivElement, calls: Call[];
const identity = { provider: "gitlab", instance_url: "https://gitlab.com", path: "team/sub/sample", canonical_url: "https://gitlab.com/team/sub/sample" };
const binding = { ...identity, id: "repo_sample", name: "Sample", aliases: ["demo"], purpose: "Public-safe sample", verified_at: "2026-11-12T00:00:00Z" };
// An extension's source on a placeholder host (docs/extensions/repository-providers.md), and one that is switched off.
const LIST = "/assistant/repositories", PROVIDERS = "/assistant/repositories/providers", PREVIEW = "/assistant/repositories/preview";
const builtins = [
  { id: "github", name: "GitHub", builtin: true, hosts: ["github.com"], state: "available" },
  { id: "gitlab", name: "GitLab", builtin: true, hosts: ["gitlab.com"], state: "available" },
];
const acme = { id: "ext:acme-code/acme", name: "Acme Code", builtin: false, hosts: ["code.acme.test"], state: "available", link_kinds: [{ kind: "pull_request", label: "MR" }] };
const offSource = { id: "ext:other-code/other", name: "Other Code", builtin: false, hosts: ["code.other.test"], state: "off" };
const acmeIdentity = { provider: acme.id, instance_url: "https://code.acme.test", path: "acme-code/acme", canonical_url: "https://code.acme.test/acme-code/acme" };
const acmeBinding = { ...acmeIdentity, id: "repo_acme", name: "Acme app", provider_name: "Acme Code", verified_at: "2026-11-12T00:00:00Z" };
const text = () => document.body.textContent ?? "";
const dialogText = () => document.querySelector('[role="dialog"]')?.textContent ?? "";
const button = (label: string) => [...document.querySelectorAll<HTMLButtonElement>("button")].find((b) =>
  (b.textContent?.trim() === label || (["Edit", "Unbind"].includes(label) && b.textContent?.startsWith(`${label} `))) && !b.closest("[data-closing]"))!;
const click = async (label: string) => act(async () => { expect(button(label), label).toBeTruthy(); button(label).click(); });
const answer = async (call: Call, body: unknown, status = 200) => { call.answered = true; await act(async () => call.answer(body, status)); };
/** The newest unanswered request to a path (and method): the list and its sources are read in parallel. */
const pending = (path: string, method = "GET") => [...calls].reverse().find((c) => c.path === path && (c.init.method ?? "GET") === method && !c.answered)!;
const input = async (label: string, value: string) => act(async () => {
  const field = [...document.querySelectorAll<HTMLInputElement>("input")].find((node) => node.labels?.[0]?.textContent === label)!;
  Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(field, value);
  field.dispatchEvent(new Event("input", { bubbles: true }));
});
const choose = async (value: string) => act(async () => {
  const select = document.querySelector<HTMLSelectElement>('[role="dialog"] select')!;
  Object.getOwnPropertyDescriptor(HTMLSelectElement.prototype, "value")!.set!.call(select, value);
  select.dispatchEvent(new Event("change", { bubbles: true }));
});
const render = async (manager = "assistant") => act(async () => root.render(<><Repositories key={manager} manager={manager} owner={credentialOwner()} /><Toasts /></>));
/** Opens the list; an older manager's 404 for the sources (the default) means GitHub and GitLab only. */
const open = async (rows: unknown[] = [], providers: { body: unknown; status?: number } = { body: { code: "repository_not_found", error: "not found" }, status: 404 }) => {
  await render(); await click("Manage repositories");
  await answer(pending(LIST), { manager: "assistant", repositories: rows });
  await answer(pending(PROVIDERS), providers.body, providers.status);
};
const withSources = (...extra: unknown[]) => ({ body: { manager: "assistant", providers: [...builtins, ...extra] } });
const writes = () => calls.filter((c) => c.path !== PROVIDERS && (c.init.method ?? "GET") !== "GET");

beforeEach(async () => {
  await initI18n("en"); setToken("synthetic-repository-session"); calls = [];
  host = document.createElement("div"); document.body.append(host); root = createRoot(host);
  vi.stubGlobal("fetch", vi.fn((url: RequestInfo | URL, init: RequestInit = {}) => new Promise<Response>((resolve, reject) => {
    init.signal?.addEventListener("abort", () => reject(new DOMException("Aborted", "AbortError")), { once: true });
    calls.push({
      path: String(url).split("/api/v1")[1], init,
      answer: (body, status = 200) => resolve(new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } })),
    });
  })));
});
afterEach(async () => {
  act(() => { document.querySelectorAll<HTMLButtonElement>('[aria-live="polite"] button[aria-label]').forEach((b) => b.click()); });
  act(() => root.unmount()); host.remove(); document.body.innerHTML = ""; handleUnauthorized(() => {}); vi.useRealTimers(); vi.unstubAllGlobals(); setToken(""); await initI18n("zh-Hans");
});

it("loads only on demand and previews locally before existence-check/save", async () => {
  await render(); expect(calls).toHaveLength(0);
  await click("Manage repositories"); expect(text()).toContain("Loading");
  // The list first, its sources beside it.
  expect(calls.map((c) => c.path)).toEqual([LIST, PROVIDERS]);
  await answer(pending(LIST), { manager: "assistant", repositories: [] });
  expect(text()).toContain("No repositories bound");
  await answer(pending(PROVIDERS), { manager: "assistant", providers: builtins });
  await click("Bind repository");
  await input("Repository URL", "git@" + "gitlab.com:team/sub/sample.git");
  expect(button("Check existence and save").disabled).toBe(false);
  expect(calls).toHaveLength(2);
  expect(text()).toContain(identity.path); expect(text()).toContain("Parsed locally; existence not yet checked");
  await click("Check existence and save");
  expect(text()).toContain("Checking repository existence");
  const save = pending(LIST, "POST");
  expect(new Headers(save.init.headers).get("X-Persona-Manager")).toBe("assistant");
  await answer(save, { manager: "assistant", repository: binding });
  expect(text()).toContain("Repository saved"); expect(text()).toContain(binding.purpose);
});

it("immediately replaces local identity and clears invalid input without preview requests", async () => {
  await open(); await click("Bind repository");
  await input("Repository URL", "https://github.com/example/project/pull/5581?diff=split#review");
  expect(text()).toContain("https://github.com/example/project");
  expect(button("Check existence and save").disabled).toBe(false);
  await input("Repository URL", "https://github.com/example/other");
  expect(document.querySelector('[role="dialog"]')!.textContent).not.toContain("example/project");
  await input("Repository URL", "https://token@" + "github.com/example/other");
  expect(button("Check existence and save").disabled).toBe(true);
  expect(document.querySelector('[data-repository-preview]')).toBeNull();
  expect(calls.map((c) => c.path)).toEqual([LIST, PROVIDERS]);
});

it("submits the canonical preview and leaves a failed check unsaved with its draft", async () => {
  await open(); await click("Bind repository");
  await input("Repository URL", "git@" + "github.com:Example/Project.git");
  await click("Check existence and save");
  const save = pending(LIST, "POST");
  expect(JSON.parse(save.init.body as string)).toMatchObject({url:"https://github.com/example/project",provider:"github"});
  await answer(save, { code: "repository_timeout", error: "Repository check timed out; nothing was saved." }, 422);
  expect(text()).toContain("Repository check timed out; nothing was saved.");
  expect(document.querySelector('[role="dialog"][aria-modal="true"]')).toBeTruthy();
  expect(document.querySelector<HTMLInputElement>('input')!.value).toBe("git@" + "github.com:Example/Project.git");
  expect(text()).toContain("No repositories bound");
});

it("edits descriptive metadata and restores an unbound record with its single-use receipt", async () => {
  await open([binding]); await click("Edit"); await input("Name (optional)", "Renamed");
  await click("Save changes");
  const save = pending(`${LIST}/repo_sample`, "PUT");
  expect(JSON.parse(save.init.body as string).url).toBe(identity.canonical_url);
  await answer(save, { manager: "assistant", repository: { ...binding, name: "Renamed" } });
  await click("Unbind"); expect(text()).toContain("does not delete or change the remote repository");
  await click("Unbind repository");
  const removal = pending(`${LIST}/repo_sample`, "DELETE");
  await answer(removal, { manager: "assistant", receipt: "synthetic-undo" });
  expect(text()).toContain("No repositories bound");
  await click("Undo");
  const restore = pending(`${LIST}/repo_sample/restore`, "POST");
  expect(JSON.parse(restore.init.body as string)).toEqual({ receipt: "synthetic-undo" });
  await answer(restore, { manager: "assistant", repository: binding });
  expect(text()).toContain(binding.purpose);
  expect(writes().map((c) => c.init.method)).toEqual(["PUT", "DELETE", "POST"]);
});

it("fences late reads and 401s when the manager changes", async () => {
  let unauthorized = 0; handleUnauthorized(() => { unauthorized++; });
  await render(); await click("Manage repositories");
  const [oldList, oldSources] = [pending(LIST), pending(PROVIDERS)];
  await render("work"); await click("Manage repositories");
  const list = pending(LIST);
  await answer(list, { manager: "work", repositories: [] });
  await answer(oldList, {}, 401);
  await answer(oldSources, { manager: "assistant", providers: [...builtins, acme] });
  expect(unauthorized).toBe(0); expect(text()).not.toContain(binding.purpose);
  expect(document.querySelector("[data-repository-sources]")).toBeNull();
  expect(new Headers(list.init.headers).get("X-Persona-Manager")).toBe("work");
  expect(new Headers(pending(PROVIDERS).init.headers).get("X-Persona-Manager")).toBe("work");
});

it("rejects an out-of-scope or malformed success instead of showing an empty state", async () => {
  await render(); await click("Manage repositories");
  await answer(pending(LIST), { manager: "work", repositories: [binding] });
  expect(text()).toContain("Could not load repositories"); expect(text()).not.toContain("No repositories bound");
});

it("bounds a stalled save and ignores its late response after a new draft", async () => {
  await open(); await click("Bind repository"); await input("Repository URL", identity.canonical_url);
  const savedNotices = text().split("Repository saved").length;
  vi.useFakeTimers();
  try {
    await click("Check existence and save");
    const save = pending(LIST, "POST");
    await act(async () => { await vi.advanceTimersByTimeAsync(13_000); });
    expect(text()).toContain("The change is unconfirmed. Refresh repositories before trying again.");
    expect(document.querySelector<HTMLInputElement>("input")!.value).toBe(identity.canonical_url);
    expect(save.init.signal?.aborted).toBe(true);
    await click("Cancel");
    await act(async () => { await vi.advanceTimersByTimeAsync(500); });
    await click("Bind repository"); await input("Repository URL", "https://github.com/example/new");
    await answer(save, {manager:"assistant",repository:binding});
    expect(document.querySelector<HTMLInputElement>("input")!.value).toBe("https://github.com/example/new");
    expect(text().split("Repository saved").length).toBe(savedNotices);
  } finally { vi.useRealTimers(); }
});

it("does not accept a late save for a previous manager", async () => {
  await open(); await click("Bind repository"); await input("Repository URL", identity.canonical_url);
  const savedNotices = text().split("Repository saved").length;
  await click("Check existence and save");
  const save = pending(LIST, "POST");
  await render("work");
  await answer(save, {manager:"assistant",repository:binding});
  expect(text().split("Repository saved").length).toBe(savedNotices);
  expect(text()).not.toContain(binding.purpose);
});

it("requires the saved identity to match the canonical preview", async () => {
  await open(); await click("Bind repository"); await input("Repository URL", identity.canonical_url);
  await click("Check existence and save");
  await answer(pending(LIST, "POST"), { manager: "assistant", repository: { ...binding, path: "team/other" } });
  expect(text()).toContain("The change is unconfirmed. Refresh repositories before trying again.");
  expect(document.querySelector('[role="dialog"][aria-modal="true"]')).toBeTruthy();
  expect(document.querySelector('[data-repository]')).toBeNull();
});

it("keeps today's screen with only GitHub and GitLab, from a providers list, a 404 or a failure", async () => {
  for (const providers of [withSources(), { body: { code: "repository_not_found", error: "not found" }, status: 404 }, { body: { error: "unavailable" }, status: 503 }]) {
    await open([binding], providers);
    expect(document.querySelector("[data-repository-sources]")).toBeNull();
    expect(document.querySelector("[data-repository-source-name]")).toBeNull();
    await click("Bind repository");
    expect([...document.querySelectorAll('[role="dialog"] option')].map((o) => o.textContent)).toEqual(["Detect from public host", "GitHub / GitHub Enterprise", "GitLab"]);
    await input("Repository URL", "https://code.acme.test/acme-code/acme");
    expect(dialogText()).toContain("Use a GitHub or GitLab HTTP(S) or git SSH link without credentials.");
    await click("Cancel");
    act(() => root.unmount()); root = createRoot(host); calls = [];
  }
});

it("lists the sources only with an extension provider, naming each and saying which is unavailable", async () => {
  await open([binding], withSources(acme, offSource));
  const sources = document.querySelector("[data-repository-sources]")!;
  expect(sources.textContent).toContain("Sources");
  expect([...sources.querySelectorAll("[data-repository-source]")].map((row) => row.getAttribute("data-repository-source"))).toEqual(["github", "gitlab", acme.id, offSource.id]);
  expect(sources.querySelector('[data-repository-source="github"]')!.textContent).toContain("Built in");
  expect(sources.querySelector(`[data-repository-source="${acme.id}"]`)!.textContent).toContain("code.acme.test");
  expect(sources.querySelector(`[data-repository-source="${acme.id}"]`)!.textContent).not.toContain("Unavailable");
  expect(sources.querySelector(`[data-repository-source="${offSource.id}"]`)!.textContent).toContain("Unavailable");
  expect(sources.querySelector(`[data-repository-source="${acme.id}"] [data-repository-mark="extension"]`)!.getAttribute("aria-hidden")).toBe("true");
  // Never a raw provider id on the screen.
  expect(text()).not.toContain("ext:");
  await click("Bind repository");
  const options = [...document.querySelectorAll<HTMLOptionElement>('[role="dialog"] option')];
  expect(options.map((o) => o.textContent)).toEqual(["Detect from public host", "GitHub / GitHub Enterprise", "GitLab", "Acme Code", "Other Code (unavailable)"]);
  expect(options.map((o) => o.disabled)).toEqual([false, false, false, false, true]);
});

it("asks an extension's source to read its address, detected by host, and saves what it read", async () => {
  await open([], withSources(acme));
  await click("Bind repository");
  vi.useFakeTimers();
  await input("Repository URL", "git@" + "code.acme.test:acme-code/acme.git");
  // The reading shows at once; nothing is sent until typing pauses, and Save waits for the identity.
  expect(dialogText()).toContain("Reading the address with Acme Code…");
  expect(calls.some((c) => c.path === PREVIEW)).toBe(false);
  expect(button("Check existence and save").disabled).toBe(true);
  await input("Repository URL", "https://code.acme.test/acme-code/acme/reviews/3");
  await act(async () => { await vi.advanceTimersByTimeAsync(400); });
  vi.useRealTimers();
  const previews = calls.filter((c) => c.path === PREVIEW);
  expect(previews).toHaveLength(1);
  expect(JSON.parse(previews[0].init.body as string)).toEqual({ url: "https://code.acme.test/acme-code/acme/reviews/3", provider: acme.id });
  expect(new Headers(previews[0].init.headers).get("X-Persona-Manager")).toBe("assistant");
  await answer(previews[0], { manager: "assistant", identity: acmeIdentity, provider_name: "Acme Code" });
  const preview = document.querySelector("[data-repository-preview]")!;
  expect(preview.textContent).toContain("Acme Code · https://code.acme.test");
  expect(preview.textContent).toContain(acmeIdentity.canonical_url);
  expect(preview.textContent).toContain("Read by Acme Code; existence not yet checked");
  expect(preview.textContent).not.toContain("ext:");
  expect(dialogText()).toContain("Acme Code checks that the repository exists, with its own sign-in.");
  await click("Check existence and save");
  const save = pending(LIST, "POST");
  expect(JSON.parse(save.init.body as string)).toEqual({ url: acmeIdentity.canonical_url, provider: acme.id, name: "", aliases: [], purpose: "" });
  await answer(save, { manager: "assistant", repository: acmeBinding });
  expect(document.querySelector('[data-repository="repo_acme"] [data-repository-source-name]')!.textContent).toBe("Acme Code");
});

it("shows the source's refusal and retires a read for an address that changed", async () => {
  await open([], withSources(acme));
  await click("Bind repository"); await choose(acme.id);
  vi.useFakeTimers();
  try {
    await input("Repository URL", "https://code.acme.test/acme-code/old");
    await act(async () => { await vi.advanceTimersByTimeAsync(400); });
    const first = pending(PREVIEW, "POST");
    await input("Repository URL", "https://code.acme.test/acme-code/missing");
    expect(first.init.signal?.aborted).toBe(true);
    await act(async () => { await vi.advanceTimersByTimeAsync(400); });
    await answer(pending(PREVIEW, "POST"), { code: "repository_provider_not_found", error: "Acme Code can't find this repository." }, 422);
    expect(dialogText()).toContain("Acme Code can't find this repository.");
    expect(button("Check existence and save").disabled).toBe(true);
    // An address on another host is never sent to the source picked.
    await input("Repository URL", "https://code.elsewhere.test/acme-code/app");
    expect(dialogText()).toContain("Use an address on code.acme.test for Acme Code, without credentials.");
    await act(async () => { await vi.advanceTimersByTimeAsync(1_000); });
    expect(calls.filter((c) => c.path === PREVIEW)).toHaveLength(2);
  } finally { vi.useRealTimers(); }
});

it("edits a saved extension repository's details with no preview, even with its source unavailable", async () => {
  await open([acmeBinding], withSources({ ...acme, state: "off" }));
  const row = document.querySelector('[data-repository="repo_acme"]')!;
  expect(row.querySelector("[data-repository-source-name]")!.textContent).toContain("Acme Code");
  expect(row.querySelector("[data-repository-source-name]")!.textContent).toContain("Unavailable");
  expect(row.querySelector("[data-repository-note]")!.textContent).toContain("Acme Code is unavailable. The repository stays saved, and its name, aliases and purpose can still be edited; checking it or changing its address needs Acme Code.");
  await click("Edit");
  const select = document.querySelector<HTMLSelectElement>('[role="dialog"] select')!;
  expect(select.value).toBe(acme.id);
  // Its own source stays selectable for it, though it is off.
  expect(select.selectedOptions[0].disabled).toBe(false);
  expect(select.selectedOptions[0].textContent).toBe("Acme Code (unavailable)");
  expect(document.querySelector("[data-repository-preview]")!.textContent).toContain("Only a changed repository identity is checked again.");
  await input("Name (optional)", "Renamed");
  await click("Save changes");
  expect(text()).toContain("Saving repository details");
  const save = pending(`${LIST}/repo_acme`, "PUT");
  expect(JSON.parse(save.init.body as string)).toMatchObject({ url: acmeIdentity.canonical_url, provider: acme.id, name: "Renamed" });
  expect(calls.some((c) => c.path === PREVIEW)).toBe(false);
  await answer(save, { manager: "assistant", repository: { ...acmeBinding, name: "Renamed" } });
  expect(document.querySelector('[data-repository="repo_acme"]')!.textContent).toContain("Renamed");
  expect(document.querySelector('[data-repository="repo_acme"] [data-repository-note]')).toBeTruthy();
});

it("keeps a repository whose source was uninstalled readable by its saved name, and its address unchangeable", async () => {
  await open([acmeBinding], withSources());
  expect(document.querySelector("[data-repository-sources]")).toBeTruthy();
  const row = document.querySelector('[data-repository="repo_acme"]')!;
  expect(row.textContent).toContain(acmeIdentity.canonical_url);
  expect(row.querySelector("[data-repository-source-name]")!.textContent).toContain("Acme Code");
  expect(row.querySelector("[data-repository-source-name]")!.textContent).toContain("Unavailable");
  await click("Edit");
  expect(document.querySelector<HTMLSelectElement>('[role="dialog"] select')!.selectedOptions[0].textContent).toBe("Acme Code (unavailable)");
  await input("Repository URL", "https://code.acme.test/acme-code/other");
  expect(dialogText()).toContain("Acme Code is unavailable, so it can't read this address now.");
  expect(button("Save changes").disabled).toBe(true);
  expect(calls.some((c) => c.path === PREVIEW)).toBe(false);
});

it("claims no state for an extension row while its sources could not be read, and lets the hub answer for its source", async () => {
  await open([acmeBinding], { body: { error: "unavailable" }, status: 503 });
  const row = document.querySelector('[data-repository="repo_acme"]')!;
  expect(row.querySelector("[data-repository-source-name]")!.textContent).toBe("Acme Code");
  expect(row.querySelector("[data-repository-note]")).toBeNull();
  expect(document.querySelector("[data-repository-sources]")).toBeNull();
  // Its address can still change: the page doesn't call the source unavailable, it asks the hub, whose answer decides.
  await click("Edit");
  expect(document.querySelector<HTMLSelectElement>('[role="dialog"] select')!.selectedOptions[0].textContent).toBe("Acme Code");
  vi.useFakeTimers();
  try {
    await input("Repository URL", "https://code.acme.test/acme-code/other");
    expect(dialogText()).toContain("Reading the address with Acme Code…");
    expect(dialogText()).not.toContain("unavailable");
    await act(async () => { await vi.advanceTimersByTimeAsync(400); });
  } finally { vi.useRealTimers(); }
  const preview = pending(PREVIEW, "POST");
  expect(JSON.parse(preview.init.body as string)).toEqual({ url: "https://code.acme.test/acme-code/other", provider: acme.id });
  await answer(preview, { code: "repository_provider_off", error: "Acme Code is switched off." }, 409);
  expect(dialogText()).toContain("Acme Code is switched off.");
  expect(button("Save changes").disabled).toBe(true);
});

it("follows a source whose extension was renamed: the saved repository stays available under the id it keeps", async () => {
  // The hub keeps the id a repository was saved with and follows the rename; the list has the current id only.
  const renamed = { ...acme, id: "ext:acme-code-new/acme" };
  await open([acmeBinding], withSources(renamed));
  const row = document.querySelector('[data-repository="repo_acme"]')!;
  expect(row.querySelector("[data-repository-source-name]")!.textContent).toBe("Acme Code");
  expect(row.querySelector("[data-repository-note]")).toBeNull();
  await click("Edit");
  const select = document.querySelector<HTMLSelectElement>('[role="dialog"] select')!;
  expect([...select.options].map((o) => o.textContent)).toEqual(["Detect from public host", "GitHub / GitHub Enterprise", "GitLab", "Acme Code"]);
  expect(select.value).toBe(acme.id);
  expect(select.selectedOptions[0].textContent).toBe("Acme Code");
  await input("Name (optional)", "Renamed");
  await click("Save changes");
  const save = pending(`${LIST}/repo_acme`, "PUT");
  expect(JSON.parse(save.init.body as string)).toMatchObject({ url: acmeIdentity.canonical_url, provider: acme.id });
  expect(calls.some((c) => c.path === PREVIEW)).toBe(false);
  await answer(save, { manager: "assistant", repository: { ...acmeBinding, name: "Renamed" } });
  // A new address goes to its source under the saved id, which the hub follows to the renamed one.
  await click("Edit");
  vi.useFakeTimers();
  try {
    await input("Repository URL", "https://code.acme.test/acme-code/other");
    expect(dialogText()).toContain("Reading the address with Acme Code…");
    await act(async () => { await vi.advanceTimersByTimeAsync(400); });
  } finally { vi.useRealTimers(); }
  expect(JSON.parse(pending(PREVIEW, "POST").init.body as string)).toEqual({ url: "https://code.acme.test/acme-code/other", provider: acme.id });
});

it("asks a source again after it failed: Try again, or the address typed back", async () => {
  await open([], withSources(acme));
  await click("Bind repository"); await choose(acme.id);
  const url = "https://code.acme.test/acme-code/acme";
  vi.useFakeTimers();
  try {
    await input("Repository URL", url);
    await act(async () => { await vi.advanceTimersByTimeAsync(400); });
    await answer(pending(PREVIEW, "POST"), { code: "repository_providers_starting", error: "Extensions are still starting. Try again in a moment." }, 409);
    expect(dialogText()).toContain("Extensions are still starting. Try again in a moment.");
    await click("Try again");
    expect(dialogText()).toContain("Reading the address with Acme Code…");
    await act(async () => { await vi.advanceTimersByTimeAsync(400); });
    await answer(pending(PREVIEW, "POST"), { code: "repository_provider_failed", error: "Acme Code failed: timed out." }, 422);
    expect(dialogText()).toContain("Acme Code failed: timed out.");
    // Typing the same address again reads it again, never showing the old failure meanwhile.
    await input("Repository URL", url + "/");
    await input("Repository URL", url);
    expect(dialogText()).not.toContain("Acme Code failed");
    expect(dialogText()).toContain("Reading the address with Acme Code…");
    await act(async () => { await vi.advanceTimersByTimeAsync(400); });
  } finally { vi.useRealTimers(); }
  expect(calls.filter((c) => c.path === PREVIEW)).toHaveLength(3);
  await answer(pending(PREVIEW, "POST"), { manager: "assistant", identity: acmeIdentity, provider_name: "Acme Code" });
  expect(document.querySelector("[data-repository-preview]")!.textContent).toContain(acmeIdentity.canonical_url);
  expect(button("Check existence and save").disabled).toBe(false);
});
