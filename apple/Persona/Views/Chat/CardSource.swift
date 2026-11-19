import Foundation

/// The message a pending question card is about, in its chat's loaded items (#9392): the card's View in Chat goes there.
///
/// The card's call is the tool call that asked it: by its tool use id where the card has one (an Agent's
/// AskUserQuestion), else the newest `ask_user` call asking the card's first question (the assistant's own cards carry
/// no tool use id). The message is the agent's last words before that call in the same turn, or, where it wrote none,
/// the message that started the turn. No call found (an older page, a card raised without one): nil, and the card has no
/// View in Chat.
nonisolated enum CardSource {
    static func item(for ix: Interaction, in items: [Item]) -> String? {
        guard ix.kind == .question, let call = callIndex(ix, items) else { return nil }
        var i = call - 1
        while i >= 0 {
            let it = items[i]
            if it.role == "user", it.kind == .text, it.toolUseID == nil { return it.id }
            if it.role == "assistant", it.kind == .text, !(it.text ?? "").trimmingCharacters(in: .whitespacesAndNewlines).isEmpty {
                return it.id
            }
            i -= 1
        }
        return nil
    }

    /// How far back from the newest item a card's call is looked for: a card asks about the latest turns.
    static let lookBack = 400

    private static func callIndex(_ ix: Interaction, _ items: [Item]) -> Int? {
        let floor = max(items.count - lookBack, 0)
        if let id = ix.toolUseID, !id.isEmpty {
            return items[floor...].lastIndex { $0.kind == .toolUse && $0.toolUseID == id }
        }
        guard let first = ix.questions?.first?.question, !first.isEmpty else { return nil }
        return items[floor...].lastIndex { it in
            guard it.kind == .toolUse, let name = it.toolName, name == "ask_user" || name.hasSuffix("__ask_user") else { return false }
            guard case .array(let qs)? = it.toolInput?["questions"], let q = qs.first?["question"]?.string else { return false }
            return q == first
        }
    }
}

/// What the chat looks for the cards' messages again on (#9392): another card, or the loaded chat grown or changed at its end.
nonisolated struct CardSourceInputs: Equatable {
    var cards: [String]
    var count: Int
    var last: String?
}
