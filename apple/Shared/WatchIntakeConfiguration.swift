#if os(iOS) || os(watchOS)
import AppIntents
import Foundation

nonisolated struct WatchIntakeHub: AppEntity, Hashable, Sendable {
    static let typeDisplayRepresentation = TypeDisplayRepresentation(name: "Hub")
    static let defaultQuery = WatchIntakeHubQuery()
    var id: String
    var name: String
    var group: String { String(id.dropFirst("group:".count)) }
    init(group: String, name: String) { self.id = "group:" + group; self.name = name }
    var displayRepresentation: DisplayRepresentation { DisplayRepresentation(title: "\(name)") }
}

nonisolated struct WatchIntakeHubQuery: EntityQuery {
    func suggestedEntities() async throws -> [WatchIntakeHub] {
        ExtensionSurfaceStore.shared.read().groups.sorted { $0.key < $1.key }.map {
            WatchIntakeHub(group: $0.key, name: $0.value.credential.name)
        }
    }
    func entities(for identifiers: [String]) async throws -> [WatchIntakeHub] {
        try await suggestedEntities().filter { identifiers.contains($0.id) }
    }
}

struct WatchIntakeConfiguration: WidgetConfigurationIntent {
    nonisolated static let title: LocalizedStringResource = "Health Intake [Watch Widgets]"
    @Parameter(title: "Hub") var hub: WatchIntakeHub?
    nonisolated init() {}
    nonisolated init(hub: WatchIntakeHub) { self.hub = hub }
}
#endif
