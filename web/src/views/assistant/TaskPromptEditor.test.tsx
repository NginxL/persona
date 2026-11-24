// @vitest-environment jsdom
import { act, useState } from "react";
import { createRoot, type Root } from "react-dom/client";
import { beforeEach, beforeAll, afterEach, expect, it, vi } from "vitest";
import { TaskPromptEditor } from "./TaskPromptEditor";
import { blankTask, type Task } from "../../lib/routines";
import { loadPages } from "../../lib/lazy-page";
import { splitSkillDraft } from "../../lib/skill-mentions";
import { t } from "../../i18n";
import { storeDraft } from "../../lib/drafts";
import { HISTORY_PREFIX, recordSent } from "../../lib/send-history";
import { reloadSavedPrompts, resetSavedPrompts } from "../../lib/saved-prompts";
import { chooseManager, MANAGER_HEADER, resetManagerChoice } from "../../lib/manager-choice";
import { useMentionRows, type MentionSource } from "../chat/ComposerPanel";
import { toast } from "../../ui/atoms";

const api = vi.hoisted(() => vi.fn());
vi.mock("../../lib/api", async (original) => ({ ...(await original<typeof import("../../lib/api")>()), api }));
vi.mock("../../ui/atoms", async (original) => ({ ...(await original<typeof import("../../ui/atoms")>()), toast: vi.fn() }));
(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let root: Root, host: HTMLDivElement;
const execution = { machine_id: "current", cwd: "/current", launcher: "claude", agent: "claude" };
const catalog = { catalog_version: 1, complete: true, skills: [{ id: "opaque", name: "Index receipts", source: "project" }] };
const draft: Task = { ...blankTask(), prompt: "", cwd: "/current", execution_machine_id: "current" };
const contextPath = "/assistant/tasks/input-context";
const field = () => host.querySelector("textarea")!;
const option = (text: string) => [...host.querySelectorAll('[role="option"]')].find((row) => row.textContent?.includes(text));
// Flush the request debounce and queued React work before reading the next rendered state.
async function settle() {
  await act(async () => { await vi.advanceTimersByTimeAsync(200); });
}
const render = (task: Task) => act(() => root.render(<TaskPromptEditor draft={task} saved={null} disabled={false} onChange={() => {}} />));
function type(text: string) {
  act(() => {
    const el = field();
    el.focus();
    Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")!.set!.call(el, text);
    el.setSelectionRange(text.length, text.length);
    el.dispatchEvent(new Event("input", { bubbles: true }));
  });
}

beforeAll(async () => { await loadPages(); await loadPages(); });
beforeEach(() => {
  vi.useFakeTimers();
  Element.prototype.scrollIntoView ??= () => {};
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  api.mockReset();
  vi.mocked(toast).mockClear();
});
afterEach(() => { act(() => root.unmount()); resetManagerChoice(); host.remove(); vi.useRealTimers(); });

it("keeps a selected Skill across schedule edits and detects directory and catalog scope changes", async () => {
  let skillScope = "original";
  api.mockImplementation(async (path, options) => {
    if (path === contextPath) return { execution: { ...execution, cwd: options.json.cwd }, task_input: true, skill_scope: skillScope, catalog };
    if (path.includes("/fs?")) return { entries: [] };
    throw new Error(`Unexpected request: ${path}`);
  });
  const selected = {
    ...draft, prompt_selections: true,
    prompt: '<persona-context kind="skill" title="Index receipts" ref="opaque">project</persona-context>\n\nRead the notes',
  };
  render(selected);
  await settle();
  const edited: Task = { ...selected, repeat: "daily", push: "asked", fresh_each_run: true };
  render(edited);
  await settle();
  expect(toast).not.toHaveBeenCalled();
  expect(host.textContent).toContain("Index receipts");
  expect(field().value).toBe("Read the notes");
  act(() => host.querySelector<HTMLButtonElement>("[data-composer-plus]")!.click());
  await settle();
  expect(api.mock.calls.filter(([path]) => path === contextPath).slice(-2).map(([, options]) => options.json)).toEqual([
    expect.objectContaining({ cwd: "/current", repeat: "daily", push: "asked", fresh_each_run: true }),
    expect.objectContaining({ cwd: "/current", repeat: "daily", push: "asked", fresh_each_run: true, catalog: true }),
  ]);

  render({ ...edited, cwd: "/other" });
  await settle();
  expect(toast).toHaveBeenLastCalledWith(t("chat-composer.skills_changed"), "error");
  expect(toast).toHaveBeenCalledTimes(1);
  skillScope = "resolved-conversation-profile";
  render({ ...edited, cwd: "/other", fresh_each_run: false });
  await settle();
  expect(toast).toHaveBeenCalledTimes(2);
  expect(host.textContent).toContain("Index receipts");
});

it("discards an abandoned catalog response after getting a different manager's catalog", async () => {
  let finishOld!: (reply: unknown) => void;
  let oldSignal: AbortSignal | undefined;
  const listing = (name: string) => ({ execution, task_input: true, catalog: { ...catalog, skills: [{ id: name, name, source: "project" }] } });
  api.mockImplementation(async (path, options) => {
    if (path.includes("/fs?")) return { entries: [] };
    const manager = new Headers(options.headers).get(MANAGER_HEADER);
    if (manager === "old-manager" && options.json.catalog) {
      oldSignal = options.signal;
      return new Promise((resolve) => { finishOld = resolve; });
    }
    return listing("Current Skill");
  });
  chooseManager("old-manager");
  render(draft);
  await settle();
  const plus = () => host.querySelector<HTMLButtonElement>("[data-composer-plus]")!;
  act(() => plus().click());
  await settle();
  expect(finishOld).toBeTypeOf("function");
  act(() => chooseManager("new-manager"));
  await settle();
  if (!host.querySelector("[data-composer-panel]")) act(() => plus().click());
  await settle();
  expect(option("Current Skill")).toBeDefined();
  await act(async () => finishOld({ ...listing("Stale Skill"), task_input: false }));
  expect(oldSignal?.aborted).toBe(true);
  expect(option("Stale Skill")).toBeUndefined();
  expect(option("Current Skill")).toBeDefined();
  expect(plus().disabled).toBe(false);
});

it("routes task files to the manager that resolved the input context", async () => {
  const manager = "fixture-secondary";
  chooseManager(manager);
  const requests: { path: string; manager: string | null }[] = [];
  api.mockImplementation(async (path, options) => {
    requests.push({ path, manager: new Headers(options.headers).get(MANAGER_HEADER) });
    if (path === contextPath) return { execution, task_input: true, catalog };
    if (path.includes("/fs?")) return { path: "/current", entries: [{ name: "notes.txt", dir: false }], truncated: false };
    throw new Error(`Unexpected request: ${path}`);
  });
  render(draft);
  await settle();
  act(() => host.querySelector<HTMLButtonElement>("[data-composer-plus]")!.click());
  await settle();
  expect(option("notes.txt")).toBeDefined();
  expect(requests.filter((request) => request.path === contextPath).map((request) => request.manager)).toEqual([manager, manager]);
  expect(requests.filter((request) => request.path.includes("/fs?"))).toEqual([
    { path: "/machines/current/fs?path=%2Fcurrent&cwd=%2Fcurrent&task_input=1", manager },
  ]);
});

it("isolates task listings by manager, machine, task directory, and browsed folder", async () => {
  const files = () => [];
  function Listing({ manager, machine, cwd, query }: { manager: string; machine: string; cwd: string; query: string }) {
    const source: MentionSource = { cwd, files, sessions: false, fileManager: manager,
      referenceFiles: { machine, cwd, launcher: "claude", runtime: "" } };
    const { rows } = useMentionRows(query, source, machine);
    return <ul>{rows.map((row) => row.kind === "file" && <li key={row.id}>{row.name}</li>)}</ul>;
  }
  const show = (manager: string, machine: string, cwd: string, query = "sub/") =>
    act(() => root.render(<Listing manager={manager} machine={machine} cwd={cwd} query={query} />));
  let finishAbandoned!: (reply: unknown) => void;
  const listing = (name: string) => ({ entries: [{ name: `${name}.txt`, dir: false }] });
  api.mockImplementation(async (path, options) => {
    const url = new URL(path, "https://fixture.example");
    const manager = new Headers(options.headers).get(MANAGER_HEADER);
    if (manager === "abandoned") return new Promise((resolve) => { finishAbandoned = resolve; });
    const machine = url.pathname.split("/")[2];
    const cwd = url.searchParams.get("cwd")?.slice(1).replaceAll("/", "-");
    return listing(`${manager}-${machine}-${cwd}-${url.searchParams.get("path")?.split("/").at(-1)}`);
  });
  for (const { manager, machine, cwd, query, name } of [
    { manager: "first", machine: "one", cwd: "/workspace", name: "first-one-workspace-sub" },
    { manager: "second", machine: "one", cwd: "/workspace", name: "second-one-workspace-sub" },
    { manager: "second", machine: "two", cwd: "/workspace", name: "second-two-workspace-sub" },
    { manager: "second", machine: "two", cwd: "/workspace/sub", query: "", name: "second-two-workspace-sub-sub" },
    { manager: "second", machine: "two", cwd: "/workspace/sub", query: "nested/", name: "second-two-workspace-sub-nested" },
  ]) {
    show(manager, machine, cwd, query);
    expect(host.textContent).toBe("");
    await settle();
    expect(host.textContent).toBe(`${name}.txt`);
  }
  show("abandoned", "one", "/workspace");
  await settle();
  const last = api.mock.calls.at(-1)![1].signal as AbortSignal;
  show("first", "one", "/workspace");
  await act(async () => finishAbandoned(listing("obsolete")));
  expect(last.aborted).toBe(true);
  expect(host.textContent).toBe("first-one-workspace-sub.txt");
  expect(api.mock.calls).toHaveLength(6);
});

it("ignores an abandoned execution context and requests only the new task directory", async () => {
  let resolveOld!: (value: unknown) => void;
  api.mockImplementation(async (path: string, options) => {
    if (path === contextPath && options.json.cwd === "/old") return new Promise((resolve) => { resolveOld = resolve; });
    if (path === contextPath) return { execution, catalog, task_input: true };
    if (path.includes("/fs?")) return { path: "/current", entries: [{ name: "current.txt", dir: false }], truncated: false };
    return {};
  });
  render({ ...draft, cwd: "/old", execution_machine_id: "old" });
  await settle();
  expect(resolveOld).toBeTypeOf("function");
  render(draft);
  const plus = () => host.querySelector<HTMLButtonElement>("[data-composer-plus]")!;
  await settle();
  expect(plus().disabled).toBe(false);
  await act(async () => resolveOld({ execution: { ...execution, machine_id: "old", cwd: "/old" }, task_input: true }));
  act(() => plus().click());
  await settle();
  expect(option("current.txt")).toBeDefined();
  const files = api.mock.calls.map(([path]) => path as string).filter((path) => path.includes("/fs?"));
  expect(files).toEqual(["/machines/current/fs?path=%2Fcurrent&cwd=%2Fcurrent&task_input=1"]);
});

it.each([undefined, ""])("offers files and skills for an authorized legacy Claude session with agent %s", async (agent) => {
  const task = { ...blankTask(), id: "legacy-review", session_id: "legacy-chat" };
  api.mockImplementation(async (path, options) => {
    if (path === contextPath) {
      expect(options.json).toMatchObject({ id: task.id, session_id: task.session_id });
      // The catalog may report the default explicitly even if the preview omitted it.
      return { execution: { ...execution, agent: options.json.catalog ? "claude" : agent }, task_input: true, catalog };
    }
    if (path === "/machines/current/fs?path=%2Fcurrent&cwd=%2Fcurrent&task_input=1") {
      return { path: "/current", entries: [{ name: "docs", dir: true }], truncated: false };
    }
    if (path === "/machines/current/fs?path=%2Fcurrent%2Fdocs&cwd=%2Fcurrent&task_input=1") {
      return { path: "/current/docs", entries: [{ name: "notes.txt", dir: false }], truncated: false };
    }
    throw new Error(`Unexpected request: ${path}`);
  });

  let serialized = "";
  function Form() {
    const [draft, setDraft] = useState(task);
    serialized = draft.prompt;
    return <TaskPromptEditor draft={draft} saved={task} disabled={false}
      onChange={(prompt, prompt_selections) => setDraft({ ...draft, prompt, prompt_selections })} />;
  }

  act(() => root.render(<Form />));
  await settle();
  const plus = host.querySelector<HTMLButtonElement>("[data-composer-plus]")!;
  expect(plus.disabled).toBe(false);
  act(() => plus.click());
  await settle();
  const folder = option("docs/") as HTMLElement | undefined;
  expect(folder).toBeDefined();
  act(() => folder?.click());
  expect(splitSkillDraft(serialized, true).skills).toEqual([]);
  await settle();
  const notes = option("notes.txt") as HTMLElement | undefined;
  expect(notes).toBeDefined();
  act(() => notes?.click());
  expect(splitSkillDraft(serialized, true).skills[0]).toMatchObject({ kind: "file", attrs: { path: "docs/notes.txt", machine: "current" } });
  type("/Index");
  await settle();
  expect(option("Index receipts")).toBeDefined();
  act(() => field().dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true, cancelable: true })));
  expect(splitSkillDraft(serialized, true).skills.map((ref) => ref.kind)).toEqual(["file", "skill"]);
});

it("keeps the pickers closed for a known unsupported agent even with task input acknowledged", async () => {
  api.mockResolvedValue({ execution: { ...execution, agent: "external" }, task_input: true });
  render({ ...draft, prompt: "/Index" });
  await settle();
  expect(host.querySelector<HTMLButtonElement>("[data-composer-plus]")?.disabled).toBe(true);
  expect(option("Index receipts")).toBeUndefined();
  expect(api.mock.calls.filter(([path]) => path === contextPath)).toHaveLength(1);
});

it("selects a task-scoped slash skill with the keyboard and preserves prose and identity while editing", async () => {
  api.mockImplementation(async (path: string, options) => {
    if (path === contextPath) return { execution, task_input: true, catalog: options.json.catalog ? catalog : undefined };
    return {};
  });
  let raw = "";
  function Form() {
    const [task, setTask] = useState<Task>({ ...draft, repeat: "daily", fresh_each_run: true });
    raw = task.prompt;
    return <TaskPromptEditor draft={task} saved={null} disabled={false}
      onChange={(prompt, prompt_selections) => setTask({ ...task, prompt, prompt_selections })} />;
  }
  act(() => root.render(<Form />));
  await settle();
  expect(host.querySelector<HTMLButtonElement>("[data-composer-plus]")?.disabled).toBe(false);
  type("/Index");
  await settle();
  expect(option("Index receipts")).toBeDefined();
  act(() => { field().dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true, cancelable: true })); });
  expect(field().value).toBe("");
  expect(splitSkillDraft(raw, true).skills[0].attrs?.ref).toBe("opaque");
  type("  Review tomorrow\n");
  expect(field().value).toBe("  Review tomorrow\n");
  expect(splitSkillDraft(raw, true).text).toBe(field().value);
  const preview = api.mock.calls.filter(([path]) => path === contextPath);
  expect(preview.map(([, options]) => options.json.catalog ?? false)).toEqual([false, true]);
  expect(preview[1][1].json).toMatchObject({ cwd: "/current", repeat: "daily", fresh_each_run: true });
  expect(preview[1][1].json).not.toHaveProperty("id");
});

it("keeps task typing and saved prompt selection out of chat drafts and send history", async () => {
  const chat = "chat-fixture";
  const reusable = { id: "saved-review", name: "fixture-review", text: "Review the synthetic notes." };
  const existing = { ...draft, id: "job", prompt: "Original task prompt" };
  const chatStorage = () => Object.fromEntries(Object.keys(localStorage)
    .filter((key) => key.startsWith("draft:") || key.startsWith(HISTORY_PREFIX))
    .map((key) => [key, localStorage.getItem(key)]));
  localStorage.clear();
  storeDraft(chat, "Unsent chat draft");
  recordSent(chat, "Previously sent chat message");
  const before = chatStorage();
  expect(before).toHaveProperty("draft:chat-fixture", "Unsent chat draft");
  expect(before[HISTORY_PREFIX + chat]).toContain("Previously sent chat message");
  api.mockImplementation(async (path) => {
    if (path === contextPath) return { execution, task_input: true };
    if (path === "/assistant/saved-prompts") return { prompts: [reusable] };
    throw new Error("Unexpected request");
  });
  function Form() {
    const [task, setTask] = useState(existing);
    return <TaskPromptEditor draft={task} saved={existing} disabled={false}
      onChange={(prompt, prompt_selections) => setTask({ ...task, prompt, prompt_selections })} />;
  }
  const open = () => act(() => root.render(<Form />));
  const flush = () => act(async () => { await vi.advanceTimersByTimeAsync(400); });
  try {
    await act(async () => { await reloadSavedPrompts(); });
    open();
    type("Typed only in the task");
    await flush();
    expect(field().value).toBe("Typed only in the task");
    expect(chatStorage()).toEqual(before);

    type(`/${reusable.name}`);
    await flush();
    expect(option(reusable.name)).toBeDefined();
    act(() => field().dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true, cancelable: true })));
    expect(field().value).toBe(reusable.text);
    act(() => window.dispatchEvent(new Event("pagehide")));
    expect(chatStorage()).toEqual(before);

    act(() => root.render(null));
    expect(chatStorage()).toEqual(before);
    open();
    await flush();
    expect(field().value).toBe(existing.prompt);
    expect(chatStorage()).toEqual(before);
  } finally {
    act(() => { root.render(null); resetSavedPrompts(); });
    localStorage.clear();
  }
});

it("retains a saved prompt when the execution computer is unavailable", async () => {
  api.mockRejectedValue(new Error("Computer unavailable"));
  const literal = '<persona-context kind="skill" ref="example">Example</persona-context>\n\nKeep my note';
  const task = { ...draft, id: "existing", prompt: literal };
  const changed = vi.fn();
  act(() => root.render(<TaskPromptEditor draft={task} saved={task} disabled={false} onChange={changed} />));
  await settle();
  expect(field().value).toBe(literal);
  expect(host.querySelector("[role=alert]")?.textContent).toContain("Computer unavailable");
  expect(host.querySelector<HTMLButtonElement>("[data-composer-plus]")!.disabled).toBe(true);
  expect(changed).not.toHaveBeenCalled();
});

it.each([false, undefined])("keeps text and quick prompts usable when the selected computer reports task input %s", async (taskInput) => {
  const inputError = "Update this computer to select files and skills.";
  api.mockImplementation(async (path) => path === contextPath ? { execution, task_input: taskInput, input_error: inputError } : {});
  function Form() {
    const [task, setTask] = useState(draft);
    return <TaskPromptEditor draft={task} saved={null} disabled={false}
      onChange={(prompt, prompt_selections) => setTask({ ...task, prompt, prompt_selections })} />;
  }
  act(() => root.render(<Form />));
  await settle();
  expect(host.querySelector("[role=alert]")?.textContent).toBe(inputError);
  expect(host.querySelector<HTMLButtonElement>("[data-composer-plus]")!.disabled).toBe(true);
  type("plain @");
  await settle();
  expect(field().value).toBe("plain @");
  expect(host.querySelector("[role=listbox]")).toBeNull();
  const command = t("chat-composer.cmd_report_word");
  type(command);
  await settle();
  expect(option(command.slice(1))).toBeDefined();
  act(() => field().dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true, cancelable: true })));
  expect(field().value).toBe(t("chat-composer.cmd_report_text"));
  expect(api.mock.calls.filter(([path]) => path === contextPath)).toHaveLength(1);
  expect(api.mock.calls.some(([path]) => String(path).includes("/fs?"))).toBe(false);
});

it("retires the picker if the computer loses support while opening its catalog", async () => {
  const inputError = "This computer no longer supports task selections.";
  api.mockImplementation(async (path, options) => {
    if (path !== contextPath) return {};
    return options.json.catalog ? { execution, task_input: false, input_error: inputError } : { execution, task_input: true };
  });
  render({ ...draft, prompt: "/Index" });
  await settle();
  expect(host.querySelector("[role=alert]")?.textContent).toBe(inputError);
  expect(host.querySelector<HTMLButtonElement>("[data-composer-plus]")!.disabled).toBe(true);
  expect(field().value).toBe("/Index");
});
