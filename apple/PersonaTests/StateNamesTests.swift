import Foundation
import Testing
@testable import Persona

/// One set of state names on iOS, Mac, the watch and the widgets (#3960), as on the web (#3415, `state-labels.test.ts`):
/// the words are `StateName`'s, and no other catalog entry or source file defines a state name again or brings back
/// one of the old variants (「等你」 / 「在做」 / 「待查看」 / 「做完了」 / 「停着」 / 「全部标为已读」…). Reads the catalog and sources next to this
/// file, like `HoldSourceTests`. The scheme runs in zh-Hans.
struct StateNamesTests {
    private static let apple = URL(fileURLWithPath: #filePath).deletingLastPathComponent().deletingLastPathComponent()
    private static let suffix = " [State Names]"

    /// The names only an agent's or a task's state has. 「已完成」, 「未知」… also name other things (a tool run, a project, a
    /// step), so they are not checked.
    private static let names = ["需要你", "工作中", "完成未看", "已看", "未运行", "待唤醒", "待你回复", "待跟进", "刚做完待看", "全部标为已看"]
    /// Names a screen used before #3960 for one of those states.
    private static let old = ["等你", "在做", "待查看", "做完", "做完了", "做完了，还没看", "做完未看", "停着", "等你处理", "需要你处理",
                              "正在工作", "待回答", "没在运行", "全部标为已读"]
    /// A count of them: 「3 个需要你」, old or new.
    private static let count = try! NSRegularExpression(pattern:
        #"^%(1\$)?lld ?[个件]?(需要你|在等你|等你|工作中|在做|在工作|完成未看|做完未看|未看|做完了)$"#)

    /// The catalog's zh-Hans value of each key.
    private static func zh() throws -> [String: String] {
        let data = try Data(contentsOf: apple.appending(path: "Shared/Localization/Localizable.xcstrings"))
        let strings = try #require((try JSONSerialization.jsonObject(with: data) as? [String: Any])?["strings"] as? [String: Any])
        var out: [String: String] = [:]
        for (key, entry) in strings {
            let zh = ((entry as? [String: Any])?["localizations"] as? [String: Any])?["zh-Hans"] as? [String: Any]
            if let value = (zh?["stringUnit"] as? [String: Any])?["value"] as? String { out[key] = value }
        }
        return out
    }

    @Test func areTheGlossarysNames() {
        let want: [StateName: String] = [
            .needsYou: "需要你", .working: "工作中", .unread: "完成未看", .seen: "已看", .notRunning: "未运行", .unknown: "未知",
            .waiting: "待唤醒", .justFinished: "刚做完待看", .awaitsReply: "待你回复", .done: "已完成", .followUp: "待跟进", .taskWaiting: "等待中",
        ]
        for s in StateName.allCases { #expect(s.text == want[s], "\(s)") }
        #expect(StateName.count(.needsYou, 2) == "2 个需要你")
        #expect(StateName.count(.working, 3) == "3 个工作中")
        #expect(StateName.count(.unread, 1) == "1 个完成未看")
        #expect(StateName.markAllSeen() == "全部标为已看")
        #expect(StateName.markAllSeen(3) == "全部标为已看（3）")
    }

    /// The To-dos list's words for where a to-do stands (#10465, docs/design/proposals/todos-redesign.md 3.11): Working
    /// is the direction tree's word, which in Chinese is not a session's; Just finished is the one a session shares.
    @Test func areTheToDosWords() {
        #expect(StateName.todoNeedsYou == "需要你" && StateName.taskWorking == "在做" && StateName.taskWaiting.text == "等待中")
        #expect(StateName.justFinished.text == "刚做完待看" && StateName.todoToDo == "待做" && StateName.todoToDoChoice == "待做")
        #expect(StateName.todoSomeday == "以后再做" && StateName.todoInactive == "暂无进展")
    }

    /// The new to-do words are defined once, as `StateName`'s; only a frozen page that came before keeps its own entry
    /// (a setup step's value).
    @Test func theToDosWordsAreDefinedOnce() throws {
        let words: Set<String> = ["刚做完待看", "待做", "以后再做", "暂无进展"]
        let frozen: Set<String> = ["To do"]
        let again = try Self.zh().filter { key, value in
            words.contains(value) && !key.hasSuffix(Self.suffix) && !frozen.contains(key)
        }.map { "\($0.key): \($0.value)" }.sorted()
        #expect(again.isEmpty, "take these from StateName (Shared/StateNames.swift):\n\(again.joined(separator: "\n"))")
    }

    /// The screens that name a state take it from `StateName` (a sample of the sites #3960 moved).
    @MainActor @Test func screensUseThem() {
        #expect(AgentState.needsYou.label == StateName.needsYou.text && AgentState.idle.label == StateName.seen.text)
        #expect(AgentState.unread.label == "完成未看" && AgentState.offline.label == "未运行")
        #expect(TodoSection.justDone.title == StateName.justFinished.text)
        #expect(PersonaState.needsYou.title == "需要你" && SessionStatus.blocked.label == "需要你")
    }

    @Test func areDefinedOnce() throws {
        let words = Set(Self.names + Self.old)
        let again = try Self.zh().filter { key, value in
            !key.hasSuffix(Self.suffix) &&
                (words.contains(value) || Self.count.firstMatch(in: value, range: NSRange(value.startIndex..., in: value)) != nil)
        }.map { "\($0.key): \($0.value)" }.sorted()
        #expect(again.isEmpty, "take these from StateName (Shared/StateNames.swift):\n\(again.joined(separator: "\n"))")
    }

    @Test func sayMarkedSeenNotRead() throws {
        let read = try Self.zh().filter { $0.value.contains("标为已读") }.map(\.key).sorted()
        #expect(read.isEmpty, "\(read)")
    }

    /// Only `StateNames.swift` uses its keys: a screen asks `StateName`, it does not spell the key out again.
    @Test func onlyStateNamesSpellsItsKeys() {
        var found: [String] = []
        let files = FileManager.default.enumerator(at: Self.apple, includingPropertiesForKeys: nil)?.compactMap { $0 as? URL } ?? []
        for url in files where url.pathExtension == "swift" && !url.path.contains("/Packages/") && !url.path.contains("Tests/")
            && url.lastPathComponent != "StateNames.swift" {
            if (try? String(contentsOf: url, encoding: .utf8))?.contains(Self.suffix + "\"") == true { found.append(url.lastPathComponent) }
        }
        #expect(files.count > 100)
        #expect(found.isEmpty, "\(found)")
    }
}
