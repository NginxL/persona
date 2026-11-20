// @vitest-environment jsdom
// Keyboard sending must not depend on a browser's sometimes-wrong precise-pointer media query (#8249).
import { act, type ComponentProps } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { loadPages } from "../../lib/lazy-page";
import { onModelPicker } from "../../lib/model-switch";
import { onPermissionPicker } from "../../lib/permissions-command";
import { Composer, type Outgoing } from "./Composer";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let host: HTMLDivElement;
let root: Root;
const cleanups: (() => void)[] = [];

beforeAll(async () => {
  Element.prototype.scrollIntoView ??= () => {};
  await loadPages();
});

beforeEach(() => {
  localStorage.clear();
  vi.stubGlobal("matchMedia", (query: string) => ({
    media: query,
    matches: false,
    addEventListener() {},
    removeEventListener() {},
  }));
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
  cleanups.splice(0).forEach((off) => off());
  vi.unstubAllGlobals();
});

const mount = (props: Partial<ComponentProps<typeof Composer>> = {}) =>
  act(() => root.render(<Composer draftKey="keyboard" placeholder="" onSend={async () => true} {...props} />));
const box = () => host.querySelector<HTMLTextAreaElement>("textarea")!;
const valueSetter = Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")!.set!;
const setValue = (el: HTMLTextAreaElement, value: string) => {
  valueSetter.call(el, value);
  el.dispatchEvent(new Event("input", { bubbles: true }));
};
const type = async (value: string) => act(async () => {
  const el = box();
  valueSetter.call(el, value);
  el.setSelectionRange(value.length, value.length);
  el.dispatchEvent(new Event("input", { bubbles: true }));
  await Promise.resolve();
});
const press = async (extra: KeyboardEventInit = {}) => {
  const event = new KeyboardEvent("keydown", { key: "Enter", bubbles: true, cancelable: true, ...extra });
  await act(async () => {
    box().dispatchEvent(event);
    await Promise.resolve();
  });
  return event;
};
const plainEnter = async () => {
  const el = box();
  const allowed = await press();
  if (!allowed.defaultPrevented) {
    // jsdom has no native textarea default action; carry out the newline the browser would insert.
    const start = el.selectionStart, end = el.selectionEnd;
    await act(async () => {
      setValue(el, `${el.value.slice(0, start)}\n${el.value.slice(end)}`);
      el.setSelectionRange(start + 1, start + 1);
    });
  }
  return allowed;
};
const rowReady = async (selector: string) => act(async () => {
  await vi.waitFor(() => expect(host.querySelector(selector)).not.toBeNull());
});

describe.sequential("composer keyboard sending without a reliable pointer query", () => {
  it("sends on the first Ctrl+Enter with a selectable slash row open", async () => {
    const onSend = vi.fn(async (_m: Outgoing) => true);
    mount({ commands: [{ word: "/compact", desc: "Compact", text: "/compact" }], onSend });
    await type("/compact");
    await rowReady("[data-slash-palette] [role=option]");
    expect((await press({ ctrlKey: true })).defaultPrevented).toBe(true);
    expect(onSend.mock.calls.map(([message]) => message.text)).toEqual(["/compact"]);
    expect(box().value).toBe("");
  });

  it("sends on the first Cmd+Enter with a selectable mention row open", async () => {
    const onSend = vi.fn(async (_m: Outgoing) => true);
    mount({ mention: { files: () => ["/code/app/lib/links.ts"] }, onSend });
    act(() => box().focus());
    await type("see @links");
    await rowReady('[data-composer-panel="at"] [role=option]');
    expect((await press({ metaKey: true })).defaultPrevented).toBe(true);
    expect(onSend.mock.calls.map(([message]) => message.text)).toEqual(["see @links"]);
    expect(box().value).toBe("");
  });
  it("keeps plain Enter as a newline, sends Ctrl/Cmd+Enter and the command pickers, then promotes Enter after a mouse", async () => {
    const onSend = vi.fn(async (_m: Outgoing) => true);
    mount({ onSend });
    await type("first");
    await plainEnter();
    expect(box().value).toBe("first\n");
    expect(onSend).not.toHaveBeenCalled();

    await type("from ctrl");
    expect((await press({ ctrlKey: true })).defaultPrevented).toBe(true);
    await type("from cmd");
    expect((await press({ metaKey: true })).defaultPrevented).toBe(true);

    const model = vi.fn(), permissions = vi.fn();
    cleanups.push(onModelPicker("keyboard", model), onPermissionPicker("keyboard", permissions));
    await type("/model");
    await press({ ctrlKey: true });
    expect(model).toHaveBeenCalledTimes(1);
    expect(onSend).toHaveBeenCalledTimes(2);
    await type("/permissions");
    await press({ metaKey: true });
    expect(permissions).toHaveBeenCalledTimes(1);
    expect(onSend).toHaveBeenCalledTimes(2);

    act(() => window.dispatchEvent(new PointerEvent("pointermove", { pointerType: "mouse", bubbles: true })));
    await type("after mouse");
    expect((await plainEnter()).defaultPrevented).toBe(true);
    expect(onSend.mock.calls.map(([message]) => message.text)).toEqual(["from ctrl", "from cmd", "after mouse"]);
  });

  it("gives the existing blocked nudge on Mod+Enter, once per fresh press", async () => {
    const onNudge = vi.fn(), onSend = vi.fn(async (_m: Outgoing) => true);
    mount({ blocked: { label: "Finish setup", onNudge }, onSend });
    await type("keep me");
    expect((await press({ ctrlKey: true })).defaultPrevented).toBe(true);
    expect(host.querySelector('[data-testid="composer-nudge"]')).not.toBeNull();
    expect(box().value).toBe("keep me");
    await press({ ctrlKey: true, repeat: true });
    expect(onNudge).toHaveBeenCalledTimes(1);
    expect(onSend).not.toHaveBeenCalled();
  });

  it("sends nothing during IME composition, including keyCode 229", async () => {
    const onSend = vi.fn(async (_m: Outgoing) => true);
    mount({ onSend });
    await type("ime draft");
    for (const extra of [
      { ctrlKey: true, isComposing: true },
      { metaKey: true, isComposing: true },
      { ctrlKey: true, keyCode: 229 },
      { isComposing: true },
      { keyCode: 229 },
    ]) {
      expect((await press(extra)).defaultPrevented).toBe(false);
    }
    expect(box().value).toBe("ime draft");
    expect(onSend).not.toHaveBeenCalled();
  });

  it("does not swallow Mod+Alt+Enter or Mod+Shift+Enter before the window shortcut dispatcher, even with a slash palette", async () => {
    const onSend = vi.fn(async (_m: Outgoing) => true);
    mount({ commands: [{ word: "/compact", desc: "Compact", text: "/compact" }], onSend });
    const seen: KeyboardEvent[] = [];
    const listener = (event: KeyboardEvent) => seen.push(event);
    window.addEventListener("keydown", listener);
    try {
      for (const draft of ["draft", "/compact"]) {
        await type(draft);
        for (const extra of [{ ctrlKey: true, altKey: true }, { metaKey: true, altKey: true }, { ctrlKey: true, shiftKey: true }, { metaKey: true, shiftKey: true }]) {
          expect((await press(extra)).defaultPrevented).toBe(false);
        }
      }
    } finally {
      window.removeEventListener("keydown", listener);
    }
    expect(seen).toHaveLength(8);
    expect(box().value).toBe("/compact");
    expect(onSend).not.toHaveBeenCalled();
  });
  it("Ctrl/Cmd+Enter hands the queue over now while the agent works, after the message is queued", async () => {
    const order: string[] = [];
    const onSend = vi.fn(async (m: Outgoing) => { order.push(`send:${m.text}`); return true; });
    const onSendNow = vi.fn(async () => { order.push("now"); });
    mount({ working: true, onSend, onSendNow });
    await type("steer");
    await press({ ctrlKey: true });
    await type("steer too");
    await press({ metaKey: true });
    expect(order).toEqual(["send:steer", "now", "send:steer too", "now"]);
  });

  it("Ctrl+Enter also sends now while the agent waits on you; plain Enter only queues", async () => {
    const onSend = vi.fn(async (_m: Outgoing) => true);
    const onSendNow = vi.fn();
    mount({ needsYou: true, onSend, onSendNow });
    await type("queued");
    await press();
    expect(onSend).toHaveBeenCalledTimes(1);
    expect(onSendNow).not.toHaveBeenCalled();
    await type("now");
    await press({ ctrlKey: true });
    expect(onSendNow).toHaveBeenCalledTimes(1);
  });

  it("Ctrl+Enter on an idle agent is a normal send, and an empty box sends nothing", async () => {
    const onSend = vi.fn(async (_m: Outgoing) => true);
    const onSendNow = vi.fn();
    mount({ onSend, onSendNow });
    await type("hello");
    await press({ ctrlKey: true });
    expect(onSend).toHaveBeenCalledTimes(1);
    expect(onSendNow).not.toHaveBeenCalled();
    mount({ working: true, onSend, onSendNow });
    await type("");
    await press({ ctrlKey: true });
    expect(onSend).toHaveBeenCalledTimes(1);
    expect(onSendNow).not.toHaveBeenCalled();
  });

  it("does not send now when the message did not go", async () => {
    const onSendNow = vi.fn();
    mount({ working: true, onSend: async () => false, onSendNow });
    await type("lost");
    await press({ ctrlKey: true });
    expect(onSendNow).not.toHaveBeenCalled();
  });
});
