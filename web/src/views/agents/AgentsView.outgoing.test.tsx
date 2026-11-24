// @vitest-environment jsdom
// Personal Agent sends remain recoverable when their view goes away. The hub keeps only a short preview, so the
// browser must retain the full text and the idempotent client id before the composer clears its draft (#12555).
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import type { AgentSession, MessagePage, SendResult } from "../../lib/agents";
import { setToken } from "../../lib/api";
import { t } from "../../i18n";

const AGENT_ID = "lark:recovery";
const TEXT = `Keep the first line\n${"x".repeat(240)}\nand the last line`;

const state = vi.hoisted(() => ({
  messages: (() => Promise.resolve({ messages: [], hasMore: false })) as (id: string) => Promise<MessagePage>,
  outbox: (() => Promise.resolve({ outbox: [] })) as (id: string) => Promise<unknown>,
  send: (() => Promise.resolve({ outbox: { clientId: "", status: "sent" }, duplicate: false })) as (id: string, clientId: string, text: string) => Promise<SendResult>,
  list: { sessions: [] as AgentSession[], loaded: true, error: "", hints: [] as string[], sources: null },
}));

vi.mock("../../lib/agents", async (original) => ({
  ...(await original<typeof import("../../lib/agents")>()),
  useAgentList: () => state.list,
  fetchHiddenSessions: async () => [],
  fetchMessages: (id: string) => state.messages(id),
  fetchAgentOutbox: (id: string) => state.outbox(id),
  sendMessage: (id: string, clientId: string, text: string) => state.send(id, clientId, text),
  markRead: async () => {},
}));

import { AgentsView } from "./AgentsView";

vi.hoisted(() => {
  (globalThis as Record<string, unknown>).IS_REACT_ACT_ENVIRONMENT = true;
});

const agent = { id: AGENT_ID, source: "lark", sessionKey: "recovery", provider: "cue", title: "Recovery Agent", canSend: true, unreadCount: 0, rankTimeMs: 1 };
const receipt = (clientId: string, status = "sent"): SendResult => ({ outbox: { clientId, status }, duplicate: false });

let host: HTMLDivElement;
let root: Root;
let mounted = false;

beforeEach(() => {
  localStorage.clear();
  setToken("synthetic-recovery-credential", "synthetic-device");
  state.list = { ...state.list, sessions: [agent] };
  state.messages = vi.fn(async () => ({ messages: [], hasMore: false }));
  state.outbox = vi.fn(async () => ({ outbox: [] }));
  state.send = vi.fn(async (_id, clientId) => receipt(clientId));
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  mounted = true;
});

afterEach(() => {
  if (mounted) act(() => root.unmount());
  host.remove();
  localStorage.clear();
  setToken("");
});

async function mount(id = AGENT_ID) {
  await act(async () => root.render(<AgentsView id={id} onToggleSidebar={() => {}} />));
}

async function remount(id = AGENT_ID) {
  act(() => root.unmount());
  root = createRoot(host);
  await mount(id);
}

function area() {
  return host.querySelector<HTMLTextAreaElement>("textarea")!;
}

async function type(value: string) {
  await act(async () => {
    Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")!.set!.call(area(), value);
    area().dispatchEvent(new Event("input", { bubbles: true }));
  });
}

function button(label: string) {
  return [...host.querySelectorAll("button")].find((b) => b.getAttribute("aria-label") === label || b.textContent?.includes(label));
}

async function submit(value = TEXT) {
  await type(value);
  await act(async () => button(t("agents-agentsview.send"))!.click());
}

function records(): { entries: Record<string, unknown>[]; discarded: Record<string, number> }[] {
  const out = [];
  for (let i = 0; i < localStorage.length; i++) {
    const key = localStorage.key(i);
    if (!key?.includes(":agent-outgoing-v1:")) continue;
    const value = JSON.parse(localStorage.getItem(key)!);
    expect(key).not.toContain("synthetic-recovery-credential");
    expect(localStorage.getItem(key)).not.toContain("synthetic-recovery-credential");
    out.push(value);
  }
  return out;
}

function savedEntry(clientId: string) {
  return records().flatMap((r) => r.entries).find((e) => e.clientId === clientId);
}

it("persists the full sending and failed message before and after the composer clears", async () => {
  let reject!: (error: Error) => void;
  state.send = vi.fn(() => new Promise<SendResult>((_resolve, rejectPromise) => { reject = rejectPromise; }));
  await mount();
  await submit();

  const clientId = vi.mocked(state.send).mock.calls[0][1];
  expect(area().value).toBe("");
  expect(records()).toHaveLength(1);
  expect(savedEntry(clientId)).toMatchObject({ clientId, text: TEXT, status: "sending" });
  expect(savedEntry(clientId)?.at).toEqual(expect.any(Number));
  expect(savedEntry(clientId)?.updatedAt).toEqual(expect.any(Number));

  await act(async () => {
    reject(new Error("Synthetic network failure"));
    await Promise.resolve();
  });
  expect(savedEntry(clientId)).toMatchObject({ clientId, text: TEXT, status: "failed", error: "Synthetic network failure" });
  expect(host.textContent).toContain(TEXT);
  expect(button(t("agents-agentsview.retry"))).toBeDefined();
  expect(button(t("agents-agentsview.discard"))).toBeDefined();
});

it("restores a failed full-text bubble and its error after remount", async () => {
  state.send = vi.fn(async () => { throw new Error("Synthetic refusal"); });
  await mount();
  await submit();
  const clientId = vi.mocked(state.send).mock.calls[0][1];

  await remount();

  expect(host.textContent).toContain(TEXT);
  expect(host.textContent).toContain("Synthetic refusal");
  expect(savedEntry(clientId)).toMatchObject({ text: TEXT, status: "failed", error: "Synthetic refusal" });
  expect(button(t("agents-agentsview.retry"))).toBeDefined();
});

it("restores an interrupted send as retryable without automatically posting it again", async () => {
  let reject!: (error: Error) => void;
  state.send = vi.fn(() => new Promise<SendResult>((_resolve, rejectPromise) => { reject = rejectPromise; }));
  await mount();
  await submit();
  const clientId = vi.mocked(state.send).mock.calls[0][1];

  await remount();

  expect(host.textContent).toContain(TEXT);
  expect(host.textContent).toContain(t("lib-outbox.not_confirmed"));
  expect(vi.mocked(state.send)).toHaveBeenCalledTimes(1);
  expect(savedEntry(clientId)).toMatchObject({ status: "failed", error: t("lib-outbox.not_confirmed") });
  await act(async () => {
    reject(new Error("Original view stopped"));
    await Promise.resolve();
  });
});

it("retries the restored message with the same client id and complete text", async () => {
  state.send = vi.fn()
    .mockRejectedValueOnce(new Error("Synthetic offline"))
    .mockImplementationOnce(async (_id, clientId) => receipt(clientId));
  await mount();
  await submit();
  const clientId = vi.mocked(state.send).mock.calls[0][1];
  await remount();

  await act(async () => button(t("agents-agentsview.retry"))!.click());

  expect(vi.mocked(state.send)).toHaveBeenCalledTimes(2);
  expect(vi.mocked(state.send).mock.calls[1]).toEqual([AGENT_ID, clientId, TEXT]);
  expect(savedEntry(clientId)).toMatchObject({ status: "sent", text: TEXT });
  expect(host.textContent).toContain(t("agents-agentsview.sent"));
});

it("settles a restored entry from outbox acceptance and its history echo", async () => {
  state.send = vi.fn(async () => { throw new Error("Answer was lost"); });
  await mount();
  await submit();
  const clientId = vi.mocked(state.send).mock.calls[0][1];
  expect(savedEntry(clientId)).toBeDefined();
  state.outbox = vi.fn(async () => ({ outbox: [{ clientId, sessionId: AGENT_ID, preview: TEXT.slice(0, 200), length: TEXT.length, status: "sent", attempts: 1, createdAt: new Date().toISOString(), updatedAt: new Date().toISOString() }] }));
  state.messages = vi.fn(async () => ({ messages: [{ externalId: "persisted-copy", role: "user", blocks: [{ type: "text", text: TEXT }], createTimeMs: Date.now(), position: 1 }], hasMore: false }));

  await remount();

  expect(host.textContent).toContain(TEXT);
  expect(button(t("agents-agentsview.retry"))).toBeUndefined();
  expect(savedEntry(clientId)).toBeUndefined();
  expect(vi.mocked(state.send)).toHaveBeenCalledTimes(1);
});

it("scopes restored text to its Agent and credential", async () => {
  state.send = vi.fn(async () => { throw new Error("Synthetic refusal"); });
  await mount();
  await submit();
  expect(savedEntry(vi.mocked(state.send).mock.calls[0][1])).toBeDefined();

  await remount("example:another-agent");
  expect(host.textContent).not.toContain(TEXT);
  await remount();
  expect(host.textContent).toContain(TEXT);

  setToken("synthetic-other-credential", "synthetic-other-device");
  await remount();
  expect(host.textContent).not.toContain(TEXT);
  expect(vi.mocked(state.send)).toHaveBeenCalledTimes(1);
});

it("does not resurrect a discarded entry when its original POST completes late", async () => {
  let resolve!: (result: SendResult) => void;
  state.send = vi.fn(() => new Promise<SendResult>((resolvePromise) => { resolve = resolvePromise; }));
  await mount();
  await submit();
  const clientId = vi.mocked(state.send).mock.calls[0][1];
  await remount();
  await act(async () => button(t("agents-agentsview.discard"))!.click());
  expect(records()[0]?.discarded[clientId]).toEqual(expect.any(Number));

  await act(async () => {
    resolve(receipt(clientId));
    await Promise.resolve();
  });
  state.outbox = vi.fn(async () => ({ outbox: [{ clientId, sessionId: AGENT_ID, status: "sent" }] }));
  await remount();

  expect(host.textContent).not.toContain(TEXT);
  expect(savedEntry(clientId)).toBeUndefined();
  expect(records()[0]?.discarded[clientId]).toEqual(expect.any(Number));
  expect(vi.mocked(state.send)).toHaveBeenCalledTimes(1);
});

it("keeps an explicit discard deleted across metadata reconciliation and remount", async () => {
  state.send = vi.fn(async () => { throw new Error("Synthetic refusal"); });
  await mount();
  await submit();
  const clientId = vi.mocked(state.send).mock.calls[0][1];
  expect(savedEntry(clientId)).toBeDefined();

  await act(async () => button(t("agents-agentsview.discard"))!.click());

  expect(host.textContent).not.toContain(TEXT);
  expect(records()[0]?.entries.some((e) => e.clientId === clientId)).toBe(false);
  expect(records()[0]?.discarded[clientId]).toEqual(expect.any(Number));
  state.outbox = vi.fn(async () => ({ outbox: [{ clientId, sessionId: AGENT_ID, preview: TEXT.slice(0, 200), length: TEXT.length, status: "failed", error: "Still on the hub", attempts: 1, createdAt: new Date().toISOString(), updatedAt: new Date().toISOString() }] }));
  await remount();
  expect(host.textContent).not.toContain(TEXT);
  expect(button(t("agents-agentsview.retry"))).toBeUndefined();
  expect(vi.mocked(state.send)).toHaveBeenCalledTimes(1);
});

it("keeps a failed send in its own conversation when the view switches to another Agent without a remount", async () => {
  const other = { ...agent, id: "lark:other", sessionKey: "other", title: "Other Agent" };
  state.list = { ...state.list, sessions: [agent, other] };
  state.send = vi.fn(async () => { throw new Error("Synthetic refusal"); });
  await mount();
  await submit();
  const clientId = vi.mocked(state.send).mock.calls[0][1];

  // the same root re-renders with another id, as the router does when the owner picks another Agent
  await mount(other.id);
  expect(host.textContent).not.toContain(TEXT);
  expect(button(t("agents-agentsview.retry"))).toBeUndefined();

  await mount();
  expect(host.textContent).toContain(TEXT);
  await act(async () => button(t("agents-agentsview.retry"))!.click());
  expect(vi.mocked(state.send).mock.calls.at(-1)).toEqual([AGENT_ID, clientId, TEXT]);
  expect(vi.mocked(state.send).mock.calls.every(([id]) => id === AGENT_ID)).toBe(true);
});

it("does not erase another tab's failed send when this tab saves its own", async () => {
  const { personalOutgoingStore, restorePersonalOutgoing, savePersonalOutgoing } = await import("../../lib/agent-outgoing");
  const tabA = personalOutgoingStore(AGENT_ID);
  const tabB = personalOutgoingStore(AGENT_ID);
  expect(restorePersonalOutgoing(tabA)).toEqual([]);
  expect(restorePersonalOutgoing(tabB)).toEqual([]);
  const at = Date.now();
  savePersonalOutgoing(tabB, [{ clientId: "from-b", text: "tab B text", at, updatedAt: at, status: "failed", error: "x" }]);
  savePersonalOutgoing(tabA, [{ clientId: "from-a", text: "tab A text", at, updatedAt: at, status: "sending" }]);

  expect(savedEntry("from-b")).toMatchObject({ text: "tab B text", status: "failed" });
  expect(savedEntry("from-a")).toMatchObject({ text: "tab A text", status: "sending" });
  // what tab A settles itself still goes away
  savePersonalOutgoing(tabA, []);
  expect(savedEntry("from-a")).toBeUndefined();
  expect(savedEntry("from-b")).toBeDefined();
});
