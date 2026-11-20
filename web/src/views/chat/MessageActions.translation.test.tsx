// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const request = vi.hoisted(() => vi.fn());
const media = vi.hoisted(() => ({ touch: false }));
vi.mock("../../lib/api", async (original) => ({ ...await original<typeof import("../../lib/api")>(), api: request }));
vi.mock("../../ui/media-query", async (original) => ({
  ...await original<typeof import("../../ui/media-query")>(),
  useMedia: () => media.touch,
  isNoHover: () => media.touch,
}));

import { initI18n, t } from "../../i18n";
import { setToken } from "../../lib/api";
import { LinkManager } from "../../lib/manager-choice";
import { ChatActionsContext, type ChatActions } from "./chat-actions";
import { MessageActions } from "./MessageActions";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let host: HTMLDivElement;
let root: Root;
let owner = 0;
beforeEach(async () => {
  await initI18n("zh-Hans");
  setToken(`test-message-actions-${++owner}`);
  request.mockReset().mockResolvedValue({ text: "Translated **reply**" });
  media.touch = false;
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

async function draw({ assistant = true, text = "Original reply", isUser = false, bar = true } = {}) {
  const actions: ChatActions = { assistant, draftKey: "m/s", canCompose: true, speaker: "Persona", title: "Chat", images: () => [] };
  await settle(async () => {
    root.render(
      <LinkManager.Provider value="chosen-assistant">
        <ChatActionsContext.Provider value={actions}>
          <MessageActions text={text} bar={bar} isUser={isUser} itemId="reply#0"><p data-original>{text}</p></MessageActions>
        </ChatActionsContext.Provider>
      </LinkManager.Provider>,
    );
  });
  await settle(async () => { await vi.dynamicImportSettled(); });
}

function translationButton(): HTMLButtonElement {
  const area = host.querySelector(media.touch ? "[data-reply-touch-bar]" : "[data-reply-bar]");
  const button = area?.querySelector<HTMLButtonElement>("button[aria-pressed]");
  expect(button).toBeTruthy();
  return button!;
}

describe("one-click reply translation", () => {
  it.each([true, false])("appears in the action row (main chat=%s), targets UI language and toggles without spending twice", async (assistant) => {
    await draw({ assistant });
    expect(request).not.toHaveBeenCalled();
    expect(translationButton().getAttribute("aria-label")).toBe(t("chat-messageactions.translate"));
    await settle(async () => translationButton().click());
    expect(request).toHaveBeenCalledExactlyOnceWith("/translate/message", expect.objectContaining({
      json: { text: "Original reply", target: "zh-Hans" },
      headers: { "X-Persona-Manager": "chosen-assistant" },
    }));
    expect(host.querySelector("[data-reply-translation] strong")?.textContent).toBe("reply");
    expect(host.querySelector("[data-original]")).toBeNull();
    expect(translationButton().getAttribute("aria-label")).toBe(t("chat-messageactions.show_original"));
    await settle(async () => translationButton().click());
    expect(host.querySelector("[data-original]")?.textContent).toBe("Original reply");
    await settle(async () => translationButton().click());
    expect(host.querySelector("[data-reply-translation]")).not.toBeNull();
    expect(request).toHaveBeenCalledTimes(1);
  });

  it("has the same translation action in the phone's touch row", async () => {
    media.touch = true;
    await draw();
    expect(translationButton().className).toContain("touch-hit");
    await settle(async () => translationButton().click());
    expect(host.querySelector("[data-reply-translation]")).not.toBeNull();
    expect(request).toHaveBeenCalledTimes(1);
  });

  it("disables the action during its request and ignores a result for replaced source text", async () => {
    let resolve!: (value: { text: string }) => void;
    request.mockReturnValue(new Promise((done) => { resolve = done; }));
    await draw();
    await settle(async () => { translationButton().click(); translationButton().click(); });
    expect(translationButton().disabled).toBe(true);
    expect(request).toHaveBeenCalledTimes(1);
    await draw({ text: "A different reply" });
    await settle(async () => resolve({ text: "Stale translation" }));
    expect(host.querySelector("[data-original]")?.textContent).toBe("A different reply");
    expect(host.querySelector("[data-reply-translation]")).toBeNull();
  });

  it("keeps the original on failure and retries only when explicitly clicked", async () => {
    request.mockRejectedValueOnce(new Error("Translation service unavailable"));
    await draw();
    await settle(async () => translationButton().click());
    expect(host.querySelector("[role=alert]")?.textContent).toContain("Translation service unavailable");
    expect(host.querySelector("[data-original]")).not.toBeNull();
    await settle(async () => translationButton().click());
    expect(host.querySelector("[role=alert]")).toBeNull();
    expect(host.querySelector("[data-reply-translation]")).not.toBeNull();
    expect(request).toHaveBeenCalledTimes(2);
  });

  it("never offers a paid action on user messages or streaming/narration rows", async () => {
    await draw({ isUser: true, bar: false });
    expect(host.querySelector("button[aria-pressed]")).toBeNull();
    await draw({ isUser: false, bar: false });
    expect(host.querySelector("button[aria-pressed]")).toBeNull();
    expect(request).not.toHaveBeenCalled();
  });

  it.each(["en", "ja"])("uses the actual %s interface language, not translation preferences", async (language) => {
    await initI18n(language);
    await draw();
    await settle(async () => translationButton().click());
    expect(request.mock.calls[0][1].json.target).toBe(language);
  });
});
