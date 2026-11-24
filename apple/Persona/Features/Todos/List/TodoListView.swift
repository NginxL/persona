#if os(iOS)
import PersonaWire
import SwiftUI

extension TodosListDeps {
    /// The app's: the shared store and outbox, the hub, the knowledge base's projects and the app's toast.
    @MainActor static var live: TodosListDeps {
        let app = AppModel.shared
        return TodosListDeps(store: TodoStore.shared, outbox: TodoOutbox.shared, api: { app.api }, now: { Date() },
                             calendar: { TodoCalendar.userCalendar }, projects: { TodoProjects.shared.list },
                             toast: { app.toast = $0 }, show: { app.show($0) },
                             assistive: { UIAccessibility.isVoiceOverRunning || UIAccessibility.isSwitchControlRunning })
    }
}

/// The V1 To-dos list (#10465; docs/design/proposals/todos-redesign.md 3.3, 3.4; DESIGN_SYSTEM 3.4 "To-dos list"): nothing
/// under the title; the sections in the hub's order, each one card (`sectionCards()`), with Can wait folded inside
/// Needs you's and Someday and Done today folded after the last; the rows (`TodoListRow`). The foot toolbar, Mail's,
/// holds the Filter, the system's search field and New To-Do (`TodosFootBar`); ⋯ holds Sort By and Group by Project
/// (`TodosViewMenu`). While a choice hides what needs the owner, the list's first line says so
/// (`TodosGuardLine`). The calm rule hides the Filter and the headers while only plain To do rows exist. A row ticks,
/// swipes (Done, Someday) and has a long-press menu (`TodoRowActions`); the writes wait out their Undo on the device
/// (`TodoOutbox`). While the hub is out of reach the list is the one last read, under one line that says so. Rows open
/// today's to-do page until the to-do's own page arrives. iOS only: visionOS keeps today's To-dos.
///
/// It shows one place (`TodosPlacesModel`, DESIGN_SYSTEM 3.5 "To-dos places"), picked in the sidebar or from its title's
/// menu: All To-dos and a project's place draw the Filter, Today and Upcoming do not; Upcoming is by day, its ⋯ holding
/// Show as Calendar alone. In a place, the guard line also counts what needs the owner outside it. New To-Do starts the
/// to-do in the place shown (`TodosListModel.capture`), as Upcoming's day headings do.
struct TodoListView: View {
    @Environment(AppModel.self) private var app
    @Environment(\.shownTree) private var shownTree
    @Environment(\.builtTree) private var builtTree
    @State private var model = TodosListModel(deps: .live)
    @StoredValue("todos.v1.sort") private var sort = TodosListSort.suggested
    @StoredValue("todos.v1.byProject") private var byProject = false
    /// The rows in sight, for where the list rests (not observed: scrolling redraws nothing).
    @State private var sight = TodoListSight()
    /// Whether the guard line has scrolled away: the title's subtitle takes its place then.
    @State private var lines = TodosTopLinesSight()
    /// The place this page last put its list back for (where it was left, or its top): once per place, as the page is
    /// made or another place is picked. A list kept in its navigation stack keeps its own place when a to-do pushed over
    /// it pops.
    @State private var placedIn: String?
    /// New To-Do's sheet, with where it starts (the row's confirmations and Date… are the model's, presented from their
    /// row).
    @State private var creating: TodoCapture?
    /// The words "Search Chats for …" took to the app's search, while it is up.
    @State private var chatSearch: TodosChatSearch?
    /// The chat picked there, opened once the search is gone.
    @State private var pickedChat: SessionKey?
    private let workspace = TodoWorkspace.shared
    /// The places: the title's menu, and the place the list shows (`model.place`) and its name, which the page takes
    /// from them only when they change (`TodoPlaceFollower`), so a place's number changing redraws nothing here (P5).
    @State private var places = TodosPlacesModel(deps: .live)
    @State private var placeTitle: String?
    /// Upcoming drawn as a calendar (⋯ › Show as Calendar), kept on the device.
    @StoredValue("todos.v1.upcomingCalendar") private var upcomingCalendar = false
    @State private var allProjects = false
    /// A to-do opened from Upcoming's calendar.
    @State private var pushedTodo: TodoDetailRoute?

    /// Whether this page's tree is the one the window shows: a tree on its way out leaves the workspace alone.
    private var shownHere: Bool { builtTree == nil || shownTree?.tree === builtTree }

    /// The place's name: the page's title.
    private var title: String { placeTitle ?? String(localized: "All To-dos [To-dos List]") }

    var body: some View {
        Group {
            // A search lists what matches as a list, whatever the place draws.
            if model.place == .upcoming && upcomingCalendar && !model.searching {
                TodoUpcomingCalendar(asCalendar: $upcomingCalendar, model: model, place: title, showAll: { places.show(.all) },
                                     add: { creating = model.capture(on: $0.day, at: $0.time) }) {
                    pushedTodo = TodoDetailRoute(id: $0)
                }
            } else {
                list
            }
        }
        .background {
            TodoPlaceFollower(places: places, allProjects: { allProjects = true }) { place, title in
                if model.place != place { model.place = place }
                if placeTitle != title { placeTitle = title }
            }
        }
        // The model follows the store from the page's first appearance, whichever it draws: Upcoming's calendar says
        // what needs the owner elsewhere (`hiddenNeeds`) from the model too. The list says when it is in sight (its hold).
        .onAppear {
            model.sort = sort
            model.byProject = byProject
            model.place = places.current
            model.start()
        }
        .detailTitle(title)
        .navigationBarTitleDisplayMode(.inline)
        .modifier(TodosSubtitle(model: model, lines: lines, place: title))
        .modifier(TodosViewOptions(model: model, place: title, asCalendar: $upcomingCalendar))
        .pageSearchable(text: $model.query, placement: Self.searchPlacement, prompt: String(localized: "Search To-dos [To-dos Search]"))
        // The search narrows the list or gives it back: it no longer rests where a scroll left it.
        .onChange(of: model.query) { _, _ in sight.moved() }
        .modifier(TodosFootBar(model: model, place: title, creating: $creating))
        .sheet(item: Binding(get: { model.dating.flatMap { key in model.item(key).map { DueTarget(key: key, item: $0) } } },
                             set: { model.dating = $0?.key })) { target in
            TodoDueSheet(item: target.item) { due in model.setDue(target.key, to: due) }
        }
        .sheet(item: $creating) { capture in
            // A new to-do the hub turned down comes back with what it said (its toast's Edit).
            TodoNewSheet(draft: model.draft, capture: capture, projectName: capture.project.map { places.title(.project($0)) }) {
                model.create(title: $0, notes: $1, due: $2, project: capture.project)
            }
            .onAppear { model.tookDraft() }
        }
        // The app's search over the window, Persona's chats only (no Agents on an everyday page), with the words typed.
        .fullScreenCover(item: $chatSearch, onDismiss: openPickedChat) { search in
            AssistantFullSearch(query: search.words) { key in
                pickedChat = key
                chatSearch = nil
            }
        }
        .onChange(of: model.draft) { _, draft in if let draft { creating = model.capture(draft) } }
        // The arrangement is kept on this device, as ⋯ left it.
        .onChange(of: model.sort) { _, s in sort = s }
        .onChange(of: model.byProject) { _, on in byProject = on }
        // Each minute while the page is in front, and as it comes back to the front: the rows' times, and what the clock
        // moves (Done today, the due days).
        .tabActiveTask {
            while !Task.isCancelled {
                model.tick()
                try? await Task.sleep(for: .seconds(60))
            }
        }
        // The projects' names, read when the page opens and for the assistant made current.
        .task(id: app.assistantSwitches) { await TodoProjects.shared.load(app.api?.currentAssistantSettings()) }
        .task { await model.refresh() }
        .navigationDestination(item: $pushedTodo) { r in TodoDetailView(todoID: r.id) }
        .sheet(isPresented: $allProjects) {
            TodoAllProjectsSheet(places: places) { place in
                allProjects = false
                places.show(place)
            }
        }
    }

    private var list: some View {
        ScrollViewReader { proxy in
            List {
                if model.ready {
                    TodosTopLines(model: model, sight: lines, place: title) { places.show(.all) }
                    content
                    TodosSearchChatsRow(model: model) { chatSearch = TodosChatSearch(words: $0) }
                }
            }
            .sectionCards()
            .accessibilityIdentifier("todos.v1") // the list itself
            .themedPage()
            .refreshable { await model.refresh() }
            .onScrollGeometryChange(for: TodoListEdges.self) { TodoListEdges($0) } action: { _, edges in
                sight.covered = edges.top
                sight.atEnd = edges.atEnd
                lines.covered = edges.top
            }
            .onScrollPhaseChange { old, phase in
                model.hold.set(.scrolling, phase != .idle)
                sight.scrolling = phase != .idle
                if phase != .idle { model.settleArrivals() } // nothing fades in under a finger
                // Where a scroll came to rest; not the idle a page is made in, before its rows are drawn.
                if phase == .idle, old != .idle {
                    sight.rested()
                    if let top = sight.top(in: model.groups) { workspace.rested(model.place.id, at: top, shown: shownHere) }
                }
            }
            .onAppear {
                model.hold.set(.outOfSight, false)
                // Back from a to-do pushed over it: a list that rested at its very end is at its end again. The to-do
                // page has no foot toolbar, so while it is in front the list's foot inset is smaller, and a list at its
                // end is held to that shorter end; once the toolbar is back it would sit lower than it rested.
                if placedIn != nil, sight.restedAtEnd { backToTheEnd(proxy) }
            }
            .onDisappear { model.hold.set(.outOfSight, true) }
            // A choice re-files the list from its top, once the list has the new rows. Its guard line starts in sight, so
            // the subtitle waits until it scrolls away again.
            .onChange(of: model.choice) { _, _ in
                sight.moved()
                lines.guardGone = false
                toTop(proxy)
            }
            // Another place is another list: where the last one rested no longer holds (the new place's own is put back
            // below), and its guard line starts in sight.
            .onChange(of: model.place) { _, _ in
                sight.moved()
                lines.guardGone = false
            }
            // Back where the place's list last rested, once its rows are laid out: only for a page made anew (another
            // page opened in between) or another place picked, never when a to-do pushed over it pops. A place picked
            // with nothing kept starts at its top.
            .task(id: PlacedKey(ready: model.ready, place: model.place.id)) {
                let place = model.place.id
                guard model.ready, placedIn != place else { return }
                let switched = placedIn != nil
                guard let key = workspace.scrolled(in: place), model.draws(key) else {
                    placedIn = place
                    if switched { toTop(proxy) }
                    return
                }
                // Once the list has drawn its first rows under its bars (a second at most): until then it has nothing to
                // scroll to, and a bar laid out after moves it again. Once more if a late layout moved it all the same;
                // a page that went meanwhile tries again as it comes back.
                for _ in 0..<20 where (sight.visible.isEmpty || sight.covered == 0) && !Task.isCancelled {
                    try? await Task.sleep(for: .milliseconds(50))
                }
                guard !Task.isCancelled else { return }
                proxy.scrollTo(key, anchor: .top)
                try? await Task.sleep(for: .milliseconds(150))
                guard !Task.isCancelled else { return }
                if sight.top(in: model.groups) != key { proxy.scrollTo(key, anchor: .top) }
                placedIn = place
            }
        }
    }

    private struct PlacedKey: Equatable {
        var ready: Bool
        var place: String
    }

    /// The list from its top, once it has its new rows (a choice, another place): its first line or row is put at the
    /// foot of what is in sight, which the list cannot scroll that far up for, so it stops at its top, the first heading
    /// in sight (a heading's own id puts its rows at the top, the heading under the bar).
    private func toTop(_ proxy: ScrollViewProxy) {
        let target = TodosTopLines.first(offline: TodosTopLines.offline(app), hidden: model.hiddenNeeds)
            ?? model.groups.first.map { $0.drawnKeys.first ?? TodosSectionHeader.id($0.id) }
        guard let target else { return }
        Task { @MainActor in
            await Task.yield()
            proxy.scrollTo(target, anchor: .bottom)
        }
    }

    private func openPickedChat() {
        guard let key = pickedChat else { return }
        pickedChat = nil
        app.openAssistantConversation(key)
    }

    /// Where the search field is: iOS 26's foot toolbar (the system's); before it, the drawer under the title, always
    /// shown, since under an inline title the system's drawer starts folded away until a pull, and a calm list would
    /// show no way to search at all.
    private static var searchPlacement: SearchFieldPlacement? {
        if #available(iOS 26.0, *) { return nil }
        return .navigationBarDrawer(displayMode: .always)
    }

    /// The list at its end again, as it rested: put to its last drawn row's top, which a list at its end cannot reach,
    /// so the list stops at its end. Each moment of the way back while it is not there (the toolbar's inset comes back
    /// over the pop's frames), a second at most; never while a finger moves it.
    private func backToTheEnd(_ proxy: ScrollViewProxy) {
        Task { @MainActor in
            for _ in 0..<20 where !Task.isCancelled && !sight.scrolling {
                if !sight.atEnd, let last = model.groups.last(where: { !$0.drawnKeys.isEmpty })?.drawnKeys.last {
                    proxy.scrollTo(last, anchor: .top)
                }
                try? await Task.sleep(for: .milliseconds(50))
            }
        }
    }

    // MARK: The list

    @ViewBuilder private var content: some View {
        if model.isEmpty {
            Section {
                emptyState
                    .listRowBackground(Color.clear)
                    .listRowSeparator(.hidden)
            }
        } else {
            ForEach(model.groups) { group in
                if let tail = group.tail {
                    // Someday, Done today: a fold on the page after the last card; open, its rows a card under it.
                    Section {
                        if tail.open { ForEach(tail.keys, id: \.self) { key in row(key) } }
                    } header: {
                        TodosTailFold(fold: tail, model: model)
                    }
                } else {
                    Section {
                        ForEach(group.keys, id: \.self) { key in row(key) }
                        ForEach(group.folds) { fold in
                            foldLine(fold)
                            if fold.open { ForEach(fold.keys, id: \.self) { key in row(key) } }
                        }
                    } header: {
                        if model.place == .upcoming && !model.searching {
                            TodoUpcomingHeader(group: group) { creating = model.capture(on: $0) }
                                .id(TodosSectionHeader.id(group.id))
                        } else if let title = group.title {
                            TodosSectionHeader(model: model, group: group, title: title)
                        }
                    }
                }
            }
        }
    }

    @ViewBuilder private func row(_ key: String) -> some View {
        if let cell = model.row(key) {
            // Its cell and the model, never closures or its content: a list update that leaves it alone skips it (P6).
            TodoListCell(cell: cell, model: model)
            .onAppear { sight.visible.insert(key) }
            .onDisappear {
                sight.visible.remove(key)
                sight.middles[key] = nil
            }
            // Where its middle is in the list's frame, kept unobserved: whether the bars cover it.
            .onGeometryChange(for: CGFloat.self) { $0.frame(in: .scrollView).midY } action: { sight.middles[key] = $0 }
            .id(key)
            // The row's list traits on its outermost view, where the list reads them (`TodoListCell.insets`).
            .listRowInsets(TodoListCell.insets)
        }
    }

    /// "Can wait · 1 ⌄": what is folded at Needs you's foot, opened in place inside its card. A row of the card, the whole
    /// row its button (at least a finger's height, as every row), its words in the titles' column (a tick's room before
    /// them, unseen) with ⌄ or ⌃ beside them; spoken as `FoldButton` speaks a fold.
    private func foldLine(_ fold: TodosListGroup.Fold) -> some View {
        Button {
            Haptics.tap()
            withAnimation(Theme.Motion.reduced ? nil : Theme.Motion.state) { model.toggle(fold.id) }
        } label: {
            HStack(alignment: .firstTextBaseline, spacing: Theme.Space.m) {
                TodoTickGlyph(closed: false).hidden()
                TodosFoldWords(fold: fold)
                    .font(Theme.Font.secondary)
                Spacer(minLength: Theme.Space.flush)
            }
            .contentShape(.rect)
        }
        .listRowInsets(TodoListCell.insets)
        .disabled(model.searching) // a search opens every fold: there is nothing to fold until it ends
        .todosFoldAccessibility(fold)
    }

    @ViewBuilder private var emptyState: some View {
        if let offline = app.assistantManagerOffline, !model.hasRows {
            AssistantOfflineNotice(manager: offline)
        } else if model.searching {
            // Nothing open matches: the line under it widens the search to the chats.
            EmptyStateView(symbol: Theme.Icon.search,
                           title: String(localized: "No Results for “\(model.query.trimmingCharacters(in: .whitespacesAndNewlines))”"),
                           message: String(localized: "Search looks through the titles and notes of your open to-dos. [To-dos Search]"))
        } else if model.placeIsEmpty {
            TodoPlaceEmptyState(place: model.place, title: title, comingUp: model.comingUp) { places.show($0) }
        } else {
            switch model.choice {
            case .all:
                EmptyStateView(symbol: Theme.Icon.todo, title: String(localized: "Nothing to do yet. [To-dos List]"),
                               message: String(localized: "Write down anything you want done. Persona can help with the rest. [To-dos List]"))
            case .needsYou:
                EmptyStateView(symbol: Theme.Icon.todo, title: String(localized: "Nothing needs you [To-dos List]"),
                               message: String(localized: "When a to-do needs your answer, it shows up here. [To-dos List]"))
            case .inProgress:
                EmptyStateView(symbol: Theme.Icon.todo, title: String(localized: "Nothing in progress [To-dos List]"),
                               message: String(localized: "To-dos Persona or an Agent works on show up here. [To-dos List]"))
            case .todo:
                EmptyStateView(symbol: Theme.Icon.todo, title: String(localized: "Nothing to do [To-dos List]"),
                               message: String(localized: "Your own to-dos that nobody works on show up here. [To-dos List]"))
            case .done:
                EmptyStateView(symbol: Theme.Icon.todo, title: String(localized: "Nothing done yet [To-dos List]"),
                               message: String(localized: "Closed to-dos show up here by the day they closed. [To-dos List]"))
            }
        }
    }

    /// Date…'s row and its to-do.
    private struct DueTarget: Identifiable {
        var key: String
        var item: TodoItem
        var id: String { key }
    }
}

/// The words "Search Chats for …" hands to the app's search.
private struct TodosChatSearch: Identifiable {
    var words: String
    var id: String { words }
}

// MARK: The foot toolbar

/// The list's foot (DESIGN_SYSTEM 3.4 "To-dos list"), Mail's toolbar: the Filter at the leading end, the system's search
/// field, then New To-Do, the bar's one prominent item. On iOS 26 and later the Filter is a pop-up that names its choice
/// with its own × beside it, and the field folds to ⌕ while a choice is on; iOS 18's classic toolbar draws Mail's iOS 18
/// form: the Filter's glyph (filled while a choice is on, a tap back to All), "Filtered by: ‹choice›" in the middle, and
/// +, with the search field under the title. The calm list, a list with no rows, Today and Upcoming have no Filter; when
/// it comes back it takes the toolbar's free leading slot, so no row moves. + starts the to-do in the place shown, the
/// one prominent action in a place's empty state too. Toolbar items are chosen on the view, since a toolbar's
/// builder cannot ask the OS (docs/lessons/ios.md "iOS 18.6 floor"). Its own modifier reading the model's few flags,
/// never its layout, so a choice or a count redraws the toolbar and a landing does not (P5).
private struct TodosFootBar: ViewModifier {
    var model: TodosListModel
    /// The place's name: + says where a new to-do starts ("New To-Do in Kyoto trip").
    var place: String
    @Binding var creating: TodoCapture?

    /// + in the place shown: the to-do starts there (`TodosListModel.capture`), and its name says where.
    private func newButton(prominent: Bool) -> TodosNewButton {
        TodosNewButton(title: Self.newTitle(model.place, place: place), prominent: prominent) { creating = model.capture() }
    }

    /// "New To-Do in Kyoto trip", "New To-Do Due Today", "New To-Do Due Tomorrow"; in All To-dos, "New To-Do".
    private static func newTitle(_ shown: TodoPlaceRef, place: String) -> String {
        switch shown.id {
        case TodoPlaceKind.today: TodoUpcomingLayout.newTodo(due: String(localized: "Today [To-dos Due]"))
        case TodoPlaceKind.upcoming: TodoUpcomingLayout.newTodo(due: String(localized: "Tomorrow [To-dos Due]"))
        default: shown.project == nil ? String(localized: "New To-Do [To-dos Actions]")
            : String(localized: "New To-Do in \(place) [To-dos Places]")
        }
    }

    private var filters: Bool { model.filters }
    private var filtered: Bool { filters && model.choice != .all }

    func body(content: Content) -> some View {
        if #available(iOS 26.0, *) {
            content
                .toolbar { bar }
                .searchToolbarBehavior(filtered ? .minimize : .automatic)
        } else {
            content.toolbar { classicBar }
        }
    }

    /// The Filter at rest and the pop-up naming a choice are bar items of their own: one item whose label turned from
    /// the glyph into the glyph and a name was drawn empty on iOS 26.3.
    @available(iOS 26.0, *)
    @ToolbarContentBuilder private var bar: some ToolbarContent {
        if filtered {
            ToolbarItem(placement: .bottomBar) { TodosFilterButton(model: model) }
            ToolbarItem(placement: .bottomBar) { TodosFilterClear(model: model) }
        } else if filters {
            ToolbarItem(placement: .bottomBar) { TodosFilterButton(model: model) }
        }
        ToolbarSpacer(.flexible, placement: .bottomBar)
        DefaultToolbarItem(kind: .search, placement: .bottomBar)
        ToolbarSpacer(.fixed, placement: .bottomBar)
        ToolbarItem(placement: .bottomBar) { newButton(prominent: true) }
    }

    @ToolbarContentBuilder private var classicBar: some ToolbarContent {
        ToolbarItemGroup(placement: .bottomBar) {
            if filtered {
                TodosFilterClear(model: model, glyph: true)
            } else if filters {
                TodosFilterButton(model: model)
            }
            Spacer()
            if filtered {
                TodosFilteredBy(model: model)
                Spacer()
            }
            newButton(prominent: false)
        }
    }
}

/// The Filter: its glyph at rest; once a choice is on (iOS 26 and later), the filled glyph and the choice's name, which
/// open the menu again. The bar draws a `Label` as its icon alone, so the choice's name is a `Text` of its own beside
/// the glyph; the large content viewer shows it at any text size. Its menu keeps All at the top: a bottom bar's menu
/// otherwise lists its items from the finger up. Spoken "Filter" with the choice as its value.
private struct TodosFilterButton: View {
    var model: TodosListModel

    var body: some View {
        let choice = model.choice
        Menu {
            TodosFilterChoices(model: model)
        } label: {
            if choice == .all {
                Label(String(localized: "Filter [To-dos Filter]"), systemImage: Theme.Icon.filter)
            } else {
                HStack(spacing: Theme.Space.xs) {
                    ThemedIcon(Theme.Icon.filterActive)
                    Text(choice.title)
                }
                .lineLimit(1)
            }
        }
        .menuOrder(.fixed)
        .tint(choice == .all ? Color.primary : Theme.accent)
        .accessibilityShowsLargeContentViewer()
        .accessibilityLabel(String(localized: "Filter [To-dos Filter]"))
        .accessibilityValue(choice.title)
        .accessibilityIdentifier("todos.filter")
    }
}

/// The Filter's menu: "Show", then the five choices, each with how many it would show under its name (the system's
/// subtitle, spoken with the name), the one on checked. Toggles, since a picker in a menu drops the heading and the
/// subtitles.
private struct TodosFilterChoices: View {
    var model: TodosListModel

    var body: some View {
        Section(String(localized: "Show [To-dos Filter]")) {
            ForEach(model.filterOptions) { option in
                Toggle(isOn: Binding(get: { model.choice == option.choice }, set: { on in
                    guard on, model.choice != option.choice else { return }
                    Haptics.tap()
                    model.choice = option.choice
                })) {
                    Text(option.choice.title)
                    if let subtitle = option.subtitle { Text(subtitle) }
                }
                .accessibilityLabel([option.choice.title, option.subtitle].compactMap { $0 }
                    .joined(separator: String(localized: ", ", comment: "Separator between parts of an accessibility label")))
                .accessibilityIdentifier("todos.filter." + option.choice.rawValue)
            }
        }
    }
}

/// Back to All: × beside the Filter's name (iOS 26 and later), or the filled glyph itself on iOS 18, Mail's toggle.
private struct TodosFilterClear: View {
    var model: TodosListModel
    var glyph = false

    var body: some View {
        Button {
            Haptics.tap()
            model.choice = .all
        } label: {
            Label(String(localized: "Show All To-dos [To-dos Filter]"), systemImage: glyph ? Theme.Icon.filterActive : Theme.Icon.close)
                .labelStyle(.iconOnly)
        }
        .tint(glyph ? Theme.accent : Color.primary)
        .accessibilityIdentifier("todos.filter.clear")
    }
}

/// iOS 18's middle: "Filtered by:" over the choice's name, a tap opening the Filter's menu (Mail's).
private struct TodosFilteredBy: View {
    var model: TodosListModel

    var body: some View {
        Menu {
            TodosFilterChoices(model: model)
        } label: {
            VStack(spacing: Theme.Space.flush) {
                Text("Filtered by: [To-dos Filter]")
                    .font(Theme.Font.label)
                    .foregroundStyle(Color.primary)
                Text(model.choice.title)
                    .font(Theme.Font.labelStrong)
                    .foregroundStyle(Theme.accent)
            }
            .lineLimit(1)
        }
        .accessibilityLabel(String(localized: "Filter [To-dos Filter]"))
        .accessibilityValue(model.choice.title)
        .accessibilityIdentifier("todos.filter.value")
    }
}

/// New To-Do: + at the toolbar's trailing end, the bar's one prominent item on iOS 26 and later; its name for VoiceOver
/// and in the large content viewer, which in a project says the project it starts in.
private struct TodosNewButton: View {
    var title: String
    var prominent: Bool
    var action: () -> Void

    var body: some View {
        let button = Button(action: action) {
            Label(title, systemImage: Theme.Icon.add)
                .labelStyle(.iconOnly)
        }
        .keyboardShortcut("n", modifiers: .command)
        .accessibilityShowsLargeContentViewer()
        .accessibilityIdentifier("todos.new")
        if prominent { button.themedButton(prominent: true) } else { button }
    }
}

/// The title's subtitle (iOS 26 and later; iOS 18 names the choice in its toolbar all the while): once the guard line has
/// scrolled away, what it said (`TodosListModel.subtitle`), so N stays in sight, in a place too. Its own modifier, so N
/// changing redraws the title, not the list (P5).
private struct TodosSubtitle: ViewModifier {
    var model: TodosListModel
    var lines: TodosTopLinesSight
    /// The place's name, for what needs the owner outside it.
    var place: String

    func body(content: Content) -> some View {
        content.navigationSubtitleWhenSupported(lines.guardGone ? model.subtitle(place: place) : "")
    }
}

// MARK: ⋯

/// ⋯ (DESIGN_SYSTEM 3.4 "To-dos list"): how the list is arranged, kept on the device (the page writes the model's
/// arrangement back). Sort By as a section with the current order checked (Toggles under a heading, which a menu draws
/// whole, where a picker drops it); choosing Suggested is the reset. Then Group by Project, one checkmarked item as Mail's
/// Group by Sender: never offered in the calm list, greyed with why under a choice that draws one section or the days,
/// and in a project's place, which is one project. Upcoming's holds Show as Calendar alone (3.5 "To-dos places").
/// Monochrome, as the system's bar items. Before iOS 27 (or built with an older SDK) it is a menu of ours at the bar's
/// trailing end.
struct TodosViewMenu: View {
    var model: TodosListModel
    var place: String
    @Binding var asCalendar: Bool

    var body: some View {
        Menu {
            TodosViewMenuItems(model: model, place: place, asCalendar: $asCalendar)
        } label: {
            Label(String(localized: "More [To-dos List]"), systemImage: Theme.Icon.more)
                .labelStyle(.iconOnly)
        }
        .tint(Color.primary)
        .accessibilityIdentifier("todos.view")
    }
}

/// Where ⋯ goes: on iOS 27, built with its SDK, the bar's own overflow menu (`ToolbarOverflowMenu`, spoken "More"), so
/// whatever no longer fits the bars lands in the same ⋯ (DECISIONS 11-21; the HIG's "use the system overflow menu");
/// before it, `TodosViewMenu` as the bar's trailing item. Chosen on the view, since a toolbar's builder cannot ask the
/// OS (docs/lessons/ios.md "iOS 18.6 floor").
private struct TodosViewOptions: ViewModifier {
    var model: TodosListModel
    /// The place's name, for why a project's place does not group by project.
    var place: String
    @Binding var asCalendar: Bool

    func body(content: Content) -> some View {
        #if compiler(>=6.4) // `ToolbarOverflowMenu` is in the iOS 27 SDK; Xcode 26 (Swift ≤ 6.3) has no such type
        if #available(iOS 27.0, *) {
            content.toolbar { ToolbarOverflowMenu { TodosViewMenuItems(model: model, place: place, asCalendar: $asCalendar) } }
        } else {
            content.pageToolbar { ToolbarItem(placement: .primaryAction) { menu } }
        }
        #else
        content.pageToolbar { ToolbarItem(placement: .primaryAction) { menu } }
        #endif
    }

    private var menu: TodosViewMenu { TodosViewMenu(model: model, place: place, asCalendar: $asCalendar) }
}

/// What ⋯ holds, in either form. One view, so what ⋯ holds is changed here alone.
private struct TodosViewMenuItems: View {
    var model: TodosListModel
    var place: String
    @Binding var asCalendar: Bool

    var body: some View {
        if model.place == .upcoming {
            // Upcoming's days are its grouping and its order: ⋯ holds its other way to draw them.
            Toggle(isOn: $asCalendar) {
                Label(String(localized: "Show as Calendar [To-dos Places]"), systemImage: Theme.Icon.calendarView)
            }
            .accessibilityIdentifier("todos.places.showCalendar")
        } else {
            arrangement
        }
    }

    @ViewBuilder private var arrangement: some View {
        Section(String(localized: "Sort By [To-dos List]")) {
            ForEach(TodosListSort.allCases) { s in
                Toggle(s.title, isOn: Binding(get: { model.sort == s }, set: { on in
                    guard on, model.sort != s else { return }
                    Haptics.tap()
                    model.sort = s
                }))
                .accessibilityIdentifier("todos.view.sort." + s.rawValue)
            }
        }
        if !model.calm {
            if let note = model.place.project == nil ? TodosFilter.groupingNote(model.choice)
                : String(localized: "\(place) is one project [To-dos View]") {
                // Greyed, with why in the section's heading: a disabled item's own words are too faint to read on the
                // menu's glass (docs/lessons/ios.md "A switch with words under it inside a menu").
                Section(note) {
                    Button {} label: { Text("Group by Project [To-dos View]") }
                        .disabled(true)
                        .accessibilityIdentifier("todos.view.byProject")
                }
            } else {
                Section {
                    Toggle(isOn: Binding(get: { model.byProject }, set: { on in
                        Haptics.tap()
                        model.byProject = on
                    })) {
                        Text("Group by Project [To-dos View]")
                    }
                    .accessibilityIdentifier("todos.view.byProject")
                }
            }
        }
    }
}

// MARK: Headers, folds and the lines over the list

/// A section's title with its count, the system's inset grouped header (no font of ours), its words over the rows'
/// ticks: Needs you's is N in the needs-you badge, as the drawer says it; the others, and Needs you's while searching
/// (what matches, which is not N), count the rows under them in plain numbers (DESIGN_SYSTEM 3.4, the Agents list's
/// state headers), so the blue badge only ever says N. A view of its own, so N changing redraws the header, not the
/// list (P5). Spoken once: the title, then the count as its value.
private struct TodosSectionHeader: View {
    var model: TodosListModel
    var group: TodosListGroup
    var title: String

    static func id(_ group: String) -> String { "todos.header." + group }

    var body: some View {
        let badge = group.needs && !model.searching
        let count = badge ? model.needsYou : group.count
        HStack(spacing: Theme.Space.sm) {
            SectionHeader(title: title, count: badge ? count : nil, tone: StatusPalette.needsYou)
            if !badge, count > 0 { Text(verbatim: String(count)).monospacedDigit() }
        }
        .textCase(nil)
        .lineLimit(1)
        .listRowHorizontalInsets(Theme.Space.l)
        .accessibilityElement(children: .ignore)
        .accessibilityLabel(title)
        .accessibilityValue(String(count))
        .accessibilityAddTraits(.isHeader)
        .accessibilityIdentifier("todos.section." + group.id)
        .id(Self.id(group.id))
    }
}

/// "Someday · 1 ⌄", "Done today · 2 ⌄": a tail fold, a header on the page after the last card in the headers' type, the
/// whole line its button; open, its rows are a card under it.
private struct TodosTailFold: View {
    var fold: TodosListGroup.Fold
    var model: TodosListModel

    var body: some View {
        Button {
            Haptics.tap()
            withAnimation(Theme.Motion.reduced ? nil : Theme.Motion.state) { model.toggle(fold.id) }
        } label: {
            TodosFoldWords(fold: fold)
                .frame(maxWidth: .infinity, alignment: .leading)
                .scaledFrame(minHeight: Theme.Size.tapTarget)
                .contentShape(.rect)
        }
        .buttonStyle(.plain)
        .disabled(model.searching) // a search opens every fold
        .textCase(nil)
        .listRowHorizontalInsets(Theme.Space.l)
        .todosFoldAccessibility(fold)
        .id(TodosSectionHeader.id("tail:" + fold.id)) // its group's: where a choice scrolls the list to
    }
}

/// A fold's words and its sign: ⌄ while folded, ⌃ while open, beside the words (never a page's ›), in the secondary
/// ink, not a button's tint: a fold is part of the list, not an action beside it.
private struct TodosFoldWords: View {
    var fold: TodosListGroup.Fold

    var body: some View {
        HStack(alignment: .firstTextBaseline, spacing: Theme.Space.xs) {
            Text(verbatim: fold.title + TodoTimeWords.separator + String(fold.keys.count))
                .lineLimit(1)
            ThemedIcon(fold.open ? Theme.Icon.collapse : Theme.Icon.expand)
                .font(Theme.Font.labelHeavy)
                .accessibilityHidden(true)
        }
        .foregroundStyle(Color.secondary)
    }
}

/// What the list says before its first card, each line on the page: the hub out of reach, then what needs the owner
/// that the list does not show (what a choice hides, and in a place what is outside it). A view of its own, so only it
/// follows the hub's link and N (P5).
private struct TodosTopLines: View {
    @Environment(AppModel.self) private var app
    var model: TodosListModel
    var sight: TodosTopLinesSight
    /// The place's name, for a line about what is outside it.
    var place: String
    /// Show All in a place: All To-dos, where everything that needs the owner is.
    var showAll: () -> Void

    /// Whether the titles say the hub is out of reach (`ConnectionStatus`, after its delay).
    static func offline(_ app: AppModel) -> Bool {
        switch app.connection.shown {
        case .waitingForNetwork, .connecting, .rejected: true
        case .online, .updating: false
        }
    }

    /// The first line's id, where the list's top is while there is one.
    static func first(offline: Bool, hidden: Int) -> String? {
        offline ? "todos.lines.offline" : hidden > 0 ? "todos.lines.guard" : nil
    }

    var body: some View {
        let hidden = model.hiddenNeeds
        let offline = Self.offline(app) && model.hasRows
        if offline || hidden > 0 {
            Section {
                if offline {
                    // A surface over the cards: as wide as they are.
                    TodosOfflineLine(model: model)
                        .id("todos.lines.offline")
                        .listRowInsets(EdgeInsets(top: Theme.Space.flush, leading: Theme.Space.flush, bottom: Theme.Space.flush,
                                                  trailing: Theme.Space.flush))
                }
                if hidden > 0 {
                    TodosGuardLine(text: TodosFilter.guardWords(hidden, elsewhere: model.hiddenElsewhere, place: place)) {
                        Haptics.tap()
                        if model.place.isAll { model.choice = .all } else { showAll() }
                    }
                    // Gone once the bars cover it: where it is in the list's frame against what they cover, as
                    // `TodoListSight` tells where the list rests (a list row says nothing of being under a bar, and
                    // its cell is reused once off screen, so the last value it reported stands).
                    .onGeometryChange(for: CGFloat.self) { $0.frame(in: .scrollView).maxY } action: { bottom in
                        let gone = bottom < sight.covered
                        if sight.guardGone != gone { sight.guardGone = gone }
                    }
                    .id("todos.lines.guard")
                    .listRowInsets(TodoListCell.insets)
                }
            }
            .listRowBackground(Color.clear)
            .listRowSeparator(.hidden)
        }
    }
}

/// Whether the guard line has gone under the bars (told by the line as the list scrolls; a list that recycles the row
/// off screen says nothing more), for the title's subtitle.
@MainActor @Observable final class TodosTopLinesSight {
    var guardGone = false
    /// How much of the list's top the bars cover (not observed: scrolling redraws nothing).
    @ObservationIgnored var covered: CGFloat = 0
}

/// "● 3 more need you · Show All" (DESIGN_SYSTEM 3.4 "Sessions that need the user"): while a choice hides what needs
/// the owner, a line on the page over the first card, never a card or a tint of its own (the quiet-surfaces rule), the
/// whole line its button back to everything. The needs-you dot, still; the words in the secondary color; Show All in
/// the accent. One element with its action. In a place it also counts what needs the owner outside it, one line for
/// both, and Show All opens All To-dos ("● 2 more need you, not in Today · Show All", 3.5 "To-dos places").
struct TodosGuardLine: View {
    var text: String
    var showAll: () -> Void

    var body: some View {
        Button(action: showAll) {
            HStack(alignment: .firstTextBaseline, spacing: Theme.Space.s) {
                Circle().fill(StatusPalette.needsYou.dot)
                    .scaledFrame(width: Theme.Size.dotS, height: Theme.Size.dotS)
                    .alignmentGuide(.firstTextBaseline) { $0[VerticalAlignment.center] + $0.height }
                Text(Self.words(text))
                    .font(Theme.Font.secondary)
                    .lineLimit(2)
                Spacer(minLength: Theme.Space.flush)
            }
            .scaledFrame(minHeight: Theme.Size.tapTarget)
            .contentShape(.rect)
        }
        .buttonStyle(.plain)
        .accessibilityElement(children: .ignore)
        .accessibilityLabel([text, String(localized: "Show All [To-dos Filter]")]
            .joined(separator: String(localized: ", ", comment: "Separator between parts of an accessibility label")))
        .accessibilityHint(String(localized: "Shows all your to-dos. [To-dos Filter]"))
        .accessibilityAddTraits(.isButton)
        .accessibilityIdentifier("todos.guard")
    }

    /// The words in the secondary color, then Show All in the accent: one paragraph, wrapping as one line of text.
    private static func words(_ text: String) -> AttributedString {
        var said = AttributedString(text + TodoTimeWords.separator)
        said.foregroundColor = .secondary
        var action = AttributedString(String(localized: "Show All [To-dos Filter]"))
        action.foregroundColor = Theme.accent
        return said + action
    }
}

/// "Can't reach your hub · showing what was saved 10:14": the list's line while the hub is out of reach as the titles
/// say it (`ConnectionStatus`, after its delay). It says the link itself: the shell's pill stays away while it is in
/// sight, so the link is said once and the list stays clear (`titleSaysLink`, docs/apple/shell.md "Said once").
private struct TodosOfflineLine: View {
    var model: TodosListModel

    var body: some View {
        NoticeBanner(text: model.offlineLine, tone: .caution, symbol: Theme.Icon.offline)
            .titleSaysLink()
            .accessibilityIdentifier("todos.offline")
    }
}

/// "⌕ Search Chats for “kyoto” ›": the search's last row, which takes the words to the app's search over Persona's chats.
/// A view of its own, so only it follows the words (P5).
private struct TodosSearchChatsRow: View {
    var model: TodosListModel
    var open: (String) -> Void

    var body: some View {
        let words = model.query.trimmingCharacters(in: .whitespacesAndNewlines)
        if !words.isEmpty {
            Section {
                Button {
                    Haptics.tap()
                    open(words)
                } label: {
                    HStack(spacing: Theme.Space.m) {
                        ThemedIcon(Theme.Icon.search).foregroundStyle(.secondary)
                        Text("Search Chats for “\(words)” [To-dos Search]")
                            .foregroundStyle(Color.primary)
                            .lineLimit(2)
                        Spacer(minLength: Theme.Space.flush)
                        ThemedIcon(Theme.Icon.disclosure)
                            .font(Theme.Font.labelHeavy)
                            .foregroundStyle(.tertiary)
                            .accessibilityHidden(true)
                    }
                    .contentShape(.rect)
                }
                // Plain, so its glyph and chevron are the grays they say, not the accent a list's button tints them.
                .buttonStyle(.plain)
                .listRowInsets(TodoListCell.insets)
                .accessibilityIdentifier("todos.searchChats")
            }
        }
    }
}

private extension View {
    /// A fold spoken as `FoldButton` speaks a section's: its name, folded or not, and how many it holds.
    func todosFoldAccessibility(_ fold: TodosListGroup.Fold) -> some View {
        let detail = String(localized: "\(fold.keys.count) to-dos [To-dos List]")
        let state = fold.open ? String(localized: "Expanded") : String(localized: "Collapsed [Readiness Card ee6046]")
        return accessibilityElement(children: .ignore)
            .accessibilityLabel([fold.title, state, detail]
                .joined(separator: String(localized: ", ", comment: "Separator between parts of an accessibility label")))
            .accessibilityAddTraits(.isButton)
            .accessibilityIdentifier("todos.fold." + fold.id)
    }
}

/// What the list's scroll geometry says the page needs: how much of its top the bars cover, and whether it is at its
/// very end (a list shorter than its frame has no end to come back to).
struct TodoListEdges: Equatable {
    var top: CGFloat
    var atEnd: Bool

    init(_ g: ScrollGeometry) {
        top = g.contentInsets.top
        let end = g.contentSize.height + g.contentInsets.bottom - g.containerSize.height
        atEnd = end > -g.contentInsets.top && g.contentOffset.y >= end - 1
    }
}

/// The rows a list has drawn, and where they are: where the list rests, by its top row in sight. Not observed: scrolling
/// redraws nothing.
@MainActor final class TodoListSight {
    /// The rows drawn, the ones under the bars included.
    var visible: Set<String> = []
    /// Each drawn row's middle in the list's frame.
    var middles: [String: CGFloat] = [:]
    /// How much of the list's top the bars cover.
    var covered: CGFloat = 0
    /// The list is at its very end now; it came to rest there; a finger moves it.
    var atEnd = false
    private(set) var restedAtEnd = false
    var scrolling = false

    /// A scroll came to rest: Back from a to-do pushed over the list puts a list resting at its end there again.
    func rested() { restedAtEnd = atEnd }

    /// The page moved the list itself, which ends no scroll phase: a choice's scroll to the top, the search narrowing
    /// the list or giving it back. Where a scroll last rested no longer holds, so Back leaves the list where it is; a
    /// to-do opened without a touch on the list (VoiceOver, the keyboard, a link) must not put it back at its end.
    func moved() { restedAtEnd = false }

    /// The first row whose middle is clear of the bars.
    func top(in groups: [TodosListGroup]) -> String? {
        for group in groups {
            for key in group.drawnKeys where visible.contains(key) {
                if (middles[key] ?? covered) >= covered { return key }
            }
        }
        return nil
    }
}
#endif
