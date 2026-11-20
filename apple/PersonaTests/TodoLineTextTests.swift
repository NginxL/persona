import Foundation
import Testing
@testable import Persona
import PersonaWire

/// A row's second line in words (#10465): every key of spec/fixtures/todo-view/README.md "Line keys" is said in en,
/// zh-Hans and ja, never as the raw key; times become words; a key this build does not know draws no line.
struct TodoLineTextTests {
    private static let readme = URL(fileURLWithPath: #filePath).deletingLastPathComponent().deletingLastPathComponent()
        .deletingLastPathComponent().appending(path: "spec/fixtures/todo-view/README.md")
    private let now = Date(timeIntervalSince1970: 1_800_000_000)
    private let calendar = Calendar(identifier: .gregorian)

    /// The keys of the README's "Line keys" table.
    private static func readmeKeys() throws -> Set<String> {
        let text = try String(contentsOf: readme, encoding: .utf8)
        let section = try #require(text.components(separatedBy: "### Line keys").dropFirst().first)
            .components(separatedBy: "\n## ").first ?? ""
        let row = try NSRegularExpression(pattern: #"^\| `([a-z_]+)` \|"#, options: .anchorsMatchLines)
        return Set(row.matches(in: section, range: NSRange(section.startIndex..., in: section)).map {
            String(section[Range($0.range(at: 1), in: section)!])
        })
    }

    /// Every argument a key may take, so each key's fullest sentence is said.
    private func args(_ key: String) -> [String: String] {
        let at = "2026-11-17T09:00:00Z"
        return ["error": "Desk Mac is offline", "run": "Widget", "text": "Which ryokan?", "recommend": "Ryokan Hanami",
                "default_at": at, "reason": "A reviewer to merge", "until": at, "resets_at": at, "on": "agent", "at": at,
                "by": "auto"]
    }

    @Test func everyReadmeKeyIsSaid() throws {
        let keys = try Self.readmeKeys()
        #expect(keys.count > 20, "read the README's table")
        #expect(keys == TodoLineText.keys, "the README and the app say the same keys")
    }

    @Test(arguments: ["en", "zh-Hans", "ja"]) func inEveryLanguage(_ language: String) throws {
        let words = TodoWords(locale: Locale(identifier: language))
        for key in TodoLineText.keys.sorted() {
            let said = try #require(TodoLineText.said(TodoLine(key: key, args: args(key)), since: now.addingTimeInterval(-86_400 * 2),
                                                      now: now, words: words, calendar: calendar), "\(key)")
            let text = said.spoken(separator: words.listSeparator)
            #expect(!text.isEmpty, "\(key)")
            #expect(text != key && (!text.contains(key) || !key.contains("_")), "\(key) said as its raw key: \(text)")
            #expect(!text.contains("["), "\(key) kept its catalog context: \(text)")
            #expect(!text.contains("2026-11-17"), "\(key) said a time as written: \(text)")
            // The hub's own words (its error, a reason) stay as sent; ours are in the language.
            let ours = [said.lead, said.rest].compactMap { $0 }.filter { !args(key).values.contains($0) && $0 != "Persona" && $0 != "Widget" }
            if language != "en", let first = ours.first {
                #expect(first.unicodeScalars.contains { $0.value >= 0x3040 }, "\(key) in \(language): \(first)")
            }
        }
    }

    @Test func linesHaveTheirTones() {
        let say = { (key: String, args: [String: String]) in
            TodoLineText.said(TodoLine(key: key, args: args), now: self.now, words: TodoWords(locale: Locale(identifier: "en")),
                              calendar: self.calendar)
        }
        let decision = say("decision", ["text": "Which ryokan?", "recommend": "Ryokan Hanami"])
        #expect(decision == TodoLineText.Said(lead: "Which ryokan?", rest: "Ryokan Hanami recommended", tone: .needs))
        #expect(say("failed_start", ["error": "Desk Mac is offline"])?.tone == .danger)
        #expect(say("finished", [:])?.tone == .ready)
        #expect(say("persona_working", ["text": "writing part 2 of 3"]) == TodoLineText.Said(lead: "Persona", rest: "writing part 2 of 3"))
        #expect(say("decision", ["text": "Which of 3?", "step": "Ryokan"])?.lead == "Ryokan · Which of 3?", "a step's line names it")
        #expect(say("no_such_key", [:]) == nil, "a key this build does not know is not said")
        #expect(TodoLineText.said(nil, now: now) == nil)
    }

    /// Persona's own pick says when: "on" a day, "at" a time today (proposal 3.4: "… Hanami on Fri unless you answer").
    @Test func aDefaultSaysOnADayAndAtATime() {
        var utc = Calendar(identifier: .gregorian)
        utc.timeZone = TimeZone(identifier: "UTC")!
        let say = { (at: Date) in
            TodoLineText.said(TodoLine(key: "decision_default", args: ["recommend": "Ryokan Hanami", "default_at": at.formatted(.iso8601)]),
                              now: self.now, words: TodoWords(locale: Locale(identifier: "en_US")), calendar: utc)?.rest ?? ""
        }
        let later = say(now.addingTimeInterval(86_400 * 2))
        #expect(later.hasPrefix("Persona picks Ryokan Hanami on ") && later.hasSuffix(" unless you answer"), "\(later)")
        let today = say(now.addingTimeInterval(3600))
        #expect(today.hasPrefix("Persona picks Ryokan Hanami at ") && today.contains(":"), "\(today)")
    }

    /// Absolute times become words relative to now, in the model, never in a body: today by the clock, the week by its
    /// day, further by its date.
    @Test func timesAreWords() {
        let words = TodoWords(locale: Locale(identifier: "en_US"))
        var utc = Calendar(identifier: .gregorian)
        utc.timeZone = TimeZone(identifier: "UTC")!
        let hour = TodoTimeWords.day(now.addingTimeInterval(-60), now: now, words: words, calendar: utc)
        #expect(hour.contains(":"))
        let weekday = TodoTimeWords.day(now.addingTimeInterval(-86_400 * 2), now: now, words: words, calendar: utc)
        #expect(weekday.count <= 4 && !weekday.contains(":"))
        #expect(TodoTimeWords.age(now.addingTimeInterval(-16 * 60), now: now, words: words, calendar: utc) == "16m")
        #expect(TodoTimeWords.age(now.addingTimeInterval(-3 * 3600), now: now, words: words, calendar: utc) == "3h")
        #expect(TodoTimeWords.due(now, now: now, words: words, calendar: utc) == "Today")
    }
}

/// A row's content (#10465): a line this build cannot say still draws the section's mark and word; VoiceOver hears the
/// state, the line, the door and the time in full words.
struct TodoRowContentTests {
    private let now = Date(timeIntervalSince1970: 1_800_000_000)
    private let words = TodoWords(locale: Locale(identifier: "en_US"))

    private func filed(_ id: String, _ section: String) -> TodoFiled {
        TodoFiled(id: id, manager: nil, section: section, closed: false, rank: 0, inactive: false, byView: true, quiet: false,
                  partOf: nil, project: nil, asks: [])
    }

    private func view(_ section: String, key: String, asks: [OwnerAsk] = []) -> TodoSummary {
        TodoSummary(phase: section, section: section, reason: TodoReason.agentRunning, line: TodoLine(key: key), rank: 0,
                    asks: asks)
    }

    @Test(arguments: [TodoListSection.needsYou, TodoListSection.working, TodoListSection.waiting, TodoListSection.justFinished])
    func aLineThisBuildCannotSayKeepsTheMark(_ section: String) {
        let c = TodoRowContentLogic.content(filed("a", section), item: TodoItem(id: "a", title: "A"),
                                            view: view(section, key: "computer_offline"), grouping: .phase, now: now, words: words)
        #expect(c.mark != nil, "\(section): the mark is drawn on line 2")
        #expect(c.said?.lead != nil, "\(section): the section's word in place of the line")
    }

    @Test func theRowIsSpokenInFullWords() {
        let item = TodoItem(id: "a", title: "Plan the Kyoto trip", updatedAt: now.addingTimeInterval(-16 * 60))
        let asks = [OwnerAsk(id: "todo:decision@1", kind: "plan", home: "todo"), OwnerAsk(id: "todo:decision@2", kind: "decision", home: "todo")]
        let c = TodoRowContentLogic.content(filed("a", TodoListSection.needsYou), item: item,
                                            view: view(TodoListSection.needsYou, key: "decision", asks: asks), grouping: .phase,
                                            now: now, words: words)
        #expect(c.trailing == "16m")
        #expect(c.spoken.hasPrefix("Plan the Kyoto trip, "), "\(c.spoken)")
        #expect(c.spoken.contains(StateName.todoNeedsYou), "the state its mark draws: \(c.spoken)")
        #expect(c.spoken.contains("Review Plan") && c.spoken.contains("2 asks"), "\(c.spoken)")
        #expect(c.spoken.hasSuffix("16 minutes ago"), "the time in words, never \"16m\": \(c.spoken)")
    }
}
