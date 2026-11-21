// @vitest-environment jsdom
import { act, createElement } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";

// draw() imports Markdown afresh below. Evaluate its module graph once while collecting this file: under the complete
// parallel suite a cold first import can exceed the unchanged five-second test deadline and leave later draws empty.
beforeAll(async () => {
  await import("./Markdown");
});

// What a click copies (atoms.copyText, which also says "Copied").
const copied = vi.hoisted(() => vi.fn(async (_text: string) => true));
vi.mock("./atoms", async (orig) => ({ ...(await orig<typeof import("./atoms")>()), copyText: (text: string) => copied(text) }));

// Code blocks, inline code and quotes in a reply, as Telegram draws them (#2984).
describe("reply blocks (#2984)", () => {
  let host: HTMLDivElement;
  let root: Root;
  beforeEach(() => {
    (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
    host = document.createElement("div");
    document.body.appendChild(host);
    root = createRoot(host);
    copied.mockClear();
  });
  afterEach(() => {
    act(() => root.unmount());
    host.remove();
  });

  async function draw(text: string, streaming = false) {
    const { Markdown } = await import("./Markdown");
    await act(async () => root.render(createElement(Markdown, { text, streaming })));
  }

  it("a code block has its language and Copy in a bar on top; Copy takes the whole block", async () => {
    await draw("```ts\nconst a = 1\nreturn a\n```\n\n```\nrm -rf .cache\n```");
    const bars = host.querySelectorAll("[data-code-bar]");
    expect(bars).toHaveLength(2);
    expect(bars[0].textContent).toContain("ts");
    expect(bars[1].querySelector("span")).toBeNull(); // no language, no label
    act(() => (bars[0].querySelector("button") as HTMLButtonElement).click());
    expect(copied).toHaveBeenCalledWith("const a = 1\nreturn a");
  });

  it("a click on inline code copies it; in a link it is the link's", async () => {
    await draw("先跑 `npm run build`，见 [`文档`](https://example.com/docs)。");
    const code = host.querySelector("[data-copy-code]") as HTMLElement;
    expect(code.textContent).toBe("npm run build");
    expect(code.getAttribute("role")).toBe("button");
    act(() => code.click());
    expect(copied).toHaveBeenCalledWith("npm run build");
    const linked = host.querySelector("a code") as HTMLElement;
    expect(linked.hasAttribute("data-copy-code")).toBe(false);
    expect(linked.hasAttribute("data-linked")).toBe(false);
  });

  it("leaves room where inline code meets a CJK letter, none beside a space or CJK punctuation (#9477)", async () => {
    await draw("用`pnpm`装依赖，提交是 `3854bdc`，写成`设置`。见[`docs`](https://example.com)里\n\n**注意**`HOME`要指向 v`x1`\n\n```\n用`x`装\n```");
    const sides = (text: string) => {
      const code = [...host.querySelectorAll("code")].find((c) => c.textContent === text)!;
      const gap = code.getAttribute("data-gap") ?? "";
      return [gap.includes("s"), gap.includes("e")];
    };
    expect(sides("pnpm")).toEqual([true, true]);
    expect(sides("3854bdc")).toEqual([false, false]);
    expect(sides("设置")).toEqual([true, false]);
    // only plain words beside it count: code alone in a link, or after bold words, gets none on that side
    expect(sides("docs")).toEqual([false, false]);
    expect(sides("HOME")).toEqual([false, true]);
    expect(sides("x1")).toEqual([false, false]); // a Latin letter: no room added
    expect(host.querySelector("pre code")!.hasAttribute("data-gap")).toBe(false);
  });

  it("a quote has its mark; a long one folds, opens on a click and folds again", async () => {
    // jsdom lays nothing out: the quote's words say they are tall
    const tall = vi.spyOn(HTMLElement.prototype, "scrollHeight", "get").mockReturnValue(400);
    try {
      await draw("> 短的引用");
      expect(host.querySelector("[data-reply-quote] svg")).not.toBeNull();
      await draw("> 很长的引用\n\n正文");
      const body = host.querySelector("[data-reply-quote] > div") as HTMLElement;
      expect(body.style.maxHeight).toBe("104px");
      const fold = host.querySelector("[data-quote-fold]") as HTMLButtonElement;
      expect(fold.getAttribute("aria-expanded")).toBe("false");
      act(() => body.click());
      expect(body.style.maxHeight).toBe("");
      expect(fold.getAttribute("aria-expanded")).toBe("true");
      act(() => fold.click());
      expect(body.style.maxHeight).toBe("104px");
      // a reply still being written does not fold its quotes
      await draw("> 很长的引用\n\n正文", true);
      expect(host.querySelector("[data-quote-fold]")).toBeNull();
    } finally {
      tall.mockRestore();
    }
  });
});
