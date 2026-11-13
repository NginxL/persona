import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { isOverlayEntry, onOverlayEntry, openOverlay, overlayDepth, overlayState, settleOverlayEntry, takeOverlayEntry } from "./back-close";
import { navigate } from "./router";

describe("overlay history entries", () => {
  it("marks an entry as one overlay's, keeping the page's state", () => {
    const s = overlayState({ scroll: 3 }, "o1");
    expect(s).toEqual({ scroll: 3, personaOverlay: "o1" });
    expect(isOverlayEntry(s, "o1")).toBe(true);
    expect(isOverlayEntry(s, "o2")).toBe(false);
  });
  it("is not fooled by the router's null state or another overlay's entry", () => {
    expect(isOverlayEntry(null, "o1")).toBe(false);
    expect(isOverlayEntry(overlayState(null, "o2"), "o1")).toBe(false);
    expect(overlayState("x", "o3")).toEqual({ personaOverlay: "o3" });
  });
});

// A browser's history in miniature: back() pops asynchronously and fires popstate, like the real one.
function fakeBrowser() {
  const stack: unknown[] = [null];
  let at = 0;
  const listeners = new Set<() => void>();
  const history = {
    get state() {
      return stack[at];
    },
    get length() {
      return at + 1;
    },
    pushState(s: unknown) {
      stack.splice(at + 1);
      stack.push(s);
      at++;
    },
    replaceState(s: unknown) {
      stack[at] = s;
    },
    back() {
      setTimeout(() => {
        if (at === 0) return;
        at--;
        listeners.forEach((l) => l());
      }, 5);
    },
  };
  vi.stubGlobal("history", history);
  vi.stubGlobal("location", { href: "/s/m/a", pathname: "/s/m/a" });
  vi.stubGlobal("addEventListener", (_: string, l: () => void) => listeners.add(l));
  vi.stubGlobal("removeEventListener", (_: string, l: () => void) => listeners.delete(l));
  return { history, pressBack: () => history.back() };
}

describe("openOverlay", () => {
  beforeEach(() => vi.useFakeTimers());
  afterEach(() => {
    vi.useRealTimers();
    vi.unstubAllGlobals();
  });

  it("Back closes it and leaves the page where it was", async () => {
    const b = fakeBrowser();
    const onClose = vi.fn();
    openOverlay(onClose);
    expect(b.history.length).toBe(2);
    b.pressBack();
    await vi.runAllTimersAsync();
    expect(onClose).toHaveBeenCalledOnce();
    expect(b.history.length).toBe(1);
  });

  it("closing it from the page takes its entry back off", async () => {
    const b = fakeBrowser();
    const onClose = vi.fn();
    openOverlay(onClose)();
    await vi.runAllTimersAsync();
    expect(b.history.length).toBe(1);
    expect(onClose).not.toHaveBeenCalled();
  });

  it("opened again right after closing (StrictMode's double effect) stays open on one entry", async () => {
    const b = fakeBrowser();
    const first = vi.fn();
    const second = vi.fn();
    openOverlay(first)(); // mount, then StrictMode's cleanup…
    openOverlay(second); // …and the effect again
    await vi.runAllTimersAsync();
    expect(second).not.toHaveBeenCalled();
    expect(first).not.toHaveBeenCalled();
    expect(b.history.length).toBe(2);
    b.pressBack();
    await vi.runAllTimersAsync();
    expect(second).toHaveBeenCalledOnce();
    expect(b.history.length).toBe(1);
  });

  it("counts stacked entries, while reopening the same entry keeps its depth", async () => {
    const b = fakeBrowser();
    const closeFirst = openOverlay(vi.fn());
    expect(overlayDepth()).toBe(1);
    closeFirst();
    openOverlay(vi.fn()); // StrictMode's cleanup, then the same overlay's effect again: reuse, not another entry
    await vi.runAllTimersAsync();
    expect(overlayDepth()).toBe(1);
    expect(b.history.length).toBe(2);

    openOverlay(vi.fn());
    expect(overlayDepth()).toBe(2);
    expect(b.history.length).toBe(3);
    b.pressBack();
    await vi.runAllTimersAsync();
    expect(overlayDepth()).toBe(1);
    expect(onOverlayEntry()).toBe(true);
    b.pressBack();
    await vi.runAllTimersAsync();
    expect(overlayDepth()).toBe(0);
    expect(onOverlayEntry()).toBe(false);
  });

  it("an overlay entry written before the depth field still counts as one", () => {
    fakeBrowser();
    expect(overlayDepth(null)).toBe(0);
    expect(overlayDepth(overlayState(null, "old"))).toBe(1);
  });

  it("a navigation from inside the overlay takes its entry: one Back leaves the new page", async () => {
    const b = fakeBrowser();
    const onClose = vi.fn();
    const close = openOverlay(onClose); // the drawer opens over /s/m/a
    expect(b.history.length).toBe(2);
    // a drawer link: the router replaces the overlay's entry with the new page, then the drawer closes
    expect(takeOverlayEntry()).toBe(true);
    b.history.replaceState(null);
    close();
    await vi.runAllTimersAsync();
    expect(b.history.length).toBe(2); // the page under the drawer, then the new page: no dead copy between them
    expect(onClose).not.toHaveBeenCalled();
  });

  it("nothing to take when no overlay is open", () => {
    fakeBrowser();
    expect(takeOverlayEntry()).toBe(false);
  });

  it("the router: a drawer link replaces the drawer's entry, so one Back leaves the new page", async () => {
    const b = fakeBrowser();
    const close = openOverlay(vi.fn());
    navigate("/overview"); // the link, then the drawer closes
    close();
    await vi.runAllTimersAsync();
    expect(b.history.length).toBe(2);
    expect(isOverlayEntry(b.history.state, "o1")).toBe(false);
  });

  it("the router: a replace keeps an open overlay's marker (an old /s/ link turned into its /t/)", () => {
    const b = fakeBrowser();
    openOverlay(vi.fn());
    const marked = b.history.state;
    navigate("/t/m/w1/w1:t1/a", true);
    expect(b.history.state).toEqual(marked);
  });

  it("a sheet that sends the page elsewhere with a replace (deleting this chat goes home) closes without going back", async () => {
    const b = fakeBrowser();
    const close = openOverlay(vi.fn());
    const loc = location as unknown as { href: string; pathname: string };
    navigate("/overview", true);
    loc.href = loc.pathname = "/overview"; // the stub's location follows the replace
    close();
    await vi.runAllTimersAsync();
    expect(b.history.length).toBe(2); // stayed on the new page: no Back to the deleted chat
    expect(isOverlayEntry(b.history.state, "o1")).toBe(false);
  });
});

// A reload keeps the history and its states but none of the page's overlays (#5134): the first load settles them.
describe("settleOverlayEntry", () => {
  beforeEach(() => vi.useFakeTimers());
  afterEach(() => {
    vi.useRealTimers();
    vi.unstubAllGlobals();
  });
  const settle = async () => {
    const done = settleOverlayEntry();
    await vi.runAllTimersAsync();
    await done;
  };

  it("a reload with the side panel open goes back off its entry: the first Back then leaves the page", async () => {
    const b = fakeBrowser();
    openOverlay(vi.fn()); // the panel, then the reload
    expect(b.history.length).toBe(2);
    await settle();
    expect(b.history.length).toBe(1);
    expect(b.history.state).toBeNull();
  });

  it("goes back off overlays stacked on each other (the drawer and a sheet over it)", async () => {
    const b = fakeBrowser();
    openOverlay(vi.fn());
    openOverlay(vi.fn());
    expect(b.history.length).toBe(3);
    await settle();
    expect(b.history.length).toBe(1);
  });

  it("keeps the page's own state on the entry it lands on", async () => {
    const b = fakeBrowser();
    b.history.replaceState({ scroll: 3 });
    openOverlay(vi.fn());
    await settle();
    expect(b.history.state).toEqual({ scroll: 3 });
  });

  it("the page moved under it (a replace while it was open): takes the marker off and stays", async () => {
    const b = fakeBrowser();
    openOverlay(vi.fn());
    const loc = location as unknown as { href: string; pathname: string };
    navigate("/overview", true);
    loc.href = loc.pathname = "/overview";
    await settle();
    expect(b.history.length).toBe(2);
    expect(isOverlayEntry(b.history.state, "o1")).toBe(false);
    expect(Object.keys((b.history.state as object | null) ?? {}).some((k) => k.startsWith("personaOverlay"))).toBe(false);
  });

  it("an overlay's entry with nothing under it stays, without the marker", async () => {
    const b = fakeBrowser();
    b.history.replaceState({ personaOverlay: "o9", personaOverlayAt: "/s/m/a" });
    await settle();
    expect(b.history.length).toBe(1);
    expect(b.history.state).toBeNull();
  });

  it("leaves a page with no overlay's entry alone", async () => {
    const b = fakeBrowser();
    const back = vi.spyOn(b.history, "back");
    b.history.pushState({ scroll: 1 });
    await settle();
    expect(back).not.toHaveBeenCalled();
    expect(b.history.length).toBe(2);
  });
});
