import Foundation

/// A model API error in a chat, read from the text Claude Code writes when it gives up ("API Error: 502 …", an
/// assistant message) or from the hub's notice for a retry (「API 错误：Overloaded（正在重试）」). What a person needs
/// first is in front: why, in words, the status code, and whether it is likely to pass by itself; the request id,
/// session and the raw text are folded under it.
public nonisolated struct APIErrorInfo: Hashable, Sendable {
    public var status: Int?
    /// One line, in words.
    public var reason: String
    /// Likely to pass if tried again later (5xx, rate limits, timeouts); false when someone has to act (login,
    /// a bad request); nil when the text does not say.
    public var transient: Bool?
    public var requestID: String?
    public var session: String?
    public var raw: String

    /// Two errors are "the same" for folding when these match.
    public var signature: String { "\(status ?? 0)|\(reason)" }

    nonisolated(unsafe) private static let statusRe = /^\s*(\d{3})\b/
    nonisolated(unsafe) private static let requestRe = /(?i)\b(?:req(?:uest)?(?:[ _-]?id)?)[:=\s"]+([A-Za-z0-9_-]{8,})/
    nonisolated(unsafe) private static let sessionRe = /(?i)\bsession(?:[ _-]?id)?[:=\s"]+([A-Za-z0-9_:-]{6,})/

    public static func unrecoverable(_ text: String?) -> Bool {
        let s = (text ?? "").lowercased()
        return s.contains("compacted natively") && s.contains("compaction is sealed") && s.contains("to that account")
    }

    public static func parse(_ text: String?) -> APIErrorInfo? {
        guard let t = text?.trimmingCharacters(in: .whitespacesAndNewlines) else { return nil }
        let body: String
        if t.hasPrefix("API Error:") {
            body = String(t.dropFirst("API Error:".count))
        } else if t.hasPrefix("API 错误：") { // i18n-ignore: the hub's Chinese retry notice, matched as it writes it
            body = String(t.dropFirst("API 错误：".count)) // i18n-ignore
        } else if unrecoverable(t), t.lowercased().hasPrefix("this conversation was compacted natively by ") {
            body = t
        } else {
            return nil
        }
        let status = (try? statusRe.firstMatch(in: body)).flatMap { Int($0.1) }
        let (reason, transient) = explain(body.lowercased(), status: status)
        return APIErrorInfo(
            status: status,
            reason: reason,
            transient: transient,
            requestID: (try? requestRe.firstMatch(in: body)).map { String($0.1) },
            session: (try? sessionRe.firstMatch(in: body)).map { String($0.1) },
            raw: t
        )
    }

    /// What went wrong, as a person would say it, and whether it is likely to pass.
    private static func explain(_ s: String, status: Int?) -> (String, Bool?) {
        if unrecoverable(s) {
            return (String(localized: "Conversation invalid: its compacted context belongs to another account. Start a new conversation to continue."), false)
        }
        if s.contains("no available gpu") { return (String(localized: "The inference service has no GPU available right now"), true) }
        if s.contains("overloaded") || status == 529 { return (String(localized: "The model service is overloaded"), true) }
        if status == 401 || status == 403 || s.contains("authentication") || s.contains("/login") || s.contains("login expire") {
            return (String(localized: "The sign-in expired. Run /login again on the computer"), false)
        }
        if status == 429 || s.contains("rate limit") || s.contains("rate_limit") { return (String(localized: "Too many requests: rate limited"), true) }
        if s.contains("timed out") || s.contains("timeout") { return (String(localized: "The request timed out"), true) }
        if s.contains("connection") { return (String(localized: "Can't reach the model service"), true) }
        if status == 400 { return (String(localized: "The request was refused: something is wrong with its parameters or context"), false) }
        if let status, (500..<600).contains(status) { return (String(localized: "The model service is unavailable right now"), true) }
        let temporary = s.contains("temporary") || s.contains("try again")
        return (String(localized: "The model request failed"), temporary ? true : nil)
    }
}
