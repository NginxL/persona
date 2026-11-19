import Foundation
import Testing
@testable import Persona

@MainActor
struct MachineMetricsStoreTests {
    private var roster: ManagerRoster {
        ManagerRoster(managers: [
            HubManager(id: "primary", name: "Primary", online: true, isDefault: true),
            HubManager(id: "secondary", name: "Secondary", online: true),
            HubManager(id: "offline", name: "Offline", online: false),
            HubManager(id: "installing", name: "Installing", online: true, installing: Date(timeIntervalSince1970: 10)),
        ], defaultID: "primary")
    }

    private func body(_ rows: [(String, Int, Int, Int)]) -> Data {
        let values = rows.map { row in
            #"{"machine_id":"\#(row.0)","online":true,"latest":{"t":10,"cpu":\#(row.1),"mem_used":\#(row.2),"mem_total":1000,"swap_used":0,"swap_total":0,"disk_used":\#(row.3),"disk_total":1000},"points":[]}"#
        }
        return Data("{\"machines\":[\(values.joined(separator: ","))]}".utf8)
    }

    @Test func readsEachOnlineManagerAndKeepsOnlyTheFailedManagersPreviousSamples() async {
        let hub = FakeHub()
        let replies = ManagerMetricsReplies()
        hub.on("GET", "/api/v1/managers", json: #"""
        {"default":"primary","managers":[
          {"id":"primary","name":"Primary","online":true,"default":true},
          {"id":"secondary","name":"Secondary","online":true,"default":false},
          {"id":"offline","name":"Offline","online":false,"default":false},
          {"id":"installing","name":"Installing","online":true,"default":false,"installing":"2026-11-11T01:00:00Z"}]}
        """#)
        hub.on("GET", "/api/v1/metrics") { request in
            let manager = request.headers[APIClient.managerHeader] ?? "primary"
            if let status = replies.refused[manager] { throw APIError(status: status, message: "Unavailable") }
            switch manager {
            case "primary": return body([("first", replies.primaryCPU, 111, 10)])
            case "secondary": return body(replies.emptySecondary ? [] : [("second", 22, 222, 20)])
            default: throw APIError(status: 400, message: "Unexpected manager")
            }
        }
        let model = MachineMetricsLoader()
        let apis = ["group": hub.client], rosters = ["group": roster]
        let first = MachineRef(group: "group", id: "first"), second = MachineRef(group: "group", id: "second")
        await model.load(groups: ["group"], apis: apis, rosters: [:])
        #expect(model.metrics.count == 2)
        #expect(model.metrics[first]?.latest?.cpu == 11)
        #expect(model.metrics[second]?.latest?.cpu == 22)
        #expect(model.metrics[second]?.latest?.memUsed == 222)
        #expect(model.metrics[second]?.latest?.diskUsed == 20)
        let requests = hub.sent("GET", "/api/v1/metrics")
        #expect(requests.count == 2)
        #expect(hub.sent("GET", "/api/v1/managers").count == 1)
        #expect(requests.filter { $0.headers[APIClient.managerHeader] == nil }.count == 1, "the default stays bare")
        #expect(Set(requests.compactMap { $0.headers[APIClient.managerHeader] }) == Set(["secondary"]))
        let previous = model.metrics[second]
        replies.primaryCPU = 33
        replies.refused["secondary"] = 503
        await model.load(groups: ["group"], apis: apis, rosters: rosters)
        #expect(model.metrics[first]?.latest?.cpu == 33)
        #expect(model.metrics[second] == previous)
        #expect(model.unmonitoredGroups.isEmpty)
        var offline = roster
        offline.managers = offline.managers.map { var manager = $0; manager.online = false; return manager }
        let callsBeforeOffline = hub.sent("GET", "/api/v1/metrics").count
        await model.load(groups: ["group"], apis: apis, rosters: ["group": offline])
        #expect(hub.sent("GET", "/api/v1/metrics").count == callsBeforeOffline)
        #expect(model.metrics.isEmpty)
        await model.load(groups: ["group"], apis: apis, rosters: rosters)
        #expect(model.metrics[second] == previous)
        replies.refused = ["primary": 404, "secondary": 404]
        await model.load(groups: ["group"], apis: apis, rosters: rosters)
        #expect(model.unmonitoredGroups == Set(["group"]))
        #expect(model.metrics[second] == previous)
        replies.refused.removeValue(forKey: "primary")
        replies.primaryCPU = 44
        await model.load(groups: ["group"], apis: apis, rosters: rosters)
        #expect(model.unmonitoredGroups.isEmpty)
        #expect(model.metrics[first]?.latest?.cpu == 44)
        #expect(model.metrics[second] == previous)
        replies.refused.removeValue(forKey: "secondary")
        replies.emptySecondary = true
        await model.load(groups: ["group"], apis: apis, rosters: rosters)
        #expect(model.metrics[second] == nil)
    }

    @Test func duplicateMachineIdsFollowRosterOrderInsteadOfResponseOrder() async {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/metrics") { request in
            body([("same", request.headers[APIClient.managerHeader] == nil ? 11 : 22, 111, 10)])
        }
        let model = MachineMetricsLoader()
        await model.load(groups: ["group"], apis: ["group": hub.client], rosters: ["group": roster])
        #expect(model.metrics.count == 1)
        #expect(model.metrics[MachineRef(group: "group", id: "same")]?.latest?.cpu == 11)
    }

    @Test func groupsKeepTheirOwnSamplesAndAPairingReplacementDiscardsTheOldCache() async {
        let firstHub = FakeHub(url: "https://first.invalid"), secondHub = FakeHub(url: "https://second.invalid")
        firstHub.on("GET", "/api/v1/metrics") { _ in body([("same", 11, 111, 10)]) }
        secondHub.on("GET", "/api/v1/metrics") { _ in body([("same", 22, 222, 20)]) }
        let model = MachineMetricsLoader()
        await model.load(groups: ["first", "second"], apis: ["first": firstHub.client, "second": secondHub.client], rosters: [:])
        let first = MachineRef(group: "first", id: "same"), second = MachineRef(group: "second", id: "same")
        #expect(model.metrics[first]?.latest?.cpu == 11)
        #expect(model.metrics[second]?.latest?.cpu == 22)
        let replacement = FakeHub(url: "https://replacement.invalid")
        replacement.on("GET", "/api/v1/metrics") { _ in throw APIError(status: 503, message: "Unavailable") }
        await model.load(groups: ["first"], apis: ["first": replacement.client, "second": secondHub.client], rosters: [:])
        #expect(model.metrics[first] == nil)
        #expect(model.metrics[second]?.latest?.cpu == 22)
    }

    @Test func anOfflineRosterMakesNoRequestAndTheLegacyHubStillUsesItsDefault() async {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/metrics") { _ in body([("first", 11, 111, 10)]) }
        let model = MachineMetricsLoader()
        await model.load(groups: ["group"], apis: ["group": hub.client], rosters: [:])
        #expect(hub.sent("GET", "/api/v1/metrics").count == 1)
        #expect(hub.sent("GET", "/api/v1/metrics").first?.headers[APIClient.managerHeader] == nil)
        var offline = roster
        offline.managers = offline.managers.map { var manager = $0; manager.online = false; return manager }
        await model.load(groups: ["group"], apis: ["group": hub.client], rosters: ["group": offline])
        #expect(hub.sent("GET", "/api/v1/metrics").count == 1)
        #expect(model.metrics.isEmpty)
    }

    @Test func aFailedRosterReadKeepsTheLastNumbersWithoutAskingAnUnscopedManager() async {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/metrics") { _ in body([("first", 11, 111, 10)]) }
        let model = MachineMetricsLoader()
        await model.load(groups: ["group"], apis: ["group": hub.client], rosters: ["group": roster])
        let previous = model.metrics
        let requests = hub.sent("GET", "/api/v1/metrics").count
        hub.on("GET", "/api/v1/managers") { _ in throw APIError(status: 503, message: "Unavailable") }
        await model.load(groups: ["group"], apis: ["group": hub.client], rosters: [:])
        #expect(model.metrics == previous)
        #expect(hub.sent("GET", "/api/v1/metrics").count == requests)
    }

    @Test func anOlderPollCannotOverwriteTheNewerPoll() async {
        let gate = MetricsReadGate(), hub = FakeHub()
        hub.on("GET", "/api/v1/metrics") { _ in body([("first", 99, 111, 10)]) }
        var slow = hub.client
        let oldData = body([("first", 11, 111, 10)])
        slow.transport = { request in
            if request.path == "/api/v1/managers" { throw APIError(status: 404, message: "Unsupported") }
            await gate.pause()
            return oldData
        }
        let model = MachineMetricsLoader()
        let old = Task { await model.load(groups: ["group"], apis: ["group": slow], rosters: [:]) }
        let started = await gate.started()
        #expect(started)
        await model.load(groups: ["group"], apis: ["group": hub.client], rosters: [:])
        await gate.release()
        await old.value
        #expect(model.metrics[MachineRef(group: "group", id: "first")]?.latest?.cpu == 99)
    }

    @Test func cancellationKeepsLateSamplesOutOfThePage() async {
        let gate = MetricsReadGate(), hub = FakeHub()
        var slow = hub.client
        let data = body([("first", 11, 111, 10)])
        slow.transport = { request in
            if request.path == "/api/v1/managers" { throw APIError(status: 404, message: "Unsupported") }
            await gate.pause()
            return data
        }
        let model = MachineMetricsLoader()
        let read = Task { await model.load(groups: ["group"], apis: ["group": slow], rosters: [:]) }
        let started = await gate.started()
        #expect(started)
        read.cancel()
        await gate.release()
        await read.value
        #expect(model.metrics.isEmpty)
    }
}

@MainActor
private final class ManagerMetricsReplies {
    var primaryCPU = 11
    var refused: [String: Int] = [:]
    var emptySecondary = false
}

private actor MetricsReadGate {
    private var waiting: CheckedContinuation<Void, Never>?
    private var observer: CheckedContinuation<Bool, Never>?
    private var didStart = false
    private var released = false

    func pause() async {
        if released { return }
        await withCheckedContinuation { continuation in
            waiting = continuation
            didStart = true
            observer?.resume(returning: true)
            observer = nil
        }
    }

    func started() async -> Bool {
        if didStart { return true }
        let timeout = Task {
            do { try await Task.sleep(for: .seconds(5)) } catch { return }
            expire()
        }
        defer { timeout.cancel() }
        return await withCheckedContinuation { observer = $0 }
    }

    private func expire() { observer?.resume(returning: false); observer = nil }

    func release() { released = true; waiting?.resume(); waiting = nil }
}
