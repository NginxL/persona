import { describe, expect, it } from "vitest";
import { tagFilterEntries } from "./conversation-tag-filters";

describe("conversation tag filter entries (#12018)", () => {
  it("distinguishes automatic, exact custom order, and a deliberate empty set", () => {
    expect(tagFilterEntries(["a", "b"], undefined)).toEqual(["a", "b"]);
    expect(tagFilterEntries(["a", "b"], null)).toEqual(["a", "b"]);
    expect(tagFilterEntries(["a", "b"], ["later", "b"])).toEqual(["later", "b"]);
    expect(tagFilterEntries(["a", "b"], [])).toEqual([]);
  });
});
