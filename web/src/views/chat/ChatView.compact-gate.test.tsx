// @vitest-environment jsdom
// Regression test for PR #12547 review comment 4654593168:
// One-click /compact from ChatView's Session Insights popover must align with composer preconditions
// (hidden when task route is active or setup gate is blocked) and keep the popover open + toast when send fails.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { COMPACT_FEATURE } from "../../lib/compaction";
import { setRoute } from "../../lib/feed";
import type { Session } from "../../lib/types";
import { ChatView } from "./ChatView";

const mocks = vi.hoisted(() => ({
  session: {
    machine_id: "hub",
    session_id: "c1",
    title: "Agent",
    cwd: "/code",
    status: "idle",
    agent: "claude",
    context_tokens: 80_000,
    context_window: 200_000,
  } as Session,
  features: [] as string[],
  blocked: undefined as { label: string; onClick?: () => void } | undefined,
  api: vi.fn(),
  apiOp: vi.fn(),
  toast: vi.fn(),
}));

vi.mock("./Composer", async (importOriginal) => ({
  ...await importOriginal<typeof import("./Composer")>(),
  Composer: () => <div data-testid="composer" />,
}));
vi.mock("./useTranscript", () => ({
  useTranscript: () => ({ items: [], loading: false, error: "", hasMore: false, streaming: "" }),
}));
vi.mock("../../lib/store", async (importOriginal) => {
  const real = await importOriginal<typeof import("../../lib/store")>();
  return {
    ...real,
    getSession: () => mocks.session,
    useStore: <T,>(select: (s: ReturnType<typeof real.getState>) => T) => select({ ...real.getState(), features: mocks.features, sessions: { hub: [mocks.session] } }),
  };
});
vi.mock("../../lib/api", async (importOriginal) => ({
  ...await importOriginal<typeof import("../../lib/api")>(),
  api: (...args: unknown[]) => mocks.api(...args),
}));
vi.mock("../../lib/panel", async (importOriginal) => ({
  ...await importOriginal<typeof import("../../lib/panel")>(),
  usePanel: () => null,
}));
vi.mock("../../lib/router", async (importOriginal) => ({
  ...await importOriginal<typeof import("../../lib/router")>(),
  useRoute: () => ({ name: "session", mid: "hub", id: "c1" }),
}));
vi.mock("../../lib/ops", async (importOriginal) => ({
  ...await importOriginal<typeof import("../../lib/ops")>(),
  apiOp: (...args: unknown[]) => mocks.apiOp(...args),
}));
vi.mock("../../ui/atoms", async (importOriginal) => ({
  ...await importOriginal<typeof import("../../ui/atoms")>(),
  toast: (...args: unknown[]) => mocks.toast(...args),
}));
vi.mock("../assistant/Persona", () => ({ usePersona: () => ({ name: "Persona" }) }));
vi.mock("../assistant/AssistantChat", () => ({
  AssistantFace: () => null,
  CompactionStrip: () => null,
  useAssistantMenu: () => ({ items: [], dialogs: null }),
}));
vi.mock("../assistant/Tasks", () => ({
  DayLine: () => null,
  FeedLineView: () => null,
  QuickResultCard: () => null,
  RouteChip: () => null,
  AssistantActionBar: () => null,
  TaskDock: () => null,
  TaskQuestions: () => null,
}));
vi.mock("../assistant/Readiness", () => ({
  ChatOnlyNote: () => null,
  FailureCard: () => null,
  ReadinessCard: () => null,
  useReadinessGate: () => ({ blocked: mocks.blocked, dialog: null, help: () => {}, confirm: () => {} }),
}));

let host: HTMLDivElement;
let root: Root;

beforeEach(() => {
  (globalThis as Record<string, unknown>).IS_REACT_ACT_ENVIRONMENT = true;
  (globalThis as Record<string, unknown>).matchMedia = () => ({ matches: false, addEventListener() {}, removeEventListener() {} });
  if (typeof globalThis.ResizeObserver === "undefined") {
    globalThis.ResizeObserver = class {
      observe() {}
      unobserve() {}
      disconnect() {}
    } as unknown as typeof ResizeObserver;
  }
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
  mocks.blocked = undefined;
  mocks.features = [];
  mocks.session = {
    machine_id: "hub",
    session_id: "c1",
    title: "Agent",
    cwd: "/code",
    status: "idle",
    agent: "claude",
    context_tokens: 80_000,
    context_window: 200_000,
  };
  mocks.api.mockReset().mockResolvedValue({});
  mocks.apiOp.mockReset().mockResolvedValue({ delivered: true });
  mocks.toast.mockReset();
  setRoute("c1", null);
});

afterEach(async () => {
  await act(async () => {
    root.unmount();
  });
  host.remove();
  setRoute("c1", null);
});

async function waitForSelector<T extends Element>(selector: string, container: ParentNode = document): Promise<T> {
  for (let i = 0; i < 40; i++) {
    const el = container.querySelector<T>(selector);
    if (el) return el;
    await act(async () => {
      await new Promise((r) => setTimeout(r, 10));
    });
  }
  throw new Error(`Selector not found: ${selector}`);
}

describe("ChatView one-click /compact preconditions", () => {
  it("withholds /compact while a task route is active or setup gate is blocked", async () => {
    setRoute("c1", "todo-1");
    await act(async () => {
      root.render(<ChatView mid="hub" sid="c1" />);
    });
    const meter = await waitForSelector<HTMLButtonElement>('[data-composer-usage] button[aria-haspopup="dialog"]', host);
    await act(async () => {
      meter.click();
    });
    const dialog = await waitForSelector<HTMLElement>("[data-session-insights]");
    expect(dialog.querySelector("[data-insights-compact]")).toBeNull();

    // Clear route, enable setup gate block -> still withheld
    setRoute("c1", null);
    mocks.blocked = { label: "Finish setup", onClick: vi.fn() };
    await act(async () => {
      root.render(<ChatView mid="hub" sid="c1" />);
    });
    expect(document.querySelector("[data-insights-compact]")).toBeNull();
  });

  it("keeps popover open and surfaces failure toast when send resolves false at click time", async () => {
    await act(async () => {
      root.render(<ChatView mid="hub" sid="c1" />);
    });
    const meter = await waitForSelector<HTMLButtonElement>('[data-composer-usage] button[aria-haspopup="dialog"]', host);
    await act(async () => {
      meter.click();
    });
    const dialog = await waitForSelector<HTMLElement>("[data-session-insights]");
    const compactBtn = dialog.querySelector<HTMLButtonElement>("[data-insights-compact]")!;
    expect(compactBtn).not.toBeNull();

    // Simulate session becoming read-only right before click so deliver() resolves false
    mocks.session = { ...mocks.session, capabilities: { send: false } };
    await act(async () => {
      compactBtn.click();
    });

    expect(mocks.apiOp).not.toHaveBeenCalled();
    expect(mocks.toast).toHaveBeenCalledWith(expect.any(String), "error");
    expect(document.querySelector("[data-session-insights]")).not.toBeNull();
  });

  it("keeps assistant one-click compact available even when offlineLine disables the composer, while still gating agents", async () => {
    mocks.features = [COMPACT_FEATURE];
    mocks.session = { ...mocks.session, assistant: true };

    await act(async () => {
      root.render(<ChatView mid="hub" sid="c1" assistant offline="Computer offline" />);
    });
    const meter = await waitForSelector<HTMLButtonElement>('[data-composer-usage] button[aria-haspopup="dialog"]', host);
    await act(async () => {
      meter.click();
    });
    const dialog = await waitForSelector<HTMLElement>("[data-session-insights]");
    const compactBtn = dialog.querySelector<HTMLButtonElement>("[data-insights-compact]")!;
    expect(compactBtn).not.toBeNull();

    await act(async () => {
      compactBtn.click();
    });
    expect(mocks.api).toHaveBeenCalledWith("/assistant/conversations/c1/compact", {
      method: "POST",
      json: { machine_id: "hub" },
    });

    // Agent chat with offlineLine still withholds /compact
    mocks.session = { ...mocks.session, assistant: false };
    await act(async () => {
      root.render(<ChatView mid="hub" sid="c1" offline="Computer offline" />);
    });
    const agentMeter = await waitForSelector<HTMLButtonElement>('[data-composer-usage] button[aria-haspopup="dialog"]', host);
    await act(async () => {
      agentMeter.click();
    });
    const agentDialog = await waitForSelector<HTMLElement>("[data-session-insights]");
    expect(agentDialog.querySelector("[data-insights-compact]")).toBeNull();
  });

  it("uses assistant_compact instead of agent /compact when session.assistant is true on a generic ChatView without the assistant prop", async () => {
    mocks.features = [COMPACT_FEATURE];
    mocks.session = { ...mocks.session, assistant: true };

    await act(async () => {
      root.render(<ChatView mid="hub" sid="c1" offline="Computer offline" />);
    });
    const meter = await waitForSelector<HTMLButtonElement>('[data-composer-usage] button[aria-haspopup="dialog"]', host);
    await act(async () => {
      meter.click();
    });
    const dialog = await waitForSelector<HTMLElement>("[data-session-insights]");
    const compactBtn = dialog.querySelector<HTMLButtonElement>("[data-insights-compact]")!;
    expect(compactBtn).not.toBeNull();
    expect(compactBtn.textContent).not.toContain("/compact");

    await act(async () => {
      compactBtn.click();
    });
    expect(mocks.apiOp).not.toHaveBeenCalled();
    expect(mocks.api).toHaveBeenCalledWith("/assistant/conversations/c1/compact", {
      method: "POST",
      json: { machine_id: "hub" },
    });
  });
});
