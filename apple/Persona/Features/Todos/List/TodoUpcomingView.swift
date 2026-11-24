import Foundation
import PersonaWire
#if os(iOS)
import SwiftUI
#endif

/// Upcoming (#10465; docs/design/proposals/todos-redesign.md 3.3): the open to-dos with a due date under day headings,
/// the overdue ones first, then each day that has one, soonest first. Inside a day, by the due moment, then as the hub
/// orders them. No Filter: the days are its grouping and its order (DECISIONS 11-21 "The To-dos list"), so a sort or a
/// choice never reaches it. Pure: the list's model lays it out when the rows or the clock change (P4).
nonisolated enum TodoUpcomingLayout {
    /// The overdue group's id; each day's is "day:" and its date ("day:2026-11-20").
    static let overdue = "upcoming:overdue"
    private static let dayPrefix = "day:"

    static func layout(_ rows: [TodoFiled], items: [String: TodoItem], now: Date, calendar: Calendar,
                       words: TodoWords = TodoWords()) -> TodosListLayout.Output {
        var out = TodosListLayout.Output()
        var late: [(row: TodoFiled, by: Date)] = []
        var byDay: [Date: [(row: TodoFiled, by: Date)]] = [:]
        for row in rows {
            guard let item = items[row.id], let by = TodosLogic.dueBy(item, calendar: calendar),
                  let day = TodosPlaces.dueDay(item, calendar) else { continue }
            out.sections[TodosListLayout.key(row)] = row.section
            if by < now { late.append((row, by)) } else { byDay[day, default: []].append((row, by)) }
        }
        // By the due moment; rows due at the same one keep the hub's order (the rows come in the list's order).
        func keys(_ list: [(row: TodoFiled, by: Date)]) -> [String] {
            list.enumerated().sorted { a, b in a.element.by != b.element.by ? a.element.by < b.element.by : a.offset < b.offset }
                .map { TodosListLayout.key($0.element.row) }
        }
        if !late.isEmpty {
            out.groups.append(TodosListGroup(id: overdue, title: words("Overdue [To-dos Due]"), count: late.count,
                                             keys: keys(late)))
        }
        for day in byDay.keys.sorted() {
            let list = byDay[day] ?? []
            out.groups.append(TodosListGroup(id: dayPrefix + TodosLogic.dayKey(day, calendar: calendar),
                                             title: title(day, now: now, calendar: calendar, words: words), count: list.count,
                                             keys: keys(list)))
        }
        return out
    }

    /// A day's heading: "Today", "Tomorrow", then "Sat, Nov 21", with the year when it is not this one.
    static func title(_ day: Date, now: Date, calendar: Calendar, words: TodoWords = TodoWords()) -> String {
        if calendar.isDate(day, inSameDayAs: now) { return words("Today [To-dos Due]") }
        if let tomorrow = calendar.date(byAdding: .day, value: 1, to: now), calendar.isDate(day, inSameDayAs: tomorrow) {
            return words("Tomorrow [To-dos Due]")
        }
        var style = Date.FormatStyle(calendar: calendar, timeZone: calendar.timeZone).locale(words.formatting)
            .weekday(.abbreviated).month(.abbreviated).day()
        if calendar.component(.year, from: day) != calendar.component(.year, from: now) { style = style.year() }
        return day.formatted(style)
    }

    /// New To-Do's name where it starts due on a day ("New To-Do Due Tomorrow"): a day heading's +, and the foot's + on
    /// Today and Upcoming.
    static func newTodo(due day: String) -> String {
        String(localized: "New To-Do Due \(day) [To-dos Places]")
    }

    /// The day a heading stands for, for its +; nil for Overdue.
    static func day(_ group: String, calendar: Calendar) -> Date? {
        guard group.hasPrefix(dayPrefix) else { return nil }
        let parts = group.dropFirst(dayPrefix.count).split(separator: "-").compactMap { Int($0) }
        guard parts.count == 3 else { return nil }
        return calendar.date(from: DateComponents(year: parts[0], month: parts[1], day: parts[2]))
    }

    /// The colour a to-do takes on Upcoming's calendar (⋯ › Show as Calendar), from its view's phase: what the hub filed
    /// it as, as the list draws it, instead of what the app works out from its sessions. A row without a view takes
    /// `fallback`.
    static func calendarState(_ view: TodoSummary?, fallback: TaskState) -> TaskState {
        guard let view else { return fallback }
        if view.reason == TodoReason.failedStart { return .failed }
        switch view.phase {
        case TodoPhase.needsYou: return .needsYou
        case TodoPhase.working: return .working
        case TodoPhase.waiting: return .waiting
        case TodoPhase.ready: return .unread
        case TodoPhase.done: return .done
        default: return .notStarted
        }
    }
}

#if os(iOS)
/// A day's heading on Upcoming, as the list's other headings (`TodosSectionHeader`: the system's inset grouped header, its
/// words over the ticks): its name and how many in a plain number, and + to add a to-do due that day (proposal 3.3, "a
/// day heading's + fills it"), its hit a finger's size without making the heading taller. Overdue has no +. Spoken as a
/// heading, the + as its own button.
struct TodoUpcomingHeader: View {
    var group: TodosListGroup
    var add: (Date) -> Void

    var body: some View {
        let title = group.title ?? ""
        HStack(spacing: Theme.Space.sm) {
            HStack(spacing: Theme.Space.sm) {
                SectionHeader(title: title, count: nil, tone: StatusPalette.idle)
                Text(verbatim: String(group.count)).monospacedDigit()
            }
            .accessibilityElement(children: .ignore)
            .accessibilityLabel(title)
            .accessibilityValue(String(localized: "\(group.count) to-dos [To-dos List]"))
            .accessibilityAddTraits(.isHeader)
            .accessibilityIdentifier("todos.section." + group.id)
            Spacer(minLength: Theme.Space.sm)
            if let day = TodoUpcomingLayout.day(group.id, calendar: TodoCalendar.userCalendar) {
                Button {
                    Haptics.tap()
                    add(day)
                } label: {
                    ThemedIcon(Theme.Icon.add)
                        .font(Theme.Font.labelStrong)
                        .foregroundStyle(Theme.accent)
                        .tapTargetOutset(.all)
                }
                .buttonStyle(.plain)
                .accessibilityLabel(TodoUpcomingLayout.newTodo(due: title))
                .accessibilityIdentifier("todos.upcoming.add." + group.id)
            }
        }
        .textCase(nil)
        .lineLimit(1)
        .listRowHorizontalInsets(Theme.Space.l)
    }
}

/// What a place says with nothing in it (proposal 3.3, each place's own empty state): Today points to what comes this
/// week, a link to Upcoming, never a second filled button beside the foot's + (New To-Do, which starts the to-do in the
/// place); Upcoming says what shows up there; a project, that nothing is open in it.
struct TodoPlaceEmptyState: View {
    var place: TodoPlaceRef
    var title: String
    /// Today's "3 coming up this week".
    var comingUp: Int
    var show: (TodoPlaceRef) -> Void

    var body: some View {
        switch place.id {
        case TodoPlaceKind.today:
            EmptyStateView(symbol: Theme.Icon.todoToday, title: String(localized: "Nothing for today. [To-dos Places]"),
                           message: String(localized: "To-dos due today or overdue show up here. [To-dos Places]"),
                           action: comingUp > 0
                               ? (String(localized: "\(comingUp) coming up this week [To-dos Places]"), { show(.upcoming) }) : nil,
                           actionLook: .link)
        case TodoPlaceKind.upcoming:
            EmptyStateView(symbol: Theme.Icon.calendarView, title: String(localized: "Nothing scheduled. [To-dos Places]"),
                           message: String(localized: "To-dos with a date show up here. [To-dos Places]"))
        default:
            EmptyStateView(symbol: Theme.Icon.space, title: String(localized: "Nothing open in \(title). [To-dos Places]"),
                           message: String(localized: "To-dos you put in this project show up here. [To-dos Places]"))
        }
    }
}

/// Upcoming as a calendar (⋯ › Show as Calendar; proposal 3.3): today's to-dos calendar (`TodoCalendarView`, Day, Week,
/// Month, drag to reschedule) under its List | Calendar bar, drawing what Upcoming's list holds (open dated to-dos, not
/// Someday: no "without a due date" to go to, no Show Done; `TodosListModel.upcomingItems`, worked out once per change),
/// each coloured by the hub's view rather than by its sessions; what needs the owner elsewhere stays one line above it,
/// the list's guard line. An empty slot opens the list's New To-Do at that day and hour (`add`), saved on the device
/// first as the foot's + saves it, never the calendar's own sheet, which waits for the hub.
struct TodoUpcomingCalendar: View {
    @Binding var asCalendar: Bool
    var model: TodosListModel
    /// The place's name, for its guard line.
    var place: String
    var showAll: () -> Void
    /// New To-Do from an empty slot: its day, and its hour in Day and Week.
    var add: (TodoCalendarSlot) -> Void
    var open: (String) -> Void
    @StoredValue("todos.calendar.mode") private var mode = TodoCalendarMode.week
    @State private var anchor = Date.now

    var body: some View {
        VStack(spacing: Theme.Space.flush) {
            TodosModeBar(view: Binding(get: { asCalendar ? .calendar : .list }, set: { asCalendar = $0 == .calendar }), mode: $mode,
                         options: false)
            if model.hiddenNeeds > 0 {
                TodosGuardLine(text: TodosFilter.guardWords(model.hiddenNeeds, elsewhere: model.hiddenElsewhere, place: place)) {
                    Haptics.tap()
                    showAll()
                }
                .padding(.horizontal, Theme.Space.page)
            }
            TodoCalendarView(todos: model.upcomingItems, mode: $mode, anchor: $anchor, open: open, showList: { asCalendar = false },
                             state: { item in
                                 TodoUpcomingLayout.calendarState(TodoStore.shared.views[item.id], fallback: item.isDone ? .done : .notStarted)
                             }, add: add)
        }
    }
}
#endif
