import SwiftUI

/// The watch face's complications: the Milk Frog (`PetComplication`, docs/apple/persona.md "The Milk Frog on the
/// watch"), Record and All-Day Recording. The watch widget extension compiles this file without Theme, as it
/// does `ExtensionSurfaceTokens`; the watch app's debug previews of the faces read it too.
nonisolated enum WatchFaceTokens {
    /// The rectangular complication's words, and how far they may shrink to fit two lines.
    static let title = Font.system(.headline)
    static let titleMinScale: CGFloat = 0.8
    /// When a stale snapshot's data is from, under the words.
    static let caption = Font.system(.caption)
    /// The disc drawn before the watch app has written a face (no frog chosen, or not fetched yet).
    static let placeholder = Color.gray.opacity(0.3)
    /// Behind the complication where the face shows a background (watchOS tints or drops it on others).
    static let background = Color.black

    /// Intake complications mirror Theme.Icon and share the app's intake palette, including in single-color faces.
    static let waterSymbol = "drop" // icon: Theme.Icon.water
    static let proteinSymbol = "fork.knife" // icon: Theme.Icon.protein
    static let intakeTitle = Font.system(.headline)
    static let intakeCaption = Font.system(.caption2)
    static let intakeSpacing: CGFloat = 2
    static let intakeMinimumScale: CGFloat = 0.4
    static let intakeLargestTypeSize = DynamicTypeSize.xxxLarge
    static func intakeSymbol(_ name: String) -> Image { Image(systemName: name) }

    /// The Record complication (`MeetingRecordingComplication`, docs/apple/recording.md「Apple Watch」): the system's red
    /// of a recording, its symbol (`Theme.Icon.recording`, which the extension doesn't build) and the circular face's
    /// glyph size.
    static let recording = Color.red
    static let recordingSymbol = "waveform.circle.fill"
    static let recordingGlyph = Font.system(size: 26, weight: .semibold)

    /// The All-Day Recording complication (`AllDayRecordingComplication`, docs/assistant/day-recap.md): its mark per
    /// state, mirrored from `Theme.Icon`, which the extension doesn't build.
    static let allDaySymbol = "record.circle" // icon: the widget extension doesn't build Theme.Icon (Theme.Icon.allDay)
    static let allDayPausedSymbol = "pause.circle" // icon: the widget extension doesn't build Theme.Icon (Theme.Icon.allDayPaused)
    static let allDayAlertSymbol = "exclamationmark.triangle" // icon: the widget extension doesn't build Theme.Icon (Theme.Icon.error)
    /// The circular face: the mark above one short line (the running time), which may shrink to fit the circle.
    static let allDayGlyph = Font.system(size: 20, weight: .semibold)
    static let allDayLine = Font.system(size: 11, weight: .semibold).monospacedDigit()
    static let allDayLineMinScale: CGFloat = 0.6
    /// Between the mark and the words on the rectangular face.
    static let allDaySpacing: CGFloat = 6
    /// Paused or stopped, waiting for a tap: `Theme.caution`'s dark value (the face is always dark).
    static let caution = Color.orange

    /// `-openWatchItem complication`: every family at a 46 mm face's sizes, on a mock face's grey.
    static let previewSpacing: CGFloat = 8
    static let previewCircular: CGFloat = 50
    static let previewCorner: CGFloat = 32
    static let previewRectangular = CGSize(width: 172, height: 76)
    static let previewRadius: CGFloat = 8
    static let previewWash = Color.white.opacity(0.08)
    static let previewInline = Font.system(.footnote)
}
