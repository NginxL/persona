import Foundation
import Testing
@testable import Persona
import PersonaWire

private actor PushRegistrations {
    private(set) var count = 0

    func register() -> Bool {
        count += 1
        return true
    }
}

private struct RequestBody: Codable, Equatable {
    var value: String
}

fileprivate struct NonMatchingRefusal: Sendable {
    var path: String
    var status: Int
    var code: String
}

@MainActor
struct APISendTests {
    private let key = SessionKey(machineID: "test", sessionID: "codex")
    private let path = "/api/v1/sessions/test/codex/messages"

    /// The watch's ids (RetryIDs): a retry after a failure keeps the failed try's id, a message that went through
    /// leaves the next one a new id, another message has its own.
    @Test func retriesKeepTheFailedTrysID() {
        var ids = RetryIDs<String>()
        let first = ids.id(for: "继续")
        #expect(UUID(uuidString: first) != nil)
        #expect(ids.id(for: "继续") == first) // the send failed: tapped again
        #expect(ids.id(for: "停一下") != first)
        ids.sent("继续")
        #expect(ids.id(for: "继续") != first)
    }

    /// A failed request and its retry keep the caller's id so the hub can deduplicate delivery.
    @Test func preservesMessageIDOnRetry() async throws {
        let hub = FakeHub()
        let id = "watch-message-1185"
        hub.on("POST", path) { _ in throw URLError(.timedOut) }
        await #expect(throws: URLError.self) {
            try await hub.client.send(key, text: "继续", clientMsgID: id)
        }
        hub.on("POST", path, json: "{}")
        try await hub.client.send(key, text: "继续", clientMsgID: id)

        let requests = hub.sent("POST", path)
        #expect(requests.count == 2)
        for request in requests {
            let body = try #require(request.body)
            let fields = try JSONDecoder().decode([String: String].self, from: body)
            #expect(fields["client_msg_id"] == id)
        }
    }

    @Test func aSuccessfulRequestSendsOnceWithoutRegistering() async throws {
        let hub = FakeHub(), registrations = PushRegistrations()
        var api = hub.client
        api.registerPush = { _ in await registrations.register() }
        hub.on("PUT", path, json: "{}")

        _ = try await api.request("PUT", path, body: RequestBody(value: "once"))

        #expect(hub.sent("PUT", path).count == 1)
        #expect(await registrations.count == 0)
    }

    /// Only this device-route refusal registers and retries; the same encoded body crosses both sends.
    @Test func pushRegistrationRequiredRegistersAndRetriesOnceWithTheSameBody() async throws {
        let devicePath = "/api/v1/devices/d/prefs"
        let hub = FakeHub(), registrations = PushRegistrations()
        var attempts = 0
        hub.on("PUT", devicePath) { _ in
            attempts += 1
            if attempts == 1 {
                throw APIError(status: 409, message: "register again", fields: ["code": APIClient.pushRegistrationRequired])
            }
            return Data("{}".utf8)
        }
        var api = hub.client
        api.registerPush = { _ in await registrations.register() }

        _ = try await api.request("PUT", devicePath, body: RequestBody(value: "retry"))

        #expect(attempts == 2)
        #expect(await registrations.count == 1)
        let requests = hub.sent("PUT", devicePath)
        try #require(requests.count == 2)
        let body = try #require(requests[0].body)
        #expect(requests[1].body == body)
        #expect(try JSONDecoder().decode(RequestBody.self, from: body) == RequestBody(value: "retry"))
    }

    /// A different code, status or route neither registers nor gains another send.
    @Test(arguments: [
        NonMatchingRefusal(path: "/api/v1/devices/d/prefs", status: 409, code: "input_draft"),
        NonMatchingRefusal(path: "/api/v1/devices/d/prefs", status: 500, code: APIClient.pushRegistrationRequired),
        NonMatchingRefusal(path: "/api/v1/sessions/test/codex/messages", status: 409, code: APIClient.pushRegistrationRequired),
    ])
    fileprivate func nonMatchingRefusalsDoNotRegisterOrRetry(_ refusal: NonMatchingRefusal) async throws {
        let hub = FakeHub(), registrations = PushRegistrations()
        hub.on("PUT", refusal.path) { _ in
            throw APIError(status: refusal.status, message: "refused", fields: ["code": refusal.code])
        }
        var api = hub.client
        api.registerPush = { _ in await registrations.register() }

        await #expect(throws: APIError.self) {
            _ = try await api.request("PUT", refusal.path, body: RequestBody(value: "once"))
        }

        #expect(hub.sent("PUT", refusal.path).count == 1)
        #expect(await registrations.count == 0)
    }
}
