import AppIntents
import Foundation

/// A session as Siri sees it, so `AskClaudeIntent` can hand the phone straight to that chat.
struct SessionEntity: AppEntity, Hashable, Sendable {
    nonisolated static let typeDisplayRepresentation = TypeDisplayRepresentation(name: "Session")
    nonisolated static let defaultQuery = SessionEntityQuery()

    /// `machine/session` (`group|machine/session` for another group of hubs): the same shape as `-openSession`.
    var id: String
    var machineID: String
    var sessionID: String
    var title: String
    var group: String = ""

    nonisolated init(machineID: String, sessionID: String, title: String, group: String = "") {
        self.id = SessionKey(group: group, machineID: machineID, sessionID: sessionID).path
        self.machineID = machineID
        self.sessionID = sessionID
        self.title = title
        self.group = group
    }

    nonisolated init(_ session: Session) {
        self.init(machineID: session.machineID, sessionID: session.sessionID, title: session.title, group: session.group)
    }

    var key: SessionKey { SessionKey(group: group, machineID: machineID, sessionID: sessionID) }

    var displayRepresentation: DisplayRepresentation {
        DisplayRepresentation(title: "\(title.isEmpty ? sessionID : title)", subtitle: "\(machineID)")
    }
}

struct SessionEntityQuery: EntityQuery {
    nonisolated init() {}

    nonisolated func entities(for identifiers: [String]) async throws -> [SessionEntity] {
        let all = try await Self.all()
        return identifiers.map { id in all.first { $0.id == id } ?? Self.unresolved(id) }
    }

    /// A saved session the hub does not list right now (its machine offline, say): rebuilt from its id, so it still
    /// opens that session in its group. Only an id that is not `machine/session` keeps the old shape.
    nonisolated static func unresolved(_ id: String) -> SessionEntity {
        guard let key = SessionKey(path: id) else { return SessionEntity(machineID: id, sessionID: "", title: id) }
        return SessionEntity(machineID: key.machineID, sessionID: key.sessionID, title: "", group: key.group)
    }

    nonisolated func suggestedEntities() async throws -> [SessionEntity] {
        try await Self.all().filter { $0.sessionID != "" }
    }

    private static func all() async throws -> [SessionEntity] {
        do {
            return try await IntentSupport.sessions().map(SessionEntity.init)
        } catch {
            throw IntentFailureSentence(IntentFailure.sentence(for: error, host: IntentSupport.host()))
        }
    }
}

/// Opens the app on one chat. The app is in the foreground here, so touching `AppModel` is fine.
struct OpenSessionIntent: OpenIntent {
    nonisolated static let title: LocalizedStringResource = "Open Session"
    @available(iOS 26.0, *)
    nonisolated static var supportedModes: IntentModes { .foreground(.immediate) }
    // iOS 18 has no supportedModes: it opens the app this way.
    nonisolated static var openAppWhenRun: Bool { true }

    @Parameter(title: "Session")
    var target: SessionEntity

    nonisolated init() {}

    nonisolated init(target: SessionEntity) { self.target = target }

    nonisolated func perform() async throws -> some IntentResult {
        let key = target.key
        await MainActor.run {
            // As a notification opens it: a Persona conversation on Assistant, an Agent's on Agents.
            let model = AppModel.shared
            model.openExternal(machineID: key.machineID, sessionID: key.sessionID, group: key.hubGroup)
        }
        return .result()
    }
}
