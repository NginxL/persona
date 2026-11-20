// @vitest-environment jsdom
// `/model` and `/permissions` alone open their pickers from the send buttons too, not only from Enter: a touch screen's
// return is a new line, so the round Send (and the full editor's) is how a phone sends them (#11442).
import { act, type ComponentProps } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { loadPages } from "../../lib/lazy-page";
import { onModelPicker } from "../../lib/model-switch";
import { onPermissionPicker } from "../../lib/permissions-command";
import { Composer, type Outgoing } from "./Composer";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const KEY = "commands";
let fine = true;
let phone = false;
let host: HTMLDivElement;
let root: Root;
const cleanups: (() => void)[] = [];
const scroll = Object.getOwnPropertyDescriptor(HTMLElement.prototype, "scrollHeight");

beforeAll(async () => {
  Element.prototype.scrollIntoView ??= () => {};
  await loadPages();
});

beforeEach(() => {
  fine = true;
  phone = false;
  localStorage.clear();
  // jsdom lays nothing out: a line per "\n", 24px each, so a long draft offers the phone's full editor
  Object.defineProperty(HTMLTextAreaElement.prototype, "scrollHeight", {
    configurable: true,
    get(this: HTMLTextAreaElement) { return this.value.split("\n").length * 24; },
  });
  // lib/viewport.ts keeps one list per query, so the switches are read live
  vi.stubGlobal("matchMedia", (q: string) => ({
    media: q,
    get matches() { return q.includes("max-width: 639px") ? phone : q.includes("fine") && fine; },
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
  if (scroll) Object.defineProperty(HTMLTextAreaElement.prototype, "scrollHeight", scroll);
  else delete (HTMLTextAreaElement.prototype as { scrollHeight?: number }).scrollHeight;
  vi.unstubAllGlobals();
});

const mount = (props: Partial<ComponentProps<typeof Composer>> = {}) =>
  act(() => root.render(<Composer draftKey={KEY} placeholder="" onSend={async () => true} {...props} />));
const box = () => host.querySelector<HTMLTextAreaElement>("textarea")!;
const type = (el: HTMLTextAreaElement, value: string) =>
  act(() => {
    Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")!.set!.call(el, value);
    el.setSelectionRange(value.length, value.length);
    el.dispatchEvent(new Event("input", { bubbles: true }));
  });
const tap = (el: HTMLElement) => act(async () => { el.click(); await Promise.resolve(); });
const send = () => host.querySelector<HTMLButtonElement>('[data-testid="composer-send"]')!;
const editor = () => document.querySelector<HTMLElement>('[data-testid="composer-expanded"]');
const pickers = () => {
  const model = vi.fn(), permissions = vi.fn();
  cleanups.push(onModelPicker(KEY, model), onPermissionPicker(KEY, permissions));
  return { model, permissions };
};

describe("the send button with a picker command", () => {
  for (const pointer of ["fine", "coarse"] as const) {
    it(`opens the model panel for "/model " (as the palette fills it in) instead of sending, ${pointer} pointer`, async () => {
      fine = pointer === "fine";
      const onSend = vi.fn(async (_m: Outgoing) => true);
      const { model, permissions } = pickers();
      mount({ onSend });
      type(box(), "/model ");
      await tap(send());
      expect(model).toHaveBeenCalledTimes(1);
      expect(permissions).not.toHaveBeenCalled();
      expect(onSend).not.toHaveBeenCalled();
      expect(box().value).toBe("");
    });

    it(`opens the permission menu for "/permissions" instead of sending, ${pointer} pointer`, async () => {
      fine = pointer === "fine";
      const onSend = vi.fn(async (_m: Outgoing) => true);
      const { model, permissions } = pickers();
      mount({ onSend });
      type(box(), "/permissions");
      await tap(send());
      expect(permissions).toHaveBeenCalledTimes(1);
      expect(model).not.toHaveBeenCalled();
      expect(onSend).not.toHaveBeenCalled();
      expect(box().value).toBe("");
    });
  }

  it("sends the words as typed where the chat has no picker for them", async () => {
    const onSend = vi.fn(async (_m: Outgoing) => true);
    mount({ onSend });
    type(box(), "/model");
    await tap(send());
    type(box(), "/permissions");
    await tap(send());
    expect(onSend.mock.calls.map(([m]) => m.text)).toEqual(["/model", "/permissions"]);
  });

  it("sends a message that only starts with the command", async () => {
    const onSend = vi.fn(async (_m: Outgoing) => true);
    const { model } = pickers();
    mount({ onSend });
    type(box(), "/model opus");
    await tap(send());
    expect(model).not.toHaveBeenCalled();
    expect(onSend.mock.calls.map(([m]) => m.text)).toEqual(["/model opus"]);
  });

  it("opens it from the phone's full editor too, closing the editor", async () => {
    phone = true;
    fine = false;
    const onSend = vi.fn(async (_m: Outgoing) => true);
    const { model } = pickers();
    mount({ onSend });
    type(box(), Array.from({ length: 9 }, (_, i) => `step ${i + 1}`).join("\n"));
    act(() => host.querySelector<HTMLButtonElement>('[data-testid="composer-expand"]')!.click());
    await act(async () => { await vi.waitFor(() => expect(editor()).not.toBeNull()); });
    type(editor()!.querySelector("textarea")!, "/model");
    await tap(editor()!.querySelector<HTMLButtonElement>('[data-testid="composer-expanded-send"]')!);
    expect(model).toHaveBeenCalledTimes(1);
    expect(onSend).not.toHaveBeenCalled();
    expect(editor()).toBeNull();
    expect(box().value).toBe("");
  });
});
