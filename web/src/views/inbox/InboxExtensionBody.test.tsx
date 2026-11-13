// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { InboxItem } from "../../lib/inbox";
import type { UIPlace } from "../../lib/extension-ui";
import { onOverlayEntry } from "../../lib/back-close";
import { InboxExtensionBody } from "./InboxExtensionBody";

vi.mock("../settings/ExtensionView", () => ({
  ExtensionUI: ({ onOpenSource }: { onOpenSource: () => void }) => <button type="button" onClick={onOpenSource}>Open in conversation</button>,
}));

const item: InboxItem = {
  id: "examplechat:1", source: "examplechat", kind: "dm", account: "examplechat", account_name: "Example Chat",
  title: "Weekly plan", preview: "Thursday works", count: 1, tier: "action", version: "v1",
};
const place: UIPlace = {
  extension: "example-chat", extension_name: "Example Chat", id: "event", title: "Event", placement: "inbox_item", size: "full", connector: "examplechat",
};

let host: HTMLDivElement;
let root: Root;
const flush = async () => {
  for (let i = 0; i < 4; i++) await act(async () => { await Promise.resolve(); });
};
const popped = (go: () => void) =>
  new Promise<void>((done, fail) => {
    const timer = setTimeout(() => fail(new Error("no Back happened")), 1_000);
    addEventListener("popstate", () => (clearTimeout(timer), done()), { once: true });
    go();
  });
const openSource = () => {
  const button = [...host.querySelectorAll("button")].find((b) => b.textContent === "Open in conversation")!;
  act(() => button.click());
};

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

beforeEach(() => {
  history.pushState(null, "", "/inbox/examplechat:1");
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  act(() => root.render(<InboxExtensionBody item={item} place={place} own={() => <p>own body</p>} />));
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
  history.replaceState(null, "", "/");
});

describe("Back from an extension item's source", () => {
  it("pushes a same-address entry and browser Back returns to the extension page", async () => {
    const entries = history.length;
    openSource();
    expect(location.pathname).toBe("/inbox/examplechat:1");
    expect(history.length).toBe(entries + 1);
    expect(onOverlayEntry()).toBe(true);
    expect(host.querySelector("[data-inbox-source]")).not.toBeNull();
    expect(host.textContent).toContain("own body");

    await act(() => popped(() => history.back()));
    await flush();
    expect(location.pathname).toBe("/inbox/examplechat:1");
    expect(onOverlayEntry()).toBe(false);
    expect(host.querySelector("[data-inbox-source]")).toBeNull();
    expect(host.textContent).not.toContain("own body");
    expect(host.querySelector("[data-extension-inbox]")?.className).not.toContain("hidden");
  });

  it("the page's back button closes the source and takes its entry back off", async () => {
    openSource();
    expect(onOverlayEntry()).toBe(true);

    await act(() => popped(() => host.querySelector<HTMLButtonElement>("[data-inbox-source] button")!.click()));
    await flush();
    expect(onOverlayEntry()).toBe(false);
    expect(host.querySelector("[data-inbox-source]")).toBeNull();
    expect(host.textContent).not.toContain("own body");
    expect(host.querySelector("[data-extension-inbox]")?.className).not.toContain("hidden");
  });
});
