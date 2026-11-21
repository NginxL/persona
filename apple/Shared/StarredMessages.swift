#if os(iOS)
import Foundation
import PersonaWire

/// The compact index and the snapshot use the same identity. Positional IDs alone can be reused by a rewritten transcript.
nonisolated struct StarredSource: Hashable, Sendable {
    enum Conversation: String, Codable, Sendable { case assistant, agent }

    let machine: String
    let session: String
    let item: String
    let conversation: Conversation
    let fingerprint: String?

    var isPositional: Bool {
        guard item.hasPrefix("line") else { return false }
        let parts = item.dropFirst(4).split(separator: "#", omittingEmptySubsequences: false)
        return parts.count == 2 && parts.allSatisfy { !$0.isEmpty && $0.utf8.allSatisfy { (48...57).contains($0) } }
    }

    var isVerified: Bool {
        guard isPositional else { return true }
        return fingerprint.map { proof in
            proof.utf8.count == 32 && proof.utf8.allSatisfy { (48...57).contains($0) || (97...102).contains($0) }
        } ?? false
    }

    func matches(_ value: Item) -> Bool {
        value.id == item && isVerified && (!isPositional || value.sourceFingerprint == fingerprint)
    }

    /// Saved snapshots already contain the complete source identity. They never use the short-link prefix lookup.
    func key(group: String) -> SessionKey {
        SessionKey(group: group, machineID: machine, sessionID: session)
    }

    func identifies(_ key: SessionKey) -> Bool {
        key.machineID == machine && key.sessionID == session
    }
}

nonisolated struct StarredReference: Decodable, Sendable {
    let id: String
    let machine_id: String
    let session_id: String
    let item_id: String
    let conversation: StarredSource.Conversation
    let source_fingerprint: String?

    var source: StarredSource {
        StarredSource(machine: machine_id, session: session_id, item: item_id, conversation: conversation, fingerprint: source_fingerprint)
    }
}

nonisolated struct StarredSnapshot: Codable, Equatable, Sendable {
    var id: String? = nil
    let machine_id: String
    let session_id: String
    let item_id: String
    let conversation: StarredSource.Conversation
    let source_fingerprint: String?
    let role: String
    let title: String
    let speaker: String
    let source_manager: String?
    let written_at: Date?
    var starred_at: Date? = nil
    var text: String
    var omitted_chars: Int? = nil

    var source: StarredSource {
        StarredSource(machine: machine_id, session: session_id, item: item_id, conversation: conversation, fingerprint: source_fingerprint)
    }

    /// The server's limit counts Unicode scalars, including characters hidden by a transcript page's byte limit.
    func bounded() -> Self {
        var copy = self
        copy.id = nil
        copy.starred_at = nil
        let text = text.trimmingCharacters(in: .whitespacesAndNewlines)
        let limit = 4_000
        let kept = text.unicodeScalars.prefix(limit)
        copy.text = String(String.UnicodeScalarView(kept))
        copy.omitted_chars = (omitted_chars ?? 0) + max(0, text.unicodeScalars.count - limit)
        return copy
    }
}

extension APIClient {
    static let starredMessagesFeature = "starred_messages"

    nonisolated struct StarredIndex: Decodable { let refs: [StarredReference]; let can_write: Bool }
    nonisolated struct StarredList: Decodable { let stars: [StarredSnapshot]; let can_write: Bool }
    private nonisolated struct AddedStar: Decodable { let star: StarredSnapshot }
    private nonisolated struct RemovedStar: Decodable { let removed: StarredSnapshot? }

    /// Wait for the live route as usual. If it installs another authority in the meantime, the old operation retires.
    func pinStarredAuthority() -> APIClient {
        guard let wait = requestRoute else { return self }
        let original = config
        var client = self
        client.requestRoute = {
            guard let selected = try await wait(), selected == original else { throw CancellationError() }
            return selected
        }
        return client
    }

    func starredIndex() async throws -> StarredIndex {
        try await get("/api/v1/assistant/starred", query: ["index": "1"], as: StarredIndex.self)
    }

    func starredList() async throws -> StarredList {
        try await get("/api/v1/assistant/starred", as: StarredList.self)
    }

    func addStar(_ star: StarredSnapshot) async throws -> StarredSnapshot {
        let data = try await request("POST", "/api/v1/assistant/starred", body: star.bounded())
        return try JSON.decoder.decode(AddedStar.self, from: data).star
    }

    func removeStar(_ id: String) async throws -> StarredSnapshot? {
        let data = try await request("DELETE", "/api/v1/assistant/starred/\(TodosLogic.pathComponent(id))")
        return try JSON.decoder.decode(RemovedStar.self, from: data).removed
    }
}
#endif
