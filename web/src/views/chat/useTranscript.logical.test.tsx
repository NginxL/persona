// @vitest-environment jsdom
// Logical segments preserve the existing integer/history protocol. A boundary
// bridge must not reopen the feed; an actual source rewrite must replace it.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import type { ItemsEvent, listenSession } from "../../lib/store";
import type { Item, ItemsPage } from "../../lib/types";
import type { Transcript } from "./useTranscript";

type Listener = Parameters<typeof listenSession>[2];
let listener: Listener | undefined;
let phase = 1;
let reads = 0;
const first = (text = "source"): Item => ({ id: "line1#0", seq: 1, kind: "text", role: "user", text });
const second: Item = { id: "segment:two:line1#0", seq: 101, kind: "text", role: "assistant", text: "target" };

vi.mock("../../lib/api", async (original) => ({
  ...(await original<typeof import("../../lib/api")>()),
  api: vi.fn(async (): Promise<ItemsPage> => {
    reads++;
    return phase === 1
      ? { items: [first()], history: "source-h1", from_seq: 0, to_seq: 100, has_more: false }
      : { items: [first("rewritten source"), second], history: "logical-h2", from_seq: 0, to_seq: 101, has_more: false };
  }),
}));
vi.mock("../../lib/store", async (original) => ({
  ...(await original<typeof import("../../lib/store")>()),
  send: vi.fn(),
  listenSession: (_machine: string, _session: string, value: Listener) => {
    listener = value;
    return () => { listener = undefined; };
  },
}));
const { useTranscript } = await import("./useTranscript");
(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
let root: Root;
let transcript: Transcript;
function Probe() { transcript = useTranscript("public-machine", "logical-conversation", { assistant: true }); return null; }
beforeEach(async () => {
  vi.useFakeTimers(); phase = 1; reads = 0;
  root = createRoot(document.createElement("div"));
  act(() => root.render(<Probe />));
  await act(async () => {});
});
afterEach(() => { act(() => root.unmount()); vi.useRealTimers(); });
const push = (event: Omit<ItemsEvent, "machine_id" | "session_id">) => act(() => listener!.items!({ machine_id: "public-machine", session_id: "logical-conversation", ...event }));

it("keeps first IDs and continuous integer coverage across a later engine, then reloads a real rewrite", async () => {
  expect(transcript.items.map(item => item.id)).toEqual(["line1#0"]);
  const beforeBridge = reads;
  push({ items: [], history: "source-h1", from_seq: 100, to_seq: 100 });
  push({ items: [second], history: "source-h1", from_seq: 100, to_seq: 101 });
  await act(async () => { await vi.advanceTimersByTimeAsync(1700); });
  expect(reads).toBe(beforeBridge);
  expect(transcript.items.map(item => item.id)).toEqual(["line1#0", "segment:two:line1#0"]);
  phase = 2;
  push({ items: [], history: "logical-h2", from_seq: 0, to_seq: 0 });
  await act(async () => {});
  expect(reads).toBeGreaterThan(beforeBridge);
  expect(transcript.items.map(item => item.text)).toEqual(["rewritten source", "target"]);
  expect(transcript.history).toBe("logical-h2");
});
