// @vitest-environment jsdom
// Long chats mount completed rich bodies near the viewport, without moving their section anchors or withholding search hits.
import { act, type ReactNode } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { emptyTaskIndex } from "../../lib/tasks";
import { chatLines, type Segment } from "../../lib/turns";
import type { Item } from "../../lib/types";
import { ChatContext } from "./chat-context";
import { clearReveal, revealItem } from "./search-reveal";
import { SegmentView } from "./SegmentView";

vi.mock("../../ui/Markdown", () => ({ Markdown: ({ text }: { text: string }) => <div data-markdown>{text}</div> }));
vi.mock("./MessageActions", () => ({ MessageActions: ({ children }: { children?: ReactNode }) => <>{children}</> }));
vi.mock("./FileCards", () => ({ FileCards: () => null, ReplyFiles: () => null }));
vi.mock("./PortChips", () => ({ ReplyPorts: () => null }));
vi.mock("../../lib/message-send", async (importOriginal) => ({ ...await importOriginal<typeof import("../../lib/message-send")>(), useMessageFeedback: () => "" }));

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

class FakeIntersectionObserver {
  static all: FakeIntersectionObserver[] = [];
  readonly observed = new Set<Element>();
  disconnected = false;
  constructor(private callback: IntersectionObserverCallback, readonly options?: IntersectionObserverInit) {
    FakeIntersectionObserver.all.push(this);
  }
  observe(el: Element) { this.observed.add(el); }
  unobserve(el: Element) { this.observed.delete(el); }
  disconnect() { this.disconnected = true; this.observed.clear(); }
  fire(isIntersecting: boolean) {
    this.callback([{ isIntersecting } as IntersectionObserverEntry], this as unknown as IntersectionObserver);
  }
}

function segment(): Segment {
  const items: Item[] = [
    { id: "u1#0", seq: 1, role: "user", kind: "text", text: "Find the synthetic question" },
    { id: "a2#0", seq: 2, role: "assistant", kind: "text", text: "The **synthetic answer** is ready." },
  ];
  const line = chatLines(items).find((l) => l.kind === "segment");
  if (!line || line.kind !== "segment") throw new Error("fixture has no exchange");
  return line.seg;
}

describe("deferred long-chat exchanges", () => {
  let scroller: HTMLDivElement;
  let host: HTMLDivElement;
  let root: Root;
  const offscreen = vi.fn(() => vi.fn());
  const windowDrawn = vi.fn();

  beforeEach(() => {
    FakeIntersectionObserver.all = [];
    vi.stubGlobal("IntersectionObserver", FakeIntersectionObserver);
    scroller = document.createElement("div");
    scroller.setAttribute("data-chat-scroll", "");
    host = document.createElement("div");
    scroller.append(host);
    document.body.append(scroller);
    root = createRoot(host);
  });

  afterEach(async () => {
    await act(async () => root.unmount());
    scroller.remove();
    clearReveal();
    vi.unstubAllGlobals();
    offscreen.mockClear();
    windowDrawn.mockClear();
  });

  function draw(seg: Segment, watch?: typeof offscreen, live = false) {
    root.render(
      <ChatContext.Provider value={{ tasks: emptyTaskIndex(), subagents: [], scope: "m/s:", windowHistory: true, windowDrawn }}>
        <SegmentView seg={seg} live={live} mid="m" sid="s" streaming={live ? "Live synthetic words" : undefined} expandFold={async () => {}} offscreen={watch} />
      </ChatContext.Provider>,
    );
  }

  it("keeps the section and estimate, mounts near the viewport, and stays mounted", async () => {
    const seg = segment();
    await act(async () => draw(seg, offscreen));
    const section = host.querySelector("section[data-segment]");
    expect(section?.getAttribute("style")).toContain("--seg-est");
    expect(host.querySelector("[data-segment-placeholder]")?.textContent).toContain("Find the synthetic question");
    expect(host.querySelector("[data-segment-placeholder]")?.getAttribute("style")).toContain("block-size: var(--seg-est)");
    expect(host.querySelector("[data-markdown]")).toBeNull();
    const observer = FakeIntersectionObserver.all[0]!;
    expect(observer.options).toMatchObject({ root: scroller, rootMargin: "100% 0px" });
    expect(observer.observed.has(section!)).toBe(true);

    await act(async () => observer.fire(true));
    expect(host.querySelector("[data-segment-placeholder]")).toBeNull();
    expect(host.querySelector("[data-markdown]")?.textContent).toBe("The **synthetic answer** is ready.");
    expect(windowDrawn).toHaveBeenCalledWith(section, 0);
    expect(observer.disconnected).toBe(true);
    await act(async () => draw(seg, offscreen));
    expect(host.querySelector("[data-markdown]")).not.toBeNull();
  });

  it("mounts the exact search target immediately and keeps it after reveal clears", async () => {
    await act(async () => draw(segment(), offscreen));
    expect(host.querySelector('[data-items="a2#0"]')).toBeNull();
    await act(async () => revealItem("m/s:", "a2#0"));
    expect(host.querySelector("[data-segment-placeholder]")).toBeNull();
    expect(host.querySelector('[data-items="a2#0"]')).not.toBeNull();
    await act(async () => clearReveal());
    expect(host.querySelector('[data-items="a2#0"]')).not.toBeNull();
  });

  it.each([false, true])("does not defer an immediately drawn exchange (live=%s), including when it later becomes off-screen", async (live) => {
    const seg = segment();
    await act(async () => draw(seg, undefined, live));
    expect(host.querySelector("[data-segment-placeholder]")).toBeNull();
    expect(host.textContent).toContain(live ? "Live synthetic words" : "The **synthetic answer** is ready.");
    expect(FakeIntersectionObserver.all).toHaveLength(0);

    await act(async () => draw(seg, offscreen, live));
    expect(host.querySelector("[data-segment-placeholder]")).toBeNull();
    expect(FakeIntersectionObserver.all).toHaveLength(0);
  });
});
