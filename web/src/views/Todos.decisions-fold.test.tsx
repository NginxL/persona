// @vitest-environment jsdom
// A to-do's Decisions fold (#6447, docs/assistant/todos.md "Decision records"): the first five at first with Show all under
// them, the header's fold-all button (the Agents title row's icon) folding the list away and opening it whole, the count
// unchanged, nothing asked of the hub, and each to-do opening at the first five again. The hub is faked; the data made up.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { renderToStaticMarkup } from "react-dom/server";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Todo, TodoDecision } from "../lib/todos";
import { Icon, type IconName } from "../ui/icons";

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

const { t } = await import("../i18n");
const { getState } = await import("../lib/store");
const { TodosView } = await import("./Todos");

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let host: HTMLDivElement;
let root: Root;
const flush = async () => {
  for (let i = 0; i < 6; i++) await act(async () => { await Promise.resolve(); });
};
const click = async (el: Element | undefined | null) => {
  expect(el, "element").toBeTruthy();
  await act(async () => (el as HTMLElement).click());
  await flush();
};
const section = () => document.querySelector<HTMLElement>("[data-testid=todo-decisions]")!;
const rows = () => section().querySelectorAll("ol > li").length;
const fold = () => section().querySelector<HTMLButtonElement>("[data-testid=todo-decisions-fold]");
/** The glyph an icon draws, and the one a button shows: corners in (Collapse all) or out (Expand all). */
const glyph = (name: IconName) => renderToStaticMarkup(<Icon name={name} />).match(/ d="([^"]+)"/)?.[1];
const shows = (b: Element | null | undefined) => b?.querySelector("path")?.getAttribute("d");
const more = () => section().querySelector<HTMLButtonElement>("[data-testid=todo-decisions-more]");
const tip = () => section().querySelector("[data-testid=todo-decisions-fold-tip]")?.textContent;
const decisionReads = () => api.mock.calls.filter(([path]) => path.includes("/decisions")).length;

const iso = (agoMs: number) => new Date(Date.now() - agoMs).toISOString();
const todo = (id: string, title: string): Todo => ({ id, title, created_at: iso(7_200_000), updated_at: iso(3_600_000) });
const decision = (todoId: string, n: number, patch: Partial<TodoDecision> = {}): TodoDecision => ({
  id: `${todoId}-d${n}`, todo_id: todoId, question: `Question ${n}?`, choice: `Choice ${n}`, reason: "", source: "manual",
  source_machine: "", source_session: "", source_message: "", supersedes_id: "", superseded_by: "",
  created_at: iso(3_600_000 - n * 1000), ...patch,
});
// t1: twelve decisions, the first revised by the last, so eleven are in effect; t2: three
const lists: Record<string, TodoDecision[]> = {};

const seed = (patch: Record<string, unknown>) => Object.assign(getState() as unknown as Record<string, unknown>, patch);
const before = { ...getState() };
const open = async (id: string) => {
  await act(async () => root.render(<TodosView selectedId={id} />));
  await flush();
};

beforeEach(() => {
  api.mockReset();
  lists.t1 = Array.from({ length: 12 }, (_, i) => decision("t1", i + 1));
  lists.t1[0] = { ...lists.t1[0], superseded_by: "t1-d12" };
  lists.t1[11] = { ...lists.t1[11], supersedes_id: "t1-d1" };
  lists.t2 = Array.from({ length: 3 }, (_, i) => decision("t2", i + 1));
  lists.t3 = [];
  api.mockImplementation(async (path: string, init?: unknown) => {
    const m = /^\/todos\/([^/]+)\/decisions/.exec(path);
    if (m && (init as { method?: string } | undefined)?.method === "POST") {
      const body = (init as { json: { question: string; choice: string } }).json;
      const d = decision(m[1], lists[m[1]].length + 1, { question: body.question, choice: body.choice });
      lists[m[1]].push(d);
      return { decision: d };
    }
    if (m) return { decisions: lists[m[1]] ?? [] };
    return path.endsWith("/launchers") ? { launchers: [] } : {};
  });
  seed({ todos: [todo("t1", "Weather app release"), todo("t2", "Blog move"), todo("t3", "Backups")], todosLoaded: true, machines: [], sessions: {} });
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});
afterEach(async () => {
  await act(async () => root.unmount());
  host.remove();
  document.body.innerHTML = "";
  seed(before);
  vi.restoreAllMocks();
});

describe("a to-do's decisions", () => {
  it("show the first five, with Show all under them, and count every one in effect", async () => {
    await open("t1");
    expect(section().textContent).toContain(t("todos.decisions_section", { count: 11 }));
    expect(rows()).toBe(5);
    expect(section().textContent).toContain("Question 2?");
    expect(section().textContent).not.toContain("Question 7?");
    expect(more()?.textContent).toBe(t("todos.decisions_show_all", { count: 11 }));
    expect(more()?.getAttribute("aria-expanded")).toBe("false");
  });

  it("open whole with Show all, and go back to the first five", async () => {
    await open("t1");
    await click(more());
    expect(rows()).toBe(11);
    expect(more()?.textContent).toBe(t("todos.decisions_show_first", { count: 5 }));
    await click(more());
    expect(rows()).toBe(5);
  });

  it("fold away with the fold-all button and open whole again, the count and the hub untouched", async () => {
    await open("t1");
    const reads = decisionReads();
    expect(fold()?.getAttribute("aria-label")).toBe(t("todos.decisions_fold"));
    expect(tip()).toBe(t("todos.decisions_fold"));
    expect(shows(fold())).toBe(glyph("foldToggle"));

    await click(fold());
    expect(rows()).toBe(0);
    expect(more()).toBeNull();
    expect(section().textContent).not.toContain(t("todos.decisions_empty"));
    expect(section().textContent).toContain(t("todos.decisions_section", { count: 11 }));
    expect(fold()?.getAttribute("aria-label")).toBe(t("todos.decisions_expand"));
    expect(tip()).toBe(t("todos.decisions_expand"));
    expect(shows(fold())).toBe(glyph("unfoldToggle"));

    await click(fold());
    expect(rows()).toBe(11);
    expect(fold()?.getAttribute("aria-label")).toBe(t("todos.decisions_fold"));
    expect(shows(fold())).toBe(glyph("foldToggle"));
    expect(decisionReads()).toBe(reads);
  });

  it("fold from the first five straight away", async () => {
    await open("t1");
    await click(fold());
    expect(rows()).toBe(0);
  });

  it("show a short list whole, with no Show all, and still fold it", async () => {
    await open("t2");
    expect(rows()).toBe(3);
    expect(more()).toBeNull();
    await click(fold());
    expect(rows()).toBe(0);
  });

  it("offer no fold-all button while there is nothing to fold", async () => {
    await open("t3");
    expect(section().textContent).toContain(t("todos.decisions_empty"));
    expect(fold()).toBeNull();
  });

  it("start at the first five on another to-do, whatever the last one showed", async () => {
    await open("t1");
    await click(fold());
    await open("t2");
    await open("t1");
    expect(rows()).toBe(5);
  });

  it("show the whole list once one is recorded, so the new one is in view", async () => {
    await open("t1");
    const add = [...section().querySelectorAll<HTMLButtonElement>("button")].find((b) => b.textContent?.trim() === t("todos.add_decision"));
    await click(add);
    const dialog = document.querySelector<HTMLElement>("[role=dialog]")!;
    const type = async (id: string, value: string) => {
      const input = dialog.querySelector<HTMLInputElement>(`#${id}`)!;
      const set = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!;
      await act(async () => {
        set.call(input, value);
        input.dispatchEvent(new Event("input", { bubbles: true }));
      });
    };
    await type("decision-question", "A new question?");
    await type("decision-choice", "The new choice");
    await act(async () => dialog.querySelector("form")!.requestSubmit());
    await flush();
    expect(rows()).toBe(12);
    expect(section().textContent).toContain("A new question?");
  });
});
