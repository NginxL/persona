import { describe, expect, it } from "vitest";
import type { Interaction } from "./types";
import { holdsTheTurn, splitTurnAsks, type TurnAsk } from "./turn-asks";

const ix = (id: string, more: Partial<Interaction> = {}) => ({ id, machine_id: "hub", session_id: "c1", kind: "permission", ...more }) as Interaction;

describe("splitTurnAsks", () => {
  it("takes the conversation's own cards and leaves a folded worker's, a side session's and another chat's", () => {
    const own = ix("own");
    const subagent = ix("sub", { agent_id: "a1", agent_type: "Explore" });
    const worker = ix("worker", { session_id: "w1", folded_under: "c1", folded_as: "worker" });
    const side = ix("side", { session_id: "s1", folded_side: "c1", folded_as: "side" });
    const other = ix("other", { session_id: "c2" });
    const elsewhere = ix("elsewhere", { machine_id: "mac" });
    const { turn, rest } = splitTurnAsks([worker, own, side, subagent, other, elsewhere], "hub", "c1");
    expect(turn.map((x) => x.id)).toEqual(["own", "sub"]);
    expect(rest.map((x) => x.id)).toEqual(["worker", "side", "other", "elsewhere"]);
  });

  it("does not let a card folded under another session count as its own, even on the same id", () => {
    expect(holdsTheTurn(ix("odd", { folded_under: "lead" }), "hub", "c1")).toBe(false);
    expect(holdsTheTurn(ix("odd", { folded_side: "lead" }), "hub", "c1")).toBe(false);
  });

  it("is the only way to a TurnAsk", () => {
    // @ts-expect-error -- a plain card is no TurnAsk: only splitTurnAsks / holdsTheTurn make one
    const plain: TurnAsk = ix("own");
    expect(plain.id).toBe("own");
  });
});
