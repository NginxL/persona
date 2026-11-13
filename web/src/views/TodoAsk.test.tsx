// @vitest-environment jsdom
// What a to-do waits on from the user (#2056, TodoAsk.tsx): the to-do's row, its page and the main chat's task card show
// the ask the hub put on it (GET /todos `ask`), and one tap answers it with the round it was made for.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { ApiError } from "../lib/api";
import { getState } from "../lib/store";
import { boardSectionOf } from "../lib/task-board";
import { askTake, askTone, awaitsReview, openAsk, sortTodos, type Todo, type TodoAsk } from "../lib/todos";
import { getFeedState, setRoute } from "../lib/feed";

vi.hoisted(() => {
  const g = globalThis as Record<string, unknown>;
  g.matchMedia = () => ({ matches: false, addEventListener() {}, removeEventListener() {} });
  const kept = new Map<string, string>();
  g.localStorage = { getItem: (k: string) => kept.get(k) ?? null, setItem: (k: string, v: string) => void kept.set(k, v), removeItem: (k: string) => void kept.delete(k) };
});

const api = vi.hoisted(() => vi.fn());
vi.mock("../lib/api", async (orig) => ({ ...(await orig<typeof import("../lib/api")>()), api }));
const navigate = vi.hoisted(() => vi.fn());
vi.mock("../lib/router", async (orig) => ({ ...(await orig<typeof import("../lib/router")>()), navigate }));
const toast = vi.hoisted(() => vi.fn());
vi.mock("../ui/atoms", async (orig) => ({ ...(await orig<typeof import("../ui/atoms")>()), toast }));

const { TodosView } = await import("./Todos");

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const ASKED = "2026-11-09T06:30:00.123456789Z";
const now = new Date().toISOString();
const decide: TodoAsk = { outcome: "decide", verdict: "🤔 需要你决定：两个方案各有取舍", recommend: "先发预发环境", asked_at: ASKED };
const todo = (o: Partial<Todo> = {}): Todo => ({ id: "t1", title: "上线新登录页", created_at: now, updated_at: now, origin: "conv1", ask: decide, ...o });

const seed = (patch: Record<string, unknown>) => Object.assign(getState() as unknown as Record<string, unknown>, patch);
const before = { ...getState() };

let root: Root;
let host: HTMLDivElement;
// what GET /todos answers after a take: the to-do with nothing waiting
let listed: Todo[] = [];
beforeEach(() => {
  api.mockReset();
  navigate.mockReset();
  toast.mockReset();
  listed = [todo({ ask: undefined })];
  api.mockImplementation(async (path: string) => (path === "/todos" ? { todos: listed } : {}));
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});
afterEach(() => {
  act(() => root.unmount());
  host.remove();
  seed(before);
});

const flush = () => act(async () => {
  for (let i = 0; i < 5; i++) await Promise.resolve();
});
const drawList = async (todos: Todo[], selectedId?: string) => {
  seed({ todos, todosLoaded: true, sessions: {}, interactions: [] });
  act(() => root.render(<TodosView selectedId={selectedId} />));
  await flush();
};
const askRow = () => host.querySelector<HTMLElement>("[data-testid=todo-ask]");
const askButton = (within: ParentNode = host) => within.querySelector<HTMLButtonElement>("[data-testid=todo-ask] button");
const accepts = () => api.mock.calls.filter(([path]) => String(path).endsWith("/accept"));

describe("an ask, as the hub sends it", () => {
  it("is open only with a verdict and a round", () => {
    expect(openAsk(todo())).toEqual(decide);
    expect(openAsk(todo({ ask: undefined }))).toBeUndefined();
    expect(openAsk(todo({ ask: { verdict: "", asked_at: ASKED } }))).toBeUndefined();
    expect(openAsk(todo({ ask: { verdict: "✅ 已上线", asked_at: "" } }))).toBeUndefined();
  });

  it("takes a decision's recommendation, acknowledges a conclusion, accepts a deployed to-do", () => {
    expect(askTake(decide)).toBe("take");
    expect(askTake({ ...decide, recommend: undefined })).toBeUndefined(); // nothing for one tap to take
    expect(askTake({ outcome: "cannot", verdict: "❌ 做不了", asked_at: ASKED })).toBe("ack");
    expect(askTake({ outcome: "done", verdict: "✅ 做完了", asked_at: ASKED })).toBe("ack");
    expect(askTake({ verdict: "✅ 做完了，已上线", asked_at: ASKED })).toBe("accept");
    expect([decide, { ...decide, outcome: "cannot" }, { ...decide, outcome: undefined }].map(askTone)).toEqual(["attention", "red", "green"]);
  });

  it("puts a stopped to-do that waits on the user with Awaiting review, ahead of what runs", () => {
    const asked = todo();
    expect(awaitsReview(asked, "follow_up")).toBe(true);
    expect(awaitsReview(asked, "working")).toBe(false); // its agent is at it again
    expect(awaitsReview(todo({ ask: undefined }), "follow_up")).toBe(false);
    expect(boardSectionOf({ todo: asked, status: "follow_up" })).toBe("review");
    const working = todo({ id: "t2", ask: undefined, created_at: new Date(Date.now() + 1000).toISOString(), agents: [{ machine_id: "m1", session_id: "s1", linked_at: now }] });
    // its agent working (t2), and the asked one's stopped and seen (t1)
    const lookup = (_m: string, s: string) => ({ machine_id: "m1", session_id: s, status: s === "s1" ? "working" : "idle", state: s === "s1" ? "working" : "idle" }) as never;
    expect(sortTodos([working, { ...asked, agents: [{ machine_id: "m1", session_id: "s0", linked_at: now }] }], lookup).map((t) => t.id)).toEqual(["t1", "t2"]);
  });
});

describe("a to-do's row", () => {
  it("says what it waits on and the recommendation, with OK", async () => {
    await drawList([todo()]);
    const row = askRow()!;
    expect(row.dataset.ask).toBe("decide");
    expect(row.textContent).toContain("需要你决定：两个方案各有取舍");
    expect(row.textContent).toContain("推荐：先发预发环境");
    expect(askButton()!.textContent).toBe("就这样");
    expect(askButton()!.getAttribute("aria-label")).toContain("上线新登录页");
  });

  it("OK takes it with its round, without opening the to-do", async () => {
    await drawList([todo()]);
    act(() => askButton()!.click());
    await flush();
    expect(accepts()).toEqual([["/todos/t1/accept", { method: "POST", json: { asked_at: ASKED } }]]);
    expect(host.querySelector("[data-testid=todo-ask-panel]")).toBeNull();
    expect(api.mock.calls.some(([path]) => path === "/todos")).toBe(true); // read again: the ask is gone
    expect(askRow()).toBeNull();
    expect(toast).toHaveBeenCalledWith("已按推荐来", "ok");
  });

  it("an earlier round already answered says so and reads the to-dos again", async () => {
    listed = [todo()]; // the hub still lists it until the read comes back
    api.mockImplementation(async (path: string) => {
      if (path.endsWith("/accept")) throw new ApiError(409, "already handled", {});
      return path === "/todos" ? { todos: listed } : {};
    });
    await drawList([todo()]);
    act(() => askButton()!.click());
    await flush();
    expect(host.querySelector("[data-ask-done=handled]")?.textContent).toBe("已经处理过了");
    expect(api.mock.calls.some(([path]) => path === "/todos")).toBe(true);
  });

  it("a recommendation that did not reach the agent leaves OK to try again", async () => {
    api.mockImplementation(async (path: string) => {
      if (path.endsWith("/accept")) throw new ApiError(502, "推荐的方案没送到 Agent：电脑离线", {});
      return {};
    });
    await drawList([todo()]);
    act(() => askButton()!.click());
    await flush();
    expect(askButton()!.disabled).toBe(false);
    expect(toast).toHaveBeenCalledWith(expect.stringContaining("推荐的方案没送到 Agent：电脑离线"), "error");
  });

  it("a newer round offers OK again after the last one was handled", async () => {
    api.mockImplementation(async (path: string) => {
      if (path.endsWith("/accept")) throw new ApiError(409, "already handled", {});
      return path === "/todos" ? { todos: [todo()] } : {};
    });
    await drawList([todo()]);
    act(() => askButton()!.click());
    await flush();
    expect(askButton()).toBeNull();
    seed({ todos: [todo({ ask: { ...decide, asked_at: "2026-11-09T09:00:00Z", recommend: "直接上线" } })] });
    act(() => root.render(<TodosView />));
    await flush();
    expect(askButton()?.textContent).toBe("就这样");
    expect(askRow()!.textContent).toContain("推荐：直接上线");
  });

  it("a decision with nothing recommended has no button; a conclusion has Got it, a deployed to-do Accept", async () => {
    await drawList([
      // distinct titles: to-dos with the same title share one row (#3156)
      todo({ ask: { ...decide, recommend: undefined } }),
      todo({ id: "t2", title: "接入开放接口", ask: { outcome: "cannot", verdict: "❌ 做不了：接口不开放", asked_at: ASKED } }),
      todo({ id: "t3", title: "上线新版本", ask: { verdict: "✅ 做完了，已上线", asked_at: ASKED } }),
    ]);
    const byOutcome = Object.fromEntries([...host.querySelectorAll<HTMLElement>("[data-testid=todo-ask]")].map((el) => [el.dataset.ask, el]));
    expect(askButton(byOutcome.decide)).toBeNull();
    expect(askButton(byOutcome.cannot)!.textContent).toBe("知道了");
    expect(askButton(byOutcome.deployed)!.textContent).toBe("通过");
  });
});

describe("a to-do's page", () => {
  it("lists parked work outside Done and counts it under the badge's name", async () => {
    await drawList([todo({
      seen_at: new Date(Date.now() + 1000).toISOString(),
      ask: { outcome: "done", verdict: "The previous turn ended.", asked_at: now },
      supervision: {
        supervisor: "conv1", state: "parked",
        parked: { reason: "Waiting on another team", waiting_on: "someone_else", until: new Date(Date.now() + 86400_000).toISOString(), count: 1 },
      },
    })]);
    expect(host.querySelector('[data-section="parked"]')?.textContent).toContain("上线新登录页");
    expect(host.querySelector('[data-section="done"]')?.textContent ?? "").not.toContain("上线新登录页");
    expect(host.querySelector("header")?.textContent).toContain("已搁置 1");
    expect(host.querySelector("header")?.textContent).toContain("暂无进展 0");
  });

  it("shows the ask in full with its button and the conversation to answer it in", async () => {
    await drawList([todo()], "t1");
    const panel = host.querySelector<HTMLElement>("[data-testid=todo-ask-panel]")!;
    expect(panel.textContent).toContain("需要你"); // the state name (lib/state-labels.ts)
    expect(panel.textContent).toContain("推荐：先发预发环境");
    const buttons = [...panel.querySelectorAll("button")].map((b) => b.textContent);
    expect(buttons).toEqual(["按推荐来", "去对话里回答"]);
    act(() => [...panel.querySelectorAll("button")][1].click());
    expect(navigate).toHaveBeenCalledWith({ name: "persona", sid: "conv1" });
    expect(getFeedState().routes.conv1).toEqual({ todoId: "t1", askedAt: ASKED });
    setRoute("conv1", null);
  });

  it("a decision with nothing recommended says where to answer it", async () => {
    await drawList([todo({ ask: { ...decide, recommend: undefined } })], "t1");
    const panel = host.querySelector<HTMLElement>("[data-testid=todo-ask-panel]")!;
    expect(panel.textContent).toContain("没有推荐方案");
    expect([...panel.querySelectorAll("button")].map((b) => b.textContent)).toEqual(["去对话里回答"]);
  });

  it("shows nothing while the to-do waits on nothing", async () => {
    await drawList([todo({ ask: undefined })], "t1");
    expect(host.querySelector("[data-testid=todo-ask-panel]")).toBeNull();
  });
});
