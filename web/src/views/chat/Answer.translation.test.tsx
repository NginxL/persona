// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Item } from "../../lib/types";

const request = vi.hoisted(() => vi.fn());
const fullItem = vi.hoisted(() => vi.fn());
vi.mock("../../lib/api", async (original) => ({
  ...await original<typeof import("../../lib/api")>(),
  api: request,
  fullItem,
}));

import { initI18n } from "../../i18n";
import { setToken } from "../../lib/api";
import { LinkManager } from "../../lib/manager-choice";
import { ChatActionsContext, type ChatActions } from "./chat-actions";
import { Answer } from "./SegmentView";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
globalThis.ResizeObserver ??= class {
  observe() {}
  unobserve() {}
  disconnect() {}
} as unknown as typeof ResizeObserver;

const prefix = "Visible reply prefix!!!";
const tail = "\n\nThe end of the complete reply.";
const whole = `${prefix}\n\n`.padEnd(11_525 - tail.length, "a") + tail;
const item: Item = { id: "paged-reply#0", seq: 3, role: "assistant", kind: "text", text: prefix, full_size: whole.length };
let host: HTMLDivElement;
let root: Root;
let owner = 0;

beforeEach(async () => {
  await initI18n("en");
  setToken(`test-paged-translation-${++owner}`);
  fullItem.mockReset().mockResolvedValue({ ...item, text: whole });
  request.mockReset().mockResolvedValue({ text: "The **whole** translation." });
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});
afterEach(() => {
  act(() => root.unmount());
  host.remove();
});

async function settle(operation: () => void | Promise<void>) {
  await act(async () => {
    await operation();
    await vi.dynamicImportSettled();
  });
}

async function draw({ assistant = true, reply = item, sid = "s" } = {}) {
  const actions: ChatActions = { assistant, draftKey: "m/s", canCompose: true, speaker: "Persona", title: "Chat", images: () => [] };
  await settle(async () => {
    root.render(
      <LinkManager.Provider value="chosen-assistant">
        <ChatActionsContext.Provider value={actions}>
          <Answer item={reply} mid="m" sid={sid} bar />
        </ChatActionsContext.Provider>
      </LinkManager.Provider>,
    );
  });
  await settle(async () => { await vi.dynamicImportSettled(); });
}

function translate(): HTMLButtonElement {
  const button = host.querySelector<HTMLButtonElement>("[data-reply-bar] button[aria-pressed]");
  expect(button).toBeTruthy();
  return button!;
}

function expectOriginal(text: string) {
  // Paragraph elements need not retain the source's blank lines in textContent.
  expect(host.querySelector("[data-answer]")?.textContent?.replaceAll("\n", "")).toBe(text.replaceAll("\n", ""));
}

function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (error: unknown) => void;
  const promise = new Promise<T>((done, fail) => { resolve = done; reject = fail; });
  return { promise, resolve, reject };
}

describe("translation of hub-paged answers", () => {
  it.each([true, false])("loads and sends all 11525 characters, never just the 23-character prefix (main=%s)", async (assistant) => {
    await draw({ assistant });
    expect(fullItem).not.toHaveBeenCalled();
    expect(request).not.toHaveBeenCalled();
    expect(host.querySelector("[data-full-text]")).not.toBeNull();
    await settle(async () => translate().click());
    const sent = request.mock.calls[0]?.[1]?.json?.text as string | undefined;
    expect({
      fullItemCalls: fullItem.mock.calls.length,
      sentCharacters: sent?.length,
      sentWholeReply: sent === whole,
    }).toEqual({ fullItemCalls: 1, sentCharacters: 11_525, sentWholeReply: true });
    expect(fullItem).toHaveBeenCalledWith("m", "s", item.id);
    expect(request).toHaveBeenCalledExactlyOnceWith("/translate/message", expect.objectContaining({
      json: { text: whole, target: "en" },
      headers: { "X-Persona-Manager": "chosen-assistant" },
    }));
    expect(host.querySelector("[data-reply-translation] strong")?.textContent).toBe("whole");
    await settle(async () => translate().click());
    expectOriginal(whole);
    expect(host.querySelector("[data-full-text]")).toBeNull();
    await settle(async () => translate().click());
    expect(host.querySelector("[data-reply-translation]")).not.toBeNull();
    expect(fullItem).toHaveBeenCalledTimes(1);
    expect(request).toHaveBeenCalledTimes(1);
  });

  it("keeps full-text loading and the original visible before the translation can be requested", async () => {
    const full = deferred<Item>();
    const translated = deferred<{ text: string }>();
    fullItem.mockReturnValue(full.promise);
    request.mockReturnValue(translated.promise);
    await draw();
    await settle(async () => { translate().click(); translate().click(); });
    expect(fullItem).toHaveBeenCalledTimes(1);
    expect(request).not.toHaveBeenCalled();
    expect(translate().disabled).toBe(true);
    const more = host.querySelector<HTMLButtonElement>("[data-full-text=loading]");
    expect(more?.disabled).toBe(true);
    expect(host.querySelector("[data-answer]")?.textContent).toBe(prefix);
    expect(host.querySelector("[data-reply-translation]")).toBeNull();
    await settle(async () => full.resolve({ ...item, text: whole }));
    expect(request).toHaveBeenCalledTimes(1);
    expectOriginal(whole);
    expect(translate().disabled).toBe(true);
    await settle(async () => translated.resolve({ text: "The complete translation." }));
    expect(translate().disabled).toBe(false);
    expect(host.querySelector("[data-reply-translation]")?.textContent).toBe("The complete translation.");
  });

  it("does not spend on a failed full-text load, keeps its retry control, and retries explicitly", async () => {
    fullItem.mockRejectedValueOnce(new Error("Full reply unavailable"));
    await draw();
    await settle(async () => translate().click());
    expect(request).not.toHaveBeenCalled();
    expect(host.querySelector("[data-answer]")?.textContent).toBe(prefix);
    expect(host.querySelector("[data-full-text=failed]")).not.toBeNull();
    expect(host.querySelector("[role=alert]")?.textContent).toContain("Full reply unavailable");
    expect(host.querySelector("[data-reply-translation]")).toBeNull();
    await settle(async () => translate().click());
    expect(fullItem).toHaveBeenCalledTimes(2);
    expect(request).toHaveBeenCalledTimes(1);
    expect(request.mock.calls[0][1].json.text).toBe(whole);
    expect(host.querySelector("[role=alert]")).toBeNull();
  });

  it("reuses a manually loaded full reply", async () => {
    await draw();
    await settle(async () => host.querySelector<HTMLButtonElement>("[data-full-text]")!.click());
    expectOriginal(whole);
    await settle(async () => translate().click());
    expect(fullItem).toHaveBeenCalledTimes(1);
    expect(request.mock.calls[0][1].json.text).toBe(whole);
  });

  it("shares an in-flight manual full-text request with translation", async () => {
    const full = deferred<Item>();
    fullItem.mockReturnValue(full.promise);
    await draw();
    await settle(async () => host.querySelector<HTMLButtonElement>("[data-full-text]")!.click());
    await settle(async () => translate().click());
    expect(request).not.toHaveBeenCalled();
    expect(fullItem).toHaveBeenCalledTimes(1);
    await settle(async () => full.resolve({ ...item, text: whole }));
    expect(request).toHaveBeenCalledTimes(1);
    expect(request.mock.calls[0][1].json.text).toBe(whole);
  });

  it("reuses the loaded original when translation fails and is explicitly retried", async () => {
    request.mockRejectedValueOnce(new Error("Translation unavailable"));
    await draw();
    await settle(async () => translate().click());
    expectOriginal(whole);
    expect(host.querySelector("[role=alert]")?.textContent).toContain("Translation unavailable");
    await settle(async () => translate().click());
    expect(fullItem).toHaveBeenCalledTimes(1);
    expect(request).toHaveBeenCalledTimes(2);
    expect(request.mock.calls[1][1].json.text).toBe(whole);
    expect(host.querySelector("[data-reply-translation]")).not.toBeNull();
  });

  it("does not request a translation after credentials change during full-text loading", async () => {
    const full = deferred<Item>();
    fullItem.mockReturnValue(full.promise);
    await draw();
    await settle(async () => translate().click());
    setToken("test-paged-translation-other-owner");
    await settle(async () => full.resolve({ ...item, text: whole }));
    expect(request).not.toHaveBeenCalled();
    expect(host.querySelector("[data-reply-translation]")).toBeNull();
  });

  it("fails closed when a paged reply has no session to load its complete text from", async () => {
    await draw({ sid: "" });
    await settle(async () => translate().click());
    expect(fullItem).not.toHaveBeenCalled();
    expect(request).not.toHaveBeenCalled();
    expect(host.querySelector("[role=alert]")).not.toBeNull();
    expect(host.querySelector("[data-answer]")?.textContent).toBe(prefix);
  });

  it("does not translate a stale full-text result after the answer is replaced", async () => {
    const full = deferred<Item>();
    fullItem.mockReturnValue(full.promise);
    await draw();
    await settle(async () => translate().click());
    const replacement = { ...item, id: "replacement#0", text: "Another complete reply", full_size: undefined };
    await draw({ reply: replacement });
    await settle(async () => full.resolve({ ...item, text: whole }));
    expect(request).not.toHaveBeenCalled();
    expect(host.querySelector("[data-answer]")?.textContent).toBe(replacement.text);
    expect(host.querySelector("[data-reply-translation]")).toBeNull();
  });

  it("keeps the new source loading when the replaced source's full-text request fails", async () => {
    const old = deferred<Item>();
    const next = deferred<Item>();
    const complete = `${whole}\n\nA different complete reply.`;
    const replacement: Item = { ...item, id: "next-reply#0", text: "Next reply prefix", full_size: complete.length };
    fullItem.mockReturnValueOnce(old.promise).mockReturnValueOnce(next.promise);
    await draw();
    await settle(async () => translate().click());
    await draw({ reply: replacement });
    await settle(async () => translate().click());
    expect(fullItem).toHaveBeenCalledTimes(2);
    await settle(async () => old.reject(new Error("Previous full text failed")));
    expect(host.querySelector("[data-full-text]")?.getAttribute("data-full-text")).toBe("loading");
    expect(translate().disabled).toBe(true);
    expect(request).not.toHaveBeenCalled();
    await settle(async () => next.resolve({ ...replacement, text: complete }));
    expect(request).toHaveBeenCalledExactlyOnceWith("/translate/message", expect.objectContaining({
      json: { text: complete, target: "en" },
    }));
    await settle(async () => translate().click());
    expectOriginal(complete);
    expect(fullItem).toHaveBeenCalledTimes(2);
  });

  it("does not spend on a full-text result after the answer unmounts", async () => {
    const full = deferred<Item>();
    fullItem.mockReturnValue(full.promise);
    await draw();
    await settle(async () => translate().click());
    await settle(async () => root.render(null));
    await settle(async () => full.resolve({ ...item, text: whole }));
    expect(request).not.toHaveBeenCalled();
  });
});
