// @vitest-environment jsdom
// Back on a narrow screen (#1996, #11442): the palette closes first, as the drawer and the sheets do. Opening it pushes an
// entry, a pick that goes to a page takes that entry over (Back from the page lands where the palette opened), and a
// sheet a command opens reuses it. A wide screen's palette writes no history.
import { act, useState } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { onOverlayEntry, useCloseOnBack } from "../lib/back-close";
import { closePalette, getPalette, openPalette, registerContextCommands, usePalette } from "../lib/palette";
import { NARROW } from "../lib/viewport";
import { Palette } from "./Palette";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
// the screen's width, read through matchMedia (lib/viewport.ts keeps one list per query and reads `matches` each time)
const screen = vi.hoisted(() => ({ narrow: true, query: "(max-width: 1023px)" }));
vi.hoisted(() => {
  (globalThis as Record<string, unknown>).matchMedia = (query: string) => ({
    get matches() {
      return query === screen.query && screen.narrow;
    },
    addEventListener() {},
    removeEventListener() {},
  });
});

let host: HTMLDivElement;
let root: Root;
let showSheet: (open: boolean) => void = () => {};

/** A sheet as Dialog is on a phone: Back closes it. */
function Sheet() {
  const [open, setOpen] = useState(false);
  showSheet = setOpen;
  useCloseOnBack(open, () => setOpen(false));
  return open ? <div data-testid="sheet" /> : null;
}

function Shell() {
  const { open } = usePalette();
  return (
    <>
      <Palette open={open} onClose={closePalette} onNewAgent={() => {}} />
      <Sheet />
    </>
  );
}

const option = (name: string) => [...document.querySelectorAll<HTMLElement>("[role=option]")].find((o) => o.textContent?.includes(name));
const sheetShown = () => document.querySelector("[data-testid=sheet]") !== null;
/** Lets history.back() land (jsdom pops on a later task, then fires popstate) and what it sets off render. */
const settle = () => act(() => new Promise((ok) => setTimeout(ok, 50)));

beforeEach(async () => {
  screen.narrow = true;
  vi.spyOn(Element.prototype, "getClientRects").mockReturnValue([{}] as unknown as DOMRectList);
  Element.prototype.scrollIntoView ??= () => {};
  // a fresh entry on top, so no entry is ahead of the current one and history.length counts what is under it
  history.pushState(null, "", "/agents");
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  act(() => root.render(<Shell />));
  await settle();
});

afterEach(async () => {
  act(() => closePalette());
  await settle();
  act(() => root.unmount());
  host.remove();
  vi.restoreAllMocks();
});

describe("the palette and Back on a narrow screen", () => {
  it("pushes an overlay entry when it opens", () => {
    expect(screen.query).toBe(NARROW);
    const before = history.length;
    act(() => openPalette("jump"));
    expect(document.querySelector("[role=dialog]")).not.toBeNull();
    expect(history.length).toBe(before + 1);
    expect(onOverlayEntry()).toBe(true);
    expect(location.pathname).toBe("/agents");
  });

  it("closes when Back pops its entry, and stays on the page", async () => {
    act(() => openPalette("jump"));
    act(() => history.back());
    await settle();
    expect(getPalette().open).toBe(false);
    expect(onOverlayEntry()).toBe(false);
    expect(location.pathname).toBe("/agents");
  });

  it("closed from the page, takes its entry back off", async () => {
    act(() => openPalette("jump"));
    act(() => closePalette());
    await settle();
    expect(onOverlayEntry()).toBe(false);
    expect(location.pathname).toBe("/agents");
  });

  it("a pick that goes to a page replaces its entry instead of stacking a copy of this page under the new one", async () => {
    act(() => openPalette("command"));
    const opened = history.length;
    act(() => option("新对话")!.click());
    await settle();
    expect(getPalette().open).toBe(false);
    expect(location.pathname).toBe("/persona/new");
    expect(history.length).toBe(opened);
    expect(onOverlayEntry()).toBe(false);
    // one Back lands where the palette opened
    act(() => history.back());
    await settle();
    expect(location.pathname).toBe("/agents");
    expect(onOverlayEntry()).toBe(false);
  });

  it("a command that opens a sheet hands its entry over: one entry, and Back closes the sheet", async () => {
    const off = registerContextCommands(() => [{ id: "test:sheet", section: "测试", title: "打开面板", run: () => showSheet(true) }]);
    try {
      act(() => openPalette("command"));
      const opened = history.length;
      act(() => option("打开面板")!.click());
      await settle();
      expect(getPalette().open).toBe(false);
      expect(sheetShown()).toBe(true);
      expect(history.length).toBe(opened);
      expect(onOverlayEntry()).toBe(true);
      act(() => history.back());
      await settle();
      expect(sheetShown()).toBe(false);
      expect(onOverlayEntry()).toBe(false);
      expect(location.pathname).toBe("/agents");
    } finally {
      off();
    }
  });
});

describe("the palette on a wide screen", () => {
  it("writes no history", async () => {
    screen.narrow = false;
    act(() => root.render(<Shell />));
    const before = history.length;
    act(() => openPalette("jump"));
    expect(document.querySelector("[role=dialog]")).not.toBeNull();
    expect(history.length).toBe(before);
    expect(onOverlayEntry()).toBe(false);
    act(() => closePalette());
    await settle();
    expect(location.pathname).toBe("/agents");
    expect(history.length).toBe(before);
  });
});
