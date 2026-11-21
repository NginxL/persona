// @vitest-environment jsdom
import { act } from "react";
import { createRoot } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { ConversationOrderRow } from "./ConversationOrderRow";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

describe("ConversationOrderRow (#12017)", () => {
  let host: HTMLDivElement;
  let root: ReturnType<typeof createRoot>;
  beforeEach(() => {
    host = document.createElement("div");
    document.body.append(host);
    root = createRoot(host);
  });
  afterEach(() => act(() => root.unmount()));

  it("drags by the grip and offers the same move through accessible buttons", () => {
    const onMove = vi.fn(), onStep = vi.fn();
    const row = (id: string, index: number) => (
      <ConversationOrderRow key={id} id={id} title={id} index={index} count={3} onMove={onMove} onStep={onStep}>
        <a href={`#${id}`}>{id}</a>
      </ConversationOrderRow>
    );
    act(() => root.render(<>{row("b", 1)}{row("c", 2)}</>));
    const source = host.querySelector<HTMLElement>('[data-order-row="b"] [draggable="true"]')!;
    const target = host.querySelector<HTMLElement>('[data-order-row="c"]')!;
    const data: Record<string, string> = {};
    const transfer = {
      effectAllowed: "", dropEffect: "", types: ["application/x-persona-conversation-order"],
      setData: (kind: string, value: string) => { data[kind] = value; },
      getData: (kind: string) => data[kind] ?? "",
    };
    const drag = new Event("dragstart", { bubbles: true });
    Object.defineProperty(drag, "dataTransfer", { value: transfer });
    act(() => { source.dispatchEvent(drag); });
    const drop = new Event("drop", { bubbles: true, cancelable: true });
    Object.defineProperty(drop, "dataTransfer", { value: transfer });
    act(() => { target.dispatchEvent(drop); });
    expect(onMove).toHaveBeenCalledWith("b", "c");

    const buttons = host.querySelectorAll<HTMLButtonElement>('[data-order-row="b"] button');
    expect(buttons).toHaveLength(2);
    expect(buttons[0].getAttribute("aria-label")).toContain("上移");
    expect(buttons[1].getAttribute("aria-label")).toContain("下移");
    act(() => { buttons[0].click(); });
    act(() => { buttons[1].click(); });
    expect(onStep.mock.calls).toEqual([["b", -1], ["b", 1]]);
    expect(host.querySelector<HTMLButtonElement>('[data-order-row="c"] button:last-child')?.disabled).toBe(true);
    // While arranging, the original link cannot navigate accidentally.
    expect(host.querySelector<HTMLElement>('[data-order-row="b"] > div[inert] a')).not.toBeNull();
  });
});
