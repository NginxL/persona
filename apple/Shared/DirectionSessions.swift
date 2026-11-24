import Foundation
import PersonaWire

// Filing a session under a direction and reading one direction (#11391, #11516; docs/protocol/protocol.md, the rows of
// `PUT /api/v1/assistant/sessions/{mid}/{sid}/direction` and `GET /api/v1/assistant/directions/{id}`), behind the hub's
// `direction_sessions`. The app draws what these answer; it decides no placement itself.

/// Where a write puts a session: under a direction (its leader conversation's id), on its own, or (`remove`) its entry
/// taken out of the book, which only an Undo of a first filing sends.
nonisolated enum DirectionTarget: Hashable, Sendable {
    case direction(String)
    case own
    case remove

    /// The hub's `previous` (or `direction`) as the answer gave it: a string as it is ("" on its own), `null` none.
    init(answer: String?) {
        switch answer {
        case nil: self = .remove
        case "": self = .own
        case let id?: self = .direction(id)
        }
    }

    /// The direction it is drawn under after the write: its id, "" for none.
    var drawnUnder: String {
        if case .direction(let id) = self { return id }
        return ""
    }
}

/// The write's body: `{"direction":"<id>"}`, `{"direction":""}`, or `{"direction":null}` with the key present. A body
/// without the key means "on its own" to the hub (#11458), so null is written out, never left out.
nonisolated struct DirectionFilingBody: Encodable, Sendable {
    var target: DirectionTarget

    func encode(to encoder: Encoder) throws {
        var c = encoder.container(keyedBy: CodingKeys.self)
        switch target {
        case .direction(let id): try c.encode(id, forKey: .direction)
        case .own: try c.encode("", forKey: .direction)
        case .remove: try c.encodeNil(forKey: .direction)
        }
    }

    private enum CodingKeys: String, CodingKey { case direction }
}

/// The write's answer: what the book holds for the session now and held before (`null`: no entry), and whether that
/// changed. Undo sends `previous` exactly.
nonisolated struct DirectionFilingAnswer: Decodable, Hashable, Sendable {
    var direction: String?
    var previous: String?
    var changed: Bool

    init(direction: String?, previous: String?, changed: Bool) {
        self.direction = direction
        self.previous = previous
        self.changed = changed
    }

    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        direction = try c.decodeIfPresent(String.self, forKey: .direction)
        previous = try c.decodeIfPresent(String.self, forKey: .previous)
        changed = try c.decodeIfPresent(Bool.self, forKey: .changed) ?? false
    }

    private enum CodingKeys: String, CodingKey { case direction, previous, changed }
}

/// `GET /api/v1/assistant/directions/{id}`, the parts this version draws: its name, whether it has ended, the sessions
/// placed in it (quiet ones too) and whether those could be read. Notes, progress and ended tasks are left for the next
/// step (#11516 "Out of scope").
nonisolated struct DirectionRead: Decodable, Hashable, Sendable {
    var id: String
    var name: String
    var endedAt: Date?
    var sessions: [DirectionTree.FiledSession]
    /// What places sessions could not be read: `sessions` is empty rather than partial.
    var sessionsUnavailable: Bool

    init(id: String, name: String = "", endedAt: Date? = nil, sessions: [DirectionTree.FiledSession] = [],
         sessionsUnavailable: Bool = false) {
        self.id = id
        self.name = name
        self.endedAt = endedAt
        self.sessions = sessions
        self.sessionsUnavailable = sessionsUnavailable
    }

    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        id = try c.decodeIfPresent(String.self, forKey: .id) ?? ""
        name = (try? c.decodeIfPresent(String.self, forKey: .name)) ?? ""
        endedAt = try? c.decodeIfPresent(Date.self, forKey: .endedAt)
        sessions = (try? c.decodeIfPresent([DirectionTree.FiledSession].self, forKey: .sessions)) ?? []
        sessionsUnavailable = (try? c.decodeIfPresent(Bool.self, forKey: .sessionsUnavailable)) ?? false
    }

    private enum CodingKeys: String, CodingKey {
        case id, name, sessions
        case endedAt = "ended_at", sessionsUnavailable = "sessions_unavailable"
    }
}

nonisolated enum DirectionRoutes {
    /// The hub lists it while the default manager can file sessions and answer one direction (docs/protocol/protocol.md).
    static let feature = "direction_sessions"
    /// The codes these routes answer 404 with: a 404 that carries one is a refusal, not a missing route.
    static let notFoundCodes: Set<String> = ["session_not_found", "machine_not_found", "direction_not_found"]

    /// The routes are not there: an older manager that reconnected meanwhile answers 404 or 405 without one of their
    /// codes (as for `todo_asks`, docs/protocol/protocol.md). The entries hide until the next hello.
    static func missing(_ error: Error) -> Bool {
        guard let e = error as? APIError, e.status == 404 || e.status == 405 else { return false }
        return !notFoundCodes.contains(e.code ?? "")
    }
}

extension APIClient {
    /// Files session `key` under a direction, on its own, or takes its entry out (`PUT …/sessions/{mid}/{sid}/direction`).
    /// Sent once: the route takes no operation receipt, and sending the same move again would answer `changed: false`.
    func fileSession(_ key: SessionKey, to target: DirectionTarget) async throws -> DirectionFilingAnswer {
        let path = "/api/v1/assistant/sessions/\(Self.directionPathSegment(key.machineID))/\(Self.directionPathSegment(key.sessionID))/direction"
        let data = try await request("PUT", path, body: DirectionFilingBody(target: target))
        return try JSON.decoder.decode(DirectionFilingAnswer.self, from: data)
    }

    /// One direction in a single read (`GET /api/v1/assistant/directions/{id}`).
    func direction(_ id: String) async throws -> DirectionRead {
        try await getDecodedOffMain("/api/v1/assistant/directions/\(Self.directionPathSegment(id))", as: DirectionRead.self)
    }

    /// One path segment: a session id may hold characters a path cannot carry as they are.
    nonisolated static func directionPathSegment(_ s: String) -> String {
        var allowed = CharacterSet.urlPathAllowed
        allowed.remove(charactersIn: "/?#")
        return s.addingPercentEncoding(withAllowedCharacters: allowed) ?? s
    }
}
