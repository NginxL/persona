import SwiftUI

/// Someone @-mentioned in a message I send: Feishu notifies them. The text must hold the literal `@name`
/// (docs/protocol/protocol.md, the send and reply routes); Everyone is `user_id` "all".
nonisolated struct LarkMention: Codable, Hashable, Sendable {
    var userID: String
    var name: String

    enum CodingKeys: String, CodingKey {
        case userID = "user_id"
        case name
    }

    static let everyone = LarkMention(userID: "all", name: String(localized: "Everyone"))
}

/// A chat member for the @ picker: `name` is the group nickname when there is one.
nonisolated struct LarkMember: Decodable, Hashable, Sendable, Identifiable {
    var userID: String
    var name: String
    var avatar: String?
    var avatarURL: String?
    var avatarUrl: String?
    var picture: String? { avatarURL ?? avatar ?? avatarUrl }
    var id: String { userID }

    enum CodingKeys: String, CodingKey {
        case userID = "user_id"
        case name, avatar, avatarUrl, avatarURL = "avatar_url"
    }
}

/// Typing @ in a group's box: what is being looked up, what picking someone writes, and who the sent text mentions.
nonisolated enum LarkMentions {
    /// Longest lookup after @ that still keeps the picker up.
    static let maxQuery = 20

    /// An @ being typed: what follows it up to the cursor (the lookup), where the @ is and where the cursor is, as
    /// character offsets into the text.
    struct Typing: Equatable, Sendable {
        var query: String
        var start: Int
        var end: Int
    }

    /// The @ being typed just before `cursor` (a character offset; nil: the end of the text), or nil when none is: no
    /// @ before the cursor, one inside a word like an email address, or a space / new line typed since (the picked
    /// name and its space end it too). What comes after the cursor does not matter.
    static func typing(in text: String, cursor: Int? = nil) -> Typing? {
        let chars = Array(text)
        let end = min(max(cursor ?? chars.count, 0), chars.count)
        guard let at = chars[..<end].lastIndex(of: "@") else { return nil }
        if at > 0 {
            let before = chars[at - 1]
            if before.isASCII && (before.isLetter || before.isNumber) { return nil }
        }
        let typed = chars[(at + 1)..<end]
        guard typed.count <= maxQuery, !typed.contains(where: { $0.isWhitespace }) else { return nil }
        return Typing(query: String(typed), start: at, end: end)
    }

    /// The name being typed after the @ before the cursor ("" right after the @); nil when no @ is being typed.
    static func query(in text: String, cursor: Int? = nil) -> String? {
        typing(in: text, cursor: cursor)?.query
    }

    /// Picking `m`: the @ being typed before the cursor becomes `@name ` (Feishu's mention, a space after it to go on
    /// writing; none is added when a space already follows), and the cursor goes after that space. With no @ being
    /// typed, `@name ` goes in at the cursor.
    static func insert(_ m: LarkMention, into text: String, cursor: Int? = nil) -> (text: String, cursor: Int) {
        let chars = Array(text)
        let at = min(max(cursor ?? chars.count, 0), chars.count)
        let t = typing(in: text, cursor: at) ?? Typing(query: "", start: at, end: at)
        let rest = chars[t.end...]
        let spaced = rest.first == " "
        let written = "@" + m.name + (spaced ? "" : " ")
        return (String(chars[..<t.start]) + written + String(rest), t.start + written.count + (spaced ? 1 : 0))
    }

    /// `insert` at the end of the text.
    static func inserting(_ m: LarkMention, into text: String) -> String {
        insert(m, into: text).text
    }

    /// Who the text mentions out of those picked: one whose `@name` was deleted is left out, someone picked twice
    /// counts once, and of several picked with the same name only as many as the text still holds stay (the first
    /// picked). Longer names are matched first, so `@Tommy` does not keep a picked Tom.
    static func mentions(in text: String, picked: [LarkMention]) -> [LarkMention] {
        var seen = Set<String>()
        let unique = picked.filter { seen.insert($0.userID).inserted }
        var rest = text
        var left: [String: Int] = [:]
        for name in Set(unique.map(\.name)).sorted(by: { $0.count > $1.count }) {
            let token = "@" + name
            left[name] = rest.components(separatedBy: token).count - 1
            rest = rest.replacingOccurrences(of: token, with: "\u{0}")
        }
        return unique.filter { m in
            guard let n = left[m.name], n > 0 else { return false }
            left[m.name] = n - 1
            return true
        }
    }

    /// Everyone is offered while the lookup is empty or could still become Everyone / all.
    static func offersEveryone(_ query: String) -> Bool {
        query.isEmpty || LarkMention.everyone.name.hasPrefix(query) || "all".hasPrefix(query.lowercased())
    }
}

/// The list over a group's box while an @ is typed: Everyone, then the chat's members matching what follows the @ (the
/// box asks the hub, LarkComposer).
struct LarkMentionPicker: View {
    var query: String
    var members: [LarkMember]
    /// Everyone is a group's: a document's comment has no one to notify that way.
    var everyone = true
    var pick: (LarkMention) -> Void
    /// The rows' height: the list is as tall as they are up to 240 pt, then scrolls. (A height-limited frame took all
    /// the box's container offered: one name sat in the middle of a tall empty panel.)
    @State private var rowsHeight: CGFloat = 0

    var body: some View {
        ScrollView {
            list.onGeometryChange(for: CGFloat.self) { $0.size.height } action: { rowsHeight = $0 }
        }
        .scrollBounceBehavior(.basedOnSize)
        .frame(height: min(rowsHeight, Theme.Size.box240))
        .surface(in: .rect(cornerRadius: Theme.Radius.card, style: .continuous))
        .accessibilityElement(children: .contain)
        .accessibilityIdentifier("lark.mentionPicker")
    }

    private var list: some View {
        VStack(alignment: .leading, spacing: 0) {
            if everyone && LarkMentions.offersEveryone(query) {
                row(name: LarkMention.everyone.name, avatar: nil) { pick(.everyone) }
            }
            ForEach(members) { m in
                row(name: m.name, avatar: m.picture) { pick(LarkMention(userID: m.userID, name: m.name)) }
            }
        }
        .padding(.vertical, Theme.Space.xs)
    }

    private func row(name: String, avatar: String?, action: @escaping () -> Void) -> some View {
        Button(action: action) {
            HStack(spacing: Theme.Space.s) {
                LarkAvatarImage(name: name, url: avatar, size: Theme.Size.well)
                Text(name).font(Theme.Font.body).lineLimit(1)
                Spacer(minLength: 0)
            }
            .padding(.horizontal, Theme.Space.m)
            .scaledFrame(minHeight: Theme.Size.tapTarget)
            .contentShape(.rect)
        }
        .buttonStyle(.plain)
        .accessibilityLabel("@\(name)")
    }
}
