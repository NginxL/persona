import Foundation

/// The replayed meeting (#9417, #10589; docs/apple/recording.md "A meeting's tape and its replay"): the made-up room
/// meeting a tape plays in, recorded with no microphone under one fixed id. It is on no hub and never goes to one:
/// sync never uploads or deletes it there and ignores the hub's word on it, the meeting room offers nothing that asks
/// the hub about it, and `APIClient` refuses any request that names it before it leaves the phone (`refuses`).
nonisolated enum ReplayedMeeting {
    static let id = UUID(uuidString: "00000000-0000-0000-0000-000000009417")!
    /// How a path or a query names it (paths carry the id in lower case; an upper-case one is matched too).
    private static let named = id.uuidString.lowercased()

    /// Whether `recording` is the replayed meeting.
    static func holds(_ recording: UUID) -> Bool { recording == id }

    /// The error a request about the replayed meeting ends with, sent nowhere.
    static let refusal = "replayed_meeting"

    /// Whether a request to `path` with `query` names the replayed meeting: refused before anything is sent.
    static func refuses(_ path: String, query: [String: String] = [:]) -> Bool {
        mentions(path) || query.values.contains(where: mentions)
    }

    private static func mentions(_ text: String) -> Bool {
        text.count >= named.count && text.range(of: named, options: .caseInsensitive) != nil
    }
}
