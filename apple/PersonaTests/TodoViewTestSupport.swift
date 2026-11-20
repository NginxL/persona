import Foundation
@testable import Persona
import PersonaWire

/// What the V1 To-dos tests (#10465) share: a store's V1 dependencies with V1 on, its cache in memory and its waits
/// short, and list rows as the hub writes them, each with its `view`.
extension TodoStore.V1 {
    @MainActor
    static func on(features: TodoFeatures = TodoFeatures(view: true, places: true), cache: TodoViewCache? = nil,
                   saved: (@MainActor (TodoViewCache?) -> Void)? = nil, api: APIClient? = nil,
                   timing: Timing = .fast) -> TodoStore.V1 {
        TodoStore.V1(isOn: { true }, features: { features }, lookup: { _ in nil }, loadCache: { cache }, saveCache: saved,
                     api: { api }, timing: timing)
    }
}

extension TodoStore.V1.Timing {
    /// Waits a test can sit through: the cache written within milliseconds, the timer read again soon after.
    static var fast: Self {
        .init(slack: 0, retries: [0.15, 0.3], floor: 0, hold: 0.3, saveFirst: .milliseconds(10), saveGap: .milliseconds(10),
              rulesTick: 0.1)
    }
}

enum TodoViewJSON {
    /// One row of `GET /api/v1/todos`: `extra` is more of the row's members ("\"manager\":\"work\""), `view` its view.
    static func row(_ id: String, title: String? = nil, extra: String = "", view: String? = nil) -> String {
        var members = [#""id":"\#(id)""#, #""title":"\#(title ?? id)""#]
        if !extra.isEmpty { members.append(extra) }
        if let view { members.append(#""view":\#(view)"#) }
        return "{" + members.joined(separator: ",") + "}"
    }

    /// A view: in `section` (its phase the same, `done` for Done), ranked `rank`, with `asks` (ids) and a line.
    static func view(_ section: String, rank: Int = 0, phase: String? = nil, asks: [String] = [], line: String = "persona_working",
                     next: String? = nil, inactive: String? = nil) -> String {
        var members = [#""phase":"\#(phase ?? section)""#, #""section":"\#(section)""#, #""reason":"rest""#,
                       #""line":{"key":"\#(line)"}"#, #""rank":\#(rank)"#]
        if !asks.isEmpty {
            members.append(#""asks":["# + asks.map { #"{"id":"\#($0)","kind":"decision","home":"todo"}"# }.joined(separator: ",") + "]")
        }
        if let next { members.append(#""next_change_at":"\#(next)""#) }
        if let inactive { members.append(#""inactive":"\#(inactive)""#) }
        return "{" + members.joined(separator: ",") + "}"
    }

    /// Counts with every section named: `needsYou` N, `inProgress`, `open`, and the sections given.
    static func counts(needsYou: Int, inProgress: Int = 0, open: Int = 0, sections: [String: Int] = [:]) -> String {
        let all = TodoListSection.all.map { #""\#($0)":\#(sections[$0] ?? 0)"# }.joined(separator: ",")
        return #"{"open":\#(open),"needs_you":\#(needsYou),"in_progress":\#(inProgress),"sections":{\#(all)}}"#
    }

    static func list(_ rows: [String], counts: String? = nil) -> String {
        #"{"todos":[\#(rows.joined(separator: ","))]"# + (counts.map { #","counts":\#($0)"# } ?? "") + "}"
    }
}
