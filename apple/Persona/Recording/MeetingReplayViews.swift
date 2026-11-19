#if os(iOS)
import SwiftUI

/// What the meeting page offers for a recording (#10589). The replayed meeting (`ReplayedMeeting`) is on no hub, so it
/// offers nothing that would ask one: no composer, Talk or Just Me, no Bookmark, Photo, Speaker or Invite, no page menu
/// (Persona's role, Private Meeting, Meeting / Note, Rename), no Hand to Agent and no follow-up after it. It has the
/// replay's clock and speed instead, Pause and End.
nonisolated struct MeetingRoomOffers: Equatable, Sendable {
    /// The composer, Talk and the [+] menu's questions.
    var ask: Bool
    var justMe: Bool
    var bookmark: Bool
    var photos: Bool
    /// Speaker and Invite / Remove.
    var persona: Bool
    /// The live page's menu: Persona's role, Private Meeting, Meeting / Note, Rename.
    var pageMenu: Bool
    var privateMeeting: Bool
    var handOff: Bool
    /// The composer and voice after the meeting.
    var followup: Bool
    /// The replay's clock, state and speed in place of the recording's.
    var replayClock: Bool

    static let meeting = MeetingRoomOffers(ask: true, justMe: true, bookmark: true, photos: true, persona: true, pageMenu: true,
                                           privateMeeting: true, handOff: true, followup: true, replayClock: false)
    static let replay = MeetingRoomOffers(ask: false, justMe: false, bookmark: false, photos: false, persona: false, pageMenu: false,
                                          privateMeeting: false, handOff: false, followup: false, replayClock: true)

    static func of(_ recording: UUID) -> MeetingRoomOffers { ReplayedMeeting.holds(recording) ? .replay : .meeting }
}

/// The replayed meeting's line under its title (#10589): Play or Pause, where the tape is of its length ("1:23 / 3:46"),
/// Replaying or Paused (words, never the color alone), and the speed, a menu of the speeds. The time is read on its own
/// clock once a second, not observed (the tape moves ten times a second).
struct MeetingReplayStateLine: View {
    var paused: Bool
    @State private var replay = MeetingReplay.shared

    var body: some View {
        HStack(spacing: Theme.Space.s) {
            ThemedIcon(paused ? Theme.Icon.pause : Theme.Icon.run)
                .font(Theme.Font.caption)
                .foregroundStyle(paused ? Color.secondary : Theme.accent)
            TimelineView(.periodic(from: .now, by: 1)) { _ in
                Text(verbatim: Self.clock(replay.position, of: replay.length))
                    .font(Theme.Font.bodyStrong).monospacedDigit().lineLimit(1)
                    .foregroundStyle(paused ? Color.secondary : Color.primary)
                    .accessibilityIdentifier("meeting-replay-clock")
            }
            Text(paused ? String(localized: "Paused") : String(localized: "Replaying"))
                .font(Theme.Font.secondaryStrong)
                .foregroundStyle(paused ? Color.secondary : Theme.accent)
                .lineLimit(1)
                .accessibilityIdentifier("meeting-replay-state")
            Menu {
                Picker(selection: Binding(get: { replay.speed }, set: { replay.setSpeed($0) })) {
                    ForEach(MeetingReplay.speeds, id: \.self) { Text(MeetingTapesPage.speedLabel($0)).tag($0) }
                } label: {
                    Text("Speed")
                }
            } label: {
                Chip(text: MeetingTapesPage.speedLabel(replay.speed), symbol: Theme.Icon.choices)
            }
            .accessibilityLabel(Text("Speed"))
            .accessibilityValue(Text(verbatim: MeetingTapesPage.speedLabel(replay.speed)))
            .accessibilityIdentifier("meeting-replay-speed")
        }
    }

    /// "1:23 / 3:46" (hours when the tape is that long).
    static func clock(_ position: Double, of length: Double) -> String {
        func text(_ seconds: Double) -> String {
            let s = max(0, Int(seconds.isFinite ? seconds : 0))
            return length >= 3600 ? String(format: "%d:%02d:%02d", s / 3600, s / 60 % 60, s % 60) : String(format: "%d:%02d", s / 60, s % 60)
        }
        return "\(text(min(position, length))) / \(text(length))"
    }
}
#endif
