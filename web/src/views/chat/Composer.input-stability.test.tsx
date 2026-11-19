// @vitest-environment jsdom
// Input methods, palettes, measuring and focus in the chat composer (#11237).
import { act, type ComponentProps } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { COMPOSER_INPUT } from "../../lib/dock-focus";
import { COMPOSITION_TAIL_MS } from "../../lib/ime";
import { loadPages } from "../../lib/lazy-page";
import { Composer, type Outgoing } from "./Composer";

// a skill catalog for the skill-aware "/" palette (a chat whose mention source offers skills)
const catalog = vi.hoisted(() => ({ skills: [{ id: "review", name: "/review", description: "Review the change", source: "project" }] }));
vi.mock("./useSkillCatalog", () => ({ useSkillCatalog: () => ({ catalog, status: undefined }) }));

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let host: HTMLDivElement;
let root: Root;
let now = 1_000;
const scroll = Object.getOwnPropertyDescriptor(HTMLElement.prototype, "scrollHeight");

beforeAll(async () => {
  Element.prototype.scrollIntoView ??= () => {};
  await loadPages();
});

beforeEach(() => {
  localStorage.clear();
  now = 1_000;
  vi.spyOn(performance, "now").mockImplementation(() => now);
  // a precise pointer: Enter sends
  vi.stubGlobal("matchMedia", (query: string) => ({ media: query, matches: query.includes("fine"), addEventListener() {}, removeEventListener() {} }));
  Object.defineProperty(HTMLTextAreaElement.prototype, "scrollHeight", {
    configurable: true,
    get(this: HTMLTextAreaElement) { return this.value.split("\n").length * 24; },
  });
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
  document.body.replaceChildren();
  if (scroll) Object.defineProperty(HTMLTextAreaElement.prototype, "scrollHeight", scroll);
  else delete (HTMLTextAreaElement.prototype as { scrollHeight?: number }).scrollHeight;
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

const mount = (props: Partial<ComponentProps<typeof Composer>> = {}) =>
  act(() => root.render(<Composer draftKey="stability" placeholder="" onSend={async () => true} {...props} />));
const box = () => host.querySelector<HTMLTextAreaElement>(COMPOSER_INPUT)!;
const valueSetter = Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")!.set!;
const type = async (value: string) => act(async () => {
  const el = box();
  valueSetter.call(el, value);
  el.setSelectionRange(value.length, value.length);
  el.dispatchEvent(new Event("input", { bubbles: true }));
  await Promise.resolve();
});
const compose = (type: "compositionstart" | "compositionend") => act(() => { box().dispatchEvent(new CompositionEvent(type, { bubbles: true, data: "中" })); });
const press = async (extra: KeyboardEventInit = {}) => {
  const event = new KeyboardEvent("keydown", { key: "Enter", keyCode: 13, bubbles: true, cancelable: true, ...extra });
  await act(async () => {
    box().dispatchEvent(event);
    await Promise.resolve();
  });
  return event;
};
const settle = () => act(async () => { for (let i = 0; i < 20; i++) await Promise.resolve(); });

describe("an input method's Enter", () => {
  it("is not a send when Safari fires it right after compositionend", async () => {
    const onSend = vi.fn(async (_m: Outgoing) => true);
    mount({ onSend });
    compose("compositionstart");
    await type("你好");
    compose("compositionend");
    now += 10;
    expect((await press()).defaultPrevented).toBe(false);
    expect(onSend).not.toHaveBeenCalled();
    expect(box().value).toBe("你好");

    now += COMPOSITION_TAIL_MS;
    expect((await press()).defaultPrevented).toBe(true);
    expect(onSend.mock.calls.map(([m]) => m.text)).toEqual(["你好"]);
  });

  it("is not a send while the composition is open, even without isComposing", async () => {
    const onSend = vi.fn(async (_m: Outgoing) => true);
    mount({ onSend });
    compose("compositionstart");
    await type("ni");
    expect((await press()).defaultPrevented).toBe(false);
    expect(onSend).not.toHaveBeenCalled();
  });

  it("opens no slash palette in the middle of a composition, and opens it once it ends", async () => {
    mount({ commands: [{ word: "/compact", desc: "Compact", text: "/compact" }] });
    compose("compositionstart");
    await type("/co");
    await settle();
    expect(host.querySelector("[data-slash-palette]")).toBeNull();
    compose("compositionend");
    await settle();
    expect(host.querySelector("[data-slash-palette]")).not.toBeNull();
  });

  it("opens no @ panel in the middle of a composition", async () => {
    mount({ mention: { files: () => ["/code/app/lib/links.ts"] } });
    act(() => box().focus());
    compose("compositionstart");
    await type("see @li");
    await settle();
    expect(host.querySelector('[data-composer-panel="at"]')).toBeNull();
    compose("compositionend");
    await act(async () => {
      await vi.waitFor(() => expect(host.querySelector('[data-composer-panel="at"]')).not.toBeNull());
    });
  });
});

describe("Shift+Enter with the slash palette open", () => {
  it("is a new line: no row is picked and nothing is sent", async () => {
    const onSend = vi.fn(async (_m: Outgoing) => true);
    mount({ commands: [{ word: "/compact", desc: "Compact", text: "/compact" }], onSend });
    await type("/compact");
    await act(async () => {
      await vi.waitFor(() => expect(host.querySelector("[data-slash-palette] [role=option]")).not.toBeNull());
    });
    expect((await press({ shiftKey: true })).defaultPrevented).toBe(false);
    expect(box().value).toBe("/compact");
    expect(onSend).not.toHaveBeenCalled();
    // a plain Enter still picks the row
    expect((await press()).defaultPrevented).toBe(true);
  });
});

describe("Alt+Enter with a menu open", () => {
  it("never sends the half-typed /command or @file, and still sends without a menu", async () => {
    const onSend = vi.fn(async (_m: Outgoing) => true);
    mount({ commands: [{ word: "/compact", desc: "Compact", text: "/compact" }], mention: { files: () => ["/code/app/lib/links.ts"] }, onSend });
    act(() => box().focus());
    await type("/com");
    await act(async () => {
      await vi.waitFor(() => expect(host.querySelector("[data-slash-palette] [role=option]")).not.toBeNull());
    });
    await press({ altKey: true });
    expect(onSend).not.toHaveBeenCalled();
    expect(box().value).toBe("/com");

    await type("see @lin");
    await act(async () => {
      await vi.waitFor(() => expect(host.querySelector('[data-composer-panel="at"] [role=option]')).not.toBeNull());
    });
    await press({ altKey: true });
    expect(onSend).not.toHaveBeenCalled();
    expect(box().value).toBe("see @lin");

    // without a menu, Alt+Enter sends on a desktop as before (lib/viewport.ts enterSends)
    await type("done here");
    expect((await press({ altKey: true })).defaultPrevented).toBe(true);
    expect(onSend.mock.calls.map(([m]) => m.text)).toEqual(["done here"]);
  });
});

describe("the skill palette and Safari's committing Enter", () => {
  const skills = { files: () => [], skills: { manager: "assistant", environment: "test" } };
  const option = () => host.querySelector("[data-slash-palette] [role=option]");
  // Opens the palette once so its lazy chunk is in (React reveals a lazy part on its scheduler's clock, which these
  // tests hold still), then empties the box.
  const loadPalette = async () => {
    await type("/rev");
    await vi.waitFor(async () => {
      await act(async () => { now += 100; await new Promise((r) => setTimeout(r, 20)); });
      expect(option()).not.toBeNull();
    }, { timeout: 8_000, interval: 20 });
    await type("");
  };
  it("opened by compositionend, does not take the Enter right after it as a pick", async () => {
    const onSend = vi.fn(async (_m: Outgoing) => true);
    mount({ mention: skills, commands: [], onSend });
    act(() => box().focus());
    await loadPalette();
    compose("compositionstart");
    await type("/rev");
    await settle();
    expect(option()).toBeNull();
    compose("compositionend");
    await settle();
    expect(option()).not.toBeNull();
    now += 10;
    expect((await press()).defaultPrevented).toBe(false);
    expect(box().value).toBe("/rev");
    expect(onSend).not.toHaveBeenCalled();
    // the person's own Enter, a moment later, picks the skill
    now += COMPOSITION_TAIL_MS;
    expect((await press()).defaultPrevented).toBe(true);
    expect(box().value).not.toBe("/rev");
    expect(onSend).not.toHaveBeenCalled();
  }, 20_000);

  it("picks again after a blur in the middle of a composition", async () => {
    mount({ mention: skills, commands: [] });
    act(() => box().focus());
    await loadPalette();
    await type("/rev");
    await settle();
    expect(option()).not.toBeNull();
    compose("compositionstart");
    act(() => box().blur());
    act(() => box().focus());
    now += 1_000;
    expect((await press()).defaultPrevented).toBe(true);
    expect(box().value).not.toBe("/rev");
  }, 20_000);
});

describe("measuring the box", () => {
  // jsdom lays nothing out: a collapse to 0 resets the box's own scroll, as a browser does
  const watch = (el: HTMLTextAreaElement) => {
    const heights: string[] = [];
    let top = 0;
    let height = "";
    Object.defineProperty(el, "scrollTop", { configurable: true, get: () => top, set: (v: number) => { top = v; } });
    Object.defineProperty(el.style, "height", {
      configurable: true,
      get: () => height,
      set: (v: string) => {
        height = v;
        heights.push(v);
        if (v === "0px") top = 0;
      },
    });
    return { heights, scrollTo: (v: number) => { top = v; }, top: () => top };
  };
  const lines = (n: number) => Array.from({ length: n }, (_, i) => `step ${i + 1}`).join("\n");

  it("keeps its scroll position when it is measured again", async () => {
    mount();
    const seen = watch(box());
    await type(lines(9));
    seen.scrollTo(120);
    await type(lines(8));
    expect(seen.heights).toContain("0px");
    expect(seen.top()).toBe(120);
  });

  it("is never collapsed mid-composition, nor while it only grows past the cap", async () => {
    mount();
    const seen = watch(box());
    await type(lines(8));
    seen.heights.length = 0;
    seen.scrollTo(80);
    await type(`${lines(8)}\nstep 9`);
    await type(`${lines(8)}\nstep 9 more`);
    // deleting mid-composition waits: the box shrinks once the composition ends
    compose("compositionstart");
    await type(lines(3));
    expect(seen.heights).not.toContain("0px");
    expect(seen.top()).toBe(80);
    compose("compositionend");
    expect(seen.heights).toContain("0px");
    expect(seen.top()).toBe(80);
  });
});

describe("the composer's focus", () => {
  it("marks its own field, apart from any other textarea near it", () => {
    mount();
    expect(box()).not.toBeNull();
    expect(host.querySelectorAll(COMPOSER_INPUT)).toHaveLength(1);
  });

  it("does not take the focus from another field on mount", async () => {
    vi.useFakeTimers();
    try {
      const other = document.createElement("input");
      document.body.append(other);
      mount({ autoFocus: true });
      other.focus();
      await act(async () => { vi.runAllTimers(); });
      expect(document.activeElement).toBe(other);
    } finally { vi.useRealTimers(); }
  });

  it("takes it when nothing else has it", async () => {
    vi.useFakeTimers();
    try {
      mount({ autoFocus: true });
      await act(async () => { vi.runAllTimers(); });
      expect(document.activeElement).toBe(box());
    } finally { vi.useRealTimers(); }
  });
});

describe("a re-addressed conversation (carryDraft)", () => {
  it("keeps the words being typed when only the key changes", async () => {
    mount({ draftKey: "cached-hub/main", carryDraft: true });
    await type("still typing");
    mount({ draftKey: "hub/main", carryDraft: true });
    expect(box().value).toBe("still typing");
    expect(localStorage.getItem("draft:cached-hub/main")).toBeNull();
  });

  it("switches drafts as before without it", async () => {
    mount({ draftKey: "one/chat" });
    await type("words for one");
    mount({ draftKey: "two/chat" });
    expect(box().value).toBe("");
  });
});
