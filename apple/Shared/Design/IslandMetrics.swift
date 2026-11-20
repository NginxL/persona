import SwiftUI

/// The Dynamic Island's sizes (docs/apple/live-activity.md): the Fleet activity's pieces and the picture of it in Settings › Notifications ›
/// Dynamic Island. A table of its own because the widget extension builds this file but not `Theme`. The island's type has fixed
/// sizes: the system gives its regions in points, whatever the text size.
nonisolated enum IslandMetrics {
    // MARK: What the system gives (measured on the iOS 27 simulators, docs/apple/live-activity.md「Sharing the Dynamic Island with other apps」)

    /// A compact side limited in width (`isDynamicIslandLimitedInWidth`: another app's activity beside ours on iPhone 18
    /// Pro / Pro Max, landscape, iPhone Duo), and the minimal island in landscape: a 28 pt square, drawn as a circle.
    static let limited: CGFloat = 28
    /// What the glyph may take of it: the widest a line of the glyph's height can be inside the 28 pt circle.
    static let limitedContent: CGFloat = 24
    /// The minimal island (another app's activity beside ours on the other iPhones): a circle that grows into an oval
    /// with what it shows, up to this wide, of which this much shows (the rest is the system's margins; wider is cut).
    static let minimal: CGFloat = 45
    static let minimalContent: CGFloat = 35
    /// A compact side when the activity is alone: no width is offered, the island grows with what it shows, up to about
    /// this much on iPhone 17 / 17 Pro (more on iPhone 18 Pro, whose camera is narrower); wider is cut.
    static let compactContent: CGFloat = 94

    // MARK: Pieces

    /// A compact side's symbol and number.
    static let symbol: CGFloat = 13
    static let number: CGFloat = 15
    /// The minimal island's (and a limited side's) symbol and number.
    static let smallSymbol: CGFloat = 10
    static let smallNumber: CGFloat = 12
    /// Unread's dot, smaller than the other symbols.
    static let dot: CGFloat = 8
    static let smallDot: CGFloat = 6
    /// The assistant's star.
    static let star: CGFloat = 15
    static let smallStar: CGFloat = 12
    /// The timer's symbol and its digits, and the most the digits take (`Text(timerInterval:)` takes all it is given).
    static let timerSymbol: CGFloat = 11
    static let timerNumber: CGFloat = 13
    static let timerWidth: CGFloat = 40
    /// Between a symbol and its number.
    static let gap: CGFloat = 3
    static let smallGap: CGFloat = 1
    /// A compact side's inset from the island's rounded end.
    static let edge: CGFloat = 2
    /// The persona on the compact island's left side (docs/apple/live-activity.md「Avatar」), and the gap to its count.
    static let face: CGFloat = 22
    static let faceGap: CGFloat = 4
    /// The persona in the expanded island's corner and on the lock screen (a hub package's pictures are made at every
    /// face size, `FleetFaces.sizes`).
    static let expandedFace: CGFloat = 26
    static let lockFace: CGFloat = 26
    /// The ring around a count that has no room for its symbol.
    static let ring: CGFloat = 1.5
    /// A row's text after its status dot (7 pt and an 8 pt gap), on the expanded island and the lock screen.
    static let rowInset: CGFloat = 15
    /// A background workflow's bar (#3437), on the row's second line under its clock: its length and thickness.
    static let bar: CGFloat = 48
    static let barHeight: CGFloat = 4
    /// Between the expanded island's rows, and under them the "+2"; packed when two rows of two lines (background work,
    /// requests) and the "+2" would pass the island's about 160 pt: at the usual gap the "+2" was cut in half (#3437).
    static let rowGap: CGFloat = 8
    static let packedRowGap: CGFloat = 4
    /// Above the expanded island's rows, below its corners.
    static let rowsTop: CGFloat = 4

    // MARK: The recording activity (docs/apple/live-activity.md「Recording」)

    /// The compact island's time: room for "1:23:45" at `timerNumber` (a two-hour meeting), no more.
    static let recordingTimerWidth: CGFloat = 54
    /// The expanded island's and the Lock Screen's time, at `.title3`: "1:23:45" whole. Fixed, as `Text(timerInterval:)`
    /// takes all the width it is offered.
    static let recordingExpandedTimerWidth: CGFloat = 92
    /// Between the Lock Screen's and the expanded island's buttons.
    static let buttonGap: CGFloat = 10
    /// The meeting's End (#8731 V4): a link that opens the meeting and asks first, drawn as the capsule the other buttons
    /// are (the system's bordered capsule at the regular size).
    static let recordingButtonHeight: CGFloat = 34
    /// The box around Persona's answer (#8731 V4): its inset and its corners.
    static let recordingAnswerInset: CGFloat = 6
    static let recordingAnswerRadius: CGFloat = 10
    /// The expanded island's button row's inset from its sides (#5819): the row sits at the island's bottom, where the
    /// corners round in, and with Ask Persona taking the width the Stop capsule ran into the right corner and was cut.
    static let recordingIslandButtonsInset: CGFloat = 12
    /// The Lock Screen's inset from the activity's edges.
    static let lockPadding: CGFloat = 16

    // MARK: Alarms and timers (docs/apple/live-activity.md「Alarms and timers (AlarmKit)」)

    /// The compact island's countdown: room for "1:23:45" at `timerNumber`, no more.
    static let alarmTimerWidth: CGFloat = 54
    /// The expanded island's countdown, at `.title3`: "1:23:45" whole.
    static let alarmExpandedTimerWidth: CGFloat = 92
    /// The Lock Screen's countdown, at `.title2` (capped at the default text size there): "1:23:45" whole, with margin.
    /// Fixed, as `Text(timerInterval:)` takes all the width it is offered.
    static let alarmLockTimerWidth: CGFloat = 100
    /// The expanded island's button row's inset from its sides, so the capsules clear the rounded bottom corners.
    static let alarmIslandButtonsInset: CGFloat = 12

    // MARK: The settings page's picture of the island

    /// The pill, as wide as the island with both sides showing.
    static let previewWidth: CGFloat = 230
    static let previewHeight: CGFloat = 36
    /// Where the camera sits.
    static let previewCamera = CGSize(width: 70, height: 22)
    /// The sides' inset from the pill's ends.
    static let previewInset: CGFloat = 14
    /// The island when sharing: the pill holds only the camera and our glyph, another app's activity is a circle.
    static let previewSharedWidth: CGFloat = 140
    static let previewGap: CGFloat = 6
    /// The other app's activity in the picture: a gray stand-in for whatever it shows.
    static let previewOther: CGFloat = 14

    static func numberFont(small: Bool) -> Font {
        .system(size: small ? smallNumber : number, weight: .bold, design: .rounded)
    }

    static func symbolFont(small: Bool) -> Font {
        .system(size: small ? smallSymbol : symbol, weight: .bold)
    }
}
