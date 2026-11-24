#if os(iOS)
import Foundation
import Observation
#if canImport(AlarmKit)
import ActivityKit
import AlarmKit
#endif

/// Settings › Alarms and Timers (#12805, `AlarmsPage`): the alarms and timers Persona set, followed while the page is in
/// front, and what the page does to them. AlarmKit is reached only through `Deps`, so tests and DEBUG shots run on
/// made-up data. A tap shows its result at once; AlarmKit's next list confirms it.
@MainActor @Observable
final class AlarmsPageModel {
    struct Deps {
        /// AlarmKit's permission, read without asking.
        var access: @MainActor () -> PhoneAuthStatus
        /// Asks for it: only from the page's button, never on opening it.
        var request: @MainActor () async -> PhoneAuthStatus
        /// Every alarm and timer now, then again after each change, until the caller stops listening.
        var entries: @MainActor () -> AsyncStream<[AlarmEntry]>
        /// Deletes one and forgets its name, as `phone_alarm_cancel` does.
        var cancel: @MainActor (UUID) throws -> Void
        /// Pauses a timer, or resumes it (true), as `phone_alarm_pause` / `phone_alarm_resume` do.
        var pause: @MainActor (_ id: UUID, _ resume: Bool) throws -> Void
    }

    /// How long a pause or resume tapped here is shown while AlarmKit's lists still say otherwise.
    static let settle: TimeInterval = 2

    let deps: Deps
    /// Nil until the page first reads it.
    private(set) var access: PhoneAuthStatus?
    /// Nil until AlarmKit's first list.
    private(set) var entries: [AlarmEntry]?
    private(set) var timers: [AlarmEntry] = []
    private(set) var alarms: [AlarmEntry] = []
    private(set) var asking = false
    /// Bumped when the permission was asked for here, so the page follows AlarmKit again.
    private(set) var generation = 0
    /// Deleted here, kept off the list until AlarmKit's lists drop them too.
    private var deleted: Set<UUID> = []
    /// Paused or resumed here: the state shown until AlarmKit's lists say the same, or `settle` passes.
    private var expected: [UUID: (state: AlarmEntry.State, since: Date)] = [:]

    init(deps: Deps) {
        self.deps = deps
    }

    /// Reads the permission and, when Persona has it, follows AlarmKit's list until the task is cancelled (the page left,
    /// or its tab or the app went to the background).
    func follow() async {
        access = deps.access()
        guard access?.isUsable == true else { return }
        for await list in deps.entries() {
            merge(list)
        }
    }

    /// The button on the page without permission: asks once; afterwards the page follows AlarmKit if it was allowed.
    func requestAccess() async {
        guard !asking else { return }
        asking = true
        access = await deps.request()
        asking = false
        generation += 1
    }

    /// AlarmKit's list, with what was tapped here and has not reached it yet.
    func merge(_ incoming: [AlarmEntry], now: Date = Date()) {
        let ids = Set(incoming.map(\.id))
        deleted.formIntersection(ids)
        let previous = Dictionary((entries ?? []).map { ($0.id, $0) }, uniquingKeysWith: { first, _ in first })
        var shown: [AlarmEntry] = []
        for var entry in incoming where !deleted.contains(entry.id) {
            if let want = expected[entry.id] {
                if entry.state == want.state || now.timeIntervalSince(want.since) > Self.settle {
                    expected[entry.id] = nil
                } else if let local = previous[entry.id] {
                    shown.append(local)
                    continue
                }
            }
            // A clock its Live Activity has not caught up with yet: the one shown stays while the state does.
            if entry.face == nil, let old = previous[entry.id], old.state == entry.state, old.face != nil {
                entry.clock = old.clock
            }
            shown.append(entry)
        }
        expected = expected.filter { ids.contains($0.key) }
        publish(shown, now: now)
    }

    /// Deletes one; it leaves the list at once. False when AlarmKit refused (the row stays).
    @discardableResult func delete(_ entry: AlarmEntry, now: Date = Date()) -> Bool {
        do { try deps.cancel(entry.id) } catch { return false }
        deleted.insert(entry.id)
        expected[entry.id] = nil
        publish((entries ?? []).filter { $0.id != entry.id }, now: now)
        return true
    }

    /// Pauses a counting timer or resumes a paused one; the new state and its time left show at once. False when
    /// AlarmKit refused.
    @discardableResult func setPaused(_ entry: AlarmEntry, _ paused: Bool, now: Date = Date()) -> Bool {
        do { try deps.pause(entry.id, !paused) } catch { return false }
        var changed = entry
        changed.state = paused ? .paused : .countdown
        switch (paused, entry.face) {
        case (true, .counting(let ends)?): changed.clock = .paused(left: max(0, ends.timeIntervalSince(now)))
        case (false, .still(let left)?): changed.clock = .running(ends: now.addingTimeInterval(left))
        default: changed.clock = nil
        }
        expected[entry.id] = (changed.state, now)
        publish((entries ?? []).map { $0.id == entry.id ? changed : $0 }, now: now)
        return true
    }

    private func publish(_ list: [AlarmEntry], now: Date) {
        entries = list
        timers = AlarmEntry.timers(list, now: now)
        alarms = AlarmEntry.alarms(list)
    }
}

extension AlarmsPageModel.Deps {
    /// AlarmKit on iOS 26 and later; nothing before it (the page isn't offered there).
    static var live: Self {
        #if canImport(AlarmKit)
        if #available(iOS 26.0, *) {
            return Self(
                access: { AlarmsAccess.status() },
                request: { await AlarmsAccess.request() },
                entries: { AlarmFeed.stream() },
                // The tools' own calls, so the page and phone_alarm_cancel / _pause / _resume stay one path.
                cancel: { id in _ = try AlarmsTool.cancel(.object(["id": .string(id.uuidString)])) },
                pause: { id, resume in _ = try AlarmsTool.pause(.object(["id": .string(id.uuidString)]), resume: resume) })
        }
        #endif
        return Self(access: { .denied }, request: { .denied }, entries: { AsyncStream { $0.finish() } },
                    cancel: { _ in }, pause: { _, _ in })
    }

    /// The page's: AlarmKit, or in a DEBUG build `-openAlarms <sample>`'s made-up data.
    static var forPage: Self {
        #if DEBUG
        if let name = UserDefaults.standard.string(forKey: "openAlarms") { return .sample(name) }
        #endif
        return .live
    }
}

#if canImport(AlarmKit)
/// AlarmKit's alarms as `AlarmEntry`s, live (#12805): the list now (read off the main thread), then again on each change
/// AlarmKit reports, each timer with its Live Activity's clock. A timer whose activity has not caught up with its state is
/// read again a few times, half a second apart. Everything stops when the caller stops listening.
@available(iOS 26.0, *)
enum AlarmFeed {
    static let catchUpTries = 6
    static let catchUpStep: Duration = .milliseconds(500)

    static func stream() -> AsyncStream<[AlarmEntry]> {
        AsyncStream { continuation in
            let task = Task { @MainActor in
                var catchUp: Task<Void, Never>?
                defer { catchUp?.cancel() }
                @MainActor func publish(_ alarms: [Alarm]) {
                    let first = entries(alarms)
                    continuation.yield(first)
                    catchUp?.cancel()
                    guard first.contains(where: \.awaitsClock) else { return }
                    catchUp = Task { @MainActor in
                        var last = first
                        for _ in 0..<catchUpTries {
                            try? await Task.sleep(for: catchUpStep)
                            guard !Task.isCancelled else { return }
                            let again = entries(alarms)
                            if again != last { continuation.yield(again) }
                            last = again
                            if !again.contains(where: \.awaitsClock) { return }
                        }
                    }
                }
                if let now = await current() { publish(now) }
                for await alarms in AlarmManager.shared.alarmUpdates {
                    if Task.isCancelled { break }
                    publish(alarms)
                }
                continuation.finish()
            }
            continuation.onTermination = { _ in task.cancel() }
        }
    }

    /// AlarmKit's list now, asked off the main thread (its daemon answers it, P1); nil without permission.
    @concurrent static func current() async -> [Alarm]? {
        try? AlarmManager.shared.alarms
    }

    /// The alarms with the names Persona kept and their Live Activities' states.
    @MainActor static func entries(_ alarms: [Alarm]) -> [AlarmEntry] {
        var modes: [UUID: AlarmPresentationState.Mode] = [:]
        for activity in Activity<AlarmAttributes<PersonaAlarmData>>.activities {
            modes[activity.content.state.alarmID] = activity.content.state.mode
        }
        return AlarmEntry.entries(alarms, labels: AlarmsTool.Labels.all, activities: modes)
    }
}
#endif

#if DEBUG
extension AlarmsPageModel.Deps {
    /// `-openAlarms list|empty|denied|notAsked` (docs/apple/phone-tools.md "Alarms and timers page"): made-up alarms and
    /// timers with every state, none, or the page without permission. AlarmKit is never asked, so no system prompt shows.
    static func sample(_ name: String, now: Date = Date()) -> Self {
        let access: PhoneAuthStatus = switch name {
        case "denied": .denied
        case "notAsked": .notDetermined
        default: .granted
        }
        let list = name == "list" ? AlarmEntry.samples(now: now) : []
        return Self(access: { access }, request: { access },
                    entries: { AsyncStream { $0.yield(list) } },
                    cancel: { _ in }, pause: { _, _ in })
    }
}

extension AlarmEntry {
    /// Made-up alarms and timers, one in each state (DEBUG shots).
    static func samples(now: Date, calendar: Calendar = .current) -> [AlarmEntry] {
        let day = calendar.startOfDay(for: now)
        let flight = calendar.date(byAdding: DateComponents(day: 3, hour: 9, minute: 15), to: day) ?? now
        func sample(_ n: Int) -> UUID { UUID(uuidString: String(format: "00000000-0000-4000-8000-%012d", n)) ?? UUID() }
        return [
            AlarmEntry(id: sample(1), label: "Tea", state: .countdown, length: 300, clock: .running(ends: now.addingTimeInterval(272))),
            AlarmEntry(id: sample(2), label: "Focus", state: .paused, length: 1500, clock: .paused(left: 725)),
            AlarmEntry(id: sample(3), label: "Pasta", state: .alerting, length: 600),
            AlarmEntry(id: sample(4), label: "Morning run", state: .scheduled,
                       when: .time(hour: 6, minute: 40, weekdays: ["mon", "tue", "wed", "thu", "fri"])),
            AlarmEntry(id: sample(5), label: "Piano lesson", state: .scheduled, when: .time(hour: 17, minute: 30, weekdays: ["tue", "thu"])),
            AlarmEntry(id: sample(6), label: "Flight check-in", state: .scheduled, when: .at(flight)),
        ]
    }
}
#endif
#endif
