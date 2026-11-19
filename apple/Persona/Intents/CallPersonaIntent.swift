#if os(iOS)
import AppIntents
import Foundation

/// "Call Persona" (#9290, docs/apple/siri.md「Calling Persona」): a call to Persona on the main conversation, the chat's Call
/// from Siri ("Hey Siri, call Persona", AirPods included), Shortcuts, Spotlight or the Action button. An App Shortcut, so
/// no SiriKit entitlement; "call Persona using Persona" is the opt-in SiriKit extension's (apple/siri-calls.yml).
/// It opens the app first: the system takes a call's Start only from an app in front, and the call's page shows there.
/// The call waits for the app to be active before its Start (`PersonaCalls.place`, any origin but `.app`): one started
/// while the app is still coming to the front is refused (LiveCommunicationKit's code 6).
struct CallPersonaIntent: AppIntent {
    nonisolated static let title: LocalizedStringResource = "Call Persona"
    // App Intent metadata says "this device", never "iPhone": App Store Connect rejects it (90626, #6471).
    nonisolated static let description = IntentDescription(
        "Call Persona on your main chat, like a phone call: it keeps going when you switch apps or lock this device, and you hang up from the call or your headphones.",
        categoryName: "Chat",
        searchKeywords: ["Persona", "call", "phone", "voice", "talk"]
    )
    @available(iOS 26.0, *)
    nonisolated static var supportedModes: IntentModes { .foreground(.immediate) }
    // iOS 18 has no supportedModes: it opens the app this way.
    nonisolated static var openAppWhenRun: Bool { true }

    nonisolated init() {}

    nonisolated func perform() async throws -> some IntentResult & ProvidesDialog {
        await MainActor.run { Self.place() }
        return .result(dialog: IntentSupport.dialog(Self.calling))
    }

    /// What Siri says while the call's page comes up (and Siri's own calls say): the page is where it goes on.
    nonisolated static var calling: String { String(localized: "Calling Persona") }

    /// Places the call to Persona from Siri or Shortcuts, as `TalkToPersonaIntent` does on the phone too. Not awaited:
    /// the hub, the voice extension and the microphone prompt may take longer than Siri waits for an answer.
    @MainActor static func place() {
        Task { await PersonaCalls.shared.callPersona(origin: .siri) }
    }
}
#endif
