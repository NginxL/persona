// @vitest-environment jsdom
import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import type { InboxItem } from "../../lib/inbox";
import { EventMark } from "./InboxEvent";

const item = (patch: Partial<InboxItem> = {}): InboxItem => ({
  id: "event-1", source: "chat", kind: "group", account: "chat", account_name: "Team chat", title: "A matter",
  preview: "Latest line", count: 1, tier: "action", ...patch,
});
const mark = (it: InboxItem) => renderToStaticMarkup(<EventMark it={it} />);

describe("EventMark and the manager's now key", () => {
  it("keeps every old word and mark without now, including for an unknown key", () => {
    const old = item({ handling: { owner: "manager", state: "waiting", since: "2026-01-01T09:00:00Z" } });
    expect(mark(old)).toContain("等对方回复");
    expect(mark({ ...old, now: "a_future_state" })).toBe(mark(old));
    expect(mark({ ...old, now: "tomorrow" })).toBe(mark(old));
  });

  for (const [now, state, word] of [
    ["queued", "looking", "排队中，等 AI 看"],
    ["looking", "looking", "AI 正在看"],
    ["agent_working", "agent", "Agent 在做"],
    ["waiting", "waiting", "等对方回复"],
    ["failed", "failed", "AI 处理失败"],
    ["follow_up_ask", "done", "看过 3 次还没动静，还要跟吗？"],
    ["check_due", "done", "到了再看的时间"],
  ] as const) {
    it(`shows ${now} on a row that already has an event mark`, () => {
      expect(mark(item({ now, handling: { owner: "manager", state } }))).toContain(word);
    });
  }

  it("uses the composed waiting words when the key is known", () => {
    const html = mark(item({
      now: "waiting",
      handling: { owner: "manager", state: "waiting", since: "2026-01-01T09:00:00Z" },
      follow: { who: "张三确认日期", next: "问周五行不行" },
    }));
    expect(html).toContain("等张三确认日期：问周五行不行");
    expect(html).not.toContain("等对方回复");
  });

  it("keeps the suggestion itself ahead of its confirmation state", () => {
    const html = mark(item({
      now: "confirm_reply",
      handling: { owner: "manager", state: "done", since: "2026-01-01T09:00:00Z" },
      suggestion: { version: 1, kind: "reply", text: "周五可以。", handback: false },
    }));
    expect(html).toContain("建议回复");
    expect(html).toContain("周五可以。");
    expect(html).not.toContain("等你确认回复");
  });

  it("uses the known reason for a failure and the shared words for a state without handling", () => {
    const failed = mark(item({ now: "failed", handling: { owner: "manager", state: "failed", error: "处理超时了" } }));
    expect(failed).toContain("AI 没处理完：处理超时了");
    expect(mark(item({ now: "check_due", follow: { next: "问周五行不行" } }))).toContain("到了再看的时间：问周五行不行");
    expect(mark(item({ handling: { owner: "manager", state: "done" } }))).toBe("");
  });

  it("uses the shared looking and done words, and leaves their old forms without a key", () => {
    const looking = item({ urgent: true, handling: { owner: "manager", state: "looking" } });
    expect(mark(looking)).toContain("AI 正在准备建议");
    expect(mark({ ...looking, now: "looking" })).toContain("AI 正在看");
    const done = item({ done_at: "2026-01-01T10:00:00Z", done_by: "manager" });
    expect(mark(done)).toContain("助手处理了");
    expect(mark({ ...done, now: "done_manager" })).toContain("助手看完，不用管");
  });
});
