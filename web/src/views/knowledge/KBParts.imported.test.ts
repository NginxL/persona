import { describe, expect, it } from "vitest";
import type { KBDetail, KBEntry } from "../../lib/kb";
import { importedMeetingOf, importedNotesLabel } from "./KBParts";

const e = (id: string, o: Partial<KBEntry> = {}): KBEntry => ({ kind: "projects", id, name: id, relevance: 0, ...o });

describe("an imported meeting's notes (#8872)", () => {
  it("are named after the app this client knows the connector of, else plainly", () => {
    expect(importedNotesLabel("lark")).toBe("飞书会议纪要");
    expect(importedNotesLabel("larkcli")).toBe("飞书会议纪要");
    expect(importedNotesLabel("calendar-x")).toBe("原会议纪要");
    expect(importedNotesLabel()).toBe("原会议纪要");
  });

  it("links a recording's page to its imported meeting's notes (#8872)", () => {
    const detail = (meta: Record<string, unknown>, links: KBEntry[]): KBDetail => ({ kind: "recordings", id: "r1", path: "", meta, links });
    const link = e("m1", { kind: "meetings", source: "lark", extra: " https://meet.example.com/m1 " });
    expect(importedMeetingOf(detail({ imported_meeting: { kind: "meetings", id: "m1" } }, [link]))).toEqual({ url: "https://meet.example.com/m1", source: "lark" });
    // Nothing to open: no imported meeting, no link for it, or a link without a URL.
    expect(importedMeetingOf(detail({}, [link]))).toBeNull();
    expect(importedMeetingOf(detail({ imported_meeting: { kind: "meetings", id: "m9" } }, [link]))).toBeNull();
    expect(importedMeetingOf(detail({ imported_meeting: { kind: "meetings", id: "m1" } }, [e("m1", { kind: "meetings" })]))).toBeNull();
    expect(importedMeetingOf({ ...detail({ imported_meeting: { kind: "meetings", id: "m1" } }, [link]), kind: "meetings" })).toBeNull();
  });
});
