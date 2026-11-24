#if os(iOS)
import PersonaWire
import SwiftUI

/// One To-dos place under the sidebar's To-dos row (#10465), for the sidebar to draw as it draws every place to go
/// (`AssistantSidebar.action`: the style's row, set in under To-dos).
struct TodoPlaceRowItem: Identifiable {
    var id: String
    var title: String
    var symbol: String
    /// Its part of N, drawn blue and hidden at 0.
    var count = 0
    /// The place shown beside the sidebar.
    var selected = false
    /// The 12-pt gap before the first project (proposal 3.3).
    var gapBefore = false
    var run: () -> Void

    /// Its accessibility identifier: `sidebar-todos-place-` and the place's id (`more` for All Projects…).
    var identifier: String { "sidebar-todos-place-" + id }
}

/// The sidebar's To-dos row on the V1 path and the places it folds open (#10465; proposal 3.3; DESIGN_SYSTEM 3.5
/// "To-dos places"): To-dos itself (All To-dos, with N), then Today, Upcoming, a 12-pt gap, up to five projects each with
/// its blue part of N, then All Projects…. A pick opens To-dos on that place at its root (`TodosPlacesModel.open`).
/// Without the hub's places, To-dos alone, as before. A view of its own that reads the places and the place shown, so
/// they redraw these rows and not the sidebar (P5); the sidebar draws each row as it draws every place to go (`head`,
/// `row`). With several assistants only the current one's projects are listed, their parts counted from its rows
/// (`TodoCountsSource`). The knowledge base's projects are read here too, for the current assistant once the hub's places
/// have come (the hub answered), so All Projects… (the way to a project with nothing open) is there from the start, not
/// once To-dos has been opened.
struct TodoPlaceRows<Head: View, Row: View>: View {
    /// To-dos is the page beside the sidebar: the place it shows is shaded.
    var shown: Bool
    /// Opens To-dos, the sidebar's own way, once the place is picked.
    var open: () -> Void
    /// The places folded away under To-dos (`sidebar.fold.todos`).
    @Binding var folded: Bool
    /// To-dos' own row: whether it is shaded, what a tap does, and its chevron when it has places to fold.
    @ViewBuilder var head: (_ selected: Bool, _ run: @escaping () -> Void, _ fold: TodoPlacesFold?) -> Head
    @ViewBuilder var row: (TodoPlaceRowItem) -> Row
    @State private var places = TodosPlacesModel(deps: .live)
    @State private var allProjects = false
    /// The project picked in All Projects…, opened once its sheet has gone (a task over the drawer, DESIGN_SYSTEM 3.5).
    @State private var picked: TodoPlaceRef?

    var body: some View {
        let entries = places.entries
        VStack(alignment: .leading, spacing: Theme.Space.flush) {
            if entries.isEmpty {
                head(shown, open, nil)
            } else {
                let shownRows = folded ? [] : items(entries)
                // The place shown has its row shaded; one with no row in sight (folded away, a sixth project, one with
                // nothing open picked in All Projects…) shades All Projects… for a project, else To-dos itself.
                head(shown && !shownRows.contains(where: \.selected), {
                    places.pickRow()
                    open()
                }, TodoPlacesFold(folded: $folded))
                ForEach(shownRows) { item in row(item) }
            }
        }
        .task(id: ProjectsRead(switches: AppModel.shared.assistantSwitches, placed: !entries.isEmpty)) {
            await TodoProjects.shared.load(AppModel.shared.api?.currentAssistantSettings())
        }
        .sheet(isPresented: $allProjects, onDismiss: {
            guard let place = picked else { return }
            picked = nil
            go(place)
        }) {
            TodoAllProjectsSheet(places: places) { place in
                picked = place
                allProjects = false
            }
        }
    }

    private func items(_ entries: TodosPlaces.Entries) -> [TodoPlaceRowItem] {
        let current = places.current
        var out = entries.fixed.map { place in
            let ref = TodoPlaceRef(id: place.id)
            return TodoPlaceRowItem(id: place.id, title: places.title(ref),
                                    symbol: place.kind == TodoPlaceKind.today ? Theme.Icon.todoToday : Theme.Icon.calendarView,
                                    selected: shown && current == ref, run: { go(ref) })
        }
        for (n, place) in entries.projects.enumerated() {
            let ref = TodoPlaceRef(id: place.id)
            out.append(TodoPlaceRowItem(id: place.id, title: places.title(ref), symbol: Theme.Icon.space, count: place.needsYou,
                                        selected: shown && current == ref, gapBefore: n == 0, run: { go(ref) }))
        }
        if entries.more {
            let unlisted = current.project != nil && !out.contains { $0.id == current.id }
            out.append(TodoPlaceRowItem(id: "more", title: String(localized: "All Projects… [To-dos Places]"),
                                        symbol: Theme.Icon.moreMenu, selected: shown && unlisted, gapBefore: entries.projects.isEmpty,
                                        run: { allProjects = true }))
        }
        return out
    }

    private func go(_ place: TodoPlaceRef) {
        places.open(place)
        open()
    }

    /// When the knowledge base's projects are read: for each assistant made current, and as the places come.
    private struct ProjectsRead: Equatable {
        var switches: Int
        var placed: Bool
    }
}

/// The To-dos row's chevron on the V1 path: folds its places away and back (`sidebar.fold.todos`, kept on the device;
/// it starts unfolded and never folds by itself, so the rows under it never jump). Spoken as what it folds, To-dos, as a
/// Pinned direction's chevron says its direction.
struct TodoPlacesFold: View {
    @Binding var folded: Bool
    /// Laid over the row's end (`overRow`): as tall as the row, not a row's height of its own.
    private var over = false

    init(folded: Binding<Bool>) {
        _folded = folded
    }

    /// The chevron to lay over a row that keeps room for it: the row's height, whatever the style's rows measure.
    func overRow() -> Self {
        var fold = self
        fold.over = true
        return fold
    }

    var body: some View {
        Button {
            withAnimation(Theme.Motion.reduced ? nil : Theme.Motion.state) { folded.toggle() }
        } label: {
            if over {
                glyph
                    .padding(.trailing, Theme.Space.s)
                    .scaledFrame(minWidth: Theme.Size.tapTarget, alignment: .trailing)
                    .frame(maxHeight: .infinity)
                    .contentShape(.rect)
            } else {
                glyph
                    .padding(.trailing, Theme.Space.s)
                    .padding(.vertical, Theme.Sidebar.rowPadding)
                    .scaledFrame(minWidth: Theme.Size.tapTarget, minHeight: Theme.Sidebar.row)
                    .contentShape(.rect)
            }
        }
        .buttonStyle(.plain)
        .accessibilityLabel(String(localized: "To-dos"))
        .accessibilityValue(folded ? String(localized: "Collapsed") : String(localized: "Expanded"))
        .accessibilityIdentifier("sidebar-todos-fold")
    }

    private var glyph: some View {
        ThemedIcon(folded ? Theme.Icon.expand : Theme.Icon.collapse)
            .font(Theme.Font.labelStrong).foregroundStyle(.tertiary)
    }
}
#endif
