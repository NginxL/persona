#if os(iOS)
import AVFoundation
import Foundation
import Observation
import OSLog

/// Plays a meeting's tape in the meeting room (#9417, #10589; docs/apple/recording.md "A meeting's tape and its
/// replay"): a made-up room meeting (Persona answering on screen, the live view and the highlights) records itself with
/// no microphone (`RecordingCapture.beginReplay`) while the tape's events reach the meeting room's stores when the tape's
/// clock passes their times: the hub's captions and lines in `MeetingTranscription` (as hub transcription shows them),
/// the room, its topics and highlights in `MeetingConversation`.
///
/// The tape's clock runs at the speed (1×, 2× or 4×, changeable while it plays) and stands still while the meeting is
/// paused. The tape's audio, when it has one and Sound is on, plays along at 1× and 2× and is the clock while it plays;
/// at 4× it is silent. At the tape's end the meeting goes on, silent, until End; its minutes are the tape's.
///
/// Nothing goes to a hub (`ReplayedMeeting`): the replayed meeting is never uploaded or deleted there, its `…/live` is
/// never read, it opens no audio socket, and the room offers nothing that would ask the hub about it.
@MainActor @Observable final class MeetingReplay {
    static let shared = MeetingReplay()

    /// The replayed meeting's recording (`ReplayedMeeting`).
    nonisolated static var id: UUID { ReplayedMeeting.id }
    /// Whether `recording` is the replayed meeting: kept away from every hub.
    nonisolated static func holds(_ recording: UUID) -> Bool { ReplayedMeeting.holds(recording) }

    /// The speeds the page and the room offer.
    static let speeds: [Double] = [1, 2, 4]
    /// The audio plays up to this speed; faster is silent.
    nonisolated static let loudest: Double = 2

    nonisolated static func clampedSpeed(_ speed: Double) -> Double {
        speed.isFinite && speed > 0 ? min(16, max(0.25, speed)) : 1
    }

    /// The tape playing, or played last (its minutes are the ended meeting's); nil before any.
    private(set) var tape: MeetingTape?
    private(set) var speed: Double = 1
    /// Whether the tape's audio plays (at speeds up to `loudest`).
    private(set) var sound = true
    /// Whether the tape has audio to play.
    private(set) var hasAudio = false
    /// Where the tape is, in its own seconds. Not observed: it moves ten times a second; a view reads it on its own clock.
    @ObservationIgnored private(set) var position: Double = 0

    @ObservationIgnored private var clock = MeetingReplayClock()
    @ObservationIgnored private var task: Task<Void, Never>?
    /// The run playing now: a run that ended late (its last tick after the next began) touches nothing of the next.
    @ObservationIgnored private var generation = UUID()
    @ObservationIgnored private(set) var audio: AVAudioPlayer?
    @ObservationIgnored private var sessionActive = false
    nonisolated private static let log = Logger(subsystem: "persona", category: "recording")

    var length: Double { tape?.length ?? 0 }

    /// Whether a replay is running now (until End).
    var running: Bool { task != nil }

    /// The tape's minutes for the replayed meeting once it ended. Without their `version`: the hub has no such minutes,
    /// so Add to To-Dos is never offered for them.
    static func minutes(for recording: UUID) -> RecordingMinutes? {
        holds(recording) ? shown(shared.tape?.minutes) : nil
    }

    /// A tape's minutes as the replayed meeting shows them: without their `version`.
    nonisolated static func shown(_ minutes: RecordingMinutes?) -> RecordingMinutes? {
        guard var minutes else { return nil }
        minutes.version = nil
        return minutes
    }

    /// Whether a replay can start now: nothing records (a meeting, a note or all day), and no system call is live.
    static var canStart: Bool {
        let capture = RecordingCapture.shared
        return capture.state == .idle && capture.recordingID == nil && capture.callGate() == nil
    }

    /// Starts `tape` in the meeting room, its audio from `audioFile` where there is one; false, and nothing changes,
    /// while something records or a system call is live. `ends`: the meeting ends by itself after the tape's last event
    /// (DEBUG screenshots).
    @discardableResult
    func play(_ tape: MeetingTape, audioFile: URL? = nil, speed: Double = 1, sound: Bool = true, ends: Bool = false) -> Bool {
        guard Self.canStart else { return false }
        stopTask()
        let id = Self.id
        let library = RecordingLibrary.shared
        library.restore(id)
        var entry = RecordingEntry(id: id, createdAt: .now, source: .phone, title: tape.title, active: true)
        entry.meeting = RecordingMeeting(eventID: "replay", title: tape.title, start: .now, end: .now.addingTimeInterval(max(60, tape.length)),
                                         location: nil, attendees: [])
        entry.attendance = RecordingAttendance.room(.listener, private: false, photos: false, liveView: true, highlights: true)
        do { try library.save(entry) } catch { return false }
        // The entry first: the room the live sheet opens reads it as the meeting begins.
        guard RecordingCapture.shared.beginReplay(id) else {
            try? library.remove(id)
            return false
        }
        self.tape = tape
        self.speed = Self.clampedSpeed(speed)
        self.sound = sound
        hasAudio = audioFile != nil
        position = 0
        clock = MeetingReplayClock(schedule: tape.ordered, length: tape.length)
        MeetingTranscription.shared.showReplay(id, frame: MeetingTape.Frame())
        MeetingConversation.shared.showReplay(id, frame: MeetingTape.Frame())
        Self.log.notice("replay: \(tape.events.count, privacy: .public) events over \(Int(tape.length), privacy: .public) s at \(self.speed, privacy: .public)x")
        let run = UUID()
        generation = run
        task = Task { @MainActor in
            if let audioFile { await self.prepareAudio(audioFile, run: run) }
            await self.run(run, ends: ends)
        }
        return true
    }

    func setSpeed(_ speed: Double) {
        let speed = Self.clampedSpeed(speed)
        guard speed != self.speed else { return }
        self.speed = speed
        syncAudio()
    }

    func setSound(_ on: Bool) {
        guard on != sound else { return }
        sound = on
        syncAudio()
    }

    /// After the replayed meeting's page after it closed (the live sheet went away), and at launch: the replayed
    /// meeting leaves this phone's recordings. Not while a replay runs.
    func clearEnded() {
        guard !running, RecordingCapture.shared.recordingID != Self.id else { return }
        if RecordingLibrary.shared.entry(Self.id) != nil { try? RecordingLibrary.shared.remove(Self.id) }
    }

    // MARK: Playing

    private func run(_ run: UUID, ends: Bool) async {
        let capture = RecordingCapture.shared
        var last = ContinuousClock.now
        while !Task.isCancelled, generation == run, capture.recordingID == Self.id {
            try? await Task.sleep(for: .milliseconds(100))
            let now = ContinuousClock.now
            let step = (now - last) / .seconds(1)
            last = now
            let recording = capture.state == .recording
            // The audio is the clock while it plays; else the tape's own time moves at the speed, and stands while paused.
            if let audio, audio.isPlaying {
                clock.move(to: audio.currentTime)
            } else if recording {
                clock.advance(by: step, speed: speed)
            }
            position = clock.position
            let changed = clock.take()
            if changed.hub { MeetingTranscription.shared.showReplay(Self.id, frame: clock.frame) }
            if changed.room { MeetingConversation.shared.showReplay(Self.id, frame: clock.frame) }
            syncAudio()
            guard recording else { continue }
            capture.advanceReplay(elapsed: clock.position, level: clock.level(speed: speed))
            if ends, clock.atEnd {
                Self.log.notice("replay: the tape ended; ending the meeting")
                MeetingRecorder.shared.stop(recordingID: nil)
                break
            }
        }
        if generation == run { finish() }
    }

    /// The meeting ended (End, or anything that stopped it), or another tape begins: the audio stops and the session
    /// goes back.
    private func finish() {
        task?.cancel()
        task = nil
        audio?.stop()
        audio = nil
        hasAudio = false
        guard sessionActive else { return }
        sessionActive = false
        Self.deactivateSession()
    }

    private static func deactivateSession() {
        Task.detached(priority: .utility) {
            LiveCallGate.sessionQueue.sync {
                // A call that went live owns the session now: deactivating it here would stop the call's audio.
                guard !LiveCallGate.liveNow else { return }
                try? AVAudioSession.sharedInstance().setActive(false, options: .notifyOthersOnDeactivation)
            }
        }
    }

    private func stopTask() {
        generation = UUID()
        finish()
    }

    // MARK: Audio

    /// Opens the tape's audio and the session for playing it, off the main actor (a file read and the audio service).
    private func prepareAudio(_ file: URL, run: UUID) async {
        let box = await Task.detached(priority: .userInitiated) { () -> AudioBox? in
            do {
                let player = try AVAudioPlayer(contentsOf: file)
                player.enableRate = true
                player.prepareToPlay()
                let session = AVAudioSession.sharedInstance()
                // Ordered with the call's own category: a call that went live while the file opened keeps its session.
                let activated = try LiveCallGate.sessionQueue.sync { () -> Bool in
                    guard !LiveCallGate.liveNow else { return false }
                    try session.setCategory(.playback, mode: .spokenAudio)
                    try session.setActive(true)
                    return true
                }
                guard activated else { return nil }
                return AudioBox(player: player)
            } catch {
                Self.log.error("replay: no audio: \(error.localizedDescription, privacy: .public)")
                return nil
            }
        }.value
        guard let box else { return }
        guard generation == run else {
            // Ended while the file opened: the session goes back, unless the next run plays audio of its own.
            if !sessionActive { Self.deactivateSession() }
            return
        }
        sessionActive = true
        audio = box.player
        syncAudio()
    }

    /// The audio plays exactly while the meeting records, Sound is on, the speed is at most `loudest` and the tape has
    /// audio left at its position; started where the tape is, at its speed.
    private func syncAudio() {
        guard let audio else { return }
        let plays = sound && speed <= Self.loudest && RecordingCapture.shared.state == .recording
            && RecordingCapture.shared.recordingID == Self.id && position < audio.duration
        if plays {
            if audio.rate != Float(speed) { audio.rate = Float(speed) }
            if !audio.isPlaying {
                audio.currentTime = position
                audio.play()
            }
        } else if audio.isPlaying {
            audio.pause()
        }
    }
}

/// An `AVAudioPlayer` made off the main actor, handed to it once.
private nonisolated final class AudioBox: @unchecked Sendable {
    let player: AVAudioPlayer
    init(player: AVAudioPlayer) { self.player = player }
}

/// The tape's clock and the room it makes (pure, for the replay and its tests): where the tape is in its own seconds,
/// the events that came by then applied to a frame, and which stores changed since the last look.
nonisolated struct MeetingReplayClock: Sendable {
    private(set) var position: Double = 0
    private(set) var frame = MeetingTape.Frame()
    private var schedule: [MeetingTape.Event] = []
    private var next = 0
    private var length: Double = 0
    /// When the last caption or line came, tape seconds: the meter shows speech for a while after.
    private var lastSpeech = -Double.infinity

    init(schedule: [MeetingTape.Event] = [], length: Double = 0) {
        self.schedule = schedule
        self.length = length
    }

    /// `step` seconds of the wall clock went by at `speed`; never past the tape's end.
    mutating func advance(by step: Double, speed: Double) {
        guard step.isFinite, step > 0 else { return }
        move(to: position + step * MeetingReplay.clampedSpeed(speed))
    }

    /// The tape is at `t` (the audio's time); never back, never past its end.
    mutating func move(to t: Double) {
        guard t.isFinite else { return }
        position = min(max(position, t), max(length, schedule.last?.t ?? 0))
    }

    /// Applies the events up to `position`; which stores they change.
    mutating func take() -> (hub: Bool, room: Bool) {
        var hub = false, room = false
        while next < schedule.count, schedule[next].t <= position {
            let event = schedule[next]
            frame.apply(event)
            switch event.kind {
            case .interim, .line:
                hub = true
                lastSpeech = event.t
            case .problem: hub = true
            case .topics, .highlights, .room: room = true
            }
            next += 1
        }
        return (hub, room)
    }

    /// Every event came and the tape's length is reached.
    var atEnd: Bool { next >= schedule.count && position >= length }

    /// The meter: speech-like while lines come, quiet in between.
    func level(speed: Double) -> Double {
        let speaking = position - lastSpeech < 3 || frame.interim != nil
        return speaking ? min(1, 0.1 + 0.6 * max(0, sin(position * 0.9)) * (0.5 + 0.5 * sin(position * 9))) : 0.02
    }
}

extension MeetingTranscription {
    /// The replayed meeting (`MeetingReplay`): hub transcription in effect with its socket up, holding `frame`'s caption,
    /// lines and problem.
    func showReplay(_ id: UUID, frame: MeetingTape.Frame) {
        var m = meeting(id)
        m.asked = .hub
        m.decided = MeetingTranscriptionRule.Decided(mode: .hub, olderManager: false)
        m.socket = .ready
        m.everUp = true
        m.lines = frame.lines
        m.interim = frame.interim
        m.problem = frame.problem
        replace(id, m)
    }
}

extension MeetingConversation {
    /// The replayed meeting (`MeetingReplay`): the room as `frame` has it (its state only: the room's conversation isn't
    /// on any hub), its topics and highlights.
    func showReplay(_ id: UUID, frame: MeetingTape.Frame) {
        var m = meeting(id)
        m.room = MeetingRoom(state: frame.room?.state ?? MeetingRoom.State.ready.rawValue, problem: frame.room?.problem)
        m.topics = frame.topics
        m.highlights = Array(frame.highlights.prefix(Self.maxHighlights))
        m.privateMark = false
        m.privateRead = true
        m.loaded = true
        replace(id, m)
    }
}

#if DEBUG
// MARK: - Launch arguments (DEBUG)

/// `-showMeetingReplay <tape>` (an absolute path, a name in Documents/meeting-tapes, or `sample` for the made-up one in
/// `MeetingTapeSample`), `-showMeetingReplaySpeed 1|2|4`, `-showMeetingReplayEnd YES`: plays it at launch, with no
/// sound. `release.sh` keeps these names out of Release builds.
extension MeetingReplay {
    static let key = "showMeetingReplay"
    static let speedKey = "showMeetingReplaySpeed"
    static let endKey = "showMeetingReplayEnd"

    /// The tape `value` names: `sample`, an absolute path, or a name in `MeetingTape.folder` (with or without `.json`).
    /// Reads a file: off the main actor.
    nonisolated static func load(_ value: String) -> MeetingTape? {
        if value == "sample" { return MeetingTapeSample.tape }
        let url: URL
        if value.hasPrefix("/") {
            url = URL(fileURLWithPath: value)
        } else {
            url = MeetingTape.folder.appending(path: value.hasSuffix(".json") ? value : value + ".json")
        }
        // A folder holds the tape as tape.json.
        let file = (try? url.resourceValues(forKeys: [.isDirectoryKey]).isDirectory) == true ? url.appending(path: "tape.json") : url
        do {
            return try MeetingTape.decode(Data(contentsOf: file))
        } catch {
            log.error("replay: can't read the tape \(file.lastPathComponent, privacy: .public): \(error.localizedDescription, privacy: .public)")
            return nil
        }
    }

    /// At launch, with the other made-up meetings.
    static func startIfAsked() {
        let defaults = UserDefaults.standard
        guard let value = defaults.string(forKey: key) else { return }
        let speed = clampedSpeed(defaults.double(forKey: speedKey) == 0 ? 1 : defaults.double(forKey: speedKey))
        let ends = defaults.bool(forKey: endKey)
        Task { @MainActor in
            guard let tape = await Task.detached(priority: .userInitiated, operation: { load(value) }).value else { return }
            shared.play(tape, speed: speed, sound: false, ends: ends)
        }
    }
}
#endif
#endif
