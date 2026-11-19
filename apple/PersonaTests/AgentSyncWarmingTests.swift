import Foundation
import Testing
@testable import Persona
import PersonaWire

/// The Agents' chats that are not open are warmed only while an Agents list is in sight (#10582): out of sight nothing
/// is read or written, coming back reads what moved at once, leaving stops a read on its way without counting it as a
/// failure, and a read that changes nothing shown leaves the cache file as it was.
@MainActor
@Suite(.serialized)
struct AgentSyncWarmingTests {
    /// The hub's side: every `/items` read, answered with `answer` (the latest page or what came after a cursor), or
    /// held until its task is cancelled. Other requests are refused.
    final class Hub: @unchecked Sendable {
        private let lock = NSLock()
        private var queries: [[String: String]] = []
        private var holding = false
        private var page: [String: JSONValue] = [:]

        var reads: [[String: String]] { lock.withLock { queries } }
        func hold(_ on: Bool) { lock.withLock { holding = on } }
        func answer(_ object: [String: JSONValue]) { lock.withLock { page = object } }

        var transport: APIClient.Transport {
            { [self] req in try await serve(req) }
        }

        private func serve(_ req: LocalRequest) async throws -> Data {
            guard req.method == "GET", req.path.hasSuffix("/items") else { throw APIError(status: 404, message: "not here") }
            let (held, object) = lock.withLock { () -> (Bool, [String: JSONValue]) in
                queries.append(req.query)
                return (holding, page)
            }
            if held { try await Task.sleep(for: .seconds(60)) }
            return try JSONEncoder().encode(JSONValue.object(object))
        }
    }

    private let machine = "warm-" + String(UUID().uuidString.prefix(8))
    private var key: SessionKey { SessionKey(machineID: machine, sessionID: "s1") }
    private let folder = FileManager.default.temporaryDirectory.appending(path: "agent-sync-warming-\(UUID().uuidString)")

    private static func line(_ seq: Int, _ role: String, _ text: String) -> JSONValue {
        .object(["id": .string("l\(seq)#0"), "seq": .number(Double(seq)), "ts": .string("2026-01-02T03:04:05Z"),
                 "role": .string(role), "kind": .string("text"), "text": .string(text)])
    }

    /// A model connected to `hub`, with `AgentSync` working for it and no list in sight.
    private func connected(_ hub: Hub) throws -> AppModel {
        try #require(!AgentSync.shared.warming, "the test host's own window shows an Agents list")
        let model = AppModel(stateFolder: folder)
        model.linkHooksForTests = (transport: hub.transport, socket: { SyncSocket() })
        model.connectOnly(ServerConfig(url: URL(string: "https://hub.invalid")!, token: "t"))
        model.linksUpForTests = true
        AgentSync.shared.forgetChatsForTests()
        AgentSync.shared.model = { [weak model] in model ?? AppModel.shared }
        return model
    }

    private func finish(_ model: AppModel, sight: UUID?) {
        if let sight { AgentSync.shared.listInSight(sight, false) }
        AgentSync.shared.model = { AppModel.shared }
        for link in model.links { link.live.disconnect() }
        try? FileManager.default.removeItem(at: folder)
    }

    /// The hub's list for the machine: the one session, `state` (working, idle) at `rev`.
    private func list(_ model: AppModel, state: String, rev: Int) throws {
        let row: JSONValue = .object([
            "machine_id": .string(machine), "session_id": .string("s1"), "title": .string("Chat"), "cwd": .string("/home/user/project"),
            "updated_at": .string(Date.now.ISO8601Format()), "rev": .number(Double(rev)), "state": .string(state),
            "status": .string(state == "working" ? "working" : "idle"),
        ])
        let frame = try JSONEncoder().encode(JSONValue.object(["type": .string("sessions"), "machine_id": .string(machine),
                                                               "sessions": .array([row])]))
        let ev = try #require(ServerEvent.decode(frame))
        let group = model.groupIDs.first ?? ""
        model.handle(ev.stamped(group), group: group)
    }

    private func until(_ what: String, within seconds: Double = 5, _ done: () -> Bool,
                       sourceLocation: SourceLocation = #_sourceLocation) async throws {
        let end = Date.now.addingTimeInterval(seconds)
        while !done() {
            if Date.now > end {
                Issue.record("timed out: \(what)", sourceLocation: sourceLocation)
                return
            }
            try await Task.sleep(for: .milliseconds(20))
        }
    }

    /// Long enough for a list to be flushed and a plan to run (300 ms after the last event) and pull, had it pulled.
    private func settle() async throws { try await Task.sleep(for: .milliseconds(900)) }

    @Test func outOfSightNothingIsReadAndComingIntoSightReadsWhatMovedAtOnce() async throws {
        let hub = Hub()
        hub.answer(["items": .array([Self.line(1, "user", "Plan a trip."), Self.line(2, "assistant", "A hike.")]),
                    "has_more": .bool(false), "history": .string("h1"), "to_seq": .number(2)])
        let model = try connected(hub)
        let sight = UUID()
        defer { finish(model, sight: sight) }

        try list(model, state: "idle", rev: 100)
        try await settle()
        #expect(hub.reads.isEmpty, "no Agents list in sight: nothing warmed")

        AgentSync.shared.listInSight(sight, true)
        try await until("the latest page read once a list is in sight") { hub.reads.count == 1 }
        #expect(hub.reads.first?["after_seq"] == nil) // nothing cached yet: the latest page
        try await until("the page cached") { ChatCache.load(key, in: model.stateFolder)?.covered == 2 }

        AgentSync.shared.listInSight(sight, false)
        hub.answer(["items": .array([Self.line(3, "user", "And Sunday?")]), "from_seq": .number(2), "to_seq": .number(3),
                    "more_after": .bool(false), "history": .string("h1")])
        try list(model, state: "idle", rev: 200)
        try await settle()
        #expect(hub.reads.count == 1, "out of sight again: the moved transcript waits")

        AgentSync.shared.listInSight(sight, true)
        try await until("what came after the cache read as the list is back") { hub.reads.count == 2 }
        #expect(hub.reads.last?["after_seq"] == "2")
        try await until("the line added to the cache") { ChatCache.load(key, in: model.stateFolder)?.covered == 3 }
    }

    @Test func leavingStopsAReadWithoutHoldingTheNextOne() async throws {
        let hub = Hub()
        hub.answer(["items": .array([Self.line(1, "user", "Fix the build.")]), "has_more": .bool(false), "to_seq": .number(1)])
        hub.hold(true)
        let model = try connected(hub)
        let sight = UUID()
        defer { finish(model, sight: sight) }

        try list(model, state: "working", rev: 100)
        AgentSync.shared.listInSight(sight, true)
        try await until("a read on its way") { hub.reads.count == 1 }
        AgentSync.shared.listInSight(sight, false)
        try await settle()
        #expect(ChatCache.load(key, in: model.stateFolder) == nil, "the stopped read wrote nothing")

        // A working agent is read at most once a minute and a failed read waits 30 s: a stopped one is neither.
        hub.hold(false)
        AgentSync.shared.listInSight(sight, true)
        try await until("read again at once", within: 3) { hub.reads.count == 2 }
        try await until("the page cached") { ChatCache.load(key, in: model.stateFolder)?.covered == 1 }
    }

    @Test func aReadThatChangesNothingShownLeavesTheCacheFile() async throws {
        let hub = Hub()
        let model = try connected(hub)
        let sight = UUID()
        defer { finish(model, sight: sight) }
        let lines = [Self.line(1, "user", "Plan a trip."), Self.line(2, "assistant", "A hike.")]
        let items = try JSON.decoder.decode([Item].self, from: JSONEncoder().encode(JSONValue.array(lines)))
        ChatCache.save(key, items: items, hasMore: false, history: "h1", covered: 2, in: model.stateFolder)

        // The transcript grew by lines that show nothing.
        hub.answer(["items": .array([]), "from_seq": .number(2), "to_seq": .number(5), "more_after": .bool(false),
                    "history": .string("h1")])
        try list(model, state: "idle", rev: 100)
        AgentSync.shared.listInSight(sight, true)
        try await until("the read after the cache") { hub.reads.count == 1 }
        try await settle()
        #expect(ChatCache.load(key, in: model.stateFolder)?.covered == 2, "not written again for nothing")

        // Then by a line that shows: written, from the cursor the file still has.
        hub.answer(["items": .array([Self.line(6, "user", "And Sunday?")]), "from_seq": .number(2), "to_seq": .number(6),
                    "more_after": .bool(false), "history": .string("h1")])
        try list(model, state: "idle", rev: 200)
        try await until("the next read") { hub.reads.count == 2 }
        #expect(hub.reads.last?["after_seq"] == "2")
        try await until("the line added to the cache") { ChatCache.load(key, in: model.stateFolder)?.covered == 6 }
        #expect(ChatCache.load(key, in: model.stateFolder)?.items.map(\.seq) == [1, 2, 6])
    }
}
