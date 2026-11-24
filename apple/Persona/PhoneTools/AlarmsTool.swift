import Foundation
#if os(iOS) && canImport(AlarmKit)
import AlarmKit
import SwiftUI
#if DEBUG
import ActivityKit
#endif
#endif
import PersonaWire

/// Alarms and timers through AlarmKit (#2278): they ring like the Clock app's, through silent mode and Focus. Persona
/// only sees and changes the ones it set itself; the Clock app's own alarms are out of reach.
enum AlarmsTool {
    static var tools: [PhoneTool] {
        #if os(iOS) && canImport(AlarmKit)
        guard #available(iOS 26.0, *) else { return [] } // AlarmKit came with iOS 26
        return [
            PhoneTool(
                spec: ToolSpec(
                    name: "phone_alarms_list",
                    description: """
                    List the alarms and timers Persona set on the iPhone, with id, label, state (scheduled, \
                    countdown, paused, alerting), time or repeat days, and a timer's length. Alarms made in the \
                    Clock app are not visible to Persona and cannot be changed.
                    """,
                    inputSchema: Schema.object([:])
                ),
                source: .alarms,
                run: { _ in try list() }
            ),
            PhoneTool(
                spec: ToolSpec(
                    name: "phone_alarm_create",
                    description: """
                    Set an alarm that rings like the Clock app's, even in silent mode or Focus. `time` is either \
                    HH:mm (24-hour, the next time it comes, or on the `repeat` days) or an ISO 8601 date and time \
                    with a timezone for one exact moment. The user must approve on the phone. Returns the alarm's id.
                    """,
                    inputSchema: Schema.object([
                        "time": Schema.string("HH:mm (24-hour), or ISO 8601 date-time with a timezone."),
                        "repeat": Schema.array("Days it repeats on, with HH:mm: mon, tue, wed, thu, fri, sat, sun. Default: once.",
                                               of: Schema.string("A weekday.", values: AlarmRequest.weekdays)),
                        "label": Schema.string("What the alarm is for, shown when it rings."),
                    ], required: ["time"])
                ),
                source: .alarms,
                confirmation: { args in alarmConfirmation(args) },
                run: { args in try await createAlarm(args) }
            ),
            PhoneTool(
                spec: ToolSpec(
                    name: "phone_timer_start",
                    description: """
                    Start a countdown timer that rings when it ends, even in silent mode or Focus. The user must \
                    approve on the phone. Returns the timer's id (pause, resume and cancel it with that id).
                    """,
                    inputSchema: Schema.object([
                        "seconds": Schema.integer("Length in seconds, 1–86400.", min: 1, max: 86400),
                        "label": Schema.string("What the timer is for."),
                    ], required: ["seconds"])
                ),
                source: .alarms,
                confirmation: { args in timerConfirmation(args) },
                run: { args in try await startTimer(args) }
            ),
            PhoneTool(
                spec: ToolSpec(
                    name: "phone_alarm_cancel",
                    description: """
                    Cancel (delete) an alarm or timer Persona set: pass its id from phone_alarms_list. The user must \
                    approve on the phone.
                    """,
                    inputSchema: Schema.object([
                        "id": Schema.string("Alarm or timer id from phone_alarms_list."),
                    ], required: ["id"])
                ),
                source: .alarms,
                confirmation: { args in cancelConfirmation(args) },
                run: { args in try cancel(args) }
            ),
            PhoneTool(
                spec: ToolSpec(
                    name: "phone_alarm_pause",
                    description: """
                    Pause a running timer Persona set (its countdown stops until resumed). Pass its id from \
                    phone_alarms_list.
                    """,
                    inputSchema: Schema.object([
                        "id": Schema.string("Timer id from phone_alarms_list."),
                    ], required: ["id"])
                ),
                source: .alarms,
                run: { args in try pause(args, resume: false) }
            ),
            PhoneTool(
                spec: ToolSpec(
                    name: "phone_alarm_resume",
                    description: """
                    Resume a paused timer Persona set. Pass its id from phone_alarms_list.
                    """,
                    inputSchema: Schema.object([
                        "id": Schema.string("Timer id from phone_alarms_list."),
                    ], required: ["id"])
                ),
                source: .alarms,
                run: { args in try pause(args, resume: true) }
            ),
        ]
        #else
        []
        #endif
    }

    // MARK: - Arguments

    /// When an alarm rings, read from the arguments before anything is set.
    nonisolated struct AlarmRequest: Equatable, Sendable {
        nonisolated enum When: Equatable, Sendable {
            /// One exact moment.
            case at(Date)
            /// A time of day: the next one, or on these weekdays (mon…sun) every week.
            case time(hour: Int, minute: Int, weekdays: [String])
        }

        static let weekdays = ["mon", "tue", "wed", "thu", "fri", "sat", "sun"]

        var when: When
        var label: String?

        static func parse(_ args: JSONValue, now: Date = Date()) throws -> AlarmRequest {
            guard let raw = ToolArgs.string(args, "time") else { throw ToolFailure(.invalidArgument, "time is required") }
            var days: [String] = []
            if case .array(let items)? = args["repeat"] {
                for item in items {
                    let day = String((item.string ?? "").lowercased().prefix(3))
                    guard weekdays.contains(day) else {
                        throw ToolFailure(.invalidArgument, "repeat takes mon, tue, wed, thu, fri, sat or sun")
                    }
                    if !days.contains(day) { days.append(day) }
                }
            }
            let label = ToolArgs.string(args, "label")
            let clock = raw.split(separator: ":")
            if clock.count == 2, let hour = Int(clock[0]), let minute = Int(clock[1]), raw.count <= 5 {
                guard (0...23).contains(hour), (0...59).contains(minute) else {
                    throw ToolFailure(.invalidArgument, "time \"\(raw)\" is not a time of day (00:00–23:59)")
                }
                return AlarmRequest(when: .time(hour: hour, minute: minute, weekdays: weekdays.filter(days.contains)), label: label)
            }
            guard days.isEmpty else {
                throw ToolFailure(.invalidArgument, "repeat goes with a time of day (HH:mm), not a date")
            }
            guard let date = try ToolDate.parse(raw), date > now else {
                throw ToolFailure(.invalidArgument, "time \"\(raw)\" is in the past")
            }
            return AlarmRequest(when: .at(date), label: label)
        }
    }

    /// A timer's length, 1 second to 24 hours.
    nonisolated static func timerSeconds(_ args: JSONValue) throws -> Int {
        guard let seconds = ToolArgs.int(args, "seconds"), (1...86_400).contains(seconds) else {
            throw ToolFailure(.invalidArgument, "seconds must be 1–86400")
        }
        return seconds
    }

    // MARK: - Approval cards

    static func alarmSummary(_ request: AlarmRequest) -> String {
        var lines: [String] = []
        switch request.when {
        case .at(let date):
            let when = CardTime.text(date)
            lines.append(String(localized: "Set an alarm for \(when)"))
        case .time(let hour, let minute, let days):
            let when = String(format: "%02d:%02d", hour, minute)
            lines.append(String(localized: "Set an alarm at \(when)"))
            if !days.isEmpty {
                let names = days.map(weekdayName).joined(separator: ", ")
                lines.append(String(localized: "Repeats: \(names)"))
            }
        }
        if let label = request.label { lines.append(String(localized: "Label: \(label)")) }
        return lines.joined(separator: "\n")
    }

    static func timerSummary(seconds: Int, label: String?) -> String {
        let length = Duration.seconds(seconds).formatted(.units(allowed: [.hours, .minutes, .seconds], width: .wide))
        var lines = [String(localized: "Start a timer for \(length)")]
        if let label { lines.append(String(localized: "Label: \(label)")) }
        return lines.joined(separator: "\n")
    }

    /// The weekday's short name in the phone's language (for example, `mon` → `Mon`).
    nonisolated static func weekdayName(_ day: String) -> String {
        guard let index = AlarmRequest.weekdays.firstIndex(of: day) else { return day }
        var calendar = Calendar.current
        calendar.locale = Locale.current
        // shortWeekdaySymbols starts on Sunday.
        return calendar.shortWeekdaySymbols[(index + 1) % 7]
    }

    static func alarmConfirmation(_ args: JSONValue) -> String {
        guard let request = try? AlarmRequest.parse(args) else {
            return String(localized: "Invalid parameters; nothing will be set.")
        }
        return alarmSummary(request)
    }

    static func timerConfirmation(_ args: JSONValue) -> String {
        guard let seconds = try? timerSeconds(args) else {
            return String(localized: "Invalid parameters; nothing will be set.")
        }
        return timerSummary(seconds: seconds, label: ToolArgs.string(args, "label"))
    }

    static func cancelConfirmation(_ args: JSONValue) -> String {
        let id = ToolArgs.string(args, "id") ?? "?"
        if let label = Labels.all[id] { return String(localized: "Cancel the alarm “\(label)”") }
        return String(localized: "Cancel the alarm or timer \(id)")
    }

    /// Labels of the alarms Persona set, by id: AlarmKit does not hand an alarm's text back.
    enum Labels {
        static let key = "phoneAlarms.labels"

        static var all: [String: String] {
            get { UserDefaults.standard.dictionary(forKey: key) as? [String: String] ?? [:] }
            set { UserDefaults.standard.set(newValue, forKey: key) }
        }
    }

    #if DEBUG && os(iOS) && canImport(AlarmKit)
    /// The ActivityKit activity `drawSample` started, so `TerminationActivities` ends only the sample on SIGTERM and
    /// never a real AlarmKit timer.
    fileprivate(set) static var sampleActivityID: String?
    /// The `AlarmPresentationState.alarmID` of the active DEBUG sample activity (never scheduled in `AlarmManager`).
    fileprivate(set) static var sampleAlarmID: UUID?
    /// Every sample `alarmID` created in this process, so duplicate or late button taps after Stop still stay off
    /// `AlarmManager` (#13269).
    private static var knownSampleAlarmIDs: Set<UUID> = []
    #endif
}

// MARK: - AlarmKit

#if os(iOS) && canImport(AlarmKit)
@available(iOS 26.0, *)
extension AlarmsTool {
    static func list() throws -> [ToolContent] {
        try AlarmsAccess.require()
        let alarms = try AlarmManager.shared.alarms
        let labels = Labels.all
        // Forget the labels of alarms that are gone.
        let live = Set(alarms.map { $0.id.uuidString })
        if labels.keys.contains(where: { !live.contains($0) }) { Labels.all = labels.filter { live.contains($0.key) } }
        let rows = alarms.map { alarm -> JSONValue in
            Output.object([
                ("id", .string(alarm.id.uuidString)),
                ("label", Output.opt(labels[alarm.id.uuidString])),
                ("state", .string(stateName(alarm.state))),
                ("schedule", alarm.schedule.map(scheduleJSON)),
                ("timer_seconds", alarm.countdownDuration?.preAlert.map { JSONValue.number($0.rounded()) }),
            ])
        }
        return Output.json(Output.object([("count", .number(Double(rows.count))), ("alarms", .array(rows))]))
    }

    static func createAlarm(_ args: JSONValue) async throws -> [ToolContent] {
        let request = try AlarmRequest.parse(args)
        try AlarmsAccess.require()
        let schedule: Alarm.Schedule
        switch request.when {
        case .at(let date):
            schedule = .fixed(date)
        case .time(let hour, let minute, let days):
            let weekdays = days.compactMap(localeWeekday)
            let time = Alarm.Schedule.Relative.Time(hour: hour, minute: minute)
            schedule = .relative(Alarm.Schedule.Relative(time: time, repeats: weekdays.isEmpty ? .never : .weekly(weekdays)))
        }
        let id = UUID()
        let label = request.label ?? String(localized: "Alarm [Persona Alarm]")
        let configuration = AlarmManager.AlarmConfiguration<PersonaAlarmData>.alarm(
            schedule: schedule, attributes: attributes(label: label, timer: false))
        let alarm = try await AlarmManager.shared.schedule(id: id, configuration: configuration)
        Labels.all[id.uuidString] = label
        return Output.json(Output.object([
            ("id", .string(alarm.id.uuidString)),
            ("label", .string(label)),
            ("state", .string(stateName(alarm.state))),
            ("schedule", alarm.schedule.map(scheduleJSON)),
        ]))
    }

    static func startTimer(_ args: JSONValue) async throws -> [ToolContent] {
        let seconds = try timerSeconds(args)
        try AlarmsAccess.require()
        let label = ToolArgs.string(args, "label") ?? String(localized: "Timer [Persona Alarm]")
        let alarm = try await scheduleTimer(seconds: seconds, label: label)
        return Output.json(Output.object([
            ("id", .string(alarm.id.uuidString)),
            ("label", .string(label)),
            ("seconds", .number(Double(seconds))),
            ("ends_at", .string(ToolDate.string(Date().addingTimeInterval(TimeInterval(seconds))))),
            ("state", .string(stateName(alarm.state))),
        ]))
    }

    /// A countdown timer of Persona's, its label kept for phone_alarms_list.
    private static func scheduleTimer(seconds: Int, label: String) async throws -> Alarm {
        let id = UUID()
        let configuration = AlarmManager.AlarmConfiguration<PersonaAlarmData>.timer(
            duration: TimeInterval(seconds), attributes: attributes(label: label, timer: true))
        let alarm = try await AlarmManager.shared.schedule(id: id, configuration: configuration)
        Labels.all[id.uuidString] = label
        return alarm
    }

    static func cancel(_ args: JSONValue) throws -> [ToolContent] {
        try AlarmsAccess.require()
        let id = try alarmID(args)
        try AlarmManager.shared.cancel(id: id)
        Labels.all[id.uuidString] = nil
        return Output.json(Output.object([("id", .string(id.uuidString)), ("cancelled", .bool(true))]))
    }

    static func pause(_ args: JSONValue, resume: Bool) throws -> [ToolContent] {
        try AlarmsAccess.require()
        let id = try alarmID(args)
        if resume { try AlarmManager.shared.resume(id: id) } else { try AlarmManager.shared.pause(id: id) }
        return Output.json(Output.object([("id", .string(id.uuidString)), ("state", .string(resume ? "countdown" : "paused"))]))
    }

    #if DEBUG
    /// `-showAlarmSample`: on a device, asks for AlarmKit, cancels the alarms Persona set before, starts a 25-minute
    /// "Focus" timer as the tool does and, for `paused`, pauses it 3 s later. On the simulator (which cannot ring a
    /// timer and whose AlarmKit prompt varies by runtime), draws the same activity directly through ActivityKit with
    /// `sampleAttributes`, so screenshots never depend on a system prompt.
    static func showSample(paused: Bool) {
        Task {
            #if targetEnvironment(simulator)
            await drawSample(paused: paused)
            #else
            guard await AlarmsAccess.request() == .granted else { return await drawSample(paused: paused) }
            for alarm in (try? AlarmManager.shared.alarms) ?? [] { try? AlarmManager.shared.cancel(id: alarm.id) }
            guard let alarm = try? await scheduleTimer(seconds: 1500, label: "Focus"), paused else { return }
            try? await Task.sleep(for: .seconds(3))
            try? AlarmManager.shared.pause(id: alarm.id)
            #endif
        }
    }

    private static func drawSample(paused: Bool) async {
        let previousID = sampleActivityID
        sampleActivityID = nil
        sampleAlarmID = nil
        for activity in Activity<AlarmAttributes<PersonaAlarmData>>.activities
        where activity.attributes.metadata?.isSample == true || activity.id == previousID {
            await activity.end(nil, dismissalPolicy: .immediate)
        }
        let content = sampleContent(paused: paused, now: Date())
        // Both presentation states are encoded, so the sample carries only the state it draws (#12248).
        // The custom widget supplies the button's words and colours; only the presentation's action marker is required.
        let sample = sampleAttributes(label: "Focus", paused: paused)
        if let activity = try? Activity.request(attributes: sample, content: content) {
            sampleActivityID = activity.id
            sampleAlarmID = content.state.alarmID
            knownSampleAlarmIDs.insert(content.state.alarmID)
        }
    }

    /// The fields of an active `Activity<AlarmAttributes<PersonaAlarmData>>` that DEBUG sample action handling reads.
    struct SampleActivitySnapshot: Equatable, Sendable {
        var activityID: String
        var state: AlarmPresentationState
        var label: String
        var hasCountdownPauseButton: Bool
        var isSample: Bool
    }

    @concurrent
    private nonisolated static func updateLiveActivity(id: String, content: ActivityContent<AlarmPresentationState>) async {
        guard let activity: Activity<AlarmAttributes<PersonaAlarmData>> =
            Activity<AlarmAttributes<PersonaAlarmData>>.activities.first(where: { $0.id == id }) else { return }
        await activity.update(content)
    }

    @concurrent
    private nonisolated static func endLiveActivity(id: String) async {
        guard let activity: Activity<AlarmAttributes<PersonaAlarmData>> =
            Activity<AlarmAttributes<PersonaAlarmData>>.activities.first(where: { $0.id == id }) else { return }
        await activity.end(nil, dismissalPolicy: .immediate)
    }

    /// ActivityKit operations used by DEBUG sample action handling, injectable so unit tests can verify Pause, Resume
    /// and Stop end-to-end without the live ActivityKit daemon.
    struct SampleActivityBackend: Sendable {
        var find: @MainActor @Sendable (UUID) -> SampleActivitySnapshot?
        var update: @MainActor @Sendable (String, ActivityContent<AlarmPresentationState>) async -> Void
        var end: @MainActor @Sendable (String) async -> Void
        var request: @MainActor @Sendable (AlarmAttributes<PersonaAlarmData>, ActivityContent<AlarmPresentationState>) -> String?

        static var live: Self {
            Self(
                find: { alarmID in
                    Activity<AlarmAttributes<PersonaAlarmData>>.activities
                        .first { $0.content.state.alarmID == alarmID }
                        .map { activity in
                            SampleActivitySnapshot(
                                activityID: activity.id,
                                state: activity.content.state,
                                label: activity.attributes.metadata?.label ?? "Focus",
                                hasCountdownPauseButton: activity.attributes.presentation.countdown?.pauseButton != nil,
                                isSample: activity.attributes.metadata?.isSample == true
                            )
                        }
                },
                update: { activityID, content in
                    await updateLiveActivity(id: activityID, content: content)
                },
                end: { activityID in
                    await endLiveActivity(id: activityID)
                },
                request: { attributes, content in
                    (try? Activity.request(attributes: attributes, content: content))?.id
                }
            )
        }
    }

    /// Next `AlarmPresentationState` for a DEBUG sample button tap, or `nil` when `Stop` ends the sample activity.
    nonisolated static func sampleState(
        after action: AlarmIntentAction,
        from state: AlarmPresentationState,
        now: Date = Date()
    ) -> AlarmPresentationState? {
        switch action {
        case .stop:
            return nil
        case .pause:
            guard case .countdown(let countdown) = state.mode else { return state }
            let elapsed = min(
                countdown.totalCountdownDuration,
                countdown.previouslyElapsedDuration + max(0, now.timeIntervalSince(countdown.startDate))
            )
            let paused = AlarmPresentationState.Mode.paused(.init(
                totalCountdownDuration: countdown.totalCountdownDuration,
                previouslyElapsedDuration: elapsed
            ))
            return .init(alarmID: state.alarmID, mode: paused)
        case .resume:
            guard case .paused(let paused) = state.mode else { return state }
            let remaining = max(0, paused.totalCountdownDuration - paused.previouslyElapsedDuration)
            let countdown = AlarmPresentationState.Mode.countdown(.init(
                totalCountdownDuration: paused.totalCountdownDuration,
                previouslyElapsedDuration: paused.previouslyElapsedDuration,
                startDate: now,
                fireDate: now.addingTimeInterval(remaining)
            ))
            return .init(alarmID: state.alarmID, mode: countdown)
        }
    }

    /// Handles a Live Activity button tap (`Pause`, `Resume`, `Stop`) when `alarmID` belongs to the DEBUG
    /// ActivityKit-only timer sample (#13269). Returns `true` when claimed by the sample (so `AlarmManager` is never
    /// called with an unscheduled UUID), and `false` for any real timer ID.
    static func handleSampleAction(
        _ action: AlarmIntentAction,
        alarmID: UUID,
        now: Date = Date(),
        backend: SampleActivityBackend = .live
    ) async -> Bool {
        let candidate = backend.find(alarmID)
        let sample = candidate.flatMap { ($0.isSample || $0.activityID == sampleActivityID) ? $0 : nil }
        let isKnownSample = sample != nil || sampleAlarmID == alarmID || knownSampleAlarmIDs.contains(alarmID)
        guard isKnownSample else { return false }
        knownSampleAlarmIDs.insert(alarmID)
        guard let sample else {
            if action == .stop && sampleAlarmID == alarmID {
                sampleActivityID = nil
                sampleAlarmID = nil
            }
            return true
        }
        guard let next = sampleState(after: action, from: sample.state, now: now) else {
            await backend.end(sample.activityID)
            if sampleActivityID == sample.activityID { sampleActivityID = nil }
            if sampleAlarmID == alarmID { sampleAlarmID = nil }
            return true
        }
        let nextContent = ActivityContent(state: next, staleDate: nil)
        // A sample launched with `-showAlarmSample paused` omits `countdown` to stay under ActivityKit's 4 KB limit
        // (#12248). When resumed, replace it with a running sample activity carrying the resumed state so `AlarmButtons`
        // exposes Pause again.
        if case .countdown = next.mode, !sample.hasCountdownPauseButton {
            await backend.end(sample.activityID)
            let running = sampleAttributes(label: sample.label, paused: false)
            if let replacementID = backend.request(running, nextContent) {
                sampleActivityID = replacementID
                sampleAlarmID = next.alarmID
            } else {
                if sampleActivityID == sample.activityID { sampleActivityID = nil }
                if sampleAlarmID == alarmID { sampleAlarmID = nil }
            }
            return true
        }
        await backend.update(sample.activityID, nextContent)
        return true
    }

    /// Resets in-memory DEBUG sample tracking (for unit tests verifying clean state and post-relaunch recognition).
    static func resetSampleTracking(sampleActivityID: String? = nil, sampleAlarmID: UUID? = nil) {
        self.sampleActivityID = sampleActivityID
        self.sampleAlarmID = sampleAlarmID
        knownSampleAlarmIDs = sampleAlarmID.map { [$0] } ?? []
    }

    /// The exact content delivered to the simulator's ActivityKit sample. AlarmKit owns the real timer's ContentState
    /// and its countdown/pause/resume updates; this never replaces or approximates that path.
    static func sampleContent(paused: Bool, now: Date = Date()) -> ActivityContent<AlarmPresentationState> {
        let mode: AlarmPresentationState.Mode = paused
            ? .paused(.init(totalCountdownDuration: 1500, previouslyElapsedDuration: 301))
            : .countdown(.init(totalCountdownDuration: 1500, previouslyElapsedDuration: 0, startDate: now, fireDate: now.addingTimeInterval(1500)))
        return .init(state: .init(alarmID: UUID(), mode: mode), staleDate: nil)
    }
    #endif

    /// A sample needs only what `AlarmLiveActivity` reads: the alert's title as the metadata fallback, the label, the
    /// resolved accent, and the current state's action marker. Both timer presentations are encoded, so the sample
    /// retains just the Pause or Resume it draws; the extension supplies all three buttons' own words and colours.
    /// Real timers use the unchanged `attributes(label:timer:)` below, with both states for AlarmKit to choose.
    static func sampleAttributes(label: String, paused: Bool) -> AlarmAttributes<PersonaAlarmData> {
        let real = attributes(label: label, timer: true)
        // These resources are display-only for the sample and need no app localization lookup. Avoid repeating its
        // bundle path and sandbox token in every title; the label is unchanged (#12248).
        let title = sampleTitle(defaultValue: label)
        let symbol = paused ? Theme.Icon.run : Theme.Icon.pause
        let button = AlarmButton(text: title, textColor: real.tintColor, systemImageName: symbol)
        let stop = sampleStopButton(title: title, tintColor: real.tintColor)
        let alert = AlarmPresentation.Alert(title: title, stopButton: stop)
        let presentation = paused
            ? AlarmPresentation(alert: alert, paused: .init(title: title, resumeButton: button))
            : AlarmPresentation(alert: alert, countdown: .init(title: title, pauseButton: button))
        #if DEBUG
        let metadata = PersonaAlarmData(label: label, isSample: true)
        #else
        let metadata = real.metadata
        #endif
        return AlarmAttributes(presentation: presentation, metadata: metadata, tintColor: real.tintColor)
    }

    /// The label as the resource's fallback, not a localization key, for the custom sample (#12248).
    static func sampleTitle(key: StaticString = "sample.title", defaultValue: String) -> LocalizedStringResource {
        .init(key, defaultValue: .init(defaultValue), bundle: .atURL(URL(fileURLWithPath: "")))
    }

    /// The required Alert Stop configuration's actual input, before AlarmKit normalizes its unused getter.
    static func sampleStopButton(title: LocalizedStringResource, tintColor: Color) -> AlarmButton {
        alarmButton(text: title, textColor: tintColor, systemImageName: Theme.Icon.interrupt)
    }

    /// An id Persona's own alarms have; anything else is refused (the Clock app's alarms are out of reach).
    private static func alarmID(_ args: JSONValue) throws -> UUID {
        guard let raw = ToolArgs.string(args, "id"), let id = UUID(uuidString: raw) else {
            throw ToolFailure(.invalidArgument, "id must be an id from phone_alarms_list")
        }
        guard try AlarmManager.shared.alarms.contains(where: { $0.id == id }) else {
            throw ToolFailure(.invalidArgument, "no alarm or timer of Persona's has id \(raw); call phone_alarms_list")
        }
        return id
    }

    /// AlarmKit attributes with `Theme.accent` resolved once: a dynamic `Theme.accent` encodes to ~5.7 KB each (~23 KB
    /// across the buttons and tint). Each string also carries its bundle and a sandbox token, and each colour an archived
    /// UIColor. AlarmKit's daemon starts real timers and may exceed 4 KB (docs/apple/live-activity.md "Alarms and timers
    /// (AlarmKit)"). Real timers keep both in memory; `AlarmPresentation` encodes both optional states when present.
    static func attributes(label: String, timer: Bool) -> AlarmAttributes<PersonaAlarmData> {
        let accent = Color(Theme.accent.resolve(in: EnvironmentValues()))
        let title = LocalizedStringResource(stringLiteral: label)
        func button(_ text: String, _ symbol: String) -> AlarmButton {
            alarmButton(text: LocalizedStringResource(stringLiteral: text), textColor: accent, systemImageName: symbol)
        }
        let alert = AlarmPresentation.Alert(title: title, stopButton: button(String(localized: "Stop [Persona Alarm]"), Theme.Icon.interrupt))
        let presentation: AlarmPresentation
        if timer {
            presentation = AlarmPresentation(
                alert: alert,
                countdown: AlarmPresentation.Countdown(title: title, pauseButton: button(String(localized: "Pause [Persona Alarm]"), Theme.Icon.pause)),
                paused: AlarmPresentation.Paused(title: title, resumeButton: button(String(localized: "Resume [Persona Alarm]"), Theme.Icon.run)))
        } else {
            presentation = AlarmPresentation(alert: alert)
        }
        return AlarmAttributes(presentation: presentation, metadata: PersonaAlarmData(label: label), tintColor: accent)
    }

    static func alarmButton(text: LocalizedStringResource, textColor: Color, systemImageName: String) -> AlarmButton {
        .init(text: text, textColor: textColor, systemImageName: systemImageName)
    }

    /// `mon`…`sun` as AlarmKit names the day (the Alarms and Timers page reads its schedules back with it).
    nonisolated static func localeWeekday(_ day: String) -> Locale.Weekday? {
        switch day {
        case "mon": .monday
        case "tue": .tuesday
        case "wed": .wednesday
        case "thu": .thursday
        case "fri": .friday
        case "sat": .saturday
        case "sun": .sunday
        default: nil
        }
    }

    /// The state's name in `phone_alarms_list`, and `AlarmEntry.State`'s (the Alarms and Timers page).
    nonisolated static func stateName(_ state: Alarm.State) -> String {
        switch state {
        case .scheduled: "scheduled"
        case .countdown: "countdown"
        case .paused: "paused"
        case .alerting: "alerting"
        @unknown default: "unknown"
        }
    }

    private static func scheduleJSON(_ schedule: Alarm.Schedule) -> JSONValue {
        if case .fixed(let date) = schedule {
            return Output.object([("at", .string(ToolDate.string(date)))])
        }
        if case .relative(let relative) = schedule {
            var days: [JSONValue] = []
            if case .weekly(let weekdays) = relative.repeats {
                days = weekdays.map { .string(String(describing: $0).prefix(3).lowercased()) }
            }
            return Output.object([
                ("time", .string(String(format: "%02d:%02d", relative.time.hour, relative.time.minute))),
                ("repeat", days.isEmpty ? nil : .array(days)),
            ])
        }
        return .null
    }
}
#endif

#if os(iOS) && canImport(AlarmKit)
/// AlarmKit's permission (`NSAlarmKitUsageDescription`).
enum AlarmsAccess {
    /// AlarmKit came with iOS 26: before it there are no alarm tools (`PhoneDataSource.onThisDevice`).
    static var isSupported: Bool {
        if #available(iOS 26.0, *) { true } else { false }
    }

    static func status() -> PhoneAuthStatus {
        guard #available(iOS 26.0, *) else { return .denied }
        return switch AlarmManager.shared.authorizationState {
        case .authorized: .granted
        case .denied: .denied
        case .notDetermined: .notDetermined
        @unknown default: .notDetermined
        }
    }

    static func request() async -> PhoneAuthStatus {
        guard #available(iOS 26.0, *) else { return .denied }
        _ = try? await AlarmManager.shared.requestAuthorization()
        return status()
    }

    static func require() throws {
        switch status() {
        case .granted, .limited, .notNeeded: return
        case .denied: throw ToolFailure(.denied, "Alarms (AlarmKit); nothing was set")
        case .notDetermined: throw ToolFailure(.denied, "Alarms access has not been requested yet; nothing was set")
        }
    }
}
#elseif os(iOS)
/// An SDK without AlarmKit: no alarm tools.
enum AlarmsAccess {
    static var isSupported: Bool { false }
    static func status() -> PhoneAuthStatus { .denied }
    static func request() async -> PhoneAuthStatus { .denied }
}
#endif
