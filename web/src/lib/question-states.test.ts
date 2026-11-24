// State derivation (not started / in progress / unread / read) and follow-up detection for the question rail (#13021).
import { describe, expect, it } from "vitest";
import { followOf, questionMarks, summarizeQuestions, type QuestionReads } from "./question-states";
import { chatLines, type ChatLine, type UserMessage } from "./turns";
import type { Item } from "./types";

const at = (min: number) => new Date(Date.UTC(2026, 10, 1, 9, min)).toISOString();
const u = (seq: number, text: string, min: number, extra: Partial<Item> = {}): Item => ({ id: `u${seq}#0`, seq, role: "user", kind: "text", text, ts: at(min), ...extra });
const a = (seq: number, text: string, min: number): Item => ({ id: `a${seq}#0`, seq, role: "assistant", kind: "text", text, ts: at(min) });
const none: QuestionReads = { ids: new Set(), since: 0 };

const segments = (lines: ChatLine[]) => lines.filter((l): l is Extract<ChatLine, { kind: "segment" }> => l.kind === "segment" && Boolean(l.seg.user));
const states = (lines: ChatLine[], busy?: string, reads = none) => {
  const marks = questionMarks(lines, busy, reads);
  return segments(lines).map((l) => marks.get(l.id)?.state);
};

describe("questionMarks: states", () => {
  const items = [
    u(1, "Plan the Garden app's Q4 launch", 0), a(2, "Here is a launch plan with three milestones.", 5),
    u(3, "Draft the Docs site outline", 10), a(4, "The outline has five sections.", 14),
    u(5, "Check the beta feedback", 20),
  ];

  it("an answered question is unread until opened, read once opened", () => {
    const lines = chatLines(items);
    expect(states(lines)).toEqual(["unread", "unread", "unread"]);
    expect(states(lines, undefined, { ids: new Set(["u1"]), since: 0 })).toEqual(["read", "unread", "unread"]);
  });

  it("the exchange at work is in progress", () => {
    const lines = chatLines(items);
    const last = segments(lines).at(-1)!.id;
    expect(states(lines, last)).toEqual(["unread", "unread", "running"]);
  });

  it("a queued message is not started, and so is anything after the exchange at work", () => {
    const lines = chatLines([...items, u(7, "Then send the summary", 21, { queued: true } as Partial<Item>)]);
    const marks = questionMarks(lines, segments(lines)[2].id, none);
    const list = segments(lines).map((l) => marks.get(l.id)?.state);
    expect(list.slice(0, 3)).toEqual(["unread", "unread", "running"]);
    // a queued message may be drawn in the turn at work or after it: either way it never counts as read or unread
    for (const s of list.slice(3)) expect(s).toBe("pending");
  });

  it("answers that ended before marks began are read", () => {
    const lines = chatLines(items);
    expect(states(lines, undefined, { ids: new Set(), since: Date.parse(at(12)) })).toEqual(["read", "unread", "unread"]);
  });

  it("keys a mark by the question's own message, without its part", () => {
    const marks = questionMarks(chatLines(items), undefined, none);
    expect([...marks.values()].map((m) => m.key)).toEqual(["u1", "u3", "u5"]);
  });

  it("summarizes the same marks: in progress counts not started too", () => {
    const lines = chatLines(items);
    const marks = questionMarks(lines, segments(lines).at(-1)!.id, { ids: new Set(["u1"]), since: 0 });
    expect(summarizeQuestions(marks)).toEqual({ working: 1, unread: 1, total: 3 });
    expect(summarizeQuestions(undefined)).toEqual({ working: 0, unread: 0, total: 0 });
  });
});

describe("questionMarks: follow-ups", () => {
  it("a question quoting an earlier answer follows that question", () => {
    const lines = chatLines([
      u(1, "Plan the Garden app's Q4 launch", 0), a(2, "Here is a launch plan with three milestones.", 5),
      u(3, "Draft the Docs site outline", 10), a(4, "The outline has five sections.", 14),
      u(5, "> a launch plan with three milestones\nCan we make it two?", 30),
    ]);
    const segs = segments(lines);
    const marks = questionMarks(lines, undefined, none);
    expect(marks.get(segs[0].id)?.follows).toBeUndefined();
    expect(marks.get(segs[1].id)?.follows).toBeUndefined();
    expect(marks.get(segs[2].id)?.follows).toBe(segs[0].id);
  });

  it("a follow-up of a follow-up points at the first question of the thread", () => {
    const lines = chatLines([
      u(1, "Plan the Garden app's Q4 launch", 0), a(2, "Here is a launch plan with three milestones.", 5),
      u(3, "> three milestones\nMake it two", 6), a(4, "Two milestones: beta, then release.", 8),
      u(5, "> beta, then release\nWhen is the beta?", 9),
    ]);
    const segs = segments(lines);
    const marks = questionMarks(lines, undefined, none);
    expect(marks.get(segs[1].id)?.follows).toBe(segs[0].id);
    expect(marks.get(segs[2].id)?.follows).toBe(segs[0].id);
  });

  it("a short quote, or one no answer has, makes no follow-up", () => {
    const lines = chatLines([
      u(1, "Plan the Garden app's Q4 launch", 0), a(2, "Here is a launch plan with three milestones.", 5),
      u(3, "> plan\nok", 30), a(4, "Done.", 31),
      u(5, "> something nobody said before\nNew topic", 40),
    ]);
    const marks = questionMarks(lines, undefined, none);
    expect([...marks.values()].map((m) => m.follows)).toEqual([undefined, undefined, undefined]);
  });

  it("a message sent while the question before it was being worked on follows it", () => {
    const msg = (text: string, min: number): UserMessage => ({ id: "x", text, images: [], contexts: [], ts: at(min) });
    const earlier = [{ line: "seg:1", root: "seg:1", answer: "", ended: Date.parse(at(10)), open: false }];
    expect(followOf(msg("Also include the pricing page", 5), earlier)).toBe("seg:1");
    expect(followOf(msg("A different question", 15), earlier)).toBeUndefined();
    expect(followOf(msg("Still working? add this", 15), [{ ...earlier[0], open: true }])).toBe("seg:1");
  });

  it("text picked from an answer as context ties the follow-up too", () => {
    const msg: UserMessage = { id: "x", text: "Why this order?", images: [], contexts: [{ kind: "selection", text: "beta, then release" }], ts: at(30) };
    const earlier = [
      { line: "seg:1", root: "seg:1", answer: "two milestones: beta, then release.", ended: Date.parse(at(8)), open: false },
      { line: "seg:2", root: "seg:2", answer: "the outline has five sections.", ended: Date.parse(at(14)), open: false },
    ];
    expect(followOf(msg, earlier)).toBe("seg:1");
  });
});
