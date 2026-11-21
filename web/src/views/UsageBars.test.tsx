// @vitest-environment jsdom
import { act } from "react";
import { createRoot } from "react-dom/client";
import { describe, expect, it, vi } from "vitest";
import { renderToStaticMarkup } from "react-dom/server";
import { initI18n } from "../i18n";
import { onDraftEvent, type DraftEvent } from "../lib/drafts";
import { type UsageAccount } from "../lib/usage-windows";
import { UsageBars } from "./UsageBars";
import { UsagePopover } from "./UsagePopover";
import { unconnectedUsageProviders } from "./UsageUnconnected";

const NOW = new Date(2026, 10, 5, 10, 0, 0).getTime();
const H = 3600_000;
const iso = (ms: number) => new Date(NOW + ms).toISOString();
const balanceAccount = (id: string, amount: string, o: Partial<UsageAccount> = {}): UsageAccount => ({
  id,
  provider: "deepseek",
  name: "DeepSeek",
  sampled_at: iso(-12_000),
  windows: [],
  balance: { amount, currency: "CNY" },
  ...o,
});
const account = (id: string, used5: number, used7: number, o: Partial<UsageAccount> = {}): UsageAccount => ({
  id,
  provider: "codex",
  name: `账号 ${id}`,
  sampled_at: iso(-12_000),
  windows: [
    { label: "5h", window_minutes: 300, used_percent: used5, resets_at: iso(3 * H) },
    { label: "7d", window_minutes: 10080, used_percent: used7, resets_at: iso(2 * 24 * H + 3 * H) },
  ],
  ...o,
});

describe("the sidebar's usage strip", () => {
  it("is not drawn without data", () => {
    expect(renderToStaticMarkup(<UsageBars accounts={[]} now={NOW} />)).toBe("");
    // all samples older than three days
    expect(renderToStaticMarkup(<UsageBars accounts={[account("a", 10, 10, { sampled_at: iso(-80 * H) })]} now={NOW} />)).toBe("");
  });
  it("shows the two fullest accounts as bars, the caution colour from 80%", () => {
    const html = renderToStaticMarkup(<UsageBars accounts={[account("a", 10, 20), account("b", 85, 30), account("c", 50, 60)]} now={NOW} />);
    expect(html.match(/role="progressbar"/g)).toHaveLength(2);
    expect(html).toContain("账号 b");
    expect(html).toContain("账号 c");
    expect(html).not.toContain("账号 a");
    expect(html).toMatch(/bg-orange[^>]*style="width:85%/);
    expect(html).toMatch(/bg-accent[^>]*style="width:60%/);
    expect(html).toContain('aria-expanded="false"');
  });
  it("opens its details on intent, including the gateway's retained-number warning", async () => {
    const host = document.createElement("div");
    document.body.appendChild(host);
    const root = createRoot(host);
    try {
      await act(async () => root.render(<UsageBars accounts={[account("a", 10, 20, { problem: "unreachable" })]} now={NOW} />));
      expect(host.querySelector("[role=dialog]")).toBeNull();
      await act(async () => host.querySelector<HTMLButtonElement>("[data-usage-bars] > button")!.click());
      await vi.waitFor(() => expect(host.querySelector("[role=dialog]")?.textContent).toContain("5h"));
      const dialog = host.querySelector("[role=dialog]");
      expect(dialog?.textContent).toContain("网关暂时读不到，显示的是上次的数字");
      await act(async () => host.querySelector<HTMLButtonElement>("[data-usage-bars] > button")!.click());
      expect(host.querySelector("[role=dialog]")).toBeNull();
    } finally {
      await act(async () => root.unmount());
      host.remove();
    }
  });
  it("marks a stale account", () => {
    expect(renderToStaticMarkup(<UsageBars accounts={[account("a", 10, 20, { stale: true })]} now={NOW} />)).toContain("数据已过期");
  });
  it("marks a kept account stale once its stale_at has passed, though the hub said it was fresh (#1905)", () => {
    const kept = account("a", 10, 20, { stale_at: iso(-60_000) });
    expect(renderToStaticMarkup(<UsageBars accounts={[kept]} now={NOW} />)).toContain("数据已过期");
    expect(renderToStaticMarkup(<UsageBars accounts={[{ ...kept, stale_at: iso(60_000) }]} now={NOW} />)).not.toContain("数据已过期");
  });
});

describe("a prepaid balance in the sidebar (#11825)", () => {
  it("shows the amount and currency beside the two fullest window accounts", () => {
    const html = renderToStaticMarkup(<UsageBars accounts={[balanceAccount("ds", "12.34"), account("a", 10, 20), account("b", 85, 30), account("c", 50, 60)]} now={NOW} />);
    expect(html).toContain("CNY 12.34");
    expect(html.match(/role="progressbar"/g)).toHaveLength(2); // the balance takes no bar
    expect(html).toContain("账号 b");
    expect(html).toContain("账号 c");
    expect(html).not.toContain("账号 a");
  });
  it("shows a genuine zero as an amount, not as no data", () => {
    const html = renderToStaticMarkup(<UsageBars accounts={[balanceAccount("ds", "0.00")]} now={NOW} />);
    expect(html).toContain("CNY 0.00");
    expect(html).toMatch(/text-orange[^>]*>CNY 0\.00/);
    expect(html).not.toContain("余额无法读取");
  });
  it("shows a failed read as no amount and keeps healthy window accounts", () => {
    const failed = balanceAccount("ds", "12.34", { balance: undefined, problem: "unreachable", stale: true });
    const html = renderToStaticMarkup(<UsageBars accounts={[failed, account("a", 10, 20)]} now={NOW} />);
    expect(html).toContain(">—<");
    expect(html).toContain("账号 a");
    expect(html.match(/role="progressbar"/g)).toHaveLength(1);
    expect(html).toContain("opacity-60");
    // the words behind it are the popover's (it is not first-screen code)
    expect(renderToStaticMarkup(<UsagePopover accounts={[failed]} now={NOW} />)).toContain("余额无法读取");
  });
  it("shows the amount, the sample age and both kinds of account in the popover", () => {
    const html = renderToStaticMarkup(<UsagePopover accounts={[balanceAccount("ds", "12.34"), account("a", 70, 10)]} now={NOW} />);
    expect(html).toContain("预付余额");
    expect(html).toContain("CNY 12.34");
    expect(html).toContain("5h");
    expect(html).toContain("70%");
    expect(html.match(/role="progressbar"/g)).toHaveLength(2); // only the window account's bars
  });
  it("says in the popover why a balance is unavailable or is the last one", () => {
    const never = balanceAccount("ds", "12.34", { balance: undefined, problem: "unreachable" });
    expect(renderToStaticMarkup(<UsagePopover accounts={[never]} now={NOW} />)).toContain("暂时连不上 DeepSeek");
    const kept = balanceAccount("ds", "12.34", { problem: "unreachable" });
    expect(renderToStaticMarkup(<UsagePopover accounts={[kept]} now={NOW} />)).toContain("上次的余额");
    const refused = balanceAccount("ds", "12.34", { problem: "rejected" });
    expect(renderToStaticMarkup(<UsagePopover accounts={[refused]} now={NOW} />)).toContain("Key");
  });
});

describe("the popover", () => {
  it("lists every window with percent, countdown, sample age and the pace", () => {
    // 5h window, 2h elapsed = 40% expected: 70% is over; 7d window, 5 days elapsed (~71%): 10% is under
    const html = renderToStaticMarkup(<UsagePopover accounts={[account("a", 70, 10)]} now={NOW} />);
    expect(html).toContain("12秒钟前");
    expect(html).toContain("5h");
    expect(html).toContain("7d");
    expect(html).toContain("70%");
    expect(html).toContain("13:00 重置");
    expect(html).toContain("2 天 3 小时后重置");
    expect(html).toContain("用得偏快");
    expect(html).toContain("用得偏慢");
    expect(html.match(/data-pace-tick/g)).toHaveLength(2);
  });
  it("draws no pace hint in the first 5% of a window, nor a tick", () => {
    const a = account("a", 90, 90);
    a.windows[0].resets_at = iso(4.9 * H + 0.05 * H); // 5h window just started
    const html = renderToStaticMarkup(<UsagePopover accounts={[a]} now={NOW} />);
    expect(html.match(/data-pace-tick/g)).toHaveLength(1);
    expect(html.match(/用得偏快/g)).toHaveLength(1);
  });
  it("keeps an out-of-date account's numbers and reset but gives no pace, nor a tick (#12411)", () => {
    const html = renderToStaticMarkup(<UsagePopover accounts={[account("a", 70, 10, { stale: true })]} now={NOW} />);
    expect(html).toContain("已过期");
    expect(html).toContain("70%");
    expect(html).toContain("2 天 3 小时后重置");
    expect(html).not.toContain("用得偏快");
    expect(html).not.toContain("用得偏慢");
    expect(html).not.toContain("data-pace-tick");
  });
  it("is a labelled dialog", () => {
    expect(renderToStaticMarkup(<UsagePopover accounts={[account("a", 1, 1)]} now={NOW} />)).toContain('role="dialog" aria-label="订阅额度"');
  });
});

describe("where the numbers came from (#2595)", () => {
  const mixed = (): UsageAccount[] => [
    account("a", 20, 30, { name: "work-claude", plan: "Max", local: true, machines: ["MacBook"], sources: ["Relay"] }),
    account("b", 5, 10, { name: "team-seat", plan: "Plus", sources: ["Relay", "Team pool"] }),
    account("c", 40, 50, { name: "Codex Pro", plan: "pro", local: true }),
    account("d", 1, 2, { name: "team-seat-3", sources: ["Relay"], stale: true, problem: "unreachable" }),
  ];
  it("names the sources of each account in the popover, the computers first", () => {
    const html = renderToStaticMarkup(<UsagePopover accounts={mixed()} now={NOW} />);
    expect(html).toContain("MacBook · Relay");
    expect(html).toContain(">电脑<"); // an account an older hub only marks local
    expect(html).toContain("Relay · Team pool");
    expect(html.match(/data-usage-source/g)).toHaveLength(4); // every account has a source line (the plan or the sources)
  });
  it("writes the plan beside the name unless the name already says it", () => {
    const html = renderToStaticMarkup(<UsagePopover accounts={mixed()} now={NOW} />);
    expect(html).toContain(">Max<");
    expect(html).toContain(">Plus<");
    expect(html).not.toContain(">pro<"); // "Codex Pro" says it already
  });
  it("says when the gateway could not be read and these are its last numbers", () => {
    const html = renderToStaticMarkup(<UsagePopover accounts={mixed()} now={NOW} />);
    expect(html).toContain("网关暂时读不到，显示的是上次的数字");
    expect(html.match(/网关暂时读不到/g)).toHaveLength(1);
  });
  it("says why a gateway's numbers are old: a refused key is not a network problem (#1902)", () => {
    const one = (problem: string) => renderToStaticMarkup(<UsagePopover accounts={[account("r", 1, 2, { name: "seat", sources: ["Relay"], stale: true, problem })]} now={NOW} />);
    expect(one("rejected")).toContain("网关不认这把 Key，请到管理界面 → 用量来源更新");
    expect(one("rejected")).not.toContain("网关暂时读不到");
    expect(one("failed")).toContain("读网关出错，显示的是上次的数字");
    expect(one("error")).toContain("网关读这个账号失败");
    expect(one("something-newer")).not.toContain("text-orange"); // a reason this build doesn't know: no words, still faded
    expect(one("something-newer")).toContain("opacity-70");
  });
  it("labels a per-model window by its model, and keeps it apart from the plan's own", () => {
    const a = account("m", 10, 20, { name: "claude-seat", sources: ["Relay"] });
    a.windows.push({ label: "7d", window_minutes: 10080, used_percent: 100, resets_at: iso(2 * 24 * H + 3 * H), model: "Fable" });
    const html = renderToStaticMarkup(<UsagePopover accounts={[a]} now={NOW} />);
    expect(html).toContain("data-usage-model");
    expect(html).toContain(">Fable<");
    expect(html).toContain("claude-seat 7d（Fable）：100%已用");
    expect(html.match(/role="progressbar"/g)).toHaveLength(3);
  });
  it("puts the sources in the tooltip of a sidebar bar", () => {
    const html = renderToStaticMarkup(<UsageBars accounts={mixed().slice(0, 1)} now={NOW} />);
    expect(html).toContain("来源：MacBook · Relay");
  });
  it.each([
    ["en", "No quota data connected"],
    ["zh-Hans", "未接额度数据"],
    ["ja", "利用量データ未連携"],
  ])("keeps the launcher row and names its missing quota data in %s", async (lang, label) => {
    await initI18n(lang);
    try {
      const html = renderToStaticMarkup(<UsageBars accounts={[]} unconnected={[{ id: "claude", title: "Claude Code", agent: "claude" }]} now={NOW} />);
      expect(html).toContain("Claude Code");
      expect(html).toContain(label);
    } finally {
      await initI18n("zh-Hans");
    }
  });
  it("shows agy quota windows instead of its missing-data row", () => {
    const live = [account("agy", 25, 75, { provider: "agy", name: "Antigravity", local: true })];
    const unconnected = unconnectedUsageProviders([{ name: "agy", agent: "agy" }], live);
    expect(unconnected).toEqual([]);
    const strip = renderToStaticMarkup(<UsageBars accounts={live} unconnected={unconnected} now={NOW} />);
    expect(strip).toContain("Antigravity");
    expect(strip).toContain("75%");
    expect(strip).not.toContain("未接额度数据");
  });
  it("says in plain words whether a provider is not read yet or can only be read through its relay (#12411)", () => {
    const html = renderToStaticMarkup(
      <UsagePopover
        accounts={[]}
        unconnected={[
          { id: "claude", title: "Claude Code", agent: "claude" },
          { id: "agy", title: "Antigravity", agent: "agy" },
          { id: "deepseek", title: "DeepSeek", agent: "claude" },
        ]}
        now={NOW}
      />,
    );
    expect(html).toContain("还没读到 Claude Code 的额度");
    expect(html).toContain("还没读到 Antigravity 的额度");
    // a launcher run through Claude Code against another provider is not Claude's subscription
    expect(html.match(/还没读到 Claude Code/g)).toHaveLength(1);
    expect(html).toContain("不能直接读这家服务商的额度");
    expect(html).not.toMatch(/statusLine|网关/);
  });
  it("lists active providers without usage data and hands setup off to the assistant on click", () => {
    const live = [account("c", 40, 75, { provider: "codex", name: "Codex Pro", local: true })];
    const unconnected = unconnectedUsageProviders(
      [
        { name: "claude" },
        { name: "codex", agent: "codex" },
        { name: "agy", agent: "agy" },
      ],
      live,
    );
    expect(unconnected).toEqual([
      { id: "claude", title: "Claude Code", agent: "claude" },
      { id: "agy", title: "Antigravity", agent: "agy" },
    ]);

    const strip = renderToStaticMarkup(<UsageBars accounts={live} unconnected={unconnected} now={NOW} />);
    expect(strip).toContain("Codex Pro");
    expect(strip).toContain("Claude Code");
    expect(strip).toContain("Antigravity");
    expect(strip).toContain("未接额度数据");

    const host = document.createElement("div");
    document.body.appendChild(host);
    const root = createRoot(host);
    const events: DraftEvent[] = [];
    const off = onDraftEvent("persona-new", (e) => events.push(e));
    let closed = false;
    try {
      act(() => root.render(<UsagePopover accounts={live} unconnected={unconnected} now={NOW} onClose={() => { closed = true; }} />));
      expect(host.textContent).toContain("让助手接入");
      act(() => host.querySelector<HTMLButtonElement>("[data-usage-connect=claude]")!.click());
      expect(closed).toBe(true);
      expect(events).toHaveLength(1);
      expect(events[0]).toMatchObject({ type: "replace" });
      expect((events[0] as { text: string }).text).toContain("Claude Code");
    } finally {
      off();
      act(() => root.unmount());
      host.remove();
    }
  });
});
