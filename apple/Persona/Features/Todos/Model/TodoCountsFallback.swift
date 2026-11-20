import Foundation
import PersonaWire

/// The To-dos counts worked out from filed rows (`TodoFiling`), by the hub's rules (proposal 3.7;
/// spec/fixtures/todo-counts/README.md "The rules the cases pin"): for a hub with several managers, whose counts add every
/// manager's up while the pages list the current assistant's, and for a hub that sends none.
///
/// - `open`: every row not closed, in any section but Someday.
/// - `needs_you` (N): the rows in Needs you, quiet tasks left out; Can wait is listed, not counted.
/// - `in_progress`: the rows of Working and Waiting; Just finished is not counted.
/// - `sections`: the rows of each section, every section named.
/// - A step its parent carries (its `part_of` names an open row of the same list) counts with its parent only.
nonisolated enum TodoCountsFallback {
    /// The ids of the steps their parents carry.
    static func carried(_ rows: [TodoFiled]) -> Set<String> {
        let open = Set(rows.filter { !$0.closed }.map(\.id))
        return Set(rows.filter { row in row.partOf.map { $0 != row.id && open.contains($0) } ?? false }.map(\.id))
    }

    static func counts(_ rows: [TodoFiled]) -> TodoCounts {
        var c = TodoCounts(sections: Dictionary(uniqueKeysWithValues: TodoListSection.all.map { ($0, 0) }))
        let skip = carried(rows)
        for row in rows where !skip.contains(row.id) {
            c.sections[row.section, default: 0] += 1
            if !row.closed && row.section != TodoListSection.someday { c.open += 1 }
            if row.section == TodoListSection.needsYou && !row.quiet { c.needsYou += 1 }
            if row.section == TodoListSection.working || row.section == TodoListSection.waiting { c.inProgress += 1 }
        }
        return c
    }

    /// Each project's part of N, counted the same way (a project place's `needs_you`).
    static func needsYouByProject(_ rows: [TodoFiled]) -> [String: Int] {
        let skip = carried(rows)
        var out: [String: Int] = [:]
        for row in rows where !skip.contains(row.id) && row.section == TodoListSection.needsYou && !row.quiet {
            if let project = row.project, !project.isEmpty { out[project, default: 0] += 1 }
        }
        return out
    }
}
