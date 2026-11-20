// @vitest-environment jsdom
// #11677: Just finished and Done show the outcome in the row itself. A cancelled to-do keeps the strike-through and the
// grey title; an accepted, deployed or owner-ticked one keeps the muted title without the strike, so a finished row no
// longer reads as thrown away. An open row (a failed start waiting on the owner) is never struck. The hub is faked; the
// data is made up.
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

const { getState } = await import("../lib/store");
const { TodosView } = await import("./Todos");

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let host: HTMLDivElement;
let root: Root;
const flush = async () => {
  for (let i = 0; i < 6; i++) await act(async () => { await Promise.resolve(); });
};
const iso = (agoMs: number) => new Date(Date.now() - agoMs).toISOString();
const HOUR = 3_600_000;

const todos = [
  {
    id: "cancelled",
    title: "Cancelled work",
    created_at: iso(2 * HOUR),
    updated_at: iso(HOUR),
    done_at: iso(HOUR),
    done_by: "review",
    supervision: { supervisor: "conv", state: "cancelled" },
  },
  {
    id: "accepted",
    title: "Accepted work",
    created_at: iso(2 * HOUR),
    updated_at: iso(HOUR),
    done_at: iso(HOUR),
    done_by: "review",
    supervision: {
      supervisor: "conv",
      state: "accepted",
      reviews: [{ at: iso(HOUR), verdict: "accepted" }],
    },
  },
  {
    id: "deployed",
    title: "Deployed work",
    created_at: iso(2 * HOUR),
    updated_at: iso(HOUR),
    done_at: iso(HOUR),
    delivery: { state: "live", at: iso(HOUR) },
  },
  {
    id: "ticked",
    title: "Ticked by the owner",
    created_at: iso(2 * HOUR),
    updated_at: iso(HOUR),
    done_at: iso(HOUR),
  },
] as unknown as Todo[];

const seed = (patch: Record<string, unknown>) => Object.assign(getState() as unknown as Record<string, unknown>, patch);
const before = { ...getState() };

beforeEach(() => {
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
});

const render = async (list = todos) => {
  localStorage.setItem("persona.todos.grouping", "status");
  seed({ todos: list, todosLoaded: true, sessions: {}, interactions: [], answered: {} });
  await act(async () => root.render(<TodosView />));
  await flush();
};
const title = (id: string) => host.querySelector<HTMLElement>(`[data-todo=${id}] [data-testid=todo-title]`);

describe("a finished to-do's title (#11677)", () => {
  it("strikes a cancelled row through and leaves accepted, deployed and ticked rows plain", async () => {
    await render();

    expect(title("cancelled")?.className).toContain("line-through");
    for (const id of ["accepted", "deployed", "ticked"]) {
      expect(title(id)?.className).not.toContain("line-through");
    }
  });

  it("keeps every finished row muted and at normal weight, so the fill and tone still say it is closed", async () => {
    await render();

    for (const id of ["cancelled", "accepted", "deployed", "ticked"]) {
      const className = title(id)?.className ?? "";
      expect(className).toContain("text-ink-3");
      expect(className).toContain("font-normal");
    }
  });
});
