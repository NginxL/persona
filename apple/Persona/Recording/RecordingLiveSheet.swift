#if os(iOS)
import SwiftUI

// The live recording sheet (#4240, docs/design/proposals/meeting-recording.md section 4C and 4D): a sheet that opens by itself
// when a recording starts or is already running, survives being swiped away (recording goes on; the now-recording card is
// the way back), owns a navigation stack, and after Stop cross-fades from the live view to the wrap-up of that recording.

/// Asks the hosts' `.recordingLiveSheet()` to present again: a Live Activity tap, a notification or the drawer, while the
/// Recordings page is already on screen with the sheet swiped away, change the route without the page ever disappearing, so
/// nothing else would tell it. A counter, bumped by `MeetingRecorder.openRecordings` and by the now-recording card.
///
/// `holdOnAppear` is the other way round: the page is opened for something else (the minutes push's meeting,
/// `MeetingRecorder.openMeeting`, #3862 phase 6), which it pushes over itself, and the sheet presenting itself as the page
/// appears while a recording runs would cover it. The next appear within `MeetingPageOpener.stale` doesn't present it,
/// once; the now-recording card stays the way back. An `open` cancels the hold.
@MainActor @Observable final class RecordingLivePresenter {
    static let shared = RecordingLivePresenter()
    private(set) var serial = 0
    /// The recording whose End was tapped on the Lock Screen or in the Dynamic Island (#8731 V4), lowercase: its page asks
    /// "End the Meeting?" (or "Stop Recording?") once it shows, then `endAsked()` clears it.
    private(set) var endAsking: String?
    @ObservationIgnored private var heldAt: Date?

    func open(askingEnd: String? = nil) {
        heldAt = nil
        endAsking = askingEnd
        serial += 1
    }

    /// The page asked (or the recording it named isn't the one running).
    func endAsked() { endAsking = nil }

    func holdOnAppear(now: Date = .now) { heldAt = now }

    /// Whether a host appearing now presents the sheet for a running recording: false once after `holdOnAppear`, soon after.
    func presentsOnAppear(now: Date = .now) -> Bool {
        guard let heldAt else { return true }
        self.heldAt = nil
        return now.timeIntervalSince(heldAt) > MeetingPageOpener.stale
    }
}

extension View {
    /// Presents the live recording sheet from this page: when a recording begins, when this page appears while one runs,
    /// and whenever `RecordingLivePresenter` is asked. Put it on the whole list of each host (Knowledge's and the Recordings
    /// page), not on `RecordingsSection`, which Knowledge removes while it changes kind and recording goes on.
    func recordingLiveSheet() -> some View { modifier(RecordingLiveSheetPresenter()) }
}

private struct RecordingLiveSheetPresenter: ViewModifier {
    @State private var capture = RecordingCapture.shared
    @State private var presenter = RecordingLivePresenter.shared
    @State private var presented = false
    @State private var visible = false

    private var capturing: Bool { capture.state == .recording || capture.state == .paused }

    func body(content: Content) -> some View {
        content
            // The meeting room is the whole screen (#8731 v2): a meeting app's, not a sheet over the page.
            .fullScreenCover(isPresented: $presented) { RecordingLiveSheet() }
            // A hidden shell page never gets onDisappear: the tab-aware pair keeps one host presenting at a time.
            .onTabVisibility(appear: {
                visible = true
                // Asked whatever runs: a hold left by an appear with nothing recording would hide a later one.
                let presents = presenter.presentsOnAppear()
                if capturing, presents { presented = true }
            }, disappear: { visible = false })
            // The observed change, not "tapped here": the drawer, Siri and the quick action open the page before the
            // calendar read and the microphone, so it is already on screen and idle when recording begins.
            .onChange(of: capture.state) { old, new in
                if visible, new == .recording, old != .paused { presented = true }
            }
            .onChange(of: presenter.serial) {
                if visible, capturing { presented = true }
            }
    }
}

/// The sheet: the live view while recording, the wrap-up once it stopped. It remembers the recording's id while capture
/// runs, and treats the observed change to idle as the stop, however it came (the button, low storage, a failed write, the
/// audio service reset): `RecordingCapture.stop` clears `recordingID` at once and `MeetingAsk.ended` clears the exchanges,
/// so the exchanges are mirrored while capture runs and the live view fades out from the mirror. A room meeting (#3862)
/// shows the meeting page in both places instead (`MeetingPage`, live then after), so its page goes on as the thread.
struct RecordingLiveSheet: View {
    @Environment(\.dismiss) private var dismiss
    @Environment(\.accessibilityReduceMotion) private var reduceMotion
    @State private var capture = RecordingCapture.shared
    @State private var ask = MeetingAsk.shared
    @State private var library = RecordingLibrary.shared
    /// The recording the sheet is about: live, then wrap-up.
    @State private var held: UUID?
    @State private var wrapUp = false
    @State private var exchanges: [MeetingAsk.Exchange] = []
    /// `RecordingCapture.error` as last seen while capture ran, and the part of it a stop caused (see `RecordingSheetMove`).
    @State private var liveError: String?
    @State private var stopError: String?

    var body: some View {
        NavigationStack {
            ZStack {
                if wrapUp, let held {
                    if roomMeeting(held) {
                        // After the meeting (#8731 V3) the page has the bar: its ✕ at the leading corner, its ⋯ (#9653).
                        MeetingPage(id: held, mode: .after, captureError: stopError, close: { dismiss() })
                            .transition(swap(arriving: true))
                    } else {
                        RecordingWrapUp(id: held, captureError: stopError, finish: { dismiss() })
                            .toolbar(.hidden, for: .navigationBar)
                            .transition(swap(arriving: true))
                    }
                } else if let held {
                    if roomMeeting(held) {
                        MeetingPage(id: held, mode: .live, exchanges: exchanges, close: { dismiss() })
                            .toolbar(.hidden, for: .navigationBar)
                            .transition(swap(arriving: false))
                    } else {
                        RecordingLiveView(id: held, exchanges: exchanges, hide: { dismiss() })
                            .toolbar(.hidden, for: .navigationBar)
                            .transition(swap(arriving: false))
                    }
                }
            }
            .themedGroupedPage()
            .navigationBarTitleDisplayMode(.inline)
        }
        // Dark while the meeting records (E1: as meeting apps are), the app's own look once it ended.
        .preferredColorScheme(wrapUp ? nil : .dark)
        .onChange(of: capture.state, initial: true) { old, new in follow(from: old, to: new) }
        .onChange(of: capture.error, initial: true) { if capture.state != .idle { liveError = capture.error } }
        .onChange(of: ask.exchanges) { mirror() }
        // A replayed tape's meeting (#10589) leaves the recordings once its page after it closed (not when the room is
        // swiped away while it plays).
        .onDisappear {
            if let held, ReplayedMeeting.holds(held), capture.state == .idle { MeetingReplay.shared.clearEnded() }
        }
    }

    /// A meeting recorded here as a room meeting: the one kind whose sheet is the meeting page. Read from the recording
    /// (not kept when it is held): its attendance is written just after capture begins.
    private func roomMeeting(_ id: UUID) -> Bool { library.entry(id)?.roomMeeting == true }

    /// The live view leaves with `conceal` and the wrap-up arrives with `reveal`: leaving is quicker than arriving.
    private func swap(arriving: Bool) -> AnyTransition {
        reduceMotion ? .identity : .opacity.animation(arriving ? Theme.Motion.reveal : Theme.Motion.conceal)
    }

    private func mirror() {
        // After the stop the exchanges are gone from `ask`: what the live view had stays for its fade.
        guard capture.state != .idle, let held else { return }
        exchanges = ask.exchanges(for: held)
    }

    private func follow(from old: RecordingCapture.State, to new: RecordingCapture.State) {
        switch RecordingSheetMove.move(from: old.phaseCapture, to: new.phaseCapture, recordingID: capture.recordingID, held: held) {
        case .hold(let id, let announce):
            held = id
            wrapUp = false
            stopError = nil
            exchanges = ask.exchanges(for: id)
            if announce { AccessibilityNotification.Announcement(String(localized: "Recording started")).post() }
        case .wrapUp:
            // Stop is confirmed (or the recording ended by itself): success once capture has ended, as the wrap-up appears.
            stopError = RecordingSheetMove.stopError(now: capture.error, whileLive: liveError)
            if stopError == nil { Haptics.success() }
            AccessibilityNotification.Announcement(String(localized: "Recording stopped")).post()
            withAnimation(reduceMotion ? nil : Theme.Motion.reveal) { wrapUp = true }
        case .dismiss:
            dismiss()
        case .none:
            break
        }
    }
}

extension RecordingCapture.State {
    var phaseCapture: RecordingPhase.Capture {
        switch self {
        case .idle: .idle
        case .starting: .starting
        case .recording: .recording
        case .paused: .paused
        }
    }
}

/// The way out of a recording page, first in its header as on every presented page (#9653): ✕ where the page is done
/// (the wrap-up, a photo still loading), ⌄ where the recording goes on without it (Hide, as the room's own). A round chip a
/// finger wide, its mark in the primary ink.
struct RecordingCornerButton: View {
    enum Kind { case close, hide }
    var kind: Kind
    var identifier: String
    var action: () -> Void

    var body: some View {
        Button(action: action) {
            ThemedIcon(kind == .close ? Theme.Icon.close : Theme.Icon.minimizeRoom).font(Theme.Font.heading).sheetBarLabel()
                .scaledFrame(width: Theme.Size.tapTarget, height: Theme.Size.tapTarget)
                .background(Theme.Fill.chip, in: .circle)
        }
        .buttonStyle(.plain)
        .accessibilityLabel(kind == .close ? Text("Close") : Text("Hide"))
        .accessibilityIdentifier(identifier)
    }
}

// MARK: The clock

/// The recording's time. The system draws and ticks it (`Text(timerInterval:)`), so the page does not draw again four times
/// a second; paused, the same text is held at the time it stopped at. Reads `RecordingCapture.timeline`, which is not observed.
struct RecordingClock: View {
    var running: Bool
    var font: Font
    @State private var capture = RecordingCapture.shared
    /// When the recording would have started to show the time it shows now (moves forward over a pause).
    @State private var start = Date.now
    @State private var frozenAt: Date?

    var body: some View {
        Text(timerInterval: start...Date.distantFuture, pauseTime: running ? nil : frozenAt, countsDown: false, showsHours: true)
            .font(font)
            .monospacedDigit()
            .lineLimit(1)
            .onChange(of: running, initial: true) {
                // After the stop `timeline` is back at zero: the time the recording ended at stays on screen while the
                // view fades out, held where it was.
                if capture.state == .idle { frozenAt = frozenAt ?? Date.now; return }
                start = Date.now.addingTimeInterval(-capture.timeline)
                frozenAt = running ? nil : Date.now
            }
    }

    /// "2 minutes, 5 seconds": the spoken form of a time, for VoiceOver.
    static func spoken(_ seconds: Double) -> String {
        guard seconds.isFinite, seconds >= 0 else { return "" }
        return Duration.seconds(Int(seconds)).formatted(.units(allowed: [.hours, .minutes, .seconds], width: .wide))
    }
}

/// A motion the page makes only when Reduce Motion is off (tokens do not gate themselves, DESIGN_SYSTEM 2.7).
extension View {
    func recordingMotion<V: Equatable>(_ animation: Animation = Theme.Motion.state, reduced: Bool, value: V) -> some View {
        self.animation(reduced ? nil : animation, value: value)
    }
}
#endif
