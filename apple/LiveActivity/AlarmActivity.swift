import AppIntents
import Foundation
#if os(iOS) && canImport(AlarmKit)
import AlarmKit
#endif

// Alarms and timers Persona sets through AlarmKit (#2278, docs/apple/phone-tools.md "iPhone system integrations"): what the
// system keeps with each, and the buttons of their Live Activity (PersonaWidgets `AlarmLiveActivity`), which draws a
// timer's countdown on the Lock Screen and in the Dynamic Island.
// Compiled into the app and the widget extension; the intents' bodies run in the app (LiveActivityIntent).

#if os(iOS) && canImport(AlarmKit)
/// What Persona keeps with an alarm it sets (AlarmKit's metadata): its label, which AlarmKit does not hand back otherwise.
@available(iOS 26.0, *)
nonisolated struct PersonaAlarmData: AlarmMetadata {
    var label: String?
}

/// The activity's Pause: stops a timer's countdown until Resume.
struct PauseAlarmIntent: LiveActivityIntent {
    nonisolated static let title: LocalizedStringResource = "Pause Timer [Persona Alarm]"
    nonisolated static let isDiscoverable = false
    nonisolated static var authenticationPolicy: IntentAuthenticationPolicy { .alwaysAllowed }

    @Parameter(title: "Timer [Persona Alarm]") var alarmID: String

    nonisolated init() {}
    nonisolated init(alarmID: UUID) { self.alarmID = alarmID.uuidString }

    nonisolated func perform() async throws -> some IntentResult {
        if #available(iOS 26.0, *), let id = UUID(uuidString: alarmID) { try AlarmManager.shared.pause(id: id) }
        return .result()
    }
}

/// The activity's Resume: a paused timer counts down again.
struct ResumeAlarmIntent: LiveActivityIntent {
    nonisolated static let title: LocalizedStringResource = "Resume Timer [Persona Alarm]"
    nonisolated static let isDiscoverable = false
    nonisolated static var authenticationPolicy: IntentAuthenticationPolicy { .alwaysAllowed }

    @Parameter(title: "Timer [Persona Alarm]") var alarmID: String

    nonisolated init() {}
    nonisolated init(alarmID: UUID) { self.alarmID = alarmID.uuidString }

    nonisolated func perform() async throws -> some IntentResult {
        if #available(iOS 26.0, *), let id = UUID(uuidString: alarmID) { try AlarmManager.shared.resume(id: id) }
        return .result()
    }
}

/// The activity's Stop: ends a timer before it rings, or silences an alarm or timer that is ringing.
struct StopAlarmIntent: LiveActivityIntent {
    nonisolated static let title: LocalizedStringResource = "Stop Alarm [Persona Alarm]"
    nonisolated static let isDiscoverable = false
    nonisolated static var authenticationPolicy: IntentAuthenticationPolicy { .alwaysAllowed }

    @Parameter(title: "Timer [Persona Alarm]") var alarmID: String

    nonisolated init() {}
    nonisolated init(alarmID: UUID) { self.alarmID = alarmID.uuidString }

    nonisolated func perform() async throws -> some IntentResult {
        if #available(iOS 26.0, *), let id = UUID(uuidString: alarmID) { try AlarmManager.shared.stop(id: id) }
        return .result()
    }
}
#endif

/// What the activity says about the time: left on a running timer, left on a paused one, or the alert. Plain values, so the
/// rules are tested without AlarmKit's types (`PhoneSystemToolsTests`).
nonisolated enum AlarmActivityTime {
    /// A paused timer's time left, as the Clock app writes it: 4:59, or 1:04:59 from an hour.
    static func paused(total: TimeInterval, elapsed: TimeInterval) -> String {
        let left = max(0, (total - elapsed).rounded(.down))
        let pattern: Duration.TimeFormatStyle.Pattern = left >= 3600 ? .hourMinuteSecond : .minuteSecond
        return Duration.seconds(left).formatted(.time(pattern: pattern))
    }

    /// The running countdown's range for `Text(timerInterval:)`: from its start (never after its end) to when it rings.
    static func countdown(start: Date, fire: Date) -> ClosedRange<Date> {
        min(start, fire)...fire
    }
}
