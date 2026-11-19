// @vitest-environment jsdom
// The first-screen part of a conversation's earlier stretches (#5054): the live stretch's items and where it starts, and
// what the stretches' module is handed.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import type { EngineLeg, Item } from "../../lib/types";
import { useEarlierStretches, type LiveTranscript, type Stitched } from "./useEarlierStretches";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const T0 = Date.parse("2026-11-12T09:00:00Z");
const at = (m: number) => new Date(T0 + m * 60_000).toISOString();
const item = (seq: number, m: number): Item => ({ id: `i${seq}#0`, seq, role: seq % 2 ? "user" : "assistant", kind: "text", text: `at ${m}`, ts: at(m) });
const noop = async () => {};
const live = (items: Item[], hasMore = false): LiveTranscript => ({ items, hasMore, loading: false, loadingEarlier: false, loadEarlier: async () => undefined, expandFold: noop });

let host: HTMLDivElement;
let root: Root;
let got: Stitched;
function Probe({ sid, history, tr }: { sid: string; history: EngineLeg[]; tr: LiveTranscript }) {
  got = useEarlierStretches("m", sid, history, tr);
  return null;
}
const draw = (sid: string, history: EngineLeg[], tr: LiveTranscript) => act(() => root.render(<Probe sid={sid} history={history} tr={tr} />));

beforeEach(() => {
  host = document.createElement("div");
  root = createRoot(host);
});
afterEach(() => act(() => root.unmount()));

describe("useEarlierStretches", () => {
  it("hands the module the conversation's history, the same array while it says the same", () => {
    const history = [{ session_id: "a", agent: "claude", until: at(10) }];
    const items = [item(1, 11), item(2, 12)];
    draw("b", history, live(items));
    const legs = got.host!.history;
    draw("b", [...history], live([...items, item(3, 13)]));
    expect(got.host!.history).toBe(legs);
    expect(got.liveItems).toHaveLength(3);
    expect(got.liveItems).toBe(got.host!.live.items); // the transcript's own: the live stretch is all of it
  });

  it("A → B → A: the live stretch's items and where it starts", () => {
    const history = [{ session_id: "a", agent: "claude", until: at(10) }, { session_id: "b", agent: "codex", until: at(20) }];
    const early = [item(1, 1), item(2, 2)];
    draw("a", history, live([...early, item(3, 21)], true));
    expect(got.liveItems.map((i) => i.seq)).toEqual([3]);
    expect(got.liveHasMore).toBe(false); // its start is loaded: A's older pages are its early stretch's
    draw("a", history, live([...early, item(3, 21), item(4, 22)], true));
    expect(got.liveItems.map((i) => i.seq)).toEqual([3, 4]);
  });

  it("A → B → A: the live stretch has older pages while nothing before it is loaded", () => {
    const history = [{ session_id: "a", agent: "claude", until: at(10) }, { session_id: "b", agent: "codex", until: at(20) }];
    draw("a", history, live([item(5, 30), item(6, 31)], true));
    expect(got.liveHasMore).toBe(true);
    expect(got.liveItems.map((i) => i.seq)).toEqual([5, 6]);
  });

  it("is the transcript as it is for a conversation that never switched", () => {
    const tr = live([item(1, 1)], true);
    draw("a", [], tr);
    expect(got.host).toBeNull();
    expect(got.liveItems).toBe(tr.items);
    expect(got.liveHasMore).toBe(true);
    expect(got.hasMore).toBe(true);
    expect(got.due).toBe(0);
  });
});
