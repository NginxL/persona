import Foundation
import Testing
@testable import Persona
import PersonaWire

/// Switching models in the effort capsule (#3119, the web's #2434): which sessions offer it, when it waits, and what goes to the hub.
struct ModelSwitchTests {
    private func session(_ extra: String = "", state: String = "idle") throws -> Session {
        try JSON.decoder.decode(Session.self, from: Data(
            #"{"machine_id":"box","session_id":"s1","status":"idle","state":"\#(state)","pane_id":"p1"\#(extra)}"#.utf8))
    }

    @Test func onlyClaudeCodeInAPane() throws {
        #expect(SessionModes.switchesModel(try session()))
        #expect(SessionModes.switchesModel(try session(#","agent":"claude""#)))
        #expect(!SessionModes.switchesModel(try session(#","agent":"codex""#)))
        #expect(!SessionModes.switchesModel(try session(#","agent":"traex""#)))
        #expect(!SessionModes.switchesModel(try session(#","assistant":true"#)))
        let noPane = try JSON.decoder.decode(Session.self, from: Data(#"{"machine_id":"box","session_id":"s1","status":"idle","state":"idle"}"#.utf8))
        #expect(!SessionModes.switchesModel(noPane))
        #expect(!SessionModes.switchesModel(nil))
    }

    /// A runtime's session switches where its node takes a model (#6294); its menus wait for its launcher's list, as Claude
    /// Code's fixed aliases are not its words. Claude Code in a pane keeps the aliases until its list is read.
    @Test func aRuntimeSwitchesWhereItsNodeSaysSoFromItsLaunchersList() throws {
        let runtime = { (caps: String) in
            try JSON.decoder.decode(Session.self, from: Data(
                #"{"machine_id":"box","session_id":"s1","status":"idle","state":"idle","source_kind":"runtime","agent":"codex","capabilities":{"read":true,"send":true\#(caps)}}"#.utf8))
        }
        let list = [AgentModel(id: "gpt-6", name: "GPT-6")]
        #expect(SessionModes.switchesModel(try runtime(#","model":true"#)))
        #expect(!SessionModes.switchesModel(try runtime(#","model":false"#)))
        #expect(!SessionModes.switchesModel(try runtime("")))
        #expect(!SessionModes.offersModels(try runtime(#","model":true"#), list: nil))
        #expect(SessionModes.offersModels(try runtime(#","model":true"#), list: list))
        #expect(!SessionModes.offersModels(try runtime(#","model":false"#), list: list))
        #expect(SessionModes.offersModels(try session(), list: nil))
        #expect(SessionModes.offersModels(try session(), list: list))
    }

    @Test func waitsWhileItWorksOrAsks() throws {
        #expect(SessionModes.modelBusy(try session()) == nil)
        #expect(SessionModes.modelBusy(try session(state: "unread")) == nil)
        #expect(SessionModes.modelBusy(try session(state: "working")) != nil)
        #expect(SessionModes.modelBusy(try session(state: "needs_you")) != nil)
        #expect(SessionModes.modelBusy(try session(state: "offline")) != nil)
    }

    @MainActor @Test func thePickItRunsSendsNothing() {
        #expect(SessionModes.modelPatch("opus", current: "claude-opus-5-5-20260101") == nil)
        #expect(SessionModes.modelPatch("sonnet", current: "claude-opus-5-5-20260101") == "sonnet")
        #expect(SessionModes.modelPatch("default", current: nil) == nil)
        #expect(SessionModes.modelPatch("haiku", current: nil) == "haiku")
    }

    /// A session running a typed model (#1644) is not on Default: picking Default switches back.
    @MainActor @Test func defaultSwitchesBackFromATypedModel() {
        #expect(SessionModes.modelPatch("default", current: "vendor/model-x") == "default")
        #expect(SessionModes.modelPatch("default", current: "~vendor/model-latest[1m]") == "default")
        #expect(SessionModes.modelPatch("opus", current: "vendor/model-x") == "opus")
    }

    /// What the menus tick: an alias for Claude's tiers, Default while nothing is known, the ID itself otherwise.
    @MainActor @Test func aTypedModelTicksNoAlias() {
        #expect(PromptItems.runningModelID(nil) == "default")
        #expect(PromptItems.runningModelID("") == "default")
        #expect(PromptItems.runningModelID("<synthetic>") == "default")
        #expect(PromptItems.runningModelID("claude-opus-5-5-20260101") == "opus")
        #expect(PromptItems.runningModelID("Sonnet") == "sonnet")
        #expect(PromptItems.runningModelID("vendor/model-x") == "vendor/model-x")
        #expect(PromptItems.unlistedModel("claude-haiku-4-5") == nil)
        #expect(PromptItems.unlistedModel(nil) == nil)
        #expect(PromptItems.unlistedModel("~vendor/model-latest[1m]") == "~vendor/model-latest[1m]")
        // The model it runs is a row of its own, by its ID, the one ticked.
        #expect(PromptItems.runningModels("vendor/model-x").map(\.id) == PromptItems.claudeModels.map(\.id) + ["vendor/model-x"])
        #expect(PromptItems.runningModels("vendor/model-x").last?.name == "vendor/model-x")
        #expect(PromptItems.runningModels(nil).map(\.id) == PromptItems.claudeModels.map(\.id))
        let menu = PromptItems.composerModels(running: "vendor/model-x").map(\.id)
        #expect(menu == PromptItems.claudeModels.map(\.id) + ["vendor/model-x", PromptItems.otherModel.id])
        #expect(PromptItems.composerModels(running: "claude-opus-5-5").map(\.id) == PromptItems.claudeModels.map(\.id) + [PromptItems.otherModel.id])
    }

    @Test func modesCarryTheModelAlone() async throws {
        let hub = FakeHub()
        hub.on("POST", "/api/v1/sessions/box/s1/modes", json: #"{"ok":true}"#)
        let key = SessionKey(machineID: "box", sessionID: "s1")
        try await hub.client.setModes(key, model: "sonnet")
        try await hub.client.setModes(key, effort: "high")
        let sent = hub.sent("POST", "/api/v1/sessions/box/s1/modes")
        #expect(sent.count == 2)
        let a = try #require(sent.first?.body), b = try #require(sent.last?.body)
        let first = try #require(try JSONSerialization.jsonObject(with: a) as? [String: String])
        #expect(first == ["model": "sonnet"])
        let second = try #require(try JSONSerialization.jsonObject(with: b) as? [String: String])
        #expect(second == ["effort": "high"]) // an older caller's body is unchanged
    }

    @Test func busyEffortReturnsThePendingChoice() async throws {
        let hub = FakeHub()
        hub.on("POST", "/api/v1/sessions/box/s1/modes", json: #"{"ok":true,"pending_effort":"high"}"#)
        let key = SessionKey(machineID: "box", sessionID: "s1")
        #expect(try await hub.client.setModes(key, effort: "high", queueEffort: true) == "high")
        let body = try #require(hub.sent("POST", "/api/v1/sessions/box/s1/modes").first?.body)
        #expect((try JSONSerialization.jsonObject(with: body) as? [String: Any])?["queue_effort"] as? Bool == true)
        let row = try session(#","effort":"low","pending_effort":"high","effort_change_failed":true"#)
        #expect(row.effort == "low")
        #expect(row.pendingEffort == "high")
        #expect(row.effortChangeFailed == true)
    }

    @MainActor @Test func summaryConfirmationWinsOverCachedEffort() throws {
        let pending = try session(#","effort":"low","pending_effort":"high""#)
        let confirmed = try session(#","effort":"high","waiting":false"#)
        let merged = confirmed.preservingDetails(from: pending)
        #expect(merged.effort == "high")
        #expect(merged.pendingEffort == nil)
    }

    @MainActor @Test func followUpsWaitForConfirmationAndGoInOrder() async throws {
        let hub = FakeHub()
        let path = "/api/v1/sessions/box/s1"
        hub.on("POST", path + "/modes", json: #"{"ok":true,"pending_effort":"high"}"#)
        hub.on("POST", path + "/messages", json: #"{"ok":true}"#)
        let app = AppModel(stateFolder: FileManager.default.temporaryDirectory.appending(path: UUID().uuidString))
        app.forgetRestoredForTests()
        app.apiForTests = hub.client
        app.linksUpForTests = true
        let row = try session(#","effort":"low","agent":"claude""#, state: "working")
        let key = row.key
        app.applySessions([key.machineRef: [row]])

        #expect(await app.setModes(key, effort: "high"))
        #expect(app.latest(key)?.effort == "low")
        #expect(app.latest(key)?.pendingEffort == "high")
        #expect(app.sendNow(key, text: "first"))
        #expect(app.sendNow(key, text: "second"))
        #expect(app.chat(key).pending.map(\.state) == [.waiting, .waiting])
        #expect(hub.sent("POST", path + "/messages").isEmpty)

        var confirmed = row
        confirmed.effort = "high"
        app.applySessions([key.machineRef: [confirmed]])
        for _ in 0..<300 where app.chat(key).pending.contains(where: { $0.state != .sent }) {
            try await Task.sleep(for: .milliseconds(10))
        }
        let texts = try hub.sent("POST", path + "/messages").map { request in
            let body = try #require(request.body)
            return (try JSONSerialization.jsonObject(with: body) as? [String: Any])?["text"] as? String
        }
        #expect(texts == ["first", "second"])
        #expect(app.chat(key).pending.map(\.state) == [.sent, .sent])
    }

    @MainActor @Test func earlyRefusalKeepsItsIDUntilEffortIsConfirmed() async throws {
        let hub = FakeHub()
        let path = "/api/v1/sessions/box/s1/messages"
        hub.on("POST", path) { _ in
            throw APIError(status: 409, message: "Thinking effort is changing", fields: [
                "code": "effort_change_pending", "delivered": "none", "effort": "high",
            ])
        }
        let app = AppModel(stateFolder: FileManager.default.temporaryDirectory.appending(path: UUID().uuidString))
        app.forgetRestoredForTests()
        app.apiForTests = hub.client
        app.linksUpForTests = true
        var row = try session(#","effort":"low","agent":"claude""#)
        let key = row.key
        app.applySessions([key.machineRef: [row]])

        #expect(app.sendNow(key, text: "follow-up"))
        let originalID = try #require(app.chat(key).pending.first?.id)
        for _ in 0..<300 where app.chat(key).pending.first?.state == .sending {
            try await Task.sleep(for: .milliseconds(10))
        }
        app.flushOutbox()
        for _ in 0..<300 where app.flushingOutbox.contains(key.group) {
            try await Task.sleep(for: .milliseconds(10))
        }
        #expect(app.chat(key).pending.first?.state == .waiting)
        #expect(app.chat(key).pending.first?.refused == true)
        app.flushOutbox()
        for _ in 0..<300 where app.flushingOutbox.contains(key.group) {
            try await Task.sleep(for: .milliseconds(10))
        }
        #expect(app.chat(key).pending.first?.id == originalID)
        #expect(hub.sent("POST", path).count == 1)

        hub.on("POST", path, json: #"{"ok":true}"#)
        row.effort = "high"
        app.applySessions([key.machineRef: [row]])
        for _ in 0..<300 where app.chat(key).pending.first?.state != .sent {
            try await Task.sleep(for: .milliseconds(10))
        }
        #expect(hub.sent("POST", path).count == 2)
        #expect(app.chat(key).pending.first?.state == .sent)
        #expect(app.chat(key).pending.first?.id != originalID)
    }

    @MainActor @Test func keepingCurrentEffortAfterALostChangeRequeuesFailedFollowUpsInOrder() async throws {
        let hub = FakeHub()
        let path = "/api/v1/sessions/box/s1/messages"
        hub.on("POST", path, json: #"{"ok":true}"#)
        let app = AppModel(stateFolder: FileManager.default.temporaryDirectory.appending(path: UUID().uuidString))
        app.forgetRestoredForTests()
        app.apiForTests = hub.client
        app.linksUpForTests = true
        let row = try session(#","effort":"low","agent":"claude""#)
        let key = row.key
        app.applySessions([key.machineRef: [row]])

        var first = PendingMessage(text: "first", images: [], state: .failed)
        first.requiredEffort = "high"
        first.failure = "missing confirmation"
        first.stalledSince = .distantPast
        first.refused = true
        var second = PendingMessage(text: "second", images: [], state: .failed)
        second.requiredEffort = "low"
        var uncertain = PendingMessage(text: "check the receipt", images: [], state: .failed)
        uncertain.requiredEffort = "high"
        uncertain.mayHaveArrived = true
        let store = app.chat(key)
        store.pending = [first, second, uncertain]

        #expect(app.canKeepOutboxEffort(key, effort: "low"))
        await app.keepCurrentEffort(key, effort: "low")
        #expect(store.pending.map(\.requiredEffort) == ["low", "low", "high"])
        #expect(store.pending.map(\.state) == [.waiting, .waiting, .failed])
        #expect(store.pending.map(\.id) == [first.id, second.id, uncertain.id])
        #expect(store.pending[0].failure == nil)
        #expect(store.pending[0].stalledSince == nil)
        #expect(!app.canKeepOutboxEffort(key, effort: "low"))

        for _ in 0..<300 where store.pending.prefix(2).contains(where: { $0.state != .sent }) {
            try await Task.sleep(for: .milliseconds(10))
        }
        let texts = try hub.sent("POST", path).map { request in
            let body = try #require(request.body)
            return (try JSONSerialization.jsonObject(with: body) as? [String: Any])?["text"] as? String
        }
        #expect(texts == ["first", "second"])
        #expect(store.pending.map(\.state) == [.sent, .sent, .failed])
        #expect(store.pending[0].id != first.id)
        #expect(store.pending[1].id == second.id)
        #expect(store.pending[2] == uncertain)
    }

    @MainActor @Test func retryAfterALostChangeUsesTheCurrentEffort() async throws {
        let hub = FakeHub()
        let path = "/api/v1/sessions/box/s1/messages"
        hub.on("POST", path, json: #"{"ok":true}"#)
        let app = AppModel(stateFolder: FileManager.default.temporaryDirectory.appending(path: UUID().uuidString))
        app.forgetRestoredForTests()
        app.apiForTests = hub.client
        app.linksUpForTests = false
        let row = try session(#","effort":"low","agent":"claude""#)
        let key = row.key
        app.applySessions([key.machineRef: [row]])

        var failed = PendingMessage(text: "follow-up", images: [], state: .failed)
        failed.requiredEffort = "high"
        failed.refused = true
        let store = app.chat(key)
        store.pending = [failed]
        app.retry(key, failed)

        for _ in 0..<300 where store.pending.first?.state == .sending {
            try await Task.sleep(for: .milliseconds(10))
        }
        try #require(store.pending.first?.state == .waiting)
        #expect(hub.sent("POST", path).isEmpty)
        app.linksUpForTests = true
        app.flushOutbox()
        for _ in 0..<300 where store.pending.first?.state != .sent {
            try await Task.sleep(for: .milliseconds(10))
        }
        #expect(hub.sent("POST", path).count == 1)
        #expect(store.pending.first?.state == .sent)
        #expect(store.pending.first?.requiredEffort == nil)
        #expect(store.pending.first?.id != failed.id)
    }

    /// "Other Model…" (#1644): a typed ID goes through …/modes like a listed one; the list it comes with is the
    /// session's launcher's.
    @Test func aTypedModelGoesThroughModes() async throws {
        let hub = FakeHub()
        hub.on("POST", "/api/v1/sessions/box/s1/modes", json: #"{"ok":true}"#)
        try await hub.client.setModes(SessionKey(machineID: "box", sessionID: "s1"), model: "~vendor/model-latest[1m]")
        let body = try #require(hub.sent("POST", "/api/v1/sessions/box/s1/modes").first?.body)
        #expect(try JSONSerialization.jsonObject(with: body) as? [String: String] == ["model": "~vendor/model-latest[1m]"])
        #expect(SessionModes.launcher(try session()) == "claude")
        #expect(SessionModes.launcher(try session(#","launcher":"cc_gateway""#)) == "cc_gateway")
    }

    /// The composer menus that type `/model` (the shell's, BeautifulUI's): "Other Model…" opens the field instead (its
    /// ID goes through …/modes, never as a message), the running model does nothing, another row is typed.
    @MainActor @Test func theComposerMenuOffersOtherModelAsAnAction() {
        #expect(!PromptItems.claudeModels.contains { $0.id == PromptItems.otherModel.id })
        #expect(PromptItems.modelPick(PromptItems.otherModel.id, running: "claude-opus-5-5") == .typeID)
        #expect(PromptItems.modelPick(PromptItems.otherModel.id, running: "vendor/model-x") == .typeID)
        #expect(PromptItems.modelPick("opus", running: "claude-opus-5-5") == .keep)
        #expect(PromptItems.modelPick("sonnet", running: "claude-opus-5-5") == .send("/model sonnet"))
        #expect(PromptItems.modelPick("default", running: nil) == .keep)
        #expect(PromptItems.modelPick("default", running: "vendor/model-x") == .send("/model default"))
        #expect(PromptItems.modelPick("vendor/model-x", running: "vendor/model-x") == .keep)
    }
}
