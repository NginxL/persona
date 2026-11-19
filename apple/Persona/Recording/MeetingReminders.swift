#if os(iOS)
import Foundation
import UIKit
import UserNotifications

// The calendar meeting reminder (#8731 G2, docs/apple/recording.md "Meeting recordings"): a minute before a meeting on the
// phone's calendar starts, a notification asks whether to record it: the meeting's name, "Starts at 10:00. Record it?"
// and Start, which starts the meeting named after the event and opens its room. A plain tap opens the Meetings page,
// whose Upcoming row has the same Start. On by default (Settings › Meetings); it needs calendar access, which the
// Meetings page asks for, and the notifications the app asks for anyway.

/// The reminders the app has asked the system for: the meetings of the next day, asked again whenever the app comes to
/// the front, the switch moves or a meeting starts (the one recording needs none). Local notifications fire with the app
/// closed, so a meeting moved in the calendar while the app stayed away keeps its old reminder until the app is opened.
@MainActor final class MeetingReminders {
    static let shared = MeetingReminders()

    /// Settings › Meetings › Remind Me Before Meetings, on until turned off.
    nonisolated static let enabledKey = "meeting.reminders"
    nonisolated static let category = "MEETING_REMINDER"
    nonisolated static let startAction = "MEETING_REMINDER_START"
    /// Where a reminder's notification carries its meeting (a `RecordingMeeting` as JSON).
    nonisolated static let meetingKey = "meeting_reminder"
    nonisolated static let idPrefix = "meeting-reminder-"
    /// How long before the start it comes, how far ahead the app asks, and for how many meetings at most (the system
    /// keeps 64 pending notifications for an app).
    nonisolated static let lead: TimeInterval = 60
    nonisolated static let horizon: TimeInterval = 36 * 3600
    nonisolated static let limit = 16

    nonisolated static var enabled: Bool { UserDefaults.standard.object(forKey: enabledKey) as? Bool ?? true }

    private var started = false
    private var refreshing: Task<Void, Never>?

    /// From `MeetingRecorder.start` at launch.
    func start() {
        guard !started else { return }
        started = true
        // Coming back to the front covers a calendar changed in another app, and the calendar prompt's answer.
        NotificationCenter.default.addObserver(forName: UIApplication.didBecomeActiveNotification, object: nil, queue: .main) { @Sendable _ in
            Task { @MainActor in MeetingReminders.shared.refresh() }
        }
        refresh()
    }

    /// Asks the system again for the reminders wanted now; the ones no longer wanted go (all of them, switched off).
    func refresh() {
        refreshing?.cancel()
        let recording = RecordingCapture.shared.recordingID.flatMap { RecordingLibrary.shared.entry($0)?.meeting?.eventID }
        refreshing = Task {
            let now = Date.now
            // EventKit's reads are synchronous calls to its daemon: off the main thread (P1).
            let events = Self.enabled ? await MeetingCalendar.eventsOffMain(from: now, to: now.addingTimeInterval(Self.horizon)) : []
            let wanted = Self.plan(events, now: now, recording: recording)
            guard !Task.isCancelled else { return }
            await Self.ask(wanted)
        }
    }

    /// One reminder: its notification's identifier, when it fires and the meeting it is for.
    nonisolated struct Reminder: Hashable, Sendable {
        var id: String
        var fire: Date
        var meeting: RecordingMeeting
    }

    /// Of `events`, the meetings to remind of at `now`: the ones the Meetings page lists (not all-day, declined or
    /// untitled) whose reminder is still ahead and that start within the horizon, not the one `recording` already,
    /// earliest first, at most `limit`.
    nonisolated static func plan(_ events: [MeetingCalendar.Candidate], now: Date, recording: String?) -> [Reminder] {
        var seen = Set<String>()
        let soon = events.filter { e in
            !e.allDay && !e.declined && !e.title.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty
                && e.start.addingTimeInterval(-lead) > now && e.start <= now.addingTimeInterval(horizon)
                && (recording == nil || e.id != recording)
        }
        .sorted { a, b in a.start == b.start ? a.end < b.end : a.start < b.start }
        let reminders = soon.compactMap { e -> Reminder? in
            let id = idPrefix + (e.id ?? e.title) + "-" + String(Int(e.start.timeIntervalSince1970))
            guard seen.insert(id).inserted else { return nil }
            return Reminder(id: id, fire: e.start.addingTimeInterval(-lead), meeting: RecordingMeeting(e))
        }
        return Array(reminders.prefix(limit))
    }

    /// What a reminder says: the meeting's name, when it starts and the question, with Start.
    nonisolated static func content(_ meeting: RecordingMeeting) -> UNMutableNotificationContent {
        let content = UNMutableNotificationContent()
        content.title = meeting.title ?? ""
        let time = (meeting.start ?? .now).formatted(date: .omitted, time: .shortened)
        content.body = String(localized: "Starts at \(time). Record it? [Meeting Reminder]")
        content.sound = .default
        content.categoryIdentifier = category
        content.threadIdentifier = category
        if let data = try? JSONEncoder().encode(meeting) { content.userInfo = [meetingKey: data] }
        return content
    }

    /// The meeting a notification reminds of; nil for any other notification.
    nonisolated static func meeting(_ userInfo: [AnyHashable: Any]) -> RecordingMeeting? {
        guard let data = userInfo[meetingKey] as? Data else { return nil }
        return try? JSONDecoder().decode(RecordingMeeting.self, from: data)
    }

    /// The reminder's notification category: Start opens the app, where the microphone starts.
    nonisolated static var notificationCategory: UNNotificationCategory {
        UNNotificationCategory(
            identifier: category,
            actions: [UNNotificationAction(identifier: startAction, title: String(localized: "Start [Meeting Reminder]"), options: [.foreground],
                                           icon: UNNotificationActionIcon(systemImageName: Theme.Icon.recording))],
            intentIdentifiers: [], options: [])
    }

    /// A reminder answered: Start starts the meeting named after the event and opens its room (with something recording
    /// already, the Meetings page opens and it goes on); a plain tap opens the Meetings page.
    static func respond(action: String, meeting: RecordingMeeting) {
        guard action != UNNotificationDismissActionIdentifier else { return }
        guard action == startAction else {
            MeetingRecorder.openRecordings()
            return
        }
        #if DEBUG
        // `-showMeetingReminder start`: a made-up meeting stands in for the microphone, as `-showMeetingSample` does.
        if UserDefaults.standard.string(forKey: "showMeetingReminder") != nil {
            RecordingSamples.startFromReminder(meeting)
            return
        }
        #endif
        Task { await MeetingRecorder.shared.startMeeting(inForeground: true, event: meeting) }
    }

    #if DEBUG
    /// `-showMeetingReminder start|open`: answers the made-up weekly meeting's reminder as its Start or a plain tap would,
    /// through the notification's own content (a UI test can't reach a notification without the system's prompt), once
    /// the hub has said what it offers (a meeting starts as a room meeting by it), or after five seconds.
    static func showIfAsked() {
        guard let mode = UserDefaults.standard.string(forKey: "showMeetingReminder"),
              let meeting = meeting(content(RecordingSamples.reminderMeeting()).userInfo) else { return }
        Task {
            for _ in 0..<20 where AppModel.shared.hubFeatures.isEmpty { try? await Task.sleep(for: .milliseconds(250)) }
            respond(action: mode == "start" ? startAction : UNNotificationDefaultActionIdentifier, meeting: meeting)
        }
    }
    #endif

    /// Removes the pending reminders not in `wanted` and asks for the ones in it (asking again replaces one).
    private nonisolated static func ask(_ wanted: [Reminder]) async {
        let center = UNUserNotificationCenter.current()
        let keep = Set(wanted.map(\.id))
        let stale = await center.pendingNotificationRequests().map(\.identifier).filter { $0.hasPrefix(idPrefix) && !keep.contains($0) }
        if !stale.isEmpty { center.removePendingNotificationRequests(withIdentifiers: stale) }
        for reminder in wanted {
            let when = Calendar.current.dateComponents([.year, .month, .day, .hour, .minute, .second], from: reminder.fire)
            let request = UNNotificationRequest(identifier: reminder.id, content: content(reminder.meeting),
                                                trigger: UNCalendarNotificationTrigger(dateMatching: when, repeats: false))
            try? await center.add(request)
        }
    }
}
#endif
