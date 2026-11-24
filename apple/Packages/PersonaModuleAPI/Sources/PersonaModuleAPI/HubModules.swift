import Foundation

/// What a hub says about its modules in its hello (#13849; the wire rule agreed on #13844): which modules with a
/// `feature` it lets the app turn on. The two wire names live here and nowhere else.
///
/// - A module's feature flag is `module.<id>` (`ModuleDescriptor.hubFeature(for:)`).
/// - A hub that publishes module flags also announces the marker `modules`.
/// - A module with a feature is off only when the hub announces `modules` and not the module's flag. An older hub (no
///   `modules` in its hello) leaves every module on, and so does a hub never heard from.
///
/// Before this launch's hello the app uses the answer it remembered for that hub (`stored`), so a module does not
/// appear and then vanish on launch.
public struct HubModules: Hashable, Sendable {
    /// The marker flag of a hub that publishes module flags.
    public static let marker = "modules"
    /// The prefix of every module flag.
    public static let prefix = "module."

    /// The module flags the hub announced; nil when it does not publish module flags (an older hub, or no answer yet).
    public let published: Set<String>?

    /// Every module on: an older hub, or nothing heard from this hub.
    public static let everyOn = HubModules(published: nil)

    private init(published: Set<String>?) { self.published = published }

    /// From a hello's `features`. An empty list says nothing (a hello that carries no features at all), as for the
    /// app's other hello-gated features: the caller keeps what it remembered.
    public init(hello features: some Sequence<String>) {
        let all = Set(features)
        published = all.contains(Self.marker) ? all.filter { $0.hasPrefix(Self.prefix) } : nil
    }

    /// Whether a module may be active: true unless its feature is gated by a hub that publishes module flags and did
    /// not announce this one.
    public func allows(_ module: ModuleDescriptor) -> Bool {
        guard let feature = module.feature, let published else { return true }
        return published.contains(feature)
    }

    /// The form kept per hub: the announced module flags, sorted, or nil for an older hub (nothing to keep).
    public var stored: [String]? { published.map { $0.sorted() } }

    /// The remembered answer: `stored` read back. nil (never heard, or an older hub) is every module on.
    public init(stored: [String]?) { published = stored.map(Set.init) }
}
