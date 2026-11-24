import AppIntents
import Foundation
import PersonaWire
import Testing
@testable import Persona

struct LogWaterIntentTests {
    private let path = "/api/v1/assistant/health/intake"
    private let snapshot = #"{"water":{"today":750,"goal":2000,"entries":[]},"protein":{"today":36,"goal":60,"entries":[]},"days":[],"zone":"UTC"}"#
    private let change = #"{"entry":{"id":"00000000-0000-4000-8000-000000000001","kind":"water","amount":300,"at":"2026-11-23T08:00:00Z","note":"","seq":4,"created_at":"2026-11-23T08:00:01Z"},"today":{"water":1050,"protein":36},"zone":"UTC"}"#

    @Test func runsInTheBackgroundWithoutTakingAnotherAppShortcut() {
        #expect(!LogWaterIntent.openAppWhenRun)
        #expect(LogWaterIntent().amount == nil)
        #expect(LogWaterIntent(amount: 300).amount == 300)
        if #available(iOS 26.0, *) { #expect(LogWaterIntent.supportedModes == .background) }
        #expect(PersonaShortcuts.appShortcuts.count == 10)
    }

    @Test func eachInvocationGetsANewUUIDAndReturnsTheAuthoritativeProgress() async throws {
        let hub = FakeHub()
        hub.on("GET", path, json: snapshot)
        hub.on("POST", path, json: change)
        let api = hub.client.manager("another-manager")
        let first = try await LogWaterFlow.log(api, amount: nil)
        let second = try await LogWaterFlow.log(api, amount: 300)
        #expect(first == String(localized: "Logged \(300) ml, today \(1050) / \(2000) ml"))
        #expect(second == first)
        let posts = hub.sent("POST", path)
        #expect(posts.count == 2)
        let a = try JSON.decoder.decode(JSONValue.self, from: #require(posts.first?.body))
        let b = try JSON.decoder.decode(JSONValue.self, from: #require(posts.last?.body))
        let firstID = try #require(a["id"]?.string), secondID = try #require(b["id"]?.string)
        #expect(UUID(uuidString: firstID) != nil)
        #expect(UUID(uuidString: secondID) != nil)
        #expect(firstID != secondID)
        #expect(a["kind"]?.string == "water")
        #expect(a["amount"] == nil)
        #expect(b["amount"] == .number(300))
        for request in hub.requests {
            #expect(request.query == ["space": "health"])
            #expect(request.headers[APIClient.managerHeader] == nil)
        }
        #expect(hub.requests.map(\.method) == ["GET", "POST", "GET", "POST"])
    }

    @Test(arguments: [403, 404]) func oldHubOnlyAsksForAnUpdate(status: Int) async throws {
        let hub = FakeHub()
        hub.on("GET", path, json: snapshot)
        hub.on("POST", path) { _ in throw APIError(status: status, message: "Sample old hub") }
        do {
            _ = try await LogWaterFlow.log(hub.client, amount: nil)
            Issue.record("an old hub must not report a successful write")
        } catch {
            #expect(error.localizedDescription == String(localized: "This hub doesn't support logging water yet. Update the hub first."))
            #expect(error.localizedDescription != IntentFailure.sentence(for: APIError(status: 403, message: "Sample")))
        }
        #expect(hub.sent("POST", path).count == 1)
    }

    @Test func aFailedGoalReadNeverWritesAndInvalidAmountsNeverReachTheHub() async {
        let hub = FakeHub()
        for amount in [0, -1, 3001] {
            do { _ = try await LogWaterFlow.log(hub.client, amount: amount); Issue.record("invalid amount accepted") }
            catch { #expect(error.localizedDescription == String(localized: "Enter an amount from 1 to 3000 ml.")) }
        }
        #expect(hub.requests.isEmpty)
        do { _ = try await LogWaterFlow.log(hub.client, amount: nil); Issue.record("missing goal accepted") }
        catch { #expect(error.localizedDescription == String(localized: "This hub doesn't support logging water yet. Update the hub first.")) }
        #expect(hub.sent("POST", path).isEmpty)
    }

    @Test func credentialsKeepTheSelectedGroupAndNeverFallBackAfterRemoval() throws {
        let first = ExtensionSurfaceCredential(url: URL(string: "https://first.invalid")!, token: "sample-first", name: "First")
        let second = ExtensionSurfaceCredential(url: URL(string: "https://second.invalid")!, token: "sample-second", name: "Second", answerOnly: true)
        let state = ExtensionSurfaceSnapshot(groups: ["": .init(credential: first), "watch": .init(credential: second)])
        let api = try LogWaterFlow.client(snapshot: state, selectedGroup: "watch")
        #expect(api.group == "watch")
        #expect(api.config.url == second.url)
        #expect(api.config.token == second.token)
        #expect(try LogWaterFlow.client(snapshot: state, selectedGroup: nil).config == ServerConfig(url: first.url, token: first.token))
        #expect(throws: LogWaterFailure.self) { try LogWaterFlow.client(snapshot: state, selectedGroup: "removed") }
        #expect(throws: LogWaterFailure.self) { try LogWaterFlow.client(snapshot: .init(), selectedGroup: nil) }
        #expect(state.groups["watch"]?.credential == second, "credential and catalogs are never revoked by this flow")
    }
}
