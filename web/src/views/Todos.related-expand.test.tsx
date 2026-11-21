// @vitest-environment jsdom
// #12417: expanding "N related" on a TodoTriageRow stays open and keeps member rows from jumping
// when live updates swap which member is most recently active (group.lead) or when a member is selected.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
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

const { getState, upsertSession } = await import("../lib/store");
const { TodosView } = await import("./Todos");

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let host: HTMLDivElement;
let root: Root;

const flush = async () => {
  for (let i = 0; i < 6; i++) await act(async () => { await Promise.resolve(); });
};

const MIN = 60_000;
const iso = (agoMs: number) => new Date(Date.now() - agoMs).toISOString();
const link = (sid: string) => ({ machine_id: "m1", session_id: sid, linked_at: iso(120 * MIN) });
const session = (sid: string, agoMs: number, lastText = "working"): Session =>
  ({ machine_id: "m1", session_id: sid, title: `agent ${sid}`, cwd: "/code/app", status: "running", state: "working", updated_at: iso(agoMs), last_text: lastText }) as Session;
const todo = (id: string, title: string, sid: string, agoMs: number): Todo =>
  ({ id, title, plan_id: "plan-412", origin: "conv", created_at: iso(180 * MIN), updated_at: iso(agoMs), done_at: null, agents: [link(sid)] }) as Todo;

const seed = (patch: Record<string, unknown>) => Object.assign(getState() as unknown as Record<string, unknown>, patch);
const before = { ...getState() };

const baseTodos = (): Todo[] => [
  todo("task-3", "Fix #412 timeout step 3", "s3", 10 * MIN),
  todo("task-5", "Fix #412 timeout step 5", "s5", 20 * MIN),
  todo("task-1", "Fix #412 timeout step 1", "s1", 30 * MIN),
];

const baseSessions = () => ({
  m1: [
    session("s3", 10 * MIN, "step 3 running"),
    session("s5", 20 * MIN, "step 5 running"),
    session("s1", 30 * MIN, "step 1 running"),
  ],
});

beforeEach(() => {
  api.mockReset();
  api.mockResolvedValue({});
  localStorage.clear();
  window.history.replaceState(null, "", "/todos");
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
});

const triageRow = () => host.querySelector<HTMLElement>("[data-testid=todo-triage-row]")!;
const relatedToggle = () => triageRow().querySelector<HTMLButtonElement>("button[aria-expanded]")!;
const renderedIds = () => [...triageRow().querySelectorAll<HTMLElement>("[data-todo]")].map((el) => el.dataset.todo);

describe("TodoTriageRow related group stability (#12417)", () => {
  it("stays expanded without jumping member rows when live updates swap group.lead, and updates lead on collapse", async () => {
    seed({ todos: baseTodos(), todosLoaded: true, todosSupported: true, sessions: baseSessions(), interactions: [], answered: {} });
    await act(async () => root.render(<TodosView />));
    await flush();

    // Initially collapsed with task-3 (newest) as lead and stable group key task-1
    expect(triageRow().dataset.groupKey).toBe("task-1");
    expect(relatedToggle().getAttribute("aria-expanded")).toBe("false");
    expect(renderedIds()).toEqual(["task-3"]);

    // Expand "2 related"
    await act(async () => relatedToggle().click());
    await flush();
    expect(relatedToggle().getAttribute("aria-expanded")).toBe("true");
    expect(renderedIds()).toEqual(["task-3", "task-5", "task-1"]);

    // Live update: task-5 becomes most recently active (swapping group.lead from task-3 -> task-5)
    await act(async () => upsertSession(session("s5", 2 * MIN, "step 5 tick")));
    await flush();
    expect(triageRow().dataset.groupKey).toBe("task-1");
    expect(relatedToggle().getAttribute("aria-expanded")).toBe("true");
    expect(renderedIds()).toEqual(["task-3", "task-5", "task-1"]);

    // Live update: task-1 becomes most recently active (swapping group.lead from task-5 -> task-1)
    await act(async () => upsertSession(session("s1", 1 * MIN, "step 1 tick")));
    await flush();
    expect(relatedToggle().getAttribute("aria-expanded")).toBe("true");
    expect(renderedIds()).toEqual(["task-3", "task-5", "task-1"]);

    // Explicit collapse restores canonical lead (task-1)
    await act(async () => relatedToggle().click());
    await flush();
    expect(relatedToggle().getAttribute("aria-expanded")).toBe("false");
    expect(renderedIds()).toEqual(["task-1"]);
  });

  it("keeps the group expanded without jumping when clicking a member that becomes lead", async () => {
    seed({ todos: baseTodos(), todosLoaded: true, todosSupported: true, sessions: baseSessions(), interactions: [], answered: {} });
    await act(async () => root.render(<TodosView />));
    await flush();

    // Expand and click task-5 inside the related list
    await act(async () => relatedToggle().click());
    await flush();
    await act(async () => triageRow().querySelector<HTMLElement>("[data-todo=task-5]")!.click());
    await flush();
    expect(relatedToggle().getAttribute("aria-expanded")).toBe("true");
    expect(renderedIds()).toEqual(["task-3", "task-5", "task-1"]);
    expect(triageRow().querySelector<HTMLElement>("[data-todo=task-5]")?.getAttribute("aria-current")).toBe("true");

    // task-5 gets a newer tick and becomes group.lead: group must remain expanded without collapsing or jumping
    await act(async () => upsertSession(session("s5", 1 * MIN, "step 5 active")));
    await flush();
    expect(relatedToggle().getAttribute("aria-expanded")).toBe("true");
    expect(renderedIds()).toEqual(["task-3", "task-5", "task-1"]);
    expect(triageRow().querySelector<HTMLElement>("[data-todo=task-5]")?.getAttribute("aria-current")).toBe("true");
  });

  it("stays expanded when a deep-linked non-lead member becomes lead, while still allowing manual collapse", async () => {
    seed({ todos: baseTodos(), todosLoaded: true, todosSupported: true, sessions: baseSessions(), interactions: [], answered: {} });
    await act(async () => root.render(<TodosView selectedId="task-5" />));
    await flush();

    // Auto-expanded because task-5 is not the initial lead
    expect(relatedToggle().getAttribute("aria-expanded")).toBe("true");
    expect(renderedIds()).toEqual(["task-3", "task-5", "task-1"]);

    // task-5 becomes group.lead: must not snap shut
    await act(async () => upsertSession(session("s5", 1 * MIN, "step 5 promoted")));
    await flush();
    expect(relatedToggle().getAttribute("aria-expanded")).toBe("true");
    expect(renderedIds()).toEqual(["task-3", "task-5", "task-1"]);

    // Manual collapse still works cleanly
    await act(async () => relatedToggle().click());
    await flush();
    expect(relatedToggle().getAttribute("aria-expanded")).toBe("false");
    expect(renderedIds()).toEqual(["task-5"]);
  });
});
