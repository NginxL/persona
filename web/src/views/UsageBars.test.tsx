// @vitest-environment jsdom
import { act } from "react";
import { createRoot } from "react-dom/client";
import { afterEach, describe, expect, it, vi } from "vitest";
import { renderToStaticMarkup } from "react-dom/server";
import { initI18n } from "../i18n";
import { onDraftEvent, type DraftEvent } from "../lib/drafts";
import { PINNED_USAGE_ACCOUNTS_KEY, resetPinnedUsageAccounts, timelinePosition, type UsageAccount } from "../lib/usage-windows";
import { UsageBars } from "./UsageBars";
import { compactResetText, providerHue, UsagePopover, usageTier, visibleTimelineTickHours } from "./UsagePopover";
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
  it("shows Antigravity and Codex by default when the hub has reconciled the quota windows", () => {
    const html = renderToStaticMarkup(<UsageBars accounts={[
      account("gemini", 3, 38, { provider: "antigravity", name: "Antigravity · Gemini", machines: ["Computer"], sources: ["External"] }),
      account("claude", 0, 0, { provider: "antigravity", name: "Antigravity · Claude/GPT" }),
      account("pro", 20, 23, { name: "Codex Pro" }),
    ]} now={NOW} />);
    expect(html.match(/role="progressbar"/g)).toHaveLength(2);
    expect(html).toContain("Antigravity · Gemini");
    expect(html).toContain("Codex Pro");
    expect(html).not.toContain("Antigravity · Claude/GPT");
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
    const refused = balanceAccount("ds", "12.34", { balance: undefined, problem: "rejected" });
    expect(renderToStaticMarkup(<UsagePopover accounts={[refused]} now={NOW} />)).toContain("Key");
    expect(renderToStaticMarkup(<UsagePopover accounts={[refused]} now={NOW} />)).not.toContain("上次的余额");
    const refusedKept = balanceAccount("ds", "12.34", { problem: "rejected" });
    expect(renderToStaticMarkup(<UsagePopover accounts={[refusedKept]} now={NOW} />)).toContain("上次的余额");
  });
});

describe("the popover", () => {
  it("puts local and external Antigravity aliases in one provider card", () => {
    const html = renderToStaticMarkup(<UsagePopover accounts={[
      account("local", 3, 38, { provider: "agy", name: "Antigravity" }),
      account("external", 0, 0, { provider: "antigravity", name: "Antigravity · Claude/GPT" }),
    ]} now={NOW} />);
    expect(html.match(/data-usage-provider=/g)).toHaveLength(1);
    expect(html).toContain('data-usage-provider="antigravity" aria-label="Antigravity"');
    expect(html).toContain("Antigravity · Claude/GPT");
  });
  it("lists every window in a single-line grid with tiered colors, 1px pace tick, countdown, and sample age", () => {
    // 5h window: 70% (mid tier -> orange), 2h elapsed = 40% expected; 7d window: 10% (low tier -> green), 5d elapsed (~71%); 30d: 85% (high tier -> red)
    const a = account("a", 70, 10);
    a.windows.push({ label: "30d", window_minutes: 43200, used_percent: 85, resets_at: iso(10 * 24 * H) });
    const host = document.createElement("div");
    host.innerHTML = renderToStaticMarkup(<UsagePopover accounts={[a]} now={NOW} />);
    const html = host.innerHTML;
    expect(html).toContain("12秒钟前");
    expect(html).toContain("5h");
    expect(html).toContain("7d");
    expect(html).toContain("70%");
    expect(html).toContain("13:00 重置");
    expect(html).toContain("2 天 3 小时后重置");

    const row5 = host.querySelector('[data-window-row="5h"]')!;
    const row7 = host.querySelector('[data-window-row="7d"]')!;
    const row30 = host.querySelector('[data-window-row="30d"]')!;
    expect(row5.classList.contains("grid")).toBe(true);
    expect(row5.querySelector("[data-usage-percent]")?.classList.contains("text-orange")).toBe(true);
    expect(row5.querySelector('[role="progressbar"] > div')?.classList.contains("bg-orange")).toBe(true);
    expect(row7.querySelector("[data-usage-percent]")?.classList.contains("text-green")).toBe(true);
    expect(row7.querySelector('[role="progressbar"] > div')?.classList.contains("bg-green")).toBe(true);
    expect(row30.querySelector("[data-usage-percent]")?.classList.contains("text-red")).toBe(true);
    expect(row30.querySelector('[role="progressbar"] > div')?.classList.contains("bg-red")).toBe(true);
    expect(row5.querySelector("[data-usage-percent]")?.classList.contains("whitespace-nowrap")).toBe(true);
    expect(row5.querySelector("[data-usage-percent]")?.classList.contains("tabular-nums")).toBe(true);

    // 1px ink-3 expected pace ticks
    const ticks = [...host.querySelectorAll("[data-pace-tick]")];
    expect(ticks).toHaveLength(3);
    for (const tick of ticks) {
      expect(tick.classList.contains("w-px")).toBe(true);
      expect(tick.classList.contains("bg-ink-3")).toBe(true);
    }

    // Tier boundaries: <50 green, 50-80 orange, >=80 red
    expect([0, 49.9, 50, 79.9, 80, 100].map(usageTier)).toEqual(["low", "low", "mid", "mid", "high", "high"]);
  });
  it("draws no pace hint in the first 5% of a window, keeps focus across pace transitions, and discloses details on tap/keyboard activation", async () => {
    const a = account("a", 90, 90);
    a.windows[0].resets_at = iso(4.9 * H + 0.05 * H); // 5h window just started
    const host = document.createElement("div");
    document.body.appendChild(host);
    const root = createRoot(host);
    try {
      await act(async () => root.render(<UsagePopover accounts={[a]} now={NOW} />));
      expect(host.querySelectorAll("[data-pace-tick]")).toHaveLength(1);
      const row5 = host.querySelector<HTMLButtonElement>('[data-window-row="5h"]')!;
      const row7 = host.querySelector<HTMLButtonElement>('[data-window-row="7d"]')!;
      expect(row5.tagName).toBe("BUTTON");
      expect(row5.getAttribute("title")).not.toContain("用得偏快");
      expect(row7.tagName).toBe("BUTTON");
      expect(row7.className).toContain("[@media(pointer:coarse)]:min-h-11");
      expect(row7.querySelector("[data-window-chevron]")).not.toBeNull();
      expect(row7.getAttribute("aria-expanded")).toBe("false");
      expect(row7.getAttribute("aria-label")).toContain("用得偏快");
      expect(host.querySelector("[data-usage-pace-detail]")).toBeNull();

      row7.focus();
      expect(document.activeElement).toBe(row7);
      // Pace transitions to on-pace: same <button> stays mounted and retains keyboard focus
      const onPace = account("a", 90, 71);
      onPace.windows[0].resets_at = iso(4.9 * H + 0.05 * H);
      await act(async () => root.render(<UsagePopover accounts={[onPace]} now={NOW} />));
      expect(document.activeElement).toBe(row7);

      await act(async () => root.render(<UsagePopover accounts={[a]} now={NOW} />));
      await act(async () => row7.click());
      expect(row7.getAttribute("aria-expanded")).toBe("true");
      const detail = host.querySelector("[data-usage-pace-detail]")!;
      expect(detail.classList.contains("flex-wrap")).toBe(true);
      expect(detail.textContent).toContain("用得偏快");
      expect(detail.textContent).toContain("2 天 3 小时后重置");

      await act(async () => row7.click());
      expect(row7.getAttribute("aria-expanded")).toBe("false");
      expect(host.querySelector("[data-usage-pace-detail]")).toBeNull();
    } finally {
      await act(async () => root.unmount());
      host.remove();
    }
  });
  it("keeps an out-of-date account's numbers and reset with readable neutral percentage, no pace, and no tick (#12411)", () => {
    const host = document.createElement("div");
    host.innerHTML = renderToStaticMarkup(<UsagePopover accounts={[account("a", 70, 10, { stale: true })]} now={NOW} />);
    const html = host.innerHTML;
    expect(html).toContain("已过期");
    expect(html).toContain("70%");
    expect(html).toContain("2 天 3 小时后重置");
    expect(html).not.toContain("用得偏快");
    expect(html).not.toContain("用得偏慢");
    expect(html).not.toContain("data-pace-tick");
    // Stale percentage uses full-opacity neutral text-ink-2 (>=4.5:1) rather than faded green/orange/red
    const staleSection = host.querySelector('[data-usage-stale="true"]')!;
    expect(staleSection.classList.contains("opacity-70")).toBe(false);
    const pct = host.querySelector('[data-window-row="5h"] [data-usage-percent]')!;
    expect(pct.classList.contains("text-ink-2")).toBe(true);
    expect(pct.classList.contains("text-orange")).toBe(false);
    expect(pct.closest(".opacity-70")).toBeNull();
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
  it.each(["agy", "antigravity"])("matches %s usage and visibility for either Antigravity launcher alias", (provider) => {
    const launchers = [
      { name: "native-agy", agent: "agy" },
      { name: "native-antigravity", display_name: "Native quota", agent_type: "antigravity" },
      { name: "relay", agent: "agy", base_url: "https://relay.example.com" },
    ];
    const live = [account("gemini", 3, 38, { provider, name: "Antigravity · Gemini" })];
    expect(unconnectedUsageProviders(launchers, live).map((p) => p.id)).toEqual(["relay"]);
    expect(unconnectedUsageProviders(launchers, [], [provider]).map((p) => p.id)).toEqual(["relay"]);
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
  it("leaves out a provider the owner hid from the bars, as a row too (#13179)", () => {
    const launchers = [{ name: "claude" }, { name: "codex", agent: "codex" }, { name: "agy", agent: "agy" }, { name: "deepseek", display_name: "DeepSeek", agent: "claude", base_url: "https://relay.example.com" }];
    // nothing hidden: the rows are what they were
    expect(unconnectedUsageProviders(launchers, [], []).map((p) => p.id)).toEqual(["claude", "codex", "agy", "deepseek"]);
    // Claude hidden: no "Claude Code" row, but a launcher that runs through Claude Code against another service stays
    expect(unconnectedUsageProviders(launchers, [], ["claude"]).map((p) => p.id)).toEqual(["codex", "agy", "deepseek"]);
    // Codex hidden but not Claude: only Claude's row (and the others) remain
    expect(unconnectedUsageProviders(launchers, [], ["codex"]).map((p) => p.id)).toEqual(["claude", "agy", "deepseek"]);
    // hidden by the launcher's own name, and "antigravity" is agy
    expect(unconnectedUsageProviders(launchers, [], ["deepseek", "agy"]).map((p) => p.id)).toEqual(["claude", "codex"]);
    expect(unconnectedUsageProviders([{ name: "ag", agent: "antigravity" }], [], ["agy"])).toEqual([]);
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

describe("user-selected pinned usage accounts (#12418)", () => {
  afterEach(() => {
    resetPinnedUsageAccounts();
    localStorage.removeItem(PINNED_USAGE_ACCOUNTS_KEY);
  });

  const sampleAccounts = (): UsageAccount[] => [
    account("codex", 85, 30, { provider: "codex", name: "Codex Pro" }),
    account("claude", 60, 40, { provider: "claude", name: "Claude Max" }),
    account("agy", 15, 20, { provider: "agy", name: "Antigravity" }),
    balanceAccount("ds", "12.34"),
  ];
  const sampleUnconnected = [{ id: "kimi", title: "Kimi", agent: "claude" }];

  it("pins and unpins accounts from the popover, persists to localStorage, and resets to automatic", async () => {
    const host = document.createElement("div");
    document.body.appendChild(host);
    const root = createRoot(host);
    try {
      await act(async () => root.render(<UsageBars accounts={sampleAccounts()} unconnected={sampleUnconnected} now={NOW} />));
      const stripBtn = () => host.querySelector<HTMLButtonElement>("[data-usage-bars] > button")!;

      // Default: top 2 window accounts (Codex Pro, Claude Max) + DeepSeek balance + unconnected row
      expect(stripBtn().textContent).toContain("Codex Pro");
      expect(stripBtn().textContent).toContain("Claude Max");
      expect(stripBtn().textContent).not.toContain("Antigravity");
      expect(stripBtn().textContent).toContain("CNY 12.34");
      expect(stripBtn().textContent).toContain("Kimi");

      // Open popover: all 4 accounts listed, none pinned, no reset bar
      await act(async () => stripBtn().click());
      await vi.waitFor(() => expect(host.querySelector("[data-usage-pin=agy]")).not.toBeNull());
      expect(host.querySelector("[data-usage-reset-pins]")).toBeNull();
      expect(host.querySelectorAll("[data-usage-pin]")).toHaveLength(4);
      expect(host.querySelector("[data-usage-pin=agy]")?.getAttribute("aria-pressed")).toBe("false");

      // Pin Antigravity only: collapsed strip shows Antigravity + unconnected row; popover still lists all 4 live + 1 unconnected
      await act(async () => host.querySelector<HTMLButtonElement>("[data-usage-pin=agy]")!.click());
      expect(localStorage.getItem(PINNED_USAGE_ACCOUNTS_KEY)).toBe('["agy"]');
      expect(host.querySelector("[data-usage-pin=agy]")?.getAttribute("aria-pressed")).toBe("true");
      expect(host.querySelector("[data-usage-reset-pins]")).not.toBeNull();
      expect(stripBtn().textContent).toContain("Antigravity");
      expect(stripBtn().textContent).not.toContain("Codex Pro");
      expect(stripBtn().textContent).not.toContain("Claude Max");
      expect(stripBtn().textContent).toContain("Kimi");
      expect(host.querySelectorAll("[data-usage-pin]")).toHaveLength(4);
      expect(host.querySelectorAll("[data-usage-connect]")).toHaveLength(1);

      // Also pin Codex Pro and DeepSeek balance
      await act(async () => host.querySelector<HTMLButtonElement>("[data-usage-pin=codex]")!.click());
      await act(async () => host.querySelector<HTMLButtonElement>("[data-usage-pin=ds]")!.click());
      expect(localStorage.getItem(PINNED_USAGE_ACCOUNTS_KEY)).toBe('["agy","codex","ds"]');
      expect(stripBtn().textContent).toContain("Antigravity");
      expect(stripBtn().textContent).toContain("Codex Pro");
      expect(stripBtn().textContent).toContain("CNY 12.34");
      expect(stripBtn().textContent).not.toContain("Claude Max");

      // Unpin Antigravity
      await act(async () => host.querySelector<HTMLButtonElement>("[data-usage-pin=agy]")!.click());
      expect(localStorage.getItem(PINNED_USAGE_ACCOUNTS_KEY)).toBe('["codex","ds"]');
      expect(stripBtn().textContent).not.toContain("Antigravity");
      expect(stripBtn().textContent).toContain("Codex Pro");
      expect(stripBtn().textContent).toContain("CNY 12.34");

      // Reset to automatic
      await act(async () => host.querySelector<HTMLButtonElement>("[data-usage-reset-pins]")!.click());
      expect(localStorage.getItem(PINNED_USAGE_ACCOUNTS_KEY)).toBeNull();
      expect(host.querySelector("[data-usage-reset-pins]")).toBeNull();
      expect(stripBtn().textContent).toContain("Codex Pro");
      expect(stripBtn().textContent).toContain("Claude Max");
      expect(stripBtn().textContent).toContain("CNY 12.34");
      expect(stripBtn().textContent).not.toContain("Antigravity");
    } finally {
      await act(async () => root.unmount());
      host.remove();
    }
  });

  it("tolerates corrupt localStorage and syncs pinned accounts across tabs via storage events", async () => {
    localStorage.setItem(PINNED_USAGE_ACCOUNTS_KEY, "{bad-json");
    const corruptHtml = renderToStaticMarkup(<UsageBars accounts={sampleAccounts()} now={NOW} />);
    expect(corruptHtml).toContain("Codex Pro");
    expect(corruptHtml).toContain("Claude Max");
    expect(corruptHtml).not.toContain("Antigravity");

    const host = document.createElement("div");
    document.body.appendChild(host);
    const root = createRoot(host);
    try {
      localStorage.setItem(PINNED_USAGE_ACCOUNTS_KEY, '["agy"]');
      await act(async () => root.render(<UsageBars accounts={sampleAccounts()} now={NOW} />));
      const stripBtn = () => host.querySelector<HTMLButtonElement>("[data-usage-bars] > button")!;
      expect(stripBtn().textContent).toContain("Antigravity");
      expect(stripBtn().textContent).not.toContain("Codex Pro");

      // Simulate another tab changing the pinned selection
      await act(async () => {
        localStorage.setItem(PINNED_USAGE_ACCOUNTS_KEY, '["claude"]');
        window.dispatchEvent(new StorageEvent("storage", { key: PINNED_USAGE_ACCOUNTS_KEY, newValue: '["claude"]' }));
      });
      expect(stripBtn().textContent).toContain("Claude Max");
      expect(stripBtn().textContent).not.toContain("Antigravity");
    } finally {
      await act(async () => root.unmount());
      host.remove();
    }
  });

  it("falls back to automatic ranking when all pinned accounts are offline or expired, and lets the user reset", () => {
    localStorage.setItem(PINNED_USAGE_ACCOUNTS_KEY, '["offline-agy"]');
    const strip = renderToStaticMarkup(<UsageBars accounts={sampleAccounts()} now={NOW} />);
    expect(strip).toContain("Codex Pro");
    expect(strip).toContain("Claude Max");
    expect(strip).toContain("CNY 12.34");
    expect(strip).not.toContain("Antigravity");

    const popover = renderToStaticMarkup(<UsagePopover accounts={sampleAccounts()} now={NOW} />);
    expect(popover).toContain("固定账号暂不可用");
    expect(popover).toContain("恢复默认");
  });

  it.each([
    ["en", "Pin Antigravity to sidebar", "Unpin Antigravity from sidebar", "Reset to automatic"],
    ["zh-Hans", "将 Antigravity 固定到侧栏", "取消将 Antigravity 固定到侧栏", "恢复默认"],
    ["ja", "Antigravity をサイドバーに固定", "Antigravity のサイドバー固定を解除", "自動表示に戻す"],
  ])("localizes pin controls and reset action in %s", async (lang, pinLabel, unpinLabel, resetLabelText) => {
    await initI18n(lang);
    try {
      const live = [account("agy", 20, 30, { provider: "agy", name: "Antigravity" })];
      const unpinned = renderToStaticMarkup(<UsagePopover accounts={live} now={NOW} />);
      expect(unpinned).toContain(`aria-label="${pinLabel}"`);
      expect(unpinned).not.toContain(resetLabelText);

      localStorage.setItem(PINNED_USAGE_ACCOUNTS_KEY, '["agy"]');
      const pinned = renderToStaticMarkup(<UsagePopover accounts={live} now={NOW} />);
      expect(pinned).toContain(`aria-label="${unpinLabel}"`);
      expect(pinned).toContain(resetLabelText);
    } finally {
      localStorage.removeItem(PINNED_USAGE_ACCOUNTS_KEY);
      await initI18n("zh-Hans");
    }
  });
});

describe("provider cards with a reset timeline (#13173)", () => {
  it("draws one card per provider with every account, 5h before 7d, and a timeline under each account", () => {
    const claude = (id: string, name: string, h7: number): UsageAccount => ({
      ...account(id, 10, 20, { provider: "claude", name }),
      windows: [
        { label: "7d", window_minutes: 10080, used_percent: 20, resets_at: iso(h7 * H) },
        { label: "5h", window_minutes: 300, used_percent: 10, resets_at: iso(3 * H) },
      ],
    });
    const html = renderToStaticMarkup(<UsagePopover accounts={[claude("1", "Later", 100), claude("2", "Sooner", 30), account("3", 1, 1)]} now={NOW} />);
    expect(html.match(/data-usage-provider="/g)).toHaveLength(2);
    expect(html).toContain("2 个账号");
    expect(html.indexOf("Sooner")).toBeLessThan(html.indexOf("Later"));
    expect(html.match(/data-usage-timeline/g)).toHaveLength(2); // one chart per card, not per account
    expect(html.match(/data-timeline-row="/g)).toHaveLength(3);
    const first = html.slice(html.indexOf("Sooner"));
    expect(first.indexOf(">5h<")).toBeLessThan(first.indexOf(">7d<"));
    expect(html).toContain("额度重置时间线");
    // the chart's rows follow the quota list's order
    const chart = html.slice(html.indexOf('data-timeline-row="2"'));
    expect(chart.indexOf('data-timeline-row="2"')).toBeLessThan(chart.indexOf('data-timeline-row="1"'));
  });

  it.each([
    ["zh-Hans", "额度（按 7d 重置时间，近的在前）", ["现在", "6h", "12h", "1d", "3d", "5d", "7d"], ["4.4 小时", "4 天 16 小时", "6 天 24 小时"], ["5h 窗口恢复", "7d 窗口恢复", "30d 窗口恢复", "现在", "保护线"], "到此自动暂停调度"],
    ["en", "Quota (by 7d reset, soonest first)", ["Now", "6h", "12h", "1d", "3d", "5d", "7d"], ["4.4h", "4d 16h", "6d 24h"], ["5h window resets", "7d window resets", "30d window resets", "Now", "Protection line"], "Scheduling pauses automatically at this limit"],
    ["ja", "利用枠（7d リセットが早い順）", ["今", "6h", "12h", "1d", "3d", "5d", "7d"], ["4.4時間", "4日16時間", "6日24時間"], ["5h 枠リセット", "7d 枠リセット", "30d 枠リセット", "今", "保護ライン"], "この上限に達すると自動で一時停止します"],
  ])("renders card header dot, sort subtitle, short-unit ticks and dynamic legend in %s", async (lang, subtitle, ticks, countdowns, legendLabels, protectTitle) => {
    await initI18n(lang);
    try {
      const a = account("a", 10, 20, { provider: "claude", name: "Codex Pro team account", plan: "Team" });
      a.windows[0].resets_at = iso(4.4 * H);
      a.windows[1].resets_at = iso(112 * H);
      a.windows.push({ label: "30d", window_minutes: 43200, used_percent: 10, resets_at: iso(167.75 * H) });
      const host = document.createElement("div");
      host.innerHTML = renderToStaticMarkup(<UsagePopover accounts={[a]} now={NOW} />);

      const card = host.querySelector('[data-usage-provider="claude"]')!;
      expect(card.classList.contains("@container")).toBe(true);
      const dot = card.querySelector<HTMLElement>('[data-provider-dot="claude"]')!;
      expect(dot.style.background).toBe("var(--hue-orange)");
      expect(providerHue("claude")).toBe("var(--hue-orange)");
      expect(providerHue("codex")).toBe("var(--hue-green)");
      expect(providerHue("agy")).toBe("var(--hue-blue)");
      expect(providerHue("unknown-provider")).toBe("var(--hue-gray)");
      expect(card.querySelector("[data-usage-sort-subtitle]")?.textContent).toBe(subtitle);

      const axis = host.querySelector("[data-timeline-axis]")!;
      const tickEls = [...axis.querySelectorAll("[data-timeline-tick]")];
      expect(tickEls.map((tick) => tick.textContent)).toEqual(ticks);
      expect(axis.classList.contains("whitespace-nowrap")).toBe(true);
      for (const tick of tickEls) expect(tick.classList.contains("whitespace-nowrap")).toBe(true);

      const row = host.querySelector("[data-timeline-row]")!;
      const name = row.querySelector("[title='Codex Pro team account']")!;
      expect(name.textContent).toBe(a.name);
      expect(name.classList.contains("truncate")).toBe(true);
      expect(name.parentElement!.classList.contains("pr-2")).toBe(true);
      expect(row.getAttribute("aria-label")).toContain(a.name);

      const lanes = [...row.querySelectorAll("[data-timeline-lane]")];
      expect(lanes.map((lane) => lane.getAttribute("data-timeline-lane"))).toEqual(["5h", "7d", "30d"]);
      const labels = lanes.map((lane) => lane.querySelector<HTMLElement>("[data-timeline-remaining]")!);
      expect(labels.map((label) => label.textContent)).toEqual(countdowns);
      const positions = labels.map((label) => parseFloat(label.parentElement!.style.gridTemplateColumns));
      expect(positions).toEqual([4.4, 112, 167.75].map((hours) => timelinePosition(hours * H)));

      const legend = host.querySelector("[data-timeline-legend]")!;
      expect(legend.classList.contains("flex-wrap")).toBe(true);
      expect(legend.classList.contains("gap-x-3")).toBe(true);
      expect(legend.classList.contains("gap-y-1")).toBe(true);
      const items = [...legend.querySelectorAll<HTMLElement>("[data-legend-item]")];
      expect(items.map((el) => el.textContent)).toEqual(legendLabels);
      for (const item of items) expect(item.classList.contains("whitespace-nowrap")).toBe(true);
      const protectItem = legend.querySelector<HTMLElement>('[data-legend-item="protect"]')!;
      expect(protectItem.getAttribute("title")).toBe(protectTitle);
      expect(protectItem.textContent).not.toContain(protectTitle);
    } finally {
      await initI18n("zh-Hans");
    }
  });

  it("progressively hides colliding interior ticks (6h, 5d, 12h, 3d) and rebinds ResizeObserver when the timeline mounts later", async () => {
    expect(visibleTimelineTickHours(null)).toEqual([0, 6, 12, 24, 72, 120, 168]);
    expect(visibleTimelineTickHours(360)).toEqual([0, 6, 12, 24, 72, 120, 168]);
    expect(visibleTimelineTickHours(200)).toEqual([0, 12, 24, 72, 120, 168]);
    // 284px sidebar (~146px axis): Now and 12h must never collide
    expect(visibleTimelineTickHours(146)).toEqual([0, 24, 72, 168]);
    // 240px minimum sidebar (~102px axis): hides 6h, 5d, 12h, 3d with zero overlap
    expect(visibleTimelineTickHours(102)).toEqual([0, 24, 168]);

    let observerCb: ((entries: { contentRect: { width: number } }[]) => void) | undefined;
    let observedCount = 0;
    const origRO = globalThis.ResizeObserver;
    vi.stubGlobal(
      "ResizeObserver",
      class {
        constructor(cb: (entries: { contentRect: { width: number } }[]) => void) {
          observerCb = cb;
        }
        observe() {
          observedCount++;
        }
        disconnect() {}
      },
    );
    const host = document.createElement("div");
    document.body.appendChild(host);
    const root = createRoot(host);
    try {
      // Mount without reset times first (no timeline), then update with reset times while popover stays open
      const noReset = account("a", 10, 20);
      noReset.windows.forEach((w) => delete w.resets_at);
      await act(async () => root.render(<UsagePopover accounts={[noReset]} now={NOW} />));
      expect(host.querySelector("[data-timeline-axis]")).toBeNull();
      expect(observedCount).toBe(0);

      await act(async () => root.render(<UsagePopover accounts={[account("a", 10, 20)]} now={NOW} />));
      expect(observedCount).toBe(1);
      const ticks = () => [...host.querySelectorAll("[data-timeline-tick]")].map((el) => el.textContent);
      expect(ticks()).toEqual(["现在", "6h", "12h", "1d", "3d", "5d", "7d"]);

      await act(async () => observerCb?.([{ contentRect: { width: 200 } }]));
      expect(ticks()).toEqual(["现在", "12h", "1d", "3d", "5d", "7d"]);

      await act(async () => observerCb?.([{ contentRect: { width: 146 } }]));
      expect(ticks()).toEqual(["现在", "1d", "3d", "7d"]);

      await act(async () => observerCb?.([{ contentRect: { width: 102 } }]));
      expect(ticks()).toEqual(["现在", "1d", "7d"]);

      await act(async () => observerCb?.([{ contentRect: { width: 360 } }]));
      expect(ticks()).toEqual(["现在", "6h", "12h", "1d", "3d", "5d", "7d"]);
    } finally {
      await act(async () => root.unmount());
      host.remove();
      if (origRO) vi.stubGlobal("ResizeObserver", origRO);
      else Reflect.deleteProperty(globalThis, "ResizeObserver");
    }
  });

  it("matches timeline legend labels to the actual window lengths present", () => {
    const dayOnly: UsageAccount = {
      id: "d1",
      provider: "claude",
      name: "Daily seat",
      sampled_at: iso(-12_000),
      windows: [{ label: "1d", window_minutes: 1440, used_percent: 30, resets_at: iso(12 * H) }],
    };
    const host = document.createElement("div");
    host.innerHTML = renderToStaticMarkup(<UsagePopover accounts={[dayOnly]} now={NOW} />);
    const items = [...host.querySelectorAll<HTMLElement>("[data-legend-item]")].map((el) => el.textContent);
    expect(items).toEqual(["1d 窗口恢复", "现在", "保护线"]);
  });

  it("adapts reset copy for narrow (<300px) container cards while letting the meter shrink before short countdowns", () => {
    expect(compactResetText(iso(45 * 60_000), NOW)).toBe("45m");
    expect(compactResetText(iso(1.5 * H), NOW)).toBe("1.5h");
    expect(compactResetText(iso(50 * H), NOW)).toBe("2d 2h");
    expect(compactResetText(iso(48 * H), NOW)).toBe("2d");
    expect(compactResetText(iso(47.8 * H), NOW)).toBe("2d");
    expect(compactResetText(iso(-1000), NOW)).toBe("即将重置");

    const a = account("a", 12, 72);
    a.windows[0].resets_at = iso(1.5 * H);
    const host = document.createElement("div");
    host.innerHTML = renderToStaticMarkup(<UsagePopover accounts={[a]} now={NOW} />);
    const row = host.querySelector('[data-window-row="5h"]')!;
    expect(row.className).toContain("grid-cols-[1.75rem_minmax(2.5rem,1fr)_auto_minmax(0,auto)_auto]");
    const full = row.querySelector("[data-reset-full]")!;
    const short = row.querySelector("[data-reset-short]")!;
    expect(full.className).toContain("hidden");
    expect(full.className).toContain("@[300px]:inline");
    expect(short.className).toContain("@[300px]:hidden");
    expect(short.textContent).toBe("1.5h");
  });

  it("draws no timeline for a card whose accounts have no reset time", () => {
    const a = account("a", 1, 1);
    a.windows.forEach((w) => delete w.resets_at);
    expect(renderToStaticMarkup(<UsagePopover accounts={[a]} now={NOW} />)).not.toContain("data-usage-timeline");
  });
  it("draws a protection line, flag and zone and reserves mt-3.5 only on windows with protect_percent", () => {
    const a = account("a", 10, 20);
    a.windows[1].protect_percent = 95;
    const host = document.createElement("div");
    host.innerHTML = renderToStaticMarkup(<UsagePopover accounts={[a]} now={NOW} />);
    const html = host.innerHTML;
    expect(html.match(/data-protect-line/g)).toHaveLength(1);
    expect(html).toContain("保护 95%");
    expect(html).toContain("data-protect-zone");
    expect(html).toContain("保护线在 95%");
    expect(host.querySelector('[data-window-row="5h"]')?.classList.contains("mt-3.5")).toBe(false);
    expect(host.querySelector('[data-window-row="7d"]')?.classList.contains("mt-3.5")).toBe(true);
    expect(renderToStaticMarkup(<UsagePopover accounts={[account("b", 10, 20)]} now={NOW} />)).not.toContain("data-protect");
  });
  it("renders usage bars and popover typography with named text tokens instead of hard-coded pixel classes (#13211)", () => {
    const a = account("a", 40, 65, {
      provider: "claude",
      name: "Work Claude",
      plan: "Max",
      sources: ["Relay"],
      problem: "unreachable",
    });
    a.windows[1].protect_percent = 90;
    a.windows.push({ label: "7d", window_minutes: 10080, used_percent: 50, resets_at: iso(30 * H), model: "Sonnet" });
    const stripHtml = renderToStaticMarkup(<UsageBars accounts={[a, balanceAccount("ds", "12.34")]} now={NOW} />);
    const popoverHtml = renderToStaticMarkup(<UsagePopover accounts={[a, balanceAccount("ds", "12.34")]} now={NOW} />);
    const arbitraryPxFont = /text-\[(?:length:)?\d+(?:\.\d+)?px\]/;
    expect(stripHtml).not.toMatch(arbitraryPxFont);
    expect(popoverHtml).not.toMatch(arbitraryPxFont);
    expect(stripHtml).toContain("text-submeta");
    expect(popoverHtml).toContain("text-meta");
    expect(popoverHtml).toContain("text-label");
    expect(popoverHtml).toContain("text-submeta");
  });
  it("keeps and renders extension accounts that carry notice or headline with or without windows", () => {
    const noticeOnly: UsageAccount = {
      id: "cool-1",
      provider: "claude",
      name: "Cooldown seat",
      plan: "Max",
      sampled_at: iso(-12_000),
      windows: [],
      notice: "Cooldown · resumes in 30m",
    };
    const stripHtml = renderToStaticMarkup(<UsageBars accounts={[noticeOnly]} now={NOW} />);
    expect(stripHtml).toContain('data-usage-notice-strip="cool-1"');
    expect(stripHtml).toContain("Cooldown seat");
    expect(stripHtml).toContain("Cooldown · resumes in 30m");
    expect(stripHtml).not.toContain('role="progressbar"');

    const popOnly = renderToStaticMarkup(<UsagePopover accounts={[noticeOnly]} now={NOW} />);
    expect(popOnly).toContain("data-usage-notice");
    expect(popOnly).toContain("Cooldown · resumes in 30m");
    expect(popOnly).not.toContain("data-usage-sort-subtitle");
    expect(popOnly).not.toContain("data-usage-timeline");
    expect(popOnly).not.toContain("data-usage-pin=");

    // If a previously pinned ID becomes notice-only, pinned summary reflects fallback and unpinning stays available
    localStorage.setItem(PINNED_USAGE_ACCOUNTS_KEY, '["cool-1"]');
    try {
      const host = document.createElement("div");
      host.innerHTML = renderToStaticMarkup(<UsagePopover accounts={[noticeOnly]} now={NOW} />);
      expect(host.textContent).toContain("固定账号暂不可用");
      const unpinBtn = host.querySelector('[data-usage-pin="cool-1"]');
      expect(unpinBtn).not.toBeNull();
      expect(unpinBtn?.getAttribute("aria-pressed")).toBe("true");
    } finally {
      resetPinnedUsageAccounts();
      localStorage.removeItem(PINNED_USAGE_ACCOUNTS_KEY);
    }

    const withWindows = account("cool-2", 62, 84, {
      provider: "claude",
      name: "Restricted seat",
      headline: "Available 4/5",
      notice: "Rate limited · resumes in 15m",
    });
    const popBoth = renderToStaticMarkup(<UsagePopover accounts={[withWindows]} now={NOW} />);
    expect(popBoth).toContain("data-usage-headline");
    expect(popBoth).toContain("Available 4/5");
    expect(popBoth).toContain("data-usage-notice");
    expect(popBoth).toContain("Rate limited · resumes in 15m");
    expect(popBoth).toContain("data-usage-sort-subtitle");
    expect(popBoth).toContain("data-window-row=\"5h\"");
  });
});
