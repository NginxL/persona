import { describe, expect, it } from "vitest";
import type { Element, Root } from "hast";
import { assignKeys, BACKLOG_STEP_MS, chunkState, createWordFadeState, FADE_MS, forgetShown, MAX_WAIT_MS, PACE_MAX_WAIT_MS, rehypeStreamWordFade, rememberShown, remainingMs, scheduleSegment, shownBefore, splitWords, STEP_MS } from "./wordFade";

const text = (value: string) => ({ type: "text" as const, value });
const el = (tagName: string, children: Element["children"], properties: Element["properties"] = {}): Element => ({ type: "element", tagName, properties, children });
const root = (...children: Root["children"]): Root => ({ type: "root", children });

function fakeEl() {
  const props: Record<string, string> = {};
  return { el: { style: { setProperty: (k: string, v: string) => (props[k] = v) } } as unknown as HTMLElement, props };
}

function spans(n: Root | Element): Element[] {
  return n.children.flatMap((c) => (c.type !== "element" ? [] : c.properties?.dataWfKey && c.tagName === "span" ? [c, ...spans(c)] : spans(c)));
}

describe("splitWords", () => {
  it("splits English and Chinese into words, keeping the space after a word on it", () => {
    expect(splitWords("hello big world")).toEqual(["hello ", "big ", "world"]);
    const zh = splitWords("今天天气很好");
    expect(zh.length).toBeGreaterThan(1);
    expect(zh.join("")).toBe("今天天气很好");
  });

  it("keeps leading whitespace on its own", () => {
    expect(splitWords("  hi there")).toEqual(["  ", "hi ", "there"]);
  });
});

describe("rehypeStreamWordFade", () => {
  it("wraps each word in a span.stream-word with a stable key", () => {
    const tree = root(el("p", [text("one two three")]));
    rehypeStreamWordFade(createWordFadeState())(tree);
    const s = spans(tree);
    expect(s.map((x) => (x.children[0] as { value: string }).value)).toEqual(["one ", "two ", "three"]);
    expect(s.every((x) => (x.properties.className as string[]).includes("stream-word"))).toBe(true);
    expect(new Set(s.map((x) => x.properties.dataWfKey)).size).toBe(3);
  });

  it("leaves code blocks and formulas whole, and fades an inline code as one segment", () => {
    const pre = el("pre", [el("code", [text("let a = 1")])]);
    const math = el("span", [text("x^2")], { className: ["katex"] });
    const tree = root(el("p", [text("see "), el("code", [text("a.ts")]), text(" now")]), pre, el("p", [math]));
    rehypeStreamWordFade(createWordFadeState())(tree);
    expect(pre.children[0]).toMatchObject({ tagName: "code", children: [{ type: "text", value: "let a = 1" }] });
    expect(math.children).toEqual([text("x^2")]);
    const s = spans(tree);
    expect(s).toHaveLength(3);
    expect((s[1].children[0] as Element).tagName).toBe("code");
  });

  it("marks a list item's dot to fade with its first word", () => {
    const li = el("li", [text("first item")]);
    rehypeStreamWordFade(createWordFadeState())(root(el("ul", [li])));
    expect(li.properties.dataStreamMarker).toBe(true);
    expect(li.properties.dataWfKey).toBe(spans(li)[0].properties.dataWfKey);
  });

  it("keeps the keys of words seen before as the text grows, and settles them once faded", () => {
    let now = 1000;
    const state = createWordFadeState();
    state.timeline.now = () => now;
    const first = root(el("p", [text("alpha beta")]));
    rehypeStreamWordFade(state)(first);
    const keys = spans(first).map((s) => s.properties.dataWfKey as string);
    for (const k of keys) scheduleSegment(state, k, fakeEl().el);
    now += FADE_MS + 200;
    const second = root(el("p", [text("alpha beta gamma")]));
    rehypeStreamWordFade(state)(second);
    const s = spans(second);
    expect(s.slice(0, 2).map((x) => x.properties.dataWfKey)).toEqual(keys);
    // the old words have had their time: no animation class; the new one still fades
    expect(s[0].properties.className).toBeUndefined();
    expect(s[2].properties.className).toEqual(["stream-word"]);
  });

  it("puts no style on a word: FadeSpan's ref owns the delay", () => {
    const tree = root(el("p", [text("one two")]));
    rehypeStreamWordFade(createWordFadeState())(tree);
    expect(spans(tree).every((x) => x.properties.style === undefined)).toBe(true);
  });

  it("with `plain` (a block fading in once) writes the words already in as plain text", () => {
    let now = 1000;
    const state = createWordFadeState();
    state.timeline.now = () => now;
    const first = root(el("p", [text("alpha beta "), el("code", [text("x")])]), el("ul", [el("li", [text("item")])]));
    rehypeStreamWordFade(state)(first);
    for (const s of spans(first)) scheduleSegment(state, s.properties.dataWfKey as string, fakeEl().el);
    now += FADE_MS + 200;
    const p = el("p", [text("alpha beta "), el("code", [text("x")]), text(" gamma")]);
    const li = el("li", [text("item")]);
    rehypeStreamWordFade(state, true)(root(p, el("ul", [li])));
    // the settled run is one text node, the inline code its own <code> again, only the new word fades
    expect(p.children[0]).toEqual(text("alpha beta "));
    expect(p.children[1]).toMatchObject({ tagName: "code" });
    expect(p.children[2]).toEqual(text(" "));
    expect(p.children[3]).toMatchObject({ tagName: "span", properties: { className: ["stream-word"] } });
    expect(spans(root(p))).toHaveLength(1);
    expect(li.children).toEqual([text("item")]);
    expect(li.properties.dataStreamMarker).toBeUndefined();
  });
});

describe("shownBefore", () => {
  it("finds the timeline a text was drawn on: the same text, or the stream it ends", () => {
    forgetShown();
    let now = 0;
    const stream = createWordFadeState();
    stream.timeline.now = () => now;
    rememberShown("今天先把", stream);
    rememberShown("今天先把构建机改好", stream); // the stream grew: one entry, moved along
    expect(shownBefore("今天先把")).toBeNull();
    scheduleSegment(stream, "k", fakeEl().el);
    expect(shownBefore("今天先把构建机改好，再看测试机")).toEqual({ state: stream, done: false });
    expect(shownBefore("今天先把构建机改好\n")).toEqual({ state: stream, done: false }); // still fading
    now = FADE_MS + 1;
    expect(shownBefore("今天先把构建机改好")).toEqual({ state: stream, done: true });
    expect(shownBefore("别的回答")).toBeNull();
  });

  it("does not take a short reply that happens to start a longer one", () => {
    forgetShown();
    rememberShown("好", createWordFadeState());
    expect(shownBefore("好的，我先看一下构建机的日志")).toBeNull();
  });
});

describe("assignKeys", () => {
  it("reuses a key when the word grew, and finds words that moved", () => {
    const state = createWordFadeState();
    const [a, b] = assignKeys([{ kind: "text", content: "hel" }, { kind: "text", content: "x" }], state);
    const next = assignKeys([{ kind: "text", content: "new" }, { kind: "text", content: "x" }, { kind: "text", content: "hello" }], state);
    expect(next[1]).toBe(b);
    expect(next).not.toContain(a); // "hello" is no longer at "hel"'s place: a new key
    expect(new Set(next).size).toBe(3);
  });
});

describe("scheduleSegment", () => {
  it("starts words 16 ms apart, 4 ms once the queue is behind, never more than 160 ms out", () => {
    const state = createWordFadeState();
    state.timeline.now = () => 0;
    const delays = Array.from({ length: 40 }, (_, i) => scheduleSegment(state, `k${i}`, fakeEl().el));
    expect(delays.slice(0, 4)).toEqual([0, STEP_MS, 2 * STEP_MS, 3 * STEP_MS]);
    expect(delays[7] - delays[6]).toBe(BACKLOG_STEP_MS);
    expect(Math.max(...delays)).toBe(MAX_WAIT_MS);
  });

  it("spreads a stream's bursts over the time between them instead of fading each in at once (#6651)", () => {
    let now = 0;
    const state = createWordFadeState();
    state.timeline.now = () => now;
    const starts: number[] = [];
    // 8 words every 350 ms for 4 s, as a model's bursts come
    for (let burst = 0; burst * 350 <= 4000; burst++) {
      now = burst * 350;
      for (let w = 0; w < 8; w++) starts.push(now + scheduleSegment(state, `b${burst}w${w}`, fakeEl().el));
    }
    const paced = starts.filter((s) => s > 1000);
    const gaps = paced.slice(1).map((s, i) => s - paced[i]);
    expect(Math.max(...gaps)).toBeLessThan(100); // no pause between bursts once the pace is known
    expect(Math.min(...gaps)).toBeGreaterThan(STEP_MS); // and slower than one word a frame
    starts.forEach((s, i) => expect(s - Math.floor(i / 8) * 350).toBeLessThanOrEqual(PACE_MAX_WAIT_MS));
  });

  it("writes the delay into --wf-delay and keeps a key's start when it mounts again", () => {
    let now = 0;
    const state = createWordFadeState();
    state.timeline.now = () => now;
    scheduleSegment(state, "a", fakeEl().el);
    const b = fakeEl();
    scheduleSegment(state, "b", b.el);
    expect(b.props["--wf-delay"]).toBe(`${STEP_MS}ms`);
    now = 10;
    const again = fakeEl();
    expect(scheduleSegment(state, "b", again.el)).toBe(STEP_MS - 10);
    expect(remainingMs(state)).toBe(STEP_MS + FADE_MS - 10);
  });
});

describe("chunkState", () => {
  it("gives each chunk its own keys over the shared timeline, and merges them back into one", () => {
    const owner = createWordFadeState();
    const a = chunkState(owner, "0");
    const b = chunkState(owner, "12");
    expect(a.timeline).toBe(b.timeline);
    const ka = assignKeys([{ kind: "text", content: "one" }], a);
    const kb = assignKeys([{ kind: "text", content: "two" }], b);
    expect(ka[0]).not.toBe(kb[0]);
    const whole = chunkState(owner, "0", true);
    expect(owner.chunks.size).toBe(1);
    expect(assignKeys([{ kind: "text", content: "one" }, { kind: "text", content: "two" }], whole)).toEqual([ka[0], kb[0]]);
  });
});
