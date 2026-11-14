#if os(iOS)
import SwiftUI

/// What the live sheet shows after Stop (docs/design/proposals/meeting-recording.md section 4D): where the recording is on
/// its way to the minutes, as steps worked out from state the phone already has (`RecordingPhase`), with the way to open
/// the result and Done, which closes the sheet while everything goes on in the background. It holds only the recording's
/// id, owns its own minutes model and asks the hub for them every 15 seconds while they are on their way.
struct RecordingWrapUp: View {
    var id: UUID
    /// What ended the recording badly, when its stop caused it (`RecordingSheetMove.stopError`); nil: it was saved fine.
    var captureError: String?
    var finish: () -> Void

    @Environment(AppModel.self) private var model
    @Environment(\.dynamicTypeSize) private var size
    @Environment(\.accessibilityReduceMotion) private var reduceMotion
    @State private var library = RecordingLibrary.shared
    @State private var transcription = RecordingTranscription.shared
    @State private var sync = RecordingSync.shared
    @State private var capture = RecordingCapture.shared
    @State private var minutes = RecordingMinutesModel()
    /// The first answer about the minutes is in: until then a synced meeting counts as writing them.
    @State private var minutesAsked = false
    @State private var opening = false

    var body: some View {
        if let entry = library.entry(id) {
            let phase = self.phase(entry)
            ScrollView {
                VStack(alignment: .leading, spacing: Theme.Space.l) {
                    header(entry, phase: phase)
                    if case .failed(let failure) = phase, failure.step == .saved {
                        NoticeBanner(text: failure.reason, tone: .danger)
                    }
                    steps(phase, entry: entry)
                    actions(phase, entry: entry)
                }
                .padding(.horizontal, Theme.Space.page)
                .padding(.top, Theme.Space.s)
                .padding(.bottom, Theme.Space.l)
                .frame(maxWidth: .infinity, alignment: .leading)
            }
            .safeAreaInset(edge: .top, spacing: Theme.Space.flush) {
                HStack {
                    Spacer(minLength: Theme.Space.flush)
                    RecordingSheetCloseButton(title: String(localized: "Done"), identifier: "recording-wrapup-done") { finish() }
                }
                .padding(.horizontal, Theme.Space.page)
            }
            .safeAreaInset(edge: .bottom, spacing: Theme.Space.flush) { buttons(phase, entry: entry) }
            .navigationDestination(isPresented: $opening) { RecordingDetailView(id: id) }
            .tabActiveTask(id: "\(entry.revision)|\(minutes.round)|\(entry.transcribedAll)") { await followMinutes() }
            .onChange(of: phase) { old, new in announce(from: old, to: new) }
        }
    }

    // MARK: The phase

    /// `RecordingPhase` from the recording's own state; `capture` is idle here (the wrap-up shows after capture ended), and
    /// what ended it badly (`captureError`) is the first thing the wrap-up says.
    private func phase(_ entry: RecordingEntry) -> RecordingPhase {
        let held = RecordingPhase.Held(
            isMeeting: entry.isMeeting, active: entry.active, receivedAll: entry.receivedAll,
            noAudio: !entry.active && entry.expectedSegments == 0, transcribedAll: entry.transcribedAll,
            transcribing: transcription.current == id, transcriptionError: transcription.errors[id],
            sync: Self.sync(sync.status(entry)), hubAvailable: model.api?.config.isDemo == false,
            minutes: minutesAsked ? Self.minutes(minutes.state) : nil,
            syncRejectedReason: String(localized: "Not accepted by Knowledge"),
            minutesFailedReason: String(localized: "The minutes couldn't be written."))
        return RecordingPhase.derive(.init(capture: capture.state == .idle ? .idle : .recording, captureError: captureError, held: held))
    }

    static func sync(_ status: RecordingSync.Status) -> RecordingPhase.Sync {
        switch status {
        case .waiting: .waiting
        case .pending: .pending
        case .uploading: .uploading
        case .synced: .synced
        case .rejected: .rejected
        case .expired: .expired
        }
    }

    static func minutes(_ state: RecordingMinutesModel.State) -> RecordingPhase.Minutes {
        switch state {
        case .none: .absent
        case .waitingForText: .waitingForText
        case .waitingForSync: .waitingForSync
        case .writing: .writing
        case .ready: .ready
        case .failed: .failed
        case .unsupported: .unsupported
        }
    }

    /// Asks the hub for the minutes while they are on their way, like the recording's page does.
    private func followMinutes() async {
        while !Task.isCancelled, let entry = library.entry(id) {
            await minutes.load(entry, synced: sync.status(entry) == .synced)
            minutesAsked = true
            guard minutes.waiting else { return }
            try? await Task.sleep(for: .seconds(15))
        }
    }

    private func announce(from old: RecordingPhase, to new: RecordingPhase) {
        switch new {
        case .done(let hasMinutes):
            AccessibilityNotification.Announcement(hasMinutes ? String(localized: "Minutes ready") : String(localized: "Recording saved")).post()
        case .failed:
            // After capture has ended, so a haptic cannot reach the audio.
            Haptics.error()
        default:
            break
        }
    }

    // MARK: Views

    /// The headline first: a large mark that is a spinner while the recording is on its way and a check once it is saved,
    /// the title, and how long it ran. The steps under it say where it is.
    private func header(_ entry: RecordingEntry, phase: RecordingPhase) -> some View {
        VStack(alignment: .leading, spacing: Theme.Space.s) {
            headline(phase)
                .scaledFrame(width: Theme.Size.avatar, height: Theme.Size.avatar)
            Text(entry.displayTitle).font(Theme.Font.display).accessibilityAddTraits(.isHeader)
                .lineLimit(3).minimumScaleFactor(0.7)
                .accessibilityIdentifier("recording-wrapup-title")
            Text("Recorded \(RecordingFormat.clock(entry.duration))").font(Theme.Font.secondary).foregroundStyle(.secondary)
        }
        .padding(.top, Theme.Space.l)
        .accessibilityElement(children: .contain)
    }

    @ViewBuilder private func headline(_ phase: RecordingPhase) -> some View {
        switch phase {
        case .done:
            ThemedIcon(Theme.Icon.successFilled).font(Theme.Font.display).foregroundStyle(Theme.positive)
        case .failed:
            ThemedIcon(Theme.Icon.errorFilled).font(Theme.Font.display).foregroundStyle(Theme.danger)
        default:
            ProgressView().controlSize(.large)
        }
    }

    private func steps(_ phase: RecordingPhase, entry: RecordingEntry) -> some View {
        VStack(alignment: .leading, spacing: Theme.Space.m) {
            ForEach(RecordingPhase.steps(for: phase, isMeeting: entry.isMeeting)) { step in
                stepRow(step)
            }
        }
        .recordingMotion(reduced: reduceMotion, value: phase)
        .accessibilityIdentifier("recording-wrapup-steps")
    }

    private func stepRow(_ step: RecordingPhase.Step) -> some View {
        HStack(alignment: .top, spacing: Theme.Space.m) {
            mark(step.state)
                .scaledFrame(width: Theme.Size.icon, height: Theme.Size.icon)
            VStack(alignment: .leading, spacing: Theme.Space.xxs) {
                Text(Self.title(step.kind, state: step.state)).font(Theme.Font.body)
                    .foregroundStyle(step.state == .waiting ? Color.secondary : Color.primary)
                switch step.state {
                case .waitingForHub:
                    Text("Waiting for the hub").font(Theme.Font.caption).foregroundStyle(.secondary)
                case .failed(let reason):
                    Text(reason).font(Theme.Font.caption).foregroundStyle(Theme.danger)
                        .fixedSize(horizontal: false, vertical: true)
                default:
                    EmptyView()
                }
            }
        }
        .accessibilityElement(children: .combine)
        .accessibilityValue(Self.spoken(step.state))
        .accessibilityIdentifier("recording-wrapup-step-\(step.kind.rawValue)")
    }

    /// A done step has a check, the current one a spinner, one held up for the hub a clock, a failed one a warning.
    @ViewBuilder private func mark(_ state: RecordingPhase.Step.State) -> some View {
        switch state {
        case .current:
            ProgressView()
        case .done:
            ThemedIcon(Theme.Icon.successFilled).foregroundStyle(Theme.positive)
                .contentTransition(reduceMotion ? .identity : .symbolEffect(.replace))
        case .waiting:
            ThemedIcon(Theme.Icon.unchecked).foregroundStyle(.tertiary)
        case .waitingForHub:
            ThemedIcon(Theme.Icon.time).foregroundStyle(.secondary)
        case .failed:
            ThemedIcon(Theme.Icon.errorFilled).foregroundStyle(Theme.danger)
        }
    }

    /// A step reads as an action while it runs and as a result once it is done.
    private static func title(_ kind: RecordingPhase.Step.Kind, state: RecordingPhase.Step.State) -> String {
        let done = state == .done
        switch kind {
        case .saved: return String(localized: "Saved")
        case .transcribed: return String(localized: "Transcribed")
        case .synced: return done ? String(localized: "Synced to Knowledge") : String(localized: "Syncing to Knowledge")
        case .writing: return done ? String(localized: "Minutes written") : String(localized: "Writing minutes")
        case .minutes: return String(localized: "Minutes ready")
        }
    }

    private static func spoken(_ state: RecordingPhase.Step.State) -> String {
        switch state {
        case .done: String(localized: "Done")
        case .current: String(localized: "In progress")
        case .waiting: String(localized: "Not started")
        case .waitingForHub: String(localized: "Waiting for the hub")
        case .failed(let reason): reason
        }
    }

    /// The one thing a failed step can do, where there is one: transcribing again, or asking for the minutes again.
    @ViewBuilder private func actions(_ phase: RecordingPhase, entry: RecordingEntry) -> some View {
        if case .failed(let failure) = phase, let action = failure.action {
            switch action {
            case .resumeTranscribing:
                Button { transcription.enqueue(id) } label: {
                    Label("Resume Transcribing", systemImage: Theme.Icon.retry).lineLimit(1)
                }
                .themedButton()
                .accessibilityIdentifier("recording-wrapup-resume")
            case .tryAgainMinutes:
                Button { Task { await minutes.regenerate(id) } } label: { Label("Retry", systemImage: Theme.Icon.retry).lineLimit(1) }
                    .themedButton()
                    .disabled(minutes.asking)
                    .accessibilityIdentifier("recording-wrapup-retry")
            }
        }
    }

    /// Open Minutes, or Open Recording: the one solid control, pinned under the steps. Done is at the top, where Hide is.
    private func buttons(_ phase: RecordingPhase, entry: RecordingEntry) -> some View {
        let hasMinutes: Bool = { if case .done(true) = phase { true } else { false } }()
        return Button { opening = true } label: {
            Text(hasMinutes ? String(localized: "Open Minutes") : String(localized: "Open Recording"))
                .font(Theme.Font.bodyStrong).lineLimit(1).minimumScaleFactor(0.7)
                .frame(maxWidth: .infinity)
                .scaledFrame(minHeight: Theme.Size.tapTarget)
        }
        .themedButton(prominent: true)
        .accessibilityIdentifier("recording-wrapup-open")
        .padding(.horizontal, Theme.Space.page)
        .padding(.vertical, Theme.Space.s)
        .frame(maxWidth: .infinity)
        .background { Theme.groupedPage.ignoresSafeArea(edges: .bottom) }
    }
}
#endif
