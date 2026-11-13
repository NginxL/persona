import { describe, expect, it } from "vitest";
import { foldsBackgroundRounds } from "./background-fold";
import type { AssistantInfo } from "./types";

const assistant = (over: Partial<AssistantInfo> = {}): AssistantInfo =>
  ({ machine_id: "m1", conversations: [], ...over }) as AssistantInfo;

describe("foldsBackgroundRounds", () => {
  it("folds in the main conversation while the switch is on", () => {
    expect(foldsBackgroundRounds("main", assistant({ main: "main", main_background_folded: true }))).toBe(true);
  });

  it("folds in a leader conversation while the switch is on (#6595)", () => {
    const info = assistant({ main: "main", leaders: ["lead-1", "lead-2"], main_background_folded: true });
    expect(foldsBackgroundRounds("lead-1", info)).toBe(true);
    expect(foldsBackgroundRounds("lead-2", info)).toBe(true);
  });

  it("never folds an ordinary side conversation", () => {
    const info = assistant({ main: "main", leaders: ["lead-1"], main_background_folded: true });
    expect(foldsBackgroundRounds("side", info)).toBe(false);
  });

  it("the switch off disables folding in the main and the leaders", () => {
    const info = assistant({ main: "main", leaders: ["lead-1"], main_background_folded: false });
    expect(foldsBackgroundRounds("main", info)).toBe(false);
    expect(foldsBackgroundRounds("lead-1", info)).toBe(false);
  });

  it("a hub that omits the switch means on, and one that sends no assistant means off", () => {
    expect(foldsBackgroundRounds("lead-1", assistant({ main: "main", leaders: ["lead-1"] }))).toBe(true);
    expect(foldsBackgroundRounds("main", assistant({ main: "main" }))).toBe(true);
    expect(foldsBackgroundRounds("main", undefined)).toBe(false);
  });

  it("a leader id equal to the main does not break the main case", () => {
    expect(foldsBackgroundRounds("main", assistant({ main: "main", leaders: ["main"] }))).toBe(true);
  });
});
