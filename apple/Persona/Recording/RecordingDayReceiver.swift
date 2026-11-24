import Foundation
import os
#if os(iOS)
import UIKit
#endif

/// The phone's end of all-day recording's "watch to phone" (docs/assistant/day-recap.md「Watch to phone」). Chunks and
/// control events arrive on WatchConnectivity's queue; `RecordingDayIntake` stores each there (`RecordingDayStore`) and
/// only then acknowledges it. This, on the main actor, then lists the day among the recordings
/// (`RecordingLibrary.adoptDay`), asks for the background catch-up, sends the erases and deletes the watch has not
/// confirmed yet, and has the watch told what the phone takes (`RecordingDayPhoneCapabilities`). Transcription, the
/// upload and the summaries follow the store's `changes`. Day recordings never go through `onRecording` (so never
/// through `MeetingRecorder.tagWatchRecording`).
@MainActor final class RecordingDayReceiver {
    static let shared = RecordingDayReceiver()

    /// Submits the background catch-up (transcribe and upload while charging); replacing the pending request is cheap.
    /// Tests swap it out.
    static var requestCatchUp: () -> Void = {
        #if os(iOS)
        RecordingDayCatchUp.schedule()
        #endif
    }

    /// At most one catch-up request per this long from arriving chunks (C9): a chunk arrives every ten seconds.
    static let catchUpInterval: TimeInterval = 15 * 60
    /// The erases and deletes still waiting go again at most this often while the watch keeps sending: its app is
    /// running then, so a request whose answer went missing gets one soon.
    static let resendInterval: TimeInterval = 60
    /// Arriving chunks ask for an audio retention pass at most this often (`RecordingDayKeeper` runs one at most hourly
    /// anyway): a phone only ever woken by WatchConnectivity never comes to the front, where it otherwise runs.
    static let retentionInterval: TimeInterval = 60 * 60

    private let store: RecordingDayStore
    private let library: RecordingLibrary
    private let summaries: RecordingDaySummaries
    private let capabilities: RecordingDayPhoneCapabilities
    private let sendErase: (RecordingDayEraseRequest) -> Void
    private let sendDelete: (RecordingDayDelete) -> Void
    private let retain: () -> Void
    private let now: () -> Date
    private var started = false
    /// The intake WatchConnectivity calls; made by `start()`.
    private(set) var intake: RecordingDayIntake?
    /// Days whose index row to bring up to date, gathered while one is being adopted.
    private var rows: Set<UUID> = []
    private var adopting: Task<Void, Never>?
    private var lastCatchUp: Date?
    private var lastRetention: Date?
    private var lastResend: Date?
    private var resending: Task<Void, Never>?
    private var resendAgain = false
    private var listening: Task<Void, Never>?
    private var summariesRead = false
    #if os(iOS)
    private var flushTask = UIBackgroundTaskIdentifier.invalid
    #endif

    /// - Parameters:
    ///   - sendErase, sendDelete: one request to the watch (WatchSync's: a message while its app runs, else queued user
    ///     info, never queued twice).
    ///   - retain: asks for an audio retention pass (`RecordingDayKeeper.schedule`): once per launch, whatever the app's
    ///     state, and from arriving chunks (`retentionInterval`).
    init(store: RecordingDayStore = .shared, library: RecordingLibrary = .shared, summaries: RecordingDaySummaries = .shared,
         capabilities: RecordingDayPhoneCapabilities = .shared,
         sendErase: @escaping (RecordingDayEraseRequest) -> Void = { WatchSync.shared.sendDayErase($0) },
         sendDelete: @escaping (RecordingDayDelete) -> Void = { WatchSync.shared.sendDayDelete($0) },
         retain: @escaping () -> Void = { RecordingDayKeeper.shared.schedule() },
         now: @escaping () -> Date = { .now }) {
        self.store = store
        self.library = library
        self.summaries = summaries
        self.capabilities = capabilities
        self.sendErase = sendErase
        self.sendDelete = sendDelete
        self.retain = retain
        self.now = now
    }

    /// From `PhoneRecordingReceiver.start()`, before `WatchSync.activate()`: the system may launch the app in the
    /// background just to deliver a chunk or an event, so the hooks are in place before the session can call them.
    /// Idempotent; the hooks are set once and never again (nothing synchronizes them).
    func start() {
        guard !started else { return }
        started = true
        RecordingDayStore.useTombstones(of: library)
        let sync = WatchSync.shared
        let intake = RecordingDayIntake(store: store,
                                        sendAck: { @Sendable ack in sync.sendDayAck(ack) },
                                        arrived: { @Sendable id in Task { @MainActor in self.arrived(id) } },
                                        refused: { @Sendable id in Task { @MainActor in self.refused(id) } })
        self.intake = intake
        sync.onDayChunk = { @Sendable frame, reply in reply(intake.receive(frame: frame)) }
        sync.onDayFile = { @Sendable header, file in intake.receive(file: file, header: header) }
        sync.onDayEvent = { @Sendable envelope in intake.receive(event: envelope) }
        sync.onDayErased = { [weak self] answer in Task { await self?.watchErased(answer) } }
        sync.onDayDeleted = { [weak self] answer in Task { await self?.watchDeleted(answer) } }
        sync.onDayWatchState = { [summaries] state in summaries.receive(watch: state) }
        sync.onDayReachable = { [weak self] in self?.resendRequests() }
        // What the watch gets at the session's activation (WatchSync keeps it until then).
        capabilities.start()
        // Following changes only: the full read waits for the app to be in front (`readSummaries`).
        summaries.start()
        followRequests()
        Task { await recover() }
        #if os(iOS)
        followAppState()
        #endif
    }

    /// The watch session activated: what waits for the watch goes now (a send before activation goes nowhere).
    func sessionActivated() {
        // `AppModel`'s `WatchSync.shared.onActivated` calls this. Requests also go once recovery is done, whenever the
        // app comes to the front, when the watch becomes reachable (`onDayReachable`), and when the watch sends (also a
        // chunk of a deleted day, `refused`).
        resendRequests()
    }

    /// A chunk or an event was stored for `id` (any change the receiver made).
    func arrived(_ id: UUID) {
        // The transcriber and the upload follow `RecordingDayStore.shared.changes` on their own.
        requestCatchUpIfDue()
        retainIfDue()
        resendIfDue()
        rows.insert(id)
        guard adopting == nil else { return }
        adopting = Task {
            while let id = rows.popFirst() { await adoptRow(id) }
            adopting = nil
        }
    }

    /// A chunk or an event of a day deleted here came: the watch is still at it, so the delete (which the store made
    /// sure is waiting) goes again, at most once a minute (`resendIfDue`). Without this nothing would: refused chunks
    /// change no day, so they never pass through `arrived`.
    func refused(_ id: UUID) {
        resendIfDue()
    }

    /// Returns once every day that arrived so far has its library row (tests).
    func rowsAdopted() async {
        while let adopting { await adopting.value }
    }

    /// Sends every erase and delete the watch has not confirmed yet (`RecordingDayStore.watchRequests`); each answer
    /// settles its request (`watchErased`, `watchDeleted`). A send while one is under way runs once more after it.
    func resendRequests() {
        guard resending == nil else {
            resendAgain = true
            return
        }
        resending = Task {
            repeat {
                resendAgain = false
                let pending = await store.watchRequests()
                for erase in pending.erases { sendErase(erase) }
                for delete in pending.deletes { sendDelete(delete) }
            } while resendAgain
            resending = nil
        }
    }

    /// Returns once the requests asked for so far are sent (tests).
    func requestsSent() async {
        while let resending { await resending.value }
    }

    /// The watch erased what the phone asked it to.
    func watchErased(_ answer: RecordingDayErased) async {
        await store.watchErased(answer)
    }

    /// The watch deleted its copy of a day the phone deleted.
    func watchDeleted(_ answer: RecordingDayDeleted) async {
        await store.watchDeleted(answer)
    }

    /// A request the UI or the upload just made goes at once.
    private func followRequests() {
        guard listening == nil else { return }
        let changes = store.watchRequestChanges
        listening = Task { [weak self] in
            for await _ in changes { self?.resendRequests() }
        }
    }

    /// A crash can leave days whose chunks were acknowledged before the library's row for them was written (C14):
    /// the store rebuilds them from their folders, and each day without a row gets one.
    private func recover() async {
        let changed = Set(await store.recoverOnLaunch())
        for id in await store.ids() where changed.contains(id) || library.entry(id) == nil {
            await adoptRow(id)
        }
        readSummaries()
        resendRequests()
        // Audio past its deadline goes on every launch, also one in the background to take a chunk (the catch-up needs
        // the phone on power, and the app may not come to the front for days).
        retainIfDue()
    }

    /// Every day's summary, read once per launch. A launch in the background to take a chunk shows nothing, so it
    /// doesn't decode fourteen days for them; the read waits until the app comes to the front (P9). Days that change in
    /// the meantime get theirs from the store's `changes` (`RecordingDaySummaries.start`, which reads nothing else).
    private func readSummaries() {
        #if os(iOS)
        guard UIApplication.shared.applicationState != .background else { return }
        #endif
        guard !summariesRead else { return }
        summariesRead = true
        summaries.readAll()
    }

    /// The library's small index row for the day (kind `day`, no segments), closed once the watch closed it;
    /// `adoptDay` writes only when it changes and refuses a deleted day.
    private func adoptRow(_ id: UUID) async {
        guard let record = await store.record(id) else { return }
        library.adoptDay(id: id, day: record.day, timeZone: record.timeZone, createdAt: record.startedAt,
                         closed: record.closedAt != nil)
    }

    /// The catch-up is requested from here too: on a day the app is only ever woken by WatchConnectivity it never
    /// passes through the background transition that also requests it (C9).
    private func requestCatchUpIfDue() {
        let at = now()
        if let last = lastCatchUp, at >= last, at.timeIntervalSince(last) < Self.catchUpInterval { return }
        lastCatchUp = at
        Self.requestCatchUp()
    }

    private func retainIfDue() {
        let at = now()
        if let last = lastRetention, at >= last, at.timeIntervalSince(last) < Self.retentionInterval { return }
        lastRetention = at
        // Hourly across launches too: a WatchConnectivity relaunch must not read every held day each time.
        let store = store, interval = Self.retentionInterval
        Task { [weak self] in
            guard await store.claimRetentionPass(now: at, every: interval) else { return }
            self?.retain()
        }
    }

    private func resendIfDue() {
        let at = now()
        if let last = lastResend, at >= last, at.timeIntervalSince(last) < Self.resendInterval { return }
        lastResend = at
        resendRequests()
    }

    #if os(iOS)
    /// - Leaving the screen: iOS can suspend the app soon after and end it later without another word, so what changed
    ///   since the last coalesced write (transcripts, the upload's state) is written first, under a short background task.
    /// - Coming to the front: the summaries a background launch left unread (`readSummaries`), and the requests still
    ///   waiting for the watch.
    /// - A memory warning: the store lets go of the days it holds decoded (P11).
    private func followAppState() {
        let center = NotificationCenter.default
        center.addObserver(forName: UIApplication.didEnterBackgroundNotification, object: nil, queue: .main) { @Sendable _ in
            MainActor.assumeIsolated { self.flushInBackground() }
        }
        center.addObserver(forName: UIApplication.didBecomeActiveNotification, object: nil, queue: .main) { @Sendable _ in
            MainActor.assumeIsolated {
                self.readSummaries()
                self.resendRequests()
            }
        }
        center.addObserver(forName: UIApplication.didReceiveMemoryWarningNotification, object: nil, queue: .main) { @Sendable [store] _ in
            store.trimMemory()
        }
    }

    private func flushInBackground() {
        guard flushTask == .invalid else { return }
        flushTask = UIApplication.shared.beginBackgroundTask(withName: "recording-day-flush") { @Sendable in
            // Ended at once when time runs out; the sidecars and the event log hold everything acknowledged whatever
            // happens to the write.
            MainActor.assumeIsolated { self.endFlushTask() }
        }
        Task {
            await store.flushAll()
            endFlushTask()
        }
    }

    private func endFlushTask() {
        guard flushTask != .invalid else { return }
        UIApplication.shared.endBackgroundTask(flushTask)
        flushTask = .invalid
    }
    #endif
}

/// What WatchConnectivity's queue runs for an all-day chunk or event, synchronously: store it, then acknowledge it
/// (docs/assistant/day-recap.md「Acknowledgements and retries」). It never touches the main actor; what follows there
/// goes through `arrived`. What could not be stored is not acknowledged, so the watch keeps it and sends it again.
/// Acknowledgements go together where they can: those of chunks that came by file gather for a moment, and ride along
/// with the next reply to that recording's live chunk or event, or go on their own.
nonisolated final class RecordingDayIntake: Sendable {
    private let store: RecordingDayStore
    private let sendAck: @Sendable (RecordingDayAck) -> Void
    private let arrived: @Sendable (UUID) -> Void
    private let refused: @Sendable (UUID) -> Void
    private let ackDelay: TimeInterval
    private let queue = DispatchQueue(label: "persona.recording-day-acks", qos: .utility)
    private let waiting = OSAllocatedUnfairLock(initialState: Waiting())

    /// Acknowledgements per recording, until their batch goes.
    private nonisolated struct Waiting {
        var seqs: [UUID: Set<Int>] = [:]
        var scheduled = false
    }

    /// - Parameters:
    ///   - sendAck: sends one recording's acknowledgements (a message while reachable, else user info).
    ///   - arrived: a day changed (a chunk or an event stored); called on WatchConnectivity's queue.
    ///   - refused: a chunk or an event of a day deleted here came (the store asked the watch again to delete it);
    ///     called on WatchConnectivity's queue.
    ///   - ackDelay: how long file acknowledgements gather (a backlog arrives as a burst of files).
    init(store: RecordingDayStore, sendAck: @escaping @Sendable (RecordingDayAck) -> Void,
         arrived: @escaping @Sendable (UUID) -> Void, refused: @escaping @Sendable (UUID) -> Void = { _ in },
         ackDelay: TimeInterval = 1) {
        self.store = store
        self.sendAck = sendAck
        self.arrived = arrived
        self.refused = refused
        self.ackDelay = ackDelay
    }

    private func follow(_ result: RecordingDayStore.StoreResult, _ id: UUID) {
        if result.changed { arrived(id) }
        if result == .refused { refused(id) }
    }

    /// A chunk by `sendMessageData`: the reply is its acknowledgement (JSON `RecordingDayAck`), with the recording's
    /// file acknowledgements still waiting; empty when it was not stored.
    func receive(frame: Data) -> Data {
        guard let unframed = RecordingDayHeader.unframe(frame),
              let result = try? store.store(header: unframed.header, audio: unframed.audio) else { return Data() }
        let header = unframed.header
        follow(result, header.recording)
        return acknowledgement(header.recording, seqs: [header.seq], events: []).data
    }

    /// A chunk by `transferFile`: moved into the day before this returns. True when taken care of (stored, settled,
    /// already held, or for a deleted day), false to leave it to the watch's resend.
    func receive(file: URL, header: RecordingDayHeader) -> Bool {
        guard let result = try? store.store(header: header, movingFile: file) else { return false }
        follow(result, header.recording)
        batch(header.recording, seq: header.seq)
        return true
    }

    /// A control event, by message or user info: its acknowledgement once it is on disk (with the recording's file
    /// acknowledgements still waiting), which WatchSync sends as the message's reply or on its own; nil when it was not
    /// stored.
    func receive(event envelope: RecordingDayEventEnvelope) -> RecordingDayAck? {
        guard let result = try? store.store(event: envelope) else { return nil }
        if result.changed { arrived(envelope.recording) }
        if result == .refused { refused(envelope.recording) }
        return acknowledgement(envelope.recording, seqs: [], events: [envelope.event.n])
    }

    /// Sends the waiting acknowledgements now, one per recording.
    func flushAcks() {
        let batch = waiting.withLock { (state: inout Waiting) -> [UUID: Set<Int>] in
            defer { state = Waiting() }
            return state.seqs
        }
        for (id, seqs) in batch where !seqs.isEmpty { sendAck(RecordingDayAck(recording: id, seqs: seqs)) }
    }

    /// This acknowledgement with the recording's waiting ones, which then don't go again on their own. Every seq in it is
    /// on disk or settled, so the watch may delete each, whichever message the acknowledgement answers.
    private func acknowledgement(_ id: UUID, seqs: [Int], events: [Int]) -> RecordingDayAck {
        let waiting = self.waiting.withLock { $0.seqs.removeValue(forKey: id) } ?? []
        return RecordingDayAck(recording: id, seqs: waiting.union(seqs), events: events)
    }

    private func batch(_ id: UUID, seq: Int) {
        let first = waiting.withLock { (state: inout Waiting) -> Bool in
            state.seqs[id, default: []].insert(seq)
            defer { state.scheduled = true }
            return !state.scheduled
        }
        guard first else { return }
        queue.asyncAfter(deadline: .now() + ackDelay) { [weak self] in self?.flushAcks() }
    }
}
