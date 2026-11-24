// The hub-kept read marks of a chat's answers (#13021): loaded once, a mark shown at once and sent behind it, an older hub
// without them answered locally; and the summary published from the same marks.
import { afterEach, describe, expect, it, vi } from "vitest";

type Init = { method?: string; json?: { ids?: string[] } };
const hub = vi.hoisted(() => ({ calls: [] as { path: string; init?: Init }[], answer: (_path: string, _init?: Init): Promise<unknown> => Promise.resolve({}) }));
vi.mock("./api", async (orig) => {
  const real = await orig<typeof import("./api")>();
  return { ...real, api: (path: string, init?: Init) => { hub.calls.push({ path, init }); return hub.answer(path, init); } };
});

const { ApiError } = await import("./api");
const { loadQuestionReads, markQuestionsRead, publishQuestionMarks, questionReadsOf, resetQuestionReads, QUESTION_READS_BATCH } = await import("./question-reads");
const { rowOpened } = await import("../views/chat/useQuestionMarks");

const flush = async () => { for (let i = 0; i < 5; i++) await new Promise((r) => setTimeout(r, 0)); };
const posts = () => hub.calls.filter((c) => c.init?.method === "POST");

afterEach(() => {
  resetQuestionReads();
  hub.calls = [];
});

describe("question reads", () => {
  it("loads the hub's marks once, with when marks began", async () => {
    hub.answer = () => Promise.resolve({ since: "2026-11-01T09:00:00Z", ids: ["u1"] });
    await loadQuestionReads("mac", "main");
    await loadQuestionReads("mac", "main");
    expect(hub.calls.map((c) => c.path)).toEqual(["/sessions/mac/main/question-reads"]);
    const r = questionReadsOf("mac", "main");
    expect([...r.ids]).toEqual(["u1"]);
    expect(r.since).toBe(Date.parse("2026-11-01T09:00:00Z"));
  });

  it("shows a mark at once and sends it; a refused send goes again with the next mark", async () => {
    hub.answer = (_p, init) => (init?.method === "POST" ? Promise.reject(new ApiError(503, "offline", {}, true)) : Promise.resolve({ ids: [] }));
    await loadQuestionReads("mac", "main");
    markQuestionsRead("mac", "main", ["u2"]);
    expect(questionReadsOf("mac", "main").ids.has("u2")).toBe(true);
    await flush();
    hub.answer = () => Promise.resolve({ ok: true });
    markQuestionsRead("mac", "main", ["u3"]);
    await flush();
    expect(posts().map((c) => c.init?.json?.ids)).toEqual([["u2"], ["u2", "u3"]]);
    markQuestionsRead("mac", "main", ["u3"]); // already read: nothing to send
    await flush();
    expect(posts()).toHaveLength(2);
  });

  it("marked before the first read: sent once the read is in", async () => {
    hub.answer = (_p, init) => Promise.resolve(init?.method === "POST" ? { ok: true } : { ids: [] });
    markQuestionsRead("mac", "main", ["u4"]);
    expect(posts()).toHaveLength(0);
    await loadQuestionReads("mac", "main");
    await flush();
    expect(posts().map((c) => c.init?.json?.ids)).toEqual([["u4"]]);
  });

  it("sends at most a batch per request", async () => {
    hub.answer = (_p, init) => Promise.resolve(init?.method === "POST" ? { ok: true } : { ids: [] });
    await loadQuestionReads("mac", "main");
    markQuestionsRead("mac", "main", Array.from({ length: QUESTION_READS_BATCH + 5 }, (_, i) => `u${i}`));
    await flush();
    expect(posts().map((c) => c.init?.json?.ids?.length)).toEqual([QUESTION_READS_BATCH, 5]);
  });

  it("an older hub without marks (404) keeps them in this page only", async () => {
    hub.answer = () => Promise.reject(new ApiError(404, "not found", {}, true));
    const before = Date.now();
    await loadQuestionReads("mac", "main");
    markQuestionsRead("mac", "main", ["u5"]);
    await flush();
    expect(posts()).toHaveLength(0);
    const r = questionReadsOf("mac", "main");
    expect(r.ids.has("u5")).toBe(true);
    expect(r.since).toBeGreaterThanOrEqual(before - 1000); // nothing older than this page shows unread
  });

  it("publishes the summary of the chat's marks and clears it", async () => {
    const { useQuestionSummary } = await import("./question-reads");
    expect(typeof useQuestionSummary).toBe("function");
    publishQuestionMarks("mac/main", new Map([
      ["seg:1", { state: "running", key: "u1" }],
      ["seg:2", { state: "unread", key: "u2" }],
      ["seg:3", { state: "read", key: "u3" }],
    ]));
    const { summaryOf } = await import("./question-reads");
    expect(summaryOf("mac/main")).toEqual({ working: 1, unread: 1, total: 3 });
    publishQuestionMarks("mac/main", undefined);
    expect(summaryOf("mac/main")).toEqual({ working: 0, unread: 0, total: 0 });
  });
});

describe("rowOpened", () => {
  const view = { top: 0, bottom: 800 };
  it("needs half of a short exchange on screen", () => {
    expect(rowOpened({ top: 700, bottom: 900 }, view)).toBe(true);
    expect(rowOpened({ top: 750, bottom: 950 }, view)).toBe(false);
    expect(rowOpened({ top: 900, bottom: 1000 }, view)).toBe(false);
  });
  it("needs half the view of an exchange taller than it", () => {
    expect(rowOpened({ top: 300, bottom: 3000 }, view)).toBe(true);
    expect(rowOpened({ top: 500, bottom: 3000 }, view)).toBe(false);
  });
});
