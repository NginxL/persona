#if os(iOS)
import SwiftUI

/// Everything the call screen shortens, whole (#9887 polish, docs/design/DESIGN_SYSTEM.md "Call screen"): who the call is
/// with (a long conversation or Agent title), whose conversation it is, where the call is (why it ended), and what was
/// said on it, as ⋯ › All Captions lists it. A tap on the call screen's name or its caption opens it; it opens on the
/// name or on the newest caption accordingly. A sheet over the call screen, in the call's window: Minimize and the end
/// of the call take it away with the page.
struct CallDetailsSheet: View {
    /// What was tapped: the sheet opens with that in sight.
    nonisolated enum Opened: String, Identifiable, Sendable {
        case name, caption
        var id: String { rawValue }
    }

    let call: PersonaCall
    let opened: Opened
    @Environment(\.dismiss) private var dismiss

    var body: some View {
        NavigationStack {
            ScrollView {
                VStack(alignment: .leading, spacing: Theme.Space.section) {
                    CallDetailsWho(call: call)
                    CallDetailsCaptions(call: call)
                }
                .frame(maxWidth: .infinity, alignment: .leading)
                .padding(Theme.Space.page)
            }
            .defaultScrollAnchor(opened == .caption ? .bottom : .top)
            .themedPage()
            .navigationTitle("Call Details")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                SheetClose(id: "call-details-close") { dismiss() }
            }
        }
        // The call window's look and the app's text size (capped as every page's): a sheet over it takes the system's
        // own otherwise, past the interface's cap.
        .modifier(CoverLook(model: AppModel.shared))
        .accessibilityIdentifier("call-details")
    }
}

/// The call's name, its conversation's owner and its line, each whole.
private struct CallDetailsWho: View {
    let call: PersonaCall

    var body: some View {
        VStack(alignment: .leading, spacing: Theme.Space.s) {
            Text(verbatim: call.title)
                .font(Theme.Font.title)
                .textSelection(.enabled)
                .accessibilityAddTraits(.isHeader)
                .accessibilityIdentifier("call-details-title")
            if let detail = call.conversation?.detail {
                Text(verbatim: detail)
                    .font(Theme.Font.secondary)
                    .foregroundStyle(.secondary)
            }
            CallStateWords(line: CallScreen.state(call).line)
                .font(Theme.Font.body)
                .foregroundStyle(.secondary)
        }
        .frame(maxWidth: .infinity, alignment: .leading)
    }
}

/// What was said: the voice's captions; a sample has none, its one caption stands in.
private struct CallDetailsCaptions: View {
    let call: PersonaCall

    var body: some View {
        VoiceCaptionList(captions: captions, persona: call.assistant.name)
    }

    private var captions: [VoiceCaption] {
        if let voice = call.voice { return voice.captions }
        #if DEBUG
        if let sample = call.sampleCaption {
            return [VoiceCaption(id: "sample", generation: 0, role: VoiceLine.assistant, text: sample, final: true)]
        }
        #endif
        return []
    }
}
#endif
