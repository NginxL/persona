import Foundation

/// What a /loop draft will do, in one line under the composer.
public struct LoopHint: Equatable, Sendable {
    /// What it will do, or what is still missing.
    public var text: String
    /// Nothing to repeat yet: sending would only start Claude's own /loop help.
    public var missing: Bool

    public init(text: String, missing: Bool) {
        self.text = text
        self.missing = missing
    }
}

/// /loop typed in an agent's composer (#2601, after the web's #1586): Claude Code's own command,
/// `/loop [interval] <what to do>`, which re-runs the prompt on a schedule. The composer only helps write it; the node
/// types it into Claude as it is. The rules are the web's `web/src/lib/loop-command.ts`.
public enum LoopCommand {
    /// The command word, as the composer's menus put it in the box (the interval and the task follow).
    public static let text = "/loop "

    /// The draft is a /loop command: the word alone or followed by white space (not "/looping").
    public static func isDraft(_ draft: String) -> Bool {
        guard draft.count >= 5, draft.prefix(5).lowercased() == "/loop" else { return false }
        guard let next = draft.dropFirst(5).first else { return true }
        return next.isWhitespace
    }

    /// What a /loop draft will do, in words; nil for any other draft.
    public static func hint(_ draft: String) -> LoopHint? {
        guard isDraft(draft) else { return nil }
        let words = draft.dropFirst(5).split(whereSeparator: \.isWhitespace).map(String.init)
        let every = words.first.flatMap(interval)
        let task = (every == nil ? words : Array(words.dropFirst())).joined(separator: " ")
        guard !task.isEmpty else {
            return LoopHint(text: every.map { String(localized: "Every \($0): no task written yet") }
                                ?? String(localized: "/loop [interval] task, e.g. /loop 10m check CI. No interval: Claude's own pace"),
                            missing: true)
        }
        return LoopHint(text: every.map { String(localized: "Repeats every \($0): \(task)") } ?? String(localized: "Repeats at Claude's own pace: \(task)"),
                        missing: false)
    }

    /// Claude Code has /loop; Codex, TRAE CLI and other runtimes do not, and the assistant's composer offers no commands
    /// (as on the web).
    public static func offered(agent: String?, sourceKind: String?, assistant: Bool) -> Bool {
        guard !assistant else { return false }
        let claude = agent == nil || agent == "" || agent == "claude"
        return claude && !(sourceKind ?? "").hasPrefix("codex")
    }

    /// Claude Code's interval forms, a number and s / m / h / d, in the web's words ("10m" → "10 minutes"; the catalog
    /// has "1 minute"); nil for anything else.
    static func interval(_ word: String) -> String? {
        guard word.count >= 2, let unit = word.last?.lowercased(), let n = Int(word.dropLast()),
              word.dropLast().allSatisfy({ $0.isASCII && $0.isNumber }) else { return nil }
        switch unit {
        case "s": return String(localized: "\(n) seconds")
        case "m": return String(localized: "\(n) minutes")
        case "h": return String(localized: "\(n) hours")
        case "d": return String(localized: "\(n) days")
        default: return nil
        }
    }
}
