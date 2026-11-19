import Foundation
import Testing
@testable import Persona
import PersonaWire

/// Machines with several managers (#4031): each connected hub is asked once per installed, online manager, in the
/// roster's order (the default's request names none, as elsewhere). A manager's last good snapshot survives its own
/// failure alone, and a success replaces that snapshot even when it is empty. Legacy hubs keep their one bare read.
/// Loads are fenced by request, hub group and pairing; Settings keeps its captured hub and manager.
@MainActor
struct MachineMetricsLoadingTests {
    private static let path = "/api/v1/metrics"
    private static let header = APIClient.managerHeader

    private struct Overview: Encodable {
        var machines: [MachineMetrics]
    }

    private static let roster = ManagerRoster(managers: [
        // Deliberately not the default-first order: `installed` owns the merge order.
        HubManager(id: "work", name: "Work", online: true),
        HubManager(id: "offline", name: "Offline", online: false),
        HubManager(id: "installing", name: "Installing", online: true,
                   installing: Date(timeIntervalSince1970: 4_102_444_800)),
        HubManager(id: "assistant", name: "Personal", online: true, isDefault: true),
    ], defaultID: "assistant")

    private static func client(_ hub: FakeHub, group: String, token: String? = nil) -> APIClient {
        var client = hub.client
        client.group = group
        if let token { client.config.token = token }
        return client
    }

    private static func machine(_ id: String, cpu: Double, memory: Int64, disk: Int64) -> MachineMetrics {
        MachineMetrics(machineID: id, online: true,
                       latest: MetricSample(t: 1_790_003_600, cpu: cpu, memUsed: memory, memTotal: 100,
                                            swapUsed: 0, swapTotal: 0, diskUsed: disk, diskTotal: 100),
                       points: [])
    }

    private static func overview(_ machines: [MachineMetrics]) throws -> Data {
        try JSON.encoder.encode(Overview(machines: machines))
    }

    private static func ref(_ id: String, group: String = "home") -> MachineRef {
        MachineRef(group: group, id: id)
    }

    @Test func onlineInstalledManagersAreReadOnceAndMergedInRosterOrder() async throws {
        let home = FakeHub(), other = FakeHub()
        let personal = Self.machine("personal", cpu: 10, memory: 11, disk: 12)
        let defaultShared = Self.machine("shared", cpu: 20, memory: 21, disk: 22)
        let workShared = Self.machine("shared", cpu: 80, memory: 81, disk: 82)
        let work = Self.machine("work", cpu: 30, memory: 31, disk: 32)
        for hub in [home, other] {
            hub.on("GET", Self.path) { request in
                switch request.headers[Self.header] {
                case nil: return try Self.overview([personal, defaultShared])
                case "work": return try Self.overview([workShared, work])
                default: throw APIError(status: 500, message: "an offline or installing manager was asked")
                }
            }
        }
        let loader = MachineMetricsLoader()
        let apis = ["home": Self.client(home, group: "home"), "other": Self.client(other, group: "other")]
        await loader.load(["home", "other"], rosters: ["home": Self.roster, "other": Self.roster], apis: apis,
                          connected: ["home", "other"])

        for hub in [home, other] {
            let requests = hub.sent("GET", Self.path)
            #expect(requests.count == 2)
            #expect(requests.filter { $0.headers[Self.header] == nil }.count == 1,
                    "the default's one read, with no bare duplicate")
            #expect(requests.filter { $0.headers[Self.header] == "work" }.count == 1)
            #expect(requests.allSatisfy { $0.query.isEmpty })
        }
        #expect(loader.metrics.count == 6)
        #expect(loader.metrics[Self.ref("personal")]?.latest == personal.latest)
        #expect(loader.metrics[Self.ref("work")]?.latest == work.latest)
        #expect(loader.metrics[Self.ref("shared")]?.latest == defaultShared.latest,
                "a duplicate is resolved by roster order, not response completion order")
        #expect(loader.metrics[Self.ref("shared", group: "other")]?.latest == defaultShared.latest)
    }

    @Test func failedManagerKeepsItsSamplesUntilSuccessReplacesOrEmptiesThem() async throws {
        let hub = FakeHub()
        let oldDefault = Self.machine("personal", cpu: 10, memory: 11, disk: 12)
        let newDefault = Self.machine("personal", cpu: 20, memory: 21, disk: 22)
        let oldWork = Self.machine("work", cpu: 30, memory: 31, disk: 32)
        let newWork = Self.machine("work", cpu: 40, memory: 41, disk: 42)
        var defaultRows = [oldDefault]
        var workRows = [oldWork]
        var workFails = false
        hub.on("GET", Self.path) { request in
            if request.headers[Self.header] == "work" {
                if workFails { throw APIError(status: 503, message: "assistant offline") }
                return try Self.overview(workRows)
            }
            return try Self.overview(defaultRows)
        }
        let loader = MachineMetricsLoader()
        let api = Self.client(hub, group: "home")
        await loader.load(["home"], rosters: ["home": Self.roster], apis: ["home": api], connected: ["home"])

        defaultRows = [newDefault]
        workFails = true
        await loader.load(["home"], rosters: ["home": Self.roster], apis: ["home": api], connected: ["home"])
        #expect(loader.metrics[Self.ref("personal")]?.latest == newDefault.latest)
        #expect(loader.metrics[Self.ref("work")]?.latest == oldWork.latest,
                "the failed manager keeps all three last-good samples while the default refreshes")

        workFails = false
        workRows = [newWork]
        await loader.load(["home"], rosters: ["home": Self.roster], apis: ["home": api], connected: ["home"])
        #expect(loader.metrics[Self.ref("personal")]?.latest == newDefault.latest)
        #expect(loader.metrics[Self.ref("work")]?.latest == newWork.latest)

        workRows = []
        await loader.load(["home"], rosters: ["home": Self.roster], apis: ["home": api], connected: ["home"])
        #expect(loader.metrics[Self.ref("personal")]?.latest == newDefault.latest)
        #expect(loader.metrics[Self.ref("work")] == nil, "an empty success replaces, rather than retains, that manager's snapshot")
    }

    @Test func onlyAnUnsupportedLegacyOverviewMarksAHubUnmonitored() async throws {
        let legacy = FakeHub()
        legacy.on("GET", Self.path) { _ in throw APIError(status: 404, message: "not found") }
        let legacyLoader = MachineMetricsLoader()
        await legacyLoader.load(["legacy"], rosters: [:], apis: ["legacy": Self.client(legacy, group: "legacy")],
                                connected: ["legacy"])
        #expect(legacyLoader.unmonitoredGroups == ["legacy"])
        #expect(legacy.sent("GET", Self.path).count == 1)

        legacy.on("GET", Self.path, json: #"{"machines":[]}"#)
        await legacyLoader.load(["legacy"], rosters: [:], apis: ["legacy": Self.client(legacy, group: "legacy")],
                                connected: ["legacy"])
        #expect(legacyLoader.unmonitoredGroups.isEmpty, "a later supported overview clears the legacy state")

        // A routed manager's missing/private 404 is not proof that the hub lacks machine monitoring.
        let hub = FakeHub()
        hub.on("GET", Self.path) { request in
            if request.headers[Self.header] == "work" {
                throw APIError(status: 404, message: "no manager", fields: ["code": "no_manager"])
            }
            return try Self.overview([Self.machine("personal", cpu: 10, memory: 11, disk: 12)])
        }
        let loader = MachineMetricsLoader()
        await loader.load(["home"], rosters: ["home": Self.roster], apis: ["home": Self.client(hub, group: "home")],
                          connected: ["home"])
        #expect(!loader.unmonitoredGroups.contains("home"))
        #expect(loader.metrics[Self.ref("personal")] != nil)
    }

    @Test func legacyGroupsKeepOneBareRequestAndSameMachineIDsStayIsolated() async throws {
        let home = FakeHub(), work = FakeHub()
        let oldHome = Self.machine("shared", cpu: 10, memory: 11, disk: 12)
        let newHome = Self.machine("shared", cpu: 20, memory: 21, disk: 22)
        let workMetrics = Self.machine("shared", cpu: 30, memory: 31, disk: 32)
        home.on("GET", Self.path) { _ in try Self.overview([oldHome]) }
        work.on("GET", Self.path) { _ in try Self.overview([workMetrics]) }
        let loader = MachineMetricsLoader()
        let homeAPI = Self.client(home, group: "home"), workAPI = Self.client(work, group: "work")
        await loader.load(["home", "work"], rosters: [:], apis: ["home": homeAPI, "work": workAPI],
                          connected: ["home", "work"])
        #expect(loader.metrics[Self.ref("shared", group: "home")]?.latest == oldHome.latest)
        #expect(loader.metrics[Self.ref("shared", group: "work")]?.latest == workMetrics.latest)
        for hub in [home, work] {
            let requests = hub.sent("GET", Self.path)
            let request = try #require(requests.first)
            #expect(requests.count == 1)
            #expect(request.headers[Self.header] == nil && request.query.isEmpty)
        }

        home.on("GET", Self.path) { _ in try Self.overview([newHome]) }
        await loader.load(["home", "work"], rosters: [:], apis: ["home": homeAPI, "work": workAPI],
                          connected: ["home"])
        #expect(loader.metrics[Self.ref("shared", group: "home")]?.latest == newHome.latest)
        #expect(loader.metrics[Self.ref("shared", group: "work")]?.latest == workMetrics.latest)
        #expect(home.sent("GET", Self.path).count == 2)
        #expect(work.sent("GET", Self.path).count == 1, "a disconnected hub keeps its snapshot without another read")
    }

    @Test func aNewPairingDropsOldSamplesAndRejectsItsPendingRead() async throws {
        let oldHub = FakeHub(), newHub = FakeHub()
        let old = Self.machine("shared", cpu: 10, memory: 11, disk: 12)
        let stale = Self.machine("shared", cpu: 20, memory: 21, disk: 22)
        let new = Self.machine("shared", cpu: 30, memory: 31, disk: 32)
        oldHub.on("GET", Self.path) { _ in try Self.overview([old]) }
        let loader = MachineMetricsLoader()
        let oldAPI = Self.client(oldHub, group: "home", token: "old-pairing")
        await loader.load(["home"], rosters: [:], apis: ["home": oldAPI], connected: ["home"])
        #expect(loader.metrics[Self.ref("shared")]?.latest == old.latest)

        oldHub.on("GET", Self.path) { _ in try Self.overview([stale]) }
        let responses = Responses()
        let delayedOld = delayed(oldAPI, responses: responses)
        let oldRead = Task {
            await loader.load(["home"], rosters: [:], apis: ["home": delayedOld], connected: ["home"])
        }
        await responses.started(1)

        // Same group and URL, a new credential: a failed first read must not borrow the old pairing's samples.
        let newAPI = Self.client(newHub, group: "home", token: "new-pairing")
        newHub.on("GET", Self.path) { _ in throw APIError(status: 503, message: "starting") }
        await loader.load(["home"], rosters: [:], apis: ["home": newAPI], connected: ["home"])
        #expect(loader.metrics[Self.ref("shared")] == nil)

        newHub.on("GET", Self.path) { _ in try Self.overview([new]) }
        await loader.load(["home"], rosters: [:], apis: ["home": newAPI], connected: ["home"])
        #expect(loader.metrics[Self.ref("shared")]?.latest == new.latest)
        await responses.release(1)
        await oldRead.value
        #expect(loader.metrics[Self.ref("shared")]?.latest == new.latest,
                "the old pairing's late answer cannot cross the generation boundary")
    }

    @Test func aSupersededReadCannotOverwriteTheNewestSamples() async throws {
        let hub = FakeHub()
        let original = Self.machine("shared", cpu: 10, memory: 11, disk: 12)
        let stale = Self.machine("shared", cpu: 20, memory: 21, disk: 22)
        let newest = Self.machine("shared", cpu: 30, memory: 31, disk: 32)
        hub.on("GET", Self.path) { _ in try Self.overview([original]) }
        let loader = MachineMetricsLoader()
        let api = Self.client(hub, group: "home")
        await loader.load(["home"], rosters: [:], apis: ["home": api], connected: ["home"])

        let responses = Responses()
        let delayedAPI = delayed(api, responses: responses)
        hub.on("GET", Self.path) { _ in try Self.overview([stale]) }
        let older = Task {
            await loader.load(["home"], rosters: [:], apis: ["home": delayedAPI], connected: ["home"])
        }
        await responses.started(1)
        hub.on("GET", Self.path) { _ in try Self.overview([newest]) }
        let newer = Task {
            await loader.load(["home"], rosters: [:], apis: ["home": delayedAPI], connected: ["home"])
        }
        await responses.started(2)
        await responses.release(2)
        await newer.value
        #expect(loader.metrics[Self.ref("shared")]?.latest == newest.latest)

        await responses.release(1)
        await older.value
        #expect(loader.metrics[Self.ref("shared")]?.latest == newest.latest)
    }

    @Test func aCancelledReadCannotPublishSamples() async throws {
        let hub = FakeHub()
        let original = Self.machine("shared", cpu: 10, memory: 11, disk: 12)
        let stale = Self.machine("shared", cpu: 20, memory: 21, disk: 22)
        hub.on("GET", Self.path) { _ in try Self.overview([original]) }
        let loader = MachineMetricsLoader()
        let api = Self.client(hub, group: "home")
        await loader.load(["home"], rosters: [:], apis: ["home": api], connected: ["home"])

        hub.on("GET", Self.path) { _ in try Self.overview([stale]) }
        let responses = Responses()
        let read = Task {
            await loader.load(["home"], rosters: [:], apis: ["home": delayed(api, responses: responses)],
                              connected: ["home"])
        }
        await responses.started(1)
        read.cancel()
        await responses.release(1)
        await read.value
        #expect(loader.metrics[Self.ref("shared")]?.latest == original.latest)
    }

    @Test func settingsReadsOnlyItsCapturedHubAndManager() async throws {
        let home = FakeHub(), other = FakeHub()
        let captured = Self.machine("captured", cpu: 10, memory: 11, disk: 12)
        home.on("GET", Self.path) { request in
            guard request.headers[Self.header] == "work" else {
                throw APIError(status: 500, message: "settings fanned out instead of keeping its manager")
            }
            return try Self.overview([captured])
        }
        other.on("GET", Self.path) { _ in
            throw APIError(status: 500, message: "settings read another hub")
        }
        let scope = AssistantSettingsRequestScope(group: "home", manager: "work", roster: Self.roster)
        let loader = MachineMetricsLoader()
        // `showing` is the now-current assistant, not the one captured when Settings opened.
        let apis = ["home": Self.client(home, group: "home").showing("assistant"),
                    "other": Self.client(other, group: "other").showing("assistant")]
        await loader.load(["other", "home"], rosters: ["home": Self.roster, "other": Self.roster], apis: apis,
                          connected: ["home", "other"], settings: scope)
        #expect(other.requests.isEmpty)
        let requests = home.sent("GET", Self.path)
        let request = try #require(requests.first)
        #expect(requests.count == 1)
        #expect(request.headers[Self.header] == "work" && request.query.isEmpty)
        #expect(loader.metrics == [Self.ref("captured"): captured])

        // A restored pick can arrive before this launch's roster: it must not fall back to the default manager.
        let awaitingRoster = MachineMetricsLoader()
        await awaitingRoster.load(["other", "home"], rosters: [:], apis: apis,
                                  connected: ["home", "other"], settings: scope)
        #expect(other.requests.isEmpty)
        #expect(home.sent("GET", Self.path).last?.headers[Self.header] == "work")
        #expect(awaitingRoster.metrics == [Self.ref("captured"): captured])
    }

    /// Hold real FakeHub responses after they are read, so request order is controlled without scheduler sleeps.
    private actor Responses {
        private var count = 0
        private var pending: [Int: CheckedContinuation<Void, Never>] = [:]
        private var arrivals: [Int: CheckedContinuation<Void, Never>] = [:]

        func hold() async {
            count += 1
            let id = count
            await withCheckedContinuation { continuation in
                pending[id] = continuation
                arrivals.removeValue(forKey: id)?.resume()
            }
        }

        func started(_ id: Int) async {
            if pending[id] != nil { return }
            await withCheckedContinuation { arrivals[id] = $0 }
        }

        func release(_ id: Int) {
            pending.removeValue(forKey: id)!.resume()
        }
    }

    private func delayed(_ client: APIClient, responses: Responses) -> APIClient {
        var client = client
        let answer = client.transport!
        client.transport = { request in
            let data = try await answer(request)
            await responses.hold()
            return data
        }
        return client
    }
}
