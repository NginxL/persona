import Foundation
import Observation
#if os(iOS) || os(visionOS)
import UIKit
#endif

// All-day recording's transcription lane (docs/assistant/day-recap.md): the chunks the watch sends, transcribed on the
// phone as they arrive, in runs of contiguous chunks so words aren't cut at a chunk's edge. Its own lane and its
// own `current`, apart from `RecordingTranscription`, which goes first: a recording the user is waiting for
// never waits behind a day's backlog.

// MARK: - Runs

/// Which chunks of a day to transcribe next, and how a chunk's text is taken back to transcribe it again, decided from its
/// record alone (unit tested).
nonisolated enum RecordingDayRuns {
    nonisolated enum Step: Sendable, Equatable {
        /// Quieter than `silenceDBFS`: no text, no engine (recorded silence is not a gap).
        case skip([Int])
        /// Their audio went before they were transcribed: `failed`, reported as an `untranscribed` gap.
        case missingAudio([Int])
        /// Transcribe these contiguous chunks together, in this order.
        case run([Int])
        /// A run that may still grow (its last chunk is the newest, it is shorter than `runSeconds`): look again then.
        case wait(Date)
    }

    /// The next chunk follows the previous one when it starts within this of its end (the writer's clocks).
    static let joinTolerance = 1.0
    /// A run whose newest chunk came this long ago without a newer one is transcribed as it is.
    static let settleSeconds = 20.0
    /// Room for clock rounding when adding chunk durations up to `runSeconds`.
    private static let lengthSlack = 0.5
    /// Below one audio sample at any rate the analyzer takes (16 kHz is 62.5 µs): only the sums of the header lengths
    /// round this much, so no sample of the next chunk can hide in it (`RecordingRun.assign`).
    private static let crossEpsilon = 1e-6

    static func isSilent(_ chunk: RecordingDayChunk) -> Bool {
        chunk.level.map { $0 < RecordingDayContract.silenceDBFS } ?? false
    }

    /// Not transcribed yet and still to be: neither erased nor lost. With its audio it is read; without it (retention
    /// took it first) it is settled as untranscribed.
    static func isWaiting(_ chunk: RecordingDayChunk) -> Bool {
        !chunk.isErasedOrLost && chunk.state == .waiting
    }

    /// The next step for `record`, nil when nothing waits for transcription. Chunks without audio and silent ones are
    /// settled first, all at once; then the first run in seq order, once no seq below it is still on its way
    /// (`gapWait`). A chunk that already failed a run is tried alone next time, so one unreadable file doesn't use up
    /// its neighbours' tries.
    static func next(_ record: RecordingDayRecord, now: Date) -> Step? {
        let waiting = record.chunks.values.filter(isWaiting).sorted { $0.seq < $1.seq }
        guard !waiting.isEmpty else { return nil }
        let gone = waiting.filter { !$0.hasAudio }.map(\.seq)
        if !gone.isEmpty { return .missingAudio(gone) }
        let silent = waiting.filter(isSilent).map(\.seq)
        if !silent.isEmpty { return .skip(silent) }
        let first = waiting[0]
        if let until = gapWait(record, below: first.seq, now: now) { return .wait(until) }
        var run = [first]
        var total = first.duration
        var open = first.attempts == 0
        if open {
            while let previous = run.last, let next = record.chunks[previous.seq + 1] {
                guard joins(next, after: previous, total: total, in: record) else {
                    open = false
                    break
                }
                run.append(next)
                total += next.duration
            }
        }
        guard let last = run.last else { return nil }
        // Over once something follows it, the microphone closed after it (a pause, the day's end), it is full, or no
        // newer chunk came for a while. Never inferred from silence: only the watch's events say the microphone closed.
        let later = record.chunks.keys.contains { $0 > last.seq }
        let ended = record.closedAt != nil || record.seqCount.map { last.seq + 1 >= $0 } == true
            || record.pauses.contains { $0.from >= last.startedAt }
        let full = total + RecordingDayContract.liveChunkSeconds > RecordingDayContract.runSeconds + lengthSlack
        let settled = now.timeIntervalSince(last.receivedAt) >= settleSeconds
        if !open || later || ended || full || settled { return .run(run.map(\.seq)) }
        return .wait(last.receivedAt.addingTimeInterval(settleSeconds))
    }

    /// When transcription may go on past the seqs missing below `seq`, while that is still ahead; nil when it may now.
    /// A missing seq is one the watch still holds (docs/assistant/day-recap.md「Acknowledgements and retries」: the phone
    /// transcribes up to the gap and goes on past it once it has been open `gapWaitSeconds` while later chunks keep
    /// coming): a chunk whose live send failed comes a little later and is read in one run with its neighbours, its
    /// sentences whole and its positions in order. Each gap is open from when the first held chunk after it arrived (its
    /// persisted `receivedAt`, so a relaunch doesn't restart the wait), as coverage counts it (`RecordingDayCoverage`).
    static func gapWait(_ record: RecordingDayRecord, below seq: Int, now: Date) -> Date? {
        guard seq > 0, (0..<seq).contains(where: { !record.isAccounted($0) }) else { return nil }
        let held = record.chunks.keys.sorted()
        var index = 0
        var until: Date?
        for missing in 0..<seq where !record.isAccounted(missing) {
            while index < held.count, held[index] <= missing { index += 1 }
            guard index < held.count, let later = record.chunks[held[index]] else { break }
            let expiry = later.receivedAt.addingTimeInterval(RecordingDayContract.gapWaitSeconds)
            if expiry > now { until = max(until ?? expiry, expiry) }
        }
        return until
    }

    /// Whether `next` continues a run that ends with `previous` (`total` seconds long so far): its audio follows on, with
    /// no pause of the microphone in between.
    private static func joins(_ next: RecordingDayChunk, after previous: RecordingDayChunk, total: Double,
                              in record: RecordingDayRecord) -> Bool {
        isWaiting(next) && next.hasAudio && !isSilent(next) && next.attempts == 0
            && abs(next.startedAt.timeIntervalSince(previous.endedAt)) < joinTolerance
            && !record.pauses.contains { $0.from >= previous.startedAt && $0.from < next.startedAt }
            && total + next.duration <= RecordingDayContract.runSeconds + lengthSlack
    }

    /// Transcribed chunks with a sentence that runs into an erased chunk after them: that sentence holds erased words,
    /// so the chunk is transcribed again on its own, or loses the sentence when its audio is gone (an erase must
    /// reach every sentence its audio contributed to). A sentence ends past its chunk's duration only when its run went
    /// on into the next seq (`RecordingRun.assign` measures it on the header lengths, and a chunk transcribed alone or
    /// last in its run never does), so how far it reaches is counted in the following seqs' durations, with no tolerance
    /// and whatever wall-clock gap the run joined across. A sentence with no text (an erase or a repair took it) holds
    /// no words.
    static func bleeding(_ record: RecordingDayRecord) -> [Int] {
        guard record.chunks.values.contains(where: \.erased) else { return [] }
        var out: [Int] = []
        for chunk in record.chunks.values where chunk.state == .transcribed && !chunk.isErasedOrLost {
            guard let reach = chunk.sentences?.filter({ !$0.text.isEmpty }).map(\.end).max(),
                  reach > chunk.duration + crossEpsilon else { continue }
            var edge = chunk.duration
            var seq = chunk.seq + 1
            // Every seq the sentence's audio came from: the run joined them in seq order, end to end.
            while reach > edge + crossEpsilon, let next = record.chunks[seq] {
                if next.erased {
                    out.append(chunk.seq)
                    break
                }
                edge += max(0, next.duration)
                seq += 1
            }
        }
        return out.sorted()
    }

    /// The chunk as if it had just arrived, to be transcribed again: waiting, with no tries and none of its text. A
    /// sentence that has a hub position keeps it, with no text (a position never moves except by a `PUT`, and the
    /// repair `PUT` sends it empty; the new text gets new positions); one without goes. Returns the parts whose text on
    /// the hub this took words from: they need that repair.
    static func reset(_ chunk: inout RecordingDayChunk) -> Set<Int> {
        chunk.state = .waiting
        chunk.attempts = 0
        return blank(&chunk) { _ in true }
    }

    /// For a chunk whose audio is gone and can't be read again: only the sentences inside it keep their text (see
    /// `reset` for the others). Returns the parts that need a repair.
    static func keepInside(_ chunk: inout RecordingDayChunk) -> Set<Int> {
        let duration = chunk.duration
        return blank(&chunk) { $0.end > duration + crossEpsilon }
    }

    /// Takes the text of the sentences `matching` picks: a placed one keeps its place with no text, an unplaced one goes
    /// (unplaced ones come after the placed, so the positions stay paired with their sentences).
    private static func blank(_ chunk: inout RecordingDayChunk, matching: (RecordingSentence) -> Bool) -> Set<Int> {
        guard let sentences = chunk.sentences else { return [] }
        let positions = chunk.positions ?? []
        var kept: [RecordingSentence] = []
        var repaired = Set<Int>()
        for (index, sentence) in sentences.enumerated() {
            guard matching(sentence) else {
                kept.append(sentence)
                continue
            }
            guard index < positions.count else { continue }
            if !sentence.text.isEmpty { repaired.insert(positions[index].part) }
            kept.append(RecordingSentence(start: sentence.start, end: sentence.end, text: ""))
        }
        chunk.sentences = kept.isEmpty && positions.isEmpty ? nil : kept
        return repaired
    }

    /// Asks a repair `PUT` of these parts (`RecordingDayPart.needsRepair`).
    static func needRepair(_ record: inout RecordingDayRecord, _ parts: Set<Int>) {
        guard !parts.isEmpty else { return }
        for i in record.parts.indices where parts.contains(record.parts[i].part) { record.parts[i].needsRepair = true }
    }

    /// A run's new sentences for a chunk, as its save puts them: after the sentences an earlier transcription left
    /// placed (with no text), and without any said in a range erased since the run started (the store drops those too,
    /// `keepSettled`; leaving them out here spends no position on them).
    static func saved(_ found: [RecordingSentence], onto chunk: RecordingDayChunk, in record: RecordingDayRecord) -> [RecordingSentence] {
        let placed = Array((chunk.sentences ?? []).prefix(chunk.positions?.count ?? 0))
        let fresh = found.filter { sentence in
            let span = chunk.span(of: sentence)
            return !record.erasures.contains { RecordingDayRecord.spans(span.start, span.end, overlap: $0.from, $0.to) }
        }
        return placed + fresh
    }
}

// MARK: - The app's time

/// A short stretch of background time the system granted; `end()` gives it back.
@MainActor protocol RecordingDayGrant: AnyObject {
    func end()
}

/// When the day lanes may work: always in front; in the background only inside a grant the system gives
/// (a WatchConnectivity wake), ended at once when it runs out, or inside the catch-up task.
@MainActor protocol RecordingDayAppState: AnyObject {
    var inForeground: Bool { get }
    /// A grant, or nil when the system gives none. `onExpiry` runs on the main actor when it runs out first; the grant is
    /// over by then.
    func beginGrant(named name: String, onExpiry: @escaping @MainActor () -> Void) -> RecordingDayGrant?
}

/// Always in front: the Mac, and tests.
@MainActor final class RecordingDayForeground: RecordingDayAppState {
    var inForeground: Bool { true }
    func beginGrant(named name: String, onExpiry: @escaping @MainActor () -> Void) -> RecordingDayGrant? { nil }
}

/// The app's real state.
@MainActor enum RecordingDaySystemState {
    static let shared: RecordingDayAppState = {
        #if os(iOS) || os(visionOS)
        return RecordingDayUIKitState()
        #else
        return RecordingDayForeground()
        #endif
    }()
}

#if os(iOS) || os(visionOS)
/// In front unless the system put the app in the background. Grants of background time are iOS's
/// (`beginBackgroundTask`); visionOS gives none, so the lanes work there only in front.
@MainActor private final class RecordingDayUIKitState: RecordingDayAppState {
    var inForeground: Bool { UIApplication.shared.applicationState != .background }

    func beginGrant(named name: String, onExpiry: @escaping @MainActor () -> Void) -> RecordingDayGrant? {
        #if os(iOS)
        let grant = RecordingDayBackgroundTask(onExpiry: onExpiry)
        return grant.begin(name) ? grant : nil
        #else
        return nil
        #endif
    }
}
#endif

#if os(iOS)
/// `beginBackgroundTask`, ended exactly once: by its owner, or synchronously in the expiration handler (which must end
/// it before returning, or the system ends the app).
@MainActor private final class RecordingDayBackgroundTask: RecordingDayGrant {
    private var id = UIBackgroundTaskIdentifier.invalid
    private let onExpiry: @MainActor () -> Void

    init(onExpiry: @escaping @MainActor () -> Void) { self.onExpiry = onExpiry }

    func begin(_ name: String) -> Bool {
        id = UIApplication.shared.beginBackgroundTask(withName: name) { @Sendable in
            MainActor.assumeIsolated { self.expire() }
        }
        return id != .invalid
    }

    func end() {
        guard id != .invalid else { return }
        UIApplication.shared.endBackgroundTask(id)
        id = .invalid
    }

    private func expire() {
        onExpiry()
        end()
    }
}
#endif

// MARK: - The lane

/// Transcribes each day's chunks as they arrive (docs/assistant/day-recap.md, "Transcribe as it arrives"): silent ones
/// skipped, the rest in runs of contiguous chunks (`RecordingDayRuns`), each sentence saved on the chunk it starts in.
/// A run that fails counts a try against its chunks (persisted; `failed` after `maxAttempts`, an `untranscribed` gap);
/// a failure of the device itself stops without counting: the whole lane for one every language shares (no speech
/// permission), only the days in that language for one of a language (no model for it, or none installed).
/// A run's sentences get their hub positions in the same write that saves them (`RecordingDayUpload.place`). In the
/// background it works only inside a grant or the catch-up task, never downloads a model, and stops at the step it is
/// on the moment that time ends; what it was cut short of goes again only when it may work again: the app in front, a
/// chunk arriving (a grant of its own), or the next catch-up.
@MainActor @Observable final class RecordingDayTranscriber {
    static let shared = RecordingDayTranscriber()

    typealias Engine = @Sendable (_ urls: [URL], _ durations: [Double], _ locale: Locale, _ priority: TaskPriority,
                                  _ downloadsAssets: Bool) async throws -> [(file: Int, sentence: RecordingSentence)]

    /// The day whose run is in the engine now (this lane's own; `RecordingTranscription.current` is never set for a
    /// day). Set per run, not per look at a day: a chunk arrives every ten seconds.
    private(set) var current: UUID?
    /// Why the lane stopped for the whole device (`RecordingSpeechError.isEntryLevel`, and not `isPerLanguage`); cleared
    /// when the app comes to the front or a day's language changes.
    private(set) var blocked: RecordingSpeechError?
    /// Languages (`RecordingEntry.locale`) whose days wait because the device can't transcribe them now (no model, or
    /// not installed while downloads are off): the lane goes on with the days in other languages. Cleared like
    /// `blocked` (a model may download in front).
    private(set) var blockedLanguages: [String: RecordingSpeechError] = [:]

    @ObservationIgnored private let store: RecordingDayStore
    @ObservationIgnored private let library: RecordingLibrary
    @ObservationIgnored private let engine: Engine
    @ObservationIgnored private let clipsIdle: () async -> Void
    @ObservationIgnored private let now: () -> Date
    @ObservationIgnored private let appState: RecordingDayAppState
    @ObservationIgnored private let defaults: UserDefaults
    @ObservationIgnored private let languageChanged: (UUID) async -> Void

    @ObservationIgnored private var started = false
    @ObservationIgnored private var listening: Task<Void, Never>?
    @ObservationIgnored private var worker: Task<Void, Never>?
    @ObservationIgnored private var timer: Task<Void, Never>?
    @ObservationIgnored private var grant: RecordingDayGrant?
    /// The day the worker is on (`cancel` stops it).
    @ObservationIgnored private var visiting: UUID?
    /// `cancel` stopped the worker for this day alone: the others go on.
    @ObservationIgnored private var cancelledVisit: UUID?
    /// The engine call in flight, and whether it may download a model (it started with the app in front).
    @ObservationIgnored private var call: (task: Task<[(file: Int, sentence: RecordingSentence)], Error>, downloads: Bool)?
    /// `background()` cut that call short so no model downloads in the background: the run goes again at once, without.
    @ObservationIgnored private var cutDownload = false
    /// Days to look at, in the order they changed.
    @ObservationIgnored private var queue: [UUID] = []
    /// Days with an open run: looked at again then.
    @ObservationIgnored private var waits: [UUID: Date] = [:]
    /// Days a visit found with nothing to transcribe (a foreground sweep skips them until they change).
    @ObservationIgnored private var settled: Set<UUID> = []
    /// Bumped when a day's language changes: a run that started before is thrown away.
    @ObservationIgnored private var generations: [UUID: Int] = [:]
    /// The engine changed since failed chunks were given up on (`engineKey`): each day's are tried again once.
    @ObservationIgnored private var engineChanged = false
    @ObservationIgnored private var retried: Set<UUID> = []
    /// The catch-up runs (`drain`): its task is the lane's time, and it alone starts the worker meanwhile.
    @ObservationIgnored private var draining = false

    /// The system the failed chunks were given up on: a new one (an OS update brings new models) tries them again.
    static let engineKey = "recording.day.engine"
    /// Steps one visit of a day takes at most before letting other days go (a long backlog continues next visit).
    private static let maxSteps = 500

    /// - Parameter languageChanged: a day's language changed, after its chunks were reset: the upload decides a repair
    ///   `PUT` for its parts (`RecordingDaySync.languageChanged`), the only request that carries a language.
    init(store: RecordingDayStore = .shared, library: RecordingLibrary = .shared,
         engine: @escaping Engine = RecordingDayTranscriber.onDevice,
         clipsIdle: @escaping () async -> Void = { await RecordingTranscription.shared.whenIdle() },
         now: @escaping () -> Date = { .now }, appState: RecordingDayAppState = RecordingDaySystemState.shared,
         defaults: UserDefaults = .standard,
         languageChanged: @escaping (UUID) async -> Void = { await RecordingDaySync.shared.languageChanged($0) }) {
        self.store = store; self.library = library; self.engine = engine; self.clipsIdle = clipsIdle; self.now = now
        self.appState = appState; self.defaults = defaults; self.languageChanged = languageChanged
    }

    /// The on-device engine (`RecordingAnalyzerEngine.transcribeRun`).
    nonisolated static func onDevice(_ urls: [URL], _ durations: [Double], _ locale: Locale, _ priority: TaskPriority,
                                     _ downloadsAssets: Bool) async throws -> [(file: Int, sentence: RecordingSentence)] {
        guard #available(iOS 26.0, *) else { throw RecordingSpeechError.needsNewerSystem }
        guard let (engine, resolved) = await RecordingAnalyzerEngine.best(for: locale) else { throw RecordingSpeechError.unsupportedLanguage }
        return try await engine.transcribeRun(urls, durations: durations, locale: resolved, priority: priority,
                                              downloadsAssets: downloadsAssets)
    }

    // MARK: Triggers

    /// Follows the day store from now on (launch, before WatchConnectivity activates). Idempotent.
    func start() {
        guard !started else { return }
        started = true
        noteEngine()
        let changes = store.changes
        listening = Task { [weak self] in
            for await id in changes { self?.changed(id) }
        }
    }

    /// A day changed (a chunk came, an erase): looked at soon.
    func changed(_ id: UUID) {
        settled.remove(id)
        enqueue(id)
        kick()
    }

    /// The app came to the front: a model may download now, so a block ends; every day not known to be done (within the
    /// audio's 7 days) is looked at.
    func foreground() {
        blocked = nil
        blockedLanguages = [:]
        for id in recentDayIDs() where !settled.contains(id) { enqueue(id) }
        kick()
    }

    /// The app went to the background: a run in progress may finish only inside a grant, and none given stops it
    /// now (a cut-short run counts no try). A run that started in front may be downloading a model: it is cut short and
    /// goes again at once without the download, which waits for the app to be in front.
    func background() {
        guard let worker, !draining else { return }
        guard ensureGrant() else {
            worker.cancel()
            return
        }
        if let call, call.downloads {
            cutDownload = true
            call.task.cancel()
        }
    }

    /// A day's language changed: every chunk whose audio is still here is transcribed again in it, failed ones included
    /// (its old sentences keep their hub positions with no text, the new ones get new positions), and the hub gets a
    /// repair `PUT` with the new language whether or not any text changed (chunks whose audio retention deleted keep
    /// their text; a silent day has none).
    func setLocale(_ id: UUID, _ locale: String) {
        guard library.entry(id)?.locale != locale else { return }
        library.change(id) { $0.locale = locale }
        generations[id, default: 0] += 1
        blocked = nil
        blockedLanguages = [:]
        Task {
            await store.update(id) { record in
                Self.reset(&record) { $0.hasAudio && !$0.isErasedOrLost }
            }
            await languageChanged(id)
            changed(id)
        }
    }

    /// The user asked to try the day's untranscribed chunks again (explicit retry): those whose audio is still here
    /// get their tries back.
    func retryFailed(_ id: UUID) {
        blocked = nil
        blockedLanguages = [:]
        Task {
            await store.update(id) { Self.resetFailed(&$0) }
            changed(id)
        }
    }

    /// Stops transcribing a day (deleted, purged); the lane goes on with the others.
    func cancel(_ id: UUID) {
        queue.removeAll { $0 == id }
        waits[id] = nil
        settled.remove(id)
        guard visiting == id, let worker else { return }
        cancelledVisit = id
        worker.cancel()
    }

    /// The catch-up task: every day, until nothing is left that can be done now, inside the task's time (no grant of its
    /// own, no model download). Returns when done, blocked, or cancelled; cancelled, nothing starts again after it.
    func drain() async {
        draining = true
        defer { draining = false }
        for id in recentDayIDs() { enqueue(id) }
        for id in await store.ids() { enqueue(id) }
        while !Task.isCancelled {
            // A worker already going (in front, or in a grant) becomes the catch-up's; the next ones start here.
            if worker == nil {
                guard blocked == nil, !queue.isEmpty else { break }
                startWorker()
            }
            guard let pass = worker else { break }
            await withTaskCancellationHandler { await pass.value } onCancel: { pass.cancel() }
        }
    }

    /// Returns once the worker is idle (tests).
    func whenIdle() async {
        while let worker { await worker.value }
    }

    /// Chunks still to transcribe that this lane knows of, or a block: the catch-up asks for another run.
    var hasWork: Bool { blocked != nil || !blockedLanguages.isEmpty || !queue.isEmpty || !waits.isEmpty }

    // MARK: The worker

    private func enqueue(_ id: UUID) {
        waits[id] = nil
        if !queue.contains(id) { queue.append(id) }
    }

    /// Starts the worker when there is work and the lane may do it: in front, or in the background inside a grant.
    /// While the catch-up runs, it alone starts workers (`drain`).
    private func kick() {
        guard worker == nil, !draining, blocked == nil, !queue.isEmpty else { return }
        guard appState.inForeground || ensureGrant() else { return }
        startWorker()
    }

    @discardableResult private func startWorker() -> Task<Void, Never> {
        let task = Task<Void, Never> { [weak self] in await self?.runLane() }
        worker = task
        return task
    }

    /// The lane's grant of background time: the one it holds, or a new one; false when the system gives none.
    private func ensureGrant() -> Bool {
        if grant == nil { grant = appState.beginGrant(named: "recording-day-transcribe") { [weak self] in self?.grantExpired() } }
        return grant != nil
    }

    /// Whether the next step may go: the app in front, the lane's grant, or the catch-up.
    private var mayWork: Bool { draining || appState.inForeground || grant != nil }

    /// Time ran out: the run in progress is cancelled, which counts no try (the grant is over already).
    private func grantExpired() {
        grant = nil
        worker?.cancel()
    }

    private func runLane() async {
        var interrupted = false
        defer { finish(interrupted: interrupted) }
        while blocked == nil, !queue.isEmpty {
            guard !Task.isCancelled, mayWork else {
                interrupted = true
                return
            }
            let id = queue.removeFirst()
            visiting = id
            switch await visit(id) {
            case .idle:
                settled.insert(id)
            case .later(let at):
                waits[id] = at
            case .more:
                // A long backlog: the other days go first.
                enqueue(id)
            case .languageBlocked:
                // Not settled: the next foreground sweep, or a change of language, brings it back.
                break
            case .interrupted:
                // Cut short (time ran out, a block): the day comes back with the next run, unless it was the one cancelled.
                if cancelledVisit != id { enqueue(id) }
            }
            visiting = nil
        }
    }

    /// The worker ended. It starts again here only after `cancel` stopped it for one day; time that ran out, or the app
    /// leaving the front without a grant, leaves the rest for the next time the lane may work.
    private func finish(interrupted: Bool) {
        visiting = nil
        worker = nil
        let resume = interrupted && cancelledVisit != nil && !draining && blocked == nil && !queue.isEmpty && mayWork
        cancelledVisit = nil
        if resume {
            startWorker()
        } else {
            grant?.end()
            grant = nil
        }
        scheduleTimer()
    }

    /// A timer for the soonest open run; when it fires those days are looked at, if the lane may work then (`kick`).
    private func scheduleTimer() {
        timer?.cancel()
        timer = nil
        guard let at = waits.values.min() else { return }
        let delay = max(0, at.timeIntervalSince(now()))
        timer = Task { [weak self] in
            try? await Task.sleep(for: .seconds(delay))
            guard !Task.isCancelled, let self else { return }
            self.timer = nil
            let due = self.now()
            for (id, date) in self.waits where date <= due { self.enqueue(id) }
            self.kick()
        }
    }

    // MARK: One day

    private enum Visit {
        /// Nothing left to transcribe now.
        case idle
        /// An open run: look again then.
        case later(Date)
        /// More than one visit's steps: the rest after the other days.
        case more
        /// Its language can't be transcribed now (`blockedLanguages`): the other days go on.
        case languageBlocked
        /// Cancelled, out of time, or blocked.
        case interrupted
    }

    private enum Run {
        /// Its text saved, or a try counted.
        case done
        /// Cut short so no model downloads in the background: the same run again now.
        case again
        /// Cancelled, out of time, or blocked: nothing counted.
        case stopped
        /// The day's language can't be transcribed now: nothing counted, and the lane goes on with other days.
        case languageBlocked
    }

    /// Transcribes `id` until nothing is left now.
    private func visit(_ id: UUID) async -> Visit {
        var previous: RecordingDayRuns.Step?
        for _ in 0..<Self.maxSteps {
            guard !Task.isCancelled, blocked == nil, mayWork else { return .interrupted }
            // A recording someone waits for goes first.
            await clipsIdle()
            guard !Task.isCancelled, mayWork else { return .interrupted }
            guard let record = await store.record(id) else { return .idle }
            if engineChanged, !retried.contains(id) {
                retried.insert(id)
                await retryFailedChunks(id, record)
                continue
            }
            let bleeding = RecordingDayRuns.bleeding(record)
            if !bleeding.isEmpty {
                await redo(id, bleeding)
                continue
            }
            guard let step = RecordingDayRuns.next(record, now: now()) else { return .idle }
            // Settling the same chunks twice means the last change didn't take (the day went meanwhile): stop rather than
            // spin. A run may come again: each failure counts a try, so that ends.
            defer { previous = step }
            switch step {
            case .wait(let at):
                return .later(at)
            case .skip(let seqs):
                guard step != previous else { return .idle }
                await settle(id, seqs, as: .skipped)
            case .missingAudio(let seqs):
                guard step != previous else { return .idle }
                await settle(id, seqs, as: .failed)
            case .run(let seqs):
                // Silence and missing audio settle without the engine; speech waits for its language.
                guard blockedLanguages[language(of: id)] == nil else { return .languageBlocked }
                switch await transcribe(id, seqs, record) {
                case .stopped: return .interrupted
                case .languageBlocked: return .languageBlocked
                case .done, .again: break
                }
            }
        }
        return .more
    }

    /// One run through the engine, its result saved as one change: every chunk of the run gets its sentences (none for
    /// a chunk no sentence starts in), or, when the run failed, a try.
    private func transcribe(_ id: UUID, _ seqs: [Int], _ record: RecordingDayRecord) async -> Run {
        let urls = seqs.map { store.audioURL(id, seq: $0) }
        let durations = seqs.map { record.chunks[$0]?.duration ?? 0 }
        let languageID = language(of: id)
        let locale = Locale(identifier: languageID)
        let generation = generations[id, default: 0]
        // A model downloads only with the app in front, never inside a grant or the catch-up.
        let downloads = appState.inForeground && !draining
        let engine = engine
        let task = Task { try await engine(urls, durations, locale, downloads ? .utility : .background, downloads) }
        cutDownload = false
        call = (task, downloads)
        current = id
        defer {
            current = nil
            call = nil
        }
        do {
            let found = try await withTaskCancellationHandler { try await task.value } onCancel: { task.cancel() }
            try Task.checkCancellation()
            // The language changed while it ran: its text is the old language's.
            guard generations[id, default: 0] == generation else { return .done }
            await save(found, of: seqs, in: id)
            return .done
        } catch {
            // Out of time, the catch-up over, the day cancelled: counts nothing.
            if Task.isCancelled { return .stopped }
            if cutDownload { return .again }
            if let speech = error as? RecordingSpeechError, speech.isEntryLevel {
                // A model of this language only: the days in other languages go on.
                guard !speech.isPerLanguage else {
                    blockedLanguages[languageID] = speech
                    return .languageBlocked
                }
                blocked = speech
                return .stopped
            }
            // Anything else is this run's, a cancellation the engine made on its own included: a try, so it ends.
            await store.update(id) { record in
                for seq in seqs where record.chunks[seq].map(RecordingDayRuns.isWaiting) == true {
                    record.chunks[seq]?.attempts += 1
                    if (record.chunks[seq]?.attempts ?? 0) >= RecordingDayContract.maxAttempts { record.chunks[seq]?.state = .failed }
                }
            }
            return .done
        }
    }

    /// A run's sentences onto its chunks, only as one whole: an erase or a reset of any of its chunks meanwhile means its
    /// sentences may hold erased words, and the chunks left are transcribed again without it. The sentences get their hub
    /// positions in the same change (`RecordingDayUpload.place`), so a chunk is never on disk transcribed without them.
    private func save(_ found: [(file: Int, sentence: RecordingSentence)], of seqs: [Int], in id: UUID) async {
        var bySeq: [Int: [RecordingSentence]] = Dictionary(uniqueKeysWithValues: seqs.map { ($0, []) })
        for (file, sentence) in found where seqs.indices.contains(file) { bySeq[seqs[file], default: []].append(sentence) }
        let result = bySeq
        let last = seqs.last
        await store.update(id) { record in
            guard seqs.allSatisfy({ record.chunks[$0]?.needsTranscription == true }) else { return }
            for seq in seqs {
                guard let chunk = record.chunks[seq] else { continue }
                var sentences = result[seq] ?? []
                if seq == last {
                    // Nothing of the run came after its last chunk, so nothing of it reaches past (`bleeding` reads an
                    // end past the duration as audio from the next seq).
                    sentences = sentences.map { sentence in
                        var sentence = sentence
                        sentence.end = min(sentence.end, chunk.duration)
                        sentence.start = min(sentence.start, sentence.end)
                        return sentence
                    }
                }
                let saved = RecordingDayRuns.saved(sentences, onto: chunk, in: record)
                record.chunks[seq]?.sentences = saved
                record.chunks[seq]?.state = .transcribed
            }
            RecordingDayUpload.place(&record)
        }
    }

    private func settle(_ id: UUID, _ seqs: [Int], as state: RecordingDayChunk.State) async {
        await store.update(id) { record in
            for seq in seqs {
                guard var chunk = record.chunks[seq], RecordingDayRuns.isWaiting(chunk) else { continue }
                chunk.state = state
                // Recorded silence has no text, and is no gap; what a transcription left placed stays.
                if state == .skipped { chunk.sentences = chunk.sentences ?? [] }
                record.chunks[seq] = chunk
            }
        }
    }

    /// Chunks whose text reaches into erased audio (`RecordingDayRuns.bleeding`): transcribed again alone, or, without
    /// their audio, cut to the sentences inside them. Text the hub may hold goes there by a repair `PUT`.
    private func redo(_ id: UUID, _ seqs: [Int]) async {
        await store.update(id) { record in
            var repaired = Set<Int>()
            for seq in seqs {
                guard var chunk = record.chunks[seq] else { continue }
                repaired.formUnion(chunk.hasAudio ? RecordingDayRuns.reset(&chunk) : RecordingDayRuns.keepInside(&chunk))
                record.chunks[seq] = chunk
            }
            RecordingDayRuns.needRepair(&record, repaired)
        }
    }

    /// A new engine: chunks given up on whose audio is still here get their tries back.
    private func retryFailedChunks(_ id: UUID, _ record: RecordingDayRecord) async {
        guard record.chunks.values.contains(where: Self.retriable) else { return }
        await store.update(id) { Self.resetFailed(&$0) }
    }

    /// A chunk given up on whose audio is still here.
    private nonisolated static func retriable(_ chunk: RecordingDayChunk) -> Bool {
        chunk.state == .failed && chunk.hasAudio && !chunk.isErasedOrLost
    }

    /// Every chunk given up on whose audio is still here, waiting again with its tries back.
    private nonisolated static func resetFailed(_ record: inout RecordingDayRecord) {
        reset(&record, where: retriable)
    }

    /// The chunks `matching` picks, to be transcribed again (`RecordingDayRuns.reset`), and the repairs that asks for.
    private nonisolated static func reset(_ record: inout RecordingDayRecord, where matching: (RecordingDayChunk) -> Bool) {
        var repaired = Set<Int>()
        for (seq, chunk) in record.chunks where matching(chunk) {
            var chunk = chunk
            repaired.formUnion(RecordingDayRuns.reset(&chunk))
            record.chunks[seq] = chunk
        }
        RecordingDayRuns.needRepair(&record, repaired)
    }

    /// Remembers which system transcribed; a different one than last time retries the failed chunks.
    private func noteEngine() {
        let signature = ProcessInfo.processInfo.operatingSystemVersionString
        guard defaults.string(forKey: Self.engineKey) != signature else { return }
        // The first launch has nothing failed yet; any later change of system gets every failed chunk retried once.
        if defaults.string(forKey: Self.engineKey) != nil { engineChanged = true }
        defaults.set(signature, forKey: Self.engineKey)
    }

    /// The language a day is transcribed in (its row's, the default without one).
    private func language(of id: UUID) -> String {
        library.entry(id)?.locale ?? RecordingDefaults.locale
    }

    /// The days whose audio may still be here (`audioMaxDays`), from the library's index rows (in memory).
    private func recentDayIDs() -> [UUID] {
        let since = now().addingTimeInterval(-(RecordingDayContract.audioMaxDays + 1) * 86_400)
        return library.entries.filter { $0.isDay && $0.createdAt >= since }.map(\.id)
    }
}
