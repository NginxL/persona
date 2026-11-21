import Foundation
import Testing
@testable import Persona

@MainActor
struct StarredMessagesTests {
    private let path = "/api/v1/assistant/starred"
    private let itemPath = "/api/v1/sessions/computer/session/items/line2#0/full"
    private let key = SessionKey(group: "fixture", machineID: "computer", sessionID: "session")
    private let proof = String(repeating: "a", count: 32)

    private func snapshot() -> StarredSnapshot {
        StarredSnapshot(machine_id: "computer", session_id: "session", item_id: "line2#0", conversation: .agent,
                        source_fingerprint: proof, role: "assistant", title: "Review", speaker: "Agent",
                        source_manager: nil, written_at: nil, text: "Saved reply")
    }

    private func ref(_ fingerprint: String, conversation: String = "agent") -> String {
        #"{"id":"saved","machine_id":"computer","session_id":"session","item_id":"line2#0","conversation":"\#(conversation)","source_fingerprint":"\#(fingerprint)"}"#
    }

    private func draft(_ fingerprint: String) -> String {
        String(ref(fingerprint).dropLast()) + #", "role":"assistant","title":"Review","speaker":"Agent","text":"Saved reply"}"#
    }

    private func make(_ hub: FakeHub, toast: @escaping (Toast) -> Void = { _ in }) -> StarredStore {
        let store = StarredStore(deps: .init(api: { hub.client }, report: { _ in }, toast: toast, open: { _ in }))
        store.configure(group: key.group, offered: true)
        return store
    }

    @Test func compactReadLeavesSnapshotTextLazyAndKeepsReadOnlyMarker() async {
        let hub = FakeHub()
        hub.on("GET", path, json: #"{"refs":[\#(ref(proof))],"can_write":false}"#)
        let store = make(hub)
        await store.loadIndex()
        #expect(store.count == 1 && !store.canWrite)
        #expect(store.snapshots.isEmpty)
        #expect(store.mark(snapshot().source, group: key.group) == .saved)
        #expect(store.mark(snapshot().source, group: "other") == nil)
        let replacement = StarredSource(machine: "computer", session: "session", item: "line2#0", conversation: .agent,
                                        fingerprint: String(repeating: "b", count: 32))
        #expect(store.mark(replacement, group: key.group) == nil)
        let assistant = StarredSource(machine: "computer", session: "session", item: "line2#0", conversation: .assistant, fingerprint: proof)
        #expect(store.mark(assistant, group: key.group) == nil)
        #expect(hub.sent("GET", path).allSatisfy { $0.query["index"] == "1" })
    }

    @Test func removedAndUnreadableOriginalsHaveDifferentOutcomes() async {
        let hub = FakeHub(), store = make(FakeHub())
        let original = snapshot().source
        let cases: [(Int, StarredStore.Original)] = [(404, .unavailable), (502, .failed)]
        for (status, expected) in cases {
            hub.on("GET", itemPath) { _ in throw APIError(status: status, message: "fixture") }
            #expect(await StarredStore.readOriginal(original, key: key, api: hub.client) == expected)
        }
        hub.on("GET", itemPath) { _ in
            throw APIError(status: 410, message: "fixture", fields: ["code": "conversation_deleted"])
        }
        #expect(await StarredStore.readOriginal(original, key: key, api: hub.client) == .unavailable)
        hub.on("GET", itemPath, json: #"{"id":"line2#0","seq":2,"role":"assistant","kind":"text","text":"fixture","source_fingerprint":"\#(proof)"}"#)
        #expect(await StarredStore.readOriginal(original, key: key, api: hub.client) == .verified)
        #expect(await store.verify(original, key: SessionKey(group: "other", machineID: "computer", sessionID: "session")) == .retired)
    }

    @Test func responseAfterScopeChangesCannotRestoreAnOldMarker() async {
        let hub = FakeHub()
        hub.on("GET", path, json: #"{"refs":[],"can_write":true}"#)
        let store = make(hub)
        await store.loadIndex()
        hub.on("POST", path) { _ in
            #expect(store.mark(snapshot().source, group: key.group) == .saving)
            store.configure(group: "other", offered: false)
            return Data(#"{"star":\#(draft(proof))}"#.utf8)
        }
        await store.add(snapshot())
        #expect(store.count == nil && store.snapshots.isEmpty)
        #expect(store.mark(snapshot().source, group: key.group) == nil)
    }

    @Test func aChangedDefaultFencesMarkersAndUndoAndExplicitlyNamesTheOriginalManager() async {
        let hub = FakeHub()
        var undo: Toast.Action?
        let store = make(hub) { undo = $0.action }
        hub.on("GET", path) { req in
            let refs = req.headers[APIClient.managerHeader] == "previous" ? ref(proof) : ""
            return Data(#"{"refs":[\#(refs)],"can_write":true}"#.utf8)
        }
        hub.on("DELETE", path + "/saved") { req in
            #expect(req.headers[APIClient.managerHeader] == "previous")
            return Data(#"{"removed":\#(draft(proof))}"#.utf8)
        }
        store.configure(group: key.group, offered: true, manager: "previous", defaultManager: "previous")
        await store.loadIndex()
        #expect(store.mark(snapshot().source, group: key.group) == .saved)
        await store.remove(snapshot().source)
        #expect(undo != nil)
        store.configure(group: key.group, offered: true, manager: "replacement", defaultManager: "replacement")
        #expect(!store.canWrite && store.count == nil)
        await store.loadIndex()
        undo?.run()
        await Task.yield()
        #expect(store.count == 0 && hub.sent("POST", path).isEmpty)
    }

    @Test func anUndoQueuedBeforeAnAuthorityChangeCannotRestoreIntoTheNewScope() async throws {
        let hub = FakeHub()
        var toast: Toast?
        var queued: (@MainActor () async -> Void)?
        let store = StarredStore(deps: .init(api: { hub.client }, report: { _ in }, toast: { toast = $0 }, open: { _ in }),
                                 enqueueUndo: { queued = $0 })
        hub.on("GET", path, json: #"{"refs":[\#(ref(proof))],"can_write":true}"#)
        hub.on("DELETE", path + "/saved", json: #"{"removed":\#(draft(proof))}"#)
        let source = snapshot().source
        store.configure(group: key.group, offered: true, manager: "original")
        await store.loadIndex()
        await store.remove(source)
        let undo = try #require(toast?.action)
        undo.run()
        let deferred = try #require(queued)

        hub.on("GET", path, json: #"{"refs":[],"can_write":true}"#)
        store.configure(group: key.group, offered: true, manager: "other")
        await store.loadIndex()
        #expect(store.canWrite)
        await deferred()
        #expect(hub.sent("POST", path).isEmpty)
    }

    @Test func anExactMissingSourceDoesNotReadAPrefixedTranscript() async {
        let hub = FakeHub()
        let source = snapshot().source
        let copy = SessionKey(group: key.group, machineID: key.machineID, sessionID: key.sessionID + "-copy")
        #expect(source.key(group: key.group) == key)
        #expect(await StarredStore.readOriginal(source, key: copy, api: hub.client) == .unavailable)
        #expect(hub.requests.isEmpty)
    }

    @Test func changedCredentialsFenceResponsesOnTheSameGroup() async {
        let hub = FakeHub()
        var active: APIClient? = hub.client
        let store = StarredStore(deps: .init(api: { active }, report: { _ in }, toast: { _ in }, open: { _ in }))
        store.configure(group: "", offered: true)
        hub.on("GET", path) { _ in
            active?.config.token = "replacement"
            return Data(#"{"refs":[\#(ref(proof))],"can_write":true}"#.utf8)
        }
        await store.loadIndex()
        #expect(store.count == nil && !store.canWrite)
        #expect(store.mark(snapshot().source, group: "") == nil)

        store.configure(group: "", offered: true)
        hub.on("GET", path, json: #"{"refs":[\#(ref(proof))],"can_write":true}"#)
        await store.loadIndex()
        #expect(store.count == 1 && store.canWrite)
        active = nil
        store.configure(group: nil, offered: false)
        #expect(store.count == nil && store.snapshots.isEmpty && !store.canWrite)
    }

    @Test func aRouteThatChangesAuthorityCannotSendAnOldStarMutation() async {
        let hub = FakeHub()
        var client = hub.client
        let newConfig = ServerConfig(url: hub.url, token: "replacement")
        client.requestRoute = { newConfig }
        await #expect(throws: CancellationError.self) {
            try await client.pinStarredAuthority().removeStar("saved")
        }
        #expect(hub.requests.isEmpty)
    }

    @Test func boundedTextUsesUnicodeScalarsAndRetainsTheExistingOmissionCount() {
        var value = snapshot()
        value.id = "saved"
        value.text = String(repeating: "🐈", count: 4002)
        value.omitted_chars = 11
        let input = value.bounded()
        #expect(input.id == nil)
        #expect(input.text.unicodeScalars.count == 4000)
        #expect(input.omitted_chars == 13)
    }
}
