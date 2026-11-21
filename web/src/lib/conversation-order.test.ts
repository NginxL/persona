import { describe, expect, it } from "vitest";
import { byConversationOrder } from "./conversation-order";
import { conversationOrderAfterMove, conversationOrderAfterStep } from "./conversation-order-edit";

const rows = ["a", "b", "c", "d"].map((session_id) => ({ session_id }));
const ids = (list: readonly { session_id: string }[]) => list.map((row) => row.session_id);

describe("conversation order", () => {
  it("leaves the input order untouched without a saved preference", () => {
    expect(ids(byConversationOrder(rows))).toEqual(["a", "b", "c", "d"]);
    expect(ids(byConversationOrder(rows, []))).toEqual(["a", "b", "c", "d"]);
  });

  it("keeps saved and newly arrived rows visible in deterministic positions", () => {
    expect(ids(byConversationOrder(rows, ["c", "a", "not-loaded"]))).toEqual(["c", "a", "b", "d"]);
  });

  it("drags within the complete list and preserves ids only another client loaded", () => {
    expect(conversationOrderAfterMove(rows, ["c", "a", "elsewhere"], "c", "d")).toEqual(["a", "b", "d", "c", "elsewhere"]);
    expect(conversationOrderAfterMove(rows, ["c", "a", "elsewhere"], "b", "c")).toEqual(["b", "c", "a", "d", "elsewhere"]);
  });

  it("uses the same order for accessible steps and never drops a row at an edge", () => {
    expect(conversationOrderAfterStep(rows, undefined, "b", -1)).toEqual(["b", "a", "c", "d"]);
    expect(conversationOrderAfterStep(rows, ["elsewhere"], "a", 1)).toEqual(["b", "a", "c", "d", "elsewhere"]);
    expect(conversationOrderAfterStep(rows, ["elsewhere"], "a", -1)).toEqual(["a", "b", "c", "d", "elsewhere"]);
  });
});
