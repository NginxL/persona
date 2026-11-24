import { describe, expect, it } from "vitest";
import { initI18n } from "../i18n";
import { balanceAccounts, balanceEmpty, balanceText } from "./usage-balance";
import { countdown, fullest, groupByProvider, isBalanceAccount, isPinnableUsageAccount, liveAccounts, parsePinnedUsageAccounts, planNote, providerTimeline, providerTitle, remainingText, resetLabel, sampleAge, selectSidebarUsageAccounts, sortedWindows, sortForCard, sourceNames, timelinePosition, topAccounts, windowKey, windowMinutes, type UsageAccount, type UsageWindow } from "./usage-windows";
import { accountPace, pace } from "./usage-pace";

const NOW = new Date(2026, 10, 5, 10, 0, 0).getTime(); // local time, so same-day checks do not depend on the zone
const at = (ms: number) => new Date(NOW + ms).toISOString();
const H = 3600_000;
const win = (label: string, minutes: number, used: number, resetsIn?: number): UsageWindow => ({ label, window_minutes: minutes, used_percent: used, resets_at: resetsIn === undefined ? undefined : at(resetsIn) });

describe("window length", () => {
  it("reads the label", () => {
    expect(windowMinutes({ label: "5h", window_minutes: 0 })).toBe(300);
    expect(windowMinutes({ label: "7d", window_minutes: 0 })).toBe(10080);
    expect(windowMinutes({ label: "45m", window_minutes: 0 })).toBe(45);
    expect(windowMinutes({ label: "1w", window_minutes: 0 })).toBe(10080);
  });
  it("falls back to the given minutes", () => {
    expect(windowMinutes({ label: "月", window_minutes: 600 })).toBe(600);
    expect(windowMinutes({ label: "月", window_minutes: 0 })).toBe(0);
  });
});

describe("pace", () => {
  it("is over when use is more than 5 points above an even spread", () => {
    // 5h window, 2h elapsed = 40% expected
    expect(pace(win("5h", 300, 50, 3 * H), NOW)).toEqual({ expected: 40, state: "over" });
  });
  it("is under when more than 5 below", () => {
    expect(pace(win("5h", 300, 30, 3 * H), NOW)?.state).toBe("under");
  });
  it("is on pace within 5 points, edges included", () => {
    expect(pace(win("5h", 300, 45, 3 * H), NOW)?.state).toBe("on");
    expect(pace(win("5h", 300, 35, 3 * H), NOW)?.state).toBe("on");
    expect(pace(win("5h", 300, 45.1, 3 * H), NOW)?.state).toBe("over");
  });
  it("says nothing in the first 5% of the window", () => {
    // 7d window: 5% is 8.4h
    expect(pace(win("7d", 10080, 90, 7 * 24 * H - 8 * H), NOW)).toBeNull();
    expect(pace(win("7d", 10080, 90, 7 * 24 * H - 9 * H), NOW)?.state).toBe("over");
  });
  it("needs a reset time and a length, and a time inside the window", () => {
    expect(pace(win("5h", 300, 50), NOW)).toBeNull();
    expect(pace(win("?", 0, 50, H), NOW)).toBeNull();
    expect(pace(win("5h", 300, 50, 6 * H), NOW)).toBeNull(); // not started: the reset is further than the length
    expect(pace(win("5h", 300, 50, -H), NOW)).toBeNull(); // already reset
  });
});

describe("accountPace", () => {
  it("is the window's pace for a fresh reading", () => {
    expect(accountPace({}, win("5h", 300, 50, 3 * H), NOW)).toEqual({ expected: 40, state: "over" });
    expect(accountPace({ stale: false }, win("5h", 300, 30, 3 * H), NOW)?.state).toBe("under");
  });
  it("says nothing for an out-of-date reading (#12411)", () => {
    expect(accountPace({ stale: true }, win("5h", 300, 50, 3 * H), NOW)).toBeNull();
    expect(accountPace({ stale: true }, win("7d", 10080, 100, 4 * 24 * H), NOW)).toBeNull();
  });
});

describe("countdown", () => {
  it("is minutes within the hour", () => {
    expect(countdown(at(45 * 60_000), NOW)).toBe("45 分钟");
    expect(countdown(at(30_000), NOW)).toBe("1 分钟");
  });
  it("is the clock time later the same day", () => {
    expect(countdown(at(3 * H + 5 * 60_000), NOW)).toBe("13:05");
  });
  it("is days and hours beyond today, in the language's own units", () => {
    expect(countdown(at(2 * 24 * H + 3 * H), NOW)).toBe("2 天 3 小时");
    expect(countdown(at(15 * H), NOW)).toBe("15 小时"); // tomorrow, under a day
  });
  it("leaves out a zero hour", () => {
    expect(countdown(at(5 * 24 * H), NOW)).toBe("5 天");
    expect(countdown(at(5 * 24 * H + 30 * 60_000), NOW)).toBe("5 天"); // the hours are whole: 30 minutes over is still 5 days
  });
  it("handles a due or missing time", () => {
    expect(countdown(at(-1000), NOW)).toBe("即将重置");
    expect(countdown(undefined, NOW)).toBe("");
    expect(countdown("soon", NOW)).toBe("");
  });
});

describe("reset phrase", () => {
  it("is relative for minutes and days, a clock time for later today", () => {
    expect(resetLabel(at(45 * 60_000), NOW)).toBe("45 分钟后重置");
    expect(resetLabel(at(3 * H + 5 * 60_000), NOW)).toBe("13:05 重置");
    expect(resetLabel(at(2 * 24 * H + 3 * H), NOW)).toBe("2 天 3 小时后重置");
    expect(resetLabel(at(5 * 24 * H), NOW)).toBe("5 天后重置");
    expect(resetLabel(at(-1), NOW)).toBe("即将重置");
    expect(resetLabel(undefined, NOW)).toBe("");
  });
  // each language its own units: no Latin unit letters in Chinese ("55m", "2d 3h") and no zero hour ("5d 0h")
  it.each([
    ["en", ["Resets in 45m", "Resets in 2d 3h", "Resets in 5d", "Resets in 15h"]],
    ["ja", ["45分後にリセット", "2日3時間後にリセット", "5日後にリセット", "15時間後にリセット"]],
    ["zh-Hans", ["45 分钟后重置", "2 天 3 小时后重置", "5 天后重置", "15 小时后重置"]],
  ])("reads naturally in %s", async (lang, want) => {
    await initI18n(lang);
    try {
      expect([45 * 60_000, 2 * 24 * H + 3 * H, 5 * 24 * H, 15 * H].map((ms) => resetLabel(at(ms), NOW))).toEqual(want);
    } finally {
      await initI18n("zh-Hans");
    }
  });
});

describe("sample age", () => {
  it("counts seconds first", () => {
    expect(sampleAge(at(-12_000), NOW)).toBe("12秒钟前");
    expect(sampleAge(at(5_000), NOW)).toBe("0秒钟前"); // a clock a little ahead
    expect(sampleAge(at(-5 * 60_000), NOW)).toBe("5分钟前");
    expect(sampleAge(at(-2 * H), NOW)).toBe("2小时前");
    expect(sampleAge(at(-50 * H), NOW)).toBe("2天前");
    expect(sampleAge("x", NOW)).toBe("");
  });
});

const account = (id: string, used: number, o: Partial<UsageAccount> = {}): UsageAccount => ({ id, provider: "codex", name: id, windows: [win("7d", 10080, used, 24 * H)], sampled_at: at(-60_000), ...o });

describe("which accounts show", () => {
  it("drops samples older than three days and windows that reset", () => {
    const got = liveAccounts(
      [account("old", 10, { sampled_at: at(-73 * H) }), account("ok", 10), { ...account("mixed", 10), windows: [win("5h", 300, 99, -H), win("7d", 10080, 3, 24 * H)] }, { ...account("none", 1), windows: [win("5h", 300, 99, -H)] }],
      NOW,
    );
    expect(got.map((a) => a.id)).toEqual(["ok", "mixed"]);
    expect(got[1].windows.map((w) => w.label)).toEqual(["7d"]);
  });
  it("keeps notice-only accounts in liveAccounts while excluding them from topAccounts bars and pinning", () => {
    const noticeOnly: UsageAccount = { id: "cool", provider: "claude", name: "Cooldown seat", windows: [], sampled_at: at(-60_000), notice: "Cooldown · resumes in 20m" };
    const blankNotice: UsageAccount = { id: "blank", provider: "claude", name: "Blank", windows: [], sampled_at: at(-60_000), notice: "   " };
    const ok = account("ok", 40);
    const got = liveAccounts([noticeOnly, blankNotice, ok], NOW);
    expect(got.map((a) => a.id)).toEqual(["cool", "ok"]);
    expect(isPinnableUsageAccount(noticeOnly)).toBe(false);
    expect(isPinnableUsageAccount(ok)).toBe(true);
    expect(topAccounts(got).map((a) => a.id)).toEqual(["ok"]);
    expect(selectSidebarUsageAccounts(got, ["cool"])).toEqual({ bars: [expect.objectContaining({ id: "ok" })], balances: [], custom: false });
  });
  it("ages kept numbers by stale_at, whatever the hub said when it answered (#1905)", () => {
    const kept = account("kept", 10, { stale_at: at(-H) }); // fresh when the hub answered, an hour past its time now
    const fresh = account("fresh", 10, { stale_at: at(H) });
    const older = account("older", 10); // an older hub sends no stale_at: its own mark stands
    const got = liveAccounts([kept, fresh, older], NOW);
    expect(got.map((a) => Boolean(a.stale))).toEqual([true, false, false]);
    expect(topAccounts(got).map((a) => a.id)).toEqual(["fresh", "older"]); // stale last
    expect(liveAccounts([account("flagged", 10, { stale: true, stale_at: at(H) })], NOW)[0].stale).toBe(true);
  });
  it("takes the two fullest, stale last", () => {
    const list = [account("a", 20), account("b", 90, { stale: true }), account("c", 60), account("d", 40)];
    expect(topAccounts(list).map((a) => a.id)).toEqual(["c", "d"]);
    expect(topAccounts([account("a", 20), account("b", 90, { stale: true })]).map((a) => a.id)).toEqual(["a", "b"]);
    expect(fullest({ ...account("x", 1), windows: [win("5h", 300, 30), win("7d", 10080, 70)] })).toBe(70);
  });
  it("keeps tied sidebar percentages in provider and ID order across polls", () => {
    const first = [
      account("third", 38.1),
      account("second", 38.4, { provider: "agy" }),
      account("first", 38, { provider: "antigravity" }),
    ];
    const next = [
      account("first", 38.4, { provider: "agy" }),
      account("third", 38.4),
      account("second", 38, { provider: "antigravity" }),
    ];
    for (const poll of [first, next]) {
      expect(topAccounts(poll).map((a) => a.id)).toEqual(["first", "second"]);
    }
    expect(topAccounts([account("low", 38), account("high", 39)]).map((a) => a.id)).toEqual(["high", "low"]);
  });
});

describe("accounts with several sources and per-model windows (#2595)", () => {
  const account = (o: Partial<UsageAccount> = {}): UsageAccount => ({ id: "a", provider: "claude", name: "seat", sampled_at: at(-1000), windows: [win("5h", 300, 30, 2 * H), win("7d", 10080, 40, 24 * H)], ...o });
  it("sorts by the plan's own fullest window, not a model's that ran out", () => {
    const scoped = { ...win("7d", 10080, 100, 24 * H), model: "Fable" };
    const a = account({ windows: [win("7d", 10080, 40, 24 * H), scoped] });
    expect(fullest(a)).toBe(40);
    expect(fullest(account({ windows: [scoped] }))).toBe(100); // nothing else to go by
    expect(topAccounts([account({ id: "x", windows: [scoped, win("7d", 10080, 10, 24 * H)] }), account({ id: "y" })], 1)[0].id).toBe("y");
  });
  it("names where an account was seen", () => {
    expect(sourceNames(account({ local: true, machines: ["MacBook", "Studio"], sources: ["Relay"] }))).toEqual(["MacBook", "Studio", "Relay"]);
    expect(sourceNames(account({ local: true, sources: ["Relay"] }))).toEqual(["电脑", "Relay"]); // an older hub: no names, never "this computer" (#1907)
    expect(sourceNames(account({ sources: ["Relay", "Team pool"] }))).toEqual(["Relay", "Team pool"]);
    expect(sourceNames(account())).toEqual([]); // an older hub says nothing about it
  });
  it("shows the plan unless the name has it", () => {
    expect(planNote(account({ name: "Codex Pro", plan: "pro" }))).toBe("");
    expect(planNote(account({ name: "work seat", plan: "Pro" }))).toBe("Pro");
    expect(planNote(account({ plan: " " }))).toBe("");
  });
  it("tells windows of one length apart by model", () => {
    expect(windowKey(win("7d", 10080, 1))).not.toBe(windowKey({ ...win("7d", 10080, 1), model: "Fable" }));
  });
});

describe("prepaid balances (#11825)", () => {
  const bal = (id: string, amount: string, o: Partial<UsageAccount> = {}): UsageAccount => ({ id, provider: "deepseek", name: id, windows: [], sampled_at: at(-60_000), balance: { amount, currency: "CNY" }, ...o });
  it("keeps a balance account without windows, and it takes no bar place from a window account", () => {
    const got = liveAccounts([bal("ds", "12.34"), account("ok", 10)], NOW);
    expect(got.map((a) => a.id)).toEqual(["ds", "ok"]);
    expect(isBalanceAccount(got[0])).toBe(true);
    expect(balanceAccounts(got).map((a) => a.id)).toEqual(["ds"]);
    expect(topAccounts(got).map((a) => a.id)).toEqual(["ok"]);
    expect(balanceText(got[0].balance!)).toBe("CNY 12.34");
  });
  it("leaves a DeepSeek account that reports windows as an ordinary bar account", () => {
    const gateway: UsageAccount = { id: "ds-window", provider: "deepseek", name: "DeepSeek gateway", windows: [win("7d", 10080, 10, 24 * H)], sampled_at: at(-60_000) };
    expect(isBalanceAccount(gateway)).toBe(false);
    expect(balanceAccounts([gateway])).toEqual([]);
    expect(topAccounts([gateway]).map((a) => a.id)).toEqual(["ds-window"]);
  });
  it("keeps a genuine zero apart from a failed read", () => {
    const zero = bal("ds", "0.00");
    const failed: UsageAccount = { ...bal("ds", "0.00"), balance: undefined, problem: "unreachable" };
    expect(balanceEmpty(zero.balance!)).toBe(true);
    expect(liveAccounts([zero], NOW)[0].balance?.amount).toBe("0.00");
    const kept = liveAccounts([failed], NOW);
    expect(kept).toHaveLength(1);
    expect(kept[0].balance).toBeUndefined();
    expect(kept[0].problem).toBe("unreachable");
    expect(isBalanceAccount(kept[0])).toBe(true); // the provider still names a balance row, so the popover can say why
  });
});

describe("pinned sidebar usage accounts (#12418)", () => {
  const bal = (id: string, amount: string): UsageAccount => ({ id, provider: "deepseek", name: "DeepSeek", windows: [], sampled_at: at(-60_000), balance: { amount, currency: "CNY" } });
  const live = (): UsageAccount[] => [
    account("codex", 85, { provider: "codex", name: "Codex Pro" }),
    account("claude", 60, { provider: "claude", name: "Claude Max" }),
    account("agy", 15, { provider: "agy", name: "Antigravity" }),
    bal("ds", "12.34"),
  ];

  it("parses stored IDs with deduplication and tolerates malformed JSON", () => {
    expect(parsePinnedUsageAccounts(null)).toEqual([]);
    expect(parsePinnedUsageAccounts("")).toEqual([]);
    expect(parsePinnedUsageAccounts("{not-json")).toEqual([]);
    expect(parsePinnedUsageAccounts('{"a":1}')).toEqual([]);
    expect(parsePinnedUsageAccounts('["agy", "codex", "agy", "", "  ", 42, " ds "]')).toEqual(["agy", "codex", "ds"]);
  });

  it("keeps automatic top-2 + balance behavior when nothing is pinned", () => {
    const picked = selectSidebarUsageAccounts(live(), []);
    expect(picked.custom).toBe(false);
    expect(picked.bars.map((a) => a.id)).toEqual(["codex", "claude"]);
    expect(picked.balances.map((a) => a.id)).toEqual(["ds"]);
  });

  it("selects pinned window and balance accounts in pinned order", () => {
    const onlyAgy = selectSidebarUsageAccounts(live(), ["agy"]);
    expect(onlyAgy.custom).toBe(true);
    expect(onlyAgy.bars.map((a) => a.id)).toEqual(["agy"]);
    expect(onlyAgy.balances).toEqual([]);

    const mixed = selectSidebarUsageAccounts(live(), ["agy", "ds", "codex"]);
    expect(mixed.custom).toBe(true);
    expect(mixed.bars.map((a) => a.id)).toEqual(["agy", "codex"]);
    expect(mixed.balances.map((a) => a.id)).toEqual(["ds"]);

    const onlyBalance = selectSidebarUsageAccounts(live(), ["ds"]);
    expect(onlyBalance.custom).toBe(true);
    expect(onlyBalance.bars).toEqual([]);
    expect(onlyBalance.balances.map((a) => a.id)).toEqual(["ds"]);
  });

  it("shows surviving pinned accounts on partial loss and falls back cleanly when all pinned accounts are offline", () => {
    const partial = selectSidebarUsageAccounts(live(), ["gone", "agy"]);
    expect(partial.custom).toBe(true);
    expect(partial.bars.map((a) => a.id)).toEqual(["agy"]);
    expect(partial.balances).toEqual([]);

    const fallback = selectSidebarUsageAccounts(live(), ["gone-1", "gone-2"]);
    expect(fallback.custom).toBe(false);
    expect(fallback.bars.map((a) => a.id)).toEqual(["codex", "claude"]);
    expect(fallback.balances.map((a) => a.id)).toEqual(["ds"]);
  });
});

describe("provider cards and the reset timeline (#13173)", () => {
  const acct = (id: string, name: string, provider: string, windows: UsageWindow[], sampled = at(-60_000)): UsageAccount => ({ id, name, provider, windows, sampled_at: sampled });
  it("lists the 5h window, then 7d, then the rest, per-model allowances last", () => {
    const a = acct("a", "A", "claude", [{ ...win("7d", 10080, 1, 3 * H), model: "Fable" }, win("1d", 1440, 1, H), win("7d", 10080, 1, 5 * H), win("5h", 300, 1, H)]);
    expect(sortedWindows(a).map((w) => `${w.label}${w.model ? "*" : ""}`)).toEqual(["5h", "7d", "1d", "7d*"]);
  });
  it("sorts a card's accounts by the 7d reset, soonest first, those without one last, then case-insensitive name and id", () => {
    const a1 = acct("1", "Zed", "claude", [win("5h", 300, 1, H), win("7d", 10080, 1, 48 * H)]);
    const a2 = acct("2", "Amy", "claude", [win("7d", 10080, 1, 5 * H)]);
    const a3 = acct("3", "bob", "claude", [win("5h", 300, 1, H)]);
    const a4 = acct("4", "Abe", "claude", [win("7d", 10080, 1)]); // no reset time
    const a5 = acct("5", "Abe", "claude", [win("7d", 10080, 1)]); // same name, tie-broken by id
    expect(sortForCard([a1, a3, a5, a4, a2]).map((a) => `${a.name}:${a.id}`)).toEqual(["Amy:2", "Zed:1", "Abe:4", "Abe:5", "bob:3"]);
  });
  it("groups by provider in the hub's order, with the newest sample of each", () => {
    const g = groupByProvider([acct("1", "A", "claude", [], at(-5 * H)), acct("2", "B", "codex", [], at(-H)), acct("3", "C", "claude", [], at(-2 * H))]);
    expect(g.map((x) => [x.provider, x.accounts.length, x.sampledAt])).toEqual([["claude", 2, at(-2 * H)], ["codex", 1, at(-H)]]);
    expect(providerTitle("xai")).toBe("xAI");
    expect(providerTitle("codex")).toBe("Codex");
  });
  it("puts 24 hours at 45% of the shared axis and 7 days at the end, capped", () => {
    expect(timelinePosition(0)).toBe(0);
    expect(timelinePosition(6 * H)).toBeCloseTo(0.1125, 6);
    expect(timelinePosition(24 * H)).toBeCloseTo(0.45, 6);
    expect(timelinePosition(72 * H)).toBeCloseTo(0.45 + (48 / 144) * 0.55, 6);
    expect(timelinePosition(7 * 24 * H)).toBeCloseTo(1, 6);
    expect(timelinePosition(30 * 24 * H)).toBe(1);
    expect(timelinePosition(-H)).toBe(0);
  });
  it("makes a row per account in the given order, a lane per plan window from now to its reset", () => {
    const a = acct("a", "A", "claude", [win("7d", 10080, 1, 48 * H), win("5h", 300, 1, 2 * H)]);
    const b = acct("b", "B", "claude", [win("7d", 10080, 1, 20 * 24 * H)]);
    const rows = providerTimeline([b, a], NOW);
    expect(rows.map((r) => r.account.id)).toEqual(["b", "a"]);
    expect(rows[1].lanes.map((l) => [l.label, l.long, Number(l.end.toFixed(4)), l.remainingMs])).toEqual([["5h", false, 0.0375, 2 * H], ["7d", true, 0.45 + (24 / 144) * 0.55 > 0 ? Number((0.45 + (24 / 144) * 0.55).toFixed(4)) : 0, 48 * H]]);
    expect(rows[0].lanes[0].end).toBe(1); // beyond 7 days runs to the end of the axis
  });
  it("leaves out windows without a reset time, ended ones and per-model allowances, and an account with none has no row", () => {
    const a = acct("a", "A", "claude", [win("5h", 300, 1), win("7d", 10080, 1, -H), { ...win("7d", 10080, 1, 3 * H), model: "Fable" }]);
    expect(providerTimeline([a], NOW)).toEqual([]);
  });
  it("writes the time left short, in each language's units", async () => {
    await initI18n("zh-Hans");
    expect(remainingText(40 * 60_000)).toBe("40 分钟");
    expect(remainingText(1.5 * H)).toBe("1.5 小时");
    expect(remainingText(50 * H)).toBe("2 天 2 小时");
    expect(remainingText(48 * H)).toBe("2 天");
  });
});
