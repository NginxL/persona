import Foundation
import Testing
@testable import Persona
import PersonaWire

/// The owner's messages the assistant keeps after they could not go in (#10194): the row carries them, a kept send
/// leaves the outbox for the row, and Retry / Delete reach the hub.
@MainActor
struct UndeliveredTests {
    private let ref = MachineRef(group: "", id: "m")
    private let key = SessionKey(machineID: "m", sessionID: "s")

    private func session(_ extra: String) throws -> Session {
        let json = #"{"machine_id":"m","session_id":"s","title":"t","cwd":"/tmp/p","status":"offline","state":"offline","assistant":true"# + extra + "}"
        return try JSON.decoder.decode(Session.self, from: Data(json.utf8))
    }

    @Test func theRowCarriesKeptMessages() throws {
        let row = try session(#","undelivered":[{"id":"k1","text":"hello","images":1,"at":"2026-11-17T10:00:00Z","state":"waiting","attempts":2,"next_at":"2026-11-17T10:01:00Z","error":"The assistant could not start"},{"id":"k2","text":"later","at":"2026-11-17T10:02:00Z","state":"failed"}]"#)
        let kept = try #require(row.undelivered)
        #expect(kept.map(\.id) == ["k1", "k2"])
        #expect(kept[0].images == 1 && kept[0].nextAt != nil && !kept[0].gaveUp)
        #expect(kept[1].gaveUp)
        #expect(try session("").undelivered == nil, "an older hub sends none")
        #expect(try session(#","undelivered":"broken""#).undelivered == nil, "a malformed list never hides the row")
    }

    /// Both of the hub's names read and write (#10966): `next_at` (when it is tried again) and `delete_only` (a message
    /// from a signed-out device: Delete, no Retry), once two enums each knew only one of them.
    @Test func nextAtAndDeleteOnlyRoundTrip() throws {
        let json = #"{"id":"k3","text":"from the old phone","at":"2026-11-17T10:00:00Z","state":"failed","attempts":3,"next_at":"2026-11-17T10:05:00Z","delete_only":true}"#
        let kept = try JSON.decoder.decode(UndeliveredMessage.self, from: Data(json.utf8))
        #expect(kept.nextAt == ISO8601DateFormatter().date(from: "2026-11-17T10:05:00Z"))
        #expect(kept.deleteOnly == true && kept.gaveUp && !kept.canRetry)
        let written = try JSONSerialization.jsonObject(with: JSON.encoder.encode(kept)) as? [String: Any]
        #expect(written?["delete_only"] as? Bool == true)
        #expect(written?["next_at"] != nil && written?["nextAt"] == nil && written?["deleteOnly"] == nil)
        let again = try JSON.decoder.decode(UndeliveredMessage.self, from: JSON.encoder.encode(kept))
        #expect(again.deleteOnly == true && again.attempts == 3)
        let retryable = try JSON.decoder.decode(UndeliveredMessage.self, from: Data(#"{"id":"k4","text":"t","at":"2026-11-17T10:00:00Z","state":"failed"}"#.utf8))
        #expect(retryable.deleteOnly == nil && retryable.nextAt == nil && retryable.canRetry)
    }

    @Test func aKeptSendLeavesTheOutboxForTheRow() async throws {
        let hub = FakeHub()
        hub.on("POST", "/api/v1/sessions/m/s/messages",
               json: #"{"delivered":"later","undelivered":{"id":"x","text":"hello","at":"2026-11-17T10:00:00Z","state":"waiting","error":"The assistant could not start"}}"#)
        let model = AppModel()
        model.apiForTests = hub.client
        model.applySessions([ref: [try session("")]])
        let msg = PendingMessage(text: "hello", images: [])
        model.chat(key).pending = [msg]
        model.chat(key).beginSendFeedback(msg)
        let outcome = await model.sendPending(key, msg)
        model.settleSend(key, msg.id, outcome)
        #expect(outcome == .kept)
        #expect(model.chat(key).pending.isEmpty, "the row draws it from now on")
        #expect(model.session(key)?.undelivered?.map(\.id) == ["x"])
        #expect(model.chat(key).sendFeedback?.phase == .finished, "no Processing left hanging while nothing runs")
    }

    @Test func aKeptHandOffIsNotFiledAsDone() async throws {
        let hub = FakeHub()
        hub.on("POST", "/api/v1/sessions/m/s/messages", json: #"{"delivered":"later","undelivered":{"id":"x","text":"context","at":"2026-11-17T10:00:00Z","state":"waiting"}}"#)
        let model = AppModel()
        model.apiForTests = hub.client
        model.applySessions([ref: [try session("")]])
        var filings: [PendingMessage.InboxHandOff] = []
        model.inboxHandOffDoneForTests = { filings.append($0) }
        let item = try JSON.decoder.decode(InboxItem.self, from: Data(#"{"id":"mail:a:1","source":"mail","kind":"mail","account":"a","account_name":"A","title":"t","preview":"p","count":1,"manager":"mgr"}"#.utf8))
        let result = await model.sendForHandOff(key, text: "context", item: item, file: true)
        #expect(result.delivered && result.kept && !result.filed)
        #expect(filings.isEmpty, "a message the agent has not got yet does not file the item")
    }

    @Test func shownLeavesOutWhatTheTranscriptOrThisPhoneHas() {
        let at = Date(timeIntervalSince1970: 0)
        let list = ["A", "b", "c"].map { UndeliveredMessage(id: $0, text: $0, at: at, state: "waiting") }
        #expect(UndeliveredMessage.shown(list, landed: ["a"], drawn: ["B"]).map(\.id) == ["c"])
        #expect(UndeliveredMessage.shown(nil, landed: [], drawn: []).isEmpty)
    }

    @Test func retryAndDeleteReachTheHub() async throws {
        let hub = FakeHub()
        hub.on("POST", "/api/v1/assistant/conversations/s/undelivered/k2/retry", json: #"{"ok":true}"#)
        hub.on("DELETE", "/api/v1/assistant/conversations/s/undelivered/k2", json: #"{"ok":true}"#)
        let model = AppModel()
        model.apiForTests = hub.client
        model.applySessions([ref: [try session(#","undelivered":[{"id":"k2","text":"later","at":"2026-11-17T10:02:00Z","state":"failed","error":"gone"}]"#)]])
        await model.retryUndelivered(key, "k2")
        #expect(hub.sent("POST", "/api/v1/assistant/conversations/s/undelivered/k2/retry").count == 1)
        #expect(model.session(key)?.undelivered?.first?.gaveUp == false)
        await model.deleteUndelivered(key, "k2")
        #expect(hub.sent("DELETE", "/api/v1/assistant/conversations/s/undelivered/k2").count == 1)
        #expect(model.session(key)?.undelivered?.isEmpty == true)
    }
}
