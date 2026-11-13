// @vitest-environment jsdom
// The way back from a full-page conversation to its Inbox item.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { t } from "../../i18n";
import { openFromInbox } from "../../lib/inbox-nav";

const origin = vi.hoisted(() => ({ current: undefined as { id: string; manager?: string; title?: string } | undefined }));
vi.mock("../../lib/inbox-origin", () => ({ useInboxOrigin: () => origin.current }));

import { InboxBack, inboxBackLabel } from "./InboxBack";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let host: HTMLDivElement;
let root: Root;
beforeEach(() => {
  origin.current = { id: "i1", title: "Release review" };
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  history.replaceState(null, "", "/s/m1/s1");
});
afterEach(() => {
  act(() => root.unmount());
  host.remove();
  history.replaceState(null, "", "/");
  vi.restoreAllMocks();
});

function render() {
  act(() => root.render(<InboxBack mid="m1" sid="s1" session={undefined} />));
}

describe("the Inbox way-back strip", () => {
  it("names the item when its title is known, and uses generic words otherwise", () => {
    render();
    expect(host.querySelector("button")?.textContent).toBe(t("chat-chatview.inbox_back_title", { title: "Release review" }));
    expect(inboxBackLabel({ title: "  " })).toBe(t("chat-chatview.inbox_back_generic"));

    origin.current = { id: "i1" };
    act(() => root.render(<InboxBack key="untitled" mid="m1" sid="s1" session={undefined} />));
    expect(host.querySelector("button")?.textContent).toBe(t("chat-chatview.inbox_back_generic"));
  });

  it("goes Back when this history entry came from the same item", () => {
    history.replaceState(null, "", "/inbox/i1?manager=m2");
    openFromInbox({ name: "session", mid: "m1", sid: "s1" });
    origin.current = { id: "i1", manager: "m2", title: "Release review" };
    const back = vi.spyOn(history, "back").mockImplementation(() => {});
    render();
    act(() => host.querySelector("button")!.click());
    expect(back).toHaveBeenCalledOnce();
    expect(location.pathname).toBe("/s/m1/s1");
  });

  it("pushes the item when there is no matching way back in history", () => {
    render();
    act(() => host.querySelector("button")!.click());
    expect(location.pathname).toBe("/inbox/i1");
    expect(location.search).toBe("");
  });

  it("pushes the resolved item rather than going Back to a different item or manager", () => {
    history.replaceState(null, "", "/inbox/another");
    openFromInbox({ name: "session", mid: "m1", sid: "s1" });
    const back = vi.spyOn(history, "back").mockImplementation(() => {});
    origin.current = { id: "i1", manager: "m2" };
    render();
    act(() => host.querySelector("button")!.click());
    expect(back).not.toHaveBeenCalled();
    expect(location.pathname).toBe("/inbox/i1");
    expect(location.search).toBe("?manager=m2");
  });

  it("draws nothing without an item, and ✕ puts it away for this page", () => {
    origin.current = undefined;
    render();
    expect(host.querySelector("[data-inbox-back]")).toBeNull();

    origin.current = { id: "i1" };
    act(() => root.render(<InboxBack key="found" mid="m1" sid="s1" session={undefined} />));
    const close = host.querySelector<HTMLButtonElement>(`button[aria-label="${t("chat-chatview.handoff_dismiss")}"]`)!;
    act(() => close.click());
    expect(host.querySelector("[data-inbox-back]")).toBeNull();
  });
});
