// @vitest-environment jsdom
// "Hand to AI" › "Also mark as done" (docs/assistant/inbox.md "Item actions"): off by default for every target — the main
// chat, a new Persona chat, a running agent and "New agent…", whose agent files nothing unless the user asked; a box the
// user set keeps its value when the target changes.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const world = vi.hoisted(() => ({ sessions: {} as Record<string, unknown[]>, machines: [] as unknown[] }));
vi.mock("../../lib/store", async (orig) => {
  const real = await orig<typeof import("../../lib/store")>();
  return {
    ...real,
    useStore: <T,>(sel: (s: ReturnType<typeof real.getState>) => T) =>
      real.useStore((s) => sel({ ...s, sessions: world.sessions, machines: world.machines } as ReturnType<typeof real.getState>)),
  };
});
vi.mock("../../lib/router", () => ({ navigate: vi.fn() }));
vi.mock("../../lib/new-agent", async (orig) => ({ ...(await orig<typeof import("../../lib/new-agent")>()), openNewAgent: vi.fn() }));
vi.mock("../../ui/atoms", async (orig) => ({ ...(await orig<typeof import("../../ui/atoms")>()), toast: vi.fn() }));

import { reloadInbox, resetInboxForTests, type InboxItem } from "../../lib/inbox";
import { handOffSourceKey, rememberHandOff, agentTarget } from "../../lib/handoff-memory";
import { openNewAgent, type NewAgentPreset } from "../../lib/new-agent";
import { resetManagerChoice, setRosterDefault } from "../../lib/manager-choice";
import type { Session } from "../../lib/types";
import { HandOffDialog } from "./InboxDialogs";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const item: InboxItem = {
  id: "lark:f1:1", manager: "assistant", source: "lark", kind: "dm", account: "feishu", account_name: "Lark", title: "Example", preview: "text", count: 1,
  updated_at: "2026-11-01T09:00:00Z", tier: "action", lark: { feed_id: "f1", is_group: false },
  mutes: [{ tag: "chat", value: "oc_example", name: "Example" }],
};
const agent = { machine_id: "mac", session_id: "s1", title: "Release check", cwd: "/code", status: "idle", state: "idle" } as Session;

let host: HTMLDivElement;
let root: Root;
type Call = { url: string; method: string };
const calls: Call[] = [];

beforeEach(() => {
  calls.length = 0;
  localStorage.clear();
  resetManagerChoice();
  resetInboxForTests();
  setRosterDefault("assistant");
  world.sessions = { mac: [agent] };
  world.machines = [{ id: "mac", name: "Studio", online: true }];
  vi.mocked(openNewAgent).mockClear();
  vi.stubGlobal("fetch", vi.fn(async (url: string, init?: RequestInit) => {
    calls.push({ url: String(url), method: init?.method ?? "GET" });
    const body = String(url).includes("/inbox?") ? { items: [item] }
      : String(url).includes("/inbox/") ? item
      : String(url).endsWith("/handoff") ? { item, session: { machine_id: "mac", session_id: "main-a" } }
      : String(url).endsWith("/assistant/conversations") ? { session: { machine_id: "mac", session_id: "s-new" } }
      : String(url).endsWith("/assistant") ? { machine_id: "mac", conversations: [], main: "main-a" }
      : {};
    return { ok: true, status: 200, text: async () => JSON.stringify(body), headers: new Headers() };
  }));
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
  resetManagerChoice();
  resetInboxForTests();
  vi.unstubAllGlobals();
});

const render = () => act(() => root.render(<HandOffDialog item={item} onClose={() => {}} />));
const pick = (label: string) => {
  const radio = [...document.body.querySelectorAll('[role="radio"]')].find((b) => b.textContent?.includes(label)) as HTMLButtonElement;
  expect(radio).toBeTruthy();
  act(() => radio.click());
};
const agentRow = () => {
  const radio = [...document.body.querySelectorAll('[role="radio"]')].find((b) => b.textContent?.includes("Release check")) as HTMLButtonElement;
  expect(radio).toBeTruthy();
  act(() => radio.click());
};
const doneBox = () => document.body.querySelector('input[type="checkbox"]') as HTMLInputElement;
const toggle = () => act(() => doneBox().click());
const newAgentRow = () => document.body.querySelector('[data-testid="handoff-new-agent"]') as HTMLButtonElement;
const filed = (c: Call) => c.method === "POST" && c.url === "/api/v1/inbox/lark:f1:1/done";

describe("Hand to AI: the done box's default", () => {
  it("is off for the main chat, a new chat and a running agent", () => {
    render();
    expect(doneBox().checked).toBe(false);
    pick("Persona（新对话）");
    expect(doneBox().checked).toBe(false);
    agentRow();
    expect(doneBox().checked).toBe(false);
    pick("Persona（主对话）");
    expect(doneBox().checked).toBe(false);
  });

  it("is off when the source's last target was an agent (Last time)", () => {
    rememberHandOff(handOffSourceKey(item.source, item.mutes)!, agentTarget("mac", "s1"));
    render();
    expect(doneBox().checked).toBe(false);
  });

  it("stays off through every target when the user never set it", () => {
    render();
    for (const label of ["Persona（新对话）", "Persona（主对话）"]) {
      pick(label);
      expect(doneBox().checked).toBe(false);
    }
    agentRow();
    expect(doneBox().checked).toBe(false);
  });

  it("keeps the user's tick when the target changes", () => {
    render();
    toggle();
    expect(doneBox().checked).toBe(true);
    pick("Persona（新对话）");
    expect(doneBox().checked).toBe(true);
    agentRow();
    expect(doneBox().checked).toBe(true);
    pick("Persona（主对话）");
    expect(doneBox().checked).toBe(true);
  });

  it("keeps the user's clear when the target changes", () => {
    render();
    toggle(); // on
    toggle(); // and off, the user's choice
    pick("Persona（新对话）");
    agentRow();
    pick("Persona（主对话）");
    expect(doneBox().checked).toBe(false);
  });
});

describe("Hand to AI: what the default does on the way out", () => {
  it("a new chat leaves the item open by default", async () => {
    await reloadInbox();
    render();
    pick("Persona（新对话）");
    const button = [...document.body.querySelectorAll("button")].find((b) => b.textContent?.trim() === "发送")!;
    await act(async () => button.click());
    await vi.waitFor(() => expect(calls.some((c) => c.url.endsWith("/assistant/conversations"))).toBe(true));
    await new Promise((r) => setTimeout(r, 30));
    expect(calls.some(filed)).toBe(false);
  });

  it("a new chat files the item when the user ticked the box", async () => {
    await reloadInbox();
    render();
    pick("Persona（新对话）");
    toggle();
    const button = [...document.body.querySelectorAll("button")].find((b) => b.textContent?.trim() === "发送")!;
    await act(async () => button.click());
    await vi.waitFor(() => expect(calls.some(filed)).toBe(true));
  });

  it("an older hub's main conversation files the item only when the user ticked the box", async () => {
    await reloadInbox();
    const fetcher = vi.mocked(fetch);
    const answer = fetcher.getMockImplementation()!;
    fetcher.mockImplementation(async (url, init) => {
      if (String(url).endsWith("/handoff")) {
        calls.push({ url: String(url), method: "POST" });
        return { ok: false, status: 404, text: async () => JSON.stringify({ error: "not found" }), headers: new Headers() } as Response;
      }
      return answer(url, init);
    });
    render();
    const button = [...document.body.querySelectorAll("button")].find((b) => b.textContent?.trim() === "发送")!;
    await act(async () => button.click());
    await vi.waitFor(() => expect(calls.some((c) => c.method === "POST" && c.url === "/api/v1/sessions/mac/main-a/messages")).toBe(true));
    await new Promise((r) => setTimeout(r, 30));
    expect(calls.some(filed)).toBe(false);
  });

  it("an agent leaves the item open by default and files it when the user ticked the box", async () => {
    await reloadInbox();
    render();
    agentRow();
    const button = [...document.body.querySelectorAll("button")].find((b) => b.textContent?.trim() === "发送")!;
    await act(async () => button.click());
    await vi.waitFor(() => expect(calls.some((c) => c.method === "POST" && c.url === "/api/v1/sessions/mac/s1/messages")).toBe(true));
    await new Promise((r) => setTimeout(r, 30));
    expect(calls.some(filed)).toBe(false);

    resetInboxForTests();
    await reloadInbox();
    render();
    agentRow();
    toggle();
    await act(async () => [...document.body.querySelectorAll("button")].find((b) => b.textContent?.trim() === "发送")!.click());
    await vi.waitFor(() => expect(calls.some(filed)).toBe(true));
  });

  it("New agent… starts the agent and leaves the item open by default", async () => {
    await reloadInbox();
    render();
    await act(async () => newAgentRow().click());
    await vi.waitFor(() => expect(openNewAgent).toHaveBeenCalledTimes(1));
    const preset = vi.mocked(openNewAgent).mock.calls[0][0] as NewAgentPreset;
    act(() => preset.onStarted!(agent));
    await new Promise((r) => setTimeout(r, 30));
    expect(calls.some(filed)).toBe(false);
  });

  it("New agent… files the item when the user ticked the box", async () => {
    await reloadInbox();
    render();
    toggle();
    await act(async () => newAgentRow().click());
    await vi.waitFor(() => expect(openNewAgent).toHaveBeenCalledTimes(1));
    const preset = vi.mocked(openNewAgent).mock.calls[0][0] as NewAgentPreset;
    act(() => preset.onStarted!(agent));
    await vi.waitFor(() => expect(calls.some(filed)).toBe(true));
  });
});
