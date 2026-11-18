import Foundation
import Observation
import PersonaKit

/// The localized, classified retry state derived once per live event, never parsed or formatted in a view body.
nonisolated struct SessionLiveRetryPresentation: Hashable, Sendable {
    var error: APIErrorInfo
    var attempt: Int
    var maxAttempts: Int
    var attemptText: String

    static func of(_ live: SessionLive?, working: Bool = true, subscribed: Bool = true, lang: String? = nil) -> Self? {
        guard working, subscribed, let live, let raw = live.retryError?.trimmingCharacters(in: .whitespacesAndNewlines),
              !raw.isEmpty, let attempt = live.retryAttempt, attempt > 0, let maxAttempts = live.retryMax, maxAttempts > 0,
              let error = APIErrorInfo.parseRetry(raw) else { return nil }
        var resource: LocalizedStringResource = "Retrying model request (\(attempt)/\(maxAttempts))"
        resource.locale = lang.map(Locale.init) ?? interfaceTextLocale
        let attemptText = String(localized: resource) // perf-ok: resolved once per changed live event, not in a view body
        return Self(error: error, attempt: attempt, maxAttempts: maxAttempts, attemptText: attemptText)
    }
}

/// One chat's retry presentation, observed without redrawing another chat when its attempt changes.
@Observable final class SessionLiveRetryCell {
    var value: SessionLiveRetryPresentation?
    init(_ value: SessionLiveRetryPresentation? = nil) { self.value = value }
}

nonisolated extension APIErrorInfo {
    /// A live retry has the final answer's error without its "API Error:" prefix. Normalize that new input shape only,
    /// preserve the observed text as the detail, and leave final-answer parsing unchanged.
    static func parseRetry(_ text: String?) -> APIErrorInfo? {
        guard let raw = text?.trimmingCharacters(in: .whitespacesAndNewlines), !raw.isEmpty else { return nil }
        let normalized = raw.hasPrefix("API Error:") ? raw : raw == "API Error" ? "API Error:" : "API Error: \(raw)"
        guard var info = parse(normalized) else { return nil }
        info.raw = raw
        return info
    }
}
