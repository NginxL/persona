import SwiftUI

// What the sidebar, the To-dos page and the Agents page draw of the hub's direction tree (#4352, `DirectionTree`): one
// state word per to-do in one tone, and what a direction has going (its activity mark, its "N need you"). The tree
// decides both; these only draw them.

extension DirectionTree.TaskState {
    /// The word's color: Needs You's for needs you, Working's for working, Caution's for stuck and to verify (the words
    /// say which), and the quiet idle grey for not started (#5860, the web's text-ink-3).
    var tone: Color {
        switch self {
        case .needsYou: StatusPalette.needsYou.text
        case .working: StatusPalette.working.text
        case .stuck, .review: Theme.cautionText
        case .planned: StatusPalette.idle.text
        }
    }
}

/// A to-do's state as a word in its tone (`DirectionTree.TaskState`).
struct DirectionTaskStateWord: View {
    var state: DirectionTree.TaskState
    var font: Font = Theme.Font.caption

    var body: some View {
        Text(state.text)
            .font(font)
            .foregroundStyle(state.tone)
            .lineLimit(1)
    }
}

nonisolated extension DirectionTree.Counts {
    /// "N need you" (`DirectionCountsText`, the sidebar's lead rows' words); nil while nothing waits on you.
    var label: String? { DirectionCountsText.label(needsYou: needsYou) }
}

/// What a direction has going in a To-dos or Agents section header, as its sidebar row shows it (#6626): the working
/// spinner while anything under it runs (`Direction.running`), then the sidebar's words (`DirectionTree.Counts.label`);
/// nothing while neither.
struct DirectionCountsLine: View {
    var direction: DirectionTree.Direction
    var font: Font = Theme.Font.caption

    var body: some View {
        HStack(spacing: Theme.Space.xs) {
            if direction.running { WorkingIndicator() }
            if let label = direction.counts.label {
                Text(label)
                    .font(font.monospacedDigit())
                    .foregroundStyle(.secondary)
                    .lineLimit(1)
            }
        }
    }
}

/// The To-dos page grouped by Pinned direction (#4352): the hub's tree decides which to-dos are open under which
/// direction; the page draws its own rows for them.
nonisolated enum DirectionTodoSections {
    nonisolated struct Section: Identifiable, Sendable {
        var direction: DirectionTree.Direction
        var todos: [TodoItem]
        var id: String { direction.id }
    }

    /// The tree's directions in its order (the secretary's last), each with its to-dos in the tree's order: those this
    /// device does not hold, or `keep` leaves out (a search), are dropped, and a direction left with none.
    static func build(_ tree: DirectionTree, todos: [String: TodoItem], keep: (TodoItem) -> Bool = { _ in true }) -> [Section] {
        tree.withTasks.compactMap { d in
            let rows = d.tasks.compactMap { todos[$0.id] }.filter(keep)
            return rows.isEmpty ? nil : Section(direction: d, todos: rows)
        }
    }
}
