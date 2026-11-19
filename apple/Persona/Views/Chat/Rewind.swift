import SwiftUI

/// Rewind to Here (#2915, the web's #2436): go back in a Claude session to before one of your messages. The node drives
/// Claude Code's own /rewind in the session's terminal (server/internal/node/rewind.go), so what the app says is which
/// message: its text, which of the messages with that text counted from the end, and how many of your messages came
/// after it. Claude's list and the app's transcript are both read from the end, so older pages not loaded yet change
/// nothing. The node checks all three against its screen and stops, changing nothing, when they disagree.
nonisolated enum Rewind {
    enum Restore: String, Sendable, CaseIterable {
        case conversation
        case codeAndConversation = "code_and_conversation"

        var title: String {
            switch self {
            case .conversation: String(localized: "Rewind Chat Only")
            case .codeAndConversation: String(localized: "Rewind Code and Chat")
            }
        }
    }

    struct Target: Equatable, Sendable {
        /// The message as the transcript has it (Claude's own words for it, tags and all).
        var prompt: String
        /// It is the Nth message with this text counting from the end (1 = the last one).
        var occurrence: Int
        /// How many of your messages came after it.
        var promptsAfter: Int
    }

    /// One message of yours, as Claude Code lists it in /rewind: the lines of one transcript entry are one message.
    struct Prompt: Equatable {
        var entry: String
        var raw: String
        /// One of its items was cut by the hub for the page (`Item.fullSize`): the text here is not the whole.
        var cut: Bool
    }

    /// What the chat's session offers once Rewind to Here is tapped (#6294): whose words its lines are, and whether its
    /// files can go back too.
    struct Offer: Equatable, Sendable {
        /// The node declared rewind (`capabilities.rewind`: a runtime's session, a Codex thread). Its lines are its own
        /// (`<seq>#<n>`, not Claude's uuids), and there is no Claude input box on the computer to speak of.
        var declared = false
        /// Rewind Code and Chat is offered too (`Session.rewindsCode`).
        var code = true
    }

    static func offer(_ session: Session?) -> Offer {
        Offer(declared: session?.capabilities?.rewind == true, code: session?.rewindsCode ?? true)
    }

    /// `<transcript line uuid>#<n>` → the line's uuid; nil for items that are not a transcript line of their own
    /// (a message still being sent, the hub's notices). `anyLine`: the session numbers its lines its own way
    /// (`Offer.declared`), and any `<line>#<n>` is one, as the web reads them (`entryOf`).
    static func entry(of id: String, anyLine: Bool = false) -> String? {
        guard let hash = id.firstIndex(of: "#") else { return nil }
        let line = String(id[..<hash])
        if anyLine { return line.isEmpty ? nil : line }
        return UUID(uuidString: line) == nil ? nil : line
    }

    /// Your messages in order. Images alone are a message too (Claude lists them), with no text; a message queued while
    /// Claude worked is not in its list yet.
    static func prompts(_ items: [Item], anyLine: Bool = false) -> [Prompt] {
        var out: [Prompt] = []
        for it in items {
            guard it.role == "user", it.queued != true,
                  it.kind == .text || (it.kind == .image && it.toolUseID == nil),
                  let entry = entry(of: it.id, anyLine: anyLine) else { continue }
            let text = it.kind == .text ? it.cutText : ""
            if let last = out.last, last.entry == entry {
                if !text.isEmpty { out[out.count - 1].raw += (last.raw.isEmpty ? "" : "\n") + text }
                if it.fullSize != nil { out[out.count - 1].cut = true }
                continue
            }
            out.append(Prompt(entry: entry, raw: text, cut: it.fullSize != nil))
        }
        return out
    }

    private static func squash(_ s: String) -> String { s.filter { !$0.isWhitespace } }

    /// Which message the bubble with this item id stands for, in the words the node needs; nil when it cannot be told
    /// apart (no words) or is not one of your transcript messages. `full` replaces the text of a message the page cut.
    static func target(_ items: [Item], id: String, full: String? = nil, anyLine: Bool = false) -> Target? {
        guard let entry = entry(of: id, anyLine: anyLine) else { return nil }
        var all = prompts(items, anyLine: anyLine)
        guard let at = all.firstIndex(where: { $0.entry == entry }) else { return nil }
        if let full { all[at].raw = full }
        guard !all[at].raw.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty else { return nil }
        let same = squash(all[at].raw)
        return Target(prompt: all[at].raw,
                      occurrence: 1 + all[(at + 1)...].filter { squash($0.raw) == same }.count,
                      promptsAfter: all.count - 1 - at)
    }

    /// The message was cut for the page: its whole text has to be fetched first (the first cut item's id).
    static func cutItem(_ items: [Item], id: String, anyLine: Bool = false) -> String? {
        guard let entry = entry(of: id, anyLine: anyLine) else { return nil }
        let mine = items.filter { $0.role == "user" && $0.kind == .text && Self.entry(of: $0.id, anyLine: anyLine) == entry }
        guard mine.contains(where: { $0.fullSize != nil }) else { return nil }
        return mine.first?.id
    }

    /// Whether the menu offers it at all: a Claude conversation of an agent (not the assistant's, not a subagent or a
    /// thread another source lists), or a session whose node says it can (`capabilities.rewind`: a runtime's session, a
    /// Codex thread, #6294). Nil when it does not; otherwise why it is off right now ("" when it is on).
    static func block(_ session: Session?) -> String? {
        guard let s = session, !s.assistant, !s.sessionID.contains("@") else { return nil }
        if s.capabilities?.rewind != true {
            // Not declared (an older node) or declared off: an Agent's own Claude row, as before.
            guard s.sourceKind == nil else { return nil }
            if let agent = s.agent, agent != "claude" { return String(localized: "Claude only") }
            if s.capabilities?.rewind == false { return nil }
        }
        if s.state == .offline || s.starting == true { return StateName.notRunning.text }
        if s.state == .needsYou || s.pendingInteractions > 0 { return StateName.needsYou.text }
        if s.state == .working { return StateName.working.text }
        return ""
    }

    struct Result: Decodable, Sendable {
        var prompt: String?
        var restore: String?
        /// The files went back too (not when nothing changed after the message).
        var codeRestored: Bool?
        /// Claude's input box was emptied: the message goes into the app's composer instead.
        var inputCleared: Bool?

        enum CodingKeys: String, CodingKey {
            case prompt, restore, codeRestored = "code_restored", inputCleared = "input_cleared"
        }
    }

    /// What the toast says after it went through.
    static func done(_ result: Result, asked: Restore) -> String {
        if asked == .codeAndConversation, result.codeRestored != true { return String(localized: "Chat rewound (no code changes after this message)") }
        return result.codeRestored == true ? String(localized: "Code and chat rewound") : String(localized: "Chat rewound")
    }

    /// The input box on the computer still holds the message (it could not be emptied).
    static var inputKept: String { String(localized: "Rewound, but the message is still in Claude's input box on the computer") }

    /// The hub's refusal in words for the toast.
    static func failure(_ error: Error) -> String {
        if let e = error as? APIError {
            if (e.status == 404 || e.status == 405) && e.code != "gone" { return String(localized: "This hub doesn't support rewinding yet. Update it first.") }
            if e.status == 403 { return String(localized: "Rewinding needs a device with full access") }
            return e.localizedDescription
        }
        if (error as? URLError)?.code == .timedOut { return String(localized: "Rewind timed out: Claude on the computer didn't respond as expected") }
        return error.localizedDescription
    }
}

extension View {
    /// The questions Rewind on your own message asks (#2915): what to restore, and once more before the files go back.
    func rewindDialogs(menu: MessageMenu) -> some View { modifier(RewindDialogs(menu: menu)) }
}

private struct RewindDialogs: ViewModifier {
    @Bindable var menu: MessageMenu
    /// Rewind Code and Chat was picked: asked again, it puts the files on the computer back too.
    @State private var confirmCode: RewindAsk?

    func body(content: Content) -> some View {
        let block = menu.rewindBlock() ?? ""
        // A session whose node declares rewind (#6294) restores files only where it says so, and has no Claude input box.
        let offer = menu.rewindOffer()
        content
            .accessibleConfirmationDialog("Rewind to Here", isPresented: Binding(get: { menu.rewinding != nil }, set: { if !$0 { menu.rewinding = nil } }),
                                titleVisibility: .visible, hasCancel: true, presenting: menu.rewinding) { ask in
                if block.isEmpty {
                    Button(Rewind.Restore.conversation.title) { menu.rewind?(ask.id, ask.text, .conversation) }
                    if offer.code {
                        Button(Rewind.Restore.codeAndConversation.title, role: .destructive) {
                            // The next question waits for this dialog to go: one presented while it leaves is dropped.
                            Task {
                                try? await Task.sleep(for: .milliseconds(400))
                                confirmCode = ask
                            }
                        }
                    }
                }
                Button("Cancel", role: .cancel) {}
            } message: { _ in
                Text(!block.isEmpty ? String(localized: "Can't rewind right now: \(block)")
                    : offer.declared ? String(localized: "Go back to before this message: everything after it is removed from the conversation.")
                    : String(localized: "Go back to before this message: everything after it is removed from Claude, and this message goes back into the input box."))
            }
            .alert("Rewind Code?", isPresented: Binding(get: { confirmCode != nil }, set: { if !$0 { confirmCode = nil } }),
                   presenting: confirmCode) { ask in
                Button("Rewind Code and Chat", role: .destructive) { menu.rewind?(ask.id, ask.text, .codeAndConversation) }
                Button("Cancel", role: .cancel) {}
            } message: { _ in
                Text("Files on the computer also go back to how they were before this message, and later changes are lost.")
            }
    }
}
