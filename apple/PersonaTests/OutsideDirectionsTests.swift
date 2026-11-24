import Foundation
import SwiftUI
import Testing
@testable import Persona
import PersonaWire

/// Outside reporting directions (#8236): additive wire decoding, the sidebar's direction-ID projection, the existing
/// task groupings, and the rule that an outside id is not a temporarily missing Persona conversation.
@Suite(.serialized)
@MainActor
struct OutsideDirectionsTests {
    private func session(_ id: String, machine: String = "box") throws -> Session {
        let object: [String: Any] = [
            "machine_id": machine,
            "session_id": id,
            "status": "idle",
            "state": "idle",
            "title": id,
            "assistant": true,
        ]
        return try JSON.decoder.decode(Session.self, from: JSONSerialization.data(withJSONObject: object))
    }

    private func model(_ hub: FakeHub) throws -> AppModel {
        let transport = try #require(hub.client.transport)
        let folder = FileManager.default.temporaryDirectory.appending(path: "outside-directions-" + UUID().uuidString)
        let model = AppModel(stateFolder: folder)
        model.linkHooksForTests = (transport: transport, socket: { SyncSocket() })
        model.connectOnly(.demo)
        return model
    }

    @Test func treeDecodesOutsideDirectionsLossily() throws {
        let raw = #"""
        {"directions":[
          {"id":"old","name":"Old","counts":{"needs_you":1}},
          {"id":"bound","name":"Bound","external":true,"machine_id":"m","session_id":"s",
           "counts":{"working":1,"needs_you":2},"running":true},
          {"id":"targetless","name":"Targetless","external":true},
          {"id":"partial","name":"Partial","external":true,"machine_id":"m"},
          {"id":"blank","name":"Blank","external":true,"machine_id":" ","session_id":"s"},
          {"id":"blank-session","name":"Blank session","external":true,"machine_id":"m","session_id":" "},
          {"id":"false","name":"False","external":false,"machine_id":"m","session_id":"s"},
          {"id":"wrong","name":"Wrong","external":"yes","machine_id":7,"session_id":false}
        ]}
        """#
        let tree = try JSON.decoder.decode(DirectionTree.self, from: Data(raw.utf8))
        let byID = Dictionary(uniqueKeysWithValues: tree.directions.map { ($0.id, $0) })
        #expect(byID["old"]?.isExternal == false)
        #expect(byID["old"]?.externalTarget == nil)
        #expect(byID["bound"]?.isExternal == true)
        #expect(byID["bound"]?.externalTarget == SessionKey(machineID: "m", sessionID: "s"))
        #expect(byID["bound"]?.counts.needsYou == 2)
        #expect(byID["bound"]?.running == true)
        for id in ["targetless", "partial", "blank", "blank-session"] {
            #expect(byID[id]?.isExternal == true)
            #expect(byID[id]?.externalTarget == nil)
        }
        #expect(byID["false"]?.isExternal == false)
        #expect(byID["false"]?.externalTarget == nil)
        #expect(byID["wrong"]?.isExternal == false)
        #expect(byID["wrong"]?.externalTarget == nil)
        #expect(Set(tree.outsideDirections.keys) == ["bound", "targetless", "partial", "blank", "blank-session"])
        #expect(!tree.hasUnlistedConversation(known: ["old", "bound", "targetless", "partial", "blank", "blank-session", "false", "wrong"]))
        #expect(tree.hasUnlistedConversation(known: []), "an ordinary unknown leader still needs its conversation read")
    }

    @Test func leadersDecodeOutsideDirectionsAndOlderPayloads() async throws {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/assistant/leaders", json: #"""
        {"leaders":[
          {"id":"old","name":"Old"},
          {"id":"bound","name":"Bound","external":true,"machine_id":"m","session_id":"s"},
          {"id":"partial","name":"Partial","external":true,"machine_id":"m"},
          {"id":"blank","name":"Blank","external":true,"machine_id":" ","session_id":"s"},
          {"id":"ended","name":"Ended","external":true,"machine_id":"m","session_id":"s","ended":true},
          {"id":"false","name":"False","external":false,"machine_id":"m","session_id":"s"},
          {"id":"wrong","name":"Wrong","external":"yes","machine_id":7,"session_id":false}
        ]}
        """#)
        let list = try await hub.client.leaders()
        #expect(list.count == 7, "a wrongly typed new optional field must not reject the whole list")
        #expect(list[0].external == nil && list[0].machineID == nil && list[0].sessionID == nil)
        let outside = APIClient.outsideDirections(list)
        #expect(Set(outside.keys) == ["bound", "partial", "blank"])
        #expect(outside["bound"]?.externalTarget == SessionKey(machineID: "m", sessionID: "s"))
        #expect(outside["partial"]?.externalTarget == nil)
        #expect(outside["blank"]?.externalTarget == nil)
        #expect(hub.sent("GET", "/api/v1/assistant/leaders").count == 1)
    }

    @Test func sidebarKeepsHubOrderAndAdmitsOnlyKnownOutsideDirections() throws {
        let all = [try session("main"), try session("lead-b"), try session("lead-a")]
        let main = try #require(all.first?.key)
        let bound = DirectionTree.Direction(
            id: "dir-bound", name: "Bound", counts: .init(needsYou: 2), running: true,
            external: true, machineID: "m", sessionID: "s")
        let targetless = DirectionTree.Direction(id: "dir-targetless", name: "Targetless", external: true)
        let partial = DirectionTree.Direction(id: "dir-partial", name: "Partial", external: true, machineID: "m")
        let outside = [
            bound.id: bound,
            targetless.id: targetless,
            partial.id: partial,
            // Conflicting metadata never replaces a real conversation, and the main row never becomes a direction.
            "lead-a": DirectionTree.Direction(id: "lead-a", external: true, machineID: "x", sessionID: "y"),
            "main": DirectionTree.Direction(id: "main", external: true),
        ]
        let entries = AssistantSidebarGroups.directions(
            all,
            leaders: ["lead-a", bound.id, "gone", targetless.id, partial.id, "main", "lead-b"],
            main: main,
            outside: outside)
        #expect(entries.map(\.id) == ["lead-a", bound.id, targetless.id, partial.id, "lead-b"])
        #expect(entries[0].session?.sessionID == "lead-a")
        #expect(entries[0].canDisclose)
        #expect(entries[1].id == bound.id, "row identity is the direction id, never the bound Agent's session id")
        #expect(entries[1].target == SessionKey(machineID: "m", sessionID: "s"))
        #expect(entries[1].canDisclose)
        #expect(entries[1].outside?.counts.needsYou == 2)
        #expect(entries[1].outside?.running == true)
        #expect(entries[2].session == nil && entries[2].target == nil && !entries[2].canDisclose)
        #expect(entries[3].session == nil && entries[3].target == nil && !entries[3].canDisclose)

        let outsideOnly = AssistantSidebarGroups.directions(
            [], leaders: [targetless.id, bound.id], main: nil, outside: outside)
        #expect(outsideOnly.map(\.id) == [targetless.id, bound.id])
        #expect(AssistantSidebarGroups.directions([], leaders: ["gone"], main: nil, outside: outside).isEmpty)
    }

    @Test func outsideDirectionsWithTasksJoinTheExistingGroupings() throws {
        let node = DirectionTree.AgentNode(machineID: "m", sessionID: "agent", title: "Builder", state: "working")
        let task = DirectionTree.TaskNode(id: "outside-task", title: "Ship", state: .working, agents: [node])
        let bound = DirectionTree.Direction(
            id: "dir-bound", name: "Bound", counts: .init(working: 1), running: true, tasks: [task],
            external: true, machineID: "m", sessionID: "target")
        let empty = DirectionTree.Direction(id: "dir-empty", name: "Empty", external: true)
        let tree = DirectionTree(directions: [bound, empty])
        let todos = ["outside-task": TodoItem(id: "outside-task", title: "Ship")]
        #expect(DirectionTodoSections.build(tree, todos: todos).map(\.id) == [bound.id])

        let agent = try session("agent", machine: "m")
        let layout = DirectionAgentSections.build(
            tree, shown: [agent], known: [agent.key], group: "", isMuted: { _ in false })
        #expect(layout.entries.map(\.key) == ["direction:" + bound.id])
        #expect(layout.entries.first?.tasks.first?.lines.first?.key == agent.key)
        #expect(layout.others.isEmpty)
    }

    @Test func outsideIDsDoNotTriggerMissingConversationReads() async throws {
        let chosen = UserDefaults.standard.object(forKey: "assistant.current")
        defer { UserDefaults.standard.set(chosen, forKey: "assistant.current") }
        let zone = TimeZone.current.identifier
        let hub = FakeHub()
        hub.on("GET", "/api/v1/assistant", json: #"""
        {"machine_id":"box","launcher":"claude","cwd":"/a","conversations":[],"main":"main",
         "timezone":"\#(zone)","leaders":["dir-outside"]}
        """#)
        hub.on("GET", "/api/v1/assistant/leaders", json: #"""
        {"leaders":[{"id":"dir-outside","name":"Outside","external":true}]}
        """#)
        hub.on("GET", "/api/v1/assistant/directions/tree", json: #"""
        {"directions":[
          {"id":"dir-outside","name":"Outside","external":true,"counts":{},"tasks":[]},
          {"id":"main","name":"","secretary":true,"counts":{},"tasks":[]}
        ]}
        """#)
        let model = try model(hub)
        await model.refreshAssistant()
        await model.refreshDirectionTree()
        #expect(model.outsideDirections["dir-outside"]?.isExternal == true)
        let settledReads = hub.sent("GET", "/api/v1/assistant").count
        await model.refreshDirectionTree()
        #expect(hub.sent("GET", "/api/v1/assistant").count == settledReads)

        hub.on("GET", "/api/v1/assistant/directions/tree", json: #"""
        {"directions":[{"id":"new-conversation","name":"New","counts":{},"tasks":[]}]}
        """#)
        await model.refreshDirectionTree()
        #expect(hub.sent("GET", "/api/v1/assistant").count == settledReads + 1,
                "a genuinely unknown conversation-backed leader still refreshes the assistant")
    }

    @Test func aLateFlatAnswerCannotEraseTreeCountsAndRunning() async throws {
        let chosen = UserDefaults.standard.object(forKey: "assistant.current")
        defer { UserDefaults.standard.set(chosen, forKey: "assistant.current") }
        let zone = TimeZone.current.identifier
        let gate = OutsideDirectionReadGate()
        defer { gate.finish() }
        let hub = FakeHub()
        hub.on("GET", "/api/v1/assistant", json: #"""
        {"machine_id":"box","launcher":"claude","cwd":"/a","conversations":[],"main":"main",
         "timezone":"\#(zone)","leaders":["dir-outside"]}
        """#)
        hub.onAsync("GET", "/api/v1/assistant/leaders") { _ in
            await gate.wait()
            return Data(#"{"leaders":[{"id":"dir-outside","name":"Outside","external":true,"machine_id":"m","session_id":"s"}]}"#.utf8)
        }
        hub.on("GET", "/api/v1/assistant/directions/tree", json: #"""
        {"directions":[{"id":"dir-outside","name":"Outside","external":true,"machine_id":"m","session_id":"s",
         "counts":{"working":1,"needs_you":2},"running":true,"tasks":[]}]}
        """#)
        let model = try model(hub)
        let refresh = Task { await model.refreshAssistant() }
        try await waitForRefreshTest { gate.started }
        await model.refreshDirectionTree() // the tree lands while the flat answer is still held
        #expect(model.outsideDirections["dir-outside"]?.counts.needsYou == 2)
        gate.finish()
        await refresh.value
        #expect(model.outsideDirections["dir-outside"]?.counts.needsYou == 2,
                "the late flat answer must not replace the tree's counts with zero")
        #expect(model.outsideDirections["dir-outside"]?.running == true)
        #expect(model.outsideDirections["dir-outside"]?.externalTarget == SessionKey(machineID: "m", sessionID: "s"))
    }

    @Test func aTree404RestoresFlatMetadataWithoutTreeCounts() async throws {
        let chosen = UserDefaults.standard.object(forKey: "assistant.current")
        defer { UserDefaults.standard.set(chosen, forKey: "assistant.current") }
        let zone = TimeZone.current.identifier
        let hub = FakeHub()
        hub.on("GET", "/api/v1/assistant", json: #"""
        {"machine_id":"box","launcher":"claude","cwd":"/a","conversations":[],"main":"main",
         "timezone":"\#(zone)","leaders":["dir-outside"]}
        """#)
        hub.on("GET", "/api/v1/assistant/leaders", json: #"""
        {"leaders":[{"id":"dir-outside","name":"Outside","external":true,"machine_id":"m","session_id":"s"}]}
        """#)
        hub.on("GET", "/api/v1/assistant/directions/tree", json: #"""
        {"directions":[{"id":"dir-outside","name":"Outside","external":true,"machine_id":"m","session_id":"s",
         "counts":{"working":1,"needs_you":2},"running":true,"tasks":[]}]}
        """#)
        let model = try model(hub)
        await model.refreshAssistant()
        await model.refreshDirectionTree()
        #expect(model.outsideDirections["dir-outside"]?.counts.needsYou == 2)
        hub.on("GET", "/api/v1/assistant/directions/tree") { _ in throw APIError(status: 404, message: "older hub") }
        await model.refreshDirectionTree()
        #expect(model.directionTree == nil)
        let fallback = model.outsideDirections["dir-outside"]
        #expect(fallback?.name == "Outside")
        #expect(fallback?.externalTarget == SessionKey(machineID: "m", sessionID: "s"))
        #expect(fallback?.counts == DirectionTree.Counts())
        #expect(fallback?.running == false)
    }

    @Test func removingTheCurrentHubClearsOutsideDirectionsBeforeTheNextHubAnswers() async throws {
        let chosen = UserDefaults.standard.object(forKey: "assistant.current")
        defer { UserDefaults.standard.set(chosen, forKey: "assistant.current") }
        // `removeGroup` saves the registry to this simulator's keychain. Put back what was there, or every later
        // `AppModel()` starts paired to this test's "other" group and opens agents as another hub's (#14145).
        let paired = HubStore.read()
        defer { if case .registry(let registry) = paired { HubStore.save(registry) } }
        let zone = TimeZone.current.identifier
        let hub = FakeHub()
        hub.on("GET", "/api/v1/assistant", json: #"""
        {"machine_id":"box","launcher":"claude","cwd":"/a","conversations":[],"main":"main",
         "timezone":"\#(zone)","leaders":["dir-outside"]}
        """#)
        hub.on("GET", "/api/v1/assistant/leaders", json: #"""
        {"leaders":[{"id":"dir-outside","name":"Outside","external":true}]}
        """#)
        hub.on("GET", "/api/v1/assistant/directions/tree", json: #"""
        {"directions":[{"id":"dir-outside","name":"Outside","external":true,"counts":{"needs_you":2},"running":true}]}
        """#)
        let configA = ServerConfig(url: URL(string: "https://a.invalid")!, token: "a")
        let configB = ServerConfig(url: URL(string: "https://b.invalid")!, token: "b")
        let model = AppModel(stateFolder: FileManager.default.temporaryDirectory.appending(path: "outside-remove-" + UUID().uuidString))
        model.forgetRestoredForTests() // no assistant pick or hub snapshot from another test on this simulator
        // A restored default-group link predates this test's hook. buildLinks retains links by group, so this makes
        // the original network escape deterministic instead of depending on which suites ran first (#8236).
        model.registry = HubRegistry(hubs: [
            PairedHub(id: "a", config: configA, group: "", hubID: "A"),
        ])
        model.buildLinks()
        #expect(model.link("")?.api.transport == nil)
        model.linkHooksForTests = (transport: try #require(hub.client.transport), socket: { SyncSocket() })
        model.registry = HubRegistry(hubs: [
            PairedHub(id: "a", config: configA, group: "", hubID: "A"),
            PairedHub(id: "b", config: configB, group: "other", hubID: "B"),
        ])
        model.buildLinks()
        // Hooks apply to links made from now on; backfill a restored link retained for the default group too.
        for link in model.links { link.transportForTests = hub.client.transport }
        #expect(model.links.allSatisfy { $0.api.transport != nil })
        model.hubGroup = ""
        await model.refreshAssistant()
        await model.refreshDirectionTree()
        #expect(!model.outsideDirections.isEmpty)

        model.removeGroup("")
        #expect(model.hubGroup == "other")
        #expect(model.outsideDirections.isEmpty)
        #expect(model.assistantLeaders.isEmpty)
    }
}

@MainActor private final class OutsideDirectionReadGate {
    private var continuation: CheckedContinuation<Void, Never>?
    private(set) var started = false
    func wait() async {
        started = true
        await withCheckedContinuation { continuation = $0 }
    }
    func finish() {
        continuation?.resume()
        continuation = nil
    }
}
