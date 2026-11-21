import Foundation
import Testing
@testable import Persona

/// A launcher's /model list with the IDs typed before (#1644): reading it, and Forget (`DELETE …/models/custom`): the row
/// goes at once and comes back where it was when the hub says no.
@MainActor @Suite struct LauncherModelsModelTests {
    private let path = "/api/v1/machines/box/launchers/claude/models"
    private let list = """
        {"agent":"claude","models":[
          {"id":"default","name":"Default","default":true},
          {"id":"opus","name":"Opus 5.5"},
          {"id":"vendor/model-x","name":"vendor/model-x","custom":true},
          {"id":"~vendor/model-latest","name":"~vendor/model-latest","custom":true}
        ]}
        """
    private let box = MachineRef(id: "box")

    private func loaded(_ hub: FakeHub) async -> LauncherModelsModel {
        let m = LauncherModelsModel(deps: .init(api: { _ in hub.client }))
        await m.load(box, launcher: "claude")
        return m
    }

    @Test func readsTheListWithTheTypedIDs() async {
        let hub = FakeHub()
        hub.on("GET", path, json: list)
        let m = await loaded(hub)
        #expect(m.key == "box|claude" && !m.loading && m.error == nil)
        #expect(m.list?.models.count == 4)
        #expect(m.remembered.map(\.id) == ["vendor/model-x", "~vendor/model-latest"])
    }

    @Test func anExplicitHerdrWorkspaceReadsItsOwnCatalogAndDropsTheLateDefaultHostReply() async throws {
        let hub = FakeHub()
        let route = "/api/v1/machines/box/launchers/omp/models"
        let releaseDefault = AsyncStream<Void>.makeStream()
        defer { releaseDefault.continuation.finish() }
        hub.onAsync("GET", route) { request in
            if hub.sent("GET", route).count == 1 {
                for await _ in releaseDefault.stream { break }
            }
            if request.query["host"] == "herdr" {
                return Data(#"{"agent":"omp","models":[],"supports_start_model":true}"#.utf8)
            }
            return Data(#"{"agent":"omp","models":[],"locked":"The default herdr-core host does not take a model."}"#.utf8)
        }
        let m = LauncherModelsModel(deps: .init(api: { _ in hub.client }))
        let first = Task { await m.load(box, launcher: "omp") }
        try await waitForRefreshTest { !hub.sent("GET", route).isEmpty }
        await m.load(box, launcher: "omp", host: "herdr")
        releaseDefault.continuation.yield(())
        await first.value
        #expect(hub.sent("GET", route).map { $0.query["host"] } == [nil, "herdr"])
        #expect(m.key == "box|omp|host=herdr")
        #expect(m.list?.supportsStartModel == true && m.list?.locked == nil)
    }

    @Test func losingTheModelsRouteOnRefreshClearsTheOmpStartCapability() async {
        let hub = FakeHub()
        let route = "/api/v1/machines/box/launchers/omp/models"
        hub.on("GET", route, json: #"{"agent":"omp","models":[],"supports_start_model":true}"#)
        let m = LauncherModelsModel(deps: .init(api: { _ in hub.client }))
        await m.load(box, launcher: "omp")
        #expect(m.list?.supportsStartModel == true)
        hub.on("GET", route) { _ in throw APIError(status: 404, message: "Not found") }
        await m.load(box, launcher: "omp", refresh: true)
        #expect(m.list == nil && m.error == nil)
    }

    @Test func forgetSendsTheIDAndDropsTheRow() async throws {
        let hub = FakeHub()
        hub.on("GET", path, json: list)
        hub.on("DELETE", path + "/custom", json: #"{"ok":true}"#)
        let m = await loaded(hub)
        await m.forget("vendor/model-x", machine: box, launcher: "claude")
        #expect(m.remembered.map(\.id) == ["~vendor/model-latest"])
        #expect(m.forgetError == nil)
        let sent = hub.sent("DELETE", path + "/custom")
        #expect(sent.count == 1 && sent.first?.query == ["model": "vendor/model-x"])
    }

    @Test func aRefusedForgetPutsTheRowBackAndSaysWhy() async {
        let hub = FakeHub()
        hub.on("GET", path, json: list)
        hub.on("DELETE", path + "/custom") { _ in throw APIError(status: 403, message: "只读设备不能改") }
        let m = await loaded(hub)
        await m.forget("vendor/model-x", machine: box, launcher: "claude")
        #expect(m.list?.models.map(\.id) == ["default", "opus", "vendor/model-x", "~vendor/model-latest"])
        #expect(m.forgetError?.contains("只读设备不能改") == true)
        #expect(m.forgetError?.contains("vendor/model-x") == true)
    }

    /// Optimistic: the row is gone while the hub is still asked.
    @Test func theRowGoesBeforeTheHubAnswers() async {
        let hub = FakeHub()
        hub.on("GET", path, json: list)
        let m = await loaded(hub)
        let during = Seen()
        hub.on("DELETE", path + "/custom") { _ in
            during.ids = m.remembered.map(\.id)
            return Data(#"{"ok":true}"#.utf8)
        }
        await m.forget("~vendor/model-latest", machine: box, launcher: "claude")
        #expect(during.ids == ["vendor/model-x"])
        #expect(m.remembered.map(\.id) == ["vendor/model-x"])
    }

    /// Only typed IDs, and only for the launcher on screen.
    @Test func forgetsOnlyTypedIDsOfThisList() async {
        let hub = FakeHub()
        hub.on("GET", path, json: list)
        hub.on("DELETE", path + "/custom", json: #"{"ok":true}"#)
        let m = await loaded(hub)
        await m.forget("opus", machine: box, launcher: "claude")
        await m.forget("vendor/model-x", machine: MachineRef(id: "other"), launcher: "claude")
        #expect(m.list?.models.count == 4)
        #expect(hub.sent("DELETE", path + "/custom").isEmpty)
    }

    @Test func anOlderHubHasNoListAndNoError() async {
        let hub = FakeHub()
        hub.on("GET", path) { _ in throw APIError(status: 404, message: "not found") }
        let m = await loaded(hub)
        #expect(m.list == nil && m.error == nil && !m.loading)
    }

    @Test func typedAssistantModelsKeepTheirPurposeWhenLoadedAndForgotten() async {
        let hub = FakeHub()
        hub.on("GET", path, json: list)
        hub.on("DELETE", path + "/custom", json: #"{"ok":true}"#)
        let model = LauncherModelsModel(deps: .init(api: { _ in hub.client }))
        await model.load(box, launcher: "claude", purpose: "assistant")
        #expect(model.key == "box|claude|assistant")
        #expect(hub.sent("GET", path).first?.query == ["purpose": "assistant"])
        await model.forget("vendor/model-x", machine: box, launcher: "claude", purpose: "assistant")
        #expect(model.remembered.map(\.id) == ["~vendor/model-latest"])
        #expect(hub.sent("DELETE", path + "/custom").count == 1)
        model.select(box, launcher: "claude")
        #expect(model.key == "box|claude" && model.list == nil)
    }

    @Test func aFailedReadSaysWhyAndAnotherLauncherStartsOver() async {
        let hub = FakeHub()
        hub.on("GET", path) { _ in throw APIError(status: 502, message: "claude 没登录") }
        let m = await loaded(hub)
        #expect(m.list == nil && m.error == "claude 没登录")
        m.select(box, launcher: "codex")
        #expect(m.key == "box|codex" && m.error == nil && m.list == nil)
    }

    @Test func assistantManagersKeepTheirOwnListAndForgetRoute() async throws {
        let hub = FakeHub()
        hub.on("GET", path, json: list)
        hub.on("DELETE", path + "/custom", json: #"{"ok":true}"#)
        var api = hub.client.showing("first")
        let m = LauncherModelsModel(deps: .init(api: { _ in api }))
        await m.load(box, launcher: "claude", purpose: "assistant")
        #expect(m.key == "box|claude|assistant|first")
        api = hub.client.showing("second")
        await m.forget("vendor/model-x", machine: box, launcher: "claude", purpose: "assistant")
        #expect(hub.sent("DELETE", path + "/custom").isEmpty)
        await m.load(box, launcher: "claude", purpose: "assistant")
        await m.forget("vendor/model-x", machine: box, launcher: "claude", purpose: "assistant")
        #expect(m.key == "box|claude|assistant|second")
        #expect(hub.sent("GET", path).map { $0.headers[APIClient.managerHeader] } == ["first", "second"])
        #expect(hub.sent("DELETE", path + "/custom").first?.headers[APIClient.managerHeader] == "second")
    }
}

/// What a FakeHub route saw while the request was on its way.
@MainActor private final class Seen { var ids: [String]? }
