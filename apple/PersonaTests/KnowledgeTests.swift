import Foundation
import Testing
@testable import Persona

@MainActor
struct KnowledgeTests {
    @Test func timelineLinesSplitIntoWhenSourceAndText() {
        let l = KBLogLine("- 2026-10-30 23:16 · Agent · 迭代需求实现 · 一台电脑 · Round 1 is on the dev set.")
        #expect(l.when == "2026-10-30 23:16")
        #expect(l.source == "Agent · 迭代需求实现 · 一台电脑")
        #expect(l.text == "Round 1 is on the dev set.")
        #expect(l.shortWhen(now: ISO8601DateFormatter().date(from: "2026-11-01T00:00:00Z")!) == "10-30 23:16")
        #expect(l.shortWhen(now: ISO8601DateFormatter().date(from: "2027-02-01T00:00:00Z")!) == "2026-10-30 23:16")
        let plain = KBLogLine("- 只有一句话")
        #expect(plain.when.isEmpty && plain.source.isEmpty && plain.text == "只有一句话")
    }

    @Test func searchHitsKnowTheirEntryAndFile() {
        let a = KBHit(path: "chats/lark-1/summary.md", line: "- 10-29：修复了 MCP")
        #expect(a.entry?.kind == .chats && a.entry?.id == "lark-1")
        #expect(a.source == "档案" && a.text == "10-29：修复了 MCP")
        let b = KBHit(path: "docs/abc.json", line: "  \"title\": \"设计稿\",")
        #expect(b.entry?.kind == .docs && b.entry?.id == "abc")
        #expect(b.source == "资料" && b.text == "设计稿")
        let c = KBHit(path: "chats/lark-1/messages/2026-10-30.md", line: "10:01 张三：MCP 挂了")
        #expect(c.source == "聊天记录 · 2026-10-30")
        #expect(KBHit(path: "README.md", line: "x").entry == nil)
        #expect(c.day == "2026-10-30" && a.day == nil)
        // An archived message: time, sender, text (a colon in the text stays there).
        let m = KBMessageLine("- 10:01 张三: MCP 挂了: 重启了\n第二行")
        #expect(m.time == "10:01" && m.sender == "张三" && m.text == "MCP 挂了: 重启了\n第二行")
        #expect(KBMessageLine("没有格式的一行").text == "没有格式的一行")
        // Go's times, nanoseconds and all; its zero time is none.
        #expect(ItemFacts.parse("2026-10-29T18:27:00.519822572Z") == ISO8601DateFormatter().date(from: "2026-10-29T18:27:00Z"))
        #expect(ItemFacts.parse("0001-01-01T00:00:00Z") == nil)
        // Every word searched for is marked.
        let marked = KBHitRow.marked("MCP 挂了，重启 mcp", "mcp 重启")
        #expect(marked.runs.filter { $0.inlinePresentationIntent == .stronglyEmphasized }.count == 3)
    }
}

extension KnowledgeTests {
    @Test func theProfileLeavesOutWhatTheHeaderShows() {
        let md = "# 张三\n\n一起做评测。\n\n重要级 P0 · 与我相关性 95 · 评测\n\n## 项目\n- A"
        #expect(KnowledgeDetailView.profile(md, oneLine: "一起做评测。") == "## 项目\n- A")
    }
}
