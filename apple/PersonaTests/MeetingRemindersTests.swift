import Foundation
import Testing
import UserNotifications
@testable import Persona

/// The calendar meeting reminder (#8731 G2): which meetings get one, what it says, and the way back from its notification.
struct MeetingRemindersTests {
    private let now = Date(timeIntervalSince1970: 1_800_000_000)

    private func event(_ id: String, in minutes: Double, title: String = "Weekly", allDay: Bool = false,
                       declined: Bool = false) -> MeetingCalendar.Candidate {
        let start = now.addingTimeInterval(minutes * 60)
        return MeetingCalendar.Candidate(id: id, title: title, start: start, end: start.addingTimeInterval(1800), allDay: allDay,
                                         declined: declined, location: "Room 3", attendees: ["Ada", "Bo"])
    }

    /// The Meetings page's meetings whose reminder is still ahead and that start within the horizon, earliest first; not
    /// the one recording already.
    @Test func theMeetingsAheadGetOneEach() {
        let events = [
            event("later", in: 180),
            event("soon", in: 10),
            event("now", in: 0.5, title: "Too late: its minute has begun"),
            event("allday", in: 30, allDay: true),
            event("declined", in: 40, declined: true),
            event("untitled", in: 50, title: "  "),
            event("far", in: MeetingReminders.horizon / 60 + 5),
            event("recording", in: 20),
        ]
        let plan = MeetingReminders.plan(events, now: now, recording: "recording")
        #expect(plan.map(\.meeting.eventID) == ["soon", "later"])
        #expect(plan.first?.fire == now.addingTimeInterval(10 * 60 - MeetingReminders.lead))
        #expect(plan.first?.meeting.attendees == ["Ada", "Bo"] && plan.first?.meeting.location == "Room 3")
    }

    /// One per occurrence (a repeating meeting's id is the same each day), each asked for once, at most `limit`.
    @Test func eachOccurrenceOnceAndNoMoreThanTheLimit() {
        let twice = MeetingReminders.plan([event("weekly", in: 10), event("weekly", in: 10), event("weekly", in: 24 * 60)],
                                          now: now, recording: nil)
        #expect(twice.count == 2 && Set(twice.map(\.id)).count == 2)
        #expect(twice.allSatisfy { $0.id.hasPrefix(MeetingReminders.idPrefix) })
        let many = (0..<40).map { event("m\($0)", in: Double(10 + $0 * 5)) }
        #expect(MeetingReminders.plan(many, now: now, recording: nil).count == MeetingReminders.limit)
    }

    /// The notification: the meeting's name, the start in the body, the reminder's category, and the meeting itself, which
    /// the answer reads back.
    @Test func theNotificationCarriesItsMeeting() throws {
        let reminder = try #require(MeetingReminders.plan([event("weekly", in: 10)], now: now, recording: nil).first)
        let content = MeetingReminders.content(reminder.meeting)
        #expect(content.title == "Weekly")
        #expect(content.body.contains(reminder.meeting.start!.formatted(date: .omitted, time: .shortened)))
        #expect(content.categoryIdentifier == MeetingReminders.category)
        #expect(MeetingReminders.meeting(content.userInfo) == reminder.meeting)
        #expect(MeetingReminders.meeting([:]) == nil && MeetingReminders.meeting([MeetingReminders.meetingKey: "x"]) == nil)
    }

    /// Start opens the app (the microphone starts in front).
    @Test func startOpensTheApp() throws {
        let category = MeetingReminders.notificationCategory
        let start = try #require(category.actions.first { $0.identifier == MeetingReminders.startAction })
        #expect(category.actions.count == 1 && start.options.contains(.foreground))
    }
}
