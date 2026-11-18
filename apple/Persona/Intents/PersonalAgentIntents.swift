import AppIntents
import CoreSpotlight
import Foundation

// Personal agents (/api/v1/agents/*) for Siri, Shortcuts and Spotlight: pick a session, hand it a task, hear what is
// unread. Everything goes straight to the hub, like the other intents.

/// A personal-agent session as Siri sees it. Indexed in Spotlight when the Agents page loads (`PersonalAgentIndex`).
struct PersonalAgentEntity: IndexedEntity, Hashable, Sendable {
    nonisolated static let typeDisplayRepresentation = TypeDisplayRepresentation(name: "Personal Agents")
    nonisolated static let defaultQuery = PersonalAgentEntityQuery()

    /// The hub's session id, "<source>:<sessionKey>".
    var id: String
    var title: String
    var provider: String
    var unread: Int
    var canSend: Bool

    nonisolated init(id: String, title: String, provider: String = "", unread: Int = 0, canSend: Bool = true) {
        self.id = id
        self.title = title
        self.provider = provider
        self.unread = unread
        self.canSend = canSend
    }

    nonisolated init(_ s: PersonalAgentSession) {
        self.init(id: s.id, title: s.title, provider: s.provider, unread: s.unreadCount, canSend: s.canSend)
    }

    /// The session to open from Spotlight: what the entity knows, until the conversation's first page brings the
    /// hub's copy (`PersonalAgentSessionView` takes it). The id splits at its first ":" (keys contain ":" too).
    nonisolated var session: PersonalAgentSession {
        let colon = id.firstIndex(of: ":")
        return PersonalAgentSession(id: id, source: colon.map { String(id[..<$0]) } ?? "",
                                    sessionKey: colon.map { String(id[id.index(after: $0)...]) } ?? id,
                                    provider: provider, title: title, canSend: canSend, unreadCount: unread, rankTimeMs: 0)
    }

    var displayRepresentation: DisplayRepresentation {
        DisplayRepresentation(title: "\(PersonalAgentIntentText.name(title: title, id: id))",
                              subtitle: "\(PersonalAgentIntentText.subtitle(provider: provider, unread: unread))")
    }
}

struct PersonalAgentEntityQuery: EntityQuery, EntityStringQuery {
    nonisolated init() {}

    nonisolated func entities(for identifiers: [String]) async throws -> [PersonalAgentEntity] {
        let all = try await Self.all()
        return identifiers.map { id in all.first { $0.id == id } ?? PersonalAgentEntity(id: id, title: id) }
    }

    nonisolated func suggestedEntities() async throws -> [PersonalAgentEntity] {
        PersonalAgentIntentPicker.suggested(try await Self.all())
    }

    nonisolated func entities(matching string: String) async throws -> [PersonalAgentEntity] {
        PersonalAgentIntentPicker.matching(string, in: try await Self.all())
    }

    private static func all() async throws -> [PersonalAgentEntity] {
        do {
            return try await IntentSupport.selectedClient().personalAgentSessions().map(PersonalAgentEntity.init)
        } catch {
            throw IntentFailureSentence(IntentFailure.sentence(for: error, host: IntentSupport.host()))
        }
    }
}

/// "Give an Agent a Task": one message into a personal agent session, with a fresh clientId (the hub dedupes retries).
struct AgentTaskIntent: AppIntent {
    nonisolated static let title: LocalizedStringResource = "Give an Agent a Task"
    nonisolated static let description = IntentDescription(
        "Sends a task to a personal agent session (the Lark gateway's agent-bridge) to carry out. With no session chosen, it goes to the most recently active one that can receive messages.",
        categoryName: "Personal Agents",
        searchKeywords: ["Agent", "Personal Agents", "Tasks", "have it do", "task"]
    )
    @available(iOS 26.0, *)
    nonisolated static var supportedModes: IntentModes { .background }

    @Parameter(title: "Session")
    var agent: PersonalAgentEntity?

    @Parameter(title: "Task", requestValueDialog: "What should the agent do?")
    var task: String

    nonisolated init() {}
    nonisolated init(agent: PersonalAgentEntity?, task: String) {
        self.agent = agent
        self.task = task
    }

    nonisolated static var parameterSummary: some ParameterSummary { Summary("Have \(\.$agent) do \(\.$task)") }

    nonisolated func perform() async throws -> some IntentResult & ProvidesDialog & ReturnsValue<String> {
        let body = task.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !body.isEmpty else { throw IntentFailureSentence(String(localized: "What should the agent do?")) }
        do {
            let api = try IntentSupport.selectedClient()
            let target: PersonalAgentEntity
            if let agent { target = agent } else {
                let all = try await api.personalAgentSessions().map(PersonalAgentEntity.init)
                guard let first = PersonalAgentIntentPicker.defaultTarget(all) else {
                    throw IntentFailureSentence(PersonalAgentIntentText.noSession)
                }
                target = first
            }
            guard target.canSend else { throw IntentFailureSentence(PersonalAgentIntentText.cannotSend(target.title)) }
            let r = try await api.sendPersonalAgent(target.id, clientId: UUID().uuidString, text: body)
            let text = PersonalAgentIntentText.sent(status: r.outbox.status, to: target.title, error: r.outbox.error)
            return .result(value: text, dialog: IntentSupport.dialog(text))
        } catch let e as IntentFailureSentence {
            throw e
        } catch {
            throw IntentFailureSentence(IntentFailure.sentence(for: error, host: IntentSupport.host()))
        }
    }
}

/// "Personal Agent Unread": unread counts across personal agent sessions, the busiest first.
struct AgentsStatusIntent: AppIntent {
    nonisolated static let title: LocalizedStringResource = "Personal Agent Unread"
    nonisolated static let description = IntentDescription(
        "Your recent personal agent sessions and how many unread messages each has.",
        categoryName: "Personal Agents",
        searchKeywords: ["Agent", "Unread [Personal Agent Intents 4d0680]", "Recent", "unread"]
    )
    @available(iOS 26.0, *)
    nonisolated static var supportedModes: IntentModes { .background }

    nonisolated init() {}

    nonisolated func perform() async throws -> some IntentResult & ProvidesDialog & ReturnsValue<String> {
        do {
            let all = try await IntentSupport.selectedClient().personalAgentSessions().map(PersonalAgentEntity.init)
            let text = PersonalAgentIntentText.status(all)
            return .result(value: text, dialog: IntentSupport.dialog(text))
        } catch {
            throw IntentFailureSentence(IntentFailure.sentence(for: error, host: IntentSupport.host()))
        }
    }
}

/// Opens the app on one personal agent's conversation (Spotlight results and the dialog buttons land here).
struct OpenPersonalAgentIntent: OpenIntent {
    nonisolated static let title: LocalizedStringResource = "Open Personal Agent"
    @available(iOS 26.0, *)
    nonisolated static var supportedModes: IntentModes { .foreground(.immediate) }
    // iOS 18 has no supportedModes: it opens the app this way.
    nonisolated static var openAppWhenRun: Bool { true }

    @Parameter(title: "Session")
    var target: PersonalAgentEntity

    nonisolated init() {}
    nonisolated init(target: PersonalAgentEntity) { self.target = target }

    nonisolated func perform() async throws -> some IntentResult {
        let session = target.session
        await MainActor.run {
            // Straight into that conversation, over the personal agents list as a tap in the app opens it (#4519).
            // Off the shell's settings sheet, which would cover it, and no chat the last launch kept is pushed first.
            let model = AppModel.shared
            model.shellSettings = false
            model.openChat = nil
            model.selectedTab = .chats
            model.openPersonalAgent = session
        }
        return .result()
    }
}

/// Pure choices, so the tests pin them without a hub.
nonisolated enum PersonalAgentIntentPicker {
    /// Unread first, then the order the hub gave (most recent first).
    static func suggested(_ all: [PersonalAgentEntity]) -> [PersonalAgentEntity] {
        all.filter { $0.unread > 0 } + all.filter { $0.unread == 0 }
    }

    static func matching(_ needle: String, in all: [PersonalAgentEntity]) -> [PersonalAgentEntity] {
        let n = needle.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !n.isEmpty else { return suggested(all) }
        return all.filter { $0.title.localizedCaseInsensitiveContains(n) || $0.provider.localizedCaseInsensitiveContains(n) }
    }

    /// The hub lists sessions newest first; the first one we may send to.
    static func defaultTarget(_ all: [PersonalAgentEntity]) -> PersonalAgentEntity? { all.first(where: \.canSend) }
}

nonisolated enum PersonalAgentIntentText {
    static let noSession = String(localized: "No personal agent session can receive messages yet. Open one in the Lark gateway first.")

    static func name(title: String, id: String) -> String {
        let t = title.trimmingCharacters(in: .whitespacesAndNewlines)
        return t.isEmpty ? id : t
    }

    static func subtitle(provider: String, unread: Int) -> String {
        unread > 0 ? String(localized: "\(provider) · \(unread) unread") : provider
    }

    static func cannotSend(_ title: String) -> String { String(localized: "You can't message “\(title)” from here.") }

    static func sent(status: String, to title: String, error: String?) -> String {
        switch status {
        case "failed": String(localized: "Couldn't send to “\(title)”: \(error ?? String(localized: "Unknown error"))")
        case "sending": String(localized: "Sending to “\(title)”")
        default: String(localized: "Handed to “\(title)”")
        }
    }

    static func status(_ all: [PersonalAgentEntity], limit: Int = 3) -> String {
        guard !all.isEmpty else { return String(localized: "No personal agent sessions.") }
        let unread = all.filter { $0.unread > 0 }
        guard !unread.isEmpty else {
            let recent = all.prefix(limit).map { String(localized: "「\($0.title)」") }
                .joined(separator: String(localized: "、", comment: "Separator between quoted session titles"))
            return String(localized: "Nothing unread. Most recent: \(recent)")
        }
        let total = unread.reduce(0) { $0 + $1.unread }
        let list = unread.prefix(limit).map { String(localized: "“\($0.title)” (\($0.unread))") }
            .joined(separator: String(localized: ", ", comment: "Separator between sessions in the unread list"))
        return String(localized: "\(total) unread in \(unread.count) sessions: \(list)")
    }
}

/// Spotlight: the sessions the Agents page just loaded become searchable (and Siri can suggest them).
nonisolated enum PersonalAgentIndex {
    static func update(_ sessions: [PersonalAgentSession]) {
        let entities = sessions.map(PersonalAgentEntity.init)
        Task.detached(priority: .utility) {
            try? await CSSearchableIndex.default().indexAppEntities(entities)
        }
        PersonaShortcuts.updateAppShortcutParameters()
    }
}
