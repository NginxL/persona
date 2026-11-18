import Foundation

/// What a working Claude's status line says right now (`session.live`; docs/protocol/protocol.md "Live status").
/// The retry fields come only from the banner on screen, never from elapsed time.
nonisolated struct SessionLive: Decodable, Hashable, Sendable {
    var verb: String?
    var elapsedS: Int?
    var tokensDown: Int?
    var thinking: Bool?
    var thoughtS: Int?
    var at: String?
    var retryError: String?
    var retryAttempt: Int?
    var retryMax: Int?

    enum CodingKeys: String, CodingKey {
        case verb, thinking, at
        case elapsedS = "elapsed_s"
        case tokensDown = "tokens_down"
        case thoughtS = "thought_s"
        case retryError = "retry_error"
        case retryAttempt = "retry_attempt"
        case retryMax = "retry_max"
    }
}
