// The recording module's view of its knowledge entries (recording-view.ts), moved from lib/kb.test.ts (#13407).
import { describe, expect, it } from "vitest";
import { t } from "../../i18n";
import {
  formatAt,
  hitView,
  knownZone,
} from "../../lib/kb";
import {
  dayClock,
  dayPart,
  citedPhoto,
  meetingKind,
  minutesKeptUntil,
  recordingPhotos,
  recordingView,
  recordingClock,
  transcriptLineId,
  transcriptTarget,
  durationLabel,
  recapGaps,
  recapGroups,
} from "./recording-view";

describe("recording minutes helpers", () => {
  it("labels minutes search hits", () => {
    expect(hitView({ path: "recordings/example/minutes.md", line: "- Design review" })).toEqual({ source: t("lib-kb.hit_minutes"), text: "Design review", day: null });
  });
  it("uses local clock math across midnight and keeps fractional offsets", () => {
    const start = new Date(2026, 10, 9, 23, 59, 50).toISOString();
    expect(recordingClock(start, 15.5)).toBe("00:00:05");
    expect(recordingClock(start, 3615)).toBe("01:00:05");
    expect(recordingClock("invalid", 65.9)).toBe("1:05");
    expect(recordingClock(undefined, 3723)).toBe("1:02:03");
    expect(transcriptLineId(15.5)).toBe("recording-line-15.5");
    expect(transcriptLineId(15.5, true)).toBe("recording-bookmark-15.5");
  });
  it("targets ordered spoken lines, including gaps, not intervening bookmarks", () => {
    const rec = recordingView("recordings", { bookmarks: [{ at: 12, label: "Review" }, { at: 20.5 }] }, [
      { start: 20.5, end: 25, text: "Second" }, { start: 5, end: 10, text: "First" },
    ])!;
    expect(rec.lines.map((line) => line.seconds)).toEqual([5, 12, 20.5, 20.5]);
    expect(transcriptTarget(rec.lines, 0)).toBe(transcriptLineId(5));
    expect(transcriptTarget(rec.lines, 5)).toBe(transcriptLineId(5));
    expect(transcriptTarget(rec.lines, 18)).toBe(transcriptLineId(5));
    expect(transcriptTarget(rec.lines, 20.5)).toBe(transcriptLineId(20.5));
    expect(transcriptTarget(rec.lines, 100)).toBe(transcriptLineId(20.5));
    expect(transcriptTarget(rec.lines.filter((line) => line.bookmark), 12)).toBeUndefined();
    expect(transcriptTarget([], 0)).toBeUndefined();
  });
});

describe("recordingView", () => {
  it("is only for recordings", () => {
    expect(recordingView("meetings", {}, [])).toBeNull();
  });
  it("puts segments and bookmarks in order at their clock time", () => {
    const start = new Date(2026, 10, 5, 9, 0, 0).toISOString();
    const v = recordingView(
      "recordings",
      { started_at: start, duration: 3733, device: "watch", language: "zh-CN", complete: false, bookmarks: [{ at: 12, label: "灰度" }] },
      [
        { start: 20, end: 25, text: "后一段" },
        { start: 0, end: 9, text: " 第一段 " },
        { start: 30, end: 31, text: "  " },
      ],
    )!;
    expect(v.lines).toEqual([
      { seconds: 0, clock: "09:00:00", text: "第一段" },
      { seconds: 12, clock: "09:00:12", text: "灰度", bookmark: true },
      { seconds: 20, clock: "09:00:20", text: "后一段" },
    ]);
    expect(v.partial).toBe(true);
    expect(v.duration).toBe(durationLabel(3733));
    expect(v.device).not.toBe(recordingView("recordings", { device: "phone" }, [])!.device);
  });
  it("hides Persona's echo, as the phone does (#5905)", () => {
    const v = recordingView("recordings", { complete: true }, [
      { start: 2, end: 9, text: "Who owns the regression test?" },
      { start: 10, end: 17, text: "You kept the cap.", echo: true },
      { start: 10.5, end: 17.8, text: "Bo owns the regression test." },
    ])!;
    expect(v.lines.map((l) => l.text)).toEqual(["Who owns the regression test?", "Bo owns the regression test."]);
  });
  it("falls back to offsets without a start", () => {
    expect(recordingView("recordings", { complete: true }, [{ start: 3723, end: 3730, text: "x" }])!.lines[0].clock).toBe("1:02:03");
  });
  it.each([
    [{}, undefined, undefined],
    [{ attendance: "off" }, undefined, undefined],
    [{ attendance: "observer" }, undefined, undefined],
    [{ attendance: "listener" }, "listener", undefined],
    [{ attendance: "participant", attending_session: { machine_id: "macbook", session_id: "s1" } }, "participant", { machineId: "macbook", sessionId: "s1" }],
    [{ attendance: "participant", attending_session: { machine_id: "macbook", session_id: "" } }, "participant", undefined],
    [{ attendance: "listener", attending_session: null }, "listener", undefined],
  ])("reads the attendance of %j", (meta, attendance, attendingSession) => {
    const v = recordingView("recordings", meta, [])!;
    expect(v.attendance).toBe(attendance);
    expect(v.attendingSession).toEqual(attendingSession);
  });
  it.each([
    [{ trimmed_at: "2026-11-25T02:00:00Z" }, "2026-11-25T02:00:00Z"],
    [{ trimmed_at: "soon" }, undefined],
    [{ trimmed_at: 1 }, undefined],
    [{}, undefined],
  ])("reads trimmed_at of %j", (meta, trimmedAt) => {
    expect(recordingView("recordings", meta, [])!.trimmedAt).toBe(trimmedAt);
  });
});

describe("minutesKeptUntil", () => {
  it("is the start plus the minutes' days, as a date", () => {
    const start = new Date(2026, 10, 5, 9, 0, 0);
    expect(minutesKeptUntil(start.toISOString(), 30)).toBe(minutesKeptUntil(new Date(start.getTime() + 29 * 86_400_000).toISOString(), 1));
    expect(minutesKeptUntil(start.toISOString(), 30)).not.toBe(minutesKeptUntil(start.toISOString(), 29));
    expect(minutesKeptUntil(start.toISOString(), 365)).toContain("2027");
  });
  it.each([[undefined, 30], ["not a time", 30], ["2026-11-05T09:00:00Z", 0], ["2026-11-05T09:00:00Z", 1.5], ["2026-11-05T09:00:00Z", "30"], ["2026-11-05T09:00:00Z", undefined]])(
    "is unknown for %j and %j", (start, days) => {
      expect(minutesKeptUntil(start, days)).toBeUndefined();
    });
});

describe("all-day recordings (#4741)", () => {
  // 08:00 in Tokyo, whatever zone the tests run in.
  const start = "2026-11-11T08:00:00+09:00";
  it("labels recap search hits", () => {
    expect(hitView({ path: "recordings/example/recap.md", line: "- Design review" })).toEqual({ source: t("lib-kb.hit_recap"), text: "Design review", day: null });
  });
  it("reads the clock in the recording's zone when this browser knows it, else locally", () => {
    expect(knownZone("Asia/Tokyo")).toBe(true);
    for (const zone of ["Not/A_Zone", "", 9, undefined]) expect(knownZone(zone)).toBe(false);
    expect(recordingClock(start, 3912, "Asia/Tokyo")).toBe("09:05:12");
    expect(recordingClock(start, 3912, "America/New_York")).toBe("19:05:12");
    expect(recordingClock(start, 57600, "Asia/Tokyo")).toBe("00:00:00");
    expect(recordingClock(start, 3912, "Not/A_Zone")).toBe(recordingClock(start, 3912));
    // Across the end of daylight saving time, and in a zone a quarter hour off the hour.
    const fallBack = "2026-11-01T00:30:00-04:00";
    expect([3600, 7200, 10800].map((s) => recordingClock(fallBack, s, "America/New_York"))).toEqual(["01:30:00", "01:30:00", "02:30:00"]);
    expect(recordingClock("2026-11-11T08:00:00+05:45", 59, "Asia/Kathmandu")).toBe("08:00:59");
    expect(recordingClock(undefined, 3723, "Asia/Tokyo")).toBe("1:02:03");
    expect(dayClock(start, 3912, "Asia/Tokyo")).toBe("09:05");
    expect(dayClock(undefined, 3912, "Asia/Tokyo")).toBe("1:05:12");
    expect(formatAt(start, new Date(2026, 0, 1), "Asia/Tokyo")).toContain("08:00");
    expect(formatAt(start, new Date(2026, 0, 1), "Not/A_Zone")).toBe(formatAt(start, new Date(2026, 0, 1)));
  });
  it.each([
    [14340, "morning"], [14400, "afternoon"], [35940, "afternoon"], [36000, "evening"], [57599, "evening"],
  ])("puts %s s after 08:00 in the %s", (seconds, part) => {
    expect(dayPart(start, seconds, "Asia/Tokyo")).toBe(part);
  });
  it("has no part of the day without a start", () => {
    expect(dayPart(undefined, 0, "Asia/Tokyo")).toBeUndefined();
  });
  it("reads a day recording's day, part and zone, and its transcript on its clock", () => {
    const v = recordingView("recordings", { kind: "day", day: "2026-11-11", part: 2, tz: "Asia/Tokyo", started_at: start }, [{ start: 3912, end: 3920, text: "x" }])!;
    expect(v.allDay).toEqual({ day: "2026-11-11", part: 2 });
    expect(v.tz).toBe("Asia/Tokyo");
    expect(v.lines[0].clock).toBe("09:05:12");
    for (const part of [0, 100, 1.5, "2", undefined]) expect(recordingView("recordings", { kind: "day", day: "2026-11-11", part }, [])!.allDay!.part).toBe(1);
    expect(recordingView("recordings", { kind: "day", tz: "Not/A_Zone" }, [])!.tz).toBeUndefined();
    const other = recordingView("recordings", { kind: "meeting", tz: "Asia/Tokyo", started_at: start }, [{ start: 3912, end: 3920, text: "x" }])!;
    expect(other.allDay).toBeUndefined();
    expect(other.tz).toBeUndefined();
    expect(other.lines[0].clock).toBe(recordingClock(start, 3912));
  });
  it("orders the timeline and cuts it by part of the day", () => {
    const entry = (from: number, title: string) => ({ from, to: from + 600, kind: "outside", title });
    const groups = recapGroups([entry(37000, "c"), entry(3600, "a"), entry(16200, "b"), entry(4000, "a2"), entry(Number.NaN, "x")], start, "Asia/Tokyo");
    expect(groups.map((g) => [g.part, g.entries.map((e) => e.title)])).toEqual([["morning", ["a", "a2"]], ["afternoon", ["b"]], ["evening", ["c"]]]);
    expect(recapGroups([entry(37000, "c"), entry(3600, "a")], undefined).map((g) => [g.part, g.entries.map((e) => e.title)])).toEqual([[undefined, ["a", "c"]]]);
    expect(recapGroups(null, start)).toEqual([]);
  });
  it("names gaps of a minute or longer in time order", () => {
    expect(recapGaps([
      { from: 13000, to: 14000, reason: "paused" },
      { from: 8400, to: 9900, reason: "interrupted" },
      { from: 12000, to: 12059, reason: "lost" },
      { from: 15000, to: 15060, reason: 7 as unknown as string },
    ], start, "Asia/Tokyo")).toEqual([
      { from: "10:20", to: "10:45", reason: "interrupted" },
      { from: "11:36", to: "11:53", reason: "paused" },
      { from: "12:10", to: "12:11", reason: "" },
    ]);
    expect(recapGaps(undefined, start)).toEqual([]);
  });
});

// #3862 phase 3: a meeting's photos (GET …/photos) and the minutes items that cite one.
describe("meeting photos", () => {
  it("reads the list defensively, oldest first, blank words as absent", () => {
    const photos = recordingPhotos({ photos: [
      { n: 2, id: "b", at: 90.5, mime: "image/png", width: 800, height: 600, caption: "  ", text: "Q3 budget", cited: true, forwarded: true, added_at: "2026-11-10T06:02:00Z" },
      { n: 1, id: "a", at: -3, mime: "image/jpeg", width: 1600, height: 1200, caption: " Whiteboard ", note: "A table.", cited: false, forwarded: false, added_at: "2026-11-10T06:01:00Z" },
      { n: 0, id: "zero" }, { n: 1.5, id: "half" }, { n: 3 }, null, "x",
    ] });
    expect(photos.map((p) => p.n)).toEqual([1, 2]);
    expect(photos[0]).toEqual({ n: 1, id: "a", at: 0, mime: "image/jpeg", width: 1600, height: 1200, caption: "Whiteboard", note: "A table.", cited: false, forwarded: false, added_at: "2026-11-10T06:01:00Z" });
    expect(photos[1]).toMatchObject({ text: "Q3 budget", cited: true, forwarded: true });
    expect(photos[1]).not.toHaveProperty("caption");
    for (const v of [undefined, null, [], { photos: "x" }, { photos: null }]) expect(recordingPhotos(v)).toEqual([]);
  });

  it("knows the photo a minutes item cites", () => {
    expect(citedPhoto({ photo: 3 })).toBe(3);
    for (const photo of [undefined, 0, -1, 2.5, "3", null]) expect(citedPhoto({ photo })).toBeUndefined();
  });

  it("takes photos only on a recording whose kind is meeting", () => {
    expect(meetingKind({ kind: " Meeting " })).toBe(true);
    expect(meetingKind({ kind: "note", minutes_asked: true })).toBe(false);
    expect(meetingKind(undefined)).toBe(false);
  });
});
