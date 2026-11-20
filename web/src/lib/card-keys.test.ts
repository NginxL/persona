// @vitest-environment jsdom
// cardKeyAction: whose a key pressed inside a waiting card is (lib/card-keys.ts). Esc is the card's wherever in it
// focus sits; digits and a bare Enter are the card's only when the key is nobody else's — not an input method's, not a
// chord's, not a text field's, and not the focused control's own to press. A card inside a disabled fieldset (a session
// this page cannot answer) has no keys at all. focusComposer / focusWaitingCard are the two ways focus goes (jsdom's
// elements have no boxes, so every element counts as showing).
import { afterEach, beforeAll, beforeEach, describe, expect, it } from "vitest";
import { cardKeyAction, focusComposer, focusWaitingCard } from "./card-keys";

beforeAll(() => {
  // jsdom gives elements no boxes; without this every element would count as hidden
  Element.prototype.getClientRects = function () {
    return [{}] as unknown as DOMRectList;
  };
});

beforeEach(() => {
  document.body.innerHTML = `
    <main><div data-chat-dock><textarea id="quote-note"></textarea><textarea id="composer" data-composer-input></textarea></div></main>
    <div id="card" data-card-keys tabindex="-1">
      <button id="deny">Deny</button>
      <input id="field" />
      <span id="text">what it asks</span>
    </div>
    <fieldset disabled><div id="locked" data-card-keys tabindex="-1"><button id="lockedDeny">Deny</button></div></fieldset>
    <div id="other"></div>`;
});

afterEach(() => {
  document.body.innerHTML = "";
});

const press = (key: string, target: string, mods: Partial<{ ctrlKey: boolean; metaKey: boolean; altKey: boolean; shiftKey: boolean; isComposing: boolean; keyCode: number }> = {}) => ({
  key,
  ctrlKey: false,
  metaKey: false,
  altKey: false,
  shiftKey: false,
  target: document.getElementById(target),
  ...mods,
});

const card = () => document.getElementById("card")!;

describe("a waiting card's keys", () => {
  it("takes Esc from anywhere in the card, with no modifier held", () => {
    for (const at of ["card", "deny", "field", "text"]) {
      expect(cardKeyAction(press("Escape", at), card())).toEqual({ kind: "esc" });
    }
    expect(cardKeyAction(press("Escape", "other"), card())).toBeNull();
    expect(cardKeyAction(press("Escape", "card", { ctrlKey: true }), card())).toBeNull();
    expect(cardKeyAction(press("Escape", "card", { altKey: true }), card())).toBeNull();
  });

  it("takes a digit 1–9 on the card itself or on plain content, but not from a field, a control, shifted, with a modifier, or outside", () => {
    expect(cardKeyAction(press("3", "card"), card())).toEqual({ kind: "digit", digit: 3 });
    expect(cardKeyAction(press("9", "text"), card())).toEqual({ kind: "digit", digit: 9 });
    // a digit with a control focused is that control's to press (its click's), not the card's
    expect(cardKeyAction(press("1", "deny"), card())).toBeNull();
    expect(cardKeyAction(press("3", "field"), card())).toBeNull();
    expect(cardKeyAction(press("3", "card", { shiftKey: true }), card())).toBeNull();
    expect(cardKeyAction(press("3", "card", { ctrlKey: true }), card())).toBeNull();
    expect(cardKeyAction(press("0", "card"), card())).toBeNull();
    expect(cardKeyAction(press("3", "other"), card())).toBeNull();
  });

  it("takes Enter on the card itself or on plain content, and leaves a focused control's Enter to it", () => {
    expect(cardKeyAction(press("Enter", "card"), card())).toEqual({ kind: "enter" });
    expect(cardKeyAction(press("Enter", "text"), card())).toEqual({ kind: "enter" });
    // the focused action: a button's Enter is its own (Deny stays an explicit press)
    expect(cardKeyAction(press("Enter", "deny"), card())).toBeNull();
    expect(cardKeyAction(press("Enter", "field"), card())).toBeNull();
    expect(cardKeyAction(press("Enter", "other"), card())).toBeNull();
    for (const mods of [{ ctrlKey: true }, { metaKey: true }, { altKey: true }, { shiftKey: true }] as const) {
      expect(cardKeyAction(press("Enter", "card", mods), card())).toBeNull();
    }
  });

  it("has no keys at all for a card this page cannot answer (a disabled fieldset)", () => {
    const locked = document.getElementById("locked")!;
    for (const k of ["Escape", "1", "Enter"]) {
      expect(cardKeyAction(press(k, "locked"), locked)).toBeNull();
      expect(cardKeyAction(press(k, "lockedDeny"), locked)).toBeNull();
    }
  });

  it("yields to an input method composing, and to keys that are no card's", () => {
    for (const composing of [{ isComposing: true }, { keyCode: 229 }]) {
      expect(cardKeyAction(press("Escape", "card", composing), card())).toBeNull();
      expect(cardKeyAction(press("1", "card", composing), card())).toBeNull();
      expect(cardKeyAction(press("Enter", "card", composing), card())).toBeNull();
    }
    expect(cardKeyAction(press("k", "card"), card())).toBeNull();
    expect(cardKeyAction(press(" ", "card"), card())).toBeNull();
    expect(cardKeyAction(press("Backspace", "card"), card())).toBeNull();
    expect(cardKeyAction(press("1", "card"), null)).toBeNull();
  });
});

describe("where focus goes", () => {
  it("Esc's target: the chat's composer in its dock (not a textarea elsewhere on the page, nor a quote's note before it)", () => {
    const stray = document.createElement("textarea");
    stray.id = "stray";
    document.getElementById("other")!.append(stray);
    expect(focusComposer()).toBe(true);
    expect(document.activeElement).toBe(document.getElementById("composer"));
  });

  it("the shortcut's target: the first waiting card that is showing and can be answered", () => {
    expect(focusWaitingCard()).toBe(true);
    expect(document.activeElement).toBe(card());
    // the locked card is never the target
    document.getElementById("card")!.remove();
    (document.getElementById("composer") as HTMLTextAreaElement).focus();
    expect(focusWaitingCard()).toBe(false);
    // no card, no handoff: the caller leaves the key alone
    document.body.querySelector("#locked")!.closest("fieldset")!.remove();
    expect(focusWaitingCard()).toBe(false);
  });
});
