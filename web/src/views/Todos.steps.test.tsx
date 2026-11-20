// @vitest-environment jsdom
// A to-do split into steps on the To-dos page (#11858): its row and its page say the hub's count of its steps instead
// of "Not started · Not handed to an agent yet": "Steps 1/3" while some are open, "3/3 steps done · Next step not
// arranged yet" once all are closed, "· Steps 1/2" after the Agent count of one its own Agent is on. A row without the
// hub's view, or without steps, says what it said before. The hub is faked; the data made up.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { TodoSummary } from "../lib/todo-view-wire";
import type { Todo } from "../lib/todos";
import type { Session } from "../lib/types";

vi.hoisted(() => {
  globalThis.ResizeObserver ??= class { observe() {} unobserve() {} disconnect() {} } as unknown as typeof ResizeObserver;
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

const { getState } = await import("../lib/store");
const { TodosView } = await import("./Todos");
const { initI18n, t } = await import("../i18n");

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let host: HTMLDivElement;
let root: Root;
const flush = async () => {
  for (let i = 0; i < 6; i++) await act(async () => { await Promise.resolve(); });
};
const iso = (agoMs: number) => new Date(Date.now() - agoMs).toISOString();
const HOUR = 3_600_000;
const view = (steps: { done: number; total: number }): TodoSummary => ({ phase: "todo", section: "todo", reason: "rest", rank: 0, steps });
const link = (sid: string) => ({ machine_id: "mac", session_id: sid, linked_at: iso(2 * HOUR) });
const agent = (sid: string): Session => ({ machine_id: "mac", session_id: sid, title: `agent ${sid}`, cwd: "/code/a", status: "working", state: "working", updated_at: iso(HOUR) }) as Session;
const todo = (id: string, title: string, patch: Partial<Todo> = {}): Todo => ({ id, title, created_at: iso(5 * HOUR), updated_at: iso(HOUR), ...patch }) as Todo;

const todos: Todo[] = [
  // every step closed, nothing arranged next
  todo("done-goal", "Move the photo library", { view: view({ done: 3, total: 3 }) }),
  ...[1, 2, 3].map((n) => todo(`d${n}`, `Photo step ${n}`, { part_of: "done-goal", quiet: true, origin: "conv", done_at: iso(3 * HOUR) })),
  // one of three closed, one being done
  todo("busy-goal", "Ship the settings page", { view: view({ done: 1, total: 3 }) }),
  todo("b1", "Settings: search", { part_of: "busy-goal", origin: "conv", agents: [link("b")] }),
  // its own Agent works on it, and it has steps
  todo("own", "Draft the newsletter", { origin: "conv", agents: [link("o")], view: view({ done: 1, total: 2 }) }),
  // its supervisor accepted it and nothing runs: it reads Accepted, its steps after that
  todo("accepted-goal", "Archive last year's invoices", { view: view({ done: 2, total: 2 }), supervision: { state: "accepted", reviews: [{ at: iso(2 * HOUR), verdict: "accepted" }] } as unknown as Todo["supervision"] }),
  ...[1, 2].map((n) => todo(`a${n}`, `Invoice step ${n}`, { part_of: "accepted-goal", quiet: true, origin: "conv", done_at: iso(3 * HOUR) })),
  // the owner's errand: no steps, no view
  todo("errand", "Replace the disk in the home server"),
];
const seed = (patch: Record<string, unknown>) => Object.assign(getState() as unknown as Record<string, unknown>, patch);
const before = { ...getState() };

beforeEach(async () => {
  await initI18n("en");
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
  seed(before);
  await initI18n("zh-Hans");
});

const render = async (selectedId?: string) => {
  localStorage.setItem("persona.todos.grouping", "status");
  seed({ todos, todosLoaded: true, todosSupported: true, sessions: { mac: [agent("b"), agent("o")] }, interactions: [], answered: {} });
  await act(async () => root.render(<TodosView selectedId={selectedId} />));
  await flush();
};
const line = (id: string) => host.querySelector<HTMLElement>(`[data-todo="${id}"] [data-testid=todo-status-line]`)!.textContent;

describe("a to-do split into steps on the To-dos page (#11858)", () => {
  it("says its steps on its row instead of Not started", async () => {
    await render();
    expect(line("done-goal")).toBe("3/3 steps done· Next step not arranged yet");
    expect(line("busy-goal")).toBe("Steps 1/3");
    // its status word stays (the state names' own), the steps after its Agent count
    expect(line("own")?.endsWith(`·${t("todos.agent_count", { count: 1 })}·Steps 1/2`)).toBe(true);
    // no view, no steps: as before
    expect(line("errand")?.endsWith(`· ${t("todos.not_handed")}`)).toBe(true);
    expect(line("errand")).not.toContain("Steps");
  });

  it("keeps Accepted on a to-do its supervisor accepted, its steps after it", async () => {
    await render();
    const accepted = line("accepted-goal")!;
    expect(accepted.startsWith(t("lib-todos.review_done"))).toBe(true);
    // its steps after its status word; its steps were handed out, so nothing says it was not
    expect(accepted).toContain(`${t("lib-todos.review_done")}·Steps 2/2`);
    expect(accepted).not.toContain(t("todos.not_handed"));
    expect(accepted).not.toContain(t("todos.steps_next_unplanned"));
    expect(accepted).not.toContain("2/2 steps done");
    await act(async () => root.unmount());
    root = createRoot(host);
    await render("accepted-goal");
    const page = host.querySelector<HTMLElement>("[data-testid=todo-detail]")!;
    expect(page.querySelector("[data-testid=todo-detail-status]")?.textContent).toBe(t("lib-todos.review_done"));
    expect(page.querySelector("[data-testid=todo-detail-steps]")?.textContent).toBe("Steps 2/2");
    expect(page.querySelector("[data-testid=todo-detail-steps-next]")).toBeNull();
  });

  it("says the same on its page", async () => {
    await render("done-goal");
    const page = host.querySelector<HTMLElement>("[data-testid=todo-detail]")!;
    expect(page.querySelector("[data-testid=todo-detail-status]")?.textContent).toBe("3/3 steps done");
    expect(page.querySelector("[data-testid=todo-detail-steps-next]")?.textContent).toBe("Next step not arranged yet");
    await act(async () => root.unmount());
    root = createRoot(host);
    await render("own");
    const own = host.querySelector<HTMLElement>("[data-testid=todo-detail]")!;
    expect(own.querySelector("[data-testid=todo-detail-steps]")?.textContent).toBe("Steps 1/2");
    expect(own.querySelector("[data-testid=todo-detail-steps-next]")).toBeNull();
  });
});
