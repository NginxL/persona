import Foundation
import Observation

/// Saves transcripts segment by segment: after the app is killed, the next run picks up at the unfinished segment.
@Observable
final class RecordingTranscription {
    static let shared = RecordingTranscription()
    private(set) var current: UUID?
    private(set) var errors: [UUID: String] = [:]
    /// The wall-clock range each rolling recording was pulled for; only that part is transcribed.
    private(set) var pulls: [UUID: ClosedRange<Date>] = [:]
    @ObservationIgnored private let library: RecordingLibrary
    @ObservationIgnored private let transcribe: @Sendable (URL, Locale) async throws -> [RecordingSentence]
    @ObservationIgnored private var worker: Task<Void, Never>?
    @ObservationIgnored private var queued: [UUID] = []
    /// `whenIdle` callers waiting for the worker to end, each resumed once: then, or when its own task is cancelled.
    @ObservationIgnored private var idleWaiters: [UUID: CheckedContinuation<Void, Never>] = [:]

    init(library: RecordingLibrary = .shared,
         transcribe: @escaping @Sendable (URL, Locale) async throws -> [RecordingSentence] = RecordingTranscription.onDevice) {
        self.library = library; self.transcribe = transcribe
    }

    nonisolated static func onDevice(_ url: URL, _ locale: Locale) async throws -> [RecordingSentence] {
        guard #available(iOS 26.0, *) else { throw RecordingSpeechError.needsNewerSystem }
        guard let (engine, resolved) = await RecordingAnalyzerEngine.best(for: locale) else {
            throw RecordingSpeechError.unsupportedLanguage
        }
        return try await engine.transcribe(url, locale: resolved)
    }

    func enqueue(_ id: UUID) {
        // An all-day recording's chunks live in its day store and have their own lane (`RecordingDayTranscriber`).
        guard let entry = library.entry(id), !entry.isDay else { return }
        errors[id] = nil
        if !queued.contains(id) { queued.append(id) }
        kick()
    }

    func pull(_ id: UUID, minutes: Int, now: Date = .now) {
        pull(id, range: now.addingTimeInterval(-Double(minutes) * 60)...now)
    }

    func pull(_ id: UUID, range: ClosedRange<Date>) {
        guard let entry = library.entry(id), entry.isRolling else { return }
        pulls[id] = range
        enqueue(id)
    }

    func resumePending() {
        // Rolling recordings only transcribe after an explicit pull; all-day recordings have their own lane.
        for entry in library.entries where !entry.active && !entry.isRolling && !entry.isDay
            && entry.segments.contains(where: { $0.sentences == nil }) {
            enqueue(entry.id)
        }
    }

    func cancel(_ id: UUID) {
        queued.removeAll { $0 == id }
        if current == id { worker?.cancel() }
    }

    func setLocale(_ id: UUID, _ locale: String) {
        if library.entry(id)?.isDay == true {
            RecordingDayTranscriber.shared.setLocale(id, locale)
            return
        }
        guard current != id else { return }
        library.change(id) { entry in
            entry.locale = locale
            for index in entry.segments.indices { entry.segments[index].sentences = nil }
        }
        enqueue(id)
    }

    private func kick() {
        guard worker == nil else { return }
        worker = Task { [weak self] in
            guard let self else { return }
            while !Task.isCancelled, !self.queued.isEmpty {
                let id = self.queued.removeFirst()
                self.current = id
                await self.process(id)
                self.current = nil
            }
            self.worker = nil
            self.kickIfNeeded()
            if self.worker == nil { self.resumeIdleWaiters() }
        }
    }

    private func kickIfNeeded() { if !queued.isEmpty { kick() } }

    /// Returns once nothing is being transcribed or queued: all-day recording's lane runs only then, so a recording the
    /// user is waiting for never waits behind a day's backlog. Also returns as soon as the caller's task is cancelled,
    /// clips still being transcribed (awaiting the worker itself would not): the catch-up task whose time ran out stops
    /// at once, whatever a clip's transcription or model download still takes.
    func whenIdle() async {
        while worker != nil, !Task.isCancelled {
            let key = UUID()
            await withTaskCancellationHandler {
                await withCheckedContinuation { (continuation: CheckedContinuation<Void, Never>) in
                    if worker == nil || Task.isCancelled {
                        continuation.resume()
                    } else {
                        idleWaiters[key] = continuation
                    }
                }
            } onCancel: { @Sendable in
                Task { @MainActor in self.resumeIdleWaiter(key) }
            }
        }
    }

    private func resumeIdleWaiter(_ key: UUID) {
        idleWaiters.removeValue(forKey: key)?.resume()
    }

    private func resumeIdleWaiters() {
        let waiting = idleWaiters
        idleWaiters = [:]
        for continuation in waiting.values { continuation.resume() }
    }

    /// A rolling recording only transcribes the segments of its pulled range; an all-day recording none (its own lane).
    private func nextSegment(_ entry: RecordingEntry) -> RecordingSegment? {
        guard !entry.isDay else { return nil }
        guard entry.isRolling else { return entry.segments.first(where: { $0.sentences == nil }) }
        guard let range = pulls[entry.id] else { return nil }
        return entry.segments(from: range.lowerBound, to: range.upperBound).first(where: { $0.sentences == nil })
    }

    private func process(_ id: UUID) async {
        while !Task.isCancelled, let entry = library.entry(id),
              let segment = nextSegment(entry) {
            // A segment whose audio is gone (a file the system removed, a broken transfer) has no text to give: it must
            // not hold up the rest of a long recording, as it did when every later segment waited behind it.
            if !FileManager.default.fileExists(atPath: library.audio(id, segment.index).path) {
                library.change(id) { e in
                    if let i = e.segments.firstIndex(where: { $0.index == segment.index }) { e.segments[i].sentences = [] }
                }
                // Not saved (no space, deleted meanwhile): stop here rather than try the same segment for ever.
                guard library.entry(id)?.segments.first(where: { $0.index == segment.index })?.sentences != nil else {
                    errors[id] = library.error ?? String(localized: "The recording info wasn't saved. Keep the app installed and try again.")
                    return
                }
                continue
            }
            do {
                let raw = try await transcribe(library.audio(id, segment.index), Locale(identifier: entry.locale))
                try Task.checkCancellation()
                guard let latest = library.entry(id), latest.locale == entry.locale else { return }
                var saved = latest
                guard let index = saved.segments.firstIndex(where: { $0.index == segment.index }) else { return }
                saved.segments[index].sentences = raw.map {
                    RecordingSentence(start: segment.start + $0.start, end: segment.start + $0.end, text: $0.text)
                }
                try library.save(saved)
            } catch is CancellationError { return }
            catch {
                // A hub-transcribed segment (#9491): the hub's final pass transcribes its own copy of the audio, so the
                // phone's failure (no model for the language, a format it can't read) leaves the segment with no text of
                // its own rather than holding the recording's `complete: true`, and with it the minutes, for 12 hours.
                if Self.hubTranscribes(segment, of: entry) {
                    library.change(id) { e in
                        if let i = e.segments.firstIndex(where: { $0.index == segment.index }) { e.segments[i].sentences = [] }
                    }
                    if library.entry(id)?.segments.first(where: { $0.index == segment.index })?.sentences != nil { continue }
                }
                errors[id] = error.localizedDescription
                return
            }
        }
    }

    /// Whether the hub transcribes `segment`'s audio (#9491, pure, for the tests): a meeting that asked for hub
    /// transcription and was never rolled back, whose hub-mode time (`RecordingAttendance.hubStretches`: none recorded as
    /// a Note, none after hub transcription stopped) holds the whole segment. Only such a segment may go without the
    /// phone's text; a device-mode meeting needs it for every segment.
    nonisolated static func hubTranscribes(_ segment: RecordingSegment, of entry: RecordingEntry) -> Bool {
        guard let attendance = entry.attendance, attendance.transcription == .hub, !attendance.hubRolledBack else { return false }
        let end = segment.start + segment.duration
        // Stretches that meet (a Note turned back into a meeting at once) cover what they hold together.
        var covered: [(from: Double, to: Double)] = []
        for s in attendance.hubStretches(until: max(entry.duration, end)).sorted(by: { $0.from < $1.from }) {
            if let last = covered.last, s.from <= last.to + slack {
                covered[covered.count - 1].to = max(last.to, s.to)
            } else {
                covered.append(s)
            }
        }
        return covered.contains { $0.from <= segment.start + slack && end <= $0.to + slack }
    }

    /// How far apart two times may be and still meet (a segment's edge against a stretch's): a sample's rounding.
    nonisolated static let slack = 0.01
}

extension RecordingEntry {
    /// Transcribed sentences of a pulled range with their wall-clock times, for the preview.
    func pulledSentences(from: Date, to: Date) -> [(Date, String)] {
        segments(from: from, to: to).flatMap { (segment: RecordingSegment) -> [(Date, String)] in
            (segment.sentences ?? []).compactMap { sentence -> (Date, String)? in
                segment.wallTime(sentence.start).map { ($0, sentence.text) }
            }
        }.sorted { $0.0 < $1.0 }
    }

    /// Where transcribing a pulled range stands, for the preview. A failure wins over the rest so it can be retried.
    func pullState(from: Date, to: Date, transcribing: Bool, error: String?) -> RecordingPullState {
        if transcribing { return .transcribing }
        if let error, !error.isEmpty { return .failed(error) }
        let picked = segments(from: from, to: to)
        if picked.isEmpty { return .noAudio }
        if picked.contains(where: { $0.sentences == nil }) { return .waiting }
        return pulledSentences(from: from, to: to).isEmpty ? .silent : .ready
    }
}

nonisolated enum RecordingPullState: Equatable, Sendable {
    case transcribing, waiting, noAudio, silent, ready
    case failed(String)

    /// What the preview says in place of (or next to) the text; nil once there is text.
    var message: String? {
        switch self {
        case .transcribing: String(localized: "Transcribing…")
        case .waiting: String(localized: "Waiting for on-device transcription")
        case .noAudio: String(localized: "No audio was recorded in this range.")
        case .silent: String(localized: "No speech was recognized in this range.")
        case .ready: nil
        case .failed(let error): String(localized: "Transcription didn't finish: \(error)")
        }
    }
}
