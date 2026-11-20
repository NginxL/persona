// @vitest-environment jsdom
// Audit item 70: ⌘↩ / Ctrl+↩ submits the To-dos page's dialogs from their multi-line fields, as New Agent, Compose and the
// inbox's hand-off do (lib/shortcuts.ts keeps Mod+Enter for dialogs): New to-do from its notes, a decision from its
// reason, Hand to agent from what to tell the agent. Enter alone stays a new line. The hub is faked; the data made up.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Todo } from "../lib/todos";

vi.hoisted(() => {
  window.matchMedia = ((query: string) => ({
    matches: false,
    media: query,
    onchange: null,
    addEventListener() {},
    removeEventListener() {},
    addListener() {},
    removeListener() {},
    dispatchEvent: () => false,
  })) as typeof window.matchMedia;
});

const api = vi.fn(async (_path: string, _init?: unknown): Promise<unknown> => ({}));
vi.mock("../lib/api", async (orig) => ({
  ...(await orig<typeof import("../lib/api")>()),
  api: (path: string, init?: unknown) => api(path, init),
}));
const launched: Record<string, unknown>[] = [];
vi.mock("../lib/new-agent", async (orig) => ({
  ...(await orig<typeof import("../lib/new-agent")>()),
  launchAgent: (input: Record<string, unknown>) => {
    launched.push(input);
    return "local-1";
  },
}));

const { getState } = await import("../lib/store");
const { TodosView } = await import("./Todos");
const { t } = await import("../i18n");

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let host: HTMLDivElement;
let root: Root;
const flush = async () => {
  for (let i = 0; i < 6; i++) await act(async () => { await Promise.resolve(); });
};
const iso = (agoMs: number) => new Date(Date.now() - agoMs).toISOString();
const todo = { id: "t1", title: "Plan the trip", created_at: iso(7_200_000), updated_at: iso(3_600_000), agents: [] } as unknown as Todo;
const seed = (patch: Record<string, unknown>) => Object.assign(getState() as unknown as Record<string, unknown>, patch);
const before = { ...getState() };

beforeEach(async () => {
  api.mockReset();
  api.mockImplementation(async (path, init) => {
    if (path === "/todos" && (init as { method?: string })?.method === "POST") return { todo: { ...todo, id: "t2", title: "Pack" } };
    if (path === "/assistant/repositories") return { manager: "assistant", catalog_revision: 1, repositories: [
      { id: "web", provider: "github", instance_url: "https://github.com", path: "example/web", canonical_url: "https://github.com/example/web", name: "Web app", verified_at: "2026-01-01T00:00:00Z" },
    ] };
    if (path.endsWith("/decisions") && (init as { method?: string })?.method === "POST") return { decision: { id: "d1" } };
    if (path.endsWith("/decisions")) return { decisions: [] };
    if (path.endsWith("/launchers")) return { launchers: [] };
    return {};
  });
  launched.length = 0;
  seed({ todos: [todo], todosLoaded: true, sessions: {}, interactions: [], answered: {}, machines: [{ id: "m1", name: "Example box", online: true }] });
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  await act(async () => root.render(<TodosView selectedId="t1" />));
  await flush();
});
afterEach(async () => {
  await act(async () => root.unmount());
  host.remove();
  document.body.innerHTML = "";
  seed(before);
});

const button = (label: string) => [...document.querySelectorAll<HTMLButtonElement>("button")].find((b) => b.textContent?.trim() === label);
const dialog = () => document.querySelector<HTMLElement>("[role=dialog]");
const posts = (path: string) => api.mock.calls.filter(([p, init]) => p === path && (init as { method?: string })?.method === "POST").map(([, init]) => (init as { json?: unknown }).json);
const write = async (el: HTMLInputElement | HTMLTextAreaElement, value: string) => {
  await act(async () => {
    Object.getOwnPropertyDescriptor(Object.getPrototypeOf(el), "value")!.set!.call(el, value);
    el.dispatchEvent(new Event("input", { bubbles: true }));
  });
};
const press = async (el: HTMLElement, init: KeyboardEventInit) => {
  await act(async () => el.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true, cancelable: true, ...init })));
  await flush();
};
const choose = async (el: HTMLSelectElement, value: string) => {
  await act(async () => {
    Object.getOwnPropertyDescriptor(HTMLSelectElement.prototype, "value")!.set!.call(el, value);
    el.dispatchEvent(new Event("change", { bubbles: true }));
  });
  await flush();
};

describe("⌘↩ / Ctrl+↩ in the To-dos page's dialogs", () => {
  it("creates the new to-do from its notes; Enter alone does not", async () => {
    await act(async () => button(t("todos.new_todo"))!.click());
    await flush();
    const [title] = dialog()!.querySelectorAll<HTMLInputElement>("input[type=text]");
    const notes = dialog()!.querySelector("textarea")!;
    await write(title, "Pack");
    await write(notes, "Passport, charger");
    await press(notes, {});
    expect(posts("/todos")).toEqual([]);
    await press(notes, { metaKey: true });
    expect(posts("/todos")).toEqual([{ title: "Pack", notes: "Passport, charger" }]);
  });

  it("refuses an invalid optional repository draft before creating the to-do", async () => {
    await act(async () => button(t("todos.new_todo"))!.click());
    await flush();
    const title = dialog()!.querySelector<HTMLInputElement>("input[type=text]")!;
    await write(title, "Pack");
    await act(async () => button(t("task-repositories.title"))!.click());
    await flush();
    await act(async () => button(t("task-repositories.add"))!.click());
    await flush();
    await act(async () => document.querySelector<HTMLElement>('[role="option"]')!.click());
    await flush();
    await act(async () => dialog()!.querySelector("summary")!.click());
    await flush();
    await choose(dialog()!.querySelector<HTMLSelectElement>("select")!, "branch");
    const refInput = dialog()!.querySelector("details")!.querySelectorAll<HTMLInputElement>("input")[1];
    expect(refInput).toBeTruthy();
    expect(dialog()!.textContent).toContain(t("task-repositories.ref_invalid"));
    expect(button(t("todos.create"))!.disabled).toBe(true);
    // Submitting the form with the invalid optional draft must not send a create request.
    await press(dialog()!.querySelector("textarea")!, { metaKey: true });
    expect(posts("/todos")).toEqual([]);
    await write(refInput, "main");
    await flush();
    expect(dialog()!.textContent).not.toContain(t("task-repositories.ref_invalid"));
    expect(button(t("todos.create"))!.disabled).toBe(false);
    await act(async () => button(t("todos.create"))!.click());
    await flush();
    expect(posts("/todos")).toMatchObject([{
      title: "Pack",
      repository_bindings: { catalog_revision: 1, repositories: [{ repo_id: "web", requested_ref: { kind: "branch", value: "main" }, links: [] }] },
    }]);
  });

  it("records a decision from its reason", async () => {
    await act(async () => button(t("todos.add_decision"))!.click());
    await flush();
    await write(dialog()!.querySelector<HTMLInputElement>("#decision-question")!, "Which hotel?");
    await write(dialog()!.querySelector<HTMLInputElement>("#decision-choice")!, "The one by the station");
    const reason = dialog()!.querySelector<HTMLTextAreaElement>("#decision-reason")!;
    await write(reason, "Early train");
    await press(reason, { ctrlKey: true });
    expect(posts("/todos/t1/decisions")).toEqual([{ question: "Which hotel?", choice: "The one by the station", reason: "Early train" }]);
  });

  it.each([{ metaKey: true }, { ctrlKey: true }])("keeps repository picker and link Enter local (%j)", async (modifier) => {
    await act(async () => button(t("todos.new_todo"))!.click());
    await flush();
    await write(dialog()!.querySelector<HTMLInputElement>("input[type=text]")!, "Pack");
    await act(async () => button(t("task-repositories.title"))!.click());
    await flush();
    await act(async () => button(t("task-repositories.add"))!.click());
    await flush();
    const search = dialog()!.querySelector<HTMLInputElement>("[data-repository-picker] input")!;
    await press(search, modifier);
    expect(posts("/todos")).toEqual([]);
    expect(dialog()!.querySelectorAll("[data-repository-row]")).toHaveLength(1);
    await act(async () => button(t("task-repositories.add_link"))!.click());
    const link = dialog()!.querySelector<HTMLInputElement>("input[type=url]")!;
    await write(link, "https://github.com/example/docs/issues/7");
    await press(link, modifier);
    expect(posts("/todos")).toEqual([]);
    expect(dialog()!.textContent).toContain(t("task-repositories.link_invalid"));
    await write(link, "HTTPS://github.com/Example/Web/issues/7");
    await press(link, modifier);
    expect(posts("/todos")).toEqual([]);
    expect(dialog()!.querySelectorAll("[data-repository-link]")).toHaveLength(1);
    await press(dialog()!.querySelector("textarea")!, modifier);
    expect(posts("/todos")).toMatchObject([{
      title: "Pack",
      repository_bindings: { repositories: [{ repo_id: "web", links: [{ url: "https://github.com/example/web/issues/7" }] }] },
    }]);
    expect(posts("/todos")).toHaveLength(1);
  });

  it("admits only one creation when submit events arrive before the pending render", async () => {
    await act(async () => button(t("todos.new_todo"))!.click());
    await flush();
    await write(dialog()!.querySelector<HTMLInputElement>("input[type=text]")!, "Pack");
    await act(async () => button(t("task-repositories.title"))!.click());
    await flush();
    await act(async () => button(t("task-repositories.add"))!.click());
    await flush();
    await act(async () => dialog()!.querySelector<HTMLElement>('[role="option"]')!.click());
    const form = dialog()!.querySelector("form")!;
    await act(async () => {
      form.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true }));
      form.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true }));
    });
    await flush();
    expect(posts("/todos")).toHaveLength(1);
  });

  it("hands the to-do to a new agent from what to tell it", async () => {
    await act(async () => button(t("todos.hand_over"))!.click());
    await flush();
    const prompt = dialog()!.querySelector("textarea")!;
    await write(prompt, "Book the earliest train");
    await press(prompt, { metaKey: true });
    expect(launched).toHaveLength(1);
    expect(launched[0]).toMatchObject({ machineId: "m1", todoId: "t1" });
  });
});
