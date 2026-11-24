import Foundation
import Observation
import PersonaWire
import UIKit

/// Where the owner is in To-dos (#10465; docs/design/proposals/todos-redesign.md 3.3, "Places have identity"): the
/// place, and where each place's list was scrolled to. One for the app (`TodoWorkspace.shared`), outside the pages'
/// trees, so a fold or an unfold that builds the pages again (`RootView`'s trees) finds it as it was.
///
/// - The owner's place is written only while the scene is active: iOS snapshots the app on its way out in other widths,
///   and the pages it builds for them must not move it.
/// - A tree on its way out (a fold or an unfold built another) leaves it to the shown one: the page passes whether its
///   tree is the one shown (`RootView`'s `ShownTree`).
///
/// The place is picked in the sidebar or the list's title menu (`TodosPlacesModel`) and kept for the app's run, so
/// To-dos reopens on it (proposal 3.3, option 2); the selection arrives with the to-do's own page.
@MainActor @Observable
final class TodoWorkspace {
    static let shared = TodoWorkspace()

    /// The place shown: All To-dos, Today, Upcoming or a project (`TodoPlace.id`).
    private(set) var place = TodoPlaceKind.allValue
    /// Sidebar picks that changed the place, and how many of them an opening of To-dos has taken (`takePick`).
    @ObservationIgnored private var picks = 0
    @ObservationIgnored private var taken = 0
    /// The sidebar's To-dos row itself was picked, not one of its places: the opening that follows decides (`opening`).
    @ObservationIgnored private var rowPicked = false
    /// To-dos pages that still draw today's list though the gate is on now (it came on after they were made,
    /// `TodosPlacePage`): while one is up, the drawer's and the sidebar's To-dos number is today's too
    /// (`TodoStore.sidebarCount`), so the page and the drawer never say two numbers.
    private(set) var todayPages: Set<UUID> = []
    /// The row at the top of each place's list when it last came to rest, by place.
    @ObservationIgnored private var scroll: [String: String] = [:]
    @ObservationIgnored private let active: @MainActor () -> Bool

    init(active: @escaping @MainActor () -> Bool = TodoWorkspace.sceneActive) {
        self.active = active
        #if DEBUG
        // `-openTodoPlace today|upcoming|project:<slug>`: To-dos opens on that place (screenshots, UI tests).
        if let launched = UserDefaults.standard.string(forKey: "openTodoPlace"), !launched.isEmpty { place = launched }
        #endif
    }

    /// The owner picked `place`. Returns whether it changed.
    @discardableResult
    func pick(_ place: String) -> Bool {
        guard place != self.place, active() else { return false }
        self.place = place
        return true
    }

    /// The owner picked `place` in the sidebar, which then opens To-dos: what To-dos kept pushed belongs to the place
    /// before, so that opening shows the place at its root, without a pop (`takePick`).
    func open(_ place: String) {
        rowPicked = false
        if pick(place) { picks += 1 }
    }

    /// The owner picked the sidebar's To-dos row itself, which then opens To-dos (`opening`).
    func pickRow() {
        rowPicked = true
    }

    /// To-dos opens: `inFront`, it was the place in front already, whatever is pushed on it (the host's own state: the
    /// shell's place, the Sidebar layout's root, the classic page). After its own row it is All To-dos there, its root
    /// as the tab-bar rule has it, and from anywhere else To-dos as it was left, on its last place (proposal 3.3,
    /// DESIGN_SYSTEM 3.5 "To-dos places"). Returns whether To-dos opens for a place picked since it last opened, which
    /// then starts at its root (`takePick`).
    func opening(inFront: Bool) -> Bool {
        if rowPicked {
            rowPicked = false
            if inFront { open(TodoPlaceKind.allValue) }
        }
        return takePick()
    }

    /// Whether To-dos opens for a place the sidebar picked since it last opened; taken once.
    func takePick() -> Bool {
        defer { taken = picks }
        return picks != taken
    }

    /// Whether a To-dos page still draws today's list.
    var holdsToday: Bool { !todayPages.isEmpty }

    /// The row the list of `place` was left at.
    func scrolled(in place: String) -> String? { scroll[place] }

    /// The list of `place` came to rest with `key` at its top (nil: at its start).
    func rested(_ place: String, at key: String?, shown: Bool) {
        guard shown, active() else { return }
        scroll[place] = key
    }

    /// A To-dos page that draws today's list is up (`open`), or gone.
    func todayPage(_ id: UUID, open: Bool) {
        if open { todayPages.insert(id) } else { todayPages.remove(id) }
    }

    static func sceneActive() -> Bool {
        UIApplication.shared.applicationState == .active
    }
}
