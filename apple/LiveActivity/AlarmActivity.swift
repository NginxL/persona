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
    #if DEBUG
    /// Marks the DEBUG ActivityKit-only timer sample (`AlarmsTool.drawSample`) so its buttons update or end only the
    /// sample activity instead of calling `AlarmManager` with an unscheduled UUID (#13269). Nil for real timers.
    var isSample: Bool? = nil
    #endif
}

/// The operation a timer Live Activity button asks the app to perform.
nonisolated enum AlarmIntentAction: Equatable, Sendable {
    case pause, resume, stop
}

#if !WIDGET_EXTENSION
/// Routes timer Live Activity button taps. Real timer IDs go directly to `AlarmManager.shared`; in DEBUG builds, IDs
/// belonging to the ActivityKit-only timer sample (`AlarmsTool.drawSample`) are handled on the sample activity and
/// never reach `AlarmManager` (#13269).
@available(iOS 26.0, *)
nonisolated enum AlarmIntentRouter {
    nonisolated struct Deps: Sendable {
        var pauseAlarm: @Sendable (UUID) throws -> Void
        var resumeAlarm: @Sendable (UUID) throws -> Void
        var stopAlarm: @Sendable (UUID) throws -> Void
        #if DEBUG
        /// Handles an action for the DEBUG ActivityKit-only sample, returning `true` when `alarmID` belongs to the
        /// sample so `AlarmManager` is never called with an unscheduled UUID.
        var handleSample: @Sendable (AlarmIntentAction, UUID) async -> Bool
        #endif

        static var live: Self {
            #if DEBUG
            Self(
                pauseAlarm: { try AlarmManager.shared.pause(id: $0) },
                resumeAlarm: { try AlarmManager.shared.resume(id: $0) },
                stopAlarm: { try AlarmManager.shared.stop(id: $0) },
                handleSample: { action, id in await AlarmsTool.handleSampleAction(action, alarmID: id) }
            )
            #else
            Self(
                pauseAlarm: { try AlarmManager.shared.pause(id: $0) },
                resumeAlarm: { try AlarmManager.shared.resume(id: $0) },
                stopAlarm: { try AlarmManager.shared.stop(id: $0) }
            )
            #endif
        }
    }

    #if DEBUG
    @TaskLocal private static var scopedDeps: Deps?

    /// Runs `operation` with overridden dependencies so tests can exercise `PauseAlarmIntent`, `ResumeAlarmIntent` and
    /// `StopAlarmIntent` end-to-end without touching the system `AlarmManager`.
    static func withDeps<T>(_ deps: Deps, operation: () async throws -> T) async rethrows -> T {
        try await $scopedDeps.withValue(deps, operation: operation)
    }
    #endif

    static func perform(_ action: AlarmIntentAction, alarmID raw: String, deps: Deps? = nil) async throws {
        guard let id = UUID(uuidString: raw) else { return }
        let active: Deps
        #if DEBUG
        active = deps ?? scopedDeps ?? .live
        if await active.handleSample(action, id) { return }
        #else
        active = deps ?? .live
        #endif
        switch action {
        case .pause: try active.pauseAlarm(id)
        case .resume: try active.resumeAlarm(id)
        case .stop: try active.stopAlarm(id)
        }
    }
}
#endif

/// The activity's Pause: stops a timer's countdown until Resume.
struct PauseAlarmIntent: LiveActivityIntent {
    nonisolated static let title: LocalizedStringResource = "Pause Timer [Persona Alarm]"
    nonisolated static let isDiscoverable = false
    nonisolated static var authenticationPolicy: IntentAuthenticationPolicy { .alwaysAllowed }

    @Parameter(title: "Timer [Persona Alarm]") var alarmID: String

    nonisolated init() {}
    nonisolated init(alarmID: UUID) { self.alarmID = alarmID.uuidString }

    nonisolated func perform() async throws -> some IntentResult {
        #if !WIDGET_EXTENSION
        if #available(iOS 26.0, *) { try await AlarmIntentRouter.perform(.pause, alarmID: alarmID) }
        #endif
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
        #if !WIDGET_EXTENSION
        if #available(iOS 26.0, *) { try await AlarmIntentRouter.perform(.resume, alarmID: alarmID) }
        #endif
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
        #if !WIDGET_EXTENSION
        if #available(iOS 26.0, *) { try await AlarmIntentRouter.perform(.stop, alarmID: alarmID) }
        #endif
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
