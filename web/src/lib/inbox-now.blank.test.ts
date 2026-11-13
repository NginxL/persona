// The shared cases in spec/fixtures/inbox/now.json cover every composed part; these cover the pieces a client leaves
// out when the manager sent nothing usable, where the three apps must agree.
import { afterEach, describe, expect, it } from "vitest";
import { initI18n } from "../i18n";
import type { InboxItem } from "./inbox";
import { nowText, rowMeta } from "./inbox-now";

const item = (patch: Partial<InboxItem> = {}): InboxItem => ({
  id: "n-1", source: "chat", kind: "group", account: "chat", account_name: "Team chat", title: "A matter",
  preview: "Latest line", count: 1, tier: "action", ...patch,
});

afterEach(async () => {
  await initI18n("zh-Hans");
}, 30_000);

describe("nowText leaves out an empty part", () => {
  it("falls back to the plain waiting words without a who", async () => {
    expect(nowText(item({ now: "waiting", follow: {} }))).toBe("等对方回复");
    expect(nowText(item({ now: "waiting", follow: { who: "" } }))).toBe("等对方回复");
    expect(nowText(item({ now: "waiting", follow: { who: "  " } }))).toBe("等对方回复");
  });

  it("adds no next step without one", async () => {
    expect(nowText(item({ now: "waiting", follow: { who: "张三", next: "" } }))).toBe("等张三");
    expect(nowText(item({ now: "waiting", follow: { who: "张三", next: "  " } }))).toBe("等张三");
    expect(nowText(item({ now: "check_due", follow: {} }))).toBe("到了再看的时间");
    expect(nowText(item({ now: "check_due", follow: { next: "  " } }))).toBe("到了再看的时间");
  });

  it("uses the plain failure without a reason", async () => {
    const failed = (error?: string) => item({ now: "failed", handling: { owner: "manager", state: "failed", error } });
    expect(nowText(failed())).toBe("AI 处理失败");
    expect(nowText(failed(""))).toBe("AI 处理失败");
    expect(nowText(failed("  "))).toBe("AI 处理失败");
    expect(nowText(failed("处理超时了"))).toBe("AI 没处理完：处理超时了");
  });
});

describe("nowText and an absent check_at", () => {
  it("adds no look-again part when there is no time to show", async () => {
    for (const check_at of ["", "not a time", "0001-01-01T00:00:00Z"]) {
      expect(nowText(item({ now: "waiting", follow: { who: "张三", check_at } })), check_at).toBe("等张三");
    }
  });
});

describe("rowMeta leaves out an empty part", () => {
  it("adds no colon without a preview and names a direct chat's person once", () => {
    expect(rowMeta(item({ where: "Launch group", sender: "Alex", preview: "" }))).toBe("Launch group · Alex");
    expect(rowMeta(item({ where: "Alex", sender: "Alex", preview: "" }))).toBe("Alex");
  });

  it("keeps only where without a sender or preview", () => {
    expect(rowMeta(item({ where: "Launch group", sender: "", preview: "" }))).toBe("Launch group");
  });
});
