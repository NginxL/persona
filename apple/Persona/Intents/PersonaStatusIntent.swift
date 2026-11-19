import AppIntents
import Foundation

/// "Persona Status": how many sessions are working and how many are waiting, plus the first question.
struct PersonaStatusIntent: AppIntent {
    nonisolated static let title: LocalizedStringResource = "Persona Status"
    nonisolated static let description = IntentDescription(
        "How many Claude sessions are working, how many are waiting for you, and what the first question is.",
        categoryName: "Status",
        searchKeywords: ["Status", "pending [Persona Status Intent f81ff5]", "pending [Persona Status Intent 999a45]"]
    )
    @available(iOS 26.0, *)
    nonisolated static var supportedModes: IntentModes { .background }

    nonisolated init() {}

    nonisolated func perform() async throws -> some IntentResult & ProvidesDialog & ReturnsValue<String> {
        do {
            // Every group added up (docs/protocol/multi-hub.md「Watch, Siri, Mac, and demo mode」).
            async let sessionsRequest = IntentSupport.sessions()
            async let interactionsRequest = IntentSupport.interactions()
            async let mutesRequest = IntentSupport.mutes()
            let sessions = try await sessionsRequest
            let interactions = try await interactionsRequest
            let mutes = try await mutesRequest
            let text = Self.text(sessions: sessions, interactions: interactions, mutes: mutes)
            return .result(value: text, dialog: IntentSupport.dialog(text))
        } catch {
            throw IntentFailureSentence(IntentFailure.sentence(for: error, host: IntentSupport.host()))
        }
    }

    /// What it says. Working counts the agents the island does (`IntentPicker.working`, #180). Waiting counts the
    /// cards the app shows (muted agents' and quiet tasks' left out) and reads the first of them, the card
    /// ApproveFirstIntent settles (`IntentPicker.waiting`, #5257).
    static func text(sessions: [Session], interactions: [Interaction], mutes: [String: Pins]) -> String {
        let waiting = IntentPicker.waiting(interactions, sessions: sessions, mutes: mutes)
        return IntentText.status(
            working: IntentPicker.working(sessions, mutes: mutes),
            waiting: waiting.cards.count,
            first: waiting.first.map(IntentText.waitingPrompt)
        )
    }
}
