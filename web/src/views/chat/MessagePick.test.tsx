// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { initI18n } from "../../i18n";
import { onOpenForward, type ForwardBatch, type ForwardChat, type ForwardPick } from "../../lib/forward";
import type { Item } from "../../lib/types";
import { FORWARD_MOST, INCLUDE_KEY, messagePick, type MessagePick } from "../../lib/message-pick";
import { ChatActionsContext, type ChatActions } from "./chat-actions";
import { MessageActions } from "./MessageActions";
import { PickBar, PickRow } from "./Picking";
import { pickParts } from "./picking-parts";

// Picking messages to forward at once (#3838): while the chat holds picks, each message with a Forward has a checkbox
// over it (its actions off), the bar counts them, Forward is off with none, and Cancel or Escape hand the chat back as
// it was. Not picking, a message is drawn as it always was.

const toast = vi.hoisted(() => vi.fn());
vi.mock("../../ui/atoms", async (original) => ({ ...(await original<typeof import("../../ui/atoms")>()), toast }));
vi.hoisted(() => {
  window.matchMedia = ((query: string) => ({
    matches: false,
    media: query,
    onchange: null,
    addEventListener() {},
    removeEventListener() {},
    addListener() {},
    removeListener() {},
    dispatchEvent: () => false,
  })) as typeof window.matchMedia;
});

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let host: HTMLDivElement;
let root: Root;
let pick: MessagePick;
const p = (itemId: string): ForwardPick => ({ itemId, text: `words of ${itemId}`, isUser: false });

beforeEach(async () => {
  await initI18n("en");
  toast.mockClear();
  // Forward to's Select messages… hands these over as it starts picking
  pickParts.Row = PickRow;
  pickParts.Bar = PickBar;
  pick = messagePick();
  pick.start(p("u#1"));
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(async () => {
  act(() => root.unmount());
  host.remove();
  await initI18n("zh-Hans");
});

/** The chat: picking while it holds `picking` (ChatView sets ChatActions.pick only then), with its bar when `bar`. */
const from: ForwardChat = { mid: "m", sid: "s", assistant: false, title: "Network tests", items: [{ id: "u#1" }, { id: "r#2" }] as Item[] };

function renderChat(picking: MessagePick | undefined, bar?: { onEnd?: () => void }) {
  const chat: ChatActions = { draftKey: "m/s", canCompose: true, speaker: "Agent", title: "Network tests", forward: () => {}, pick: picking, images: () => [] };
  act(() =>
    root.render(
      <ChatActionsContext.Provider value={chat}>
        <MessageActions text="How many times does it retry?" isUser side messageId="u#1">
          <p>How many times does it retry?</p>
        </MessageActions>
        <MessageActions text="Three times." itemId="r#2">
          <a href="#somewhere">Three times.</a>
        </MessageActions>
        <MessageActions text="Still streaming">
          <p>Still streaming</p>
        </MessageActions>
        {picking && bar && pickParts.Bar && <pickParts.Bar pick={picking} chat={() => from} onEnd={bar.onEnd ?? (() => {})} />}
      </ChatActionsContext.Provider>,
    ),
  );
}

const boxes = () => [...host.querySelectorAll<HTMLElement>('[role="checkbox"]')];

describe("picking messages in a chat", () => {
  it("draws a message as it always was while the chat is not picking", () => {
    renderChat(undefined);
    expect(boxes()).toHaveLength(0);
    expect(host.querySelector("[inert], .isolate, [data-pick]")).toBeNull();
  });

  it("puts a checkbox over each message with a Forward, the first one picked, and a click picks or unpicks it", () => {
    renderChat(pick);
    // the streaming reply has no Forward, so it is not one to pick
    expect(boxes().map((b) => [b.getAttribute("aria-checked"), b.dataset.pick])).toEqual([
      ["true", "on"],
      ["false", "off"],
    ]);
    act(() => boxes()[1].click());
    expect(pick.picks().map((x) => x.itemId)).toEqual(["u#1", "r#2"]);
    expect(boxes()[1].getAttribute("aria-checked")).toBe("true");
    act(() => boxes()[0].click());
    expect(pick.picks().map((x) => x.itemId)).toEqual(["r#2"]);
    // what is in a message does not take the click while picking (a link in a reply)
    expect(host.querySelector("a")?.closest("[inert]")).not.toBeNull();
    expect(boxes()[1].getAttribute("aria-label")).toBe("Three times.");
  });

  it("opens no menu while picking", async () => {
    renderChat(undefined);
    const rightClick = () => act(async () => {
      host.querySelector("p")!.dispatchEvent(new MouseEvent("contextmenu", { bubbles: true, cancelable: true, clientX: 4, clientY: 4 }));
      await vi.dynamicImportSettled();
    });
    await rightClick();
    expect(document.querySelector('[role="menu"]')).not.toBeNull(); // as it does when not picking
    renderChat(pick);
    expect(document.querySelector('[role="menu"]')).toBeNull(); // the open one closes as picking starts
    await rightClick();
    expect(document.querySelector('[role="menu"]')).toBeNull();
  });

  it(`refuses more than ${FORWARD_MOST}, the bar saying so`, () => {
    renderChat(pick, {});
    act(() => {
      for (let i = 1; i < FORWARD_MOST; i++) pick.toggle(p(`x${i}`));
    });
    act(() => boxes()[1].click());
    expect(pick.has("r#2")).toBe(false);
    expect(toast).toHaveBeenCalledWith(`Up to ${FORWARD_MOST} messages at a time`, "error");
  });

  it("is drawn as before once the chat stops picking", () => {
    renderChat(pick);
    renderChat(undefined);
    expect(boxes()).toHaveLength(0);
    expect(host.querySelector("[inert], .isolate")).toBeNull();
  });
});

describe("the pick bar", () => {
  const button = (id: string) => host.querySelector<HTMLButtonElement>(`[data-testid="${id}"]`)!;

  it("counts the picked messages and forwards them in the chat's order; Forward is off with none", () => {
    const opened = vi.fn<(b: ForwardBatch) => void>();
    const off = onOpenForward(opened);
    const onEnd = vi.fn();
    pick = messagePick();
    pick.start(p("r#2"));
    renderChat(pick, { onEnd });
    expect(host.querySelector("[data-pick-count]")?.textContent).toBe("1 selected");
    expect(button("pick-forward").textContent).toContain("Forward"); // the count is in the middle (#9469)
    expect(button("pick-copy").textContent).toContain("Copy");
    expect(button("pick-export").textContent).toContain("Export image");
    act(() => boxes()[0].click());
    expect(host.querySelector("[data-pick-count]")?.textContent).toBe("2 selected");
    act(() => button("pick-forward").click());
    off();
    // Forward to opens with them in the chat's order, and they are dropped once they went
    const batch = opened.mock.calls[0][0];
    expect(batch.drafts!.map((d) => d.source.item)).toEqual(["u#1", "r#2"]);
    expect(batch.select).toBeUndefined();
    batch.sent?.();
    expect(onEnd).toHaveBeenCalledOnce();
    act(() => boxes()[0].click());
    act(() => boxes()[1].click());
    expect(button("pick-forward").disabled).toBe(true);
  });

  it.each([
    ["zh-Hans", "已选 2 条", "转发"],
    ["ja", "2件を選択", "転送"],
  ])("speaks %s", async (lang, count, forward) => {
    await initI18n(lang);
    renderChat(pick, {});
    act(() => boxes()[1].click());
    expect(host.querySelector("[data-pick-count]")?.textContent).toBe(count);
    expect(button("pick-forward").textContent).toContain(forward);
  });

  it("offers Include questions while a picked reply's question is not picked, and forwards it with the reply (#9469)", () => {
    const opened = vi.fn<(b: ForwardBatch) => void>();
    const off = onOpenForward(opened);
    const asked: ForwardPick = { itemId: "u#1", text: "How many times does it retry?", isUser: true };
    pick = messagePick();
    pick.setQuestions(() => new Map([["r#2", [asked]]]));
    pick.start(p("r#2"));
    const fromWith: ForwardChat = { ...from, items: [{ id: "u#1", seq: 1, role: "user", kind: "text", text: asked.text }, { id: "r#2", seq: 2, role: "assistant", kind: "text", text: "Three times." }] as Item[] };
    const chat: ChatActions = { draftKey: "m/s", canCompose: true, speaker: "Agent", title: "Network tests", forward: () => {}, pick, images: () => [] };
    act(() => root.render(<ChatActionsContext.Provider value={chat}><PickBar pick={pick} chat={() => fromWith} onEnd={() => {}} /></ChatActionsContext.Provider>));
    const questions = host.querySelector<HTMLButtonElement>("[data-pick-questions]");
    expect(questions?.getAttribute("aria-checked")).toBe("true");
    act(() => button("pick-forward").click());
    expect(opened.mock.calls[0][0].drafts!.map((d) => d.source.item)).toEqual(["u#1", "r#2"]);
    act(() => questions!.click());
    expect(host.querySelector("[data-pick-questions]")?.getAttribute("aria-checked")).toBe("false");
    act(() => button("pick-forward").click());
    expect(opened.mock.calls[1][0].drafts!.map((d) => d.source.item)).toEqual(["r#2"]);
    off();
    localStorage.removeItem(INCLUDE_KEY);
  });

  it("is started from the message menu's last row, Select messages (#9469)", async () => {
    const forward = vi.fn();
    const chat: ChatActions = { draftKey: "m/s", canCompose: true, speaker: "Agent", title: "Network tests", forward, images: () => [] };
    act(() =>
      root.render(
        <ChatActionsContext.Provider value={chat}>
          <MessageActions text="Three times." itemId="r#2">
            <p data-words>Three times.</p>
          </MessageActions>
        </ChatActionsContext.Provider>,
      ),
    );
    const words = host.querySelector<HTMLElement>("[data-words]")!;
    await act(async () => {
      words.dispatchEvent(new MouseEvent("contextmenu", { bubbles: true, cancelable: true, clientX: 20, clientY: 20 }));
      await vi.dynamicImportSettled();
    });
    const last = [...document.querySelectorAll<HTMLElement>('[role="menuitem"]')].at(-1)!;
    expect(last.textContent).toBe("Select messages");
    act(() => last.click());
    // Forward with `now`: Forward to's chunk starts picking with it, the picker not shown
    expect(forward).toHaveBeenCalledWith(expect.objectContaining({ itemId: "r#2", isUser: false }), true);
  });

  it("hands the chat back with Cancel or Escape", () => {
    const onEnd = vi.fn();
    renderChat(pick, { onEnd });
    act(() => button("pick-cancel").click());
    act(() => window.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape" })));
    expect(onEnd).toHaveBeenCalledTimes(2);
  });
});
