// @vitest-environment jsdom
// #11442: the overview's times move on by themselves. How long a row has worked (or waited in the background) ticks
// every 5 s and when a finished one last changed every half minute, each on its own row's clock: the page around them,
// 78 components on a busy overview, does not re-render with each tick, also while hidden. The data made up.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Machine, Session } from "../lib/types";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const fake = vi.hoisted(() => ({ sessions: [] as unknown[], renders: 0 }));

vi.mock("../lib/store", async (importOriginal) => {
  const real = await importOriginal<typeof import("../lib/store")>();
  const mac: Machine = { id: "mac", name: "Mac", online: true };
  return { ...real, useStore: <T,>(select: (s: ReturnType<typeof real.getState>) => T) => select({ ...real.getState(), loaded: true, machines: [mac], interactions: [], todos: [], answered: {} }) };
});
vi.mock("../lib/sessions", async (importOriginal) => {
  const real = await importOriginal<typeof import("../lib/sessions")>();
  const sessions = () => fake.sessions as Session[];
  return { ...real, useAgentSessions: sessions, useCounts: () => ({ ...real.stateCounts(sessions()), interactions: 0 }), useAgentListStatus: () => ({ status: "loaded", whole: true }) };
});
vi.mock("./extensions/ExtensionSlots", () => ({ OverviewCards: () => null, OverviewArrangeButton: () => null }));
// called once in each render of the page itself (and nowhere under it): its render count
vi.mock("../lib/scroll-memory", () => ({ useScrollMemory: () => void fake.renders++ }));

const { Home } = await import("./Home");
const { ago, shortDuration } = await import("../lib/format");

const NOW = Date.parse("2026-11-19T08:00:00Z");
const at = (agoMs: number) => new Date(NOW - agoMs).toISOString();
const agent = (sid: string, o: Partial<Session>): Session =>
  ({ machine_id: "mac", session_id: sid, title: sid, cwd: "/code", status: "idle", state: "idle", agent: "claude", updated_at: at(60_000), ...o }) as Session;

let host: HTMLDivElement;
let root: Root;
beforeEach(() => {
  vi.useFakeTimers({ toFake: ["setInterval", "clearInterval", "Date"] });
  vi.setSystemTime(NOW);
  fake.renders = 0;
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
});
afterEach(() => {
  act(() => root.unmount());
  host.remove();
  vi.useRealTimers();
});

const rowTime = (title: string) => {
  const row = [...host.querySelectorAll("a")].find((a) => a.textContent?.includes(title));
  return row?.querySelector(".tabular-nums")?.textContent;
};

describe("the overview's clocks", () => {
  it("move each row's time on without re-rendering the page", () => {
    fake.sessions = [
      agent("busy", { state: "working", status: "working", state_since: at(10_000) }),
      agent("done", { state: "unread", updated_at: at(40_000) }),
    ];
    act(() => root.render(<Home onNewAgent={() => {}} onToggleSidebar={() => {}} />));
    expect(rowTime("busy")).toBe(shortDuration(10_000));
    const justNow = ago(at(40_000));
    expect(rowTime("done")).toBe(justNow);
    const renders = fake.renders;
    expect(renders).toBeGreaterThan(0);

    act(() => void vi.advanceTimersByTime(5_000));
    expect(rowTime("busy")).toBe(shortDuration(15_000));
    act(() => void vi.advanceTimersByTime(25_000));
    expect(rowTime("busy")).toBe(shortDuration(40_000));
    // finished 40 s ago it was just now; 30 s on, a minute ago
    expect(rowTime("done")).toBe(ago(at(40_000)));
    expect(rowTime("done")).not.toBe(justNow);
    expect(fake.renders).toBe(renders);
  });
});
