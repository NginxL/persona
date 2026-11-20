// @vitest-environment jsdom
import { act } from "react";
import { createRoot } from "react-dom/client";
import { expect, it, vi } from "vitest";

const loading = vi.hoisted(() => {
  let release!: () => void;
  const ready = new Promise<void>((done) => { release = done; });
  return { ready, release, started: vi.fn(), copied: vi.fn(async () => true) };
});
vi.mock("./ReplyBar", async (original) => {
  loading.started();
  await loading.ready;
  return {
    ...await original<typeof import("./ReplyBar")>(),
    placeSideBar: (_row: HTMLElement, _bar: HTMLElement, place: (below: boolean) => void) => place(false),
  };
});
vi.mock("../../ui/atoms", async (original) => ({
  ...await original<typeof import("../../ui/atoms")>(),
  copyText: loading.copied,
}));
vi.mock("../../ui/media-query", async (original) => ({
  ...await original<typeof import("../../ui/media-query")>(),
  isNoHover: () => true,
}));

import { initI18n, t } from "../../i18n";
import { MessageActions } from "./MessageActions";
import { ChatActionsContext } from "./chat-actions";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

it("loads message menus and text selection only on intent, preserving the message, copy and dialog exit", async () => {
  await initI18n("en");
  const text = "First **line**\n\nSecond line.";
  const fork = vi.fn();
  const host = document.createElement("div");
  document.body.append(host);
  const root = createRoot(host);
  const animation = Object.getOwnPropertyDescriptor(Element.prototype, "animate");
  Object.defineProperty(Element.prototype, "animate", {
    configurable: true,
    value: () => ({ finished: new Promise(() => {}), cancel() {} }) as unknown as Animation,
  });
  const item = (label: string) => [...document.querySelectorAll<HTMLButtonElement>('[role="menuitem"]')]
    .find((button) => button.textContent?.trim() === label)!;
  const open = async () => {
    await act(async () => host.querySelector("[data-original]")!.dispatchEvent(
      new MouseEvent("contextmenu", { bubbles: true, cancelable: true, clientX: 20, clientY: 24 }),
    ));
  };
  const settle = async () => {
    await act(async () => { await vi.dynamicImportSettled(); });
    await act(async () => { await vi.dynamicImportSettled(); });
  };
  try {
    await act(async () => root.render(
      <ChatActionsContext.Provider value={{ draftKey: "m/s", canCompose: false, speaker: "Agent", title: "Chat", images: () => [], fork }}>
        <MessageActions text={text} itemId="u1#0" isUser side><p data-original>{text}</p></MessageActions>
      </ChatActionsContext.Provider>,
    ));
    expect(loading.started).not.toHaveBeenCalled();
    const copyMessage = host.querySelector<HTMLButtonElement>(`button[aria-label="${t("chat-messageactions.copy_message")}"]`)!;
    await act(async () => { copyMessage.focus(); });
    await vi.waitFor(() => expect(loading.started).toHaveBeenCalledTimes(1));
    expect(host.querySelector("[data-side-bar]")?.getAttribute("data-side-bar")).toBe("below");
    expect(document.activeElement).toBe(copyMessage);
    await open();
    await vi.waitFor(() => expect(loading.started).toHaveBeenCalledTimes(1));
    expect(host.querySelector("[data-original]")?.textContent).toBe(text);
    expect(document.querySelector('[role="dialog"]')).toBeNull();
    loading.release();
    await settle();
    expect(host.querySelector("[data-side-bar]")?.getAttribute("data-side-bar")).toBe("beside");
    expect(copyMessage.isConnected).toBe(true);
    await act(async () => item(t("chat-messageactions.fork")).click());
    expect(fork).toHaveBeenCalledExactlyOnceWith("u1#0");
    await open();
    await settle();
    await act(async () => item(t("chat-messageactions.select_text")).click());
    await settle();
    const dialog = () => document.querySelector('[role="dialog"]')!;
    expect(dialog().textContent).toContain(text);
    const selection = dialog().querySelector<HTMLElement>(".select-text")!;
    expect(selection.textContent).toBe(text);
    expect(selection.classList.contains("text-sm")).toBe(true);
    expect(selection.classList.contains("whitespace-pre-wrap")).toBe(true);
    const copy = [...dialog().querySelectorAll<HTMLButtonElement>("button")]
      .find((button) => button.textContent === t("chat-messageactions.copy_all"))!;
    await act(async () => copy.click());
    expect(loading.copied).toHaveBeenCalledExactlyOnceWith(text);
    expect(document.querySelector("[data-closing]")).not.toBeNull();
    await vi.waitFor(() => expect(document.querySelector('[role="dialog"]')).toBeNull());
    expect(host.querySelector("[data-original]")?.textContent).toBe(text);
    expect(loading.started).toHaveBeenCalledTimes(1);
  } finally {
    loading.release();
    act(() => root.unmount());
    host.remove();
    if (animation) Object.defineProperty(Element.prototype, "animate", animation);
    else Reflect.deleteProperty(Element.prototype, "animate");
  }
});
