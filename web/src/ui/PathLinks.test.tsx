import { renderToStaticMarkup } from "react-dom/server";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const api = vi.fn();
vi.mock("../lib/api", () => ({
  api: (...a: unknown[]) => api(...a),
  enc: encodeURIComponent,
  getToken: () => "tok",
  fileDownloadURL: (mid: string, path: string) => `/dl/${mid}?path=${encodeURIComponent(path)}`,
  ApiError: class extends Error {},
}));

import { askPaths, pathStat, resetPathPresence } from "../lib/path-presence";
import { Markdown } from "./Markdown";
import { getPanel, closePanel } from "../lib/panel";
import { openPathRef, PathText, textPaths, writtenText } from "./PathLinks";

// lib/path-presence.ts batches what is shown together for WAIT (60 ms) before it asks; the fake timers run that out at
// once, then the faked api's answer only needs the microtasks after it.
const PRESENCE_WAIT_MS = 60;
const settle = async () => {
  await vi.advanceTimersByTimeAsync(PRESENCE_WAIT_MS);
  for (let i = 0; i < 4; i++) await Promise.resolve();
};

// The machine has these (relative paths in /repo).
const there: Record<string, string> = {
  "/tmp/persona-tasks/1664-lark-test.md": "file",
  "src/a.ts": "file",
  "~/notes/": "dir",
  "/home/me/report/": "dir",
  "/home/me/report/报表_2026H2.xlsx": "file",
  "~/ドキュメント/メモ.txt": "file",
  "/tmp/report": "file",
  "/tmp/report.": "file",
  "~/notes/result..": "file",
  "/tmp/missing": "file",
};

beforeEach(() => {
  vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
  resetPathPresence();
  api.mockReset();
  api.mockImplementation(async (url: string) => {
    const q = new URL(url, "http://x").searchParams;
    return { stats: q.get("paths")!.split("\n").map((p) => ({ path: p, kind: there[p] ?? "missing", size: 10 })) };
  });
});
afterEach(() => {
  resetPathPresence();
  vi.useRealTimers();
});

describe("path presence", () => {
  it("asks once per machine and directory for the paths shown together, and remembers", async () => {
    askPaths("mac", "/repo", ["src/a.ts", "/nope/x.md"]);
    askPaths("mac", "/repo", ["src/a.ts", "~/notes/"]);
    await settle();
    expect(api).toHaveBeenCalledTimes(1);
    expect(api.mock.calls[0][0]).toBe(`/machines/mac/stat?paths=${encodeURIComponent("src/a.ts\n/nope/x.md\n~/notes/")}&cwd=%2Frepo`);
    expect(pathStat("mac", "/repo", "src/a.ts")?.kind).toBe("file");
    expect(pathStat("mac", "/repo", "/nope/x.md")?.kind).toBe("missing");
    askPaths("mac", "/repo", ["src/a.ts"]);
    await settle();
    expect(api).toHaveBeenCalledTimes(1);
  });

  it("knows nothing when the machine does not answer (plain text, asked again later)", async () => {
    api.mockRejectedValue(new Error("offline"));
    askPaths("mac", "", ["/tmp/a/b.md"]);
    await settle();
    expect(pathStat("mac", "", "/tmp/a/b.md")).toBeUndefined();
  });
});

describe("links in messages", () => {
  it("links exact code filenames with final dots in Markdown and plain message text", async () => {
    const text = "See `/tmp/report.:12` and `~/notes/result..`";
    askPaths("mac", "/repo", textPaths(text));
    await settle();
    expect(textPaths(text)).toEqual(["/tmp/report.", "~/notes/result.."]);
    const html = renderToStaticMarkup(<Markdown text={text} mid="mac" cwd="/repo" />);
    expect(html).toContain('href="/dl/mac?path=%2Ftmp%2Freport."');
    expect(html).toContain("<code>/tmp/report.:12</code></a>");
    expect(html).toContain("<code>~/notes/result..</code></a>");
    const plain = renderToStaticMarkup(<PathText text={text} mid="mac" cwd="/repo" />);
    expect(plain).toContain(">/tmp/report.:12</a>");
    expect(plain).toContain(">~/notes/result..</a>");
  });

  it("does not link a shortened filename when the exact code path is missing", async () => {
    const text = "See `/tmp/missing.`";
    askPaths("mac", "/repo", textPaths(text));
    await settle();
    expect(textPaths(text)).toEqual(["/tmp/missing."]);
    expect(renderToStaticMarkup(<PathText text={text} mid="mac" cwd="/repo" />)).not.toContain("<a ");
    expect(renderToStaticMarkup(<Markdown text={text} mid="mac" cwd="/repo" />)).not.toContain("<a ");
  });

  it("keeps genuine inner-path fallbacks in a multi-path code span", async () => {
    const text = "`src/a.ts 和 ~/notes/`";
    askPaths("mac", "/repo", textPaths(text));
    await settle();
    const html = renderToStaticMarkup(<PathText text={text} mid="mac" cwd="/repo" />);
    expect(html).toContain(">src/a.ts</a>");
    expect(html).toContain(">~/notes/</a>");
    expect(html.match(/<a /g)).toHaveLength(2);
  });

  it("links in a user's message only the paths that are there", async () => {
    const text = "读 /tmp/persona-tasks/1664-lark-test.md 并完整执行这个任务，别动 /tmp/other/x.md";
    askPaths("mac", "/repo", ["/tmp/persona-tasks/1664-lark-test.md", "/tmp/other/x.md"]);
    await settle();
    const html = renderToStaticMarkup(<PathText text={text} mid="mac" cwd="/repo" />);
    expect(html).toContain('class="path-link"');
    expect(html).toContain(">/tmp/persona-tasks/1664-lark-test.md</a>");
    expect(html).toContain("别动 /tmp/other/x.md");
    expect(html.match(/<a /g)).toHaveLength(1);
  });

  it("is plain text until the machine answers, and without a machine", () => {
    expect(renderToStaticMarkup(<PathText text="读 /tmp/a/b.md" mid="mac" />)).toBe("读 /tmp/a/b.md");
    expect(renderToStaticMarkup(<PathText text="读 /tmp/a/b.md" />)).toBe("读 /tmp/a/b.md");
  });

  it("links an answer's paths in text and code spans, with the line, and leaves code blocks alone", async () => {
    const md = "改了 `src/a.ts:12`，见 src/a.ts:40 和 ~/notes/。\n\n```\ncat src/a.ts\n```\n\n[链接](https://example.com/src/a.ts)";
    askPaths("mac", "/repo", ["src/a.ts", "~/notes/"]);
    await settle();
    const html = renderToStaticMarkup(<Markdown text={md} mid="mac" cwd="/repo" />);
    expect(html).toMatch(/<a href="\/dl\/mac\?path=%2Frepo%2Fsrc%2Fa.ts" title="src\/a.ts · 第 12 行"[^>]*><code>src\/a.ts:12<\/code><\/a>/);
    expect(html).toMatch(/<a [^>]*title="src\/a.ts · 第 40 行"[^>]*>src\/a.ts:40<\/a>/);
    expect(html).toMatch(/<a [^>]*>~\/notes\/<\/a>/);
    expect(html).toContain("<pre><code>cat src/a.ts\n</code></pre>");
    expect(html.match(/<a /g)).toHaveLength(4); // the three paths and the link
    // without a machine (a file's own Markdown), nothing is asked or linked
    expect(renderToStaticMarkup(<Markdown text="见 src/a.ts:40" />)).not.toContain("<a ");
  });

  it("links a path whose names are not ASCII whole, and leaves the words glued to it out", async () => {
    const md = "已保存到 /home/me/report/报表_2026H2.xlsx，摘要在 ~/ドキュメント/メモ.txtにあります。";
    askPaths("mac", "/repo", textPaths(md));
    await settle();
    const html = renderToStaticMarkup(<Markdown text={md} mid="mac" cwd="/repo" />);
    expect(html).toMatch(/<a [^>]*>\/home\/me\/report\/报表_2026H2.xlsx<\/a>，/);
    expect(html).toMatch(/<a [^>]*>~\/ドキュメント\/メモ.txt<\/a>にあります。/);
    expect(html.match(/<a /g)).toHaveLength(2);
    const bubble = renderToStaticMarkup(<PathText text={md} mid="mac" cwd="/repo" />);
    expect(bubble).toContain(">/home/me/report/报表_2026H2.xlsx</a>，");
    expect(bubble.match(/<a /g)).toHaveLength(2);
  });

  it("while streaming, leaves out the word still being written", () => {
    expect(textPaths(writtenText("写到 /tmp/persona-tasks/1664-lark-test.md 了，再看 /tmp/jar"))).toEqual(["/tmp/persona-tasks/1664-lark-test.md"]);
    expect(writtenText("/tmp/jar")).toBe("");
    expect(writtenText("a /tmp/x/y.md\n")).toBe("a /tmp/x/y.md");
  });

  it("opens a path in the file panel at its line; a modified click is the browser's", () => {
    const click = (over: Partial<React.MouseEvent> = {}) => ({ button: 0, defaultPrevented: false, preventDefault: vi.fn(), ...over }) as unknown as React.MouseEvent;
    closePanel();
    openPathRef(click({ metaKey: true }), "mac", "/repo", "src/a.ts", 12);
    expect(getPanel()).toBeNull();
    const e = click();
    openPathRef(e, "mac", "/repo", "src/a.ts", 12);
    expect(e.preventDefault).toHaveBeenCalled();
    expect(getPanel()).toEqual({ kind: "file", mid: "mac", path: "/repo/src/a.ts", cwd: "/repo", line: 12 });
    openPathRef(click(), "mac", "/repo", "~/notes/");
    expect(getPanel()).toMatchObject({ path: "~/notes", line: undefined });
    closePanel();
  });
});
