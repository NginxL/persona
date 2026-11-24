import { describe, expect, it } from "vitest";
import { botStateText, botSummary, botTone, credentialsChanged, credentialsOf, credentialsUpdate, DEFAULT_FEATURES, domainChoice, featureEdit, livePairing, nudgeChoices, nudgeLabel, pairingLeft, withFeature, type FeishuBotStatus } from "./feishu-bot";
import type { HubUpdateStatus } from "./hub-update";
import { channelTitle, dockerSteps, DOCKER_COMMAND, latestWarning, recordTitle, recordTone, sourceBehindWarning, updateFinished, updateStateTitle, updateView, updateWorking } from "./hub-update-settings";

const bot = (o: Partial<FeishuBotStatus> = {}): FeishuBotStatus => ({
  enabled: true, app_id: "cli_x", domain: "https://open.feishu.cn", has_secret: true, from_config: false,
  features: DEFAULT_FEATURES, connected: true, owner: { name: "我", paired_at: 1 }, ...o,
});

describe("feishu bot", () => {
  it("maps the resolved domain to the picker", () => {
    expect(domainChoice("https://open.feishu.cn")).toBe("feishu");
    expect(domainChoice("")).toBe("feishu");
    expect(domainChoice("https://larksuite.com")).toBe("lark");
    expect(domainChoice("https://open.example.com")).toBe("https://open.example.com");
  });

  it("tells changed credentials and sends only a typed secret", () => {
    const st = bot();
    const c = credentialsOf(st);
    expect(c).toEqual({ appID: "cli_x", appSecret: "", domain: "feishu" });
    expect(credentialsChanged(st, c)).toBe(false);
    expect(credentialsChanged(st, { ...c, appID: " cli_x " })).toBe(false);
    expect(credentialsChanged(st, { ...c, appSecret: "s" })).toBe(true);
    expect(credentialsChanged(st, { ...c, domain: "lark" })).toBe(true);
    expect(credentialsChanged(null, { appID: "", appSecret: "", domain: "feishu" })).toBe(false);
    expect(credentialsUpdate({ appID: " a ", appSecret: "  ", domain: "lark" })).toEqual({ app_id: "a", domain: "lark" });
    expect(credentialsUpdate({ appID: "a", appSecret: " s ", domain: "feishu" })).toEqual({ app_id: "a", domain: "feishu", app_secret: "s" });
  });

  it("couples 所有 Agent 的提问 with 提问和批准", () => {
    expect(withFeature({ ...DEFAULT_FEATURES, all_cards: true }, "cards", false)).toMatchObject({ cards: false, all_cards: false });
    expect(withFeature({ ...DEFAULT_FEATURES, cards: false }, "all_cards", true)).toMatchObject({ cards: true, all_cards: true });
    expect(withFeature(DEFAULT_FEATURES, "sync", true)).toEqual({ ...DEFAULT_FEATURES, sync: true });
  });

  it("sends only the switch that changed, so one changed elsewhere is not put back (#74)", () => {
    // The page may be stale: whatever it holds for the other switches stays out of the request.
    expect(featureEdit(DEFAULT_FEATURES, "board", true)).toEqual({ board: true });
    expect(featureEdit(DEFAULT_FEATURES, "pushes", true)).toEqual({ pushes: true });
    expect(featureEdit({ ...DEFAULT_FEATURES, board: true }, "sync", true)).toEqual({ sync: true });
    expect(featureEdit(DEFAULT_FEATURES, "reports", false)).toEqual({ reports: false });
    // The pair that goes together names both, since the hub does not tie them.
    expect(featureEdit(DEFAULT_FEATURES, "cards", false)).toEqual({ cards: false, all_cards: false });
    expect(featureEdit(DEFAULT_FEATURES, "all_cards", true)).toEqual({ all_cards: true, cards: true });
    expect(featureEdit(DEFAULT_FEATURES, "all_cards", false)).toEqual({ all_cards: false });
    expect(featureEdit(DEFAULT_FEATURES, "cards", true)).toEqual({ cards: true });
  });

  it("sends every switch to a hub from before 0.1.12, which replaces the whole object (no `reports` in its status)", () => {
    const { reports: _reports, board: _board, ...old } = DEFAULT_FEATURES;
    expect(featureEdit(old, "pushes", true)).toEqual({ ...old, pushes: true });
    expect(featureEdit({ ...old, all_cards: true }, "cards", false)).toEqual({ ...old, cards: false, all_cards: false });
  });

  it("lists the nudge delays, with the one in force among them", () => {
    expect(nudgeChoices(60)).toEqual([0, 15, 30, 60, 120, 240, 480]);
    expect(nudgeChoices(90)).toEqual([0, 15, 30, 60, 90, 120, 240, 480]);
    expect(nudgeChoices(10080).at(-1)).toBe(10080);
    expect([0, 15, 60, 90, 120].map(nudgeLabel)).toEqual(["关闭", "15 分钟", "1 小时", "90 分钟", "2 小时"]);
  });

  it("says the state", () => {
    expect(botStateText(null, true)).toBe("正在检查…");
    expect(botStateText(null)).toBe("读不到状态");
    expect(botStateText(bot({ enabled: false }))).toBe("未打开");
    expect(botStateText(bot({ has_secret: false }))).toBe("还没填应用凭证");
    expect(botStateText(bot({ connected: false, error: "x" }))).toBe("连不上：x");
    expect(botStateText(bot({ connected: false }))).toBe("正在连接…");
    expect(botStateText(bot({ owner: null }))).toBe("已连接，等你配对");
    expect(botStateText(bot())).toBe("已连接");
    expect([botTone(bot({ enabled: false })), botTone(bot({ connected: false })), botTone(bot({ owner: null })), botTone(bot())]).toEqual([undefined, "red", "orange", "green"]);
    expect([botSummary(null), botSummary(bot({ enabled: false })), botSummary(bot({ connected: false })), botSummary(bot({ owner: null })), botSummary(bot())]).toEqual(["", "未打开", "连不上", "待配对", "已连接"]);
  });

  it("keeps only a pairing code that is still valid", () => {
    const now = 1_000_000_000_000;
    expect(livePairing(bot({ pairing: { code: "1", expires_at: now / 1000 + 5 } }), now)?.code).toBe("1");
    expect(livePairing(bot({ pairing: { code: "1", expires_at: now / 1000 - 5 } }), now)).toBeNull();
    expect(pairingLeft(now / 1000 + 545, now)).toBe("9 分 5 秒内有效");
    expect(pairingLeft(now / 1000 - 3, now)).toBe("0 分 0 秒内有效");
  });
});

const hub = (o: Partial<HubUpdateStatus> = {}): HubUpdateStatus => ({
  version: "v1", build: 1, channel: "release", can_update: true, state: "idle", busy: [], auto: false, auto_notice: false, confirmed: true, ...o,
});

describe("hub update", () => {
  it("names the channel, the state and the history", () => {
    expect([channelTitle("release"), channelTitle("docker"), channelTitle("")]).toEqual(["发布版", "Docker", "开发版"]);
    expect(updateStateTitle(hub({ state: "waiting_idle" }))).toBe("等助手和 Agent 空闲");
    expect(updateStateTitle(hub({ state: "waiting_idle", busy: [{ machine_id: "m", machine: "M", title: "t" }] }))).toBe("等 1 个在忙的结束");
    expect(updateStateTitle(hub())).toBe("已是最新");
    expect([recordTitle("updated"), recordTitle("held"), recordTitle("failed")]).toEqual(["已更新", "没有自动装", "没更新成功"]);
    expect([recordTone("updated"), recordTone("rolled_back"), recordTone("held"), recordTone("failed")]).toEqual(["green", "orange", "orange", "red"]);
  });

  it("knows an update under way and one that just ended", () => {
    expect(updateWorking(hub({ state: "restarting" }))).toBe(true);
    expect(updateWorking(hub({ state: "available" }))).toBe(false);
    expect(updateFinished("restarting", "updated")).toBe(true);
    expect(updateFinished("updated", "updated")).toBe(false);
    expect(updateFinished(undefined, "available")).toBe(false);
  });

  it("picks what the 更新 group shows", () => {
    const latest = { version: "v2", build: 2, newer: true, bad: false };
    expect(updateView(hub({ channel: "docker", can_update: false }))).toEqual({ kind: "docker" });
    expect(updateView(hub({ can_update: false, why_not: "开发版" }))).toEqual({ kind: "cannot", why: "这个 hub 不会自己更新：开发版。" });
    expect(updateView(hub({ latest }))).toEqual({ kind: "newer", working: false });
    expect(updateView(hub({ latest, state: "waiting_idle" }))).toEqual({ kind: "newer", working: true });
    expect(updateView(hub({ state: "confirming" }))).toEqual({ kind: "working" });
    expect(updateView(hub({ latest: { ...latest, newer: false } }))).toEqual({ kind: "current" });
  });

  it("warns about a release that went back or is held", () => {
    const latest = { version: "v2", build: 2, newer: true, bad: false, held: "旧代码" };
    expect(latestWarning(hub({ latest }))).toBeNull(); // held only matters with auto-update on
    expect(latestWarning(hub({ latest, auto: true }))).toBe("自动更新不会装这一版：旧代码。确认没问题可以手动更新。");
    expect(latestWarning(hub({ latest: { ...latest, bad: true } }))).toContain("已经回滚过");
    expect(latestWarning(hub({ latest: { ...latest, newer: false }, auto: true }))).toBeNull();
  });

  it("says when the update source looks stopped", () => {
    const latest = { version: "v0.1.17-177-g563f7fde5b-202611071005", build: 202611071005, newer: false, bad: false };
    const site = { latest, update_url: "https://personaside.example/dl", update_url_from: "build" };
    expect(sourceBehindWarning(hub({ ...site, source_behind: true }))).toBe(
      "更新源 https://personaside.example/dl（编译时写入）最新只有 v0.1.17-177-g563f7fde5b-202611071005，比这个 hub 还旧，看起来已经不再发布了。在 hub.json 的 update.url 里换一个更新源。",
    );
    expect(sourceBehindWarning(hub({ ...site, update_url_from: "hub.json", source_behind: true }))).toContain("（hub.json 的 update.url）");
    expect(sourceBehindWarning(hub(site))).toBeNull(); // a current source: "Up to date" as before
    expect(sourceBehindWarning(hub({ source_behind: true }))).toBeNull(); // nothing to name
    expect(updateView(hub({ ...site, source_behind: true }))).toEqual({ kind: "current" });
  });

  it("tells a Docker hub what to run", () => {
    const docker = hub({ channel: "docker", can_update: false, image: { name: "ghcr.io/owner/x", tag: "latest", pinned: false } });
    expect(dockerSteps(docker).headline).toBeUndefined();
    const newer = { ...docker, latest: { version: "0.3.1", build: 0, newer: true, bad: false } };
    expect(dockerSteps(newer).steps.map((s) => s.copy)).toEqual([DOCKER_COMMAND]);
    const pinned = { ...newer, image: { name: "ghcr.io/owner/x", tag: "0.3.0", pinned: true } };
    expect(dockerSteps(pinned)).toMatchObject({ headline: "新镜像 0.3.1", steps: [{ copy: "ghcr.io/owner/x:0.3.1" }, { copy: DOCKER_COMMAND }] });
  });
});
