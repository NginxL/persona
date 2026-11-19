import Foundation
import Testing
@testable import Persona
import PersonaWire

/// One home for meetings (#8731, "S8"): Knowledge lists recordings under Meetings, beside the meetings imported from
/// connected apps, newest first; notes and all-day recordings carry a pill; recordings have no chip of their own.
@MainActor
struct KnowledgeMeetingsListTests {
    private func entry(_ kind: KBKind, _ id: String, updated: Date? = nil, recordingKind: String? = nil) -> KBEntry {
        KBEntry(kind: kind.rawValue, id: id, name: id, relevance: 0, updated: updated, recordingKind: recordingKind)
    }

    @Test func recordingsHaveNoChipAndOpenAsMeetings() {
        #expect(!KBKind.listed.contains(.recordings))
        #expect(KBKind.listed.contains(.meetings))
        #expect(KBKind.recordings.listedAs == .meetings)
        #expect(KBKind.people.listedAs == .people)
    }

    @Test func theListsMergeWithEachTagOnce() {
        let meetings = KBList(items: [entry(.meetings, "m1")], tags: ["周会", "客户"])
        let recordings = KBList(items: [entry(.recordings, "r1")], tags: ["周会", "旅行"])
        let merged = KBList.meetings(meetings, recordings: recordings)
        #expect(merged.items.map(\.id) == ["m1", "r1"])
        #expect(merged.tags == ["周会", "客户", "旅行"])
        #expect(KBList.meetings(KBList(items: []), recordings: KBList(items: [])).tags == [])
    }

    /// A meeting both recorded and imported is the recording's one row, carrying the imported meeting (#8872); a
    /// recording naming a meeting the list doesn't have stays as it is; two recordings of one meeting both stay.
    @Test func aMeetingBothImportedAndRecordedIsOneRow() {
        var lark = entry(.meetings, "m1")
        lark.source = "lark"
        lark.extra = " https://meetings.example.com/m1 "
        var r1 = entry(.recordings, "r1")
        r1.meetingID = "m1"
        var r2 = entry(.recordings, "r2")
        r2.meetingID = "elsewhere"
        let merged = KBList.meetings(KBList(items: [lark, entry(.meetings, "m2")]), recordings: KBList(items: [r1, r2, entry(.recordings, "r3")]))
        #expect(merged.items.map(\.id) == ["m2", "r1", "r2", "r3"])
        #expect(merged.items.first { $0.id == "r1" }?.imported == KBImportedMeeting(id: "m1", url: URL(string: "https://meetings.example.com/m1"), source: "lark"))
        #expect(merged.items.first { $0.id == "r2" }?.imported == nil)
        var r4 = entry(.recordings, "r4")
        r4.meetingID = "m1"
        #expect(KBList.meetings(KBList(items: [lark]), recordings: KBList(items: [r1, r4])).items.map(\.id) == ["r1", "r4"])
    }

    /// The app it came from (#8872): Lark's mark and name for its connectors; a plain link and "Original" otherwise.
    @Test func theImportedMeetingsAppAndItsNotes() {
        let lark = KBImportedMeeting(id: "m1", url: nil, source: "lark")
        #expect(lark.brand == Theme.Brand.lark && KBImportedMeeting(id: "m", url: nil, source: "larkcli").brand == Theme.Brand.lark)
        #expect(lark.notesTitle == String(localized: "Lark Meeting Notes [Imported Meeting]"))
        let other = KBImportedMeeting(id: "m2", url: nil, source: "another-app")
        #expect(other.brand == nil && other.notesTitle == String(localized: "Original Meeting Notes [Imported Meeting]"))
        #expect(KBImportedMeeting(id: "m3", url: nil, source: nil).brand == nil)
    }

    /// A recording's page finds its imported meeting in its detail (#8872): `meta.imported_meeting` and the link the hub
    /// resolved for it, on Knowledge's page and the meeting's own (the minutes' read); nothing to open, none.
    @Test func aRecordingsDetailNamesItsImportedMeeting() throws {
        func detail(_ meta: String, _ links: String) -> String {
            #"{"kind":"recordings","id":"r1","path":"","meta":"# + meta + #","links":"# + links + "}"
        }
        let link = #"[{"kind":"meetings","id":"m1","name":"周会","relevance":0,"extra":"https://meetings.example.com/m1","source":"lark"}]"#
        let named = #"{"imported_meeting":{"kind":"meetings","id":"m1","name":"周会"}}"#
        let want = KBImportedMeeting(id: "m1", url: URL(string: "https://meetings.example.com/m1"), source: "lark")
        #expect(try JSONDecoder().decode(KBDetail.self, from: Data(detail(named, link).utf8)).importedMeeting == want)
        #expect(try JSONDecoder().decode(KBDetail.self, from: Data(detail("{}", link).utf8)).importedMeeting == nil)
        #expect(try JSONDecoder().decode(KBDetail.self, from: Data(detail(named, "[]").utf8)).importedMeeting == nil)
        let noURL = #"[{"kind":"meetings","id":"m1","name":"周会","relevance":0}]"#
        #expect(try JSONDecoder().decode(KBDetail.self, from: Data(detail(named, noURL).utf8)).importedMeeting == nil)
        let answer = try JSONDecoder().decode(RecordingMinutesModel.Answer.self, from: Data((#"{"detail":"# + detail(named, link) + "}").utf8))
        #expect(answer.detail.imported == want)
        // Links this app can't read leave the minutes' answer readable, without the imported meeting.
        let odd = try JSONDecoder().decode(RecordingMinutesModel.Answer.self, from: Data((#"{"detail":"# + detail(named, #"[{"kind":1}]"#) + "}").utf8))
        #expect(odd.detail.imported == nil)
    }

    @Test func newestFirstWithUndatedLastInTheirOrder() {
        let old = Date(timeIntervalSince1970: 1_700_000_000), new = old.addingTimeInterval(86_400)
        let list = [entry(.meetings, "undated-a"), entry(.meetings, "old", updated: old), entry(.recordings, "new", updated: new),
                    entry(.meetings, "undated-b")]
        #expect(KBList.newestFirst(list).map(\.id) == ["new", "old", "undated-a", "undated-b"])
    }

    @Test func onlyNotesAndAllDayRecordingsCarryAPill() {
        #expect(entry(.recordings, "n", recordingKind: "note").recordingTag == String(localized: "Note [Recording Kind]"))
        #expect(entry(.recordings, "d", recordingKind: "day").recordingTag == String(localized: "All-Day"))
        #expect(entry(.recordings, "m", recordingKind: "meeting").recordingTag == nil)
        #expect(entry(.recordings, "older-hub").recordingTag == nil)
        #expect(entry(.meetings, "imported", recordingKind: "note").recordingTag == nil)
    }

    @Test func theWireKindDecodesAndALocalRecordingIsItsID() throws {
        let id = "0a1b2c3d-0000-4000-8000-000000000001"
        let json = #"{"kind":"recordings","id":"\#(id)","name":"周会复盘","relevance":0,"recording_kind":"note"}"#
        let e = try JSON.decoder.decode(KBEntry.self, from: Data(json.utf8))
        #expect(e.recordingKind == "note")
        #expect(e.localRecording == UUID(uuidString: id))
        #expect(entry(.meetings, id).localRecording == nil)
    }
}
