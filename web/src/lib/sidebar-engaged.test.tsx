// @vitest-environment jsdom
import { act, createElement, useRef } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { useHeldOrder, useSidebarEngaged } from "./sidebar-engaged";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
let host: HTMLDivElement, root: Root;
beforeEach(() => { host = document.createElement("div"); document.body.append(host); root = createRoot(host); });
afterEach(() => { act(() => root.unmount()); host.remove(); });

function Engaged({ out }: { out: { current: boolean } }) {
  const ref = useRef<HTMLDivElement>(null);
  out.current = useSidebarEngaged(ref);
  return createElement("aside", { "data-sidebar-root": "" }, createElement("div", { ref }, createElement("button", { id: "in" }, "in")), createElement("button", { id: "other" }, "other"));
}

describe("useSidebarEngaged", () => {
  it("is on while a mouse pointer is over the sidebar or focus is in it, and off once both are gone", () => {
    const out = { current: false };
    act(() => root.render(createElement(Engaged, { out })));
    const aside = host.querySelector("aside")!;
    const fire = (type: string, pointerType = "mouse") => act(() => { aside.dispatchEvent(new PointerEvent(type, { pointerType })); });
    expect(out.current).toBe(false);
    fire("pointerenter", "touch");
    expect(out.current).toBe(false); // a touch lifts: it is not a pointer over the sidebar
    fire("pointerenter");
    expect(out.current).toBe(true);
    fire("pointerleave");
    expect(out.current).toBe(false);
    act(() => host.querySelector<HTMLButtonElement>("#in")!.focus());
    expect(out.current).toBe(true);
    act(() => host.querySelector<HTMLButtonElement>("#in")!.blur());
    expect(out.current).toBe(false);
  });
});

describe("useHeldOrder", () => {
  function Held({ items, frozen, out }: { items: string[]; frozen: boolean; out: { current: readonly string[] } }) {
    out.current = useHeldOrder(items, (x) => x, frozen);
    return null;
  }
  it("is the items while free, then the order they had: new ones first, gone ones gone", () => {
    const out = { current: [] as readonly string[] };
    const render = (items: string[], frozen: boolean) => act(() => root.render(createElement(Held, { items, frozen, out })));
    render(["a", "b", "c"], false);
    expect(out.current).toEqual(["a", "b", "c"]);
    render(["c", "a", "b"], true);
    expect(out.current).toEqual(["a", "b", "c"]);
    render(["d", "c", "b"], true);
    expect(out.current).toEqual(["d", "b", "c"]);
    render(["d", "c", "b"], false);
    expect(out.current).toEqual(["d", "c", "b"]);
  });
});
