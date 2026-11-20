import Foundation
import PersonaWire

/// One to-do as the V1 list files it (#10465): its section, its place in it, and the facts its counts read.
nonisolated struct TodoFiled: Equatable, Sendable {
    var id: String
    var manager: String?
    /// One of `TodoListSection.all`.
    var section: String
    /// Closed: the view's phase is done (a failed start still waiting on the owner is open).
    var closed: Bool
    /// Its place in its section, 0 first: the view's rank, or for a row filed by the app's own rules its place by when it
    /// last moved among that manager's rows filed so.
    var rank: Int
    /// The Inactive mark: nothing has moved on it for 48 hours.
    var inactive: Bool
    /// Filed from the hub's view; false when the app's own rules filed it (an older manager's row, or a view this build
    /// cannot read).
    var byView: Bool
    var quiet: Bool
    var partOf: String?
    var project: String?
    /// The ids of the asks that hold it in Needs you or Can wait (the view's, the most urgent first); none for a row the
    /// app's rules filed.
    var asks: [String]
}

/// Where the V1 list files each to-do (#10465, docs/apple/ios.md "Filing and counts"): by its `view` when it has one whose section this build
/// knows, else by the app's own rules (`TodoTriageLogic`, the frozen fallback for rows from older managers) mapped
/// onto the V1 sections. The only new reader of `TodoTriageLogic` (proposal 7.7). Pure: no store, no clock of its own.
nonisolated enum TodoFiling {
    /// Whether this build can file a row by `view`: its section is one it knows (the hub's enums are open strings).
    static func knows(_ view: TodoSummary) -> Bool { TodoListSection.all.contains(view.section) }

    /// The V1 section of a section of the app's own rules: an open pull request waits on someone else; Other open is To
    /// do; Shelved is To do with the Inactive mark.
    static func section(legacy: TodoSection) -> (section: String, inactive: Bool) {
        switch legacy {
        case .waiting: (TodoListSection.needsYou, false)
        case .working: (TodoListSection.working, false)
        case .pr: (TodoListSection.waiting, false)
        case .other: (TodoListSection.todo, false)
        case .justDone: (TodoListSection.justFinished, false)
        case .shelved: (TodoListSection.todo, true)
        case .done: (TodoListSection.done, false)
        }
    }

    /// Every row of `rows` filed, in the order given. `lookup` (the sessions of the rows' Agents) and `restored` (the
    /// restores kept on this device, `TodoRestores`) are read only when a row has no view this build can file by.
    static func file(_ rows: [TodoItem], views: [String: TodoSummary], now: Date,
                     lookup: () -> TodosLogic.SessionLookup? = { nil }, restored: [String: Date] = [:],
                     calendar: Calendar = .current) -> [TodoFiled] {
        var out: [TodoFiled] = []
        out.reserveCapacity(rows.count)
        var legacy: [(index: Int, last: Date)] = []
        for row in rows {
            if let view = views[row.id], knows(view) {
                out.append(TodoFiled(id: row.id, manager: row.manager, section: view.section, closed: view.phase == TodoPhase.done,
                                     rank: view.rank, inactive: !(view.inactive ?? "").isEmpty, byView: true, quiet: row.quiet,
                                     partOf: row.partOf, project: row.project, asks: (view.asks ?? []).map(\.id)))
            } else {
                legacy.append((out.count, .distantPast))
                out.append(TodoFiled(id: row.id, manager: row.manager, section: TodoListSection.todo, closed: false, rank: 0,
                                     inactive: false, byView: false, quiet: row.quiet, partOf: row.partOf, project: row.project,
                                     asks: []))
            }
        }
        guard !legacy.isEmpty else { return out }
        let sessions = lookup()
        let superseded = TodoTriageLogic.supersededFailedStarts(rows)
        for (n, entry) in legacy.enumerated() {
            let row = rows[entry.index]
            let back = [row.restoredAt, restored[row.id]].compactMap { $0 }.max()
            let t = TodoTriageLogic.triage(row, lookup: sessions, now: now, restoredAt: back,
                                           superseded: superseded.contains(row.id), calendar: calendar)
            let placed = Self.section(legacy: t.section)
            out[entry.index].section = placed.section
            out[entry.index].inactive = placed.inactive
            out[entry.index].closed = !TodoTriageLogic.checksAsOpen(row, t)
            legacy[n].last = t.last
        }
        // Rows the app's rules filed have no rank: by when they last moved, newest first, within each manager's section,
        // so a merged list orders them with the hub's ranks as it orders two managers' rows.
        let groups = Dictionary(grouping: legacy) { "\(out[$0.index].manager ?? "")\n\(out[$0.index].section)" }
        for group in groups.values {
            let sorted = group.sorted { $0.last != $1.last ? $0.last > $1.last : out[$0.index].id < out[$1.index].id }
            for (rank, entry) in sorted.enumerated() { out[entry.index].rank = rank }
        }
        return out
    }

    /// `rows` in the list's order: by section (proposal 3.4), then rank, then manager, then id: ranks are per manager, so
    /// a merged section can hold equal ones (docs/protocol/protocol.md, the `GET /api/v1/todos` row).
    static func ordered(_ rows: [TodoFiled]) -> [TodoFiled] {
        let order = Dictionary(uniqueKeysWithValues: TodoListSection.all.enumerated().map { ($0.element, $0.offset) })
        return rows.sorted { a, b in
            let sa = order[a.section] ?? order.count, sb = order[b.section] ?? order.count
            if sa != sb { return sa < sb }
            if a.rank != b.rank { return a.rank < b.rank }
            if (a.manager ?? "") != (b.manager ?? "") { return (a.manager ?? "") < (b.manager ?? "") }
            return a.id < b.id
        }
    }
}
