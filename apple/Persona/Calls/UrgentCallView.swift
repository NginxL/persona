#if os(iOS)
import SwiftUI

/// The urgent call page (docs/assistant/urgent-call.md): over everything while a call is answered. What the call is
/// about and why Persona called, the card it needs answered with its options to tap, the read-back waiting for Confirm,
/// what was said, and Hang Up. The voice runs the call (`UrgentCallSession`); every choice can also be tapped. Every
/// word of it wraps whole (#9887 polish): the header stays, everything from the title down scrolls between it and the
/// controls.
struct UrgentCallView: View {
    let session: UrgentCallSession

    var body: some View {
        VStack(alignment: .leading, spacing: Theme.Space.section) {
            UrgentCallHeader(session: session)
            ScrollView {
                VStack(alignment: .leading, spacing: Theme.Space.section) {
                    UrgentCallTitle(session: session)
                    UrgentCallMatter(session: session)
                    UrgentCallCard(session: session)
                    UrgentCallCaptions(session: session)
                }
                .frame(maxWidth: .infinity, alignment: .leading)
            }
            .scrollIndicators(.hidden)
            UrgentCallFooter(session: session)
        }
        .padding(Theme.Space.page)
        .themedPage()
        .accessibilityAction(.escape) { UrgentCallCenter.shared.hangUp() }
    }
}

/// Who is calling, as on the call screen: the assistant's small face and its name (the call's own, taken as it rang, as
/// its handle says it: `PersonaCalls.urgentCallerName`, #9887 one name), "Urgent Call" under the name, and how long it has
/// lasted. The name wraps whole, as every word of the page.
private struct UrgentCallHeader: View {
    let session: UrgentCallSession
    @State private var calls = PersonaCalls.shared
    @Environment(\.dynamicTypeSize) private var typeSize

    var body: some View {
        HStack(spacing: Theme.Space.s) {
            // The call's own assistant (its look when it rang), else the one on screen.
            if let assistant = calls.urgentCall(session.id)?.assistant {
                CallAssistantFace(assistant: assistant, size: Theme.Size.callBarAvatar * Theme.sizeScale(typeSize))
            } else {
                PersonaAvatar(size: Theme.Size.callBarAvatar * Theme.sizeScale(typeSize))
            }
            VStack(alignment: .leading, spacing: 0) {
                Text(verbatim: calls.urgentCallerName(session.id))
                    .font(Theme.Font.heading)
                    .fixedSize(horizontal: false, vertical: true)
                Text("Urgent Call").font(Theme.Font.secondaryStrong).foregroundStyle(Theme.danger)
            }
            Spacer(minLength: Theme.Space.s)
            if let answered = session.answeredAt {
                Text(answered, style: .timer).font(Theme.Font.secondary.monospacedDigit()).foregroundStyle(.secondary)
                    .fixedSize()
            }
        }
        .accessibilityElement(children: .combine)
        .accessibilityIdentifier("urgentCall.header")
    }
}

/// The title the lock screen showed, whole: the first thing the page scrolls, so a long one never pushes the card off
/// (it was three lines, cut, at the larger text sizes).
private struct UrgentCallTitle: View {
    let session: UrgentCallSession

    var body: some View {
        Text(session.info?.title ?? session.ring.title)
            .font(Theme.Font.title)
            .fixedSize(horizontal: false, vertical: true)
            .accessibilityAddTraits(.isHeader)
            .accessibilityIdentifier("urgentCall.title")
    }
}

/// Why Persona called: the rule it matched (or the owner's own request) and what is happening.
private struct UrgentCallMatter: View {
    let session: UrgentCallSession

    var body: some View {
        if let info = session.info {
            VStack(alignment: .leading, spacing: Theme.Space.s) {
                Text("Why Persona called").font(Theme.Font.caption).foregroundStyle(.secondary)
                // The rule in full, wrapped: a one-line chip of an owner's long rule pushed the page past the screen.
                if let rule = info.rule, !rule.text.isEmpty {
                    Text(rule.text).font(Theme.Font.caption).foregroundStyle(Theme.danger).fixedSize(horizontal: false, vertical: true)
                } else if info.why == "owner_asked" {
                    Text("You asked to be called").font(Theme.Font.caption).foregroundStyle(.secondary)
                }
                if let what = info.happening ?? info.body, !what.isEmpty {
                    Text(what).font(Theme.Font.body).fixedSize(horizontal: false, vertical: true)
                }
            }
            .padding(Theme.Space.l)
            .frame(maxWidth: .infinity, alignment: .leading)
            .surface(in: .rect(cornerRadius: Theme.Radius.card, style: .continuous))
        }
    }
}

/// The card the call is about: its question and options, or Approve / Reject; then the read-back's Confirm.
private struct UrgentCallCard: View {
    let session: UrgentCallSession

    var body: some View {
        VStack(alignment: .leading, spacing: Theme.Space.m) {
            if let pending = session.pending {
                Text("You chose: \(pending.said)").font(Theme.Font.bodyStrong)
                Button { session.confirmTapped() } label: {
                    Label("Confirm", systemImage: Theme.Icon.done).lineLimit(1).frame(maxWidth: .infinity)
                }
                .themedButton(prominent: true)
                .accessibilityIdentifier("urgentCall.confirm")
            } else if let card = session.card {
                switch card.kind {
                case .question:
                    if let q = card.questions?.first {
                        Text(q.question).font(Theme.Font.bodyStrong)
                        // An option wraps whole (an answer read out to choose from), centred in its button.
                        ForEach(q.options, id: \.label) { option in
                            Button { session.choose(option.label) } label: {
                                Text(QuestionOption.display(option.label).0)
                                    .multilineTextAlignment(.center)
                                    .fixedSize(horizontal: false, vertical: true)
                                    .frame(maxWidth: .infinity)
                            }
                            .themedButton()
                        }
                    }
                case .permission, .plan:
                    if let summary = card.summary ?? card.toolDisplayName {
                        Text(summary).font(Theme.Font.body).fixedSize(horizontal: false, vertical: true)
                    }
                    HStack(spacing: Theme.Space.m) {
                        Button { session.decide(allow: true) } label: {
                            Text("Approve").lineLimit(1).frame(maxWidth: .infinity)
                        }
                        .themedButton(prominent: true)
                        Button { session.decide(allow: false) } label: {
                            Text("Reject").lineLimit(1).frame(maxWidth: .infinity)
                        }
                        .themedButton()
                    }
                default:
                    Text("It's waiting on your phone.").font(Theme.Font.body).foregroundStyle(.secondary)
                }
            }
            if session.tapOnly {
                Label("Persona can't hear you: turn on the microphone and speech recognition for Persona in Settings.", systemImage: Theme.Icon.voiceMute)
                    .font(Theme.Font.caption).foregroundStyle(.secondary)
            }
        }
    }
}

/// What was said on the call, newest at the bottom.
private struct UrgentCallCaptions: View {
    let session: UrgentCallSession

    var body: some View {
        VStack(alignment: .leading, spacing: Theme.Space.s) {
            ForEach(session.lines) { line in
                VStack(alignment: .leading, spacing: Theme.Space.xs) {
                    Text(line.who == .persona ? "Persona" : String(localized: "You")).font(Theme.Font.label).foregroundStyle(.secondary)
                    Text(line.text).font(Theme.Font.secondary).fixedSize(horizontal: false, vertical: true)
                }
                .accessibilityElement(children: .combine)
            }
        }
    }
}

/// Whether Persona is speaking or listening (or the call is on hold), and the call screen's round controls (#9290): Mute,
/// Speaker (or Audio, the system's route picker, while Bluetooth, a car or headphones are there: the call starts on the
/// earpiece), Resume when the call offers it, and Hang Up. They go through `PersonaCalls`, so the system's call UI and
/// AirPods say the same; Hang Up ends the call with the system as before (`UrgentCallCenter.hangUp`).
private struct UrgentCallFooter: View {
    let session: UrgentCallSession
    @State private var calls = PersonaCalls.shared

    var body: some View {
        let call = calls.calls.first { $0.kind.isUrgent && $0.urgentSession === session }
        let muted = call?.muted ?? session.muted
        let held = call?.held ?? session.held
        VStack(spacing: Theme.Space.l) {
            line(held: held, muted: muted)
                .font(Theme.Font.caption).foregroundStyle(.secondary)
                .multilineTextAlignment(.center).fixedSize(horizontal: false, vertical: true)
                .accessibilityIdentifier("urgentCall.state")
            // One row while the controls fit it (four with Resume, at a large text size, do not): else Mute and Speaker
            // over Resume and Hang Up.
            ViewThatFits(in: .horizontal) {
                HStack(alignment: .top, spacing: Theme.Space.xl) {
                    mute(call, muted: muted)
                    speaker
                    resume(call)
                    hangUp
                }
                VStack(spacing: Theme.Space.l) {
                    HStack(alignment: .top, spacing: Theme.Space.xl) {
                        mute(call, muted: muted)
                        speaker
                    }
                    HStack(alignment: .top, spacing: Theme.Space.xl) {
                        resume(call)
                        hangUp
                    }
                }
            }
            .disabled(session.phase == .ended)
        }
        .frame(maxWidth: .infinity)
    }

    private func mute(_ call: PersonaCall?, muted: Bool) -> some View {
        CallControl(symbol: muted ? Theme.Icon.voiceUnmute : Theme.Icon.voiceMute, label: String(localized: "Mute [Voice Bar 7db2e1]"),
                    id: "urgentCall.mute", kind: .toggle(on: muted)) {
            if let call {
                calls.setMuted(!muted, call.id)
            } else {
                // No system call behind the page (the DEBUG sample): the session alone.
                session.setMuted(!muted)
            }
        }
    }

    /// Speaker, or Audio (the system's route picker) while Bluetooth, a car or headphones can take the call.
    @ViewBuilder private var speaker: some View {
        if calls.route.external {
            CallAudioRouteControl(route: calls.route)
        } else {
            CallControl(symbol: Theme.Icon.callSpeaker, label: String(localized: "Speaker [Call Control]"), id: "urgentCall.speaker",
                        kind: .toggle(on: calls.route.speakerOn)) {
                calls.setSpeaker(!calls.route.speakerOn)
            }
        }
    }

    @ViewBuilder private func resume(_ call: PersonaCall?) -> some View {
        if let call, call.resumeOffered {
            CallControl(symbol: Theme.Icon.resume, label: String(localized: "Resume [Call Control]"), id: "urgentCall.resume",
                        hint: String(localized: "Takes the call off hold")) {
                calls.resume(call.id)
            }
        }
    }

    private var hangUp: some View {
        CallControl(symbol: Theme.Icon.voiceEnd, label: String(localized: "Hang Up"), id: "urgentCall.hangUp", kind: .end) {
            UrgentCallCenter.shared.hangUp()
        }
    }

    /// On hold first; muted while Persona listens (it hears nothing); else what the call is doing.
    private func line(held: Bool, muted: Bool) -> Label<Text, Image> {
        if held { return Label(String(localized: "On Hold [Call Screen]"), systemImage: Theme.Icon.pause) }
        if muted, session.phase == .listening || session.phase == .confirming {
            return Label(String(localized: "Muted [Inbox Mutes View e5e468]"), systemImage: Theme.Icon.voiceMute)
        }
        return Label(status.text, systemImage: status.icon)
    }

    private var status: (text: String, icon: String) {
        switch session.phase {
        case .connecting: (String(localized: "Connecting…"), Theme.Icon.voiceConnecting)
        case .speaking: (String(localized: "Persona is speaking"), Theme.Icon.voiceSpeaking)
        case .listening: (String(localized: "Listening"), Theme.Icon.voiceListening)
        case .confirming: (String(localized: "Waiting for “confirm”"), Theme.Icon.voiceListening)
        case .ended: (String(localized: "Call ended"), Theme.Icon.voiceEnded)
        }
    }
}

/// The answered call the page shows, as `UrgentCallCenter` sets it. The cover reads only this, so launching never makes
/// the call center (nor LiveCommunicationKit's manager, which the system calls back on its own queue).
@MainActor @Observable final class UrgentCallPresenter {
    static let shared = UrgentCallPresenter()
    var session: UrgentCallSession?
}

/// Puts the call page over the app while a call is answered.
struct UrgentCallCover: ViewModifier {
    @State private var presenter = UrgentCallPresenter.shared

    func body(content: Content) -> some View {
        content.fullScreenCover(item: Binding(get: { presenter.session }, set: { _ in })) { session in
            UrgentCallView(session: session)
        }
    }
}

extension View {
    func urgentCallCover() -> some View { modifier(UrgentCallCover()) }
}
#endif
