// @vitest-environment jsdom
// Developing Persona's view switch (#10226, features/todos/model): the To-dos sections come from the hub's view only with
// all three gates on, a row without a view keeps today's place, and with any gate off the page draws exactly as before.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Todo } from "../lib/todos";
import type { TodoSummary } from "../lib/todo-view-wire";

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

const { getState, reset } = await import("../lib/store");
const { TodosView } = await import("./Todos");
const { DEV_MODE_KEY } = await import("../lib/dev-probe");
const { VIEW_DRAW_KEY } = await import("../features/todos/model/view-gates");
(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const NOW = Date.parse("2026-11-16T12:00:00.000Z");
const HOUR = 3_600_000;
const iso = (agoMs: number) => new Date(NOW - agoMs).toISOString();

let host: HTMLDivElement;
let root: Root;
const flush = async () => {
  for (let i = 0; i < 6; i++) await act(async () => { await Promise.resolve(); });
};
const seed = (patch: Record<string, unknown>) => Object.assign(getState() as unknown as Record<string, unknown>, patch);

beforeEach(() => {
  reset();
  vi.useFakeTimers({ toFake: ["Date"] });
  vi.setSystemTime(NOW);
  api.mockReset();
  api.mockImplementation(async () => ({}));
  localStorage.clear();
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});
afterEach(async () => {
  await act(async () => root.unmount());
  host.remove();
  document.body.innerHTML = "";
  localStorage.clear();
  reset();
  vi.useRealTimers();
});

const render = async (todos: Todo[], features = ["todo_view"]) => {
  localStorage.setItem("persona.todos.grouping", "status");
  seed({ todos, todosLoaded: true, sessions: {}, interactions: [], answered: {}, features });
  await act(async () => root.render(<TodosView />));
  await flush();
};
const sections = () => host.querySelector<HTMLElement>('[data-testid="todos-sections"]');
const placed = () =>
  Object.fromEntries(
    [...host.querySelectorAll<HTMLElement>('[data-testid="todo-section"]')].map((s) => [s.dataset.section, [...s.querySelectorAll<HTMLElement>("[data-todo]")].map((r) => r.dataset.todo)]),
  );
const view = (section: string, reason: string, phase: string): TodoSummary => ({ phase, section, reason, rank: 0 });
const task = (id: string, fields: Partial<Todo> = {}): Todo => ({ id, title: `Errand ${id}`, created_at: iso(5 * HOUR), updated_at: iso(4 * HOUR), ...fields });

// Today's triage files all three in Other open; the hub's view puts the first two elsewhere.
const plain = [task("todo_ask"), task("todo_run"), task("todo_old")];
const withViews = [
  { ...plain[0], view: view("needs_you", "owner_decides", "needs_you") },
  { ...plain[1], view: view("working", "agent_running", "working") },
  plain[2], // an older manager's row: no view
];

describe("To-dos sections from the hub's view", () => {
  it("draws from the view with all three switches on, and today's place for a row without one", async () => {
    localStorage.setItem(DEV_MODE_KEY, iso(HOUR));
    localStorage.setItem(VIEW_DRAW_KEY, "1");
    await render(withViews);
    expect(sections()?.dataset.todoSource).toBe("view");
    expect(placed()).toEqual({ waiting: ["todo_ask"], working: ["todo_run"], other: ["todo_old"] });
  });

  for (const [name, off] of [
    ["the Developing Persona switch", () => localStorage.setItem(VIEW_DRAW_KEY, "1")],
    ["the hub's todo_view", () => (localStorage.setItem(DEV_MODE_KEY, iso(HOUR)), localStorage.setItem(VIEW_DRAW_KEY, "1"))],
    ["this browser's view switch", () => localStorage.setItem(DEV_MODE_KEY, iso(HOUR))],
  ] as const) {
    it(`draws exactly as before without ${name}`, async () => {
      const features = name === "the hub's todo_view" ? ["todo_seen"] : ["todo_view"];
      await render(plain, features);
      const before = sections()?.outerHTML;
      expect(placed()).toEqual({ other: ["todo_ask", "todo_old", "todo_run"] });
      await act(async () => root.unmount());
      root = createRoot(host);
      off();
      await render(withViews, features);
      expect(sections()?.dataset.todoSource).toBeUndefined();
      expect(sections()?.outerHTML).toBe(before);
    });
  }
});
