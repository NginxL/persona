import Foundation
import Observation
import PersonaWire
#if canImport(UIKit)
import UIKit
#endif

/// Where the owner is in To-dos (#10465; docs/design/proposals/todos-redesign.md 3.3, "Places have identity"): the
/// place, and where each place's list was scrolled to. One for the app (`TodoWorkspace.shared`), outside the pages'
/// trees, so a fold or an unfold that builds the pages again (`RootView`'s trees) finds it as it was.
///
/// - The owner's place is written only while the scene is active: iOS snapshots the app on its way out in other widths,
///   and the pages it builds for them must not move it.
/// - A tree on its way out (a fold or an unfold built another) leaves it to the shown one: the page passes whether its
///   tree is the one shown (`RootView`'s `ShownTree`).
///
/// The places themselves arrive later in #10465 (All only until then), the selection with the to-do's own page.
@MainActor @Observable
final class TodoWorkspace {
    static let shared = TodoWorkspace()

    /// The place shown: All To-dos, Today, Upcoming or a project (`TodoPlace.id`).
    private(set) var place = TodoPlaceKind.allValue
    /// To-dos pages that still draw today's list though the gate is on now (it came on after they were made,
    /// `TodosPlacePage`): while one is up, the drawer's and the sidebar's To-dos number is today's too
    /// (`TodoStore.sidebarCount`), so the page and the drawer never say two numbers.
    private(set) var todayPages: Set<UUID> = []
    /// The row at the top of each place's list when it last came to rest, by place.
    @ObservationIgnored private var scroll: [String: String] = [:]
    @ObservationIgnored private let active: @MainActor () -> Bool

    init(active: @escaping @MainActor () -> Bool = TodoWorkspace.sceneActive) {
        self.active = active
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
        #if canImport(UIKit)
        UIApplication.shared.applicationState == .active
        #else
        true
        #endif
    }
}
