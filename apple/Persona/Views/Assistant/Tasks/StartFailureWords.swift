import Foundation

/// Why a task's Agent did not start, in words for everyone (#8237). The hub's reason is often a raw error
/// ("rpc: connection closed", "context deadline exceeded"); that stays for the task's details. The hand-off card and the
/// Needs You row say the cause in a few plain words when the error names a known one, keep a sentence the hub already
/// wrote for people (in the owner's language, or a whole English sentence), and otherwise point to details when available.
nonisolated enum StartFailureWords {
    enum Cause: Equatable, Sendable { case connectionLost, offline, timedOut, notAllowed, needsUpdate }

    /// The known cause in the raw text, by the words Go, the node and the hub's own sentences use (the hub's
    /// `connectionReason` groups, plus a dropped link); nil when none is known.
    static func cause(_ raw: String) -> Cause? {
        let t = raw.lowercased()
        let tokens = Set(t.split { !$0.isLetter }.map(String.init))
        func has(_ terms: [String]) -> Bool { terms.contains { t.contains($0) } }
        if has(["connection closed", "connection reset", "connection lost", "broken pipe", "node exited", "连接断了", "接続が切れ"]) // i18n-ignore: matches the hub's text
            || tokens.contains("eof") { return .connectionLost }
        if has(["deadline exceeded", "timed out", "timeout", "超时", "タイムアウト"]) { return .timedOut } // i18n-ignore: matches the hub's text
        if has(["permission denied", "unauthorized", "unauthorised", "forbidden", "access denied", "publickey", "authentication failed"]) {
            return .notAllowed
        }
        if has(["unknown method", "version mismatch", "protocol version", "incompatible"]) { return .needsUpdate }
        if has(["offline", "not connected", "no route", "unreachable", "connection refused", "no such host", "could not resolve",
                "its computer is unavailable", "离线", "不在线", "オフライン"]) { return .offline } // i18n-ignore: matches the hub's text
        return nil
    }

    static func words(_ cause: Cause) -> String {
        switch cause {
        case .connectionLost: String(localized: "The connection to this computer dropped")
        case .offline: String(localized: "This computer is offline")
        case .timedOut: String(localized: "It took too long to start")
        case .notAllowed: String(localized: "This computer didn't allow it")
        case .needsUpdate: String(localized: "Persona on this computer needs an update")
        }
    }

    private static let continuationPrefix = try! NSRegularExpression(pattern: #"^continues: cannot (?:stop agent [^/\s]+/\S+ on to-do \S+|confirm agent [^/\s]+/\S+ stopped):(?=\s|$)"#)

    /// The reason for people: the cause in words, the hub's own sentence, or nil.
    static func plain(_ raw: String) -> String? {
        var text = raw.trimmingCharacters(in: .whitespacesAndNewlines)
        if text.hasPrefix("continues:") {
            // Strip only the hub's stop/confirmation wrappers. Unknown formats can contain internal IDs too.
            let range = NSRange(text.startIndex..., in: text)
            guard let match = continuationPrefix.firstMatch(in: text, range: range),
                  let prefix = Range(match.range, in: text) else { return nil }
            text = text[prefix.upperBound...].trimmingCharacters(in: .whitespacesAndNewlines)
        }
        guard !text.isEmpty else { return nil }
        if let cause = cause(text) { return words(cause) }
        return readsAsSentence(text) ? text : nil
    }

    /// The hand-off card's line: "Didn't start: <why>", or that the details say why.
    static func cardLine(_ raw: String, canOpen: Bool) -> String {
        if let reason = plain(raw) { return String(localized: "Didn't start: \(reason) [Tasks Store 38bbb7]") }
        return canOpen ? details : String(localized: "Didn't start.")
    }

    /// The Needs You row's second line (its status word already says it did not start): why, or that the details say.
    static func rowLine(_ raw: String) -> String { plain(raw) ?? details }

    static var details: String { String(localized: "Didn't start. Open it for details.") }

    /// Written for people: in Chinese or Japanese (the hub writes its sentences in the owner's language), or an English
    /// sentence that starts with a capital and ends with a full stop. Errors from programs are neither.
    static func readsAsSentence(_ text: String) -> Bool {
        if text.unicodeScalars.contains(where: { (0x3040...0x30FF).contains($0.value) || (0x4E00...0x9FFF).contains($0.value) }) { return true }
        guard let first = text.unicodeScalars.first, let last = text.last else { return false }
        let capitals: ClosedRange<Unicode.Scalar> = "A"..."Z"
        return capitals.contains(first) && text.contains(" ") && ".!?".contains(last)
    }
}
