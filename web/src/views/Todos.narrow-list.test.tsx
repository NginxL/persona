// @vitest-environment jsdom
// #11442: at a phone's width the list and a to-do's page take turns, and nothing is open until a row is. No row is marked
// current and no to-do's page loads unseen behind the list (its repositories and decisions are read only for one opened),
// also while a search changes the first row. A link, a row and the page's Hand to agent still reach their to-do; side by
// side the first row still stands in. The hub is faked; the data made up.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Todo } from "../lib/todos";

// the window's width, as the media queries read it (a phone's unless a case sets it)
const screen = vi.hoisted(() => ({ width: 390 }));
vi.hoisted(() => {
  const px = (q: string, k: string) => Number(new RegExp(`\\(${k}-width:\\s*(\\d+)px\\)`).exec(q)?.[1] ?? NaN);
  window.matchMedia = ((query: string) => ({
    get matches() {
      const max = px(query, "max"), min = px(query, "min");
      return Number.isFinite(max) ? screen.width <= max : Number.isFinite(min) ? screen.width >= min : false;
    },
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
const { navigate, useRoute } = await import("../lib/router");
const { afterBack, onOverlayEntry } = await import("../lib/back-close");
const { TodosView } = await import("./Todos");
const { t } = await import("../i18n");

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let host: HTMLDivElement;
let root: Root;
const settle = async () => {
  for (let i = 0; i < 4; i++) await act(async () => { await new Promise((r) => setTimeout(r, 0)); });
};
const iso = (agoMs: number) => new Date(Date.now() - agoMs).toISOString();
const todo = (id: string, title: string): Todo => ({ id, title, created_at: iso(7_200_000), updated_at: iso(3_600_000), agents: [] }) as unknown as Todo;
const todos = [todo("a", "Book the dentist"), todo("b", "Renew the passport"), todo("c", "Water the plants"), todo("d", "Pay the rent")];
const seed = (patch: Record<string, unknown>) => Object.assign(getState() as unknown as Record<string, unknown>, patch);
const before = { ...getState() };

function Page() {
  const r = useRoute();
  return <TodosView selectedId={r.name === "todos" ? r.id : undefined} />;
}

const mount = async (at: string) => {
  history.replaceState(null, "", at);
  await act(async () => root.render(<Page />));
  await settle();
};
const row = (id: string) => host.querySelector<HTMLElement>(`[data-todo="${id}"]`)!;
const current = () => [...host.querySelectorAll<HTMLElement>("[data-todo][aria-current]")].map((el) => el.dataset.todo);
// the to-dos whose page asked the hub for its repositories or decisions
const loaded = () => [...new Set(api.mock.calls.map(([path]) => /^\/todos\/([^/?]+)\/(?:repositories|decisions)/.exec(path)?.[1]).filter(Boolean))];
const detail = () => host.querySelector("[data-testid=todos-detail-column]")!;
const search = async (text: string) => {
  const input = host.querySelector<HTMLInputElement>("input[type=search], input[placeholder]")!;
  const set = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!;
  for (let i = 1; i <= text.length; i++) {
    await act(async () => {
      set.call(input, text.slice(0, i));
      input.dispatchEvent(new Event("input", { bubbles: true }));
    });
  }
  await settle();
};
const button = (label: string) => [...document.querySelectorAll<HTMLElement>("button")].find((b) => b.textContent?.trim() === label);

beforeEach(() => {
  screen.width = 390;
  localStorage.clear();
  api.mockReset();
  api.mockImplementation(async (path: string) => (path.endsWith("/launchers") ? { launchers: [] } : {}));
  seed({ todos, todosLoaded: true, todosError: null, sessions: {}, interactions: [], answered: {}, machines: [] });
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});
afterEach(async () => {
  // a sheet closed by the unmount goes Back off its history entry later, by a popstate a loaded machine can delay past
  // any fixed wait: in this case, not in the next one's (it would put that one at the address under the sheet)
  const sheet = onOverlayEntry() ? new Promise<boolean>((done) => afterBack(done, 3000)) : undefined;
  await act(async () => root.unmount());
  if (sheet) await sheet;
  await settle();
  host.remove();
  document.body.innerHTML = "";
  history.replaceState(null, "", "/");
  seed(before);
  localStorage.clear();
});

describe("the to-do list at a phone's width", () => {
  it("marks no row current and loads no to-do's page until one is opened", async () => {
    await mount("/todos");
    expect(row("a")).toBeTruthy();
    expect(current()).toEqual([]);
    expect(loaded()).toEqual([]);
    expect(detail().querySelector("input")).toBeNull();
  });

  it("loads none while a search changes the first row", async () => {
    await mount("/todos");
    await search("the p");
    expect(row("a")).toBeNull();
    expect(row("b")).toBeTruthy();
    await search("rent");
    expect(current()).toEqual([]);
    expect(loaded()).toEqual([]);
  });

  it("opens the tapped to-do, loading only its page, and keeps no page behind the list once back", async () => {
    await mount("/todos");
    await act(async () => row("c").click());
    await settle();
    expect(location.search).toBe("?id=c");
    expect(detail().querySelector<HTMLInputElement>("input[type=text]")?.value).toBe("Water the plants");
    expect(loaded()).toEqual(["c"]);
    await act(async () => host.querySelector<HTMLElement>("[data-testid=todos-back] button")!.click());
    await settle();
    expect(location.search).toBe("");
    expect(detail().querySelector("input")).toBeNull();
  });

  it("opens a link's to-do after Back from another, without the one left coming back first", async () => {
    await mount("/todos");
    await act(async () => row("a").click());
    await settle();
    await act(async () => host.querySelector<HTMLElement>("[data-testid=todos-back] button")!.click());
    await settle();
    expect(location.search).toBe("");
    expect(current()).toEqual([]);
    const readsOfA = () => api.mock.calls.filter(([path]) => path.startsWith("/todos/a/")).length;
    const before = readsOfA();
    await act(async () => navigate("/todos?id=b"));
    await settle();
    expect(detail().querySelector<HTMLInputElement>("input[type=text]")?.value).toBe("Renew the passport");
    expect(loaded()).toEqual(["a", "b"]);
    expect(readsOfA()).toBe(before);
  });

  it("opens a link's to-do, and its Hand to agent is for that one", async () => {
    await mount("/todos?id=b");
    expect(loaded()).toEqual(["b"]);
    await act(async () => button(t("todos.hand_over"))!.click());
    await settle();
    expect(document.querySelector("[role=dialog]")?.textContent).toContain("Renew the passport");
  });
});

describe("side by side", () => {
  it("still shows the first row's page beside the list", async () => {
    screen.width = 1280;
    await mount("/todos");
    expect(current()).toEqual(["a"]);
    // its page reads them once it has drawn, which a loaded machine can take past settle()
    await vi.waitFor(() => expect(loaded()).toEqual(["a"]), { timeout: 5000 });
  });
});
