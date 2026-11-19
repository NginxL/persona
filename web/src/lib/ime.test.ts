import { describe, expect, it } from "vitest";
import { COMPOSITION_TAIL_MS, imeKey } from "./ime";

// #11237: macOS Safari / WKWebView fire compositionend just before the keydown of the Enter that committed the candidate.
describe("imeKey", () => {
  const idle = { active: false, endedAt: -Infinity };
  it("is the input method's while composing, on Safari's 229, and just after compositionend", () => {
    expect(imeKey({ isComposing: true }, idle, 0)).toBe(true);
    expect(imeKey({ keyCode: 229 }, idle, 0)).toBe(true);
    expect(imeKey({ keyCode: 13 }, { active: true, endedAt: -Infinity }, 0)).toBe(true);
    expect(imeKey({ keyCode: 13 }, { active: false, endedAt: 100 }, 100 + COMPOSITION_TAIL_MS - 1)).toBe(true);
  });
  it("is the person's own key otherwise", () => {
    expect(imeKey({ keyCode: 13 }, idle, 0)).toBe(false);
    expect(imeKey({ keyCode: 13 }, { active: false, endedAt: 100 }, 100 + COMPOSITION_TAIL_MS)).toBe(false);
  });
});
