// @vitest-environment jsdom
// A streamed delta re-renders its SegmentView (the `streaming` prop changes), but not the steps of its turn: their
// props stay the same objects, so StepView and FreshAnswer skip. Words that just came in still learn it
// (fresh-words.ts): only the latest visible words (#2602), and no others.
import { act, type ComponentProps } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

// every render of the agent's persisted words (Answer draws them through Markdown with `fade` given, set or not): counted
const drawn = vi.hoisted(() => [] as { text: string; fade?: boolean }[]);
vi.mock("../../ui/Markdown", async (load) => {
  const real = await load<typeof import("../../ui/Markdown")>();
  const { createElement } = await import("react");
  return { ...real, Markdown: (p: ComponentProps<typeof real.Markdown>) => (!p.streaming && "fade" in p && drawn.push({ text: p.text, fade: p.fade }), createElement(real.Markdown, p)) };
});

import { emptyTaskIndex } from "../../lib/tasks";
import type { Item } from "../../lib/types";
import type { Segment, Step } from "../../lib/turns";
import { ChatContext, type ChatContextValue } from "./chat-context";
import { addFreshWords, resetFreshWords } from "./fresh-words";
import { SegmentView } from "./SegmentView";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const T0 = Date.parse("2026-11-04T10:00:00Z");
const at = (s: number) => new Date(T0 + s * 1000).toISOString();
const text = (n: number): Step => {
  const item = { id: `a${n}#0`, seq: n, role: "assistant", kind: "text", text: `words ${n}`, ts: at(n) } as Item;
  return { kind: "text", id: item.id, item };
};
const tool = (n: number): Step => ({
  kind: "tool",
  id: `t${n}#0`,
  use: { id: `t${n}#0`, seq: n, role: "assistant", kind: "tool_use", tool_name: "Bash", tool_use_id: `c-${n}`, tool_input: { command: `echo ${n}` }, ts: at(n) },
  result: { id: `t${n}-r#0`, seq: n, role: "user", kind: "tool_result", tool_use_id: `c-${n}`, text: "ok", ts: at(n) },
  images: [],
});

// a live turn: words between tool calls, every run of words a top-level StepView -> FreshAnswer
const steps = [1, 3, 5, 7, 9].flatMap((n) => [text(n), tool(n + 1)]);
const seg: Segment = { id: "seg", steps, tools: 5, thoughts: 0, replies: 5, folds: [], start: at(0) };
const ctx: ChatContextValue = { tasks: emptyTaskIndex(), subagents: [], scope: "" };
const expandFold = async () => {};

let host: HTMLDivElement;
let root: Root;

beforeEach(() => {
  drawn.length = 0;
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
  resetFreshWords("m", "s");
});

function draw(streaming?: string) {
  act(() =>
    root.render(
      <ChatContext.Provider value={ctx}>
        <SegmentView seg={seg} live mid="m" sid="s" streaming={streaming} expandFold={expandFold} />
      </ChatContext.Provider>,
    ),
  );
}

describe("StepView / FreshAnswer memo", () => {
  it("a streamed delta does not re-render the steps of its turn", () => {
    draw("w");
    expect(drawn).toHaveLength(0); // old persisted narration is hidden while the single live text streams
    expect(host.querySelector("[data-work-detail]")).toBeNull();
    expect(host.textContent).not.toContain("words 9");
    for (let i = 0; i < 10; i++) draw("w" + "x".repeat(i + 1));
    expect(drawn).toHaveLength(0);
    expect(host.querySelector('[data-answer="streaming"]')).not.toBeNull();
  });

  it("words that just came in learn it, and no other words render", () => {
    draw();
    drawn.length = 0;
    act(() => addFreshWords("m", "s", ["a9#0"]));
    expect(drawn).toEqual([{ text: "words 9", fade: true }]);
    // words hidden in the summary, and the same words in another chat, are not these
    act(() => addFreshWords("m", "s", ["a7#0"]));
    act(() => addFreshWords("m", "other", ["a9#0"]));
    expect(drawn).toHaveLength(1);
  });
});
