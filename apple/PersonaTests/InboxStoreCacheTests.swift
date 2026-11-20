import Foundation
import Testing
@testable import Persona
import PersonaWire

/// The disk record keeps a cursor only with the exact hub list it names; anything uncertain reads whole after a launch.
@MainActor
struct InboxStoreCacheTests {
    private func item(_ id: String, at: TimeInterval) throws -> InboxItem {
        let json = """
        {"id":"\(id)","source":"mail","kind":"mail","account":"a","account_name":"A","title":"\(id)","preview":"","count":1,
         "updated_at":"\(ISO8601DateFormatter().string(from: Date(timeIntervalSince1970: at)))","manager":"assistant"}
        """
        return try JSON.decoder.decode(InboxItem.self, from: Data(json.utf8))
    }

    @Test func recordRoundTripsItemsCursorAndHub() throws {
        let hub = try #require(URL(string: "https://hub-one.example.com"))
        let items = [try item("a", at: 200), try item("b", at: 100)]
        let saved = InboxStore.Saved(items: items, cursor: "m1.abc", hub: hub)
        let restored = try JSON.decoder.decode(InboxStore.Saved.self, from: JSON.encoder.encode(saved))
        #expect(restored.items == items)
        #expect(restored.cursor == "m1.abc")
        #expect(restored.hub == hub)
        #expect(restored.storedCursor?.value == "m1.abc")
        #expect(restored.storedCursor?.hub == hub)
    }

    @Test func legacyBareArrayLoadsWithoutACursor() async throws {
        let folder = FileManager.default.temporaryDirectory.appending(path: UUID().uuidString)
        defer { try? FileManager.default.removeItem(at: folder) }
        let items = [try item("legacy", at: 100)]
        StateCache.save("inbox-items", items, in: folder)
        for _ in 0..<100 {
            if StateCache.load("inbox-items", as: [InboxItem].self, in: folder) != nil { break }
            try await Task.sleep(for: .milliseconds(10))
        }
        #expect(StateCache.load("inbox-items", as: [InboxItem].self, in: folder) == items)
        let restored = InboxStore.loadSaved(in: folder)
        #expect(restored.items == items)
        #expect(restored.cursor == nil && restored.hub == nil)
        #expect(restored.storedCursor == nil)
    }

    @Test func localEditsAndPendingMarksWithholdTheCursor() throws {
        let hub = try #require(URL(string: "https://hub-one.example.com"))
        let cursor = (value: "m1.clean", hub: hub)
        let items = [try item("local", at: 100)]
        let clean = InboxStore.makeSaved(items: items, cursor: cursor, cursorRevision: 7,
                                         itemsRevision: 7, marksEmpty: true)
        #expect(clean.cursor == cursor.value && clean.hub == hub)
        let edited = InboxStore.makeSaved(items: items, cursor: cursor, cursorRevision: 7,
                                          itemsRevision: 8, marksEmpty: true)
        #expect(edited.items == items)
        #expect(edited.cursor == nil && edited.hub == nil)
        let marked = InboxStore.makeSaved(items: items, cursor: cursor, cursorRevision: 7,
                                          itemsRevision: 7, marksEmpty: false)
        #expect(marked.cursor == nil && marked.hub == nil)
        let empty = InboxStore.makeSaved(items: [], cursor: cursor, cursorRevision: 0,
                                         itemsRevision: 0, marksEmpty: true)
        #expect(empty.cursor == nil && empty.hub == nil)
        #expect(InboxStore.Saved(items: items, cursor: "incomplete", hub: nil).storedCursor == nil)
    }

    @Test func aDeltaCannotCertifyADirtyOrChangedBase() {
        #expect(InboxStore.listingIsCleanForDisk(isDelta: true, baseWasClean: true,
                                                 baseUnchanged: true, marksEmpty: true))
        #expect(!InboxStore.listingIsCleanForDisk(isDelta: true, baseWasClean: false,
                                                  baseUnchanged: true, marksEmpty: true))
        #expect(!InboxStore.listingIsCleanForDisk(isDelta: true, baseWasClean: true,
                                                  baseUnchanged: false, marksEmpty: true))
        #expect(!InboxStore.listingIsCleanForDisk(isDelta: true, baseWasClean: true,
                                                  baseUnchanged: true, marksEmpty: false))
        #expect(InboxStore.listingIsCleanForDisk(isDelta: false, baseWasClean: false,
                                                 baseUnchanged: false, marksEmpty: true))
    }

    @Test func aSavedCursorIsUsedOnlyForItsOwnHub() throws {
        let one = try #require(URL(string: "https://hub-one.example.com"))
        let two = try #require(URL(string: "https://hub-two.example.com"))
        let saved = InboxStore.Saved(items: [try item("a", at: 100)], cursor: "m1.private", hub: one)
        #expect(InboxStore.since(cursor: saved.storedCursor, for: one) == "m1.private")
        #expect(InboxStore.since(cursor: saved.storedCursor, for: two) == InboxListing.noCursor)
    }
}
