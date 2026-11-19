import Foundation
import PersonaWire

/// The user's own "/" prompts (#7329, iOS #9436): instructions they send again and again, kept on the hub as the current
/// assistant's settings so every device has the same list (docs/protocol/protocol.md, `/api/v1/assistant/saved-prompts`;
/// docs/web/web.md "Saved prompts (#7329)"). Picking one only types its text into the box.
nonisolated struct SavedPrompt: Codable, Identifiable, Hashable, Sendable {
    var id: String
    var name: String
    var text: String
    var created_at: String?
    var updated_at: String?
}

/// The hub's rules for a saved prompt, said before asking it (the web's `lib/saved-prompts.ts` checks the same): the hub
/// still decides, and its own refusal is shown as it comes.
nonisolated enum SavedPromptRules {
    static let nameMax = 32
    static let textMax = 8000
    static let countMax = 100

    enum Problem: Equatable, Sendable { case nameEmpty, nameTooLong, nameCharacters, nameTaken, textEmpty, textTooLong }

    /// The name as the hub keeps it: trimmed, a "/" typed before it dropped.
    static func cleanName(_ raw: String) -> String {
        var s = raw.trimmingCharacters(in: .whitespacesAndNewlines)
        while s.hasPrefix("/") { s.removeFirst() }
        return s
    }

    /// Letters (any script), digits, combining marks, "-", "_" or ".".
    static func allowedName(_ name: String) -> Bool {
        name.unicodeScalars.allSatisfy { u in
            CharacterSet.letters.contains(u) || CharacterSet.decimalDigits.contains(u) || CharacterSet.nonBaseCharacters.contains(u) || "-_.".unicodeScalars.contains(u)
        }
    }

    /// What is wrong with a name, among `others` (the list's other prompts; a name is unique ignoring case).
    static func nameProblem(_ raw: String, others: [SavedPrompt]) -> Problem? {
        let name = cleanName(raw)
        if name.isEmpty { return .nameEmpty }
        if name.count > nameMax { return .nameTooLong }
        if !allowedName(name) { return .nameCharacters }
        if others.contains(where: { $0.name.lowercased() == name.lowercased() }) { return .nameTaken }
        return nil
    }

    /// What is wrong with a text: trimmed, 1–8000 characters.
    static func textProblem(_ raw: String) -> Problem? {
        let text = raw.trimmingCharacters(in: .whitespacesAndNewlines)
        if text.isEmpty { return .textEmpty }
        if text.count > textMax { return .textTooLong }
        return nil
    }

    /// The words a problem is said in.
    static func message(_ p: Problem) -> String {
        switch p {
        case .nameEmpty: String(localized: "Give it a name.", comment: "Saved prompt editor: the name is empty")
        case .nameTooLong: String(localized: "A name has at most 32 characters.", comment: "Saved prompt editor")
        case .nameCharacters: String(localized: "Use one word: letters, digits, “-”, “_” or “.”.", comment: "Saved prompt editor: characters a name may have")
        case .nameTaken: String(localized: "Another prompt has this name.", comment: "Saved prompt editor")
        case .textEmpty: String(localized: "Write what it should type.", comment: "Saved prompt editor: the text is empty")
        case .textTooLong: String(localized: "A prompt has at most 8,000 characters.", comment: "Saved prompt editor")
        }
    }

    /// The text's first lines, as a row shows them.
    static func preview(_ text: String, lines: Int = 2) -> String {
        text.split(separator: "\n", omittingEmptySubsequences: true).prefix(lines).joined(separator: "\n")
    }
}

extension APIClient {
    nonisolated struct SavedPromptsBody: Decodable { var prompts: [SavedPrompt] }
    nonisolated struct SavedPromptBody: Decodable { var prompt: SavedPrompt }
    nonisolated struct SavedPromptInput: Encodable { var name: String; var text: String }
    nonisolated struct SavedPromptOrder: Encodable { var ids: [String] }

    /// The current assistant's saved prompts, in the "/" list's order. 404 from a hub or manager from before them.
    func savedPrompts() async throws -> [SavedPrompt] {
        try await get("/api/v1/assistant/saved-prompts", as: SavedPromptsBody.self).prompts
    }

    /// Adds one at the end (no id yet) or changes one's name and text, keeping its place; returns it as the hub has it.
    func saveSavedPrompt(id: String?, name: String, text: String) async throws -> SavedPrompt {
        let body = SavedPromptInput(name: name, text: text)
        let data = if let id { try await request("PUT", "/api/v1/assistant/saved-prompts/\(TodosLogic.pathComponent(id))", body: body) }
                   else { try await request("POST", "/api/v1/assistant/saved-prompts", body: body) }
        return try JSON.decoder.decode(SavedPromptBody.self, from: data).prompt
    }

    /// Every prompt's id once, in the new order; 409 when the list changed since it was read (nothing moves).
    func orderSavedPrompts(_ ids: [String]) async throws -> [SavedPrompt] {
        let data = try await request("PUT", "/api/v1/assistant/saved-prompts/order", body: SavedPromptOrder(ids: ids))
        return try JSON.decoder.decode(SavedPromptsBody.self, from: data).prompts
    }

    func deleteSavedPrompt(_ id: String) async throws {
        try await delete("/api/v1/assistant/saved-prompts/\(TodosLogic.pathComponent(id))")
    }
}
