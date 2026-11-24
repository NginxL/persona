// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
vi.hoisted(() => { window.matchMedia ??= ((query: string) => ({ matches: false, media: query, onchange: null, addEventListener() {}, removeEventListener() {} })) as typeof window.matchMedia; });
import { initI18n } from "../i18n";
import { credentialOwner, setToken } from "../lib/api";
import { createTodoWithRepositories, repositoryRequestContext, type RepositoryCatalog, type TaskRepositorySet } from "../lib/task-repositories";
import type { PRFacts } from "../lib/todos";
import { NewTodoRepositoryBindings, TodoRepositoryBindings } from "./TaskRepositoryBindingsEditor";
import { Toasts } from "./atoms";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
type Call = { path: string; init: RequestInit; answered?: boolean; answer: (body: unknown, status?: number) => void };
let root: Root, host: HTMLDivElement, calls: Call[];
const identity = { provider: "github" as const, instance_url: "https://github.com", path: "example/web", canonical_url: "https://github.com/example/web" };
const other = { provider: "github" as const, instance_url: "https://github.com", path: "example/docs", canonical_url: "https://github.com/example/docs" };
const catalog: RepositoryCatalog = { manager: "assistant", catalog_revision: 4, repositories: [{ ...identity, id: "web", verified_at: "2026-01-01T00:00:00Z" }] };
const set: TaskRepositorySet = { schema_version: 1, manager: "assistant", todo_id: "todo_example", revision: 1, repositories: [{ repo_id: "web", requested_ref: { kind: "default" }, source_snapshot: { identity, clone_url: identity.canonical_url, catalog_revision: 4, transport_origin: "canonical_default" }, links: [{ kind: "pull_request", number: "7", canonical_url: identity.canonical_url + "/pull/7" }], catalog_state: "current" }] };
const emptySet: TaskRepositorySet = { schema_version: 1, manager: "assistant", todo_id: "todo_example", revision: 1, repositories: [] };
const twoLinks: TaskRepositorySet = { ...set, revision: 2, repositories: [{ ...set.repositories[0], links: [
  { kind: "pull_request", number: "7", canonical_url: identity.canonical_url + "/pull/7" },
  { kind: "pull_request", number: "8", canonical_url: identity.canonical_url + "/pull/8" },
] }] };
const gitlabIdentity = { provider: "gitlab" as const, instance_url: "https://gitlab.example.com", path: "team/api", canonical_url: "https://gitlab.example.com/team/api" };
const gitlabSet: TaskRepositorySet = { schema_version: 1, manager: "assistant", todo_id: "todo_example", revision: 1, repositories: [{
  repo_id: "api", requested_ref: { kind: "default" },
  source_snapshot: { identity: gitlabIdentity, clone_url: gitlabIdentity.canonical_url, catalog_revision: 4, transport_origin: "canonical_default" },
  links: [{ kind: "pull_request", number: "9", canonical_url: gitlabIdentity.canonical_url + "/-/merge_requests/9" }], catalog_state: "current",
}] };
const titledSet: TaskRepositorySet = { ...twoLinks, repositories: [{ ...twoLinks.repositories[0], links: [
  { kind: "pull_request", number: "7", canonical_url: identity.canonical_url + "/pull/7", title: `Fix "quoted" <b>redirect</b> for the login flow across every provider` },
  { kind: "issue", number: "8", canonical_url: identity.canonical_url + "/issues/8", title: "Reported crash on the first screen" },
] }] };
const twoCatalog: RepositoryCatalog = { ...catalog, repositories: [
  ...catalog.repositories,
  { ...other, id: "docs", name: "Docs", verified_at: "2026-01-01T00:00:00Z" },
] };
const twoSet: TaskRepositorySet = { ...set, revision: 2, repositories: [
  set.repositories[0],
  { repo_id: "docs", requested_ref: { kind: "default" },
    source_snapshot: { identity: other, clone_url: other.canonical_url, catalog_revision: 4, transport_origin: "canonical_default" },
    links: [], catalog_state: "current" },
] };
const typedSet: TaskRepositorySet = { ...set, repositories: [{ ...set.repositories[0], requested_ref: { kind: "tag", value: "v1.0" } }] };

const button = (label: string) => [...document.querySelectorAll<HTMLButtonElement>("button")].find((b) => b.textContent?.trim() === label)!;
const click = async (label: string) => act(async () => { expect(button(label), label).toBeTruthy(); button(label).click(); });
const answer = async (call: Call, body: unknown, status = 200) => { call.answered = true; await act(async () => call.answer(body, status)); };
const pendingCall = (path: string) => [...calls].reverse().find((c) => c.path === path && !c.answered);
/** The editor reads the catalog and the store in parallel: both are answered by their path, never by arrival order. */
const answerEditor = async (catalogBody: unknown, setBody: unknown) => {
  await answer(pendingCall("/assistant/repositories")!, catalogBody);
  await answer(pendingCall("/todos/todo_example/repositories")!, setBody);
};
const legacy = (url: string) => ({ url, text: url.endsWith("/3211") ? "PR #3211" : "PR" });
const render = async (manager = "assistant", todoId = "todo_example", prURL?: string, version?: unknown) => act(async () => root.render(<><TodoRepositoryBindings manager={manager} todoId={todoId} owner={credentialOwner()} legacyPRs={prURL ? [legacy(prURL)] : []} version={version} /><Toasts /></>));
const showPRs = (prStatus?: PRFacts, otherPRs?: PRFacts[]) => act(async () => root.render(
  <TodoRepositoryBindings manager="assistant" owner={credentialOwner()} todoId="todo_example" prStatus={prStatus} otherPRs={otherPRs} />));
const observedPR: PRFacts = { url: identity.canonical_url + "/pull/7", title: "Original title", state: "open", draft: false,
  conflicts: false, review: "pending", checks: "none", checked_at: "2026-01-01T00:00:00Z" };
const savedWithTitle = (title: string, revision: number): TaskRepositorySet => ({ ...set, revision, repositories: [{
  ...set.repositories[0], links: [{ ...set.repositories[0].links[0], title }],
}] });
const settle = async () => act(async () => { await Promise.resolve(); await Promise.resolve(); });
/** The editor's add-link field: named by its own label, not by a repository's link count. */
const linkInput = () => document.querySelector<HTMLInputElement>('input[type="url"]')!;
const typeInto = async (node: HTMLInputElement, value: string) => act(async () => {
  Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(node, value);
  node.dispatchEvent(new Event("input", { bubbles: true }));
});
const choose = async (node: HTMLSelectElement, value: string) => act(async () => {
  Object.getOwnPropertyDescriptor(HTMLSelectElement.prototype, "value")!.set!.call(node, value);
  node.dispatchEvent(new Event("change", { bubbles: true }));
});
const press = async (node: Element, key: string) => act(async () => {
  (node as HTMLElement).dispatchEvent(new KeyboardEvent("keydown", { key, bubbles: true }));
});

beforeEach(async () => {
  await initI18n("en"); setToken("synthetic-task-repository-session"); calls = [];
  host = document.createElement("div"); document.body.append(host); root = createRoot(host);
  vi.stubGlobal("fetch", vi.fn((url: RequestInfo | URL, init: RequestInit = {}) => new Promise<Response>((resolve) => calls.push({ path: String(url).split("/api/v1")[1], init, answer: (body, status = 200) => resolve(new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } })) }))));
});
afterEach(async () => { act(() => root.unmount()); host.remove(); document.body.innerHTML = ""; vi.unstubAllGlobals(); setToken(""); await initI18n("zh-Hans"); });

it("shows the Agent's saved links without an editor, and the load rides on the detail's version", async () => {
  await render();
  expect(calls.map((c) => c.path)).toEqual(["/todos/todo_example/repositories"]);
  await answer(calls[0], twoLinks);
  expect(document.querySelector("[data-todo-repositories]")!.textContent).toContain("#7");
  expect(document.querySelector("[data-todo-repositories]")!.textContent).toContain("#8");
  expect(document.querySelector("textarea")).toBeNull();
  // The revision to edit from is the one that was shown, not a guess.
  await click("Manage links");
  expect(calls.slice(1).map((c) => c.path).sort()).toEqual(["/assistant/repositories", "/todos/todo_example/repositories"]);
  await answerEditor(catalog, twoLinks);
  const dialog = document.querySelector('[role="dialog"]')!;
  expect(dialog.textContent).toContain("#7");
  expect(dialog.textContent).toContain("Pull request");
});

it("matches current and earlier PR facts to their own rows as observations change", async () => {
  const base = identity.canonical_url + "/pull/";
  const current: PRFacts = { url: base.toUpperCase() + "7/?view=1", state: "open", draft: false, conflicts: true,
    title: "Observed title", review: "pending", checks: "none", checked_at: "2026-01-01T00:00:00Z" };
  const earlier: PRFacts = { ...current, url: base + "8", title: "Earlier title", state: "merged", draft: true };
  await showPRs(current, [earlier]);
  const [first, second] = twoLinks.repositories[0].links;
  await answer(calls[0], { ...twoLinks, repositories: [{ ...twoLinks.repositories[0], links: [
    { ...first, title: "Saved title" }, second, { ...second, kind: "issue", canonical_url: identity.canonical_url + "/issues/8" },
  ] }] });
  const [one, two, issue] = host.querySelectorAll("[data-repository-link]");
  expect(one.querySelector("a")?.textContent).toContain("Saved title");
  expect(one.textContent).not.toContain("Observed title");
  expect(one.textContent).toContain("Open");
  expect(one.textContent).toContain("Conflict");
  expect(two.querySelector("a")?.textContent).toContain("Earlier title");
  expect(two.textContent).toContain("Merged");
  expect(two.textContent).not.toMatch(/Conflict|Draft/);
  expect(issue.textContent).not.toMatch(/Earlier title|Merged/);

  await showPRs({ ...current, draft: true, conflicts: false }, [{ ...earlier, state: "closed", title: undefined }]);
  expect(one.textContent).toContain("Draft");
  expect(one.textContent).not.toMatch(/Conflict|Open/);
  expect(two.querySelector("a")?.getAttribute("aria-label")).toContain("Pull request #8");
  expect(two.textContent).toContain("Closed");
  expect(two.textContent).not.toContain("Conflict");
  await showPRs(current, [{ ...earlier, url: other.canonical_url + "/pull/8" }]);
  expect(two.textContent).not.toMatch(/Merged|Earlier title|Closed/);
  expect(calls).toHaveLength(1);
});

it.each(["current", "earlier"])("refreshes a renamed %s PR in an open detail without a to-do version change", async (which) => {
  const show = (pr: PRFacts) => which === "current" ? showPRs(pr) : showPRs(undefined, [pr]);
  await show(observedPR);
  await answer(calls[0], savedWithTitle("Original title", 1));
  const link = host.querySelector("[data-repository-link] a")!;
  expect(link.textContent).toContain("Original title");

  const renamed = { ...observedPR, title: "Renamed while reading", url: observedPR.url + "#discussion" };
  await show(renamed);
  expect(calls).toHaveLength(2);
  expect(calls[1].path).toBe("/todos/todo_example/repositories");
  await answer(calls[1], savedWithTitle("Renamed while reading", 2));
  expect(link.textContent).toContain("Renamed while reading");
  expect(link.getAttribute("aria-label")).not.toContain("Original title");

  await show({ ...renamed, state: "merged", checked_at: "2026-01-01T00:05:00Z" });
  expect(host.querySelector("[data-repository-link]")?.textContent).toContain("Merged");
  expect(calls).toHaveLength(2);
});

it("keeps a late repository response from reverting a newer PR title", async () => {
  await showPRs(observedPR);
  await showPRs({ ...observedPR, title: "Renamed before the first read returned" });
  expect(calls).toHaveLength(2);
  await answer(calls[1], savedWithTitle("Renamed before the first read returned", 2));
  await answer(calls[0], savedWithTitle("Original title", 1));
  expect(host.querySelector("[data-repository-link] a")?.textContent).toContain("Renamed before the first read returned");
});

it("uses the newest earlier observation across URL variants and gives the current PR precedence", async () => {
  const newest: PRFacts = { ...observedPR, url: observedPR.url.toUpperCase() + "/?view=all", title: "Ready", state: "merged" };
  const oldest: PRFacts = { ...observedPR, url: observedPR.url + "#details", title: "In progress", conflicts: true };
  await showPRs(undefined, [newest, oldest]);
  await answer(calls[0], set);
  const row = host.querySelector("[data-repository-link]")!;
  expect(row.querySelector("a")?.textContent).toContain("Ready");
  expect(row.textContent).toContain("Merged");
  expect(row.textContent).not.toMatch(/In progress|Open|Conflict/);

  await showPRs({ ...newest, state: "closed" }, [oldest, newest]);
  expect(row.textContent).toContain("Closed");
  expect(row.textContent).not.toMatch(/Merged|In progress|Conflict/);
});

it("adds a link by its URL, refuses another repository's, and saves the whole set with both revisions", async () => {
  await render();
  await answer(calls[0], set);
  await click("Manage links");
  await answerEditor(catalog, set);
  await settle();
  await click("Add issue or PR");
  const input = linkInput();
  await typeInto(input, "https://github.com/example/docs/pull/9");
  await click("Add issue or PR");
  expect(document.querySelector('[role="alert"]')!.textContent).toContain("Use an issue or pull request link from this repository.");
  expect(input.value).toBe("https://github.com/example/docs/pull/9");
  await typeInto(input, identity.canonical_url + "/pull/8");
  await press(input, "Enter");
  expect(document.querySelectorAll('[role="dialog"] [data-repository-link]')).toHaveLength(2);
  await click("Save");
  const save = calls.at(-1)!;
  expect(save.init.method).toBe("PUT");
  expect(new Headers(save.init.headers).get("X-Persona-Manager")).toBe("assistant");
  // A saved link keeps its kind and number; a pasted one goes as its URL (the server reads both).
  expect(JSON.parse(save.init.body as string)).toMatchObject({
    expected_revision: 1, expected_catalog_revision: 4,
    repositories: [{ repo_id: "web", requested_ref: { kind: "default" }, links: [{ kind: "pull_request", number: "7" }, { url: identity.canonical_url + "/pull/8" }] }],
  });
  await answer(save, twoLinks);
  // Saved: the dialog closes and the page's own rows are the newest saved value.
  expect(document.querySelector('[role="dialog"]')).toBeNull();
  expect(document.querySelector("[data-todo-repositories]")!.textContent).toContain("#8");
});

it("keeps the draft on a revision conflict, and reload truly reads the newest saved set", async () => {
  await render();
  await answer(calls[0], set);
  await click("Manage links");
  await answerEditor(catalog, set);
  await settle();
  await click("Add issue or PR");
  await typeInto(linkInput(), identity.canonical_url + "/pull/8");
  await press(linkInput(), "Enter");
  await click("Save");
  await answer(calls.at(-1)!, { code: "task_repositories_revision_conflict", error: "changed" }, 409);
  expect(document.querySelector('[role="dialog"]')!.textContent).toContain("#8");
  expect(document.querySelector('[role="alert"]')!.textContent).toContain("Your edits are kept");
  // Discard and reload: the store's newer value (the Agent's own addition) replaces the draft.
  await click("Discard edits and reload");
  await answerEditor({ ...catalog, catalog_revision: 5 }, { ...set, revision: 3, repositories: [{ ...set.repositories[0], links: [
    { kind: "pull_request", number: "7", canonical_url: identity.canonical_url + "/pull/7" },
    { kind: "pull_request", number: "12", canonical_url: identity.canonical_url + "/pull/12" },
  ] }] });
  await settle();
  expect(document.querySelector('[role="dialog"]')!.textContent).toContain("#12");
  expect(document.querySelector('[role="dialog"]')!.textContent).not.toContain("#8");
});

it("holds a to-do version change until the editor closes, then saves from the newest revision", async () => {
  await render("assistant", "todo_example", undefined, 1);
  await answer(calls[0], set);
  await click("Manage links");
  await answerEditor(catalog, set);
  await settle();
  // The to-do changes while the dialog is open: no background read may replace the draft.
  const before = calls.length;
  await render("assistant", "todo_example", undefined, 2);
  expect(calls.length).toBe(before);
  await click("Add issue or PR");
  await typeInto(linkInput(), identity.canonical_url + "/pull/8");
  await press(linkInput(), "Enter");
  expect(document.querySelector('[role="dialog"]')!.textContent).toContain("#8");
  // Closing fulfils the held refresh and takes the store's newer revision.
  const newer = { ...twoLinks, revision: 3, repositories: [{ ...set.repositories[0], links: [
    { kind: "pull_request", number: "7", canonical_url: identity.canonical_url + "/pull/7" },
    { kind: "issue", number: "12", canonical_url: identity.canonical_url + "/issues/12" },
  ] }] };
  await click("Cancel");
  await answer(pendingCall("/todos/todo_example/repositories")!, newer);
  await settle();
  const section = document.querySelector("[data-todo-repositories]")!;
  expect(section.textContent).toContain("#12");
  expect(section.textContent).not.toContain("#8");
  // Reopening reads revision 3, so the later save does not conflict.
  await click("Manage links");
  await answerEditor(catalog, newer);
  await settle();
  await click("Add issue or PR");
  await typeInto(linkInput(), identity.canonical_url + "/pull/9");
  await press(linkInput(), "Enter");
  await click("Save");
  const save = calls.at(-1)!;
  expect(JSON.parse(save.init.body as string).expected_revision).toBe(3);
  await answer(save, { ...newer, revision: 4 });
});

it("does not apply an older read that answers while the editor holds unsaved input", async () => {
  await render("assistant", "todo_example", undefined, 1);
  await answer(calls[0], set);
  // A version change starts a background read; keep it unanswered.
  await render("assistant", "todo_example", undefined, 2);
  const background = pendingCall("/todos/todo_example/repositories")!;
  expect(background).toBeTruthy();
  await click("Manage links");
  await answerEditor(catalog, set);
  await settle();
  await click("Add issue or PR");
  await typeInto(linkInput(), identity.canonical_url + "/pull/8");
  await press(linkInput(), "Enter");
  // The older read answers late with another set: the draft must stay exactly as the user left it.
  await answer(background, { ...twoLinks, revision: 9, repositories: [{ ...set.repositories[0], links: [
    { kind: "pull_request", number: "99", canonical_url: identity.canonical_url + "/pull/99" },
  ] }] });
  await settle();
  const dialog = document.querySelector('[role="dialog"]')!;
  expect(dialog.textContent).toContain("#8");
  expect(dialog.textContent).not.toContain("#99");
  await click("Save");
  const save = calls.at(-1)!;
  expect(JSON.parse(save.init.body as string).expected_revision).toBe(1);
  await answer(save, twoLinks);
});

it("does not let an older read replace the saved result after the editor saves", async () => {
  await render("assistant", "todo_example", undefined, 1);
  await answer(calls[0], set);
  await render("assistant", "todo_example", undefined, 2);
  const background = pendingCall("/todos/todo_example/repositories")!;
  await click("Manage links");
  await answerEditor(catalog, set);
  await settle();
  await click("Add issue or PR");
  await typeInto(linkInput(), identity.canonical_url + "/pull/8");
  await press(linkInput(), "Enter");
  await click("Save");
  const save = calls.at(-1)!;
  await answer(save, twoLinks);
  // The read that began before opening answers after the save: the saved #8 must stay.
  await answer(background, set);
  await settle();
  const section = document.querySelector("[data-todo-repositories]")!;
  expect(section.textContent).toContain("#8");
});

it("fetches a fresh store after closing instead of reusing the invalidated read", async () => {
  await render("assistant", "todo_example", undefined, 1);
  await answer(calls[0], set);
  // A version change starts background read B, which stays pending in the shared in-flight slot.
  await render("assistant", "todo_example", undefined, 2);
  const background = pendingCall("/todos/todo_example/repositories")!;
  expect(background).toBeTruthy();
  // Opening the editor reads the store again (rev2), invalidating B; B still stays pending.
  const rev2 = { ...twoLinks, revision: 2, repositories: [{ ...set.repositories[0], links: [
    { kind: "pull_request", number: "7", canonical_url: identity.canonical_url + "/pull/7" },
  ] }] };
  await click("Manage links");
  await answerEditor(catalog, rev2);
  await settle();
  await click("Add issue or PR");
  await typeInto(linkInput(), identity.canonical_url + "/pull/8");
  await press(linkInput(), "Enter");
  // The Agent moves the store to rev3 while the dialog is open: no read, no draft/baseline change.
  const rev3 = { ...rev2, revision: 3, repositories: [{ ...set.repositories[0], links: [
    { kind: "pull_request", number: "7", canonical_url: identity.canonical_url + "/pull/7" },
    { kind: "issue", number: "13", canonical_url: identity.canonical_url + "/issues/13" },
  ] }] };
  const before = calls.length;
  await render("assistant", "todo_example", undefined, 3);
  expect(calls.length).toBe(before);
  const dialog = document.querySelector('[role="dialog"]')!;
  expect(dialog.textContent).toContain("#8");
  expect(dialog.textContent).not.toContain("#13");
  // Closing must start a genuinely new read, not join the invalidated B.
  await click("Cancel");
  const fresh = pendingCall("/todos/todo_example/repositories");
  expect(fresh).toBeTruthy();
  expect(fresh).not.toBe(background);
  // B answering while the fresh close-read is pending changes nothing.
  await answer(background, { ...rev2, revision: 9, repositories: [{ ...set.repositories[0], links: [
    { kind: "pull_request", number: "99", canonical_url: identity.canonical_url + "/pull/99" },
  ] }] });
  await settle();
  expect(document.querySelector("[data-todo-repositories]")!.textContent).not.toContain("#99");
  await answer(fresh!, rev3);
  await settle();
  const section = document.querySelector("[data-todo-repositories]")!;
  expect(section.textContent).toContain("#13");
  // Reopening and saving uses rev3, not stale data.
  await click("Manage links");
  await answerEditor(catalog, rev3);
  await settle();
  await click("Add issue or PR");
  await typeInto(linkInput(), identity.canonical_url + "/pull/9");
  await press(linkInput(), "Enter");
  await click("Save");
  const save = calls.at(-1)!;
  expect(JSON.parse(save.init.body as string).expected_revision).toBe(3);
  await answer(save, { ...rev3, revision: 4 });
});

it("holds a version change while the editor is opening, then refreshes after it closes", async () => {
  await render("assistant", "todo_example", undefined, 1);
  await answer(calls[0], set);
  await click("Manage links");
  // The editor's catalog and store reads are pending. A version change now must not start a competing read.
  const before = calls.length;
  await render("assistant", "todo_example", undefined, 2);
  expect(calls.length).toBe(before);
  // The editor's own read answers with the store it began from (rev1).
  await answerEditor(catalog, set);
  await settle();
  const dialog = document.querySelector('[role="dialog"]')!;
  expect(dialog).toBeTruthy();
  // The editor keeps the baseline it loaded (rev1); the newer store is not applied over it.
  expect(dialog.textContent).toContain("#7");
  expect(dialog.textContent).not.toContain("#12");
  await click("Add issue or PR");
  await typeInto(linkInput(), identity.canonical_url + "/pull/8");
  await press(linkInput(), "Enter");
  // Closing reads the newer store and updates the page.
  const rev2 = { ...twoLinks, revision: 2, repositories: [{ ...set.repositories[0], links: [
    { kind: "pull_request", number: "7", canonical_url: identity.canonical_url + "/pull/7" },
    { kind: "issue", number: "12", canonical_url: identity.canonical_url + "/issues/12" },
  ] }] };
  await click("Cancel");
  const fresh = pendingCall("/todos/todo_example/repositories");
  expect(fresh).toBeTruthy();
  await answer(fresh!, rev2);
  await settle();
  expect(document.querySelector("[data-todo-repositories]")!.textContent).toContain("#12");
});

it("ignores a stale read's late failure after a successful save", async () => {
  await render("assistant", "todo_example", undefined, 1);
  await answer(calls[0], set);
  // A version change starts background read B; opening the editor invalidates it but leaves it pending.
  await render("assistant", "todo_example", undefined, 2);
  const background = pendingCall("/todos/todo_example/repositories")!;
  await click("Manage links");
  await answerEditor(catalog, set);
  await settle();
  await click("Add issue or PR");
  await typeInto(linkInput(), identity.canonical_url + "/pull/8");
  await press(linkInput(), "Enter");
  await click("Save");
  const save = calls.at(-1)!;
  await answer(save, twoLinks);
  expect(document.querySelector("[data-todo-repositories]")!.textContent).toContain("#8");
  // The invalidated read fails late with a 404: it must not hide the valid saved links.
  await answer(background, { code: "not_found", error: "Not Found" }, 404);
  await settle();
  const section = document.querySelector("[data-todo-repositories]");
  expect(section).toBeTruthy();
  expect(section!.textContent).toContain("#8");
  expect(section!.querySelector('[role="alert"]')).toBeNull();
});

it("reports a current read failure normally", async () => {
  await render();
  await answer(calls[0], { code: "task_repositories_store_unavailable", error: "unavailable" }, 503);
  expect(document.querySelector("[data-todo-repositories]")!.textContent).toContain("Couldn't confirm repository links were loaded or saved.");
});

it("falls back to the legacy PR link on a hub without the association route", async () => {
  await render("assistant", "todo_example", "https://example.com/pull/3211");
  await answer(calls[0], { code: "not_found", error: "Not Found" }, 404);
  const link = document.querySelector<HTMLAnchorElement>('[data-todo-repositories] a[href="https://example.com/pull/3211"]');
  expect(link).toBeTruthy();
  expect(link!.textContent).toBe("PR #3211");
  expect(document.querySelector("[data-todo-repositories]")!.textContent).toContain("Linked before repositories were stored here.");
});

it("never draws the legacy link beside the store's links", async () => {
  await render("assistant", "todo_example", "https://example.com/pull/3211");
  await answer(calls[0], twoLinks);
  const section = document.querySelector("[data-todo-repositories]")!;
  expect(section.querySelector('a[href="https://example.com/pull/3211"]')).toBeNull();
  expect(section.textContent).toContain("#7");
});

// Backend 2fb96c7b1 imports the legacy PR history into the canonical store once, and an explicit removal sticks. A
// successful empty answer is therefore the whole truth: the old pr_url must not come back as a fallback (#review).
it("trusts a successful empty store over an existing legacy PR", async () => {
  await render("assistant", "todo_example", "https://example.com/pull/3211");
  await answer(calls[0], emptySet);
  const section = document.querySelector("[data-todo-repositories]")!;
  expect(section.querySelector('a[href="https://example.com/pull/3211"]')).toBeNull();
  expect(section.textContent).toContain("No repositories linked");
});

it("keeps the last repository removed after an empty save and a reload, with no legacy PR back", async () => {
  await render("assistant", "todo_example", "https://example.com/pull/3211");
  await answer(calls[0], set);
  await click("Manage links");
  await answerEditor(catalog, set);
  await settle();
  // Remove the only repository in the dialog, then save the empty set.
  await act(async () => { [...document.querySelectorAll('[role="dialog"] [aria-label]')].find((b) => b.getAttribute("aria-label") === "Options for example/web")!.dispatchEvent(new MouseEvent("click", { bubbles: true })); });
  const remove = [...document.querySelectorAll<HTMLElement>('[role="menuitem"]')].find((item) => item.textContent?.includes("Remove repository"))!;
  expect(remove).toBeTruthy();
  await act(async () => { remove.click(); });
  await click("Save");
  const save = calls.at(-1)!;
  expect(save.init.method).toBe("PUT");
  expect(JSON.parse(save.init.body as string).repositories).toEqual([]);
  await answer(save, { ...emptySet, revision: 2 });
  // Saved empty: the page's own view is empty too, and the removed PR stays gone.
  const section = document.querySelector("[data-todo-repositories]")!;
  expect(section.querySelector('a[href="https://example.com/pull/3211"]')).toBeNull();
  expect(section.textContent).toContain("No repositories linked");
  // A reload (the version changes) reads the same empty store and still shows no legacy fallback.
  await render("assistant", "todo_example", "https://example.com/pull/3211");
  await answer(calls.at(-1)!, { ...emptySet, revision: 2 });
  const reloaded = document.querySelector("[data-todo-repositories]")!;
  expect(reloaded.querySelector('a[href="https://example.com/pull/3211"]')).toBeNull();
  expect(reloaded.textContent).toContain("No repositories linked");
});

it("draws the provider's own mark and the project address inline, with a merge mark for a GitLab merge request", async () => {
  await render();
  await answer(calls[0], gitlabSet);
  const section = document.querySelector("[data-todo-repositories]")!;
  const mark = section.querySelector('[data-repository-mark="gitlab"]')!;
  expect(mark).toBeTruthy();
  const row = section.querySelector("[data-repository-row]")!;
  // The mark is inline in the row itself, not inside the old 28 px tile.
  expect(mark.parentElement).toBe(row);
  expect(row.querySelector("a")!.getAttribute("href")).toBe(gitlabIdentity.canonical_url);
  expect(row.textContent).toContain("gitlab.example.com/team/api");
  expect(section.textContent).not.toContain("GitLab ·");
  expect(section.querySelector('[data-link-icon="merge"]')).toBeTruthy();
  expect(section.querySelector('a[href$="/-/merge_requests/9"]')).toBeTruthy();
});

it("puts a saved link's complete title in its single hyperlink instead of only type and number", async () => {
  await render();
  await answer(calls[0], titledSet);
  const links = [...document.querySelectorAll<HTMLAnchorElement>("[data-repository-link] a")];
  expect(links).toHaveLength(2);
  expect(links[0].textContent).toContain(`Fix "quoted" <b>redirect</b> for the login flow across every provider`);
  expect(links[0].textContent).toContain("#7");
  expect(links[1].textContent).toContain("Reported crash on the first screen");
  expect(links[1].textContent).toContain("#8");
  // A title is plain text: markup in it is escaped, never drawn, and the whole text stays in one hyperlink.
  expect(document.querySelector("[data-todo-repositories]")!.innerHTML).not.toContain("<b>redirect</b>");
  expect(links[0].querySelector("span")!.textContent).toBe(`Fix "quoted" <b>redirect</b> for the login flow across every provider`);
});

it("keeps loaded source titles through an editor save and sends them with their own links", async () => {
  await render();
  await answer(calls[0], titledSet);
  await click("Manage links");
  await answerEditor(catalog, titledSet);
  await settle();
  expect(document.querySelector('[role="dialog"]')!.textContent).toContain("Reported crash on the first screen");
  await click("Add issue or PR");
  await typeInto(linkInput(), identity.canonical_url + "/pull/12");
  await press(linkInput(), "Enter");
  await click("Save");
  const save = calls.at(-1)!;
  expect(JSON.parse(save.init.body as string).repositories[0].links).toEqual([
    { kind: "pull_request", number: "7", title: `Fix "quoted" <b>redirect</b> for the login flow across every provider` },
    { kind: "issue", number: "8", title: "Reported crash on the first screen" },
    { url: identity.canonical_url + "/pull/12" },
  ]);
  await answer(save, twoLinks);
});

it("ignores late results after manager, todo or credential changes", async () => {
  await render();
  const old = [...calls];
  await render("other", "todo_other");
  // The retired read's answer lands on a page that has moved on: nothing of it is drawn.
  await answer(old[0], set);
  expect(document.querySelector("[data-todo-repositories]")!.textContent).not.toContain("#7");
  const pending = calls.slice(1);
  expect(pending).toHaveLength(1);
  setToken("replacement-task-repository-session");
  await answer(pending[0], { ...set, manager: "other", todo_id: "todo_other" });
  expect(document.querySelector("[data-todo-repositories]")!.textContent).not.toContain("#7");
});

it("keeps a source snapshot whose catalog entry changed or disappeared", async () => {
  await render();
  await answer(calls[0], { ...set, repositories: [{ ...set.repositories[0], catalog_state: "identity_changed" }] });
  expect(document.querySelector("[data-todo-repositories]")!.textContent).toContain("This saved repository now has a different source.");
  expect(document.querySelector("[data-todo-repositories]")!.textContent).toContain("#7");
  await click("Manage links");
  // The catalog no longer holds the repository: the saved identity and its links still edit normally.
  await answerEditor({ ...catalog, catalog_revision: 5, repositories: [] }, { ...set, repositories: [{ ...set.repositories[0], catalog_state: "removed" }] });
  await settle();
  expect(document.querySelector('[role="dialog"]')!.textContent).toContain("The saved repository was removed.");
  expect(document.querySelector('[role="dialog"]')!.textContent).toContain("#7");
});

it("keeps an empty alias, default ref and a controlled creation draft without requests per edit", async () => {
  const context = repositoryRequestContext("assistant", credentialOwner(), () => true);
  const { useState } = await import("react");
  function Draft() { const [value, onChange] = useState<import("../lib/task-repositories").CreateRepositoryBindings>(); return <NewTodoRepositoryBindings context={context} value={value} onChange={onChange} pending={false} />; }
  await act(async () => root.render(<Draft />));
  await click("Repositories and links (optional)");
  await answer(calls[0], catalog);
  await act(async () => { button("Add repository").click(); });
  await act(async () => { document.querySelector<HTMLElement>('[role="option"]')!.click(); });
  expect(document.querySelector<HTMLInputElement>('input[maxlength="64"]')!.value).toBe("");
  await click("Add issue or PR");
  await typeInto(linkInput(), identity.canonical_url + "/pull/7");
  await press(linkInput(), "Enter");
  expect(calls).toHaveLength(1);
  expect(document.querySelector("[data-task-repository-editor]")!.textContent).toContain("#7");
  expect(document.querySelector("[data-task-repository-editor]")!.textContent).toContain("Pull request");
});

it("creates the Todo and association draft in one request, preserving old defaults", async () => {
  const context = repositoryRequestContext("assistant", credentialOwner(), () => true);
  const pending = createTodoWithRepositories(context, " New work ", " Details ", undefined, { catalog_revision: 4, repositories: [{ repo_id: "web", requested_ref: { kind: "default" }, links: [{ url: identity.canonical_url + "/pull/7" }, { url: identity.canonical_url + "/pull/8" }, { url: "" }] }] }, new Map([["web", identity]]));
  expect(calls).toHaveLength(1);
  expect(calls[0].path).toBe("/todos");
  const body = JSON.parse(calls[0].init.body as string);
  expect(body).toMatchObject({ title: "New work", notes: "Details", repository_bindings: { catalog_revision: 4, repositories: [{ repo_id: "web", requested_ref: { kind: "default" }, links: [{ url: identity.canonical_url + "/pull/7" }, { url: identity.canonical_url + "/pull/8" }] }] } });
  expect(body.workspace).toBeUndefined();
  await answer(calls[0], { todo: { id: "new_todo", title: "New work" } });
  expect((await pending).id).toBe("new_todo");
});

it("offers no empty-state sentence that promises an editor nobody can open", async () => {
  const context = repositoryRequestContext("assistant", credentialOwner(), () => true);
  const { useState } = await import("react");
  function Draft() { const [value, onChange] = useState<import("../lib/task-repositories").CreateRepositoryBindings>(); return <NewTodoRepositoryBindings context={context} value={value} onChange={onChange} pending={false} />; }
  await act(async () => root.render(<Draft />));
  await click("Repositories and links (optional)");
  await answer(calls[0], { ...catalog, repositories: [] });
  await act(async () => { button("Add repository").click(); });
  expect(document.querySelector("[data-repository-picker]")!.textContent).toContain("No saved repositories to add, or all of them are linked already.");
});

it("disables the editor save on an invalid source ref and recovers when it is corrected", async () => {
  await render();
  await answer(calls[0], set);
  await click("Manage links");
  await answerEditor(catalog, set);
  await settle();
  const dialog = document.querySelector('[role="dialog"]')!;
  await choose(dialog.querySelector("select")!, "branch");
  const refInput = [...dialog.querySelectorAll<HTMLInputElement>("details input")].find((input) => input.maxLength !== 64)!;
  expect(refInput).toBeTruthy();
  expect(dialog.textContent).toContain("Use a valid branch, tag, commit or Git ref.");
  expect((button("Save") as HTMLButtonElement).disabled).toBe(true);
  await typeInto(refInput, "main");
  await settle();
  expect(dialog.textContent).not.toContain("Use a valid branch, tag, commit or Git ref.");
  expect((button("Save") as HTMLButtonElement).disabled).toBe(false);
  await click("Save");
  const save = calls.at(-1)!;
  expect(JSON.parse(save.init.body as string).repositories[0].requested_ref).toEqual({ kind: "branch", value: "main" });
  await answer(save, { ...set, revision: 2 });
});

it("rejects duplicate directory names case-insensitively before saving", async () => {
  await render();
  await answer(calls[0], twoSet);
  await click("Manage links");
  await answerEditor(twoCatalog, twoSet);
  await settle();
  const aliases = [...document.querySelectorAll<HTMLInputElement>('[role="dialog"] input[maxlength="64"]')];
  expect(aliases).toHaveLength(2);
  await typeInto(aliases[0], "Web");
  await typeInto(aliases[1], "web");
  await settle();
  expect(document.querySelector('[role="dialog"]')!.textContent).toContain("Directory names must be unique, ignoring case.");
  expect((button("Save") as HTMLButtonElement).disabled).toBe(true);
  await typeInto(aliases[1], "docs");
  await settle();
  expect((button("Save") as HTMLButtonElement).disabled).toBe(false);
  await click("Save");
  const save = calls.at(-1)!;
  expect(JSON.parse(save.init.body as string).repositories.map((row: { directory_alias?: string }) => row.directory_alias)).toEqual(["Web", "docs"]);
  await answer(save, { ...twoSet, revision: 3 });
});

it("shows and preserves a saved tag ref instead of resetting it", async () => {
  await render();
  await answer(calls[0], typedSet);
  await click("Manage links");
  await answerEditor(catalog, typedSet);
  await settle();
  const dialog = document.querySelector('[role="dialog"]')!;
  expect(dialog.querySelector<HTMLSelectElement>("select")!.value).toBe("tag");
  const refInput = [...dialog.querySelectorAll<HTMLInputElement>("details input")].find((input) => input.maxLength !== 64)!;
  expect(refInput.value).toBe("v1.0");
  expect((button("Save") as HTMLButtonElement).disabled).toBe(true);
});

it("renders the picker as a searchable list, and Escape goes back to its button", async () => {
  const context = repositoryRequestContext("assistant", credentialOwner(), () => true);
  const { useState } = await import("react");
  function Draft() { const [value, onChange] = useState<import("../lib/task-repositories").CreateRepositoryBindings>(); return <NewTodoRepositoryBindings context={context} value={value} onChange={onChange} pending={false} />; }
  await act(async () => root.render(<Draft />));
  await click("Repositories and links (optional)");
  await answer(calls[0], { ...catalog, repositories: [{ ...identity, id: "web", name: "Web app", verified_at: "2026-01-01T00:00:00Z" }, { ...other, id: "docs", name: "Docs", verified_at: "2026-01-01T00:00:00Z" }] });
  await act(async () => { button("Add repository").click(); });
  const options = () => [...document.querySelectorAll<HTMLElement>('[role="option"]')];
  expect(options().map((o) => o.textContent)).toEqual([expect.stringContaining("Web app"), expect.stringContaining("Docs")]);
  expect(options()[0].querySelector('[data-repository-mark="github"]')).toBeTruthy();
  const search = document.querySelector<HTMLInputElement>('[data-repository-picker] input')!;
  await typeInto(search, "docs");
  expect(options()).toHaveLength(1);
  expect(options()[0].textContent).toContain("Docs");
  await press(search, "Escape");
  // The list closes (its content leaves with the closing fold), and the focus goes back to the button that opened it.
  expect(button("Add repository").getAttribute("aria-expanded")).toBe("false");
  expect(document.activeElement).toBe(button("Add repository"));
});

// An extension's repository on a placeholder host: its links keep the addresses the manager stored.
const acme = { provider: "ext:acme-code/acme", instance_url: "https://code.acme.test", path: "acme-code/acme", canonical_url: "https://code.acme.test/acme-code/acme" };
const acmeReview = { kind: "pull_request" as const, number: "12", canonical_url: acme.canonical_url + "/reviews/12" };
const acmeIssue = { kind: "issue" as const, number: "4", canonical_url: acme.canonical_url + "/tickets/4", title: "Login loops" };
const acmeCatalog: RepositoryCatalog = { manager: "assistant", catalog_revision: 4, repositories: [{ ...identity, id: "web", verified_at: "2026-01-01T00:00:00Z" }, { ...acme, id: "acme", name: "Acme app", provider_name: "Acme Code", verified_at: "2026-01-01T00:00:00Z" }] };
const acmeSet = (state: "available" | "off" | "missing" = "available"): TaskRepositorySet => ({ schema_version: 1, manager: "assistant", todo_id: "todo_example", revision: 1, repositories: [{
  repo_id: "acme", requested_ref: { kind: "default" },
  source_snapshot: { identity: acme, clone_url: acme.canonical_url, catalog_revision: 4, transport_origin: "catalog", provider_name: "Acme Code" },
  links: [acmeReview, acmeIssue], catalog_state: "current", provider_state: state, link_kinds: [{ kind: "pull_request", label: "MR" }],
}] });

it("opens an extension's links at their stored addresses, in its own words, with a generic mark and its name", async () => {
  await render();
  await answer(calls[0], acmeSet());
  // No request beyond the to-do's own read: the row carries its source's state and words.
  expect(calls.map((c) => c.path)).toEqual(["/todos/todo_example/repositories"]);
  const section = document.querySelector("[data-todo-repositories]")!;
  const mark = section.querySelector('[data-repository-mark="extension"]')!;
  expect(mark.getAttribute("aria-hidden")).toBe("true");
  expect(mark.parentElement).toBe(section.querySelector("[data-repository-row]"));
  // The address first, then its source's name: a narrow line cuts the name before the repository's path.
  expect(section.querySelector("[data-repository-row] a")!.textContent).toBe("code.acme.test/acme-code/acme · Acme Code");
  expect(section.querySelector("[data-repository-row] a")!.getAttribute("title")).toBe(`${acme.canonical_url} · Acme Code`);
  expect(section.textContent).not.toContain("ext:");
  const links = [...section.querySelectorAll<HTMLAnchorElement>("[data-repository-link] a")];
  expect(links.map((a) => a.getAttribute("href"))).toEqual([acmeReview.canonical_url, acmeIssue.canonical_url]);
  // The source's word for a pull request, as given, and its number with #; an issue keeps the localized word under its title.
  expect(links[0].textContent).toContain("MR");
  expect(links[0].textContent).toContain("#12");
  expect(links[0].getAttribute("aria-label")).toBe("MR #12 · acme-code/acme");
  expect(section.querySelector('[data-link-icon="pullRequest"]')).toBeTruthy();
  expect(section.querySelector('[data-link-icon="merge"]')).toBeNull();
  expect(links[1].textContent).toContain("Login loops");
  expect(links[1].getAttribute("aria-label")).toBe("Issue #4 · acme-code/acme · Login loops");
  expect(section.textContent).not.toContain("Unavailable");
});

it("reads a row an extension's source cloned from its own address, and refuses that origin on a built-in row", async () => {
  await render();
  const set = acmeSet();
  set.repositories[0].source_snapshot = { ...set.repositories[0].source_snapshot, clone_url: "ssh://git@code.acme.test/acme-code/acme.git", transport_origin: "provider" };
  await answer(calls[0], set);
  expect(document.querySelector("[data-todo-repositories] [data-repository-row]")).toBeTruthy();
  act(() => root.unmount()); root = createRoot(host); calls = [];
  await render();
  await answer(calls[0], { ...set, repositories: [{ ...set.repositories[0], source_snapshot: { ...set.repositories[0].source_snapshot, identity, clone_url: identity.canonical_url }, links: [], provider_state: undefined, link_kinds: undefined }] });
  expect(document.querySelector("[data-todo-repositories] [data-repository-row]")).toBeNull();
  expect(document.querySelector("[data-todo-repositories]")!.textContent).toContain("Couldn't confirm repository links were loaded or saved.");
});

it("says an unavailable source on its row, keeps its links opening and turns off adding one", async () => {
  await render();
  await answer(calls[0], acmeSet("missing"));
  const section = document.querySelector("[data-todo-repositories]")!;
  expect(section.querySelector("[data-repository-row]")!.textContent).toContain("Unavailable");
  expect(section.querySelector("[data-repository-note]")!.textContent).toBe("Acme Code is unavailable. Its links still open; adding a new one needs it.");
  expect(section.querySelector(`a[href="${acmeReview.canonical_url}"]`)).toBeTruthy();
  await click("Manage links");
  await answerEditor(acmeCatalog, acmeSet("off"));
  await settle();
  const dialog = document.querySelector('[role="dialog"]')!;
  expect(dialog.querySelector("[data-repository-row]")!.textContent).toContain("Unavailable");
  expect(dialog.querySelector("[data-repository-note]")!.textContent).toContain("Acme Code is unavailable.");
  expect(button("Add issue or PR").disabled).toBe(true);
  // Its saved links still show in the dialog and are kept, by kind and number, through a save that removes one.
  expect([...dialog.querySelectorAll<HTMLAnchorElement>("[data-repository-link] a")].map((a) => a.getAttribute("href"))).toEqual([acmeReview.canonical_url, acmeIssue.canonical_url]);
  await act(async () => { dialog.querySelector<HTMLButtonElement>('[data-repository-link] button[aria-label="Remove #4"]')!.click(); });
  await click("Save");
  const save = calls.at(-1)!;
  expect(JSON.parse(save.init.body as string).repositories).toEqual([{ repo_id: "acme", requested_ref: { kind: "default" }, links: [{ kind: "pull_request", number: "12" }] }]);
});

it("adds a pasted address of an extension's repository as pending and sends it as its URL", async () => {
  await render();
  await answer(calls[0], acmeSet());
  await click("Manage links");
  await answerEditor(acmeCatalog, acmeSet());
  await settle();
  await click("Add issue or PR");
  for (const url of ["https://code.other.test/acme-code/acme/reviews/13", "http://code.acme.test/acme-code/acme/reviews/13", "https://code.acme.test/acme-code/sibling/reviews/13", "https://code.acme.test/acme-code/acme/reviews/13?tab=files"]) {
    await typeInto(linkInput(), url);
    await press(linkInput(), "Enter");
    expect(document.querySelector('[role="alert"]')!.textContent, url).toContain("Use an issue or pull request link from this repository.");
  }
  await typeInto(linkInput(), acme.canonical_url + "/reviews/13");
  await press(linkInput(), "Enter");
  const pendingRow = document.querySelector('[role="dialog"] [data-link-pending]')!;
  expect(pendingRow.textContent).toContain(acme.canonical_url + "/reviews/13");
  expect(pendingRow.textContent).toContain("Checked when you save");
  expect(pendingRow.querySelector("a")!.getAttribute("href")).toBe(acme.canonical_url + "/reviews/13");
  await click("Save");
  const save = calls.at(-1)!;
  expect(save.path).toBe("/todos/todo_example/repositories");
  expect(JSON.parse(save.init.body as string).repositories[0].links).toEqual([
    { kind: "pull_request", number: "12" }, { kind: "issue", number: "4", title: "Login loops" }, { url: acme.canonical_url + "/reviews/13" },
  ]);
  // The source's answer decides: its refusal comes back in the hub's words, and the draft stays.
  await answer(save, { code: "task_repositories_provider_refused", error: "Acme Code refused the link: it is not a review." }, 409);
  expect(document.querySelector('[role="dialog"] [role="alert"]')!.textContent).toContain("Acme Code refused the link: it is not a review.");
  expect(document.querySelector('[role="dialog"] [data-link-pending]')).toBeTruthy();
});

it("offers an extension's saved repository in the picker with the generic mark and its source's name", async () => {
  const context = repositoryRequestContext("assistant", credentialOwner(), () => true);
  const { useState } = await import("react");
  function Draft() { const [value, onChange] = useState<import("../lib/task-repositories").CreateRepositoryBindings>(); return <NewTodoRepositoryBindings context={context} value={value} onChange={onChange} pending={false} />; }
  await act(async () => root.render(<Draft />));
  await click("Repositories and links (optional)");
  await answer(calls[0], acmeCatalog);
  await act(async () => { button("Add repository").click(); });
  const option = () => [...document.querySelectorAll<HTMLElement>('[role="option"]')].find((o) => o.textContent?.includes("Acme app"))!;
  expect(option().querySelector('[data-repository-mark="extension"]')).toBeTruthy();
  expect(option().textContent).toContain("acme-code/acme · code.acme.test · Acme Code");
  await typeInto(document.querySelector<HTMLInputElement>('[data-repository-picker] input')!, "acme code");
  expect(document.querySelectorAll('[role="option"]')).toHaveLength(1);
  await act(async () => { option().click(); });
  // A new repository has no saved links: a pasted address is pending until the to-do is created.
  await click("Add issue or PR");
  await typeInto(linkInput(), acme.canonical_url + "/reviews/2");
  await press(linkInput(), "Enter");
  expect(document.querySelector("[data-task-repository-editor] [data-link-pending]")).toBeTruthy();
  expect(calls).toHaveLength(1);
});
