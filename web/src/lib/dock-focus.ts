// The keyboard's place after a card above the composer is answered (#5154): the button pressed goes (the card turns
// into its Answered receipt) or is disabled while it sends, and the browser drops the focus onto the page's body, so the
// next words typed go nowhere and Tab starts over from the sidebar. The chat puts it back in its composer instead.

/** Whether the focus was lost with `last`, the control above the composer it was on: nothing (the page's body) has it
 *  now, and `last` is gone from the page or can no longer be focused (disabled, or in a disabled fieldset). */
export function lostWith(last: Element | null, active: Element | null, body: Element | null): boolean {
  if (!last) return false;
  if (active && active !== body) return false;
  if (!last.isConnected) return true;
  return last.matches(":disabled") || Boolean(last.closest("fieldset:disabled"));
}

/** The chat's own message box (Composer's `data-composer-input`). Quote bars and comment cards near it hold textareas too:
 *  focusing and blurring "the composer" never takes one of those by mistake. */
export const COMPOSER_INPUT = "textarea[data-composer-input]";

const EDITABLE = "input, textarea, select, [contenteditable=''], [contenteditable='true']";

/** Whether the focus is already somewhere a person may be typing or choosing (another field, or inside a dialog or
 *  menu), so a deferred autofocus of `own` would take it away from them. */
export function focusTaken(active: Element | null, own: Element | null, body: Element | null = document.body): boolean {
  if (!active || active === body || active === own) return false;
  if (active.matches(EDITABLE) || (active as HTMLElement).isContentEditable) return true;
  return Boolean(active.closest("[role=dialog], [role=alertdialog], [role=menu], [role=listbox]"));
}
