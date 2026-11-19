// When the web looks at the hub's to-do view at all (#10226; docs/web/web.md "Developing Persona"). Everyone else runs
// exactly today's code: the shadow compares only while gates 1 and 2 hold, and the To-dos page draws from the view
// only with gate 3 as well.

import { devModeSince } from "../../../lib/dev-probe";
import { hubHas } from "../../../lib/store";

/** The hello feature that says every manager puts `view` on its rows. */
export const TODO_VIEW_FEATURE = "todo_view";
/** This browser's switch for drawing the To-dos sections from the view: "1" while on. No settings row: set by hand. */
export const VIEW_DRAW_KEY = "persona.todos.viewDraw";

/** Gates 1 and 2: the Developing Persona switch is on, and the hub lists `todo_view`. */
export function viewShadowOn(features: string[]): boolean {
  return Boolean(devModeSince()) && hubHas(features, TODO_VIEW_FEATURE);
}

/** Gate 3 on top, for drawing only: `localStorage persona.todos.viewDraw` is "1". */
export function viewDrawOn(features: string[]): boolean {
  if (!viewShadowOn(features)) return false;
  try {
    return localStorage.getItem(VIEW_DRAW_KEY) === "1";
  } catch {
    return false;
  }
}
