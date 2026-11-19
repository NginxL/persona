// relayedFrom: the hub's wording of a conversation_send relay in a Persona chat's queue, read apart.
import { describe, expect, it } from "vitest";
import { relayedFrom } from "./queue-relayed";

describe("relayedFrom", () => {
  it("reads the title and the words of a conversation_send relay", () => {
    expect(relayedFrom("来自「主对话」：帮我看下日程")).toEqual({ from: "主对话", text: "帮我看下日程" });
  });

  it("trims the title", () => {
    expect(relayedFrom("来自「  日报  」：汇总")).toEqual({ from: "日报", text: "汇总" });
  });

  it("returns null for lines that are not a relay", () => {
    expect(relayedFrom("日报的通知")).toBeNull();
    expect(relayedFrom("督办消息")).toBeNull();
    expect(relayedFrom("请看 来自「主对话」：不在开头")).toBeNull();
  });

  it("returns null for an empty title", () => {
    expect(relayedFrom("来自「」：内容")).toBeNull();
    expect(relayedFrom("来自「  」：内容")).toBeNull();
  });

  it("ends the title at the first 」：, so words that open with a quoted phrase stay whole", () => {
    expect(relayedFrom("来自「主对话」：「用户追加：『看下日程』」：请处理")).toEqual({
      from: "主对话",
      text: "「用户追加：『看下日程』」：请处理",
    });
    expect(relayedFrom("来自「主对话」：「甲」：乙")).toEqual({ from: "主对话", text: "「甲」：乙" });
  });

  it("keeps newlines in the words", () => {
    expect(relayedFrom("来自「主对话」：第一行\n第二行\n")).toEqual({ from: "主对话", text: "第一行\n第二行\n" });
  });
});
