import Foundation
import Testing
@testable import Persona

/// Replaying a meeting tape on any build (#10589, docs/apple/recording.md "A meeting's tape and its replay"): the replayed
/// meeting (`ReplayedMeeting`) never reaches a hub. Every request that names it ends in `APIClient` before anything is
/// sent, and each place that would ask the hub about it refuses first (Private Meeting, Just Me, the follow-up, Hand to
/// Agent, photos, Add to To-Dos, Regenerate, Ask); the room offers none of them (`MeetingRoomOffers`). Sync's own guards
/// are in `RecordingSyncTests`. Also the tape's clock (speed, pause, the audio's time), the tape's minutes without their
/// version, and the tapes kept on this phone (`MeetingTapeLibrary`).
@MainActor struct MeetingReplayGuardTests {
    private let replayed = ReplayedMeeting.id
    private var path: String { "/api/v1/recordings/\(replayed.uuidString.lowercased())" }

    private func entry(_ id: UUID = ReplayedMeeting.id, active: Bool = true) -> RecordingEntry {
        var e = RecordingEntry(id: id, createdAt: .now, source: .phone, title: "Weekly sync", active: active)
        e.meeting = RecordingMeeting(eventID: "replay", title: "Weekly sync", start: .now, end: .now.addingTimeInterval(60), location: nil, attendees: [])
        e.attendance = RecordingAttendance.room(.listener, private: false, photos: false, liveView: true, highlights: true)
        return e
    }

    private func refused(_ error: Error) -> Bool { (error as? APIError)?.code == ReplayedMeeting.refusal }

    // MARK: The client

    @Test func everyRequestNamingTheReplayedMeetingStaysOnThePhone() async throws {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/recordings", json: #"{"recordings": []}"#)
        let client = hub.client
        await #expect(throws: APIError.self) { _ = try await client.request("GET", path + "/live") }
        await #expect(throws: APIError.self) { _ = try await client.request("PUT", path.uppercased().replacingOccurrences(of: "/API/V1/RECORDINGS", with: "/api/v1/recordings")) }
        await #expect(throws: APIError.self) { _ = try await client.request("GET", "/api/v1/kb/recordings/\(replayed.uuidString.lowercased())") }
        await #expect(throws: APIError.self) { _ = try await client.request("GET", "/api/v1/recordings", query: ["recording": replayed.uuidString]) }
        await #expect(throws: APIError.self) { _ = try await client.putBytes(path + "/audio", data: Data([1, 2])) }
        let file = FileManager.default.temporaryDirectory.appending(path: "replay-guard-\(UUID().uuidString)")
        defer { try? FileManager.default.removeItem(at: file) }
        await #expect(throws: APIError.self) { try await client.download(path + "/audio", query: [:], to: file) { _, _ in } }
        do {
            _ = try await client.request("DELETE", path)
            Issue.record("sent")
        } catch {
            #expect(refused(error))
        }
        #expect(hub.requests.isEmpty, "\(hub.requests.map(\.path))")
        // Any other recording goes as before.
        _ = try await client.request("GET", "/api/v1/recordings")
        #expect(hub.requests.count == 1)
    }

    @Test func onlyThePathsAndQueriesThatNameItAreRefused() {
        #expect(ReplayedMeeting.refuses("/api/v1/recordings/\(ReplayedMeeting.id.uuidString.lowercased())/ask"))
        #expect(ReplayedMeeting.refuses("/api/v1/recordings/\(ReplayedMeeting.id.uuidString)"))
        #expect(ReplayedMeeting.refuses("/api/v1/inbox", query: ["recording": ReplayedMeeting.id.uuidString.lowercased()]))
        #expect(!ReplayedMeeting.refuses("/api/v1/recordings/\(UUID().uuidString.lowercased())"))
        #expect(!ReplayedMeeting.refuses("/api/v1/recordings"))
    }

    // MARK: Each place that would ask the hub

    private func conversation(_ hub: FakeHub) -> MeetingConversation {
        let client = hub.client
        return MeetingConversation(deps: MeetingConversation.Deps(api: { client }, group: { "" }, now: { .now }, wait: { _ in },
                                                                  reupload: { _ in Issue.record("re-upload asked"); return false },
                                                                  readPhotos: { _ in false }))
    }

    @Test func privateMeetingJustMeAndTheFollowUpAreRefused() async {
        let hub = FakeHub()
        let c = conversation(hub)
        do {
            _ = try await c.setPrivate(replayed, true)
            Issue.record("private was sent")
        } catch {
            #expect(refused(error))
        }
        #expect(c.sendJustMe(entry(), text: "Only me") == nil)
        #expect(c.sendFollowup(replayed, text: "What was decided?") == nil)
        await c.refresh(replayed)
        c.watch(replayed, by: UUID())
        try? await Task.sleep(for: .milliseconds(50))
        #expect(hub.requests.isEmpty, "\(hub.requests.map(\.path))")
    }

    @Test func handToAgentIsRefused() async {
        let hub = FakeHub()
        do {
            _ = try await MeetingHandoff.send(replayed, MeetingHandoff.Body(machineID: "m", sessionID: "s", title: "Weekly sync", text: "", transcript: false), api: hub.client)
            Issue.record("the hand-off was sent")
        } catch {
            #expect(refused(error))
        }
        #expect(hub.requests.isEmpty)
    }

    @Test func photosAreNeverReadOrSent() async throws {
        let hub = FakeHub()
        let root = FileManager.default.temporaryDirectory.appending(path: "replay-photos-\(UUID().uuidString)")
        defer { try? FileManager.default.removeItem(at: root) }
        let client = hub.client
        let photos = MeetingPhotos(deps: MeetingPhotos.Deps(api: { client }, folder: { root.appending(path: $0.uuidString) },
                                                            prepare: { _, _ in nil }, now: { .now }, wait: { _ in },
                                                            reupload: { _ in false }, recordings: { [] }))
        #expect(await photos.refresh(replayed) == false)
        await #expect(throws: APIError.self) { try await photos.delete(replayed, n: 1) }
        #expect(hub.requests.isEmpty, "\(hub.requests.map(\.path))")
    }

    @Test func addToToDosAndRegenerateAreNeverSent() async {
        let hub = FakeHub()
        let model = RecordingMinutesModel(api: { hub.client })
        await model.regenerate(replayed)
        await model.add(["a1"], to: entry(active: false), synced: true)
        #expect(hub.requests.isEmpty, "\(hub.requests.map(\.path))")
    }

    @Test func personaCanNeverBeAskedAboutIt() {
        #expect(!MeetingAsk.canAsk(entry(), roomMissing: false))
        #expect(MeetingAsk.canAsk(entry(UUID()), roomMissing: false))
    }

    // MARK: What the room offers

    @Test func theRoomOffersTheReplayNothingThatAsksTheHub() {
        let replay = MeetingRoomOffers.of(replayed)
        #expect(replay == .replay)
        #expect(!replay.ask && !replay.justMe && !replay.bookmark && !replay.photos && !replay.persona)
        #expect(!replay.pageMenu && !replay.privateMeeting && !replay.handOff && !replay.followup)
        #expect(replay.replayClock)
        let meeting = MeetingRoomOffers.of(UUID())
        #expect(meeting == .meeting)
        #expect(meeting.ask && meeting.justMe && meeting.bookmark && meeting.photos && meeting.persona)
        #expect(meeting.pageMenu && meeting.privateMeeting && meeting.handOff && meeting.followup)
        #expect(!meeting.replayClock)
    }

    /// The tape's minutes without their version: the hub has none such, so Add to To-Dos is never offered.
    @Test func theTapesMinutesComeWithoutTheirVersion() throws {
        var minutes = try #require(MeetingTapeSample.tape.minutes)
        minutes.version = 4
        let shown = try #require(MeetingReplay.shown(minutes))
        #expect(shown.version == nil)
        #expect(shown.overview == minutes.overview)
        #expect(MeetingReplay.shown(nil) == nil)
        #expect(MeetingReplay.minutes(for: UUID()) == nil, "only the replayed meeting's")
    }

    // MARK: The tape's clock

    private func events() -> [MeetingTape.Event] {
        [MeetingTape.Event(kind: .interim, t: 1, start: 0.5, text: "We start"),
         MeetingTape.Event(kind: .line, t: 3, n: 0, start: 0.5, end: 2.5, text: "We start now."),
         MeetingTape.Event(kind: .topics, t: 5, topics: []),
         MeetingTape.Event(kind: .line, t: 9, n: 1, start: 6, end: 8.5, text: "Release on Wednesday.")]
    }

    @Test func theClockRunsAtTheSpeedAndAppliesWhatCameByThen() {
        var clock = MeetingReplayClock(schedule: events(), length: 10)
        clock.advance(by: 1, speed: 2)
        #expect(clock.position == 2)
        var changed = clock.take()
        #expect(changed.hub && !changed.room)
        #expect(clock.frame.interim?.text == "We start")
        #expect(clock.frame.lines.isEmpty)
        // Faster from here on: 4× for a second is 4 seconds of the tape.
        clock.advance(by: 1, speed: 4)
        #expect(clock.position == 6)
        changed = clock.take()
        #expect(changed.hub && changed.room)
        #expect(clock.frame.lines.map(\.text) == ["We start now."])
        #expect(clock.frame.interim == nil)
        #expect(!clock.atEnd)
        // Nothing new: nothing changed.
        changed = clock.take()
        #expect(!changed.hub && !changed.room)
    }

    @Test func theClockFollowsTheAudioNeverBackAndStopsAtTheEnd() {
        var clock = MeetingReplayClock(schedule: events(), length: 10)
        clock.move(to: 4)
        #expect(clock.position == 4)
        clock.move(to: 3) // the audio restarted a little behind: the tape never goes back
        #expect(clock.position == 4)
        clock.advance(by: 0, speed: 1) // paused: no time went by on the tape
        clock.advance(by: -1, speed: 1)
        clock.advance(by: .nan, speed: 1)
        #expect(clock.position == 4)
        clock.advance(by: 100, speed: 1)
        #expect(clock.position == 10)
        _ = clock.take()
        #expect(clock.atEnd)
        #expect(clock.frame.lines.count == 2)
    }

    @Test func theTimeReadsAsWhereTheTapeIsOfItsLength() {
        #expect(MeetingReplayStateLine.clock(83, of: 226) == "1:23 / 3:46")
        #expect(MeetingReplayStateLine.clock(300, of: 226) == "3:46 / 3:46")
        #expect(MeetingReplayStateLine.clock(-1, of: 59) == "0:00 / 0:59")
        #expect(MeetingReplayStateLine.clock(3700, of: 7200) == "1:01:40 / 2:00:00")
        #expect(MeetingTapesPage.speedLabel(2) == "2×")
    }

    // MARK: The tapes on this phone

    private func tapeFolder(audio: Bool) throws -> URL {
        let folder = FileManager.default.temporaryDirectory.appending(path: "replay-src-\(UUID().uuidString)/weekly-sync")
        try FileManager.default.createDirectory(at: folder, withIntermediateDirectories: true)
        var tape = MeetingTapeSample.tape
        tape.audio = audio ? "audio.m4a" : nil
        try tape.encoded().write(to: folder.appending(path: "tape.json"))
        if audio { try Data([0, 1, 2, 3]).write(to: folder.appending(path: "audio.m4a")) }
        return folder
    }

    @Test func aTapesFolderIsKeptWithItsAudioOutOfBackups() throws {
        let root = FileManager.default.temporaryDirectory.appending(path: "replay-lib-\(UUID().uuidString)")
        let source = try tapeFolder(audio: true)
        defer {
            try? FileManager.default.removeItem(at: root)
            try? FileManager.default.removeItem(at: source.deletingLastPathComponent())
        }
        let kept = try MeetingTapeLibrary.importing([source], into: root)
        #expect(kept.lastPathComponent.hasPrefix("weekly-sync-"))
        #expect(FileManager.default.fileExists(atPath: kept.appending(path: "audio.m4a").path))
        #expect(try root.resourceValues(forKeys: [.isExcludedFromBackupKey]).isExcludedFromBackup == true)
        let tapes = MeetingTapeLibrary.read(root)
        #expect(tapes.count == 1)
        #expect(tapes.first?.summary.title == MeetingTapeSample.tape.title)
        #expect(tapes.first?.summary.lines == Set(MeetingTapeSample.tape.events.compactMap { $0.kind == .line ? $0.n : nil }).count)
        #expect(tapes.first?.audio?.lastPathComponent == "audio.m4a")
    }

    @Test func aTapeAndItsAudioPickedTogetherAreKept() throws {
        let root = FileManager.default.temporaryDirectory.appending(path: "replay-lib-\(UUID().uuidString)")
        let source = try tapeFolder(audio: true)
        defer {
            try? FileManager.default.removeItem(at: root)
            try? FileManager.default.removeItem(at: source.deletingLastPathComponent())
        }
        // Picked as files: the audio under another name, which the tape then names.
        let renamed = source.appending(path: "recording.m4a")
        try FileManager.default.moveItem(at: source.appending(path: "audio.m4a"), to: renamed)
        let kept = try MeetingTapeLibrary.importing([source.appending(path: "tape.json"), renamed], into: root)
        let tape = try MeetingTape.decode(Data(contentsOf: kept.appending(path: "tape.json")))
        #expect(tape.audio == "audio.m4a")
        #expect(FileManager.default.fileExists(atPath: kept.appending(path: "audio.m4a").path))
    }

    @Test func whatIsNoTapeIsRefused() throws {
        let root = FileManager.default.temporaryDirectory.appending(path: "replay-lib-\(UUID().uuidString)")
        let other = FileManager.default.temporaryDirectory.appending(path: "replay-other-\(UUID().uuidString).json")
        try Data(#"{"format": "something-else"}"#.utf8).write(to: other)
        defer {
            try? FileManager.default.removeItem(at: root)
            try? FileManager.default.removeItem(at: other)
        }
        #expect(throws: MeetingTapeLibrary.ImportError.noTape) { try MeetingTapeLibrary.importing([other], into: root) }
        #expect(MeetingTapeLibrary.read(root).isEmpty)
    }

    @Test func turningTheSwitchOffDeletesEveryTape() async throws {
        let root = FileManager.default.temporaryDirectory.appending(path: "replay-lib-\(UUID().uuidString)")
        let source = try tapeFolder(audio: false)
        defer { try? FileManager.default.removeItem(at: source.deletingLastPathComponent()) }
        _ = try MeetingTapeLibrary.importing([source], into: root)
        let library = MeetingTapeLibrary(root: root)
        await library.refresh()
        #expect(library.hasTapes)
        library.deleteAll()
        #expect(!library.hasTapes)
        for _ in 0..<100 where FileManager.default.fileExists(atPath: root.path) { try await Task.sleep(for: .milliseconds(20)) }
        #expect(!FileManager.default.fileExists(atPath: root.path))
    }
}
