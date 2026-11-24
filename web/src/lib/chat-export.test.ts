// Exporting a conversation (#7375): the Markdown as spec/fixtures/chat-export/cases.json says it is written, and reading
// the whole conversation from the hub: page by page back to its start, messages read whole, a history rewritten while
// reading started over once, the limit, and Cancel.
import { describe, expect, it } from "vitest";
import fixture from "../../../spec/fixtures/chat-export/cases.json";
import { exportFileName, ExportStopped, fenced, readConversation, toolSummary, transcriptMarkdown, type ExportWords } from "./chat-export";
import type { Item, ItemsPage } from "./types";

const w = fixture.words;
const words: ExportWords = {
  ...w,
  from: (name) => w.from.replace("{name}", name),
  cut: (n) => w.cut.replace("{n}", String(n)),
  partial: (from) => w.partial.replace("{from}", from),
  partialUnknown: "Some messages could not be read.",
};
const utc = (ts: string) => ts.slice(0, 16).replace("T", " ");

describe("spec/fixtures/chat-export/cases.json", () => {
  for (const c of fixture.cases) {
    it(`${c.name}: ${c.why}`, () => {
      expect(transcriptMarkdown(c.items as Item[], fixture.meta, { work: c.work, partial: c.partial, words, time: utc })).toBe(c.markdown);
    });
  }
});

describe("server and local truncation", () => {
  const meta = fixture.meta;
  const body = (item: Item) => transcriptMarkdown([item], meta, { work: true, words, time: utc });

  it.each(["user", "assistant"] as const)("does not guess omitted character counts from a %s message's UTF-8 bytes", (role) => {
    const text = "汉".repeat(8192) + "\n…（已截断，共 27000 字节）";
    const out = body({ id: "m", seq: 1, role, kind: "text", text, full_size: 27000 });
    expect(out).toContain(text);
    expect(out).toContain(words.cutUnknown);
    expect(out).not.toContain("18790 more characters");
  });

  it("uses an unnumbered warning for a server-cut result even when locally clipped too", () => {
    const out = body({ id: "r", seq: 1, role: "user", kind: "tool_result", text: "汉".repeat(4100), full_size: 27000 });
    expect(out).toContain(words.cutUnknown);
    expect(out).not.toContain("more characters not included");
  });

  it("counts local clipping of a complete result in Unicode code points", () => {
    const out = body({ id: "r", seq: 1, role: "user", kind: "tool_result", text: "😀".repeat(4002) });
    expect(out).toContain(words.cut(2));
    expect(out).not.toContain(words.cutUnknown);
  });

  it("reports both incomplete history and a server-cut retained message", async () => {
    const page = async (): Promise<ItemsPage> => ({ items: [{ id: "a", seq: 1, role: "assistant", kind: "text", text: "partial", full_size: 100 }], has_more: true });
    const got = await readConversation({ work: false, page, full: async () => { throw new Error("unavailable"); } });
    const out = transcriptMarkdown(got.items, meta, { work: false, words, partial: got.partial });
    expect(got.partial).toBe(true);
    expect(out).toContain(words.partialUnknown);
    expect(out).toContain(words.cutUnknown);
  });

  it("retains a warning for a server-cut thinking item", () => {
    const out = body({ id: "t", seq: 1, role: "assistant", kind: "thinking", text: "partial thought", full_size: 100 });
    expect(out).toContain(words.cutUnknown);
  });
});

describe("the pieces", () => {
  it("fences text longer than any run of backticks inside it", () => {
    expect(fenced("plain")).toBe("```\nplain\n```");
    expect(fenced("a ``` b\n````", "md")).toBe("`````md\na ``` b\n````\n`````");
    expect(fenced("trailing\n\n\n")).toBe("```\ntrailing\n```");
  });

  it("names a tool call by what it was given, one line, at most 80 characters", () => {
    expect(toolSummary({ tool_name: "Bash", tool_input: { command: "ls\n  -la" } })).toBe("Bash: ls -la");
    expect(toolSummary({ tool_name: "Grep", tool_input: { path: "Sources", pattern: "TODO" } })).toBe("Grep: TODO");
    expect(toolSummary({ tool_name: "Task", tool_input: {} })).toBe("Task");
    expect(toolSummary({ tool_input: { url: "x".repeat(200) } })).toBe(`tool: ${"x".repeat(79)}…`);
  });

  it("names the file after the title and the day, without what file systems refuse", () => {
    const day = new Date(2026, 10, 14, 9);
    expect(exportFileName("Fix: login / logout?", day)).toBe("Fix login logout 2026-11-14.md");
    expect(exportFileName("  ", day)).toBe("conversation 2026-11-14.md");
    expect(exportFileName("..hidden", day)).toBe("hidden 2026-11-14.md");
    expect(exportFileName("长".repeat(100), day)).toBe(`${"长".repeat(80)} 2026-11-14.md`);
  });
});

// A conversation of `n` items on the fake hub, answered newest first in pages of `size`, as GET …/items pages back.
function hub(n: number, size = 3, cut: number[] = []) {
  const items: Item[] = Array.from({ length: n }, (_, i) => ({
    id: `i${i + 1}`,
    seq: i + 1,
    role: i % 2 ? "assistant" : "user",
    kind: "text",
    text: cut.includes(i + 1) ? "cut" : `text ${i + 1}`,
    ...(cut.includes(i + 1) ? { full_size: 100 } : {}),
  }));
  const queries: string[] = [];
  let history = "h1";
  const page = async (q: string): Promise<ItemsPage> => {
    queries.push(q);
    const before = Number(new URLSearchParams(q).get("before_seq") ?? n + 1);
    const older = items.filter((i) => i.seq < before);
    const slice = older.slice(Math.max(0, older.length - size));
    return { items: slice, has_more: older.length > size, history };
  };
  const full = async (id: string) => ({ ...items.find((i) => i.id === id)!, text: `whole ${id}`, full_size: undefined });
  return { items, queries, page, full, rewrite: (h: string) => void (history = h) };
}

describe("reading the whole conversation", () => {
  it("pages back to the start and gives the items oldest first, each message read whole", async () => {
    const h = hub(8, 3, [2, 7]);
    const seen: number[] = [];
    const got = await readConversation({ work: false, page: h.page, full: h.full, progress: (n) => seen.push(n) });
    expect(got.partial).toBe(false);
    expect(got.items.map((i) => i.seq)).toEqual([1, 2, 3, 4, 5, 6, 7, 8]);
    expect(got.items.find((i) => i.seq === 2)).toMatchObject({ text: "whole i2", full_size: undefined });
    expect(got.items.find((i) => i.seq === 7)?.text).toBe("whole i7");
    expect(h.queries).toEqual([
      "turns=10&lazy_images=1&fold_work=1",
      "turns=10&lazy_images=1&fold_work=1&before_seq=6",
      "turns=10&lazy_images=1&fold_work=1&before_seq=3",
    ]);
    expect(seen).toEqual([3, 6, 8]);
  });

  it("requests a full item even when the appended page notice is longer than its missing tail", async () => {
    const text = "x".repeat(24576) + "\n…（已截断，共 24577 字节）";
    const page = async (): Promise<ItemsPage> => ({ items: [{ id: "a", seq: 1, role: "assistant", kind: "text", text, full_size: 24577 }], has_more: false });
    let calls = 0;
    const full = async (): Promise<Item> => { calls++; return { id: "a", seq: 1, role: "assistant", kind: "text", text: "x".repeat(24577) }; };
    const got = await readConversation({ work: false, page, full });
    expect(calls).toBe(1);
    expect(got.items[0].text).toBe("x".repeat(24577));
    expect(got.items[0].full_size).toBeUndefined();
  });

  it("keeps the cut marker if a full response is still marked cut", async () => {
    const page = async (): Promise<ItemsPage> => ({ items: [{ id: "a", seq: 1, role: "assistant", kind: "text", text: "partial", full_size: 100 }], has_more: false });
    const full = async (): Promise<Item> => ({ id: "a", seq: 1, role: "assistant", kind: "text", text: "still partial", full_size: 100 });
    const got = await readConversation({ work: false, page, full });
    expect(got.items[0]).toMatchObject({ text: "still partial", full_size: 100 });
    expect(transcriptMarkdown(got.items, fixture.meta, { work: false, words })).toContain(words.cutUnknown);
  });

  it("reads the pages unfolded with work steps", async () => {
    const h = hub(2);
    await readConversation({ work: true, page: h.page, full: h.full });
    expect(h.queries).toEqual(["turns=10&lazy_images=1"]);
  });

  it("starts over once when the history is rewritten while reading, and says so the second time", async () => {
    const h = hub(9);
    let pages = 0;
    const page = async (q: string) => {
      pages++;
      if (pages === 2) h.rewrite("h2"); // a rewind while the first pass reads its second page
      return h.page(q);
    };
    const got = await readConversation({ work: false, page, full: h.full });
    expect(got.items).toHaveLength(9);
    expect(h.queries.filter((q) => !q.includes("before_seq"))).toHaveLength(2);

    const again = hub(9);
    let n = 0;
    const rewriting = async (q: string) => {
      if (q.includes("before_seq")) again.rewrite(`h${++n + 1}`);
      return again.page(q);
    };
    await expect(readConversation({ work: false, page: rewriting, full: again.full })).rejects.toEqual(new ExportStopped("changed"));
  });

  it("stops at its limit and says the document is partial", async () => {
    const h = hub(30, 5);
    const got = await readConversation({ work: false, page: h.page, full: h.full, maxItems: 12 });
    expect(got.partial).toBe(true);
    expect(got.items.map((i) => i.seq)).toEqual([16, 17, 18, 19, 20, 21, 22, 23, 24, 25, 26, 27, 28, 29, 30]);
  });

  it("stops when cancelled", async () => {
    const h = hub(30, 5);
    let stop = false;
    const page = async (q: string) => {
      stop = true;
      return h.page(q);
    };
    await expect(readConversation({ work: false, page, full: h.full, stopped: () => stop })).rejects.toEqual(new ExportStopped("cancelled"));
  });

  it("keeps a message cut when reading it whole fails, and stops on a page that adds nothing", async () => {
    const h = hub(4, 2, [1]);
    const got = await readConversation({ work: false, page: h.page, full: async () => Promise.reject(new Error("offline")) });
    expect(got.items[0]).toMatchObject({ text: "cut", full_size: 100 });
    const stuck = async (): Promise<ItemsPage> => ({ items: [{ id: "a", seq: 1, role: "user", kind: "text", text: "a" }], has_more: true });
    expect((await readConversation({ work: false, page: stuck, full: h.full })).items).toHaveLength(1);
  });

  it.each(["repeated", "empty", "newer"])("marks a stalled %s older page partial in the document", async (failure) => {
    const initial: Item = { id: "a", seq: 10, role: "user", kind: "text", text: "visible", ts: "2026-11-16T10:00:00Z" };
    let requests = 0;
    const page = async (): Promise<ItemsPage> => {
      requests++;
      if (requests === 1) return { items: [initial], has_more: true };
      const items = failure === "empty" ? [] : failure === "newer" ? [{ ...initial, id: "b", seq: 11 }] : [{ ...initial, text: "wrong" }];
      return { items, has_more: failure !== "empty" };
    };
    const result = await readConversation({ work: false, page, full: async () => initial });
    expect(requests).toBe(2);
    expect(result.partial).toBe(true);
    expect(result.items).toEqual([initial]);
    const md = transcriptMarkdown(result.items, fixture.meta, { work: false, words, time: utc, partial: result.partial });
    expect(md).toContain(words.partial("2026-11-16 10:00"));
  });

  it("keeps valid older rows but warns and ignores a conflicting row in the same page", async () => {
    const original: Item = { id: "a", seq: 10, role: "user", kind: "text", text: "original" };
    const older: Item = { id: "b", seq: 9, role: "assistant", kind: "text", text: "older" };
    let requests = 0;
    const page = async (): Promise<ItemsPage> => ++requests === 1
      ? { items: [original], has_more: true }
      : { items: [older, { ...original, seq: 8, text: "conflict" }], has_more: false };
    const result = await readConversation({ work: false, page, full: async () => original });
    expect(result.partial).toBe(true);
    expect(result.items).toEqual([older, original]);
  });

  it.each([{ items: [] }, { items: [{ id: "a", seq: 10, role: "user", kind: "text", text: "visible" }] }])(
    "warns without a date when a partial first page has no usable timestamp",
    async ({ items }) => {
      const page = async (): Promise<ItemsPage> => ({ items: items as Item[], has_more: true });
      const result = await readConversation({ work: false, page, full: async () => items[0] as Item });
      expect(result.partial).toBe(true);
      expect(transcriptMarkdown(result.items, fixture.meta, { work: false, words, partial: true })).toContain(words.partialUnknown);
    },
  );
});


describe("content keeps its internal newlines", () => {
  const text = '```python\nmessage = """first\n\n\nlast"""\nprint(message)\n```';
  it.each(["user", "assistant"] as const)("preserves the %s message's code and paragraph breaks", (role) => {
    const markdown = transcriptMarkdown([{ id: "text1", seq: 1, role, kind: "text", text }], fixture.meta, { work: false, words, time: utc });
    expect(markdown).toContain(text);
  });

  it("preserves the thinking text's internal newlines when work is included", () => {
    const markdown = transcriptMarkdown([{ id: "thought1", seq: 1, role: "assistant", kind: "thinking", text }], fixture.meta, { work: true, words, time: utc });
    expect(markdown).toContain(text);
  });

  it.each([true, false])("preserves a tool result's blank lines with its call present: %s", (paired) => {
    const result = "first\n\n\nlast";
    const items: Item[] = [
      ...(paired ? [{ id: "call1", seq: 1, role: "assistant" as const, kind: "tool_use" as const, tool_name: "Read", tool_use_id: "tool1", tool_input: { path: "notes.txt" } }] : []),
      { id: "result1", seq: 2, role: "assistant", kind: "tool_result", tool_use_id: "tool1", text: result },
    ];
    const markdown = transcriptMarkdown(items, fixture.meta, { work: true, words, time: utc });
    expect(markdown).toContain(fenced(result));
  });

  it("treats image items with tool_use_id as tool output instead of user messages", () => {
    const items: Item[] = [
      { id: "u1", seq: 1, role: "user", kind: "text", ts: "2026-11-14T10:01:00Z", text: "Capture the page" },
      { id: "c1", seq: 2, role: "assistant", kind: "tool_use", ts: "2026-11-14T10:02:00Z", tool_name: "Screenshot", tool_use_id: "t1", tool_input: { url: "https://example.com" } },
      { id: "r1", seq: 3, role: "user", kind: "tool_result", ts: "2026-11-14T10:03:00Z", tool_use_id: "t1", text: "Captured" },
      { id: "i1", seq: 4, role: "user", kind: "image", ts: "2026-11-14T10:03:00Z", tool_use_id: "t1", image: { mime: "image/png", lazy: true } },
      { id: "i0", seq: 5, role: "user", kind: "image", ts: "2026-11-14T10:04:00Z", tool_use_id: "orphan", image: { mime: "image/png", lazy: true } },
      { id: "a1", seq: 6, role: "assistant", kind: "text", ts: "2026-11-14T10:05:00Z", text: "Looks good." },
    ];

    const withoutWork = transcriptMarkdown(items, fixture.meta, { work: false, words, time: utc });
    expect(withoutWork).not.toContain("_[Image]_");
    expect(withoutWork.match(/^## You/gm)).toHaveLength(1);

    const withWork = transcriptMarkdown(items, fixture.meta, { work: true, words, time: utc });
    expect(withWork.match(/^## You/gm)).toHaveLength(1);
    expect(withWork).toContain("**Result**\n\n```\nCaptured\n```\n\n_[Image]_\n\n</details>");
    expect(withWork).toContain("<details>\n<summary>Result</summary>\n\n**Result**\n\n_[Image]_\n\n</details>");
  });
});
