// @vitest-environment jsdom
// A quote reply answers something above the newest end (#10346): sending it must not pull an earlier reader away.
// Ordinary sends keep their existing follow-to-end behavior, including a hand-written Markdown blockquote.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Item, Session } from "../../lib/types";

vi.hoisted(() => {
  const g = globalThis as Record<string, unknown>;
  g.matchMedia = () => ({ matches: false, addEventListener() {}, removeEventListener() {} });
  g.ResizeObserver = class { observe() {} unobserve() {} disconnect() {} };
  g.IntersectionObserver = class { observe() {} unobserve() {} disconnect() {} takeRecords() { return []; } };
  g.CSS ??= { escape: (s: string) => s.replace(/["\\]/g, "\\$&") };
});

const transcript = vi.hoisted(() => ({ items: [] as Item[], loading: false, error: "", hasMore: false, streaming: "" }));
vi.mock("./useTranscript", () => ({ useTranscript: () => transcript }));
vi.mock("../../lib/panel", async (importOriginal) => ({ ...await importOriginal<typeof import("../../lib/panel")>(), usePanel: () => null }));
vi.mock("../../lib/router", async (importOriginal) => ({ ...await importOriginal<typeof import("../../lib/router")>(), useRoute: () => ({ name: "session", mid: "m", sid: "s" }) }));

const { ChatView } = await import("./ChatView");
const { reset, upsertSession } = await import("../../lib/store");
const { addQuote, setQuotes } = await import("../../lib/drafts");
const { t } = await import("../../i18n");
(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const KEY = "m/s";
const items: Item[] = [
  { id: "u1#0", seq: 1, role: "user", kind: "text", text: "Earlier question", ts: "2026-01-01T00:00:00Z" },
  { id: "a2#0", seq: 2, role: "assistant", kind: "text", text: "The earlier answer to quote", ts: "2026-01-01T00:01:00Z" },
  { id: "a3#0", seq: 3, role: "assistant", kind: "text", text: "The newest answer", ts: "2026-01-01T00:02:00Z" },
];

let host: HTMLDivElement;
let root: Root;
let fetcher: ReturnType<typeof vi.fn>;
const flush = async () => {
  for (let i = 0; i < 6; i++) await act(async () => { await new Promise((r) => setTimeout(r, 0)); });
  await act(async () => { await vi.dynamicImportSettled(); });
};
const scroller = () => host.querySelector<HTMLElement>("[data-chat-scroll]")!;
const sendButton = () => host.querySelector<HTMLButtonElement>(`button[aria-label="${t("chat-composer.send")}"]`)!;

async function mountChat() {
  act(() => root.render(<ChatView mid="m" sid="s" onToggleSidebar={() => {}} />));
  await flush();
  // The mount effect marks the initial transcript for following. Commit one transcript revision, as the live load does,
  // so this test starts from a settled chat rather than sending during that initial landing.
  transcript.items = [...items];
  await act(async () => root.render(<ChatView mid="m" sid="s" onToggleSidebar={() => {}} />));
  const sc = scroller();
  Object.defineProperty(sc, "scrollHeight", { configurable: true, value: 3000 });
  Object.defineProperty(sc, "clientHeight", { configurable: true, value: 500 });
  sc.scrollTop = 3000;
  await act(async () => sc.dispatchEvent(new Event("scroll", { bubbles: true })));
}

async function readEarlier() {
  const sc = scroller();
  sc.scrollTop = 1200;
  await act(async () => sc.dispatchEvent(new WheelEvent("wheel", { deltaY: -120, bubbles: true })));
  expect(host.querySelector("[data-jump-pill]")).not.toBeNull();
}

async function send(text: string, quote = false) {
  act(() => {
    const box = host.querySelector("textarea")!;
    Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")!.set!.call(box, text);
    box.dispatchEvent(new Event("input", { bubbles: true }));
    // A real Quote action joins the box after its words are typed; do not use draft replacement, which means Edit.
    if (quote) addQuote(KEY, { text: "The earlier answer to quote" });
  });
  await act(async () => sendButton().click());
  await flush();
  expect(fetcher.mock.calls.some(([url]) => String(url).endsWith("/sessions/m/s/messages"))).toBe(true);
}

beforeEach(() => {
  localStorage.clear();
  setQuotes(KEY, []);
  transcript.items = items;
  upsertSession({ machine_id: "m", session_id: "s", title: "Quoted replies", status: "idle", state: "idle", agent: "claude", cwd: "/work" } as Session);
  fetcher = vi.fn(async () => new Response("{}", { status: 200, headers: { "Content-Type": "application/json" } }));
  vi.stubGlobal("fetch", fetcher);
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
  setQuotes(KEY, []);
  reset();
  vi.unstubAllGlobals();
});

describe("sending while reading an earlier message (#10346)", () => {
  it("keeps the reader's place for a genuine Composer quote reply", async () => {
    await mountChat();
    await readEarlier();
    await send("Answering the earlier point", true);
    expect(scroller().scrollTop).toBe(1200);
  });

  it("still follows a quote reply sent from the bottom", async () => {
    await mountChat();
    await send("Answering from the newest end", true);
    expect(scroller().scrollTop).toBe(3000);
  });

  it("still follows an ordinary send, including hand-written Markdown quotes", async () => {
    await mountChat();
    await readEarlier();
    await send("> A hand-written blockquote\n\nAn ordinary reply");
    expect(scroller().scrollTop).toBe(3000);
  });
});
