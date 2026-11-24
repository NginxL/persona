import { describe, expect, it } from "vitest";
import { canBind, capabilityText, channelOf, channelSections, channelsSummary, channelStateText, channelSubtitle, conversationChannel, isPolled, settingsTarget, targetText, type Channel } from "./channels";

const ch = (c: Partial<Channel> & Pick<Channel, "id">): Channel => ({
  name: c.id, kind: "im", source: "plugin", installed: true, enabled: true, state: "connected", capabilities: {}, bind: "qr", ...c,
});

const app = ch({ id: "app", name: "Persona", kind: "app", source: "builtin", bind: "none", read_only: true, surfaces: ["iPhone", "网页"] });
const wechat = ch({ id: "wechat", name: "微信", account: { name: "微信用户 a1b2c3" } });
const feishu = ch({ id: "feishu", name: "飞书机器人", source: "builtin", bind: "fields", state: "error", error: "长连接断了", account: { name: "示例用户" } });
const fresh = ch({ id: "examplechat", name: "Example Chat", state: "not_connected", bind: "fields", description: "示例" });
const market = ch({ id: "examplemarket", name: "Example Rooms", source: "market", installed: false, state: "not_installed", bind: "none", market: "m", plugin: "p" });

describe("the list", () => {
  it("puts the apps, the installed and the markets' in their sections", () => {
    const s = channelSections([app, wechat, feishu, fresh, market]);
    expect(s.map((x) => [x.title, x.channels.map((c) => c.id)])).toEqual([
      ["Persona", ["app"]],
      ["已接入", ["wechat", "feishu", "examplechat"]],
      ["可以安装", ["examplemarket"]],
    ]);
    expect(channelSections([app]).map((x) => x.title)).toEqual(["Persona"]);
  });

  it("says how each is doing, in words", () => {
    expect([wechat, feishu, fresh, market].map(channelStateText)).toEqual(["已连接", "出错了", "未连接", "没安装"]);
    expect(channelSubtitle(app)).toBe("iPhone · 网页");
    expect(channelSubtitle(wechat)).toBe("微信用户 a1b2c3");
    expect(channelSubtitle(feishu)).toBe("长连接断了");
    expect(channelSubtitle(fresh)).toBe("示例");
    expect(channelsSummary([app, wechat, feishu, fresh])).toBe("1 个已连接 · 1 个出错");
    expect(channelsSummary([app, wechat])).toBe("1 个已连接");
  });
});

describe("a channel's page", () => {
  it("binds only what binds here, is on and is not bound", () => {
    expect(canBind(fresh)).toBe(true);
    expect(canBind(wechat)).toBe(false);
    expect(canBind({ ...fresh, enabled: false })).toBe(false);
    expect(canBind(app)).toBe(false);
    expect(canBind(market)).toBe(false);
    expect(canBind({ ...fresh, bind: "config" })).toBe(false);
  });

  it("polls a QR code or a code, not fields", () => {
    expect(isPolled({ step: "qr", kind: "qr" })).toBe(true);
    expect(isPolled({ step: "pair", kind: "pair_code", user_code: "123456" })).toBe(true);
    expect(isPolled({ step: "token", fields: [{ name: "token", label: "令牌" }] })).toBe(false);
    expect(isPolled({ done: true, kind: "qr" })).toBe(false);
  });

  it("names where messages go", () => {
    expect(targetText(undefined)).toBe("主对话");
    expect(targetText({ kind: "new" })).toBe("新对话（下一条消息开）");
    expect(targetText({ kind: "conversation", machine_id: "m", session_id: "s", title: "周报" })).toBe("周报");
  });

  it("says what it can do", () => {
    expect(capabilityText({ images_in: true, voice_in: true, format: "plain", max_text: 3500, asks: "text", typing: true })).toEqual([
      "收文字、图片、语音", "回答是纯文字", "一条最多 3500 字，长的拆开发", "提问和批准回复序号或文字", "处理时显示「正在输入」",
    ]);
    expect(capabilityText({ format: "plain", reply_only: true })).toEqual([
      "收文字", "回答是纯文字", "仅回复收到的消息",
    ]);
  });

  it("opens its own page in the settings", () => {
    expect(settingsTarget("settings/connectors/feishu-bot")).toEqual({ tab: "connectors", sub: "feishu-bot" });
    expect(settingsTarget("settings/plugins/wechat")).toEqual({ tab: "extensions", sub: "wechat" });
    expect(settingsTarget("settings/model-config")).toEqual({ tab: "model-config", sub: null });
    expect(settingsTarget("https://example.com")).toBeNull();
  });
});

describe("where a message came from", () => {
  const list = [app, wechat, feishu];
  it("reads the client_msg_id's prefix against the channels there are", () => {
    expect(channelOf("wechat-message:12", list)?.name).toBe("微信");
    expect(channelOf("feishu-om_1", list)?.id).toBe("feishu");
    expect(channelOf("3f2a9c1e-1111-4222-8333-444455556666", list)).toBeUndefined(); // the app's own
    expect(channelOf("telegram-9", list)).toBeUndefined();
    expect(channelOf("app-1", list)).toBeUndefined(); // the apps are not a source to mark
    expect(channelOf(undefined, list)).toBeUndefined();
  });

  it("names a conversation's latest channel, even one this hub no longer has", () => {
    expect(conversationChannel(["feishu", "wechat"], list)?.name).toBe("飞书机器人");
    expect(conversationChannel(["gone"], list)).toEqual({ id: "gone", name: "gone" });
    expect(conversationChannel([], list)).toBeUndefined();
  });
});
