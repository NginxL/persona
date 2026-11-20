import { describe, expect, it } from "vitest";
import {
  entities, fieldValue, forwardCount, forwardedMessages, forwardedText, forwardPreviews, hubMediaURL, larkAvatarSrc, larkStaticResourceKey, meetingNumber,
  stripHTML, summaryFields, summaryTone, summaryVariant, voteOptions,
} from "./lark-summary";

describe("lark summary cards", () => {
  it("reads the variant, none for a plain card", () => {
    expect(summaryVariant({ variant: "merge-forward" })).toBe("merge-forward");
    expect(summaryVariant({ header: {} })).toBe("");
    expect(summaryVariant(undefined)).toBe("");
  });

  it("shows ISO times in fields as local times and anything else as is", () => {
    expect(fieldValue("李雷")).toBe("李雷");
    expect(fieldValue("2026-01-02T03:04:05Z", "en")).not.toBe("2026-01-02T03:04:05Z");
    expect(summaryFields({ fields: [{ label: "发起人", value: "李雷" }, { label: "空", value: "" }, { value: "x" }] })).toEqual([{ label: "发起人", value: "李雷" }]);
  });

  it("decodes entities and strips markup", () => {
    expect(entities("a&nbsp;&lt;b&gt; &#x4e2d;&#25991; &amp;")).toBe("a\u00a0<b> 中文 &");
    expect(stripHTML("<p>第一行</p><div>第二<b>行</b></div>")).toBe("第一行\n第二行");
  });

  it("reads a merge-forward's messages, previews and count", () => {
    const meta = {
      messageCount: 5,
      previews: [{ senderName: "A", text: "**好的**" }],
      messages: [{ id: "m1", senderName: "A", parts: [{ kind: "text", text: "图标" }, { kind: "text", text: "再小一点" }] }, { senderName: "B", textPreview: "嗯" }],
    };
    expect(forwardCount(meta)).toBe(5);
    expect(forwardCount({ previews: [{}, {}] })).toBe(2);
    expect(forwardedMessages(meta).map((m) => m.id)).toEqual(["m1", "fw-1"]);
    expect(forwardPreviews(meta)).toHaveLength(2);
    expect(forwardPreviews({ previews: [{ text: "x" }] })).toHaveLength(1);
    expect(forwardedText(meta.messages[0])).toBe("图标再小一点");
    expect(forwardedText({ text: "**好的**，下午  给你" })).toBe("好的，下午 给你");
  });

  it("maps Lark gateway media paths onto the hub", () => {
    expect(hubMediaURL("/api/media/avatar-1")).toBe("/api/v1/lark/media/avatar-1");
    expect(hubMediaURL("/agent/v1/media/default-avatar_1?ol_account=2")).toBe("/api/v1/lark/media/default-avatar_1?ol_account=2");
    expect(hubMediaURL("https://cdn.example.com/a.png")).toBe("https://cdn.example.com/a.png");
    expect(hubMediaURL("/api/v1/lark/media/x")).toBe("/api/v1/lark/media/x");
    expect(hubMediaURL(undefined)).toBeUndefined();
  });

  it("rewrites a signed Lark CDN static-resource link onto the hub media route", () => {
    expect(
      hubMediaURL("https://s1-imfile.feishucdn.com/static-resource/v1/v2_b12d2dcd-9f41-465a-9797-5c02003aab6g~?image_size=noop&format=image"),
    ).toBe("/api/v1/lark/media/v2_b12d2dcd-9f41-465a-9797-5c02003aab6g");
    expect(hubMediaURL("https://example.larksuite.com/static-resource/v1/abc%2Fdef?x=1")).toBe("/api/v1/lark/media/abc%2Fdef");
    // only Lark's own CDN hosts are rewritten, and only over https
    expect(hubMediaURL("https://evil.feishucdn.com.attacker.example/static-resource/v1/x")).toBe(
      "https://evil.feishucdn.com.attacker.example/static-resource/v1/x",
    );
    expect(hubMediaURL("http://s1-imfile.feishucdn.com/static-resource/v1/x")).toBe("http://s1-imfile.feishucdn.com/static-resource/v1/x");
    expect(larkStaticResourceKey("https://s1-imfile.feishucdn.com/other/v1/x")).toBeUndefined();
  });

  it("requests the gateway's thumbnail size on the old portrait's owning account", () => {
    expect(larkAvatarSrc("photo/one", 2, 60)).toBe("/api/v1/lark/media/photo%2Fone?ol_account=2&size=72");
    expect(larkAvatarSrc("/agent/v1/media/photo%2Fone?ol_account=2", 3, 56)).toBe("/api/v1/lark/media/photo%2Fone?ol_account=3&size=72");
    expect(larkAvatarSrc("/api/media/photo%2Fone?ol_account=4", undefined, 32)).toBe("/api/v1/lark/media/photo%2Fone?ol_account=4&size=48");
    expect(larkAvatarSrc("https://img.feishucdn.com/static-resource/v1/a~?format=image", undefined, 24)).toBe("/api/v1/lark/media/a?size=24");
    for (const ref of ["/api/v1/lark/attachments/a", "https://images.example/a", "//images.example/a", "one/../two", "/api/media/a%2F..%2Ftwo", "a//b", "a?size=3", "/api/media/%ZZ", "/api/media/x?ol_account=-1", "/api/media/x?ol_account=0x2", "/api/media/x?ol_account=1000000000"]) {
      expect(larkAvatarSrc(ref, undefined, 48)).toBeUndefined();
    }
  });

  it("colors, meeting numbers and votes", () => {
    expect(summaryTone("hongbao")).toBe("red");
    expect(summaryTone("calendar")).toBe("orange");
    expect(summaryTone("task")).toBe("green");
    expect(summaryTone("video-chat")).toBe("accent");
    expect(meetingNumber({ actions: [{ type: "join" }, { type: "copyMeetingNumber", value: "123 456" }] })).toBe("123 456");
    expect(meetingNumber({})).toBeUndefined();
    expect(voteOptions({ vote: { maxPick: 2, options: [{ content: "周一" }, { content: "" }, { content: "周二" }] } })).toEqual({ options: ["周一", "周二"], multiple: true });
    expect(voteOptions({})).toEqual({ options: [], multiple: false });
  });
});
