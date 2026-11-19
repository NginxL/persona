// @vitest-environment jsdom
// An agent's live text (#1309): a Claude session Persona started streams its reply as `delta` like the main chat; the
// reply's item replaces it, a delta that comes after the item (the hub hands its last live text to a page that subscribed
// mid-message) is not shown twice, and a chat that gets no delta has none. Only the newest items touch it: an older page,
// an opened fold or an earlier message landing late leaves the words of the one being written alone, since the node
// sends nothing new while a tool runs.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { ItemsEvent, listenSession } from "../../lib/store";
import type { Item, ItemsPage } from "../../lib/types";
import type { Transcript } from "./useTranscript";

type Listener = Parameters<typeof listenSession>[2];
let listener: Listener | undefined;

const prompt: Item = { id: "u5#0", seq: 5, role: "user", kind: "text", text: "Write about the sea, then list the folder.", ts: "2026-11-07T10:00:00Z" } as Item;
const said = (seq: number, text: string): Item => ({ id: `a${seq}#0`, seq, role: "assistant", kind: "text", text, ts: "2026-11-07T10:00:05Z" }) as Item;
const call = (seq: number): Item[] => [
  { id: `t${seq}#0`, seq, role: "assistant", kind: "tool_use", tool_use_id: `toolu_${seq}`, tool_name: "Bash", ts: "2026-11-07T10:00:06Z" } as Item,
  { id: `r${seq + 1}#0`, seq: seq + 1, role: "user", kind: "tool_result", tool_use_id: `toolu_${seq}`, text: "ok", ts: "2026-11-07T10:00:08Z" } as Item,
];
// what the fake hub has besides the newest page: the turn before (an older page) and a fold's steps
const earlier = [{ id: "u1#0", seq: 1, role: "user", kind: "text", text: "Earlier question", ts: "2026-11-07T09:00:00Z" } as Item, said(2, "Earlier answer.")];
const folded = [said(3, "Checked the folder first."), ...call(3)];

vi.mock("../../lib/api", async (orig) => ({
  ...(await orig<typeof import("../../lib/api")>()),
  api: vi.fn(async (path: string): Promise<ItemsPage> => {
    if (path.includes("before_seq=")) return { items: earlier, to_seq: 2 } as ItemsPage;
    if (path.includes("through_seq=")) return { items: folded, to_seq: 4 } as ItemsPage;
    return { items: [prompt], to_seq: 5, has_more: true } as ItemsPage;
  }),
}));
vi.mock("../../lib/store", async (orig) => ({
  ...(await orig<typeof import("../../lib/store")>()),
  send: vi.fn(),
  listenSession: (_mid: string, _sid: string, l: Listener) => {
    listener = l;
    return () => (listener = undefined);
  },
}));

const { useTranscript } = await import("./useTranscript");

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let tr: Transcript;
let assistant = false;
function Probe() {
  tr = useTranscript("macbook", "a1f3c2d4-0000-4000-8000-000000000001", { assistant });
  return null;
}

let root: Root;
const mount = async () => {
  root = createRoot(document.createElement("div"));
  act(() => root.render(<Probe />));
  await act(async () => {}); // the first page
};
beforeEach(() => {
  vi.useFakeTimers();
  assistant = false;
});
afterEach(() => {
  act(() => root.unmount());
  vi.useRealTimers();
});

const delta = async (text: string) => {
  act(() => listener!.delta!(text));
  await act(async () => void vi.advanceTimersByTime(500)); // past the throttle
};
const items = (list: Item[], from: number) => act(() => listener!.items!({ items: list, from_seq: from, to_seq: list.at(-1)!.seq } as ItemsEvent));

describe("an agent's live text", () => {
  beforeEach(mount);

  it("streams by lines until the transcript has the reply, which replaces it", async () => {
    await delta("Sea 1: waves.\n");
    expect(tr.streaming).toBe("Sea 1: waves.\n");
    await delta("Sea 1: waves.\nSea 2: tides.");
    expect(tr.streaming).toBe("Sea 1: waves.\nSea 2: tides.");
    items([said(6, "Sea 1: waves.\nSea 2: tides.")], 5);
    expect(tr.streaming).toBe("");
  });

  it("keeps the final delta visible across the empty end marker until its transcript item lands", async () => {
    await delta("Closing words.");
    await delta("");
    expect(tr.streaming).toBe("Closing words.");
    expect(tr.streamEnded).toBe(true);
    items([said(6, "Closing words.")], 5);
    expect(tr.streaming).toBe("");
    expect(tr.items.at(-1)?.text).toBe("Closing words.");
  });

  it("drops an ended preview when a new exchange opens", async () => {
    await delta("Unpersisted.");
    await delta("");
    items([{ ...prompt, id: "u6", seq: 6 }], 5);
    expect(tr.streaming).toBe("");
  });

  it("does not show again what the reply's item already holds", async () => {
    items([said(6, "Mountain 1: peaks.\nMountain 2: snow.")], 5);
    await delta("Mountain 1: peaks.\n"); // late: the transcript was first
    expect(tr.streaming).toBe("");
    await delta("Mountain 1: peaks.\nMountain 2: snow.");
    expect(tr.streaming).toBe("");
  });

  it("retires landed live words before a new user message or later tool result arrives", async () => {
    await delta("The previous reply.");
    items([said(6, "The previous reply.")], 5);
    expect(tr.streaming).toBe("");
    items([{ ...prompt, id: "u7#0", seq: 7, text: "Check the risks." }, ...call(8)], 6);
    expect(tr.streaming).toBe("");
    await delta("The new reply.");
    expect(tr.streaming).toBe("The new reply.");
  });

  it("retires a replay that was already landed before its delta arrived", async () => {
    items([said(6, "The previous reply.")], 5);
    await delta("The previous reply.");
    items([{ ...prompt, id: "u7#0", seq: 7, text: "Check the risks." }], 6);
    expect(tr.streaming).toBe("");
    await delta("The previous reply.");
    expect(tr.streaming).toBe("The previous reply.");
  });

  it("shows what goes past the words it has, and the next message", async () => {
    items([said(6, "Block one.")], 5);
    await delta("Block one.\n\nBlock two.\n");
    expect(tr.streaming).toBe("Block two.\n");
    await delta("After the tool.\n");
    expect(tr.streaming).toBe("After the tool.\n");
  });

  it("is none without a delta", async () => {
    items([said(6, "A reply that only the transcript brought.")], 5);
    await act(async () => void vi.advanceTimersByTime(1000));
    expect(tr.streaming).toBe("");
  });

  it("stays while an older page loads and a fold opens", async () => {
    await delta("Running the tests now.\n");
    await act(() => tr.loadEarlier());
    expect(tr.items.map((i) => i.seq)).toContain(2);
    expect(tr.streaming).toBe("Running the tests now.\n");
    await act(() => tr.expandFold({ id: "f3#0", seq: 3, role: "assistant", kind: "fold", fold: { through_seq: 4 } } as unknown as Item));
    expect(tr.items.map((i) => i.seq)).toContain(3);
    expect(tr.streaming).toBe("Running the tests now.\n");
  });

  it("stays when the message before it lands late, and goes when its own does", async () => {
    // the words before the tool, the call and its result land only once the next message is on screen
    await delta("Running the tests now.\n");
    items([said(6, "I'll update the names first."), ...call(7)], 5);
    expect(tr.streaming).toBe("Running the tests now.\n");
    items([said(9, "Running the tests now.")], 8);
    expect(tr.streaming).toBe("");
  });

  it("is not taken by the message before when both begin alike", async () => {
    items([said(6, "I'll update the names.\nThen I'll run the tests."), ...call(7)], 5);
    await delta("I'll update the names.\n");
    expect(tr.streaming).toBe("I'll update the names.\n");
  });
});

describe("the assistant's live text", () => {
  beforeEach(async () => {
    assistant = true;
    await mount();
  });

  it("is its reply as it came, which any reply item ends", async () => {
    items([said(6, "An earlier reply that begins alike.")], 5);
    await delta("An earlier");
    expect(tr.streaming).toBe("An earlier");
    items([said(7, "Something else.")], 6);
    expect(tr.streaming).toBe("");
  });

  it("stays while an older page loads", async () => {
    await delta("Writing the reply");
    await act(() => tr.loadEarlier());
    expect(tr.streaming).toBe("Writing the reply");
  });
});
