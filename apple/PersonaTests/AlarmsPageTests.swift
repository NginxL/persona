#if os(iOS) && canImport(AlarmKit)
import AlarmKit
#endif
import Foundation
import Testing
@testable import Persona

/// Settings › Alarms and Timers (#12805): what AlarmKit reports becomes a row's name, time, repeat and state; timers get
/// their time left from their Live Activity; the page shows a tap's result at once and opening it never asks for access.
@MainActor
struct AlarmsPageTests {
    private let now = Date(timeIntervalSince1970: 1_800_000_000)
    private let id = UUID(uuidString: "00000000-0000-4000-8000-000000000001")!
    private let other = UUID(uuidString: "00000000-0000-4000-8000-000000000002")!

    private var utc: Calendar {
        var c = Calendar(identifier: .gregorian)
        c.timeZone = .gmt
        return c
    }

    // MARK: - From AlarmKit

    #if os(iOS) && canImport(AlarmKit)
    /// Each AlarmKit state keeps the name phone_alarms_list gives it.
    @Test func everyStateHasTheToolsName() {
        guard #available(iOS 26.0, *) else { return }
        let states: [(Alarm.State, AlarmEntry.State)] = [(.scheduled, .scheduled), (.countdown, .countdown), (.paused, .paused), (.alerting, .alerting)]
        for (state, entry) in states {
            #expect(AlarmEntry(id: id, state: state, schedule: nil, countdown: nil, label: nil, activity: nil).state == entry)
            #expect(entry.rawValue == AlarmsTool.stateName(state))
        }
    }

    /// An alarm at a time of day: its name, its time on a 24- and a 12-hour clock, and how it repeats.
    @Test func aWeeklyAlarmBecomesARow() {
        guard #available(iOS 26.0, *) else { return }
        let weekdays = Alarm.Schedule.relative(.init(time: .init(hour: 6, minute: 40),
                                                     repeats: .weekly([.friday, .monday, .wednesday, .tuesday, .thursday])))
        let entry = AlarmEntry(id: id, state: .scheduled, schedule: weekdays, countdown: nil, label: "Morning run", activity: nil)
        #expect(!entry.isTimer && entry.title == "Morning run" && entry.state == .scheduled)
        #expect(entry.when == .time(hour: 6, minute: 40, weekdays: ["mon", "tue", "wed", "thu", "fri"]))
        #expect(entry.repeats == .weekdays)
        #expect(entry.detail() == String(localized: "Weekdays"))
        // The phone's own clock settings can drop the leading zero, so only the hour and minute are pinned.
        #expect(entry.timeText(locale: Locale(identifier: "en_GB"), calendar: utc)?.contains("6:40") == true)
        #expect(entry.timeText(locale: Locale(identifier: "en_US"), calendar: utc)?.contains("6:40") == true)
        #expect(entry.face == nil && !entry.canPause && !entry.canResume)

        func repeats(_ days: [Locale.Weekday]) -> AlarmEntry.Repeat {
            let s = Alarm.Schedule.relative(.init(time: .init(hour: 7, minute: 0), repeats: days.isEmpty ? .never : .weekly(days)))
            return AlarmEntry(id: id, state: .scheduled, schedule: s, countdown: nil, label: nil, activity: nil).repeats
        }
        #expect(repeats([]) == .once)
        #expect(repeats([.sunday, .saturday]) == .weekends)
        #expect(repeats([.monday, .tuesday, .wednesday, .thursday, .friday, .saturday, .sunday]) == .everyDay)
        #expect(repeats([.thursday, .tuesday]) == .days(["tue", "thu"]))
    }

    /// One exact moment rings once, on its day; an alarm without a name is "Alarm".
    @Test func aFixedAlarmRingsOnce() {
        guard #available(iOS 26.0, *) else { return }
        let at = utc.date(from: DateComponents(year: 2027, month: 1, day: 15, hour: 9, minute: 15))!
        let entry = AlarmEntry(id: id, state: .scheduled, schedule: .fixed(at), countdown: nil, label: nil, activity: nil)
        #expect(entry.when == .at(at) && entry.repeats == .once)
        #expect(entry.title == String(localized: "Alarm [Persona Alarm]"))
        #expect(entry.timeText(locale: Locale(identifier: "en_GB"), calendar: utc)?.contains("9:15") == true)
        let detail = entry.detail(locale: Locale(identifier: "en_GB"), calendar: utc)
        #expect(detail.hasSuffix(String(localized: "Once [Alarms Page]")) && detail.contains("15"))
    }

    /// A timer counts down to its Live Activity's fire date, or shows what's left while paused; a clock for the other
    /// state (its activity a step behind) isn't drawn, and a ringing timer is at 0.
    @Test func aTimerTakesItsClockFromItsLiveActivity() {
        guard #available(iOS 26.0, *) else { return }
        let length = Alarm.CountdownDuration(preAlert: 1500, postAlert: nil)
        let ends = now.addingTimeInterval(272)
        let counting = AlarmPresentationState.Mode.countdown(.init(totalCountdownDuration: 1500, previouslyElapsedDuration: 0,
                                                                   startDate: now, fireDate: ends))
        let paused = AlarmPresentationState.Mode.paused(.init(totalCountdownDuration: 1500, previouslyElapsedDuration: 775))

        let running = AlarmEntry(id: id, state: .countdown, schedule: nil, countdown: length, label: "Tea", activity: counting)
        #expect(running.isTimer && running.length == 1500 && running.when == nil)
        #expect(running.face == .counting(ends: ends) && running.canPause && !running.canResume && !running.awaitsClock)
        #expect(running.detail() == String(localized: "\(AlarmEntry.lengthText(1500)) timer [Alarms Page]"))

        let still = AlarmEntry(id: id, state: .paused, schedule: nil, countdown: length, label: "Focus", activity: paused)
        #expect(still.clock == .paused(left: 725) && still.face == .still(725) && still.canResume)
        #expect(AlarmEntry.clockText(725) == "12:05")
        #expect(AlarmEntry.clockText(3723) == "1:02:03")

        let behind = AlarmEntry(id: id, state: .paused, schedule: nil, countdown: length, label: nil, activity: counting)
        #expect(behind.face == nil && behind.awaitsClock)
        #expect(behind.title == String(localized: "Timer [Persona Alarm]"))

        let ringing = AlarmEntry(id: id, state: .alerting, schedule: nil, countdown: length, label: "Pasta", activity: nil)
        #expect(ringing.face == .still(0) && !ringing.canPause && !ringing.canResume && !ringing.awaitsClock)
    }
    #endif

    // MARK: - Order

    /// Timers before alarms; what rings first, then a timer by what is left and an alarm by its time of day.
    @Test func ringingFirstThenSoonest() {
        let timers = [
            AlarmEntry(id: UUID(), label: "Focus", state: .paused, length: 1500, clock: .paused(left: 725)),
            AlarmEntry(id: UUID(), label: "Tea", state: .countdown, length: 300, clock: .running(ends: now.addingTimeInterval(272))),
            AlarmEntry(id: UUID(), label: "Pasta", state: .alerting, length: 600),
        ]
        let alarms = [
            AlarmEntry(id: UUID(), label: "Piano", state: .scheduled, when: .time(hour: 17, minute: 30, weekdays: ["tue"])),
            AlarmEntry(id: UUID(), label: "Run", state: .scheduled, when: .time(hour: 6, minute: 40, weekdays: [])),
            AlarmEntry(id: UUID(), label: "Wake", state: .alerting, when: .time(hour: 7, minute: 0, weekdays: [])),
        ]
        let all = timers + alarms
        #expect(AlarmEntry.timers(all, now: now).map(\.title) == ["Pasta", "Tea", "Focus"])
        #expect(AlarmEntry.alarms(all, calendar: utc).map(\.title) == ["Wake", "Run", "Piano"])
    }

    // MARK: - The page

    /// Opening the page without permission reads nothing from AlarmKit and asks nothing: the button does.
    @Test func openingWithoutPermissionNeverAsks() async {
        for status in [PhoneAuthStatus.notDetermined, .denied] {
            let fake = FakeAlarms(access: status)
            let page = AlarmsPageModel(deps: fake.deps)
            await page.follow()
            #expect(page.access == status && page.entries == nil)
            #expect(fake.requests == 0 && fake.listened == 0)
        }
        let fake = FakeAlarms(access: .notDetermined, afterRequest: .granted)
        let page = AlarmsPageModel(deps: fake.deps)
        await page.follow()
        await page.requestAccess()
        #expect(fake.requests == 1 && page.access == .granted && page.generation == 1)
    }

    /// With permission it follows AlarmKit's lists, split into timers and alarms.
    @Test func followingShowsAlarmKitsList() async {
        let tea = AlarmEntry(id: id, label: "Tea", state: .countdown, length: 300, clock: .running(ends: now.addingTimeInterval(60)))
        let run = AlarmEntry(id: other, label: "Run", state: .scheduled, when: .time(hour: 6, minute: 40, weekdays: []))
        let fake = FakeAlarms(access: .granted, lists: [[tea], [tea, run]])
        let page = AlarmsPageModel(deps: fake.deps)
        await page.follow()
        #expect(fake.listened == 1 && page.entries?.count == 2)
        #expect(page.timers.map(\.id) == [id] && page.alarms.map(\.id) == [other])
    }

    /// Delete takes the row away at once, and a list AlarmKit sent before it took effect doesn't bring it back.
    @Test func aDeletedRowStaysGone() {
        let tea = AlarmEntry(id: id, label: "Tea", state: .countdown, length: 300)
        let run = AlarmEntry(id: other, label: "Run", state: .scheduled, when: .time(hour: 6, minute: 40, weekdays: []))
        let fake = FakeAlarms(access: .granted)
        let page = AlarmsPageModel(deps: fake.deps)
        page.merge([tea, run], now: now)
        #expect(page.delete(tea, now: now))
        #expect(fake.cancelled == [id] && page.entries?.map(\.id) == [other] && page.timers.isEmpty)
        page.merge([tea, run], now: now)
        #expect(page.entries?.map(\.id) == [other])

        fake.refuse = true
        #expect(!page.delete(run, now: now))
        #expect(page.entries?.map(\.id) == [other])
    }

    /// Pause shows Paused with what was left at once; a stale list keeps it, an agreeing one without a clock keeps its
    /// time left, and after a while AlarmKit's word wins again. Resume counts down from what was left.
    @Test func pauseAndResumeShowAtOnce() {
        let ends = now.addingTimeInterval(272)
        let tea = AlarmEntry(id: id, label: "Tea", state: .countdown, length: 300, clock: .running(ends: ends))
        let fake = FakeAlarms(access: .granted)
        let page = AlarmsPageModel(deps: fake.deps)
        page.merge([tea], now: now)

        let later = now.addingTimeInterval(10)
        #expect(page.setPaused(tea, true, now: later))
        #expect(fake.paused == [.init(id: id, resume: false)])
        #expect(page.entries?.first?.state == .paused && page.entries?.first?.clock == .paused(left: 262))

        page.merge([tea], now: later.addingTimeInterval(0.2)) // AlarmKit's list from before the tap
        #expect(page.entries?.first?.state == .paused)
        var agreeing = tea
        agreeing.state = .paused
        agreeing.clock = nil // its Live Activity not updated yet
        page.merge([agreeing], now: later.addingTimeInterval(0.4))
        #expect(page.entries?.first?.face == .still(262))

        let paused = page.entries![0]
        #expect(page.setPaused(paused, false, now: later.addingTimeInterval(30)))
        #expect(fake.paused.last == .init(id: id, resume: true))
        #expect(page.entries?.first?.face == .counting(ends: later.addingTimeInterval(30 + 262)))
        // AlarmKit still says paused well after the tap: its word wins.
        page.merge([agreeing], now: later.addingTimeInterval(30 + AlarmsPageModel.settle + 1))
        #expect(page.entries?.first?.state == .paused)
    }
}

/// AlarmKit as the page sees it, with made-up answers.
@MainActor
private final class FakeAlarms {
    struct Pause: Equatable { var id: UUID; var resume: Bool }

    var access: PhoneAuthStatus
    let afterRequest: PhoneAuthStatus
    let lists: [[AlarmEntry]]
    var requests = 0
    var listened = 0
    var cancelled: [UUID] = []
    var paused: [Pause] = []
    var refuse = false

    init(access: PhoneAuthStatus, afterRequest: PhoneAuthStatus? = nil, lists: [[AlarmEntry]] = []) {
        self.access = access
        self.afterRequest = afterRequest ?? access
        self.lists = lists
    }

    var deps: AlarmsPageModel.Deps {
        AlarmsPageModel.Deps(
            access: { self.access },
            request: {
                self.requests += 1
                self.access = self.afterRequest
                return self.access
            },
            entries: {
                self.listened += 1
                let lists = self.lists
                return AsyncStream { c in
                    for list in lists { c.yield(list) }
                    c.finish()
                }
            },
            cancel: { id in
                if self.refuse { throw CancellationError() }
                self.cancelled.append(id)
            },
            pause: { id, resume in
                if self.refuse { throw CancellationError() }
                self.paused.append(.init(id: id, resume: resume))
            })
    }
}
