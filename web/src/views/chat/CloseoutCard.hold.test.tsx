// @vitest-environment jsdom
// The Command evidence chunk (#8139): it starts from the store, before a card mounts; a card whose session read beats it,
// or the transcript's exchange, keeps its did, Verified and evidence rows back until both are in (HOLD_MS at most), so
// the card grows once; after a failed load they draw without the row. Each test imports the card afresh: the chunk's
// state belongs to the module.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { t } from "../../i18n";
import type { Segment } from "../../lib/turns";
import type { Session } from "../../lib/types";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

// The chunk comes in (or fails) when a test says so; `calls` counts its loads. A fresh one for every test (beforeEach).
const chunk = { calls: 0, loaded: false, open: () => {}, fail: () => {} };
function mockChunk() {
  chunk.calls = 0;
  chunk.loaded = false;
  vi.doMock("./CloseoutEvidence", async () => {
    chunk.calls++;
    await new Promise<void>((resolve, reject) => {
      chunk.open = resolve;
      chunk.fail = () => reject(new TypeError("Failed to fetch dynamically imported module"));
    });
    const { createElement, Fragment } = await import("react");
    chunk.loaded = true;
    return { CloseoutEvidence: () => createElement(Fragment, null, createElement("dt", null, "Evidence"), createElement("dd", { "data-closeout-evidence": "" })) };
  });
}
// the module system asks for the chunk a few turns after the import starts
const asked = (n: number) => vi.waitFor(() => expect(chunk.calls).toBe(n));
// one turn of the event loop, inside act (setImmediate: the fake timers below take only setTimeout)
const turn = () => act(async () => void (await new Promise((r) => setImmediate(r))));
// turns until the chunk's module came in and the card drew: up to 5 s, as under a full parallel run a cold module took
// more than the 50 turns this once allowed
async function turns(until: () => boolean) {
  const end = Date.now() + 5_000;
  while (!until() && Date.now() < end) await turn();
}
// the chunk's module in, then the 50 turns its importers once had to take it up
async function chunkIn() {
  await turns(() => chunk.loaded);
  for (let i = 0; i < 50; i++) await turn();
}

const read = {
  machine_id: "m", session_id: "s", cwd: "/x", title: "Check it", status: "done", state: "idle",
  work: { phase: "done", closeout: { status: "done", did: ["Paged the export"], verified: "`npm test`", missing: "A real payment" } },
} as Session;
// what a session list carries: where the closeout stands, without did and verified
const row = { ...read, work: { phase: "done", closeout: { status: "done", missing: "A real payment" } } } as Session;
// the closeout's exchange, once the transcript has it
const exchange = { id: "last", steps: [], tools: 0, thoughts: 0, replies: 0, folds: [] } as unknown as Segment;

let host: HTMLDivElement;
let root: Root;

// null: the transcript has no exchange yet
async function card(session: Session, segment: Segment | null = exchange) {
  const { CloseoutCard } = await import("./CloseoutCard");
  await act(async () => root.render(<CloseoutCard session={session} segment={segment ?? undefined} />));
}
const shows = (key: string) => Array.from(host.querySelectorAll("dt")).some((dt) => dt.textContent === t(key));
const evidence = () => host.querySelector("[data-closeout-evidence]") !== null;

beforeEach(async () => {
  vi.resetModules();
  // the card's fresh modules include a fresh catalog, loaded as src/i18n/test-setup.ts loads the first one
  await (await import("../../i18n")).initI18n("zh-Hans");
  mockChunk();
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
  vi.useRealTimers();
});

describe("CloseoutCard and its evidence chunk", () => {
  it("keeps did, Verified and the evidence row back until the chunk is in, then draws them together", async () => {
    await card(read);
    await asked(1); // the card's own load: nothing started it before
    expect(shows("chat-chatview.closeout_missing")).toBe(true);
    expect(shows("chat-chatview.closeout_did")).toBe(false);
    expect(shows("chat-chatview.closeout_verified")).toBe(false);
    expect(evidence()).toBe(false);
    chunk.open();
    await turns(evidence);
    expect(shows("chat-chatview.closeout_did")).toBe(true);
    expect(shows("chat-chatview.closeout_verified")).toBe(true);
    expect(evidence()).toBe(true);
  });

  it("keeps them back until the transcript has the closeout's exchange too, and not again once they showed", async () => {
    await card(read, null);
    await asked(1);
    chunk.open();
    await chunkIn();
    expect(shows("chat-chatview.closeout_verified")).toBe(false); // the chunk is in, the exchange is not
    await card(read);
    expect(shows("chat-chatview.closeout_did")).toBe(true);
    expect(shows("chat-chatview.closeout_verified")).toBe(true);
    expect(evidence()).toBe(true);
    await card(read, null); // a transcript read again
    expect(shows("chat-chatview.closeout_verified")).toBe(true);
    expect(evidence()).toBe(true);
  });

  it("holds nothing for a card drawn from a session list, which has no Verified line", async () => {
    await card(row);
    expect(chunk.calls).toBe(0);
    expect(shows("chat-chatview.closeout_missing")).toBe(true);
  });

  it("draws did and Verified without the row once HOLD_MS has passed, and the row when the chunk comes in", async () => {
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
    await card(read);
    await asked(1);
    expect(shows("chat-chatview.closeout_verified")).toBe(false);
    await act(async () => vi.advanceTimersByTime(1000));
    expect(shows("chat-chatview.closeout_did")).toBe(true);
    expect(shows("chat-chatview.closeout_verified")).toBe(true);
    expect(evidence()).toBe(false);
    chunk.open();
    await turns(evidence);
    expect(evidence()).toBe(true);
  });

  it("draws did and Verified without the row after a failed load", async () => {
    await card(read);
    await asked(1);
    chunk.fail();
    await turns(() => shows("chat-chatview.closeout_verified"));
    expect(shows("chat-chatview.closeout_did")).toBe(true);
    expect(shows("chat-chatview.closeout_verified")).toBe(true);
    expect(evidence()).toBe(false);
  });

  it("starts the chunk once the store holds a closeout a card would show, before any card, and not with Data Saver", async () => {
    const { applyCompleteSessions } = await import("../../lib/store");
    const { preloadCloseout } = await import("./closeout-chunks");
    const saver = preloadCloseout({ navigator: { connection: { saveData: true } } });
    const stop = preloadCloseout({});
    applyCompleteSessions([{ ...row, session_id: "w", state: "working", work: { ...row.work!, phase: "working" } } as Session]);
    expect(chunk.calls).toBe(0); // a working session shows no card
    applyCompleteSessions([row]);
    await asked(1);
    chunk.open();
    await chunkIn(); // the chunk comes in, before any card
    await card(read); // the card draws at once, with every row
    expect(chunk.calls).toBe(1);
    expect(shows("chat-chatview.closeout_verified")).toBe(true);
    expect(evidence()).toBe(true);
    saver();
    stop();
  });
});
