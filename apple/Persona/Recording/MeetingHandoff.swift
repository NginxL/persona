#if os(iOS) || os(visionOS)
import SwiftUI

/// Handing a meeting to an Agent after it (#8852, docs/assistant/meeting-minutes.md "Handing a meeting to an Agent
/// (#8852)"): the meeting's page offers **Hand to Agent…** in its ⋯ once the meeting is over and has something to hand,
/// and only to someone with an Agent that can take it. The hub puts the meeting's name, time and minutes (the transcript
/// too when asked) before the owner's line and sends it to the Agent picked (`POST /api/v1/recordings/{id}/handoff`);
/// each try is kept in `detail.meta.handoffs`, the last one shown at the top of the page: where it went (Open its
/// conversation), or why it didn't (Retry).
nonisolated struct MeetingHandoff: Decodable, Equatable, Sendable {
    var id: String
    var at: String
    var machineID: String
    var sessionID: String
    /// The Agent's title when it was handed; nil when the app showed none.
    var title: String?
    /// The transcript went too.
    var transcript: Bool?
    /// `sent` or `failed`.
    var state: String
    /// Why a failed one didn't go: `not_agent`, `not_allowed`, `unavailable`, `send_failed`.
    var problem: String?

    enum CodingKeys: String, CodingKey {
        case id, at, machineID = "machine_id", sessionID = "session_id", title, transcript, state, problem
    }

    var failed: Bool { state == "failed" }

    /// The Agent's name on the page's row.
    var name: String {
        let t = (title ?? "").trimmingCharacters(in: .whitespacesAndNewlines)
        return t.isEmpty ? String(localized: "Agent") : t
    }

    /// Why it didn't go, in the owner's words; an unknown problem as a failed send.
    var problemText: String { Self.problemText(problem) }

    static func problemText(_ problem: String?) -> String {
        switch problem {
        case "not_agent": String(localized: "That's a Persona chat, not an Agent.")
        case "not_allowed": String(localized: "This Agent can't take meetings.")
        case "unavailable": String(localized: "Its computer isn't reachable, or the chat is closed.")
        default: String(localized: "The message didn't go through.")
        }
    }

    /// The latest of `handoffs` this app can read (oldest first, as the hub keeps them); nil without one.
    static func latest(_ handoffs: [Lossy<MeetingHandoff>]?) -> MeetingHandoff? {
        (handoffs ?? []).compactMap(\.value).filter { $0.state == "sent" || $0.state == "failed" }.last
    }

    /// The newer of the one made on this page and the detail's (`at` is RFC 3339 UTC, so the strings order as times).
    static func newer(_ made: MeetingHandoff?, _ kept: MeetingHandoff?) -> MeetingHandoff? {
        guard let made else { return kept }
        guard let kept else { return made }
        return made.at >= kept.at ? made : kept
    }

    nonisolated struct Body: Encodable, Sendable {
        var machineID: String
        var sessionID: String
        var title: String
        var text: String
        var transcript: Bool
        enum CodingKeys: String, CodingKey { case machineID = "machine_id", sessionID = "session_id", title, text, transcript }
    }

    nonisolated struct Answer: Decodable, Sendable { var handoff: MeetingHandoff? }

    /// `POST /api/v1/recordings/{id}/handoff`: the try as the hub kept it, sent or failed with its problem.
    static func send(_ recording: UUID, _ body: Body, api: APIClient) async throws -> MeetingHandoff? {
        #if os(iOS)
        try MeetingConversation.refuseReplayed(recording)
        #endif
        let answer: Answer = try await api.post("/api/v1/recordings/\(recording.uuidString.lowercased())/handoff", body: body)
        return answer.handoff
    }

    /// Why the hub took no hand-off at all: the meeting still going, nothing to hand, or its own words.
    static func callError(_ error: Error) -> String {
        if let e = error as? APIError {
            if e.code == "meeting_running" { return String(localized: "The meeting is still going. Hand it over once it ends.") }
            if e.code == "no_material" { return String(localized: "This meeting has nothing to hand over yet: no minutes or transcript.") }
        }
        return error.shown ?? error.localizedDescription
    }
}

#if os(iOS)
extension MeetingHandoff {
    /// The Agents a meeting can be handed to, latest first: Hand to AI's (taking messages, live, on a computer online
    /// now), of the hub the meeting is on, that may hear a meeting (`MeetingAttendees.mayAttend`: not Allow All, not a
    /// work computer, not another manager's), and not a subagent or one cut off by a restart, as the hub refuses those.
    @MainActor static func agents(_ model: AppModel) -> [Session] {
        let group = model.hub?.group ?? ""
        return InboxHandOffModel.agents(model.allSessions, limit: .max) { model.machine($0)?.online == true }
            .filter { $0.key.group == group && $0.isSubagent != true && !$0.wasInterrupted && MeetingAttendees.mayAttend($0, model: model) }
    }
}

/// Hand to Agent (#8852): the sheet, with the failed hand-off it tries again.
struct MeetingHandoffAsk: Identifiable {
    let id = UUID()
    var again: MeetingHandoff?
}

/// **Hand to Agent…** in the meeting page's ⋯, only while the meeting has something to hand and the owner has an Agent
/// to take it (8852-4: someone who only uses the assistant never sees it). A view of its own, so the list of sessions it
/// reads redraws only this item (P5).
struct MeetingHandoffMenuItem: View {
    @Environment(AppModel.self) private var model
    var material: Bool
    var action: () -> Void

    var body: some View {
        if material && !MeetingHandoff.agents(model).isEmpty {
            Button(action: action) { Label("Hand to Agent…", systemImage: Theme.Icon.handToAI) }
                .accessibilityIdentifier("meeting-after-handoff")
        }
    }
}

/// The meeting's latest hand-off at the top of its page: where it went, with Open; or why it didn't, with Retry while the
/// meeting has something to hand and there is an Agent to pick.
struct MeetingHandoffRow: View {
    @Environment(AppModel.self) private var model
    var handoff: MeetingHandoff
    var mayRetry: Bool
    var open: () -> Void
    var retry: () -> Void

    var body: some View {
        HStack(spacing: Theme.Space.s) {
            ThemedIcon(handoff.failed ? Theme.Icon.errorFilled : Theme.Icon.handToAI)
                .font(Theme.Font.labelStrong)
                .foregroundStyle(handoff.failed ? Theme.danger : .secondary)
            VStack(alignment: .leading, spacing: Theme.Space.xxs) {
                Text(handoff.failed ? String(localized: "Couldn't hand to “\(handoff.name)”")
                                    : String(localized: "Handed to “\(handoff.name)”"))
                    .font(Theme.Font.secondaryMedium)
                    .lineLimit(1)
                if handoff.failed {
                    Text(handoff.problemText).font(Theme.Font.label).foregroundStyle(.secondary).lineLimit(2)
                }
            }
            .frame(maxWidth: .infinity, alignment: .leading)
            Group {
                if handoff.failed {
                    if mayRetry && !MeetingHandoff.agents(model).isEmpty {
                        Button(String(localized: "Retry"), action: retry)
                            .accessibilityIdentifier("meeting-handoff-retry")
                    }
                } else {
                    Button(String(localized: "Open"), action: open)
                        .accessibilityIdentifier("meeting-handoff-open")
                }
            }
            .font(Theme.Font.secondaryStrong)
            .lineLimit(1)
            .buttonStyle(.borderless)
        }
        .padding(.horizontal, Theme.Space.m)
        .padding(.vertical, Theme.Space.s)
        .surface(in: .rect(cornerRadius: Theme.Radius.card, style: .continuous))
        .accessibilityElement(children: .contain)
        .accessibilityIdentifier("meeting-handoff-row")
    }
}

/// Picks an Agent, an optional line and whether the transcript goes too, then hands the meeting over. From a failed
/// hand-off's Retry its Agent and transcript choice come back picked.
struct MeetingHandoffSheet: View {
    @Environment(AppModel.self) private var model
    @Environment(\.dismiss) private var dismiss
    var recording: UUID
    /// The recording still has its transcript: the switch is offered.
    var transcriptOffered: Bool
    var again: MeetingHandoff?
    /// The hub kept the try (sent, or failed and why): the page shows it at once.
    var done: (MeetingHandoff) -> Void

    /// Read once as the sheet opens: an Agent coming or going while the owner writes doesn't move the list (P3: set in
    /// `onAppear`, not as the initial value).
    @State private var agents: [Session] = []
    @State private var picked: SessionKey?
    @State private var instruction = ""
    @State private var transcript = false
    @State private var sending = false
    @State private var problem: String?

    var body: some View {
        NavigationStack {
            Form {
                Section {
                    ForEach(agents) { s in
                        Button { picked = s.key } label: {
                            HStack(spacing: Theme.Space.s) {
                                StatusDot(state: s.state, size: Theme.Size.dotL)
                                VStack(alignment: .leading, spacing: Theme.Space.xxs) {
                                    Text(s.displayTitle).font(Theme.Font.bodyStrong).foregroundStyle(Color.primary).lineLimit(1)
                                    // Color.secondary, not .secondary: inside the row's Button the latter is the tint's.
                                    Text("\(model.machine(s.machineRef)?.title ?? s.machineID) · \(s.folderName)")
                                        .font(Theme.Font.caption).foregroundStyle(Color.secondary).lineLimit(1)
                                }
                                Spacer()
                                if picked == s.key { ThemedIcon(Theme.Icon.done).foregroundStyle(Theme.accent) }
                            }
                        }
                        .accessibilityAddTraits(picked == s.key ? .isSelected : [])
                        .accessibilityIdentifier("meeting-handoff-agent")
                    }
                } header: {
                    Text("Pick an Agent")
                }
                Section {
                    TextField(String(localized: "What should it do? (optional)"), text: $instruction, axis: .vertical)
                        .lineLimit(2...6)
                        .accessibilityIdentifier("meeting-handoff-instruction")
                }
                Section {
                    if transcriptOffered {
                        Toggle("Include the Transcript", isOn: $transcript)
                            .accessibilityIdentifier("meeting-handoff-transcript")
                    }
                } footer: {
                    Text("It gets this meeting's minutes: the summary, decisions, to-dos and open questions.")
                }
            }
            .themedPage()
            .navigationTitle("Hand to Agent")
            .navigationBarTitleDisplayMode(.inline)
            .sheetCancel(unsaved: !instruction.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty) { dismiss() }
            .safeAreaInset(edge: .bottom) {
                VStack(spacing: Theme.Space.s) {
                    // Why a try didn't go, over the button: in view however far the form is scrolled.
                    if let problem {
                        Label(problem, systemImage: Theme.Icon.errorFilled)
                            .font(Theme.Font.caption)
                            .foregroundStyle(Theme.danger)
                            .frame(maxWidth: .infinity, alignment: .leading)
                            .accessibilityElement(children: .combine) // the mark and the reason, read as one
                            .accessibilityIdentifier("meeting-handoff-problem")
                    }
                    Button { Task { await send() } } label: {
                        HStack(spacing: Theme.Space.s) {
                            if sending { ProgressView().tint(.white) }
                            ThemedIcon(Theme.Icon.handToAI)
                            Text("Hand to Agent")
                        }
                        .font(Theme.Font.heading)
                        .lineLimit(1)
                        .scaledFrame(maxWidth: .infinity, minHeight: Theme.Size.wellS)
                    }
                    .themedButton(prominent: true)
                    .controlSize(.large)
                    .disabled(sending || picked == nil)
                    .accessibilityIdentifier("meeting-handoff-send")
                }
                .padding(.horizontal, Theme.Space.page)
                .padding(.vertical, Theme.Space.s)
                .background(Theme.pageBackground)
            }
        }
        .onAppear {
            guard agents.isEmpty else { return }
            agents = Array(MeetingHandoff.agents(model).prefix(8))
            let back = again.flatMap { a in agents.first { $0.machineID == a.machineID && $0.sessionID == a.sessionID } }
            picked = (back ?? agents.first)?.key
            transcript = transcriptOffered && again?.transcript == true
        }
    }

    private func send() async {
        guard let key = picked, let s = agents.first(where: { $0.key == key }), let api = model.api, !sending else { return }
        sending = true
        problem = nil
        defer { sending = false }
        let line = instruction.trimmingCharacters(in: .whitespacesAndNewlines)
        do {
            let body = MeetingHandoff.Body(machineID: s.machineID, sessionID: s.sessionID, title: s.displayTitle, text: line,
                                           transcript: transcriptOffered && transcript)
            guard let kept = try await MeetingHandoff.send(recording, body, api: api) else { return }
            done(kept)
            if kept.failed {
                // kept as failed: said here, where another Agent can be picked
                problem = kept.problemText
                Haptics.error()
                return
            }
            Haptics.success()
            // No toast: the page's row says where it went, with Open (a toast covered the meeting's title).
            dismiss()
        } catch {
            problem = MeetingHandoff.callError(error)
            Haptics.error()
        }
    }
}
#endif
#endif
