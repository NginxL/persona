import Foundation
import PersonaWire
#if os(iOS)
import SwiftUI
import UIKit

/// One row of the V1 list and what it does: drawn from its own cell and the list's model, two references that stay the
/// same, so a list update that leaves the row alone does not draw it again (P6). The link under it opens the to-do (no
/// chevron on any row, as the Agents list's and the Inbox's rows; iOS 18 draws the chevron of a link that is the row
/// whatever it is told); a new to-do opens once the hub has it. The row's confirmations are presented from the row they
/// ask about. The system's cell in its card: no row background of ours, so every shape a row takes in use is the
/// system's (`sectionCards()`).
struct TodoListCell: View {
    var cell: TodosListModel.Row
    var model: TodosListModel

    /// A row's one padding is its own (`TodoListRow`); the card's inner inset at its sides, none above or below. Set by
    /// the list on the row's outermost view (`TodoListView.row`): set here, inside the swipe and the long press, the
    /// list never saw it, and the rows kept the system's margins on every side.
    static let insets = EdgeInsets(top: Theme.Space.flush, leading: Theme.Space.l, bottom: Theme.Space.flush, trailing: Theme.Space.l)

    var body: some View {
        let c = cell.content
        TodoListRow(row: cell, model: model)
            .background {
                if !c.saving {
                    NavigationLink(value: TodoDetailRoute(id: cell.todoID)) { EmptyView() }.opacity(0)
                }
                TodoRowDialogs(model: model, key: cell.id)
            }
            .modifier(TodoRowActions(model: model, key: cell.id, row: c))
    }
}

/// "Mark it done and stop?" and "Delete …?", presented from the row they ask about (iOS 26 points its popover at it),
/// and drawn only while they ask: no row pays for an idle dialog.
private struct TodoRowDialogs: View {
    var model: TodosListModel
    var key: String

    var body: some View {
        if model.stopping == key {
            Color.clear
                .accessibleConfirmationDialog(model.stopTitle(key), isPresented: Binding(get: { model.stopping == key }, set: { shown in
                    if !shown, model.stopping == key { model.stopping = nil }
                }), titleVisibility: .visible, hasCancel: true) {
                    // It interrupts work: drawn as 4.4's busy-Agent close is.
                    Button(String(localized: "Done and Stop [To-dos Actions]"), role: .destructive) { model.close(key) }
                        .accessibilityIdentifier("todos.stop.confirm")
                    Button(String(localized: "Cancel"), role: .cancel) {}
                } message: {
                    Text(verbatim: model.item(key)?.title ?? "")
                }
        } else if model.deleting == key {
            Color.clear
                .accessibleConfirmationDialog(String(localized: "Delete “\(model.item(key)?.title ?? "")”? [To-dos Actions]"),
                                              isPresented: Binding(get: { model.deleting == key }, set: { shown in
                                                  if !shown, model.deleting == key { model.deleting = nil }
                                              }), titleVisibility: .visible, hasCancel: true) {
                    Button(String(localized: "Delete To-Do [To-dos Actions]"), role: .destructive) { model.delete(key) }
                        .accessibilityIdentifier("todos.delete.confirm")
                    Button(String(localized: "Cancel"), role: .cancel) {}
                } message: {
                    Text("It's removed from your to-dos on every device. Work already under way on it keeps going. [To-dos Actions]")
                }
        }
    }
}

/// What a To-dos row offers besides opening it (#10465; docs/design/proposals/todos-redesign.md 3.4, "Swipe"; DESIGN_SYSTEM
/// 3.4 "To-dos list"): one trailing swipe, Done (the full swipe) and Someday, as the Inbox swipes; and the long-press
/// menu: Today, Date…, Project, Someday, Copy Link and Delete…. Which of them a row has is `TodoRowActionList`'s.
struct TodoRowActions: ViewModifier {
    var model: TodosListModel
    var key: String
    var row: TodoRowContent

    func body(content: Content) -> some View {
        content
            .swipeActions(edge: .trailing, allowsFullSwipe: true) {
                ForEach(TodoRowActionList.swipe(row), id: \.self) { action in
                    button(action).tint(Self.tint(action)).accessibilityIdentifier("todos.swipe." + action.rawValue)
                }
            }
            .contextMenu { menu }
    }

    @ViewBuilder private var menu: some View {
        let actions = TodoRowActionList.menu(row)
        let first = actions.filter { [.undo, .reopen, .tryAgain, .remove].contains($0) }
        let plan = actions.filter { [.today, .date, .project, .someday, .outOfSomeday].contains($0) }
        if !first.isEmpty {
            Section {
                ForEach(first, id: \.self) { action in
                    button(action).accessibilityIdentifier("todos.menu." + action.rawValue)
                }
            }
        }
        if !plan.isEmpty {
            Section {
                ForEach(plan, id: \.self) { action in
                    if action == .project {
                        TodoProjectMenu(model: model, key: key, current: model.item(key)?.project ?? "")
                    } else {
                        button(action).accessibilityIdentifier("todos.menu." + action.rawValue)
                    }
                }
            }
        }
        if actions.contains(.copyLink) {
            Section { button(.copyLink).accessibilityIdentifier("todos.menu.copyLink") }
        }
        if actions.contains(.delete) {
            Section { button(.delete).accessibilityIdentifier("todos.menu.delete") }
        }
    }

    private func button(_ action: TodoRowAction) -> some View {
        Button(role: action == .delete ? .destructive : nil) { Self.run(action, key, model) } label: {
            Label(Self.title(action), systemImage: Self.icon(action))
        }
    }

    static func run(_ action: TodoRowAction, _ key: String, _ model: TodosListModel) {
        switch action {
        case .done, .undo, .reopen: model.tapTick(key)
        case .someday: model.someday(key)
        case .outOfSomeday: model.outOfSomeday(key)
        case .tryAgain: model.retry(key)
        case .remove: model.removeUnsure(key)
        case .today: model.setToday(key)
        case .date: model.pickDate(key)
        case .project: break // a submenu of its own
        case .copyLink: copyLink(model.item(key)?.id)
        case .delete: model.askDelete(key)
        }
    }

    static func title(_ action: TodoRowAction) -> String {
        switch action {
        case .done: String(localized: "Done [To-dos Actions]")
        case .someday: StateName.todoSomeday
        case .outOfSomeday: String(localized: "Remove from Someday [To-dos Actions]")
        case .undo: String(localized: "Undo")
        case .reopen: String(localized: "Reopen [To-dos Actions]")
        case .tryAgain: String(localized: "Try Again")
        case .remove: String(localized: "Remove [To-dos Actions]")
        case .today: String(localized: "Today [To-dos Actions]")
        case .date: String(localized: "Date… [To-dos Actions]")
        case .project: String(localized: "Project [To-dos Actions]")
        case .copyLink: String(localized: "Copy Link [To-dos Actions]")
        case .delete: String(localized: "Delete… [To-dos Actions]")
        }
    }

    private static func icon(_ action: TodoRowAction) -> String {
        switch action {
        case .done: Theme.Icon.done
        case .someday: Theme.Icon.someday
        case .outOfSomeday, .undo, .reopen: Theme.Icon.undo
        case .tryAgain: Theme.Icon.retry
        case .remove, .delete: Theme.Icon.delete
        case .today: Theme.Icon.today
        case .date: Theme.Icon.dueDate
        case .project: Theme.Icon.space
        case .copyLink: Theme.Icon.link
        }
    }

    private static func tint(_ action: TodoRowAction) -> Color {
        switch action {
        case .done: Theme.positive
        case .someday: Theme.somedayTint
        case .remove, .delete: Theme.danger
        default: Theme.accent
        }
    }

    /// The to-do's web link on the hub-level group's address its links are written with, else the one this device uses.
    private static func copyLink(_ id: String?) {
        let app = AppModel.shared
        guard let id, let link = app.hub, let url = TodoLink.web(id, on: link.hello.publicURL ?? link.config.url) else { return }
        UIPasteboard.general.url = url
        Haptics.success()
        app.toast = Toast(String(localized: "Copied [To-dos Toast]"), style: .success)
    }
}

/// Project in a row's menu: the knowledge base's projects to file it under, or none. Reads the projects itself, so only
/// an open menu follows them (P5).
private struct TodoProjectMenu: View {
    var model: TodosListModel
    var key: String
    var current: String

    var body: some View {
        Menu {
            Picker(selection: Binding(get: { current }, set: { model.setProject(key, $0) })) {
                Text("No Project [To-dos Actions]").tag("")
                ForEach(TodoProjectLogic.choices(TodoProjects.shared.list, current: current.isEmpty ? nil : current)) { p in
                    Text(p.name).tag(p.slug)
                }
            } label: {
                Text("Project [To-dos Actions]")
            }
            .pickerStyle(.inline)
        } label: {
            Label(String(localized: "Project [To-dos Actions]"), systemImage: Theme.Icon.space)
        }
        .accessibilityIdentifier("todos.menu.project")
    }
}

/// The New To-Do sheet: a title and notes, and Add. It closes at once: the to-do is listed while it is saved, and goes
/// to the hub with its op id when it can (`TodosListModel.create`). `draft`: what a new to-do the hub turned down said.
/// Made in a place (`capture`, proposal 3.3 "Capture default"), it says where it starts: its project under the title
/// ("In Kyoto trip"), or its due date, which the owner can change and give a time (Today's today, Upcoming's tomorrow or
/// the day whose + was tapped; an hour tapped on Upcoming's calendar starts with that time, All Day off).
struct TodoNewSheet: View {
    var capture: TodoCapture
    /// The project's name, for `capture`'s project.
    var projectName: String?
    var add: (_ title: String, _ notes: String?, _ due: APIClient.TodoPlanChange.Due?) -> Void
    @Environment(\.dismiss) private var dismiss
    @State private var title: String
    @State private var notes: String
    @State private var due: Date
    @State private var allDay: Bool
    /// The notes' mic, made as the sheet appears (P3: a `@State` initial value does nothing).
    @State private var dictation: Dictation?
    @FocusState private var focused: Bool

    init(draft: TodoOutbox.Draft? = nil, capture: TodoCapture = TodoCapture(), projectName: String? = nil,
         add: @escaping (_ title: String, _ notes: String?, _ due: APIClient.TodoPlanChange.Due?) -> Void) {
        self.capture = capture
        self.projectName = projectName
        self.add = add
        _title = State(initialValue: draft?.title ?? "")
        _notes = State(initialValue: draft?.notes ?? "")
        // The time it gets if All Day is turned off: the calendar's for a new to-do (`TodoQuickAddSheet`), or the one given.
        let day = capture.day ?? .distantFuture
        _allDay = State(initialValue: capture.at == nil)
        _due = State(initialValue: capture.at
            ?? TodoCalendar.userCalendar.date(bySettingHour: TodoCalendar.morning + 1, minute: 0, second: 0, of: day) ?? day)
    }

    private var ready: Bool { !title.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty }

    var body: some View {
        NavigationStack {
            Form {
                if let projectName, capture.project != nil {
                    Section { fields } footer: { Text("In \(projectName) [To-dos Places]") }
                } else {
                    Section { fields }
                }
                if capture.day != nil {
                    Section {
                        FormDatePicker("Due", selection: $due, displayedComponents: allDay ? [.date] : [.date, .hourAndMinute],
                                       identifier: "todos.new.due")
                        Toggle(String(localized: "All Day"), isOn: $allDay)
                            .accessibilityIdentifier("todos.new.allDay")
                    }
                }
            }
            .themedPage()
            .onDisappear { dictation?.stop() } // cancelled, swiped away or added
            .navigationTitle(String(localized: "New To-Do [To-dos Actions]"))
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                SheetClose { dismiss() }
                SheetConfirm(id: "todos.new.add", name: String(localized: "Add [To-dos New]"), disabled: !ready, action: save)
            }
            .onAppear {
                if dictation == nil { dictation = Dictation() }
                focused = true
            }
        }
        .presentationDetents([.medium])
    }

    @ViewBuilder private var fields: some View {
        TextField(String(localized: "Title [To-dos New]"), text: $title)
            .lineLimit(1)
            .focused($focused)
            .submitLabel(.done)
            .onSubmit(save)
            .accessibilityIdentifier("todos.new.title")
        HStack(alignment: .top, spacing: Theme.Space.s) {
            TextField(String(localized: "Notes [To-dos New]"), text: $notes, axis: .vertical)
                .lineLimit(3...8)
                .accessibilityIdentifier("todos.new.notes")
            if let dictation {
                FieldMicButton(dictation: dictation, text: $notes, id: "todos.new.notes.mic")
                    .font(Theme.Font.body)
                    .foregroundStyle(Theme.accent)
            }
        }
    }

    private func save() {
        guard ready else { return }
        dictation?.stop() // what is saved is what the field shows
        let when: APIClient.TodoPlanChange.Due? = capture.day == nil ? nil
            : allDay ? .day(TodoPlanText.day(due, calendar: TodoCalendar.userCalendar)) : .at(due)
        add(title, notes, when)
        dismiss()
    }
}

/// Date… on a row: when it is due, as the calendar's Change Due Date sets it (`TodoRescheduleSheet`: a day, or a day
/// and a time with All Day off), or no date.
struct TodoDueSheet: View {
    var item: TodoItem
    var save: (_ due: APIClient.TodoPlanChange.Due) -> Void
    @Environment(\.dismiss) private var dismiss
    @State private var allDay: Bool
    @State private var due: Date

    init(item: TodoItem, save: @escaping (_ due: APIClient.TodoPlanChange.Due) -> Void) {
        self.item = item
        self.save = save
        // As the to-do's page starts its due date: an all-day one at 18:00 should it become a moment.
        let draft = TodoPlanDraft(item)
        _allDay = State(initialValue: draft.allDay)
        _due = State(initialValue: draft.due)
    }

    private var dated: Bool { item.dueAt != nil || item.dueDate != nil }

    var body: some View {
        NavigationStack {
            Form {
                Section {
                    FormDatePicker("Due", selection: $due, displayedComponents: allDay ? [.date] : [.date, .hourAndMinute],
                                   identifier: "todos.due.day")
                    Toggle(String(localized: "All Day"), isOn: $allDay)
                        .accessibilityIdentifier("todos.due.allDay")
                } header: {
                    Text(verbatim: item.title)
                        .lineLimit(1)
                }
                if dated {
                    Section {
                        Button(role: .destructive) {
                            save(.none)
                            dismiss()
                        } label: {
                            Text("Remove Date [To-dos Due Sheet]")
                        }
                        .accessibilityIdentifier("todos.due.remove")
                    }
                }
            }
            .themedPage()
            .navigationTitle(String(localized: "Change Due Date"))
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                SheetClose { dismiss() }
                SheetConfirm(id: "todos.due.save", name: String(localized: "Save")) {
                    save(allDay ? .day(TodoPlanText.day(due)) : .at(due))
                    dismiss()
                }
            }
        }
        .formSheetDetents([.medium])
    }
}
#endif

// The pure parts, on every platform: the list's model says the offline line.

/// A to-do's web link (docs/protocol/deep-links.md "Route table": `/todos/<id>`) on the hub's address its links are
/// written with (its `public_url`, else the one this device uses), so it opens in a browser too.
nonisolated enum TodoLink {
    static func web(_ id: String, on hub: URL) -> URL? {
        var base = hub.absoluteString
        while base.hasSuffix("/") { base.removeLast() }
        let path: String = "/todos/" + APIClient.todoPath(id)
        return URL(string: base + path)
    }
}

/// The list's one line while the hub is out of reach (proposal 3.4, "Offline"): "Can't reach your hub · showing what
/// was saved 10:14", the time the hub last answered a read of the list (another day's with its day); no time when it
/// never answered on this device.
nonisolated enum TodoOfflineLine {
    static func text(readAt: Date?, now: Date, words: TodoWords = TodoWords(), calendar: Calendar = .current) -> String {
        guard let at = readAt else { return words("Can't reach your hub · showing what was saved [To-dos Offline]") }
        var style = Date.FormatStyle(calendar: calendar, timeZone: calendar.timeZone).locale(words.formatting).hour().minute()
        if !calendar.isDate(at, inSameDayAs: now) {
            let days = abs(calendar.dateComponents([.day], from: calendar.startOfDay(for: at), to: calendar.startOfDay(for: now)).day ?? 99)
            style = days <= 6 ? style.weekday(.abbreviated) : style.month(.abbreviated).day()
        }
        return words("Can't reach your hub · showing what was saved \(at.formatted(style)) [To-dos Offline]")
    }
}
