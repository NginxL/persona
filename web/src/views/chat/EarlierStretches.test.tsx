// @vitest-environment jsdom
// The earlier stretches' module (#5054): it splits the live session's items into stretches only when the live session
// ran an earlier one too (A → B → A). While a reply streams into a live session that did not, the pushes split nothing.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { EngineLeg, Item } from "../../lib/types";
import type { LiveTranscript, StretchesReport } from "./useEarlierStretches";

const split = vi.hoisted(() => ({ calls: [] as string[] }));
vi.mock("../../lib/engine-stretches", async (importOriginal) => {
  const m = await importOriginal<typeof import("../../lib/engine-stretches")>();
  return { ...m, splitStretches: (...a: Parameters<typeof m.splitStretches>) => (split.calls.push(a[2]), m.splitStretches(...a)) };
});

const { default: EarlierStretches } = await import("./EarlierStretches");
(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

// The divider wiring is what these cases cover: the full chat row subtree (SegmentView and its hooks) is mounted
// through ChatView's own tests, and needs providers this direct harness does not provide.
vi.mock("./Lines", () => ({ ChatLineView: ({ line }: { line: { id: string } }) => <div data-line={line.id} /> }));

const T0 = Date.parse("2026-11-12T09:00:00Z");
const at = (m: number) => new Date(T0 + m * 60_000).toISOString();
const item = (seq: number, m: number): Item => ({ id: `i${seq}#0`, seq, role: "assistant", kind: "text", text: `at ${m}`, ts: at(m) });
const noop = async () => {};
const live = (items: Item[]): LiveTranscript => ({ items, hasMore: false, loading: false, loadingEarlier: false, loadEarlier: async () => undefined, expandFold: noop });
const report = () => {};

let host: HTMLDivElement;
let root: Root;
beforeEach(() => {
  split.calls = [];
  host = document.createElement("div");
  root = createRoot(host);
});
afterEach(() => act(() => root.unmount()));

const draw = (sid: string, history: EngineLeg[], items: Item[]) =>
  act(() => root.render(<EarlierStretches mid="m" sid={sid} scope={`m/${sid}`} history={history} live={live(items)} report={report} />));

describe("EarlierStretches", () => {
  it("splits nothing again while a reply streams into a live session that ran one stretch", () => {
    const history = [{ session_id: "a", agent: "claude", until: at(10) }];
    const items = [item(1, 11)];
    draw("b", history, items);
    const before = split.calls.length;
    for (let n = 2; n < 6; n++) draw("b", history, [...items, item(n, 10 + n)]);
    expect(split.calls.length).toBe(before);
    expect(split.calls).not.toContain("b");
  });

  it("splits the live session's items when it ran an earlier stretch too (A → B → A)", () => {
    const history = [{ session_id: "a", agent: "claude", until: at(10) }, { session_id: "b", agent: "codex", until: at(20) }];
    draw("a", history, [item(1, 1), item(2, 21)]);
    const before = split.calls.filter((s) => s === "a").length;
    draw("a", history, [item(1, 1), item(2, 21), item(3, 22)]);
    expect(split.calls.filter((s) => s === "a").length).toBe(before + 1);
  });

  it("waits for an earlier engine's backoff and shares its next page", async () => {
    vi.useFakeTimers();
    const get = vi.fn()
      .mockResolvedValueOnce(new Response("offline", { status: 503 }))
      .mockResolvedValueOnce(Response.json({ items: [item(1, 1)], has_more: false }));
    vi.stubGlobal("fetch", get);
    try {
      let latest!: StretchesReport;
      const history = [{ session_id: "a", agent: "claude", until: at(10) }];
      await act(() => root.render(
        <EarlierStretches mid="m" sid="b" scope="m/b" history={history} live={live([item(2, 11)])}
          report={(_scope, report) => { latest = report; }} />,
      ));
      await act(async () => { await latest.loadEarlier(); });
      const first = latest.loadEarlier({ waitForRetry: true });
      const second = latest.loadEarlier({ waitForRetry: true });
      expect(get).toHaveBeenCalledTimes(1);
      await act(async () => { await vi.advanceTimersByTimeAsync(3000); });
      const page = await first;
      expect(typeof page).toBe("symbol");
      expect(await second).toBe(page);
      expect(get).toHaveBeenCalledTimes(2);
    } finally {
      vi.useRealTimers();
      vi.unstubAllGlobals();
    }
  });

  // #8160: an earlier stretch gets a divider over the first line of each day it covers, day-only keys, and a day split
  // across an engine switch is labelled once.
  const drawWithFetch = async (sid: string, history: EngineLeg[], items: Item[], pages: Record<string, Item[]>) => {
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input);
      const m = /\/sessions\/m\/([^/]+)\/items\?/.exec(url);
      const body = m && pages[m[1]] ? { items: pages[m[1]], has_more: false } : { items: [] };
      return new Response(JSON.stringify(body), { status: 200, headers: { "Content-Type": "application/json" } });
    }));
    const reports: StretchesReport[] = [];
    await act(() => root.render(
      <EarlierStretches mid="m" sid={sid} scope={`m/${sid}`} history={history} live={live(items)} report={(_s, r) => { reports.push(r); }} />,
    ));
    for (let n = 0; n < 50 && !host.querySelector("[data-day-divider]"); n++) {
      await act(async () => { await reports.at(-1)?.loadEarlier(); await new Promise((r) => setTimeout(r, 0)); });
    }
    return reports;
  };
  const dayAt = (m: number) => new Date(T0 + m * 60_000).toLocaleDateString("sv");
  const dividerIds = () => [...host.querySelectorAll("[data-day-divider]")].map((el) => el.getAttribute("data-day-divider"));
  // a full exchange: user and reply make their own segment, so each carries its own day
  const exchange = (qSeq: number, qM: number, aSeq: number, aM: number): Item[] => [
    { id: `u${qSeq}#0`, seq: qSeq, role: "user", kind: "text", text: `q${qSeq}`, ts: at(qM) },
    { id: `a${aSeq}#0`, seq: aSeq, role: "assistant", kind: "text", text: `r${aSeq}`, ts: at(aM) },
  ];

  it("puts one divider per day boundary inside a loaded earlier stretch", async () => {
    const history = [{ session_id: "a", agent: "claude", until: at(10) }];
    const pages = { a: [...exchange(1, 1, 2, 2), ...exchange(3, 60 * 24 + 5, 4, 60 * 24 + 6)] };
    const reports = await drawWithFetch("b", history, [item(5, 60 * 24 + 11)], pages);
    expect(dividerIds()).toEqual([`day-${dayAt(1)}`, `day-${dayAt(60 * 24 + 5)}`]);
    // the days the stretch covers are reported for the live transcript's own divider guard
    expect(reports.at(-1)!.dayKeys).toEqual([dayAt(1), dayAt(60 * 24 + 5)]);
  });

  it("labels a day split across two stretches once: the later stretch's opening divider is skipped", async () => {
    const history = [
      { session_id: "a", agent: "claude", until: at(10) },
      { session_id: "b", agent: "codex", until: at(60 * 48 + 10) },
    ];
    // a's first stretch (from the live session's items) and the live one; b's page opens on a's day then crosses one
    const liveItems = [...exchange(1, 1, 2, 2), ...exchange(9, 60 * 48 + 21, 10, 60 * 48 + 22)];
    const pages = { b: [...exchange(3, 20, 4, 21), ...exchange(5, 60 * 24 + 11, 6, 60 * 24 + 12)] };
    await drawWithFetch("a", history, liveItems, pages);
    const ids = dividerIds();
    expect(new Set(ids).size).toBe(ids.length); // no day twice
    // day 0 from a, day 1 from b's boundary; b opens on day 0 and adds no second divider for it
    expect(ids).toEqual([`day-${dayAt(1)}`, `day-${dayAt(60 * 24 + 11)}`]);
  });

  it("never reports a day a stretch meets only inside a folded item, so it cannot suppress the live divider (#8160)", async () => {
    const history = [{ session_id: "a", agent: "claude", until: at(10) }];
    // stretch a: a day-0 exchange, then a folded tool run whose recorded time crosses midnight into day 1. The fold is
    // a step inside the day-0 segment, not a line of its own, so the stretch draws no day-1 divider.
    const folded: Item = {
      id: "fold:f3#0", seq: 3, role: "assistant", kind: "fold", ts: at(60 * 24),
      fold: { items: 4, tools: 2, thoughts: 0, through_seq: 4, until: at(60 * 24) },
    };
    const pages = { a: [...exchange(1, 1, 2, 2), folded] };
    // the live stretch opens on the day the fold only reached: its divider must survive the guard
    const liveItems = exchange(9, 60 * 24 + 120, 10, 60 * 24 + 121);
    const reports = await drawWithFetch("b", history, liveItems, pages);
    // the stretch itself draws the day-0 divider only
    expect(dividerIds()).toEqual([`day-${dayAt(1)}`]);
    // the reported guard days equal the dividers drawn: the fold's midnight day is not among them
    expect(reports.at(-1)!.dayKeys).toEqual([dayAt(1)]);
  });
});
