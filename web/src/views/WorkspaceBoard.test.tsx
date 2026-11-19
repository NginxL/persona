// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { renderToStaticMarkup } from "react-dom/server";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
vi.hoisted(() => {
  globalThis.ResizeObserver ??= class { observe() {} unobserve() {} disconnect() {} } as unknown as typeof ResizeObserver;
  window.matchMedia ??= ((query: string) => ({ matches: false, media: query, onchange: null, addEventListener() {}, removeEventListener() {}, addListener() {}, removeListener() {}, dispatchEvent: () => false })) as typeof window.matchMedia;
});
// The Agents the board sees, as an array that counts every element read; and every render of a card, counted by the one
// t() call a card makes for its menu label.
const live = vi.hoisted(() => ({ sessions: [] as unknown[], reads: 0, cardRenders: 0, listeners: new Set<() => void>() }));
vi.mock("../lib/sessions", async (load) => {
  const { useSyncExternalStore } = await import("react");
  const subscribe = (fn: () => void) => { live.listeners.add(fn); return () => { live.listeners.delete(fn); }; };
  return { ...(await load<typeof import("../lib/sessions")>()), useAgentSessions: () => useSyncExternalStore(subscribe, () => live.sessions) };
});
vi.mock("../i18n", async (load) => {
  const real = await load<typeof import("../i18n")>();
  return { ...real, t: (key: string, vars?: Parameters<typeof real.t>[1]) => { if (key === "workspaceboard.card_menu") live.cardRenders++; return real.t(key, vars); } };
});
import { refresh, reset } from "../lib/store";
import { setRosterDefault } from "../lib/manager-choice";
import { resetWorkspaceBoardSettingForTest } from "../lib/workspace-board-setting";
import type { BoardCard } from "../lib/workspace-board";
import type { Machine, Session } from "../lib/types";
import type { Todo } from "../lib/todos";
import { WorkspaceBoard, WorkspaceCard } from "./WorkspaceBoard";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const card: BoardCard = { id: "card", machine_id: "computer", workspace_id: "space", title: "Project", cwd: "/projects/demo", source: "directory", column_id: "review", pinned: true, archived: false };
const draw = (offline = false, disabled = false) => renderToStaticMarkup(<WorkspaceCard card={card} machine={{ id: "computer", name: "Computer", online: !offline }} sessions={[]} disabled={disabled} onOpen={() => {}} onMenu={() => {}} />);
describe("WorkspaceCard", () => {
  it("renders a genuine workspace card without any running Agent", () => { const html = draw(); expect(html).toContain("Project"); expect(html).toContain("/projects/demo"); expect(html).toContain("暂无 Agent"); expect(html).toContain('data-card-id="card"'); expect(html).toContain('draggable="true"'); });
  it("keeps offline, agent-free cards visible and makes readonly cards non-draggable", () => { const html = draw(true, true); expect(html).toContain("Project"); expect(html).toContain("机器离线"); expect(html).toContain('draggable="false"'); expect(html).toContain("disabled"); expect(card.column_id).toBe("review"); });
});

// 60 cards over 3 columns and 3 computers, 10 Agents in each card's directory (written with and without a trailing slash)
const CARDS = 60, PER_CARD = 10;
const machines: Machine[] = [0, 1, 2].map((i) => ({ id: `m${i}`, name: `Computer ${i}`, online: true }));
const cards: BoardCard[] = Array.from({ length: CARDS }, (_, i) => ({ id: `c${i}`, machine_id: `m${i % 3}`, workspace_id: `w${i}`, title: `Project ${i}`, cwd: `/projects/p${i}`, source: "directory", column_id: `col${i % 3}`, pinned: false, archived: false }));
const board = { revision: 1, can_write: true, columns: [0, 1, 2].map((i) => ({ id: `col${i}`, title: `Column ${i}` })), cards };
const workspaces = cards.map((c) => ({ id: c.workspace_id, machine_id: c.machine_id, source: "directory", source_id: c.workspace_id, title: c.title, cwd: c.cwd, online: true, available: true }));
const agents = (): Session[] => Array.from({ length: CARDS * PER_CARD }, (_, i) => ({ machine_id: `m${i % CARDS % 3}`, session_id: `s${i}`, title: `Agent ${i}`, cwd: `/projects/p${i % CARDS}${Math.floor(i / CARDS) % 2 ? "/" : ""}`, status: "idle", state: "idle" }));
const counted = (rows: Session[]) => new Proxy(rows, { get(target, key, receiver) { if (typeof key === "string" && String(Number(key)) === key) live.reads++; return Reflect.get(target, key, receiver); } });
const setAgents = (rows: Session[]) => act(() => { live.sessions = counted(rows); live.listeners.forEach((fn) => fn()); });
const workSession = (id: string, partial: Partial<Session> = {}): Session => ({ machine_id: "m0", session_id: id, title: id, cwd: "/elsewhere", status: "idle", state: "idle", ...partial });
const workTodo = (id: string, partial: Partial<Todo> = {}): Todo => ({ id, title: id, created_at: "2026-11-09T00:00:00Z", updated_at: "2026-11-09T01:00:00Z", ...partial });
const linked = (machine_id: string, session_id: string) => [{ machine_id, session_id, linked_at: "2026-11-09T00:00:00Z" }];
const accepted = { supervisor: "main", state: "accepted" as const, reviews: [{ at: "2026-11-09T02:00:00Z", verdict: "accepted" as const }] };

const answers: Record<string, unknown> = { "/api/v1/workspace-board/settings": { enabled: true, can_write: true }, "/api/v1/workspace-board": board, "/api/v1/workspaces": { workspaces }, "/api/v1/machines": { machines }, "/api/v1/todos": { todos: [] } };
let host: HTMLDivElement;
let root: Root;
const flush = async () => { for (let i = 0; i < 5; i++) await act(async () => { await Promise.resolve(); }); };
const shownCards = () => host.querySelectorAll("article[data-card-id]").length;
const cardText = (id: string) => host.querySelector(`article[data-card-id="${id}"]`)?.textContent ?? "";
const lane = (name: string) => host.querySelector<HTMLElement>(`section[aria-label="${name}"]`);
const inLane = (name: string, id: string) => Boolean(lane(name)?.querySelector(`article[data-card-id="${id}"]`));
const workInLane = (name: string, id: string) => Boolean(lane(name)?.querySelector(`article[data-work-id="${id}"]`));
const laneBadge = (name: string) => lane(name)?.querySelector("h2")?.nextElementSibling?.textContent;
const laneOption = (value: string) => host.querySelector(`select[aria-label="选择工作流列"] option[value="${value}"]`)?.textContent;
const openWork = (id: string) => act(() => { host.querySelector<HTMLElement>(`article[data-work-id="${id}"] button`)!.click(); });
const type = (text: string) => act(() => {
  const input = host.querySelector<HTMLInputElement>('input[aria-label="搜索工作空间"]')!;
  Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(input, text);
  input.dispatchEvent(new Event("input", { bubbles: true }));
});

describe("the workflow board with many cards and Agents", () => {
  beforeEach(() => {
    setRosterDefault("assistant");
    live.sessions = []; live.reads = 0; live.cardRenders = 0;
    answers["/api/v1/todos"] = { todos: [] };
    resetWorkspaceBoardSettingForTest();
    vi.stubGlobal("fetch", vi.fn((url: string) => Promise.resolve(new Response(JSON.stringify(answers[new URL(url, "http://x").pathname] ?? {}), { headers: { "Content-Type": "application/json" } }))));
    vi.useFakeTimers({ toFake: ["setInterval", "clearInterval"] });
    host = document.createElement("div");
    document.body.append(host);
    root = createRoot(host);
  });
  afterEach(() => {
    act(() => root.unmount());
    host.remove();
    reset();
    vi.useRealTimers();
    vi.unstubAllGlobals();
    localStorage.removeItem("persona.workspace-board.view");
    localStorage.removeItem("agent.open");
    history.replaceState(null, "", "/");
  });

  it("reads no Agent and re-renders no kept card on a search keystroke, and each Agent once when they change", async () => {
    await act(() => refresh());
    setAgents(agents());
    act(() => root.render(<WorkspaceBoard />));
    await flush();
    expect(shownCards()).toBe(CARDS);
    expect(cardText("c7")).toContain(`${PER_CARD} 个 Agent`);
    expect(cardText("c7")).toContain("Computer 1");
    expect(cardText("c7")).not.toContain("空间未发现");

    // a keystroke that hides no card: no Agent read (was cards x Agents = 36,000), no card rendered again (was 60)
    live.reads = 0; live.cardRenders = 0;
    type("p");
    expect(shownCards()).toBe(CARDS);
    expect(live.reads).toBe(0);
    expect(live.cardRenders).toBe(0);

    // one that hides most: the cards it keeps do not render again either
    type("project 5");
    expect(shownCards()).toBe(11);
    expect(live.reads).toBe(0);
    expect(live.cardRenders).toBe(0);
    type("");
    expect(shownCards()).toBe(CARDS);
    expect(live.reads).toBe(0);

    // the Agents change: each is read once to index them, and the cards show the new ones
    live.reads = 0;
    const next = agents().filter((s) => !s.cwd.startsWith("/projects/p7") || s.cwd === "/projects/p7");
    setAgents(next.map((s) => s.session_id === "s7" ? { ...s, status: "working", state: "working" } : s));
    expect(live.reads).toBeLessThanOrEqual(next.length);
    expect(live.reads).toBeGreaterThan(0);
    expect(cardText("c7")).toContain(`${PER_CARD / 2} 个 Agent`);
    expect(cardText("c8")).toContain(`${PER_CARD} 个 Agent`);
  });

  it("flows cards by live agent state in the auto view, with actions on the card", async () => {
    localStorage.setItem("persona.workspace-board.view", "auto");
    await act(() => refresh());
    setAgents(agents());
    act(() => root.render(<WorkspaceBoard />));
    await flush();
    // Seen, idle agents are history, not work still waiting for acceptance.
    expect(lane("空闲")?.querySelectorAll("article[data-card-id]").length).toBe(CARDS);
    expect(lane("待验收")?.querySelectorAll("article[data-card-id]").length ?? 0).toBe(0);
    expect(lane("需要你")?.querySelectorAll("article[data-card-id]").length ?? 0).toBe(0);

    const rows = agents();
    rows[0] = { ...rows[0], state: "needs_you" }; // c0's agent
    rows[1] = { ...rows[1], state: "working" }; // c1's agent
    setAgents(rows);
    expect(inLane("需要你", "c0")).toBe(true);
    expect(inLane("待验收", "c0")).toBe(false);
    expect(inLane("工作中", "c1")).toBe(true);
    expect(lane("待验收")?.querySelectorAll("article[data-card-id]").length ?? 0).toBe(0);
    expect(lane("空闲")?.querySelectorAll("article[data-card-id]").length).toBe(CARDS - 2);
    expect(host.textContent).toContain("打开会话");

    // The agent is answered and stops: the card remains accessible as idle history without any dragging.
    rows[0] = { ...rows[0], state: "idle" };
    rows[1] = { ...rows[1], state: "idle" };
    setAgents(rows);
    expect(inLane("空闲", "c0")).toBe(true);
    expect(inLane("空闲", "c1")).toBe(true);
    expect(inLane("待验收", "c0")).toBe(false);
    expect(inLane("待验收", "c1")).toBe(false);
  });

  it("counts only genuine review cards in mixed work, in the badge and narrow picker", async () => {
    localStorage.setItem("persona.workspace-board.view", "auto");
    const rows = [
      workSession("bound-seen", { cwd: "/projects/p0", seen_at: "2026-11-09T03:00:00Z" }),
      workSession("bound-unread", { machine_id: "m1", cwd: "/projects/p1", state: "unread" }),
      workSession("bound-needs", { machine_id: "m2", cwd: "/projects/p2", state: "needs_you" }),
      workSession("bound-working", { cwd: "/projects/p3", state: "working" }),
      workSession("loose-seen", { seen_at: "2026-11-09T03:00:00Z" }),
      workSession("loose-offline", { machine_id: "m1", status: "offline", state: "offline" }),
      workSession("loose-unread", { machine_id: "m2", state: "unread" }),
      workSession("loose-finished", { work: { phase: "done", just_finished: true } }),
    ];
    answers["/api/v1/todos"] = { todos: [
      workTodo("closed", { done_at: "2026-11-09T02:00:00Z", done_by: "review", supervision: accepted }),
      workTodo("accepted", { agents: linked("m2", "loose-unread"), supervision: accepted }),
      workTodo("accepted-decision", { supervision: accepted, ask: { outcome: "decide", verdict: "Choose A or B", asked_at: "2026-11-09T03:00:00Z" } }),
      workTodo("pending-review", { supervision: { supervisor: "main", state: "pending_review" } }),
      workTodo("unread-todo", { agents: linked("m1", "bound-unread") }),
      workTodo("planned"),
      workTodo("needs-todo", { agents: linked("m2", "bound-needs") }),
      workTodo("working-todo", { agents: linked("m0", "bound-working") }),
    ] };
    await act(() => refresh());
    setAgents(rows);
    act(() => root.render(<WorkspaceBoard />));
    await flush();

    expect(laneBadge("待验收")).toBe("6");
    expect(laneOption("review")).toBe("待验收 · 6");
    expect(lane("待验收")?.querySelectorAll("article").length).toBe(6);
    expect(inLane("待验收", "c1")).toBe(true);
    expect(workInLane("待验收", "session:m2/loose-unread")).toBe(true);
    expect(workInLane("待验收", "session:m0/loose-finished")).toBe(true);
    expect(workInLane("待验收", "todo:accepted-decision")).toBe(true);
    expect(workInLane("待验收", "todo:pending-review")).toBe(true);
    expect(workInLane("待验收", "todo:unread-todo")).toBe(true);
    expect(inLane("空闲", "c0")).toBe(true);
    expect(workInLane("空闲", "session:m0/loose-seen")).toBe(true);
    expect(workInLane("空闲", "session:m1/loose-offline")).toBe(true);
    expect(workInLane("空闲", "todo:accepted")).toBe(true);
    expect(workInLane("空闲", "todo:closed")).toBe(true);
    expect(workInLane("待验收", "todo:accepted")).toBe(false);
    expect(workInLane("待验收", "todo:closed")).toBe(false);
    expect(laneBadge("需要你")).toBe("2");
    expect(laneBadge("工作中")).toBe("2");
    expect(laneBadge("待办")).toBe("1");

    rows[0] = { ...rows[0], state: "unread" };
    setAgents(rows);
    expect(inLane("待验收", "c0")).toBe(true);
    expect(inLane("空闲", "c0")).toBe(false);
    expect(laneBadge("待验收")).toBe("7");
    expect(laneOption("review")).toBe("待验收 · 7");
  });

  it("keeps accepted to-dos and seen sessions accessible in Idle and opens the original session", async () => {
    localStorage.setItem("persona.workspace-board.view", "auto");
    localStorage.setItem("agent.open", "page");
    answers["/api/v1/todos"] = { todos: [workTodo("closed-history", { done_at: "2026-11-09T02:00:00Z", done_by: "review", supervision: accepted })] };
    await act(() => refresh());
    setAgents([workSession("seen-history", { seen_at: "2026-11-09T03:00:00Z" })]);
    act(() => root.render(<WorkspaceBoard />));
    await flush();

    openWork("session:m0/seen-history");
    expect(location.pathname).toBe("/s/m0/seen-history");
    expect(workInLane("空闲", "session:m0/seen-history")).toBe(true);
    expect(workInLane("空闲", "todo:closed-history")).toBe(true);
    expect(workInLane("待验收", "session:m0/seen-history")).toBe(false);
    expect(workInLane("待验收", "todo:closed-history")).toBe(false);
  });

  it("opens a to-do card at its original detail rather than the undifferentiated list", async () => {
    localStorage.setItem("persona.workspace-board.view", "auto");
    answers["/api/v1/todos"] = { todos: [workTodo("accepted-closed", { done_at: "2026-11-09T02:00:00Z", done_by: "review", supervision: accepted })] };
    await act(() => refresh());
    setAgents([]);
    act(() => root.render(<WorkspaceBoard />));
    await flush();

    openWork("todo:accepted-closed");
    expect(location.pathname + location.search).toBe("/todos?id=accepted-closed");
  });
});
