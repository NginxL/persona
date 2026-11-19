#if os(iOS)
import Foundation
import Observation
import OSLog

// A meeting's tape (#9417; docs/apple/recording.md "A meeting's tape and its replay"). While a meeting records from an
// audio file (DEBUG: `-openMeetingAudio`, `RecordingFileInput`), the app writes down everything the meeting room is
// shown, as it arrives: the hub's captions and lines, the room's state, its topics (the map) and its highlights, then the
// minutes (`MeetingTapeRecorder`, DEBUG). `MeetingReplay` plays a tape back in the meeting room at its own pace, with no
// hub, so the live transcript, the highlights and the map can be looked at again and again: in DEBUG from a launch
// argument, on any build from Settings › About › Developing Persona (#10589, `MeetingTapesPage`).

/// A meeting as the app was shown it, in the order it came (`MeetingTapeRecorder` writes it, `MeetingReplay` plays it).
/// JSON, keys as below; times in seconds. A DEBUG run writes it to the app's Documents/meeting-tapes/<recording id>.json;
/// a tape to replay is a folder holding it as tape.json with the audio it names beside it (`MeetingTapeLibrary`).
nonisolated struct MeetingTape: Codable, Equatable, Sendable {
    static let formatName = "persona-meeting-tape"
    static let currentVersion = 1

    /// Always `persona-meeting-tape`.
    var format = MeetingTape.formatName
    var version = MeetingTape.currentVersion
    /// The title the meeting started with, and the one it had at the end (the hub may name it from what was said).
    var title: String
    var finalTitle: String?
    /// When the recording started (ISO 8601).
    var startedAt: Date
    /// The audio file the meeting heard (`-openMeetingAudio`): its name, which a tape folder keeps beside the tape. The
    /// recording's 0 is the file's 0.
    var audio: String?
    var locale: String?
    /// `hub` or `device`, as the meeting asked for it, and the hub's speech service when the app knew it.
    var transcription: String?
    var service: String?
    /// Persona's role as the meeting started (`listener`: answers on screen).
    var role: String?
    /// Seconds after the start when Stop was pressed, and the recording's length then; nil while it runs.
    var endedAfter: Double?
    var duration: Double?
    var events: [Event] = []
    /// The minutes the hub wrote, once they came, and the hub's `minutes_state` as last read.
    var minutes: RecordingMinutes?
    var minutesState: String?
    /// Seconds after the start when the minutes came.
    var minutesAfter: Double?
    /// The phone's own lines (its on-device transcript) at the end, when it had any.
    var phoneLines: [RecordingSentence]?

    /// One change the meeting room was shown.
    nonisolated struct Event: Codable, Equatable, Sendable {
        nonisolated enum Kind: String, Codable, Sendable {
            /// The hub's caption being said now (`text` nil: the caption went).
            case interim
            /// A line of the hub's live log (`n`, `start`, `end`, `text`, `source`).
            case line
            /// The room's topics, the map (`topics`, the whole list).
            case topics
            /// The room's highlights (`highlights`, the whole list).
            case highlights
            /// The room's state (`room`).
            case room
            /// The transcription's standing problem (`text`: its code, nil when it cleared).
            case problem
        }

        var kind: Kind
        /// Seconds after the recording started when it reached the app: what a replay waits for.
        var t: Double
        /// Where the recording was then (its timeline, pauses left out).
        var at: Double?
        var n: Int?
        var start: Double?
        var end: Double?
        var text: String?
        var source: String?
        var topics: [MeetingTopic]?
        var highlights: [MeetingHighlight]?
        var room: MeetingRoom?

        static func interim(_ caption: MeetingInterim?, t: Double, at: Double?) -> Event {
            Event(kind: .interim, t: t, at: at, start: caption?.start, text: caption?.text)
        }

        static func line(_ line: MeetingHubLine, t: Double, at: Double?) -> Event {
            Event(kind: .line, t: t, at: at, n: line.n, start: line.start, end: line.end, text: line.text, source: line.source)
        }

        /// A `line` event's line; nil for any other kind or one missing its fields.
        var hubLine: MeetingHubLine? {
            guard kind == .line, let n, let start, let end, let text else { return nil }
            return MeetingHubLine(n: n, start: start, end: end, text: text, source: source)
        }

        /// An `interim` event's caption; nil when the caption went.
        var caption: MeetingInterim? {
            guard kind == .interim, let start, let text, !text.isEmpty else { return nil }
            return MeetingInterim(start: start, text: text)
        }
    }

    /// What the meeting room holds once the events up to and including `t` came (pure: the replay's state and its
    /// tests).
    nonisolated struct Frame: Equatable, Sendable {
        var lines: [MeetingHubLine] = []
        var interim: MeetingInterim?
        var topics: [MeetingTopic] = []
        var highlights: [MeetingHighlight] = []
        var room: MeetingRoom?
        var problem: String?

        /// `event` applied, as the stores would take it: a line in its place by position (finishing the caption it
        /// covers), the lists replaced whole.
        mutating func apply(_ event: Event) {
            switch event.kind {
            case .interim: interim = event.caption
            case .line:
                guard let line = event.hubLine else { return }
                lines = MeetingTranscription.merged(lines, [line])
                if let caption = interim, caption.start <= line.end { interim = nil }
            case .topics: topics = event.topics ?? []
            case .highlights: highlights = event.highlights ?? []
            case .room: room = event.room
            case .problem: problem = event.text
            }
        }
    }

    /// The events in the order they came (by `t`, those of the same time as written).
    var ordered: [Event] {
        events.enumerated().sorted { a, b in a.element.t != b.element.t ? a.element.t < b.element.t : a.offset < b.offset }.map(\.element)
    }

    /// The room at `t` seconds after the start.
    func frame(at t: Double) -> Frame {
        var frame = Frame()
        for event in ordered where event.t <= t { frame.apply(event) }
        return frame
    }

    /// How long the tape runs: Stop, or else its last event.
    var length: Double { endedAfter ?? events.map(\.t).max() ?? 0 }

    /// When each event plays at `speed` (1 the tape's own pace), seconds after the replay began, in order.
    func schedule(speed: Double) -> [(at: Double, event: Event)] {
        let speed = MeetingReplay.clampedSpeed(speed)
        return ordered.map { ($0.t / speed, $0) }
    }

    static func decode(_ data: Data) throws -> MeetingTape {
        let tape = try decoder.decode(MeetingTape.self, from: data)
        guard tape.format == formatName else { throw DecodingError.dataCorrupted(.init(codingPath: [], debugDescription: "not a meeting tape")) }
        return tape
    }

    func encoded() throws -> Data { try Self.encoder.encode(self) }

    static var decoder: JSONDecoder {
        let d = JSONDecoder()
        d.dateDecodingStrategy = .iso8601
        return d
    }

    static var encoder: JSONEncoder {
        let e = JSONEncoder()
        e.dateEncodingStrategy = .iso8601
        e.outputFormatting = [.prettyPrinted, .sortedKeys, .withoutEscapingSlashes]
        return e
    }

    /// What a list shows of a tape: its title, length, how many lines, map changes and highlights came, and whether its
    /// audio is named.
    var summary: Summary {
        Summary(title: finalTitle?.isEmpty == false ? finalTitle! : title, length: length,
                lines: Set(events.compactMap { $0.kind == .line ? $0.n : nil }).count,
                topics: events.filter { $0.kind == .topics }.count, highlights: events.filter { $0.kind == .highlights }.count,
                audio: audio)
    }

    nonisolated struct Summary: Equatable, Sendable {
        var title: String
        var length: Double
        var lines: Int
        var topics: Int
        var highlights: Int
        var audio: String?
    }
}

#if DEBUG
extension MeetingTape {
    /// Where a DEBUG run keeps the tapes it writes.
    nonisolated static var folder: URL { URL.documentsDirectory.appending(path: "meeting-tapes") }
}

// MARK: - Writing a tape

/// Writes the tape of a meeting recorded from an audio file (`-openMeetingAudio`): from the moment it records, every
/// change of what the meeting room is shown (`MeetingTranscription`'s caption, lines and problem, `MeetingConversation`'s
/// room, topics and highlights), stamped with when it came; at Stop the length and the phone's own lines; then, read from
/// the hub every 15 seconds for up to 20 minutes, the minutes. A tape that ended without its minutes (the app quit before
/// the hub wrote them) goes on waiting for them at the next launch of such a run. It follows the stores (Observation), so
/// nothing in the meeting's own code knows of it. Written to `MeetingTape.folder` a second after each change and at once
/// at the end.
@MainActor final class MeetingTapeRecorder {
    static let shared = MeetingTapeRecorder()

    /// Every tape this run writes, by recording: the one being recorded and those waiting for their minutes.
    private var tapes: [UUID: MeetingTape] = [:]
    /// The recording being taped now.
    private var id: UUID?
    private var began = Date()
    private var stopped = false
    private var known = MeetingTape.Frame()
    private var writeDue: Task<Void, Never>?
    private let log = Logger(subsystem: "persona", category: "recording")

    /// At launch: follows the recordings of a run with `-openMeetingAudio`, and waits on for the minutes of tapes from
    /// earlier runs; nothing otherwise.
    static func startIfAsked() {
        guard UserDefaults.standard.string(forKey: RecordingFileInput.key) != nil else { return }
        shared.follow()
        Task { await shared.resumeWaiting() }
    }

    private func follow() {
        let changes = withObservationTracking { look() } onChange: {
            Task { @MainActor in MeetingTapeRecorder.shared.follow() }
        }
        // The recording's clock is read outside the tracking: it moves ten times a second.
        guard !changes.isEmpty, let id, var tape = tapes[id] else { return }
        let t = Date().timeIntervalSince(began)
        let at = stopped ? tape.duration : RecordingCapture.shared.elapsed
        for change in changes {
            var event = change
            event.t = t
            event.at = at
            tape.events.append(event)
        }
        tapes[id] = tape
        scheduleWrite()
    }

    /// Reads what is watched (Observation), and returns what changed since the last look, unstamped.
    private func look() -> [MeetingTape.Event] {
        let capture = RecordingCapture.shared
        let state = capture.state
        // Never a replay's (`ReplayedMeeting`): its minutes are no hub's to wait for.
        if id == nil, state == .recording, let recording = capture.recordingID, !ReplayedMeeting.holds(recording),
           let entry = RecordingLibrary.shared.entry(recording), entry.isMeeting {
            begin(entry)
        }
        guard let id else { return [] }
        if !stopped, capture.recordingID != id || state == .idle { stop(id) }
        let hub = MeetingTranscription.shared.meeting(id)
        let room = MeetingConversation.shared.meeting(id)
        var changes: [MeetingTape.Event] = []
        if hub.interim != known.interim {
            changes.append(.interim(hub.interim, t: 0, at: nil))
            known.interim = hub.interim
        }
        let had = Dictionary(known.lines.map { ($0.n, $0) }, uniquingKeysWith: { _, b in b })
        for line in hub.lines where had[line.n] != line { changes.append(.line(line, t: 0, at: nil)) }
        known.lines = hub.lines
        if hub.problem != known.problem {
            changes.append(MeetingTape.Event(kind: .problem, t: 0, text: hub.problem))
            known.problem = hub.problem
        }
        if room.room != known.room {
            changes.append(MeetingTape.Event(kind: .room, t: 0, room: room.room))
            known.room = room.room
        }
        if room.topics != known.topics {
            changes.append(MeetingTape.Event(kind: .topics, t: 0, topics: room.topics))
            known.topics = room.topics
        }
        if room.highlights != known.highlights {
            changes.append(MeetingTape.Event(kind: .highlights, t: 0, highlights: room.highlights))
            known.highlights = room.highlights
        }
        return changes
    }

    private func begin(_ entry: RecordingEntry) {
        id = entry.id
        began = entry.createdAt
        let attendance = entry.attendance
        tapes[entry.id] = MeetingTape(title: entry.displayTitle, startedAt: entry.createdAt,
                                      audio: UserDefaults.standard.string(forKey: RecordingFileInput.key).map { ($0 as NSString).lastPathComponent },
                                      locale: entry.locale, transcription: attendance?.transcription?.rawValue,
                                      service: MeetingTranscription.shared.service, role: attendance?.role.rawValue)
        log.notice("tape: recording meeting \(entry.id.uuidString, privacy: .public)")
    }

    /// Stop: the length, the phone's own lines and the title now; then the minutes.
    private func stop(_ id: UUID) {
        stopped = true
        guard var tape = tapes[id] else { return }
        tape.endedAfter = Date().timeIntervalSince(began)
        tape.duration = RecordingLibrary.shared.entry(id)?.duration ?? RecordingCapture.shared.elapsed
        tapes[id] = tape
        Task { @MainActor in
            // The last segment's lines are saved a moment after Stop.
            try? await Task.sleep(for: .seconds(5))
            self.finish(id)
            await self.readMinutes(id)
        }
    }

    private func finish(_ id: UUID) {
        guard var tape = tapes[id], let entry = RecordingLibrary.shared.entry(id) else { return }
        tape.finalTitle = entry.displayTitle
        let lines = entry.sentences
        tape.phoneLines = lines.isEmpty ? nil : lines
        tapes[id] = tape
        write(id)
    }

    /// The tapes of earlier runs that ended without their minutes: read from the hub as at Stop.
    private func resumeWaiting() async {
        let folder = MeetingTape.folder
        let pending = await Task.detached(priority: .utility) { () -> [(UUID, MeetingTape)] in
            let files = (try? FileManager.default.contentsOfDirectory(at: folder, includingPropertiesForKeys: nil)) ?? []
            return files.compactMap { url in
                guard let id = UUID(uuidString: url.deletingPathExtension().lastPathComponent),
                      let tape = try? MeetingTape.decode(Data(contentsOf: url)),
                      tape.endedAfter != nil, tape.minutes == nil, tape.minutesState != "failed" else { return nil }
                return (id, tape)
            }
        }.value
        for (id, tape) in pending where tapes[id] == nil {
            tapes[id] = tape
            log.notice("tape: waiting on for the minutes of \(id.uuidString, privacy: .public)")
            Task { @MainActor in await self.readMinutes(id) }
        }
    }

    private func readMinutes(_ id: UUID) async {
        let deadline = Date().addingTimeInterval(20 * 60)
        while Date() < deadline {
            try? await Task.sleep(for: .seconds(15))
            guard let api = AppModel.shared.api, let answer = try? await RecordingMinutesModel.fetch(id, api), var tape = tapes[id] else { continue }
            if let entry = RecordingLibrary.shared.entry(id) {
                tape.finalTitle = entry.displayTitle
                if tape.phoneLines == nil, !entry.sentences.isEmpty { tape.phoneLines = entry.sentences }
            }
            tape.minutesState = answer.detail.meta?.state
            if let name = answer.detail.meta?.name, !name.isEmpty { tape.finalTitle = name }
            if let minutes = answer.detail.minutes, tape.minutesState == "ready" || !minutes.isEmpty {
                tape.minutes = minutes
                tape.minutesAfter = Date().timeIntervalSince(tape.startedAt)
                tapes[id] = tape
                write(id)
                log.notice("tape: the minutes came")
                return
            }
            tapes[id] = tape
            write(id)
            if tape.minutesState == "failed" { return }
        }
        log.notice("tape: no minutes within 20 minutes")
    }

    private func scheduleWrite() {
        guard writeDue == nil else { return }
        writeDue = Task { @MainActor in
            try? await Task.sleep(for: .seconds(1))
            self.writeDue = nil
            if let id = self.id { self.write(id) }
        }
    }

    private func write(_ id: UUID) {
        guard let tape = tapes[id], let data = try? tape.encoded() else { return }
        let url = MeetingTape.folder.appending(path: "\(id.uuidString).json")
        Task.detached(priority: .utility) {
            try? FileManager.default.createDirectory(at: url.deletingLastPathComponent(), withIntermediateDirectories: true)
            try? data.write(to: url, options: .atomic)
        }
    }
}
#endif
#endif
