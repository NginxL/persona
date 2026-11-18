import SwiftUI

/// ⋯ › All Captions (docs/assistant/voice.md 6.2): everything the call heard and said so far (its last 40 lines), oldest first. Only
/// in this call: nothing is saved, and the conversation keeps the instructions and the answers as ever.
struct VoiceTranscriptSheet: View {
    let call: VoiceConversation
    let persona: String
    @Environment(\.dismiss) private var dismiss

    var body: some View {
        NavigationStack {
            ScrollView {
                VoiceCaptionList(captions: call.captions, persona: persona)
                    .padding(Theme.Space.page)
            }
            .defaultScrollAnchor(.bottom)
            .themedPage()
            .navigationTitle("Captions")
            #if os(iOS) || os(visionOS)
            .navigationBarTitleDisplayMode(.inline)
            #endif
            #if os(macOS)
            .macSheetDone { dismiss() }
            #else
            .toolbar {
                SheetClose { dismiss() }
            }
            #endif
        }
        .accessibilityIdentifier("voice-transcript")
    }
}

/// A call's captions in full, oldest first, under the note that they last only for this call: All Captions' list, and
/// the system call's details (`CallDetailsSheet`, #9887 polish).
struct VoiceCaptionList: View {
    let captions: [VoiceCaption]
    let persona: String

    var body: some View {
        VStack(alignment: .leading, spacing: Theme.Space.m) {
            Text("Captions last only for this call and aren’t saved. Instructions and answers stay in the chat as usual.")
                .font(Theme.Font.caption)
                .foregroundStyle(.secondary)
            ForEach(captions) { caption in
                VStack(alignment: .leading, spacing: Theme.Space.xxs) {
                    Text(caption.isUser ? String(localized: "You") : persona)
                        .font(Theme.Font.labelStrong)
                        .foregroundStyle(caption.isUser ? Color.secondary : Theme.persona)
                    Text(caption.text)
                        .font(Theme.Font.body)
                        .foregroundStyle(caption.final ? Color.primary : Color.secondary)
                        .textSelection(.enabled)
                }
                .frame(maxWidth: .infinity, alignment: .leading)
                .accessibilityElement(children: .combine)
            }
            if captions.isEmpty {
                EmptyStateView(symbol: Theme.Icon.voiceCaptions, title: String(localized: "No Captions Yet"),
                               message: String(localized: "Say something and captions will appear here."))
            }
        }
    }
}
