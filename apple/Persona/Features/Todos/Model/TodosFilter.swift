import Foundation
import PersonaWire

/// A choice in the To-dos list's Filter (#10465; docs/design/proposals/todos-redesign.md 3.3): All lands, so the list is
/// the owner's plain list with Needs you first; the others show their sections alone.
nonisolated enum TodosChoice: String, CaseIterable, Identifiable, Sendable {
    case all
    case needsYou = "needs_you"
    case inProgress = "in_progress"
    case todo
    case done

    var id: String { rawValue }

    /// The sections the choice draws, in the list's order. Can wait sits folded at the foot of Needs you, Just finished
    /// at the foot of In progress (listed, not counted), Someday folded after To do.
    var sections: [String] {
        switch self {
        case .all: TodoListSection.all
        case .needsYou: [TodoListSection.needsYou, TodoListSection.canWait]
        case .inProgress: [TodoListSection.working, TodoListSection.waiting, TodoListSection.justFinished]
        case .todo: [TodoListSection.todo, TodoListSection.someday]
        case .done: [TodoListSection.done]
        }
    }

    /// The choice's name: a menu item's, in Title Case, the words the Inbox's bar says for the same choices.
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

/// The list's Filter (DESIGN_SYSTEM 3.4 "To-dos list", "List controls"): a pop-up at the foot toolbar's leading end whose
/// menu lists the five choices, each with how many it would show as the system's subtitle. It lands on All and keeps no
/// choice; while a choice hides rows that need the owner, the list says so in its guard line. Pure: the list hands it the
/// hub's counts and its own rows.
nonisolated enum TodosFilter {
    struct Option: Equatable, Identifiable, Sendable {
        var choice: TodosChoice
        /// The quiet line under its name ("3 to-dos", "2 today"); nil for none.
        var subtitle: String?
        var id: String { choice.rawValue }
    }

    /// The five choices, always all of them, so none comes and goes while the menu is open: All with no subtitle; Needs
    /// You, In Progress (Working and Waiting) and To Do with their counts; Done with how many closed today, when any did.
    static func options(_ counts: TodoCounts, doneToday: Int, words: TodoWords = TodoWords()) -> [Option] {
        let todo = counts.sections[TodoListSection.todo] ?? 0
        return TodosChoice.allCases.map { choice in
            let subtitle: String? = switch choice {
            case .all: nil
            case .needsYou: words("\(counts.needsYou) to-dos [To-dos List]")
            case .inProgress: words("\(counts.inProgress) to-dos [To-dos List]")
            case .todo: words("\(todo) to-dos [To-dos List]")
            case .done: doneToday > 0 ? words("\(doneToday) today [To-dos Filter]") : nil
            }
            return Option(choice: choice, subtitle: subtitle)
        }
    }

    /// How many to-dos that need the owner the choice hides (the guard line's "3 more need you"): every one under In
    /// Progress, To Do and Done, none under All and Needs You.
    static func hiddenNeeds(_ choice: TodosChoice, needsYou: Int) -> Int {
        switch choice {
        case .all, .needsYou: 0
        case .inProgress, .todo, .done: needsYou
        }
    }

    /// The title's subtitle while a choice hides what needs the owner (iOS 26 and later), once the guard line has gone:
    /// the choice's name and how many ("In Progress · 3 need you"). Nothing while nothing is hidden: the Filter names
    /// its choice at the foot all the while.
    static func subtitle(_ choice: TodosChoice, needsYou: Int, words: TodoWords = TodoWords()) -> String {
        let hidden = hiddenNeeds(choice, needsYou: needsYou)
        return hidden > 0 ? words("\(choice.title) · \(hidden) need you [To-dos Filter]") : ""
    }

    /// The guard line's words for `hidden` to-dos that need the owner (`TodosGuardLine`), and the title's subtitle in a
    /// place once the line has scrolled away: naming the place when all of them are outside it (`elsewhere`), the
    /// choice hiding none of its own (#10465, DESIGN_SYSTEM 3.5 "To-dos places").
    static func guardWords(_ hidden: Int, elsewhere: Bool, place: String, words: TodoWords = TodoWords()) -> String {
        elsewhere ? words("\(hidden) more need you, not in \(place) [To-dos Places]")
            : words("\(hidden) more need you [To-dos Filter]")
    }

    /// Why ⋯'s Group by Project is greyed under a choice that draws one section or the days, or nil where it groups.
    static func groupingNote(_ choice: TodosChoice, words: TodoWords = TodoWords()) -> String? {
        switch choice {
        case .needsYou: words("Needs you is one section [To-dos View]")
        case .done: words("Done is listed by day [To-dos View]")
        case .all, .inProgress, .todo: nil
        }
    }

    /// The sections whose rows decide the calm rule: the folded ones (Someday, Done) never break it.
    static let unfolded: Set<String> = [TodoListSection.needsYou, TodoListSection.canWait, TodoListSection.working,
                                        TodoListSection.waiting, TodoListSection.todo, TodoListSection.justFinished]

    /// The calm rule (proposal 3.3): while only the owner's plain To do rows exist and nothing needs the owner, neither the
    /// Filter nor the section headers are drawn, so the list reads as a plain list; they come back once two sections have
    /// rows. `sections`: the sections that have rows; an empty list is not calm, it has its empty state.
    static func calm(_ sections: [String]) -> Bool {
        !sections.isEmpty && Set(sections).intersection(unfolded).subtracting([TodoListSection.todo]).isEmpty
    }
}
