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

    private let heldJSON = #"{"id":"x","text":"Review the notes","at":"2026-11-17T10:00:00Z","state":"failed","computer":"Laptop","error":"Might not have arrived. Retry checks the chat first."}"#

    private func agent(_ held: String? = nil) throws -> Session {
        var row = try session(held.map { #","undelivered":["# + $0 + "]" } ?? "")
        row.assistant = false
        row.manager = "another-assistant"
        return row
    }

    private func heldModel(_ hub: FakeHub, full: Bool = true, feature: Bool = true, folder: URL? = nil,
                           pairing: String = "fixture-pair") throws -> AppModel {
        let model = AppModel(stateFolder: folder ?? FileManager.default.temporaryDirectory.appending(path: "held-" + UUID().uuidString))
        model.linkHooksForTests = (transport: try #require(hub.client.transport), socket: { SyncSocket() })
        model.registry = HubRegistry(hubs: [PairedHub(id: pairing, config: ServerConfig(url: hub.url, token: "fixture"), deviceID: full ? "phone" : nil)])
        model.buildLinks()
        model.link("")?.received(HubHello(features: feature ? ["agent_message_hold", "sessions_lookup"] : []))
        var client = hub.client.showing("another-manager")
        client.sessionLookup = true
        model.apiForTests = client
        model.replaceMachines("", [try JSON.decoder.decode(Machine.self, from: Data(#"{"id":"m","name":"Laptop","kind":"computer","online":false,"manager":"owner"}"#.utf8))])
        model.applySessions([ref: [try agent(heldJSON)]])
        return model
    }

    @Test func heldRetryUsesTheComputerAndKeepsTheManagersReply() async throws {
        let hub = FakeHub()
        let model = try heldModel(hub)
        let path = "/api/v1/sessions/m/s/held/x"
        let after = heldJSON.replacingOccurrences(of: "failed", with: "waiting")
            .replacingOccurrences(of: "Might not have arrived. Retry checks the chat first.", with: "Will check the chat when Laptop is back")
        hub.on("POST", path + "/retry", json: #"{"ok":true}"#)
        hub.on("GET", "/api/v1/sessions", json: #"{"sessions":[{"machine_id":"m","session_id":"s","title":"t","cwd":"","status":"offline","state":"offline","undelivered":["# + after + "]}]}")
        hub.on("DELETE", path, json: #"{"ok":true}"#)
        await model.retryUndelivered(key, "x")
        #expect(hub.sent("POST", path + "/retry").map { $0.headers[APIClient.managerHeader] } == ["owner"])
        #expect(model.session(key)?.undelivered?.first?.error == "Will check the chat when Laptop is back")
        await model.deleteUndelivered(key, "x")
        #expect(hub.sent("DELETE", path).map { $0.headers[APIClient.managerHeader] } == ["owner"])
        #expect(model.session(key)?.undelivered?.isEmpty == true)
        #expect(hub.requests.allSatisfy { $0.headers[APIClient.managerHeader] != "another-manager" })
    }

    private let retryPath = "/api/v1/sessions/m/s/held/x/retry"
    private let detailPath = "/api/v1/sessions"

    private func answerDetail(_ hub: FakeHub, _ messages: String) {
        hub.on("GET", detailPath, json: #"{"sessions":[{"machine_id":"m","session_id":"s","title":"t","cwd":"","status":"offline","state":"offline","undelivered":["# + messages + "]}]}")
    }

    private func visibleOffer(_ model: AppModel, id: String = "x") throws -> HeldRetryOffer {
        let row = try #require(model.session(key))
        let message = try #require(row.undelivered?.first { $0.id == id })
        return try #require(HeldRetryOffer(model.heldTarget(key, id, row: row), message))
    }

    private func waitForHeldRetry(_ model: AppModel, _ offer: HeldRetryOffer) async throws {
        let deadline = ContinuousClock.now + .seconds(10)
        while !model.heldRetryDisabled(offer), ContinuousClock.now < deadline { try await Task.sleep(for: .milliseconds(5)) }
        try #require(model.heldRetryDisabled(offer), "the persisted held-retry receipt was not published")
    }

    /// #11431: this suite is on the main actor, so no restored value can be published between `init` and the first
    /// expectation. Receipts already there prove that the initializer waited for the file read. After a yield, the
    /// restored receipt must disable the matching Retry.
    @Test func persistedHeldRetryReceiptsArriveAfterStartup() async throws {
        let folder = FileManager.default.temporaryDirectory.appending(path: UUID().uuidString)
        let model = try heldModel(FakeHub(), folder: folder)
        let check = try visibleOffer(model)
        HeldRetryOffer.save([check], in: folder)

        let opened = AppModel(stateFolder: folder)
        #expect(opened.heldRetryReceipts.isEmpty, "AppModel.init must not read held-retry receipts on the main actor")
        try await waitForHeldRetry(opened, check)
    }

    /// #11431: a receipt recorded while the startup read is pending survives the older snapshot's arrival. Keep the
    /// folder read-only during that change so its save cannot race and replace the snapshot this test is restoring.
    @Test func aRetryStateChangeDuringRestorationIsNotOverwritten() async throws {
        let folder = FileManager.default.temporaryDirectory.appending(path: UUID().uuidString)
        let model = try heldModel(FakeHub(), folder: folder)
        let messages = ["x", "y", "z"].map { heldJSON.replacingOccurrences(of: #""id":"x""#, with: #""id":"\#($0)""#) }
        model.applySessions([ref: [try agent(messages.joined(separator: ","))]])
        let persisted = try visibleOffer(model)
        let newer = try visibleOffer(model, id: "y")
        let marker = try visibleOffer(model, id: "z")
        HeldRetryOffer.save([persisted, marker], in: folder)

        let opened = AppModel(stateFolder: folder)
        try FileManager.default.setAttributes([.posixPermissions: 0o555], ofItemAtPath: folder.path)
        defer { try? FileManager.default.setAttributes([.posixPermissions: 0o755], ofItemAtPath: folder.path) }
        opened.heldRetryReceipts.insert(newer)
        #expect(opened.heldRetryReceipts == [newer], "the newer change must be made while restoration is still pending")
        try await waitForHeldRetry(opened, marker)
        #expect(opened.heldRetryDisabled(persisted), "the rest of the persisted snapshot is restored too")
        #expect(opened.heldRetryDisabled(newer), "restoration must not replace a newer retry-state change")
    }

    @Test func aSuccessfulHeldCheckCannotBeRetriedUnderTheSameInstruction() async throws {
        let hub = FakeHub()
        let model = try heldModel(hub)
        hub.on("POST", retryPath, json: #"{"ok":true}"#)
        answerDetail(hub, heldJSON)
        let check = try visibleOffer(model)
        await model.retryUndelivered(key, "x", shown: check)
        #expect(hub.sent("GET", detailPath).count == 1)
        #expect(model.session(key)?.undelivered?.first?.error == "Might not have arrived. Retry checks the chat first.")
        #expect(model.heldRetryDisabled(check))
        var summary = try agent()
        summary.listWaiting = false
        model.landSessionSummaries([summary], group: "")
        // Updating an image count or a sibling's state cannot renew consent for this operation.
        let sibling = heldJSON.replacingOccurrences(of: #""id":"x""#, with: #""id":"y""#)
        let edited = heldJSON.replacingOccurrences(of: #""text":"Review the notes""#, with: #""text":"Review the notes","images":1"#)
        model.applySessions([ref: [try agent(edited + "," + sibling)]])
        #expect(model.heldRetryDisabled(try visibleOffer(model)))
        #expect(!model.heldRetryDisabled(try visibleOffer(model, id: "y")))
        await model.retryUndelivered(key, "x")
        #expect(hub.sent("POST", retryPath).count == 1)
        let confirmed = heldJSON.replacingOccurrences(of: "Might not have arrived. Retry checks the chat first.",
                                                       with: "Couldn't confirm it arrived. Retry to send it anyway, or delete it.")
        model.applySessions([ref: [try agent(confirmed)]])
        let resend = try visibleOffer(model)
        #expect(!model.heldRetryDisabled(resend))
        await model.retryUndelivered(key, "x", shown: check) // an old menu which was already on screen
        #expect(hub.sent("POST", retryPath).count == 1)
        answerDetail(hub, "")
        await model.retryUndelivered(key, "x", shown: resend)
        #expect(hub.sent("POST", retryPath).count == 2)
        #expect(model.session(key)?.undelivered?.isEmpty == true)
        model.applySessions([ref: [try agent(heldJSON)]])
        #expect(!model.heldRetryDisabled(try visibleOffer(model)))
    }

    @Test(arguments: [URLError.Code.networkConnectionLost, .timedOut])
    func losingTheReplyAfterTheManagerChecksCannotAuthorizeASend(_ code: URLError.Code) async throws {
        let hub = FakeHub()
        let folder = FileManager.default.temporaryDirectory.appending(path: UUID().uuidString)
        let model = try heldModel(hub, folder: folder)
        let check = try visibleOffer(model)
        var actions: [String] = []
        hub.on("POST", retryPath) { _ in
            if actions.isEmpty {
                actions.append("checked")
                throw URLError(code)
            }
            actions.append("sent")
            return Data(#"{"ok":true}"#.utf8)
        }
        answerDetail(hub, heldJSON)
        await model.retryUndelivered(key, "x", shown: check)
        let opened = try heldModel(hub, folder: folder)
        try await waitForHeldRetry(opened, check)
        await opened.retryUndelivered(key, "x", shown: try visibleOffer(opened))
        #expect(actions == ["checked"])
        #expect(opened.heldRetryDisabled(check))

        let resend = heldJSON.replacingOccurrences(of: "Might not have arrived. Retry checks the chat first.",
                                                   with: "Couldn't confirm it arrived. Retry to send it anyway, or delete it.")
        opened.applySessions([ref: [try agent(resend)]])
        await opened.retryUndelivered(key, "x", shown: check)
        #expect(actions == ["checked"])
        answerDetail(hub, "")
        await opened.retryUndelivered(key, "x", shown: try visibleOffer(opened))
        #expect(actions == ["checked", "sent"])
    }

    @Test func aRefusedConnectionLeavesTheSameManagerCheckAvailable() async throws {
        let hub = FakeHub()
        let model = try heldModel(hub)
        let check = try visibleOffer(model)
        hub.on("POST", retryPath) { _ in throw URLError(.cannotConnectToHost) }
        await model.retryUndelivered(key, "x", shown: check)
        #expect(!model.heldRetryDisabled(check))
        hub.on("POST", retryPath, json: #"{"ok":true}"#)
        answerDetail(hub, heldJSON)
        await model.retryUndelivered(key, "x", shown: check)
        #expect(hub.sent("POST", retryPath).count == 2 && model.heldRetryDisabled(check))
    }

    @Test func aRejectedPostCanBeRetriedButAnAcknowledgedPostSurvivesAFailedReadAndRelaunch() async throws {
        let hub = FakeHub()
        let folder = FileManager.default.temporaryDirectory.appending(path: UUID().uuidString)
        let model = try heldModel(hub, folder: folder)
        let check = try visibleOffer(model)
        await model.retryUndelivered(key, "x") // the fixture answers 404
        #expect(!model.heldRetryDisabled(check))
        hub.on("POST", retryPath, json: #"{"ok":true}"#)
        await model.retryUndelivered(key, "x") // the fresh GET fails after the POST succeeded
        #expect(model.heldRetryDisabled(check))
        let opened = try heldModel(hub, folder: folder)
        opened.link("")?.received(HubHello(features: ["agent_message_hold", "sessions_lookup"]))
        try await waitForHeldRetry(opened, check)
        #expect(opened.heldRetryDisabled(try visibleOffer(opened)))
        await opened.retryUndelivered(key, "x")
        #expect(hub.sent("POST", retryPath).count == 2)
        hub.on("DELETE", "/api/v1/sessions/m/s/held/x", json: #"{"ok":true}"#)
        await opened.deleteUndelivered(key, "x", target: check.target)
        #expect(hub.sent("DELETE", "/api/v1/sessions/m/s/held/x").count == 1)
        #expect(opened.session(key)?.undelivered?.isEmpty == true)
        #expect(!opened.heldRetryDisabled(check))
    }

    @Test func anUnlistedChatKeepsItsReceiptUntilACompleteSessionLosesTheOperation() async throws {
        let hub = FakeHub()
        let model = try heldModel(hub)
        let check = try visibleOffer(model)
        hub.on("POST", retryPath, json: #"{"ok":true}"#)
        hub.on("GET", detailPath, json: #"{"sessions":[]}"#)
        await model.retryUndelivered(key, "x", shown: check)
        model.applySessions([ref: []]) // a disconnected computer may stop listing all its Agents
        model.applySessions([ref: [try agent(heldJSON)]])
        await model.retryUndelivered(key, "x", shown: check)
        #expect(hub.sent("POST", retryPath).count == 1)
        model.applySessions([ref: [try agent()]]) // the Agent is listed in full and no longer holds this operation
        model.applySessions([ref: [try agent(heldJSON)]])
        #expect(!model.heldRetryDisabled(try visibleOffer(model)))
    }

    @Test func aRetryReadCannotBringBackAnotherDevicesDeletedSibling() async throws {
        let hub = FakeHub()
        let model = try heldModel(hub)
        let sibling = heldJSON.replacingOccurrences(of: #""id":"x""#, with: #""id":"y""#)
        model.applySessions([ref: [try agent(heldJSON + "," + sibling)]])
        let check = try visibleOffer(model)
        hub.on("POST", retryPath) { _ in
            model.applySessions([self.ref: [try self.agent(self.heldJSON)]])
            return Data(#"{"ok":true}"#.utf8)
        }
        answerDetail(hub, heldJSON + "," + sibling) // the Hub still lists the sibling deleted during the POST
        await model.retryUndelivered(key, "x", shown: check)
        #expect(model.session(key)?.undelivered?.map(\.id) == ["x"])
        #expect(model.heldRetryDisabled(check))
    }

    @Test(arguments: [false, true])
    func deleteStillWorksWhileTheManagerIsReadingAndALateCopyCannotRestoreIt(replyLost: Bool) async throws {
        let hub = FakeHub()
        let model = try heldModel(hub)
        let check = try visibleOffer(model)
        hub.on("POST", retryPath) { _ in
            if replyLost { throw URLError(.networkConnectionLost) }
            return Data(#"{"ok":true}"#.utf8)
        }
        hub.on("DELETE", "/api/v1/sessions/m/s/held/x", json: #"{"ok":true}"#)
        hub.onAsync("GET", detailPath) { _ in
            #expect(model.heldRetryDisabled(check))
            await model.deleteUndelivered(self.key, "x", target: check.target)
            return Data((#"{"sessions":[{"machine_id":"m","session_id":"s","title":"t","cwd":"","status":"offline","state":"offline","undelivered":["# + self.heldJSON + "]}]}").utf8)
        }
        await model.retryUndelivered(key, "x", shown: check)
        #expect(hub.sent("DELETE", "/api/v1/sessions/m/s/held/x").count == 1)
        #expect(model.session(key)?.undelivered?.isEmpty == true)
        #expect(!model.heldRetryDisabled(check))
    }

    @Test func lateRepliesStayWithThePairedManagerWhichAcceptedTheCheck() async throws {
        let hub = FakeHub()
        let model = try heldModel(hub)
        let original = try visibleOffer(model)
        let owner = try #require(model.machine(ref))
        var other = owner
        other.manager = "second-owner"
        hub.on("POST", retryPath) { _ in
            model.replaceMachines("", [other])
            return Data(#"{"ok":true}"#.utf8)
        }
        await model.retryUndelivered(key, "x", shown: original)
        let second = try visibleOffer(model)
        #expect(hub.sent("GET", detailPath).isEmpty)
        #expect(!model.heldRetryDisabled(second))
        await model.retryUndelivered(key, "x", shown: original)
        #expect(hub.sent("POST", retryPath).count == 1)
        model.replaceMachines("", [owner])
        #expect(model.heldRetryDisabled(try visibleOffer(model)))
        // A new full-device pairing may use identical machine, session, operation and local manager names.
        model.registry = HubRegistry(hubs: [PairedHub(id: "other-pair", config: ServerConfig(url: hub.url, token: "other-device"), deviceID: "phone")])
        model.buildLinks()
        model.link("")?.received(HubHello(features: ["agent_message_hold", "sessions_lookup"]))
        #expect(!model.heldRetryDisabled(try visibleOffer(model)))
        await model.retryUndelivered(key, "x", shown: original)
        #expect(hub.sent("POST", retryPath).count == 1)
    }

    @Test func partialRowsPreserveHeldMessagesAndACompleteRowCanRemoveThem() throws {
        let model = try heldModel(FakeHub())
        var summary = try agent()
        summary.listWaiting = false
        model.landSessionSummaries([summary], group: "")
        #expect(model.session(key)?.undelivered?.map(\.id) == ["x"])
        model.applySessions([ref: [try agent()]])
        #expect(model.session(key)?.undelivered?.isEmpty != false)
    }

    @Test func restrictedOfflineSendsAndHeldActionsLeaveTheDraftInPlace() async throws {
        for (full, feature) in [(false, true), (true, false)] {
            let hub = FakeHub()
            let model = try heldModel(hub, full: full, feature: feature)
            model.drafts[key] = "Still editing"
            #expect(!model.sendNow(key, text: "Still editing"))
            await model.retryUndelivered(key, "x")
            await model.deleteUndelivered(key, "x")
            #expect(model.drafts[key] == "Still editing")
            #expect(model.chat(key).pending.isEmpty)
            #expect(hub.requests.isEmpty)
        }
    }

    @Test func anExplicitRetryDoesNotOverrideAnotherDevicesDecision() async throws {
        let hub = FakeHub()
        let model = try heldModel(hub)
        let newRow = try agent(heldJSON.replacingOccurrences(of: "Might not have arrived. Retry checks the chat first.", with: "Not sent: the waiting time expired."))
        hub.on("POST", "/api/v1/sessions/m/s/held/x/retry") { _ in
            model.applySessions([self.ref: [newRow]])
            return Data(#"{"ok":true}"#.utf8)
        }
        await model.retryUndelivered(key, "x")
        #expect(model.session(key)?.undelivered?.first?.error == "Not sent: the waiting time expired.")
        #expect(model.session(key)?.undelivered?.first?.gaveUp == true)
        #expect(hub.sent("GET", "/api/v1/sessions").isEmpty)
    }

    @Test func holdingUsesThePairedHubsFeatureAndLeavesNewTextAlone() async throws {
        let hub = FakeHub()
        let model = try heldModel(hub)
        let item = PendingMessage(text: "Review the notes", images: [])
        hub.on("POST", "/api/v1/sessions/m/s/messages", json: #"{"delivered":"later","undelivered":{"id":"y","text":"Review the notes","at":"2026-11-17T10:00:00Z","state":"waiting","computer":"Laptop","error":"Will send when Laptop is back"}}"#)
        #expect(model.offlineAgent(try agent())?.canHold == true)
        #expect(!model.canManageHeld("other"))
        #expect(model.offlineAgent(try session("")) == nil, "Assistant keeps its own delivery rules")
        model.chat(key).pending = [item]
        model.chat(key).beginSendFeedback(item)
        model.drafts[key] = "A new draft"
        let outcome = await model.sendPending(key, item)
        #expect(outcome == .kept)
        #expect(hub.sent("POST", "/api/v1/sessions/m/s/messages").first?.headers["X-Persona-Features"] == "agent_message_hold")
        #expect(model.session(key)?.undelivered?.last?.heldStatus == "Will send when Laptop is back")
        #expect(model.drafts[key] == "A new draft")
        #expect(model.chat(key).pending.isEmpty && model.chat(key).sendFeedback?.phase == .finished)
        var stopped = try agent()
        stopped.capabilities = try JSON.decoder.decode(SessionCapabilities.self, from: Data(#"{"read":true,"terminal":true}"#.utf8))
        #expect(stopped.isStopped && !stopped.canSend)
        #expect(model.offlineAgent(stopped) == nil)
        var history = stopped
        history.sourceKind = "readonly"
        #expect(!history.isStopped && model.offlineAgent(history) == nil)
    }

    @Test func onlyTheIOSAppAdvertisesAgentHolding() {
        let phone = RequestClientMetadata(platform: .iOS, surface: .app, version: "1")
        let watch = RequestClientMetadata(platform: .watchOS, surface: .app, version: "1")
        let widget = RequestClientMetadata(platform: .iOS, surface: .widget, version: "1")
        #expect(LiveConnection.helloFeatures(phone).contains(UndeliveredMessage.agentHoldFeature))
        #expect(!LiveConnection.helloFeatures(watch).contains(UndeliveredMessage.agentHoldFeature))
        #expect(!LiveConnection.helloFeatures(widget).contains(UndeliveredMessage.agentHoldFeature))
    }

    @Test func heldStatusesDisplayExactlyWhatTheManagerSent() throws {
        for (state, line) in [("waiting", "Will send when Laptop is back"), ("waiting", "Sending…"),
                              ("waiting", "Waits behind the message above"), ("failed", "Might not have arrived. Retry checks the chat first."),
                              ("failed", "Not sent: the waiting time expired.")] {
            var message = try JSON.decoder.decode(UndeliveredMessage.self, from: Data(heldJSON.utf8))
            message.state = state
            message.error = line
            #expect(message.heldStatus == line)
            #expect(message.canRetry == (state == "failed"))
            message.deleteOnly = true
            #expect(!message.canRetry && message.heldStatus == line)
            let restored = try JSON.decoder.decode(UndeliveredMessage.self, from: JSON.encoder.encode(message))
            #expect(restored.computer == "Laptop" && restored.heldStatus == line)
        }
    }
}
