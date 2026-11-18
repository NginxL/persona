// @vitest-environment jsdom
import { clearConversationDeletions, conversationDeleted, rememberConversationDeleted } from "../../lib/conversation-deleted";
// The subscribe a hello sends for a chat whose first page is still on its way: none. Without since_seq the hub would
// read and send that whole page again over the socket, while the HTTP read brings it anyway (a page load opens the
// socket and reads at once); the read subscribes from where it ends.
import { ApiError } from "../../lib/api";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Item, ItemsPage } from "../../lib/types";
import { liveEntry, receiveLive, resetLive } from "../../lib/live";
import { useTranscript } from "./useTranscript";

interface Pending {
  url: string;
  resolve: (p: ItemsPage) => void;
  reject: (e: unknown) => void;
}
const calls: Pending[] = [];
const sent: Record<string, unknown>[] = [];
let handlers: { reconnected?: () => void } = {};

vi.mock("../../lib/api", async (orig) => ({
  ...(await orig<typeof import("../../lib/api")>()),
  api: (url: string) => new Promise((resolve, reject) => calls.push({ url, resolve, reject })),
}));
vi.mock("../../lib/store", async (orig) => ({
  ...(await orig<typeof import("../../lib/store")>()),
  listenSession: (_mid: string, _sid: string, h: { reconnected?: () => void }) => {
    handlers = h;
    return () => {};
  },
  send: (msg: Record<string, unknown>) => sent.push(msg),
}));

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let host: HTMLDivElement;
let root: Root;

function Probe() {
  useTranscript("m", "A");
  return null;
}

const item = (id: string, seq: number): Item => ({ id, seq, role: "user", kind: "text", text: id });
const subscribes = () => sent.filter((m) => m.type === "subscribe");
const hello = () => act(() => handlers.reconnected?.());
async function settleFirstRead(page: ItemsPage | Error) {
  const i = calls.findIndex((c) => c.url.includes("/m/A/items?") && c.url.includes("turns=2"));
  expect(i, "the first page's request").toBeGreaterThanOrEqual(0);
  const [c] = calls.splice(i, 1);
  await act(async () => (page instanceof Error ? c.reject(page) : c.resolve(page)));
}

beforeEach(() => {
  calls.length = 0;
  sent.length = 0;
  handlers = {};
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  act(() => root.render(<Probe />));
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
  clearConversationDeletions();
  resetLive();
});

describe("useTranscript subscribe", () => {
  it("does not subscribe on a hello that comes while the first page is on its way", async () => {
    hello();
    expect(subscribes()).toEqual([]);
    await settleFirstRead({ items: [item("a9", 9), item("a10", 10)], to_seq: 10 });
    // one subscribe, from where the page ends: the hub sends only what came after it
    expect(subscribes()).toEqual([expect.objectContaining({ session_id: "A", since_seq: 10 })]);
  });

  it("resubscribes from what it has on a hello after the first page", async () => {
    await settleFirstRead({ items: [item("a10", 10)], to_seq: 10 });
    sent.length = 0;
    hello();
    expect(subscribes()).toEqual([expect.objectContaining({ session_id: "A", since_seq: 10 })]);
  });

  it("still lets the subscription bring the last turns when the first read failed", async () => {
    hello();
    await settleFirstRead(new Error("boom"));
    expect(subscribes()).toHaveLength(1);
    expect(subscribes()[0].since_seq).toBeUndefined();
  });
});

it("clears the session's cached live retry on unmount", async () => {
  await settleFirstRead({ items: [item("a10", 10)], to_seq: 10 });
  receiveLive({ machine_id: "m", session_id: "A", live: { retry_error: "429 Synthetic rate limit for retry capture", retry_attempt: 5, retry_max: 10 } }, 1000);
  expect(liveEntry("m", "A")).toBeDefined();
  act(() => root.unmount());
  expect(liveEntry("m", "A")).toBeUndefined();
  root = createRoot(host);
});

it("never resubscribes or displays a page that arrives after deletion", async () => {
  await act(async () => rememberConversationDeleted("m", "A"));
  await settleFirstRead({ items: [item("late", 10)], to_seq: 10 });
  hello();
  expect(subscribes()).toEqual([]);
  expect(sent.some((m) => m.type === "unsubscribe" && m.session_id === "A")).toBe(true);
});

it("recognizes a cold old link's 410 without a prior websocket deletion event", async () => {
  await settleFirstRead(new ApiError(410, "Conversation deleted", { code: "conversation_deleted" }));
  expect(conversationDeleted("m", "A")).toBe(true);
  expect(subscribes()).toEqual([]);
});
