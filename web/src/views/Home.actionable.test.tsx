// @vitest-environment jsdom
// Actionable delivery and computer waits share the Overview's existing Needs you section (#12561). The section count
// includes each visible action, while the headline and every global badge keep counting Agent sessions as before.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { OutboxEntry } from "../lib/outbox";
import type { Interaction, Machine, Session } from "../lib/types";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const fake = vi.hoisted(() => ({
  sessions: [] as Session[],
  interactions: [] as Interaction[],
  machines: [] as Machine[],
  outbox: {} as Record<string, OutboxEntry[]>,
  retry: vi.fn((_mid: string, _sid: string, _id: string, _clearDraft?: string) => Promise.resolve(true)),
  check: vi.fn((_mid: string, _sid: string, _id: string) => Promise.resolve("applied" as const)),
  discard: vi.fn((_mid: string, _sid: string, _id: string) => undefined),
}));

vi.mock("../lib/store", async (importOriginal) => {
  const real = await importOriginal<typeof import("../lib/store")>();
  return {
    ...real,
    useStore: <T,>(select: (s: ReturnType<typeof real.getState>) => T) => select({
      ...real.getState(), loaded: true, machines: fake.machines, sessions: { mac: fake.sessions }, interactions: fake.interactions,
      outbox: fake.outbox, todos: [], answered: {},
    }),
  };
});
vi.mock("../lib/current-assistant", async () => {
  const { stateCounts } = await import("../lib/sessions");
  return {
    useScopedMachines: () => fake.machines,
    useScopedInteractions: () => fake.interactions,
    useScopedAgentSessions: () => fake.sessions,
    useScopedCounts: () => ({ ...stateCounts(fake.sessions), interactions: fake.interactions.length, quietCounted: false }),
    useScopedAgentListStatus: () => ({ status: "loaded", whole: true }),
    useScopedTodos: () => [],
  };
});
vi.mock("../lib/outbox", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../lib/outbox")>()),
  retryOutbox: fake.retry,
  checkOutbox: fake.check,
}));
vi.mock("./chat/OutboxBubbles", async (importOriginal) => ({
  ...(await importOriginal<typeof import("./chat/OutboxBubbles")>()),
  deleteFailed: fake.discard,
}));
vi.mock("./extensions/ExtensionSlots", () => ({ OverviewCards: () => null, OverviewArrangeButton: () => null, OverviewArranging: () => null }));

const { Home } = await import("./Home");

const session = (o: Partial<Session> = {}): Session => ({
  machine_id: "mac", session_id: "s1", title: "Fix the login page", cwd: "/code", status: "idle", state: "needs_you", ...o,
});
const failed = (o: Partial<OutboxEntry> = {}): OutboxEntry => ({
  id: "m1", text: "Please continue", images: [], at: 1, state: "failed", error: "computer offline", ...o,
});
const mac: Machine = { id: "mac", name: "Mac", online: true };
const box: Machine = { id: "box", name: "Studio", online: false, error_code: "node_service_down" };

let host: HTMLDivElement;
let root: Root;
beforeEach(() => {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  fake.sessions = [session()];
  fake.interactions = [];
  fake.machines = [mac];
  fake.outbox = {};
  for (const fn of [fake.retry, fake.check, fake.discard]) fn.mockClear();
  history.replaceState(null, "", "/overview");
});
afterEach(() => {
  act(() => root.unmount());
  host.remove();
});

const mount = () => act(() => root.render(<Home onNewAgent={() => {}} onToggleSidebar={() => {}} />));
const outboxRow = (id = "m1") => host.querySelector(`[data-actionable-outbox$="/${id}"]`);
const computerRow = (id: string) => host.querySelector(`[data-actionable-computer="${id}"]`);
const sectionCount = () => {
  const section = (outboxRow() ?? computerRow("box"))?.closest("section");
  return section?.querySelector("h2")?.nextElementSibling?.textContent;
};
const click = async (element: Element | null | undefined) => {
  expect(element).not.toBeNull();
  await act(async () => (element as HTMLButtonElement).click());
};

describe("actionable Needs you rows", () => {
  it("counts each request, failed send and unavailable computer once without changing the global Agent count", () => {
    fake.sessions = [session(), session({ session_id: "assistant", assistant: true, state: "idle" })];
    fake.interactions = [{ id: "q1", kind: "question", machine_id: "mac", session_id: "s1", summary: "Which option?" } as Interaction];
    fake.outbox = {
      "mac/s1": [failed(), failed({ id: "sending", state: "sending" })],
      "mac/assistant": [failed({ id: "assistant" })],
    };
    fake.machines = [mac, box, { id: "updating", name: "Updating", online: false, deploy_phase: "restarting" }];
    mount();

    expect(outboxRow()?.textContent).toContain("Fix the login page");
    expect(outboxRow()?.textContent).toContain("computer offline");
    expect(outboxRow()?.querySelector("a")?.getAttribute("href")).toBe("/s/mac/s1");
    expect(outboxRow("sending")).toBeNull();
    expect(outboxRow("assistant")).toBeNull();
    expect(computerRow("box")?.textContent).toContain("Studio");
    expect(computerRow("updating")).toBeNull();
    expect(sectionCount()).toBe("3");
    expect(host.textContent).toContain("1 个 Agent 需要你");
    expect(host.textContent).not.toContain("3 个 Agent 需要你");
    expect(fake.retry).not.toHaveBeenCalled();
    expect(fake.check).not.toHaveBeenCalled();
    expect(fake.discard).not.toHaveBeenCalled();
  });

  it("uses the existing Retry and Delete actions only when clicked", async () => {
    fake.outbox = { "mac/s1": [failed()] };
    mount();
    await click(outboxRow()?.querySelector('[data-testid="outbox-retry"]'));
    expect(fake.retry).toHaveBeenCalledTimes(1);
    expect(fake.retry).toHaveBeenCalledWith("mac", "s1", "m1");
    await click(outboxRow()?.querySelector('[data-testid="outbox-delete"]'));
    expect(fake.discard).toHaveBeenCalledTimes(1);
    expect(fake.discard).toHaveBeenCalledWith("mac", "s1", "m1");
    expect(fake.check).not.toHaveBeenCalled();
  });

  it("checks an uncertain delivery instead of posting an ordinary retry", async () => {
    fake.outbox = { "mac/s1": [failed({ inDoubt: true, error: "Delivery is uncertain" })] };
    mount();
    expect(outboxRow()?.querySelector('[data-testid="outbox-retry"]')).toBeNull();
    await click(outboxRow()?.querySelector('[data-testid="outbox-check"]'));
    expect(fake.check).toHaveBeenCalledTimes(1);
    expect(fake.check).toHaveBeenCalledWith("mac", "s1", "m1");
    expect(fake.retry).not.toHaveBeenCalled();
  });

  it("opens existing computer setup/detail routes and removes resolved rows from the same count", () => {
    fake.interactions = [{ id: "q1", kind: "question", machine_id: "mac", session_id: "s1", summary: "Which option?" } as Interaction];
    fake.outbox = { "mac/s1": [failed()] };
    fake.machines = [mac, box, { id: "new", name: "New computer", online: false, trust: "pending" }];
    mount();
    expect(computerRow("box")?.querySelector("a")?.getAttribute("href")).toBe("/machines/box");
    expect(computerRow("new")?.querySelector("a")?.getAttribute("href")).toBe("/machines");
    expect(sectionCount()).toBe("4");

    fake.interactions = [];
    fake.outbox = { "mac/s1": [failed({ state: "sent" })] };
    fake.machines = [mac, { ...box, online: true }, { id: "new", name: "New computer", online: true }];
    mount();
    expect(outboxRow()).toBeNull();
    expect(computerRow("box")).toBeNull();
    expect(computerRow("new")).toBeNull();
    expect([...host.querySelectorAll("h2")].map((h) => h.textContent)).not.toContain("需要你");
  });
});
