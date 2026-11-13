import Foundation

// A connector's inbox thread as a conversation (GET /api/v1/connectors/{id}/thread, docs/protocol/protocol.md「Connector API」):
// a page of messages at a time, newest page first, their pictures through the hub, a reply through the connector's
// action (the user's own: not put to the gate). Telegram's chats draw as bubbles (ConnectorThreadView); GitHub's pull requests and issues
// as GitHub's thread (its own page), by `ThreadChat.kind`.

nonisolated struct ThreadPage: Codable, Sendable {
    var chat: ThreadChat
    var messages: [ThreadMessage]
    /// Goes back one more page; nil: the thread's start.
    var before: String?
}

nonisolated struct ThreadChat: Codable, Hashable, Sendable {
    var id: String
    var name: String
    var kind: String
    var url: String?
    var avatar: String?
    var members: Int?
    /// The action that answers here; nil: read-only.
    var reply: String?
    // A pull request's or issue's (GitHub).
    var state: String?
    var author: ThreadPerson?
    var labels: [Label]?
    var branch: Branch?
    var body: String?
    var bodyFormat: String?

    nonisolated struct Label: Codable, Hashable, Sendable {
        var name: String
        var color: String?
    }

    nonisolated struct Branch: Codable, Hashable, Sendable {
        var head: String
        var base: String
    }

    enum CodingKeys: String, CodingKey {
        case id, name, kind, url, avatar, members, reply, state, author, labels, branch, body
        case bodyFormat = "body_format"
    }

    /// A chat app's conversation, drawn as bubbles (others on the left, mine on the right).
    var isChat: Bool { ["dm", "group", "channel"].contains(kind) }
    var isGroup: Bool { kind != "dm" }
    /// A GitHub pull request or issue, drawn as GitHub's thread (GitHubThreadView).
    var isGitHub: Bool { kind == "pr" || kind == "issue" }
}

nonisolated struct ThreadPerson: Codable, Hashable, Sendable {
    var id: String?
    var name: String
    var avatar: String?
}

nonisolated struct ThreadMedia: Codable, Hashable, Sendable {
    var kind: String // image | sticker | video | voice | file
    var ref: String?
    var thumb: String?
    var mime: String?
    var name: String?
    var size: Int64?
    var duration: Int?
    var width: Int?
    var height: Int?
    var emoji: String?

    /// The picture to show: the media itself, else its thumbnail.
    var picture: String? { ref ?? thumb }
}

nonisolated struct ThreadQuote: Codable, Hashable, Sendable {
    var id: String
    var from: String?
    var text: String?
}

nonisolated struct ThreadMessage: Codable, Hashable, Sendable, Identifiable {
    var id: String
    var at: Date
    var sender: ThreadPerson
    var mine: Bool?
    var event: Bool?
    var text: String?
    var format: String?
    var media: [ThreadMedia]?
    var replyTo: ThreadQuote?
    var edited: Bool?
    var url: String?
    // GitHub's.
    var kind: String?
    var review: String?
    var code: Code?
    var check: Check?

    nonisolated struct Code: Codable, Hashable, Sendable {
        var path: String
        var line: Int?
        var hunk: String?
    }

    nonisolated struct Check: Codable, Hashable, Sendable {
        var name: String
        var state: String
        var url: String?
        var log: String?
    }

    enum CodingKeys: String, CodingKey {
        case id, at, sender, mine, event, text, format, media, edited, url, kind, review, code, check
        case replyTo = "reply_to"
    }

    var isMine: Bool { mine == true }
    var isEvent: Bool { event == true }

    /// What the message says, for a quote and for Copy: its words, else what its media is.
    var summary: String {
        if let t = text?.trimmingCharacters(in: .whitespacesAndNewlines), !t.isEmpty { return t }
        guard let m = media?.first else { return "" }
        return ThreadMediaText.describe(m)
    }
}

/// A message's words drawn rather than shown with their marks: Markdown (`format` "markdown"), and lark-cli's envelope
/// for one interactive message, `<card title="…">…</card>` (the title optional, the body Markdown), decoded as the web
/// decodes it (web/src/lib/lark-inbox-card.ts): only a message that is one whole card is a card. A picture a Lark post
/// names by its image key, which the app cannot fetch, reads as [Image].
nonisolated struct ThreadRichText: Equatable, Sendable {
    /// The card's title, "" for an untitled card; nil when the words are not a card.
    var cardTitle: String?
    var body: String

    private static let memo = TextMemo<ThreadRichText?>(limit: 256)

    /// `parse`, kept per words: a row asks on every redraw (docs/apple/performance.md, P4).
    static func of(_ text: String, markdown: Bool, larkCard: Bool, image: String) -> ThreadRichText? {
        memo("\(markdown ? 1 : 0)\(larkCard ? 1 : 0)\u{0}\(image)\u{0}" + text) { _ in parse(text, markdown: markdown, larkCard: larkCard, image: image) }
    }

    /// The words as drawn, or nil when they are plain text: neither Markdown nor (larkCard: a Lark connector's
    /// message) a card.
    static func parse(_ text: String, markdown: Bool, larkCard: Bool, image: String) -> ThreadRichText? {
        if larkCard, let card = text.wholeMatch(of: /\s*<card(?:\s+title="([^"<>]*)")?\s*>([\s\S]*)<\/card>\s*/),
           !card.output.2.contains(/<\/?card[\s>]/) {
            return ThreadRichText(cardTitle: String(card.output.1 ?? ""), body: pictures(String(card.output.2).trimmingCharacters(in: .whitespacesAndNewlines), image))
        }
        guard markdown else { return nil }
        return ThreadRichText(cardTitle: nil, body: larkCard ? pictures(text, image) : text)
    }

    /// A Lark post's pictures: an image key (no web address) is nothing the app can fetch, so it reads as image.
    private static func pictures(_ text: String, _ image: String) -> String {
        text.replacing(/!\[[^\]\n]*\]\(([^)\s]*)\)/) { m in
            m.output.1.hasPrefix("https://") || m.output.1.hasPrefix("http://") ? String(m.output.0) : image
        }
    }
}

/// What a media is, in a few words: Image, Sticker 👍, Voice 0:12, Video 1:05, File name.
nonisolated enum ThreadMediaText {
    static func describe(_ m: ThreadMedia) -> String {
        switch m.kind {
        case "image": String(localized: "[Image]")
        case "sticker": String(localized: "[Sticker\(m.emoji.map { " " + $0 } ?? "")]")
        case "voice": String(localized: "[Voice \(duration(m.duration ?? 0))]")
        case "video": String(localized: "[Video \(duration(m.duration ?? 0))]")
        default: String(localized: "[File \(m.name ?? "")]")
        }
    }

    /// 0:12, 1:05, 1:02:03.
    static func duration(_ seconds: Int) -> String {
        let s = max(seconds, 0)
        return s >= 3600 ? String(format: "%d:%02d:%02d", s / 3600, s / 60 % 60, s % 60) : String(format: "%d:%02d", s / 60, s % 60)
    }
}

extension APIClient {
    func connectorThread(_ connector: String, thread: String, before: String? = nil, limit: Int = 30) async throws -> ThreadPage {
        var q = ["thread": thread, "limit": String(limit)]
        if let before { q["before"] = before }
        return try await getDecodedOffMain("/api/v1/connectors/\(connector)/thread", query: q)
    }

    /// A reply through the thread's action. Sent from this device it runs at once: the hub puts only the AI's writes to
    /// connector_gate, not what the user sends by hand.
    func connectorThreadReply(_ connector: String, thread: String, action: String, text: String, replyTo: String?) async throws {
        struct Body: Encodable {
            var thread, action, text: String
            var replyTo: String?
            enum CodingKeys: String, CodingKey {
                case thread, action, text
                case replyTo = "reply_to"
            }
        }
        _ = try await post("/api/v1/connectors/\(connector)/thread/reply", body: Body(thread: thread, action: action, text: text, replyTo: replyTo))
    }
}

/// A connector's media through the hub, for the image loader (LarkMedia reads hub-relative URLs, keeps them on disk).
nonisolated enum ConnectorMedia {
    static func url(_ connector: String, ref: String) -> String {
        var c = URLComponents()
        c.path = "/api/v1/connectors/\(connector)/media"
        c.queryItems = [URLQueryItem(name: "ref", value: ref)]
        return c.string ?? ""
    }
}

/// Where a conversation breaks up, as the Feishu chat does (LarkChatLayout): a time between messages after a pause or
/// on a new day, and one avatar and name for a run of messages from the same person.
nonisolated enum ThreadLayout {
    /// The widest a bubble gets, as a share of the page's width: the same for mine and others'.
    static let bubbleShare: CGFloat = 0.78

    /// The time in a message's corner, in the device's zone like the separators: 09:42, or Edited 09:42.
    static func stamp(_ m: ThreadMessage, calendar: Calendar = .current) -> String {
        let c = calendar.dateComponents([.hour, .minute], from: m.at)
        let time = String(format: "%02d:%02d", c.hour ?? 0, c.minute ?? 0)
        return m.edited == true ? String(localized: "Edited \(time)") : time
    }

    /// The line under the page's title, as the Feishu chat's (Lark / Lark Topic Group): the source, the kind of chat, and how
    /// many are in it when the hub knows — Telegram group · 128 members.
    static func subtitle(_ chat: ThreadChat?, source: String, locale: Locale = .current) -> String {
        let count = chat?.members.flatMap { $0 > 0 ? $0.formatted(.number.locale(locale)) : nil }
        switch chat?.kind {
        case "group": return count.map { String(localized: "\(source) group · \($0) members") } ?? String(localized: "\(source) group")
        case "channel": return count.map { String(localized: "\(source) channel · \($0) subscribers") } ?? String(localized: "\(source) channel")
        default: return source
        }
    }

    /// The run's first message shows its sender; a time separator goes above a new day or `LarkChatLayout.separatorGap`
    /// of quiet.
    static func separates(_ m: ThreadMessage, after prev: ThreadMessage?, calendar: Calendar = .current) -> Bool {
        guard let prev else { return true }
        return !calendar.isDate(m.at, inSameDayAs: prev.at) || m.at.timeIntervalSince(prev.at) >= LarkChatLayout.separatorGap
    }

    /// The last of its run (the message after it starts another, or there is none): the avatar goes beside it, at its
    /// bottom, as in Telegram; the name stays over the first.
    static func endsRun(_ m: ThreadMessage, before next: ThreadMessage?, calendar: Calendar = .current) -> Bool {
        guard let next else { return true }
        return !continues(next, after: m, calendar: calendar)
    }

    /// Same sender as the message before, with no separator between them: no second avatar or name.
    static func continues(_ m: ThreadMessage, after prev: ThreadMessage?, calendar: Calendar = .current) -> Bool {
        guard let prev, !m.isEvent, !prev.isEvent, !separates(m, after: prev, calendar: calendar) else { return false }
        if m.isMine || prev.isMine { return m.isMine == prev.isMine }
        if let a = m.sender.id, let b = prev.sender.id, !a.isEmpty, !b.isEmpty { return a == b }
        return m.sender.name == prev.sender.name
    }
}

/// One thread opened: its pages (oldest first on screen), going back a page at a time, a quote's message found by
/// going back further, a reply.
@MainActor @Observable final class ConnectorThreadModel {
    let connector: String
    let thread: String
    private(set) var chat: ThreadChat?
    private(set) var messages: [ThreadMessage] = []
    private(set) var before: String?
    private(set) var loaded = false
    private(set) var loadingOlder = false
    /// Why the last load failed (a rate limit says how long to wait); nil while it went fine.
    private(set) var failure: String?
    /// The first page could not be read: the caller shows what the hub collected instead.
    private(set) var unavailable = false
    private(set) var sending = false
    @ObservationIgnored private let api: () -> APIClient?

    /// Pages a jump to a quote's message reads back at most before giving up.
    static let jumpPages = 3

    init(connector: String, thread: String, api: @escaping () -> APIClient? = { AppModel.shared.api }) {
        self.connector = connector
        self.thread = thread
        self.api = api
    }

    var hasOlder: Bool { before != nil }

    func load() async {
        guard let api = api() else { return }
        do {
            let page = try await api.connectorThread(connector, thread: thread)
            (chat, messages, before, failure, unavailable) = (page.chat, page.messages, page.before, nil, false)
        } catch {
            if error.isCancellation { return }
            failure = Self.noConversation(error) ? nil : Self.said(error)
            unavailable = messages.isEmpty
        }
        loaded = true
    }

    /// An older hub's way of saying the item has no conversation of its own (#5537): no such connector (404: a
    /// subscription's item, its source the subscription) or none to open (409 without `unavailable`: a connector
    /// without threads). The page shows what the hub collected without an error, as a newer hub's `none` page does.
    private static func noConversation(_ error: Error) -> Bool {
        guard let e = error as? APIError else { return false }
        return e.status == 404 || e.status == 409 && e.code != "unavailable"
    }

    /// The page before the oldest shown; false when there is none or it could not be read.
    @discardableResult
    func loadOlder() async -> Bool {
        guard let before, !loadingOlder, let api = api() else { return false }
        loadingOlder = true
        defer { loadingOlder = false }
        do {
            let page = try await api.connectorThread(connector, thread: thread, before: before)
            let have = Set(messages.map(\.id))
            messages.insert(contentsOf: page.messages.filter { !have.contains($0.id) }, at: 0)
            self.before = page.before
            failure = nil
            return true
        } catch {
            if !error.isCancellation { failure = Self.said(error) }
            return false
        }
    }

    /// Reads back (a few pages at most) until message id is shown; whether it is.
    func reach(_ id: String) async -> Bool {
        for _ in 0..<Self.jumpPages where !messages.contains(where: { $0.id == id }) {
            guard await loadOlder() else { break }
        }
        return messages.contains { $0.id == id }
    }

    /// Sends a reply, then reads the newest page again; whether it was sent.
    func reply(_ text: String, to quote: ThreadMessage?) async -> Bool {
        guard let action = chat?.reply, let api = api(), !sending else { return false }
        sending = true
        defer { sending = false }
        do {
            try await api.connectorThreadReply(connector, thread: thread, action: action, text: text, replyTo: quote?.id)
            failure = nil
        } catch {
            if !error.isCancellation { failure = Self.said(error, sending: true) }
            return false
        }
        if let page = try? await api.connectorThread(connector, thread: thread) {
            let fresh = Set(page.messages.map(\.id))
            messages = messages.filter { !fresh.contains($0.id) && $0.at < (page.messages.first?.at ?? .distantFuture) } + page.messages
            chat = page.chat
        }
        return true
    }

    /// A failure in words, a read's or a send's: a rate limit's says how long to wait (the hub's), a refusal (403)
    /// and no answer (offline, a timeout) say what happened and what to do next.
    private static func said(_ error: Error, sending: Bool = false) -> String {
        if let e = error as? APIError, e.status == 403 {
            return sending ? String(localized: "Not sent: the hub's rules don't allow it") : String(localized: "The hub doesn't allow this device to read this conversation")
        }
        if let shown = error.shown { return shown }
        return sending ? String(localized: "It may not have been sent: once connected to the hub, check before deciding whether to send it again") : String(localized: "Can't load it right now: open it again once connected to the hub")
    }
}
