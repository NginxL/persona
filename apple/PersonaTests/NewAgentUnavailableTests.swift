import Foundation
import Testing
@testable import Persona
import PersonaWire

/// New Agent's launcher list says why a Persona-managed provider is missing on a computer (#8109): the hub's
/// `unavailable` for `agent.new` (the providers their own-service rule leaves out there, `launcher_own_service`) is
/// decoded in the main chat's shape (#5033), kept with the computer's launchers, and listed after them, once each, as a
/// search finds them.
@MainActor struct NewAgentUnavailableTests {
    private let path = "/api/v1/machines/box/launchers"
    private let reason = "MacBook already has its own Claude Code service, and DeepSeek skips such computers. Turn on “Also use on computers with their own sign-in” in its Devices, or pick another provider"
    private var skipped: String {
        #"{"launchers":[{"name":"claude"}],"unavailable":[{"name":"deepseek","display_name":"DeepSeek","kind":"launcher","agent":"claude","code":"launcher_own_service","reason":"\#(reason)"}]}"#
    }

    private func item(_ name: String, display: String? = nil) -> UnavailableLauncher {
        UnavailableLauncher(name: name, displayName: display, kind: "launcher", code: "launcher_own_service", reason: "why \(name)")
    }

    // MARK: Decoding

    @Test func decodesNewAgentsUnavailableInTheMainChatsShape() throws {
        let r = try JSON.decoder.decode(APIClient.LaunchersResponse.self, from: Data(skipped.utf8))
        let u = try #require(r.unavailable?.first)
        #expect(r.launchers.map(\.name) == ["claude"])
        #expect(u.name == "deepseek")
        #expect(u.displayName == "DeepSeek")
        #expect(u.kind == "launcher")
        #expect(u.agent == "claude")
        #expect(u.code == "launcher_own_service")
        #expect(u.reason == reason)
        #expect(u.title == "DeepSeek")
    }

    @Test func anOlderHubSendsNone() throws {
        let r = try JSON.decoder.decode(APIClient.LaunchersResponse.self, from: Data(#"{"launchers":[{"name":"claude"}]}"#.utf8))
        #expect(r.unavailable == nil)
    }

    /// Named as `Launcher.title` names a launcher: its own name, else its startup name.
    @Test func aProviderWithoutADisplayNameShowsItsName() {
        #expect(item("cx-gw").title == "cx-gw")
        #expect(item("cx-gw", display: "").title == "cx-gw")
        #expect(item("cx-gw", display: "  ").title == "cx-gw")
        #expect(item("cx-gw", display: " Team Codex ").title == Launcher(name: "cx-gw", displayName: " Team Codex ").title)
    }

    // MARK: Kept with the computer's launchers

    /// The list a start picks from is read fresh, and what it says is left out comes with it: kept per computer for the
    /// list, replaced by the next answer, and none from a hub that sends none.
    @Test func aStartKeepsWhatTheHubLeftOutWithTheLaunchers() async throws {
        let hub = FakeHub()
        hub.on("GET", path, json: skipped)
        let model = AppModel()
        model.apiForTests = hub.client
        let ref = MachineRef(id: "box")
        #expect(model.launcherUnavailable[ref] == nil)

        let launchers = try await model.launchersForStart(ref)
        #expect(launchers.map(\.name) == ["claude"])
        #expect(model.launcherUnavailable[ref]?.map(\.name) == ["deepseek"])
        #expect(model.launcherUnavailable[ref]?.first?.reason == reason)

        // Its rule turned off for this computer: the provider is a launcher here now, and nothing is left out.
        hub.on("GET", path, json: #"{"launchers":[{"name":"claude"},{"name":"deepseek","display_name":"DeepSeek","source":"persona"}]}"#)
        #expect(try await model.launchersForStart(ref).map(\.name) == ["claude", "deepseek"])
        #expect(model.launcherUnavailable[ref] == [])

        // Model Settings changed: read again with the list.
        hub.on("GET", path, json: skipped)
        _ = try await model.launchersForStart(ref)
        model.launcherDefaultsChanged()
        #expect(model.launcherUnavailable[ref] == nil)
    }

    // MARK: Listed after the launchers

    @Test func listedInTheHubsOrderAfterTheLaunchers() {
        let unavailable = [item("deepseek", display: "DeepSeek"), item("cx-gw")]
        let shown = UnavailableLauncher.shown(unavailable, after: [Launcher(name: "claude"), Launcher(name: "codex")], query: "")
        #expect(shown.map(\.name) == ["deepseek", "cx-gw"])
    }

    /// A name the list has as a launcher already stays as listed (an older or odd answer), and one the hub sends twice is
    /// listed once.
    @Test func aNameListedAlreadyOrTwiceIsListedOnce() {
        let unavailable = [item("claude"), item("deepseek"), item("deepseek")]
        let shown = UnavailableLauncher.shown(unavailable, after: [Launcher(name: "claude")], query: "")
        #expect(shown.map(\.name) == ["deepseek"])
    }

    @Test func aSearchFindsThemByNameOrDisplayName() {
        let unavailable = [item("deepseek", display: "DeepSeek"), item("cx-gw", display: "Team Codex")]
        #expect(UnavailableLauncher.shown(unavailable, after: [], query: "seek").map(\.name) == ["deepseek"])
        #expect(UnavailableLauncher.shown(unavailable, after: [], query: " team ").map(\.name) == ["cx-gw"])
        #expect(UnavailableLauncher.shown(unavailable, after: [], query: "sonnet").isEmpty)
    }

    @Test func findsTheBuiltInCLIByItsDisplayName() {
        var cli = item("traex", display: "TRAEX")
        cli.agent = "traex"
        #expect(cli.title == "TRAE CLI")
        #expect(UnavailableLauncher.shown([cli], after: [], query: "TRAE CLI") == [cli])
        cli.displayName = "My CLI"
        #expect(cli.title == "My CLI")
    }

    @Test func findsUnavailablePaseoLaunchersByProductOrCustomTitle() {
        for id in ["traex", "traecli", "trae-cli"] {
            for displayName in [nil, "TRAEX", "My CLI"] as [String?] {
                let title = displayName == "My CLI" ? "My CLI" : "TRAE CLI"
                var cli = item(id, display: displayName)
                cli.agent = "paseo"
                #expect(cli.title == title)
                #expect(UnavailableLauncher.shown([cli], after: [], query: title) == [cli])
            }
        }
    }
}
