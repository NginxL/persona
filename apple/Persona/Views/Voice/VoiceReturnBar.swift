#if os(iOS)
import SwiftUI

/// The bar back to the phone's voice chat away from its page (#9887 one button: leaving the page no longer ends it), as
/// the minimised call's bar: who it is with (the conversation's mark and title, or the assistant's face and name), how
/// long it has been on, its hold or mute. A tap goes back to its conversation, where its own bar is.
struct VoiceReturnPill: View {
    let chat: VoiceConversation
    let model: AppModel
    let open: () -> Void
    /// The assistant's look for its face and the name the main conversation's top bar says, taken when the bar shows (as
    /// a call's are).
    @State private var assistant = CallAssistant(AppModel.shared.persona, name: AppModel.shared.assistantShownName)
    @State private var conversation: CallConversation?
    @Environment(\.dynamicTypeSize) private var typeSize

    var body: some View {
        Button {
            Haptics.tap()
            open()
        } label: {
            HStack(spacing: Theme.Space.s) {
                let side = Theme.Size.callBarAvatar * Theme.sizeScale(typeSize)
                if let agent = conversation?.agent {
                    CallAgentFace(agent: agent, size: side)
                } else {
                    CallAssistantFace(assistant: assistant, size: side)
                }
                // The name cut in the middle, the time never (`CallBarWords`, #9887 polish).
                CallBarWords(title: conversation?.title ?? assistant.name) {
                    if let since = chat.connectedAt {
                        Text(since, style: .timer).monospacedDigit()
                    } else {
                        Text("Connecting…")
                    }
                }
                if chat.held {
                    ThemedIcon(Theme.Icon.pause).font(Theme.Font.captionStrong).foregroundStyle(Theme.caution)
                        .accessibilityHidden(true)
                } else if chat.muted {
                    ThemedIcon(Theme.Icon.voiceMute).font(Theme.Font.captionStrong).foregroundStyle(.secondary)
                        .accessibilityLabel("Muted [Inbox Mutes View e5e468]")
                }
            }
            .padding(.horizontal, Theme.Space.l)
            .tapTargetHeight()
            .surface(tint: Theme.softTint(Theme.positive), interactive: true, in: .capsule)
            .contentShape(.capsule)
        }
        .buttonStyle(.plain)
        .accessibilityElement(children: .combine)
        .accessibilityHint("Returns to the voice chat")
        .accessibilityAddTraits(.isButton)
        .accessibilityIdentifier("voice-return-bar")
        .task(id: chat.key) { conversation = CallConversation.make(chat.key, model: model) }
    }
}
#endif
