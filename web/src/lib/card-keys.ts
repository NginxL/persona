// A waiting card's own keyboard (docs/web/web.md "Keyboard shortcuts"): while a card has keyboard focus, digits 1–9
// pick its options, a bare Enter runs its main action and Esc goes back to the composer — never an answer. This file
// is the part with no DOM of its own: whose key it is, and where focus goes. The cards hang these on their root
// (useCardKeys in views/chat/Interactions.tsx); the shortcut that hands the card the keyboard is "focus-card"
// (lib/shortcuts.ts, lib/use-shortcuts.ts).
import { COMPOSER_INPUT } from "./dock-focus";
import { composing } from "./send-history";
import { EDITABLE } from "./shortcuts";

/** What a key press on a card is: Esc back to the composer, a digit picking an option, Enter running the main action;
 *  null when the key is someone else's. */
export type CardKeyAction = { kind: "esc" } | { kind: "digit"; digit: number } | { kind: "enter" };

/** Controls with their own keyboard semantics: an Enter on one of them is that control's (a focused Deny button's
 *  Enter denies), not the card's main action — and a digit pressed while one has focus is its click's, not the card's. */
const INTERACTIVE = "button, a[href], summary, [role='button'], [role='link'], [role^='menuitem'], [role='option'], [role='tab'], [role='radio'], [role='checkbox'], [role='switch']";

/** A card shown but not answerable here (a session this page cannot approve): InteractionCard wraps it in a disabled
 *  fieldset, which stops its buttons but not a plain div's focus — so the card's own keys stand down there too. */
const UNANSWERABLE = "fieldset[disabled]";

export interface CardKeyEvent {
  key: string;
  isComposing?: boolean;
  keyCode?: number;
  ctrlKey: boolean;
  metaKey: boolean;
  altKey: boolean;
  shiftKey: boolean;
  target: EventTarget | null;
}

/**
 * What a keydown inside a waiting card is for. `root` is the card's own element (the card's handler only sees its
 * subtree, so this is a guard, not the boundary). Yields — null — to: an input method composing (the Enter or digit
 * that picks a candidate is not ours), any chord (the shortcuts' Mod keys, and a browser or system key), typing in an
 * editable element anywhere in the card, a key that belongs to the control that has focus (a button's Enter activates
 * it; a digit is its to press), and every key of a card this page cannot answer. Esc is the card's wherever in it
 * focus sits, with no modifier held.
 */
export function cardKeyAction(e: CardKeyEvent, root: Element | null): CardKeyAction | null {
  if (composing(e)) return null;
  if (e.ctrlKey || e.metaKey || e.altKey) return null;
  const el = e.target instanceof Element ? e.target : null;
  if (!el || !root?.contains(el)) return null;
  if (root.closest(UNANSWERABLE)) return null;
  if (e.key === "Escape") return { kind: "esc" };
  if (e.shiftKey) return null;
  if (el.closest(EDITABLE) || (el as HTMLElement).isContentEditable) return null;
  // the focused action beats the main one: a control that activates itself keeps its keys
  if (el !== root && el.closest(INTERACTIVE)) return null;
  if (/^[1-9]$/.test(e.key)) return { kind: "digit", digit: Number(e.key) };
  if (e.key === "Enter") return { kind: "enter" };
  return null;
}

/** The chat's composer on this page, if there is one to type into; the card's Esc hands focus back to it. The dock
 *  holds it (only there — a comment being edited in the transcript is not the composer). */
export function focusComposer(): boolean {
  const el = [...document.querySelectorAll<HTMLTextAreaElement>(`[data-chat-dock] ${COMPOSER_INPUT}`)].find((t) => !t.disabled && t.getClientRects().length > 0);
  if (!el) return false;
  el.focus();
  return true;
}

/** The first waiting card on the page that is showing and can be answered here (what the "focus-card" shortcut gives
 *  the keyboard). */
export function focusWaitingCard(): boolean {
  const el = [...document.querySelectorAll<HTMLElement>(`[data-card-keys]:not(${UNANSWERABLE} *)`)]
    .find((e) => e.getClientRects().length > 0);
  if (!el) return false;
  el.focus();
  return true;
}
