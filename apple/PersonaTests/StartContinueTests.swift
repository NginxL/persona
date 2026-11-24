import Foundation
import Testing
@testable import Persona
import PersonaWire

/// A shell startup answer resends only messages the hub definitely refused, in their original order (#10452).
@MainActor
@Suite(.serialized)
struct StartContinueTests {
    private func session(_ extra: String = "") throws -> Session {
        try JSON.decoder.decode(Session.self, from: Data(
            #"{"machine_id":"box","session_id":"s1","status":"idle","state":"idle","pane_id":"p1","capabilities":{"read":true,"send":true}\#(extra)}"#.utf8))
    }

    private func app(_ hub: FakeHub, sessionExtra: String = "") throws -> (AppModel, SessionKey) {
        let app = AppModel(stateFolder: FileManager.default.temporaryDirectory.appending(path: UUID().uuidString))
        app.forgetRestoredForTests()
        app.apiForTests = hub.client
        app.linksUpForTests = true
        let row = try session(sessionExtra)
        app.applySessions([row.key.machineRef: [row]])
        return (app, row.key)
    }

    private func refused(_ text: String, code: String? = TaskStartRemedy.shellWaiting,
                         uncertain: Bool = false, state: PendingMessage.State = .failed) -> PendingMessage {
        var message = PendingMessage(text: text, images: [], state: state)
        message.failure = "start refused"
        message.failureCode = code
        message.refused = true
        message.mayHaveArrived = uncertain ? true : nil
        return message
    }

    private func texts(_ hub: FakeHub, path: String) throws -> [String] {
        try hub.sent("POST", path).map { request in
            let body = try #require(request.body)
            return try #require((try JSONSerialization.jsonObject(with: body) as? [String: Any])?["text"] as? String)
        }
    }

    @Test func oldOutboxesDecodeAndStructuredRefusalsSurvivePersistence() throws {
        let original = refused("kept")
        let data = try JSON.encoder.encode(original)
        var object = try #require(try JSONSerialization.jsonObject(with: data) as? [String: Any])
        object.removeValue(forKey: "failureCode")
        let oldData = try JSONSerialization.data(withJSONObject: object)
        let old = try JSON.decoder.decode(PendingMessage.self, from: oldData)
        #expect(old.failureCode == nil)
        let restored = try JSON.decoder.decode(PendingMessage.self, from: data)
        #expect(restored.failureCode == TaskStartRemedy.shellWaiting)
    }

    @Test func onlyDefiniteShellRefusalsResendInPendingOrderWithFreshIDs() async throws {
        let hub = FakeHub()
        let path = "/api/v1/sessions/box/s1/messages"
        hub.on("POST", path, json: #"{"ok":true}"#)
        let (app, key) = try app(hub)
        let first = refused("first")
        let otherCode = refused("not this start", code: "dialog_open")
        let third = refused("third")
        let uncertain = refused("uncertain", uncertain: true)
        let waiting = refused("still waiting", state: .waiting)
        app.chat(key).pending = [first, otherCode, third, uncertain, waiting]
        defer { app.outboxRetry.values.forEach { $0.cancel() } }

        await app.resendShellWaiting(key)

        #expect(try texts(hub, path: path) == ["first", "third"])
        #expect(app.chat(key).pending.map(\.state) == [.sent, .failed, .sent, .failed, .waiting])
        #expect(app.chat(key).pending[0].id != first.id)
        #expect(app.chat(key).pending[2].id != third.id)
        #expect(app.chat(key).pending[1] == otherCode)
        #expect(app.chat(key).pending[3] == uncertain)
        #expect(app.chat(key).pending[4] == waiting)
    }

    @Test func aRenewedRefusalStopsBeforeLaterContext() async throws {
        let hub = FakeHub()
        let path = "/api/v1/sessions/box/s1/messages"
        var calls = 0
        hub.on("POST", path) { _ in
            calls += 1
            if calls == 1 {
                throw APIError(status: 409, message: "still waiting",
                               fields: ["code": TaskStartRemedy.shellWaiting, "delivered": "none"])
            }
            return Data(#"{"ok":true}"#.utf8)
        }
        let (app, key) = try app(hub)
        let first = refused("first")
        let second = refused("second")
        app.chat(key).pending = [first, second]
        defer { app.outboxRetry.values.forEach { $0.cancel() } }

        await app.resendShellWaiting(key)

        #expect(calls == 1)
        #expect(app.chat(key).pending.count == 2)
        #expect(app.chat(key).pending[0].state == .failed)
        #expect(app.chat(key).pending[0].failureCode == TaskStartRemedy.shellWaiting)
        #expect(app.chat(key).pending[0].id != first.id)
        #expect(app.chat(key).pending[1] == second)
    }

    @Test func continueStartChecksTheFeatureAndNeverChangesStateOnARefusal() async throws {
        let hub = FakeHub()
        let extra = #","start_error":"Terminal is waiting","start_error_code":"shell_waiting","start_error_args":{"line":"Continue?","choices":"y,n"}"#
        let (app, key) = try app(hub, sessionExtra: extra)
        let before = try #require(app.session(key))
        let pending = refused("first")
        app.chat(key).pending = [pending]
        defer { app.outboxRetry.values.forEach { $0.cancel() } }

        await #expect(throws: (any Error).self) {
            try await app.continueStart(key, choice: "y", features: [], api: hub.client)
        }
        #expect(hub.requests.isEmpty)
        #expect(app.session(key) == before)
        #expect(app.chat(key).pending == [pending])

        let path = "/api/v1/sessions/box/s1/start/continue"
        hub.on("POST", path) { _ in throw APIError(status: 409, message: "gone", fields: ["code": "shell_waiting"]) }
        await #expect(throws: (any Error).self) {
            try await app.continueStart(key, choice: "y", features: [TaskStartRemedy.continueFeature], api: hub.client)
        }
        #expect(app.session(key) == before)
        #expect(app.chat(key).pending == [pending])
        #expect(hub.sent("POST", "/api/v1/sessions/box/s1/messages").isEmpty)
    }

    @Test func continueStartClearsTheFailureStartsTheSessionThenResends() async throws {
        let hub = FakeHub()
        let extra = #","start_error":"Terminal is waiting","start_error_code":"shell_waiting","start_error_args":{"line":"Continue?","choices":"y,n"}"#
        let (app, key) = try app(hub, sessionExtra: extra)
        let pending = refused("first")
        app.chat(key).pending = [pending]
        let continuePath = "/api/v1/sessions/box/s1/start/continue"
        let messagePath = "/api/v1/sessions/box/s1/messages"
        hub.on("POST", continuePath,
               json: #"{"session":{"machine_id":"box","session_id":"s1","status":"idle","state":"idle","pane_id":"p1","capabilities":{"read":true,"send":true}}}"#)
        hub.on("POST", messagePath, json: #"{"ok":true}"#)
        defer { app.outboxRetry.values.forEach { $0.cancel() } }

        try await app.continueStart(key, choice: "y", features: [TaskStartRemedy.continueFeature], api: hub.client)

        #expect(app.session(key)?.starting == true)
        #expect(app.session(key)?.startError == nil && app.session(key)?.startErrorCode == nil && app.session(key)?.startErrorArgs == nil)
        for _ in 0..<300 where app.chat(key).pending.first?.state != .sent {
            try await Task.sleep(for: .milliseconds(10))
        }
        #expect(app.chat(key).pending.first?.state == .sent)
        #expect(app.chat(key).pending.first?.id != pending.id)
        #expect(try texts(hub, path: messagePath) == ["first"])
        let body = try #require(hub.sent("POST", continuePath).first?.body)
        #expect(try JSONSerialization.jsonObject(with: body) as? [String: String] == ["choice": "y"])
    }
}
