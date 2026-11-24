import PersonaKit
import PersonaWire

/// What a turn's API error says, as a person would: the parse (three regexes) is done once per distinct text.
nonisolated enum TurnErrorReason {
    private static let memo = TextMemo<String?>(limit: 256)

    /// Whether the hub or the legacy raw text proves that this conversation cannot continue.
    static func invalid(_ session: Session?) -> Bool {
        session?.turnErrorKind == "unrecoverable" || session?.turnErrorCode == "compaction_sealed" ||
            APIErrorInfo.unrecoverable(session?.turnError)
    }

    /// The row's full guidance for a permanent failure, else the parsed/raw turn reason.
    static func of(_ session: Session?) -> String? {
        if invalid(session) { return APIErrorInfo.invalidConversationReason }
        return of(session?.turnError)
    }

    /// The reason in an "API Error: ..." text; nil when the text is not one.
    static func of(_ text: String?) -> String? {
        guard let text else { return nil }
        return memo(text) { APIErrorInfo.parse($0)?.reason }
    }
}
