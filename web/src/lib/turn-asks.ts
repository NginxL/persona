// Which of a conversation's waiting cards hold its own turn. A Persona chat's own request (a permission, a question, a
// confirmation from the conversation's own session) stops its turn until it is answered: the chat cannot go on, so the
// card stands above the dock's capsules, never folded behind one (the Needs you tray's count or the waiting stack).
// A card from a session folded under it (a worker, #5120; a side session, #7944) does not hold the turn: the chat goes
// on while that session waits, so it stays where it was, in the stack and behind Needs you.
import type { Interaction } from "./types";

declare const holdsTurn: unique symbol;

/** A card the conversation's own turn waits on. Only `splitTurnAsks` makes one, so a view drawn for these alone (always
 *  open, above the capsules) cannot be handed any other card: that is a type error, not a string match at runtime. */
export type TurnAsk = Interaction & { readonly folded_under?: undefined; readonly folded_side?: undefined; readonly [holdsTurn]: true };

/** Whether `ix` is the conversation `sid`'s own request on its computer `mid`, not one of a session folded under it. */
export const holdsTheTurn = (ix: Interaction, mid: string, sid: string): ix is TurnAsk =>
  ix.machine_id === mid && ix.session_id === sid && !ix.folded_under && !ix.folded_side;

/** The conversation's waiting cards, in their order: those its own turn waits on, and the rest. */
export const splitTurnAsks = (cards: readonly Interaction[], mid: string, sid: string) => ({
  turn: cards.filter((ix): ix is TurnAsk => holdsTheTurn(ix, mid, sid)),
  rest: cards.filter((ix) => !holdsTheTurn(ix, mid, sid)),
});
