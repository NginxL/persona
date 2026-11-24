// @vitest-environment jsdom
import { act, StrictMode } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { t } from "../../i18n";
import { jumpWhenOpen } from "../../lib/forward";
import { applyCompleteSessions, reset } from "../../lib/store";
import type { Item, Session } from "../../lib/types";
import { forgetExpanded } from "./expanded";

vi.hoisted(() => {
  const g = globalThis as Record<string, unknown>;
  g.matchMedia = () => ({ matches: false, addEventListener() {}, removeEventListener() {} });
  g.ResizeObserver = class { observe() {} unobserve() {} disconnect() {} };
  g.IntersectionObserver = class { observe() {} unobserve() {} disconnect() {} };
});
const transcript = vi.hoisted(() => ({
  items: [] as Item[], loading: false, hasMore: false, streaming: "", error: "",
  expandFold: vi.fn<() => Promise<boolean>>(),
}));
vi.mock("./useTranscript", () => ({ useTranscript: () => transcript }));
const { ChatView } = await import("./ChatView");
(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const question: Item = { id: "question", seq: 1, role: "user", kind: "text", text: "First question" };
const fold: Item = { id: "work", seq: 2, role: "assistant", kind: "fold", fold: { items: 1, tools: 0, thoughts: 1, through_seq: 2 } };
const hidden: Item = { id: "thought", seq: 2, role: "assistant", kind: "thinking", text: "Folded thought" };
const latest: Item = { ...question, id: "latest", seq: 4, text: "Latest question" };
let host: HTMLDivElement;
let root: Root;
const scroller = () => host.querySelector<HTMLElement>("[data-chat-scroll]")!;
const render = (sid = "one", landAt?: string) => act(async () => root.render(<ChatView mid="m" sid={sid} landAt={landAt} inPage />));
const advance = (ms: number) => act(async () => { await vi.advanceTimersByTimeAsync(ms); });
const imports = () => act(async () => { await vi.dynamicImportSettled(); });
const scrolled = vi.fn();
const hit = () => host.querySelector("[data-search-hit]");

beforeEach(() => {
  vi.useFakeTimers();
  localStorage.clear();
  transcript.expandFold.mockReset();
  vi.stubGlobal("CSS", { escape: (s: string) => s });
  vi.stubGlobal("fetch", vi.fn(async (input) => Response.json(String(input).includes("from_seq=") ? { items: [hidden] } : {})));
  vi.spyOn(HTMLElement.prototype, "clientHeight", "get").mockReturnValue(500);
  vi.spyOn(HTMLElement.prototype, "scrollHeight", "get").mockReturnValue(5000);
  Object.defineProperty(HTMLElement.prototype, "scrollIntoView", { configurable: true, value: scrolled });
  applyCompleteSessions(["one", "two"].map((sid) => ({ machine_id: "m", session_id: sid, title: sid, state: "idle", status: "idle" }) as Session));
  Object.assign(transcript, { items: [question, fold, latest], loading: false });
  host = document.body.appendChild(document.createElement("div"));
  root = createRoot(host);
});
afterEach(() => {
  act(() => root.unmount());
  host.remove();
  reset();
  forgetExpanded();
  Reflect.deleteProperty(HTMLElement.prototype, "scrollIntoView");
  scrolled.mockClear();
  vi.restoreAllMocks();
  vi.clearAllTimers();
  vi.useRealTimers();
  vi.unstubAllGlobals();
});

async function pickFoldedHit() {
  await render();
  await imports();
  act(() => host.querySelector<HTMLButtonElement>(`button[aria-label="${t("chat-chatview.more_actions")}"]`)!.click());
  const search = [...document.querySelectorAll<HTMLElement>('[role="menuitem"]')].find((el) => el.textContent?.includes(t("chat-chatview.search_menu")))!;
  await act(async () => search.click());
  await imports();
  const input = document.querySelector<HTMLInputElement>("[data-chat-search] input")!;
  act(() => {
    Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(input, "Folded thought");
    input.dispatchEvent(new Event("input", { bubbles: true }));
  });
  await advance(250);
  let release!: (ok: boolean) => void;
  transcript.expandFold.mockImplementationOnce(() => new Promise((resolve) => { release = resolve; }));
  await act(async () => document.querySelector<HTMLButtonElement>("[data-search-result]")!.click());
  await imports();
  expect(transcript.expandFold).toHaveBeenCalledExactlyOnceWith(fold);
  return () => act(async () => release(true));
}

it.each(["wheel", "touch", "scrollbar", "End key", "another chat", "another target"])(
  "a pending folded search hit yields to %s", async (action) => {
    const release = await pickFoldedHit();
    if (action === "another chat") await render("two");
    else if (action === "another target") {
      await render("one", latest.id);
      await imports();
      await advance(240);
      expect(hit()?.textContent).toContain(latest.text);
    } else if (action === "End key") {
      act(() => window.dispatchEvent(new KeyboardEvent("keydown", { key: "End" })));
    } else if (action === "touch") {
      const swipe = (type: string, clientY: number) => new TouchEvent(type, { touches: [{ clientY } as Touch] });
      act(() => scroller().dispatchEvent(swipe("touchstart", 200)));
      act(() => scroller().dispatchEvent(swipe("touchmove", 160)));
    } else {
      const event = action === "wheel" ? new WheelEvent("wheel", { deltaY: 100 })
        : new Event("pointerdown");
      act(() => scroller().dispatchEvent(event));
    }
    transcript.items = [question, hidden, latest];
    await render(action === "another chat" ? "two" : "one", action === "another target" ? latest.id : undefined);
    await release();
    await advance(800);
    if (action === "another target") {
      expect(scrolled).toHaveBeenCalledTimes(1);
      expect(hit()?.textContent).toContain(latest.text);
    } else {
      expect(scrolled).not.toHaveBeenCalled();
      expect(hit()).toBeNull();
      expect(host.querySelector("[data-jump-pill]")).toBeNull();
    }
  },
);

it("a link still waiting for the first page yields to the reader", async () => {
  transcript.loading = true;
  await render("one", question.id);
  act(() => scroller().dispatchEvent(new WheelEvent("wheel", { deltaY: -100 })));
  transcript.loading = false;
  await render("one", question.id);
  await imports();
  await advance(250);
  expect(scrolled).not.toHaveBeenCalled();
  expect(hit()).toBeNull();
});

it("Jump to bottom cancels a link while its row is settling", async () => {
  await render("one", latest.id);
  await imports();
  await advance(20);
  const pill = host.querySelector<HTMLButtonElement>("[data-jump-pill]");
  expect(pill).toBeTruthy();
  act(() => pill!.click());
  await advance(800);
  expect(scrolled).not.toHaveBeenCalled();
  expect(hit()).toBeNull();
  expect(host.querySelector("[data-jump-pill]")).toBeNull();
});

it("a forwarded original still lands after the first page arrives under StrictMode", async () => {
  jumpWhenOpen("m", "one", latest.id);
  const chat = () => act(async () => root.render(<StrictMode><ChatView mid="m" sid="one" inPage /></StrictMode>));
  transcript.loading = true;
  await chat();
  transcript.loading = false;
  await chat();
  await imports();
  await advance(800);
  expect(scrolled).toHaveBeenCalledTimes(1);
  expect(hit()?.textContent).toContain(latest.text);
});

it("lands on a folded search hit when the reader keeps the selection", async () => {
  const release = await pickFoldedHit();
  transcript.items = [question, hidden, latest];
  await render();
  await release();
  await advance(800);
  expect(scrolled).toHaveBeenCalledTimes(1);
  expect(hit()?.textContent).toContain(hidden.text);
});
