import SwiftUI

/// A message typed in a Feishu chat on its way out: on screen the moment Send is tapped, sent in the background in the
/// order written, and replaced by Feishu's own copy when that arrives (LarkChatView).
nonisolated struct LarkOutgoing: Identifiable, Equatable, Sendable {
    enum State: Codable, Equatable, Sendable { case sending, sent, failed(String), unconfirmed }

    var id = UUID()
    var text: String
    var encrypted: LarkEncryptedPayload? = nil
    var at = Date()
    var state: State = .sending
    /// The message this answers (id, sender, a one-line summary); nil for a plain message to the chat.
    var replyTo: LarkReplyTo? = nil
    /// With `replyTo`: true goes into its thread, false quotes it in the chat.
    var thread = false
    /// The thread's first message, when `thread` and it is known (a file goes into the thread by its root, #1035).
    var threadRoot: String? = nil
    var attachment: LarkAttachment?
    var messageID: String?
    /// Persisted before the first network write; queued rows remain safely retryable after a restart.
    var attempted = false
    /// Direct file uploads have no server outbox replay contract. An uncertain upload is check-only.
    var canRetry: Bool { state != .sent && (attachment?.kind != .file || state != .unconfirmed) }
    /// Who Feishu notifies (the @ picker), kept for Resend and Back to Composer.
    var mentions: [LarkMention] = []
    var mode: String? = nil
    /// What the source's `compose` gave for a mode that prepares (sealed): sent with it, once.
    var prepared: String? = nil
    /// When it was first written, once a Resend has moved `at` on (nil until then).
    var firstAt: Date? = nil
    /// When it was written: where it stands among the chat's messages (LarkChatRows), whatever Resend did since.
    var written: Date { firstAt ?? at }

    /// What the hub gets as `client_id`: this message's id, unchanged by retries.
    var clientID: String { id.uuidString.lowercased() }

    /// Resend: on its way again, as the same message.
    mutating func retry(at now: Date = .now) {
        state = .sending
        attempted = false
        firstAt = written
        at = now
    }

    /// Its row in the chat's list (LarkChatRows); Feishu's copy takes the row over under the same id.
    var rowID: String { Self.rowID(id) }
    static func rowID(_ id: UUID) -> String { "outgoing-\(id.uuidString)" }

    /// Drawn like my other messages: my name and avatar from the last one I sent here.
    func message(like mine: LarkMessage?) -> LarkMessage {
        var message = LarkMessage(id: rowID, position: "", senderID: mine?.senderID, senderName: mine?.senderName ?? String(localized: "Me"),
                    senderAvatar: mine?.senderAvatar, senderAvatarURL: mine?.senderAvatarURL, senderType: mine?.senderType, isMine: true, type: "TEXT", text: text,
                    createdAt: at, recalled: false, replyCount: 0, replyTo: thread ? nil : replyTo)
        if let encrypted {
            message.olk = LarkEncryption(version: 1, state: "decrypted",
                publicParts: [LarkPart(kind: "text", text: encrypted.cover ?? "")],
                bodyParts: [LarkPart(kind: "text", text: text)])
        } else if mode == "sealed" {
            // A source's sealed message on its way: sealed here too (its cover was chosen in the source's sheet).
            message.olk = LarkEncryption(version: 1, state: "decrypted", publicParts: [], bodyParts: [LarkPart(kind: "text", text: text)])
        }
        return message
    }
}

enum LarkOutbox {
    /// How long a sent message waits for its copy before it goes anyway (the next refresh brings it).
    static let copyWait: TimeInterval = 20

    /// Hands one outgoing message to the hub: a reply when it answers a message, else a plain send to the chat.
    @discardableResult
    static func deliver(_ o: LarkOutgoing, feed: String, api: APIClient) async throws -> LarkSendReceipt {
        if let encrypted = o.encrypted {
            guard encrypted.feedId == feed, !o.thread, o.attachment == nil else { throw LarkOutboxIdentity.Failure.changed }
            return try await api.larkSendEncrypted(encrypted, clientID: o.clientID, replyID: o.replyTo?.id)
        }
        if let attachment = o.attachment {
            return try await api.larkSendAttachment(feed, attachment: attachment, clientID: o.clientID,
                                                    replyTo: o.replyTo?.id, thread: o.thread, threadRoot: o.threadRoot)
        }
        if o.mode != nil, api.messagingSource == nil {
            throw APIError(status: 409, message: String(localized: "The message source is unavailable right now. This message wasn't sent."), fields: ["code": "lark_mode_unavailable"])
        }
        if let reply = o.replyTo {
            if let source = api.messagingSource {
                return try await api.messagingSend(source, feed, text: o.text, clientID: o.clientID, mentions: o.mentions,
                                                   replyTo: reply.id, thread: o.thread, mode: o.mode, prepared: o.prepared)
            }
            return try await api.larkReply(messageID: reply.id, text: o.text, thread: o.thread, clientID: o.clientID, mentions: o.mentions, feed: feed)
        } else {
            if let source = api.messagingSource {
                return try await api.messagingSend(source, feed, text: o.text, clientID: o.clientID, mentions: o.mentions,
                                                   mode: o.mode, prepared: o.prepared)
            }
            return try await api.larkSend(feed, text: o.text, clientID: o.clientID, mentions: o.mentions)
        }
    }

    /// Confirmed IDs replace optimistic rows, including a request whose response was lost.
    /// Only legacy text without a receipt falls back to text/time matching; media and encrypted ones wait for their exact copy.
    /// A sent plain one with no receipt and no copy within `copyWait` goes anyway (the next refresh brings it).
    static func remaining(_ out: [LarkOutgoing], messages: [LarkMessage], now: Date = .now) -> [LarkOutgoing] {
        let found = copies(out, messages: messages, now: now)
        return out.filter { o in
            guard found[o.id] == nil else { return false }
            return !(o.state == .sent && o.messageID == nil && o.attachment == nil && o.encrypted == nil
                     && now.timeIntervalSince(o.at) > copyWait)
        }
    }

    /// Feishu's copy of each outgoing message that has one among `messages`: outgoing id → the copy's message id.
    /// One copy answers one message (the same words sent twice need two).
    static func copies(_ out: [LarkOutgoing], messages: [LarkMessage], now: Date = .now) -> [UUID: String] {
        var mine = messages.filter(\.isMine)
        var found: [UUID: String] = [:]
        func take(_ o: LarkOutgoing, _ match: (LarkMessage) -> Bool) -> Bool {
            guard let i = mine.firstIndex(where: match) else { return false }
            found[o.id] = mine.remove(at: i).id
            return true
        }
        for o in out {
            // A push can arrive before the request finishes (or after its response was lost).
            // A confirmed client ID is stronger evidence than a local timeout; pending copies are not.
            if take(o, {
                $0.clientID?.lowercased() == o.clientID && !$0.id.hasPrefix("local:") && $0.localState != "pending" && $0.localState != "failed"
            }) { continue }
            guard o.state == .sent else { continue }
            if let id = o.messageID {
                // Media has no unique text. Keep its local row until this exact acknowledged copy is visible.
                _ = take(o) { $0.id == id && $0.localState != "pending" && $0.localState != "failed" }
                continue
            }
            if o.attachment != nil || o.encrypted != nil || now.timeIntervalSince(o.at) > copyWait { continue }
            let text = o.text.trimmingCharacters(in: .whitespacesAndNewlines)
            // A quoted reply's copy quotes the same message when the hub says what it quotes.
            _ = take(o) {
                $0.text.trimmingCharacters(in: .whitespacesAndNewlines) == text && ($0.createdAt ?? now) >= o.at.addingTimeInterval(-60)
                    && (o.replyTo == nil || o.thread || $0.replyTo == nil || $0.replyTo?.id == o.replyTo?.id)
            }
        }
        return found
    }
}

/// Reply on a message (long press): where the answer goes.
enum LarkReply {
    enum Route: Equatable {
        /// A quote bar over the chat's box; the message goes to the chat quoting it.
        case quote
        /// A topic group lists threads: open this one's sheet with its box focused (not a new topic at the bottom).
        case openThread
        /// Already in a thread sheet: its box, the reply goes into the thread.
        case threadBox
    }

    /// Nil when there is nothing to answer (system lines, recalled messages, ones not in Feishu yet).
    static func route(for m: LarkMessage, inTopicGroup: Bool, inThread: Bool) -> Route? {
        guard !m.isSystem, !m.recalled, !m.id.isEmpty, !m.id.hasPrefix("outgoing-") else { return nil }
        if inThread { return .threadBox }
        return inTopicGroup ? .openThread : .quote
    }

    /// What the quote bar and the sending row show of the message answered: one line of its text.
    static func summary(of m: LarkMessage) -> String {
        let line = Fmt.plain(m.displayText).split(whereSeparator: \.isNewline).map { $0.trimmingCharacters(in: .whitespaces) }
            .filter { !$0.isEmpty }.joined(separator: " ")
        if !line.isEmpty { return line }
        switch m.type.uppercased() {
        case "IMAGE": return String(localized: "[Image]")
        case "FILE": return String(localized: "[File] [Lark Chat Pins View 54e20f]")
        case "MEDIA": return String(localized: "[Video] [Lark Outbox da5d03]")
        case "AUDIO": return String(localized: "[Voice]")
        case "STICKER": return String(localized: "[Sticker]")
        default: return String(localized: "[Message]")
        }
    }

    /// The quote an outgoing reply carries.
    static func quote(_ m: LarkMessage) -> LarkReplyTo {
        LarkReplyTo(olk: m.olk, id: m.id, senderName: m.senderName, text: summary(of: m))
    }
}

/// How a message of mine on its way stands, drawn beside its bubble: a spinner while it goes, nothing once sent,
/// the failed mark when it did not go out (tap: Resend / Back to Composer / Delete).
struct LarkSendMark {
    var state: LarkOutgoing.State
    var retry: () -> Void
    var edit: () -> Void
    var discard: () -> Void
    var canRetry = true
    var canRemoveSent = false
}

struct LarkSendMarkView: View {
    var mark: LarkSendMark

    var body: some View {
        switch mark.state {
        case .sending:
            ProgressView().controlSize(.mini)
                .scaledFrame(width: Theme.Size.iconL, height: Theme.Size.iconL)
                .accessibilityLabel("Sending")
                .accessibilityIdentifier("lark.outgoing.sending")
        case .sent:
            if mark.canRemoveSent {
                Menu {
                    Button(role: .destructive, action: mark.discard) {
                        Label("Remove from This Device", systemImage: Theme.Icon.delete).lineLimit(1)
                    }
                } label: {
                    Image(systemName: Theme.Icon.done)
                        .font(Theme.Font.label).foregroundStyle(.secondary)
                        .scaledFrame(width: Theme.Size.iconL, height: Theme.Size.tapTarget)
                        .contentShape(.rect)
                }
                .accessibilityLabel("Sent. Manage the record on this device")
                .accessibilityHint("Removes only the record here; the Lark message isn't recalled")
                .accessibilityIdentifier("lark.outgoing.sentReceipt")
            }
        case .failed, .unconfirmed:
            Menu {
                Button(action: mark.retry) { Label(retryTitle, systemImage: Theme.Icon.retry).lineLimit(1) }
                if mark.state != .unconfirmed {
                    Button(action: mark.edit) { Label("Back to Composer", systemImage: Theme.Icon.undo).lineLimit(1) }
                }
                Button(role: .destructive, action: mark.discard) {
                    Label(mark.state == .unconfirmed ? String(localized: "Remove from This Device") : String(localized: "Delete"), systemImage: Theme.Icon.delete).lineLimit(1)
                }
            } label: {
                Image(systemName: Theme.Icon.errorFilled)
                    .font(Theme.Font.body)
                    .foregroundStyle(mark.state == .unconfirmed ? Theme.caution : Theme.danger)
                    // As wide as the icon (close to the bubble), as tall as a tap target.
                    .scaledFrame(width: Theme.Size.iconL, height: Theme.Size.tapTarget)
                    .contentShape(.rect)
            }
            .accessibilityLabel(mark.state == .unconfirmed ? String(localized: "Send result unconfirmed") : String(localized: "Not sent. Tap to resend"))
            .accessibilityHint(hint)
            .accessibilityIdentifier(mark.state == .unconfirmed ? "lark.outgoing.unconfirmed" : "lark.outgoing.failed")
        }
    }

    private var hint: String {
        if case .failed(let why) = mark.state { return why }
        return mark.canRetry ? String(localized: "The connection dropped before the send was confirmed. Retrying checks existing messages first.") : String(localized: "Not yet confirmed whether the file was sent. This only checks the result; it won't upload again.")
    }

    private var retryTitle: String {
        guard mark.state == .unconfirmed else { return String(localized: "Resend") }
        return mark.canRetry ? String(localized: "Check and Retry") : String(localized: "Check Send Result")
    }
}
