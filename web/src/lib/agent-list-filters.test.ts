import { describe, expect, it } from "vitest";
import { DEFAULT_DIMENSION_FILTERS, type AgentDimensionFilters } from "./agent-chatlist-filter";
import { pagedFilterOf } from "./agent-list-filters";

const with_ = (patch: Partial<AgentDimensionFilters>): AgentDimensionFilters => ({ ...DEFAULT_DIMENSION_FILTERS, ...patch });

describe("what the hub's paged list can narrow by", () => {
  it("sends the one computer as machine_id, and nothing for the words (they go as q)", () => {
    expect(pagedFilterOf(DEFAULT_DIMENSION_FILTERS)).toEqual({});
    expect(pagedFilterOf(with_({ query: "deploy" }))).toEqual({});
    expect(pagedFilterOf(with_({ machines: ["box"] }))).toEqual({ machine_id: "box" });
  });
  it("is null for every choice the hub has no parameter for, so the complete list answers", () => {
    for (const patch of [{ machines: ["a", "b"] }, { harnesses: ["claude"] }, { projects: ["demo"] }, { models: ["m"] }, { pinnedOnly: true }, { scope: "title" as const }, { since: "7d" as const }, { searchAsCode: true }]) {
      expect(pagedFilterOf(with_(patch))).toBeNull();
    }
  });
});
