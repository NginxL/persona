import Foundation
import PersonaWire

/// A choice on the To-dos list's section bar (#10465; docs/design/proposals/todos-redesign.md 3.3): All lands, so the
/// list is the owner's plain list with Needs you first; the others show their sections alone.
nonisolated enum TodosChoice: String, CaseIterable, Identifiable, Sendable {
    case all
    case needsYou = "needs_you"
    case inProgress = "in_progress"
    case todo
    case done

    var id: String { rawValue }

    /// The sections the choice draws, in the list's order. Can wait sits folded at the foot of Needs you, Just finished
    /// at the foot of In progress (listed, not counted), Someday folded at the foot of To do.
    var sections: [String] {
        switch self {
        case .all: TodoListSection.all
        case .needsYou: [TodoListSection.needsYou, TodoListSection.canWait]
        case .inProgress: [TodoListSection.working, TodoListSection.waiting, TodoListSection.justFinished]
        case .todo: [TodoListSection.todo, TodoListSection.someday]
        case .done: [TodoListSection.done]
        }
    }

    /// The choice's name: a button's, in Title Case, the words the Inbox's bar says for the same choices.
    var title: String {
        switch self {
        case .all: String(localized: "All")
        case .needsYou: StateName.needsYou.text
        case .inProgress: String(localized: "In Progress")
        case .todo: StateName.todoToDoChoice
        case .done: StateName.done.text
        }
    }
}

/// The section bar as the Inbox's (`InboxBar`, DESIGN_SYSTEM 3.4 "List controls"), with To-dos' differences: it lands on
/// All, its first choice; To do takes FYI's place; and the calm rule hides it. Pure: the list hands it the hub's counts
/// and its own rows.
nonisolated enum TodosBar {
    enum Look: Equatable, Sendable { case needs, neutral }

    struct Entry: Equatable, Identifiable, Sendable {
        var choice: TodosChoice
        /// nil: none drawn; 0 stays in the data and the badge hides it.
        var count: Int?
        var look: Look?
        var id: String { choice.rawValue }
    }

    /// The choices in order. All and Needs you always, Needs you with the blue N; In progress (Working and Waiting) and
    /// To do with their neutral counts while they have rows or are chosen; Done while it has rows or is chosen. Every
    /// count stays at every width: a strip that does not fit scrolls (DESIGN_SYSTEM 3.4, "every filter shows its whole
    /// name").
    static func entries(_ counts: TodoCounts, doneRows: Int, chosen: TodosChoice) -> [Entry] {
        var out = [Entry(choice: .all), Entry(choice: .needsYou, count: counts.needsYou, look: .needs)]
        if counts.inProgress > 0 || chosen == .inProgress {
            out.append(Entry(choice: .inProgress, count: counts.inProgress, look: .neutral))
        }
        let todo = counts.sections[TodoListSection.todo] ?? 0
        if todo > 0 || chosen == .todo { out.append(Entry(choice: .todo, count: todo, look: .neutral)) }
        if doneRows > 0 || chosen == .done { out.append(Entry(choice: .done)) }
        return out
    }

    /// The sections whose rows decide the calm rule: the folded ones (Someday, Done) never break it.
    static let unfolded: Set<String> = [TodoListSection.needsYou, TodoListSection.canWait, TodoListSection.working,
                                        TodoListSection.waiting, TodoListSection.todo, TodoListSection.justFinished]

    /// The calm rule (proposal 3.3): while only the owner's plain To do rows exist and nothing needs the owner, neither the
    /// bar nor the section headers are drawn, so the list reads as a plain list; they come back once two sections have
    /// rows. `sections`: the sections that have rows; an empty list is not calm, it has its empty state.
    static func calm(_ sections: [String]) -> Bool {
        !sections.isEmpty && Set(sections).intersection(unfolded).subtracting([TodoListSection.todo]).isEmpty
    }
}
