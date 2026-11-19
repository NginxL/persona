import Foundation
import Observation

/// What the hub-level group's hub can do for the To-dos redesign (#10465, docs/apple/ios.md "To-dos (V1, #10465)"):
/// the hello's features, each listed only while every manager behind the hub names it (docs/protocol/protocol.md
/// "Health check and feature flags"). Before this launch's hello, what the group's last hello said (`remembered`), so
/// a list restored at launch is drawn the way it will be.
nonisolated struct TodoFeatures: Equatable, Sendable {
    static let viewName = "todo_view"
    static let placesName = "todo_places"
    static let timelineName = "todo_timeline"
    static let asksName = "todo_asks"

    /// Rows carry `view`, the list `counts`, and `GET /api/v1/todos/{id}` answers; Someday and restore are the hub's.
    var view = false
    /// `GET /api/v1/todos/places`.
    var places = false
    /// `GET /api/v1/todos/{id}/timeline`.
    var timeline = false
    /// `POST /api/v1/todos/{id}/asks/{ask}/answer`.
    var asks = false

    init(view: Bool = false, places: Bool = false, timeline: Bool = false, asks: Bool = false) {
        (self.view, self.places, self.timeline, self.asks) = (view, places, timeline, asks)
    }

    init(_ features: Set<String>) {
        self.init(view: features.contains(Self.viewName), places: features.contains(Self.placesName),
                  timeline: features.contains(Self.timelineName), asks: features.contains(Self.asksName))
    }

    // MARK: Remembered per group

    private var bits: Int { (view ? 1 : 0) | (places ? 2 : 0) | (timeline ? 4 : 0) | (asks ? 8 : 0) }

    /// What `group`'s last hello listed (nothing for a group never heard from).
    static func remembered(_ group: String, in defaults: UserDefaults = .standard) -> TodoFeatures {
        let bits = defaults.integer(forKey: key(group))
        return TodoFeatures(view: bits & 1 != 0, places: bits & 2 != 0, timeline: bits & 4 != 0, asks: bits & 8 != 0)
    }

    /// Notes what `group`'s hello listed; written only when it changes, so rarely (P2).
    static func remember(_ features: TodoFeatures, group: String, in defaults: UserDefaults = .standard) {
        let bits = features.bits
        if defaults.integer(forKey: key(group)) != bits { defaults.set(bits, forKey: key(group)) }
    }

    /// The group's hub is gone (signed out): a hub paired later under the same group starts from nothing.
    static func forget(_ group: String, in defaults: UserDefaults = .standard) {
        if defaults.object(forKey: key(group)) != nil { defaults.removeObject(forKey: key(group)) }
    }

    private static func key(_ group: String) -> String { "hubTodoFeatures." + group }
}

extension HubLink {
    /// What this group's hub can do for To-dos: by its hello, or before this launch's hello by what the last one said.
    var todoFeatures: TodoFeatures { hello.features.isEmpty ? .remembered(group) : TodoFeatures(features) }
}

extension AppModel {
    /// What the hub-level group's hub can do for To-dos (`TodoFeatures`); nothing while no hub is paired.
    var todoFeatures: TodoFeatures { hub?.todoFeatures ?? TodoFeatures() }
}

/// Whether To-dos is the V1 design (#10465; docs/apple/ios.md "To-dos (V1, #10465)", "The gate and its memory"): on
/// while the hub-level group is one whose hello listed `todo_view` in this run, or, before its hello, whose last hello
/// did; and once on for a group, on for it until the app quits. A hello without `todo_view` while a manager reconnects
/// never takes the page back to today's (rows without a view are filed by `TodoFiling`, and the item and places routes
/// answering 404 or 405 are "not there"); picking another group's hub follows that group, so a hub that never listed it
/// keeps today's screens.
@MainActor @Observable
final class TodosV1 {
    static let shared = TodosV1(changed: { TodoStore.shared.v1Changed() }, followsHub: true)

    /// Whether this build shows V1 at all: off until #10465's switch-on, so hubs that send the view keep today's
    /// screens in every build that reaches people before then.
    nonisolated static let released = false
    /// The iPhone and iPad only: visionOS keeps today's To-dos, so it never reads what only V1 draws.
    #if os(iOS)
    nonisolated static let platform = true
    #else
    nonisolated static let platform = false
    #endif

    private(set) var isOn: Bool
    @ObservationIgnored private let allowed: Bool
    @ObservationIgnored private let defaults: UserDefaults
    @ObservationIgnored private let hubGroup: @MainActor () -> String?
    @ObservationIgnored private let features: @MainActor (String) -> TodoFeatures
    @ObservationIgnored private let changed: @MainActor () -> Void
    /// The groups V1 has been on for in this run: it stays on for them.
    @ObservationIgnored private var onGroups: Set<String> = []
    /// The groups whose hello listed `todo_view` in this run: a later hello without it does not change how the next
    /// launch starts.
    @ObservationIgnored private var listed: Set<String> = []
    /// The groups this run has known, forgotten when every hub is signed out.
    @ObservationIgnored private var known: Set<String> = []

    /// `launchGroup`: the hub-level group at launch, read from where the app keeps it rather than from `AppModel`, which
    /// may still be restoring itself when this is first made. `hubGroup`: the hub-level group now (nil: no hub paired).
    /// `features`: what a group's hub can do, by its hello or before it by the last. `changed`: told when `isOn` changes.
    /// `followsHub`: the app's own gate, which follows the hub-level group as the owner picks another.
    init(allowed: Bool = TodosV1.released && TodosV1.platform, defaults: UserDefaults = .standard, launchGroup: String? = nil,
         hubGroup: @escaping @MainActor () -> String? = { AppModel.shared.hub?.group },
         features: @escaping @MainActor (String) -> TodoFeatures = { AppModel.shared.link($0)?.todoFeatures ?? .remembered($0) },
         changed: @escaping @MainActor () -> Void = {}, followsHub: Bool = false) {
        self.allowed = allowed
        self.defaults = defaults
        self.hubGroup = hubGroup
        self.features = features
        self.changed = changed
        let group = launchGroup ?? defaults.string(forKey: AppModel.hubGroupKey) ?? ""
        isOn = allowed && TodoFeatures.remembered(group, in: defaults).view
        if isOn { onGroups.insert(group) }
        // Followed once the app is made: the hub-level group is the app model's, which may be making this gate now.
        if followsHub { Task { @MainActor [weak self] in self?.followHub() } }
    }

    /// A hello from `group` listed `features`: remembered for the next launch, and V1 comes on when it lists
    /// `todo_view` and `group` is the hub-level one. A hello without it after one with it in this run (a manager
    /// reconnecting) is remembered with it, so the next launch starts the way this one ends.
    func heard(_ features: TodoFeatures, from group: String) {
        known.insert(group)
        if features.view { listed.insert(group) }
        var kept = features
        if listed.contains(group) { kept.view = true }
        TodoFeatures.remember(kept, group: group, in: defaults)
        guard allowed, features.view else { return }
        onGroups.insert(group)
        if group == hubGroup() { set(true) }
    }

    /// The hub-level group is (or may be) another: on when V1 has been on for it in this run, or its hub lists
    /// `todo_view` (before this run's hello, its last hello did). No hub paired: every hub was signed out, so nothing
    /// is kept for the groups this run knew.
    func follow() {
        guard let group = hubGroup() else {
            for g in known { TodoFeatures.forget(g, in: defaults) }
            (onGroups, listed, known) = ([], [], [])
            set(false)
            return
        }
        known.insert(group)
        let on = allowed && (onGroups.contains(group) || features(group).view)
        if on { onGroups.insert(group) }
        set(on)
    }

    /// These groups are gone (signed out of, or merged into another) while others stay: nothing is kept for them. The
    /// app's stored pick can still name one at the next launch (only picking a hub writes it), and the gate must not
    /// start that launch from a hub this app no longer has; a hub paired later under one of them decides for itself.
    func forget(_ groups: Set<String>) {
        for g in groups { TodoFeatures.forget(g, in: defaults) }
        onGroups.subtract(groups)
        listed.subtract(groups)
        known.subtract(groups)
    }

    private func set(_ on: Bool) {
        guard on != isOn else { return }
        isOn = on
        changed()
    }

    /// Follows the hub-level group: told once per change, before it lands, so followed again once it has.
    private func followHub() {
        _ = withObservationTracking { hubGroup() } onChange: { [weak self] in
            Task { @MainActor [weak self] in self?.followHub() }
        }
        follow()
    }
}
