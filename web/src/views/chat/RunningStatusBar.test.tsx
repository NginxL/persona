// @vitest-environment jsdom
import { act } from "react";
import { createRoot } from "react-dom/client";
import { renderToStaticMarkup } from "react-dom/server";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { RunStep } from "../../lib/run-step";
import { Icon } from "../../ui/icons";
import { FADE_OUT_MS, LINGER_MS } from "../../ui/motion";
import { RunningStatusView, statusWords, WaitingView } from "./RunningStatusBar";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

afterEach(() => vi.useRealTimers());

const render = (props: Parameters<typeof RunningStatusView>[0]) => renderToStaticMarkup(<RunningStatusView {...props} />);
const statusIcon = (name: "terminal" | "sparkles") => renderToStaticMarkup(<Icon name={name} size={14} className="shrink-0 -translate-y-px" />);
const tool = (name: string, spawning?: boolean): RunStep => ({ kind: "tool", tool: name, ...(spawning ? { spawning } : {}) });
const agent = (step?: RunStep, live: Parameters<typeof statusWords>[0] = null) => statusWords(live, step).text;
const plain = (step?: RunStep, live: Parameters<typeof statusWords>[0] = null) => statusWords(live, step, true).text;

describe("the Agent session's state words (cindy's, #5312)", () => {
  it("says what the turn does: thinking, writing, editing, searching, a subagent, a tool", () => {
    expect(agent()).toBe("正在思考…");
    expect(agent({ kind: "thinking" })).toBe("正在思考…");
    expect(agent({ kind: "streaming" })).toBe("正在生成…");
    expect(agent({ kind: "text" })).toBe("正在生成…");
    expect(agent(tool("Edit"))).toBe("正在编辑文件…");
    expect(agent(tool("Write"))).toBe("正在编辑文件…");
    expect(agent(tool("MultiEdit"))).toBe("正在编辑文件…");
    expect(agent(tool("NotebookEdit"))).toBe("正在编辑文件…");
    expect(agent(tool("WebSearch"))).toBe("正在搜索网页…");
    expect(agent(tool("Agent", true))).toBe("正在启动子 Agent…");
    expect(agent(tool("Agent"))).toBe("Agent 运行中…");
    expect(agent(tool("Bash"))).toBe("Bash 运行中…");
    expect(agent(tool("mcp__github__create_issue"))).toBe("GitHub · Create issue 运行中…");
    expect(agent({ kind: "after" })).toBe("正在工作…");
  });

  it("takes the status line's thinking and compacting over the steps, compacting with its own mark", () => {
    expect(agent(tool("Bash"), { verb: "Frosting", thinking: true })).toBe("正在思考…");
    // Claude's spinner verb is a random word: it says nothing of what happens
    expect(agent(tool("Bash"), { verb: "Frosting" })).toBe("Bash 运行中…");
    expect(statusWords({ verb: "Compacting conversation" }, tool("Bash"))).toEqual({ text: "正在压缩上下文…", compacting: true });
  });
});

describe("the Persona conversation's plain words (cindy's plain phases, super rules 0 and 4)", () => {
  it("says the kind of work in everyday words and never names a tool", () => {
    expect(plain()).toBe("正在思考…");
    expect(plain({ kind: "streaming" })).toBe("正在回复…");
    expect(plain({ kind: "text" })).toBe("正在处理…");
    expect(plain(tool("Read"))).toBe("正在读取文件…");
    expect(plain(tool("Edit"))).toBe("正在更新文件…");
    expect(plain(tool("Grep"))).toBe("正在查找文件…");
    expect(plain(tool("Glob"))).toBe("正在查找文件…");
    expect(plain(tool("WebSearch"))).toBe("正在搜索网页…");
    expect(plain(tool("WebFetch"))).toBe("正在读取网页…");
    expect(plain(tool("Bash"))).toBe("正在处理…");
    expect(plain(tool("mcp__persona__kb_search"))).toBe("正在处理…");
    expect(plain(tool("Agent", true))).toBe("正在处理…");
    expect(plain({ kind: "after" })).toBe("正在处理…");
    expect(statusWords({ verb: "Compacting conversation" }, undefined, true)).toEqual({ text: "正在整理对话…", compacting: true });
  });
});

describe("RunningStatusView", () => {
  it("shows the state, the time in the work summary's format and the tokens, with no speed before one is measured", () => {
    const html = render({ live: { verb: "Frosting", elapsed_s: 975, tokens_down: 22_800 }, working: true, step: tool("Bash") });
    expect(html).toContain("Bash 运行中…");
    expect(html).toContain("16m 15s");
    expect(html).toContain("22.8k tok");
    expect(html).toContain("data-running-tokens");
    expect(html).not.toContain("tok/s");
    expect(html).not.toContain("data-running-speed");
    expect(html).toContain("text-status-accent");
    expect(html).toContain('data-running-status="on"');
  });

  it("reads 0s, 59s, 1m 0s and 1h 0m as cindy's bar does", () => {
    const at = (elapsed_s: number) => render({ live: { verb: "Frosting", elapsed_s }, working: true });
    expect(at(0)).toMatch(/data-running-elapsed[^>]*>0s</);
    expect(at(59)).toMatch(/data-running-elapsed[^>]*>59s</);
    expect(at(60)).toMatch(/data-running-elapsed[^>]*>1m 0s</);
    expect(at(65)).toMatch(/data-running-elapsed[^>]*>1m 5s</);
    expect(at(3600)).toMatch(/data-running-elapsed[^>]*>1 小时 0 分</);
  });

  it("keeps its time while the chat's live work line ticks (cindy shows both; #3534's rule is gone)", () => {
    const html = render({ live: { verb: "Frosting", elapsed_s: 975 }, working: true, clock: false });
    expect(html).toMatch(/data-running-elapsed[^>]*>16m 15s</);
  });

  it("in the Persona conversation: plain words and the time only, no tokens or speed", () => {
    const html = render({ live: { verb: "Frosting", elapsed_s: 65, tokens_down: 22_800 }, working: true, plain: true, step: tool("Read") });
    expect(html).toContain("正在读取文件…");
    expect(html).toContain("1m 5s");
    expect(html).not.toContain("tok");
    expect(html).not.toContain("data-running-speed");
  });

  it("background subagents after the turn: cindy's Activity line, no time, no tokens, no Stop all the node cannot do (#5030)", () => {
    const html = render({ live: { verb: "Frosting", elapsed_s: 975, tokens_down: 22_800 }, working: true, subtasks: true });
    expect(html).toContain("后台子任务运行中，模型用量仍在消耗…");
    expect(html).toContain('data-running-background="subtasks"');
    expect(html).not.toContain("Frosting");
    expect(html).not.toContain("data-running-elapsed");
    expect(html).not.toContain("tok");
    expect(html).not.toContain("全部停止");
  });

  it("a working background command uses its own start, with the label, details and N more, never the turn's metrics", () => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date("2026-11-05T10:04:05Z"));
    const html = render({ live: { verb: "Frosting", elapsed_s: 975, tokens_down: 22_800 }, working: true, startedAt: "2026-11-05T10:00:00Z", command: { what: "Build release", more: 2, since: "2026-11-05T10:01:00Z" } });
    expect(html).toContain('data-running-background="command"');
    expect(html).toContain("后台任务");
    expect(html).toContain("Build release");
    expect(html).toContain('data-running-command-separator="true"> · </span>');
    expect(html).toContain("还有 2 个");
    expect(html).toMatch(/data-running-elapsed[^>]*>3m 5s</);
    expect(html).not.toContain("16m 15s");
    expect(html).not.toContain("4m 5s");
    expect(html).not.toContain("tok");
    expect(html).not.toContain("data-running-speed");
    expect(html).toContain("text-status-accent");
    expect(html).not.toContain("text-status-waiting");
    expect(html).toContain(statusIcon("terminal"));
    expect(html).not.toContain(statusIcon("sparkles"));
  });

  it("a finished background command keeps its terminal line while it lingers, then fades without becoming Done", () => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date("2026-11-05T10:04:05Z"));
    const box = document.createElement("div");
    document.body.append(box);
    const root = createRoot(box);
    const command = { what: "Build release", more: 0, since: "2026-11-05T10:01:00Z" };
    const bar = (working: boolean, done = false) => <RunningStatusView live={working ? { verb: "Frosting", elapsed_s: 975, tokens_down: 22_800 } : null} working={working} done={done} startedAt="2026-11-05T10:00:00Z" command={working ? command : null} />;
    try {
      act(() => root.render(bar(true)));
      act(() => root.render(bar(false, true)));
      expect(box.querySelector('[data-running-status="linger"]')).not.toBeNull();
      expect(box.querySelector('[data-running-background="command"]')).not.toBeNull();
      expect(box.textContent).toContain("后台任务 · Build release");
      expect(box.querySelector("[data-running-done]")).toBeNull();
      expect(box.querySelector(".status-done")).toBeNull();
      expect(box.querySelector("svg")?.getAttribute("stroke-width")).toBe("1.9");
      expect(box.querySelector("svg")?.outerHTML).toBe(statusIcon("terminal"));
      expect(box.innerHTML).not.toContain(statusIcon("sparkles"));
      act(() => { vi.advanceTimersByTime(LINGER_MS); });
      expect(box.querySelector('[data-running-status="fading"]')).not.toBeNull();
      expect(box.textContent).toContain("后台任务 · Build release");
      expect(box.querySelector("[data-running-done]")).toBeNull();
      act(() => { vi.advanceTimersByTime(FADE_OUT_MS); });
      expect(box.querySelector("[data-running-status]")).toBeNull();
    } finally {
      act(() => root.unmount());
      box.remove();
    }
  });

  it("a background command with no listed detail, no start or an invalid start has no turn clock or tokens", () => {
    for (const command of [{ what: "", more: 0 }, { what: "Build release", more: 0, since: "not a timestamp" }, { what: "Build release", more: 0, since: "0001-01-01T00:00:00Z" }]) {
      const html = render({ live: { verb: "Frosting", elapsed_s: 975, tokens_down: 22_800 }, working: true, startedAt: "2026-11-05T10:00:00Z", command });
      expect(html).toContain('data-running-background="command"');
      expect(html).toContain("后台任务");
      expect(html).not.toContain("data-running-elapsed");
      expect(html).not.toContain("tok");
      expect(html).not.toContain("Frosting");
    }
  });

  it("a background workflow says so first, before background subagents", () => {
    const html = render({ live: null, working: true, subtasks: true, background: { run: { task_id: "w", status: "running", running: true, agents_started: 3, agents_done: 1 }, more: 0, guessed: false } });
    expect(html).toContain('data-running-background="workflow"');
    expect(html).not.toContain("后台子任务运行中");
  });

  it("without a line: thinking, counting from when the turn started, no tokens", () => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date("2026-11-05T10:01:05Z"));
    const html = render({ live: null, working: true, startedAt: "2026-11-05T10:00:00Z" });
    expect(html).toContain("正在思考…");
    expect(html).toContain("1m 5s");
    expect(html).not.toContain("tok");
  });

  it("says the hub is out of reach, in front of the last state it knew", () => {
    const html = render({ live: { verb: "Frosting", elapsed_s: 975 }, working: true, offline: true, step: tool("Bash") });
    expect(html).toContain("data-running-offline");
    expect(html).toContain("连接已断开");
    expect(html).toContain("Bash 运行中…");
    expect(html).toContain("16m 15s");
    expect(render({ live: { verb: "Frosting" }, working: true })).not.toContain("data-running-offline");
  });

  it("is not there while nothing works", () => {
    expect(render({ live: null, working: false })).toBe("");
  });
});

describe("the bar of an agent waiting in the background (#6045)", () => {
  it("is still: what it waits on, the time since it began, no breath and no tokens", () => {
    const html = renderToStaticMarkup(<WaitingView wait={{ what: "等 PR #7 的检查跑完", more: 2, since: new Date(Date.now() - 90_000).toISOString() }} />);
    expect(html).toContain('data-running-background="waiting"');
    // the header's title line says the state: the line says what it waits on (#3956)
    expect(html).toContain("等 PR #7 的检查跑完 · 还有 2 个");
    expect(html).not.toContain("后台等待中");
    // a wait without words of its own still says what it is
    expect(renderToStaticMarkup(<WaitingView wait={{ what: "", more: 0 }} />)).toContain("后台等待中");
    expect(html).toMatch(/data-running-elapsed[^>]*>1m 30s</);
    expect(html).not.toContain("tok");
    expect(html).not.toContain("status-breath");
  });
});
