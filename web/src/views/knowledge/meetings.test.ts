import { describe, expect, it } from "vitest";
import type { KBEntry } from "../../lib/kb";
import { mergeMeetingLists } from "./meetings";

const e = (id: string, o: Partial<KBEntry> = {}): KBEntry => ({ kind: "projects", id, name: id, relevance: 0, ...o });

describe("the meetings list (#8871, #8872)", () => {
  it("merges the imported meetings and the recordings, tags once each", () => {
    const m = { items: [e("m1", { kind: "meetings" })], tags: ["周会", "客户"] };
    const r = { items: [e("r1", { kind: "recordings", recording_kind: "note" })], tags: ["周会", "旅行"] };
    const out = mergeMeetingLists(m, r);
    expect(out.items?.map((x) => x.id)).toEqual(["m1", "r1"]);
    expect(out.tags).toEqual(["周会", "客户", "旅行"]);
    expect(mergeMeetingLists({ items: null }, { items: null, tags: null })).toEqual({ items: [], tags: [] });
  });

  it("shows a meeting both imported and recorded as the recording's one row (#8872)", () => {
    const lark = e("m1", { kind: "meetings", source: "lark", extra: "https://meet.example.com/m1" });
    const m = { items: [lark, e("m2", { kind: "meetings" })] };
    const r = { items: [e("r1", { kind: "recordings", meeting_id: "m1" }), e("r2", { kind: "recordings", meeting_id: "gone" }), e("r3", { kind: "recordings" })] };
    const out = mergeMeetingLists(m, r);
    expect(out.items?.map((x) => x.id)).toEqual(["m2", "r1", "r2", "r3"]);
    expect(out.items?.find((x) => x.id === "r1")?.imported).toBe(lark);
    // A meeting the list doesn't have (another tag, not imported here) leaves the recording as it is.
    expect(out.items?.find((x) => x.id === "r2")?.imported).toBeUndefined();
    // Two recordings of one meeting: both stay, the meeting's row goes once.
    const two = mergeMeetingLists(m, { items: [e("r1", { kind: "recordings", meeting_id: "m1" }), e("r4", { kind: "recordings", meeting_id: "m1" })] });
    expect(two.items?.map((x) => x.id)).toEqual(["m2", "r1", "r4"]);
  });
});
