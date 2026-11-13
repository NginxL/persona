// @vitest-environment jsdom
// Back from an Inbox item (#3329): ‹ and Done go back through history to the list the item was opened from, however many
// items were opened on the way; an item with no list under it puts the list in its place instead of adding an entry.
import { afterEach, describe, expect, it } from "vitest";
import { onOverlayEntry, openOverlay, overlayDepth } from "./back-close";
import { backToInboxList, inboxReturnTo, openFromInbox, stepsToInbox } from "./inbox-nav";
import { navigate } from "./router";

/** Runs `go`, then waits for the Back it causes (history.go() is asynchronous). */
const popped = (go: () => void) =>
  new Promise<void>((done, fail) => {
    const timer = setTimeout(() => fail(new Error("no Back happened")), 1_000);
    addEventListener("popstate", () => (clearTimeout(timer), done()), { once: true });
    go();
  });

const atList = () => {
  history.pushState(null, "", "/inbox");
  return history.length;
};

afterEach(() => history.replaceState(null, "", "/"));

describe("Back from an Inbox item", () => {
  it("goes back to the list it was opened from, through the items opened on the way", async () => {
    const start = atList();
    openFromInbox({ name: "inbox", id: "a" });
    expect(stepsToInbox(history.state)).toBe(1);
    openFromInbox({ name: "inbox", id: "b", manager: "m2" }); // a related item, or another row on a wide screen
    expect(stepsToInbox(history.state)).toBe(2);
    await popped(backToInboxList);
    expect(location.pathname).toBe("/inbox");
    expect(stepsToInbox(history.state)).toBe(0);
    expect(history.length - start).toBe(2); // the two items, still ahead; no copy of the list on top
  });

  it("puts the list in place of an item with no list under it (a link, a notification, ⌘K)", () => {
    history.pushState(null, "", "/inbox/x");
    const start = history.length;
    navigate({ name: "inbox", id: "y" }); // ⌘K from the item: no list under it either
    expect(stepsToInbox(history.state)).toBe(0);
    backToInboxList();
    expect(location.pathname).toBe("/inbox");
    expect(history.length - start).toBe(1);
  });

  it("goes back past a menu open over the item, and the menu closes", async () => {
    const start = atList();
    openFromInbox({ name: "inbox", id: "a" });
    let closed = false;
    openOverlay(() => (closed = true));
    expect(stepsToInbox(history.state)).toBe(1); // the menu's entry carries the item's way back
    expect(overlayDepth()).toBe(1);
    await popped(backToInboxList);
    expect(location.pathname).toBe("/inbox");
    expect(closed).toBe(true);
    expect(history.length - start).toBe(2); // the item and its menu, still ahead; no new entry
  });

  it("goes back past the original and a menu stacked over it in one step", async () => {
    const start = atList();
    openFromInbox({ name: "inbox", id: "a" });
    let closed = 0;
    openOverlay(() => closed++); // the item's original
    openOverlay(() => closed++); // a menu over that original
    expect(overlayDepth()).toBe(2);
    expect(stepsToInbox(history.state)).toBe(1);
    await popped(backToInboxList);
    expect(location.pathname).toBe("/inbox");
    expect(closed).toBe(2);
    expect(overlayDepth()).toBe(0);
    expect(onOverlayEntry()).toBe(false);
    expect(stepsToInbox(history.state)).toBe(0);
    expect(history.length - start).toBe(3); // the item and two overlays, no entry added or removed
  });

  it("an item opened over a menu takes the menu's entry, one step on from the item under it", () => {
    atList();
    openFromInbox({ name: "inbox", id: "a" });
    openOverlay(() => {});
    const length = history.length;
    openFromInbox({ name: "inbox", id: "b" });
    expect(history.length).toBe(length);
    expect(stepsToInbox(history.state)).toBe(2);
  });

  it("carries the item's address to a full page opened from it", () => {
    atList();
    openFromInbox({ name: "inbox", id: "a", manager: "m2" });
    openFromInbox({ name: "session", mid: "m", sid: "s" });
    expect(inboxReturnTo(history.state)).toEqual({ id: "a", manager: "m2" });
    expect(stepsToInbox(history.state)).toBe(0);
  });

  it("carries no item address from the list, and ignores malformed state", () => {
    atList();
    openFromInbox({ name: "session", mid: "m", sid: "s" });
    expect(inboxReturnTo(history.state)).toBeUndefined();
    expect(inboxReturnTo({ personaInboxFrom: "/persona/c1" })).toBeUndefined();
    expect(inboxReturnTo({ personaInboxFrom: 42 })).toBeUndefined();
  });
});
