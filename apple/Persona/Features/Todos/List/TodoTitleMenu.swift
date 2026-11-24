#if os(iOS)
import PersonaWire
import SwiftUI

/// The list's title as a menu of the same places as the sidebar (#10465; proposal 3.3: "All To-dos ▾", the second way
/// to a place, which the HIG asks for): All To-dos, Today and Upcoming, then the projects with their part of N, then
/// All Projects…. A project shown that the menu does not list (picked in All Projects…) is listed ticked above All
/// Projects…, so the menu always ticks the place shown. The system's title menu (`toolbarTitleMenu`, given by
/// `TodoPlaceFollower` only while the hub lists places); a pick changes the list where it is, with no animation.
struct TodoTitleMenu: View {
    var places: TodosPlacesModel
    /// All Projects…: the sheet of every project.
    var allProjects: () -> Void

    var body: some View {
        let entries = places.entries
        if !entries.isEmpty {
            let current = places.current
            Section {
                choice(.all, current: current)
                ForEach(entries.fixed, id: \.id) { place in choice(TodoPlaceRef(id: place.id), current: current) }
            }
            if !entries.projects.isEmpty || entries.more {
                Section {
                    ForEach(entries.projects, id: \.id) { place in
                        choice(TodoPlaceRef(id: place.id), current: current, needsYou: place.needsYou)
                    }
                    if current.project != nil, !entries.projects.contains(where: { $0.id == current.id }) {
                        choice(current, current: current,
                               needsYou: places.places.first { $0.id == current.id }?.needsYou ?? 0)
                    }
                    if entries.more {
                        Button(String(localized: "All Projects… [To-dos Places]"), action: allProjects)
                            .accessibilityIdentifier("todos.places.menu.more")
                    }
                }
            }
        }
    }

    /// A place, ticked while shown; a project's part of N in words under its name (a menu item draws no badge).
    private func choice(_ place: TodoPlaceRef, current: TodoPlaceRef, needsYou: Int = 0) -> some View {
        Toggle(isOn: Binding(get: { current == place }, set: { on in
            guard on, place != current else { return }
            Haptics.tap()
            places.show(place)
        })) {
            Text(places.title(place))
            if needsYou > 0 { Text(StateName.count(.needsYou, needsYou)) }
        }
        .accessibilityIdentifier("todos.places.menu." + place.id)
    }
}

/// Tells the page the place to show and its name, as the owner picks it or once the hub stops listing it, and gives
/// the page's title its menu while there are places to pick (none: a plain title, no menu to open): a view of its own,
/// so the places' numbers changing redraw this and not the page (P5).
struct TodoPlaceFollower: View {
    var places: TodosPlacesModel
    /// All Projects… in the title menu.
    var allProjects: () -> Void
    var follow: (TodoPlaceRef, String) -> Void

    var body: some View {
        let place = places.current
        let title = places.title(place)
        let follower = Color.clear
            .onChange(of: PlaceShown(place: place, title: title), initial: true) { _, shown in follow(shown.place, shown.title) }
        if places.entries.isEmpty {
            follower
        } else {
            follower.toolbarTitleMenu { TodoTitleMenu(places: places, allProjects: allProjects) }
        }
    }

    private struct PlaceShown: Equatable {
        var place: TodoPlaceRef
        var title: String
    }
}

/// Every project as a place (All Projects…, from the sidebar or the title menu): the hub's projects with open to-dos,
/// then the knowledge base's others (long-running ones first, then by name), each with how many to-dos are open in it
/// (neutral, `TodoCounts.open` counted from its rows) and then its part of N (blue, hidden at 0), at the row's end as on
/// the sidebar's rows, which keep N alone. Section cards, as the list's. A task over the page; a pick closes it and the
/// caller opens the place. The projects and their counts are worked out as it opens and when the places, the rows or
/// the knowledge base's projects change, never in its body (P4).
struct TodoAllProjectsSheet: View {
    @Environment(\.dismiss) private var dismiss
    @Environment(AppModel.self) private var app
    var places: TodosPlacesModel
    var pick: (TodoPlaceRef) -> Void
    /// nil until first worked out, so the sheet never says "No projects yet" for a frame.
    @State private var projects: [TodoPlace]?
    @State private var open: [String: Int] = [:]

    var body: some View {
        NavigationStack {
            List {
                if projects?.isEmpty == true {
                    EmptyStateView(symbol: Theme.Icon.space, title: String(localized: "No projects yet [To-dos Places]"),
                                   message: String(localized: "Projects come from your knowledge base. [To-dos Places]"))
                        .listRowBackground(Color.clear)
                }
                ForEach(projects ?? [], id: \.id) { place in
                    let name = places.title(TodoPlaceRef(id: place.id))
                    let count = open[place.project ?? ""] ?? 0
                    Button {
                        Haptics.tap()
                        pick(TodoPlaceRef(id: place.id))
                    } label: {
                        HStack(spacing: Theme.Space.m) {
                            ThemedIcon(Theme.Icon.space).foregroundStyle(.secondary)
                            Text(name).foregroundStyle(Color.primary).lineLimit(1)
                            Spacer(minLength: Theme.Space.s)
                            if count > 0 { Text(verbatim: String(count)).monospacedDigit().foregroundStyle(.secondary) }
                            if place.needsYou > 0 { CountBadge(count: place.needsYou, look: .needs) }
                        }
                        .contentShape(.rect)
                    }
                    .buttonStyle(.plain)
                    .accessibilityElement(children: .ignore)
                    .accessibilityLabel(name)
                    .accessibilityValue([place.needsYou > 0 ? StateName.count(.needsYou, place.needsYou) : nil,
                                         count > 0 ? String(localized: "\(count) to-dos [To-dos List]") : nil].compactMap { $0 }
                        .joined(separator: String(localized: ", ", comment: "Separator between parts of an accessibility label")))
                    .accessibilityAddTraits(.isButton)
                    .accessibilityIdentifier("todos.places.project." + place.id)
                }
            }
            .sectionCards()
            .themedPage()
            .navigationTitle(String(localized: "Projects [To-dos Places]"))
            .navigationBarTitleDisplayMode(.inline)
            .toolbar { SheetClose { dismiss() } }
        }
        // The knowledge base's projects, read as the sheet opens (one made meanwhile shows up).
        .task { await TodoProjects.shared.load(app.api?.currentAssistantSettings()) }
        .onChange(of: Inputs(places: places.places, rows: TodoStore.shared.countsSource.filed, known: TodoProjects.shared.list),
                  initial: true) { _, _ in
            let all = places.allProjects
            if all != projects { projects = all }
            let counted = places.openCounts(all)
            if counted != open { open = counted }
        }
    }

    /// What the projects and their counts are worked out from.
    private struct Inputs: Equatable {
        var places: [TodoPlace]
        var rows: [TodoFiled]
        var known: [TodoProject]
    }
}
#endif
