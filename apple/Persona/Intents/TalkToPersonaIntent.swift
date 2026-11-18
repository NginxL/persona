import AppIntents
import Foundation

/// "Talk to Persona": the hands-free voice conversation on the CarPlay screen when Persona's voice screen is in the car
/// (`VoiceConversation(surface: .carPlay)` via `VoiceCalls`, in the background as before). With no car connected it is a
/// call to Persona on the phone (#9290), as Call Persona (`CallPersonaIntent`) places it: the app comes to the front for
/// it. CarPlay connected but Persona not on the car's screen (another app there, or Persona not opened there since it
/// launched) still says to open it there: a call that needs the phone unlocked is the wrong answer while driving.
struct TalkToPersonaIntent: AppIntent {
    nonisolated static let title: LocalizedStringResource = "Talk to Persona"
    #if os(iOS)
    // App Intent metadata says "this device", never "iPhone": App Store Connect rejects it (90626, #6471).
    nonisolated static let description = IntentDescription(
        "Talk with Persona hands-free: on CarPlay, a continuous voice chat on the car's screen; elsewhere, a call to Persona on this device. You talk, it answers, and you can cut in anytime.",
        categoryName: "Chat",
        searchKeywords: ["Persona", "voice", "talk", "chat", "CarPlay", "call"]
    )
    #else
    // No call to Persona here (`PersonaCalls` is the phone's): only what CarPlay does, as before #9290.
    nonisolated static let description = IntentDescription(
        "Start a continuous, hands-free voice chat with Persona on CarPlay: you talk, it answers, and you can cut in anytime.",
        categoryName: "Chat",
        searchKeywords: ["Persona", "voice", "talk", "chat", "CarPlay"]
    )
    #endif
    #if os(iOS)
    // In the background for CarPlay (the phone may stay locked in the car); in front only for the call on the phone.
    nonisolated static var supportedModes: IntentModes { [.background, .foreground(.dynamic)] }
    #else
    nonisolated static var supportedModes: IntentModes { .background }
    #endif

    nonisolated init() {}

    #if os(iOS)
    nonisolated func perform() async throws -> some IntentResult & ProvidesDialog {
        // Connected is not enough: the voice screen must be the one in the car while the microphone is on. And no scene of
        // Persona's is not no car: the audio route says whether the phone is in one (`talkNow`).
        let talk = await CarPlayVoicePresence.talkNow()
        // A call to Persona already on the phone goes on: in the car, the car's voice chat would end it, so the car is
        // told where it is (#9290 follow-up); without a car, the call screen shows it (`callPersona` shows a live call).
        if !talk.callsOnThePhone, let onPhone = await MainActor.run(body: { CarPlayVoiceSession.onPhoneCall() }) {
            return .result(dialog: IntentSupport.dialog(onPhone))
        }
        if talk.callsOnThePhone {
            // The system takes a call's Start only from an app in front; the call's page shows there.
            try await continueInForeground(alwaysConfirm: false)
            await MainActor.run { CallPersonaIntent.place() }
            return .result(dialog: IntentSupport.dialog(CallPersonaIntent.calling))
        }
        guard talk == .start else {
            return .result(dialog: IntentSupport.dialog(talk.sentence))
        }
        // Give Siri a moment to finish its own sentence before the microphone opens; the user may leave meanwhile.
        Task { @MainActor in
            try? await Task.sleep(for: .seconds(1.5))
            guard CarPlayVoicePresence.talk == .start else { return }
            _ = await CarPlayVoiceSession.start()
        }
        return .result(dialog: IntentSupport.dialog(talk.sentence))
    }
    #else
    // macOS has no CarPlay: the same sentence as the unconnected case.
    nonisolated func perform() async throws -> some IntentResult & ProvidesDialog {
        .result(dialog: IntentSupport.dialog(CarPlayTalk.notConnected.sentence))
    }
    #endif
}
