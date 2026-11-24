import Foundation
import SwiftUI
import Testing
@testable import PersonaModuleAPI

private enum TestHost: ModuleHost {
    typealias Attachment = String
}

@MainActor
private final class Ran {
    var ids: [String] = []
}

@MainActor
private struct Gated: PersonaModule {
    typealias Host = TestHost
    let descriptor: ModuleDescriptor
    let isAvailable = true
    let ran: Ran

    init(_ id: ModuleID, hubGated: Bool = true, ran: Ran) {
        descriptor = ModuleDescriptor(id: id, hubGated: hubGated, slots: [.launchHooks, .composerActions])
        self.ran = ran
    }

    func register(into registrar: inout ModuleRegistrar<TestHost>) {
        let id = descriptor.id.rawValue, ran = ran
        registrar.onLaunch { ran.ids.append(id) }
        registrar.composerAction(id: id, title: "Sample", detail: "A sample", symbol: "circle") { _ in EmptyView() }
    }
}

/// The hub's module flags (#13849, the rule agreed on #13844): `module.<id>`, and the marker `modules` from a hub that
/// publishes them. A module is off only when the marker is there and its flag is not.
@MainActor
struct HubModulesTests {
    private func registry(_ hub: HubModules, ran: Ran = Ran()) -> ModuleRegistry<TestHost> {
        ModuleRegistry(modules: [Gated("screen-share", ran: ran), Gated("health", ran: ran), Gated("glasses", hubGated: false, ran: ran)],
                       isEnabled: { _ in true }, hub: hub)
    }

    @Test func theNamesLiveInOnePlace() {
        #expect(HubModules.marker == "modules")
        #expect(ModuleDescriptor.hubFeature(for: "screen-share") == "module.screen-share")
        #expect(ModuleDescriptor(id: "health", hubGated: true, slots: []).feature == "module.health")
        #expect(ModuleDescriptor(id: "glasses", hubGated: false, slots: []).feature == nil)
    }

    @Test func anOlderHubLeavesEveryModuleOn() {
        let hub = HubModules(hello: ["file_upload", "terminal_stream"])
        #expect(hub == .everyOn)
        #expect(hub.stored == nil)
        let r = registry(hub)
        #expect(r.active.map(\.id) == ["screen-share", "health", "glasses"])
        #expect(r.offByHub.isEmpty)
        #expect(r.problems.isEmpty)
    }

    /// A module flag without the marker is not a publishing hub: nothing is turned off.
    @Test func aFlagWithoutTheMarkerTurnsNothingOff() {
        let r = registry(HubModules(hello: ["module.health"]))
        #expect(r.active.map(\.id) == ["screen-share", "health", "glasses"])
    }

    @Test func aPublishingHubWithTheFlagKeepsTheModule() {
        let r = registry(HubModules(hello: ["modules", "module.screen-share", "module.health", "file_upload"]))
        #expect(r.active.map(\.id) == ["screen-share", "health", "glasses"])
        #expect(r.composerActions.map(\.id) == ["screen-share", "health", "glasses"])
    }

    @Test func aPublishingHubWithoutTheFlagTurnsTheModuleOff() {
        let ran = Ran()
        let r = registry(HubModules(hello: ["modules", "module.health"]), ran: ran)
        #expect(r.active.map(\.id) == ["health", "glasses"])
        #expect(r.offByHub == ["screen-share"])
        #expect(r.composerAction(id: "screen-share") == nil)
        #expect(!r.isActive("screen-share"))
        r.launch()
        #expect(ran.ids == ["health", "glasses"])
        // A module without a feature is never gated, even by a hub that publishes none of its flags.
        #expect(registry(HubModules(hello: ["modules"])).active.map(\.id) == ["glasses"])
    }

    @Test func theRememberedAnswerIsUsedBeforeTheHello() {
        let heard = HubModules(hello: ["modules", "module.health", "file_upload"])
        #expect(heard.stored == ["module.health"])
        let remembered = HubModules(stored: heard.stored)
        #expect(remembered == heard)
        #expect(registry(remembered).active.map(\.id) == ["health", "glasses"])
        // Never heard, or an older hub was remembered: every module on.
        #expect(registry(HubModules(stored: nil)).active.map(\.id) == ["screen-share", "health", "glasses"])
        // A publishing hub that announced no module flags is kept as such, not as an older hub.
        #expect(HubModules(stored: HubModules(hello: ["modules"]).stored) != .everyOn)
    }

    @Test func aLateHelloRebuildsAndSetsUpANewModuleOnce() {
        let ran = Ran()
        let before = registry(HubModules(hello: ["modules", "module.health"]), ran: ran)
        var done = before.launch()
        #expect(ran.ids == ["health", "glasses"])
        let after = before.rebuilt(hub: HubModules(hello: ["modules", "module.health", "module.screen-share"]))
        #expect(after.active.map(\.id) == ["screen-share", "health", "glasses"])
        done = after.launch(after: done)
        #expect(ran.ids == ["health", "glasses", "screen-share"])
        #expect(done == ["screen-share", "health", "glasses"])
        // Back off: no entry, and no hook runs again.
        let off = after.rebuilt(hub: HubModules(hello: ["modules"]))
        #expect(off.composerActions.map(\.id) == ["glasses"])
        off.launch(after: done)
        #expect(ran.ids == ["health", "glasses", "screen-share"])
    }

    @Test func aFeatureThatIsNotTheModulesOwnIsAProblem() {
        struct Odd: PersonaModule {
            typealias Host = TestHost
            let descriptor = ModuleDescriptor(id: "mail", feature: "mail_extension", slots: [])
            let isAvailable = true
            func register(into registrar: inout ModuleRegistrar<TestHost>) {}
        }
        #expect(ModuleRegistry<TestHost>(modules: [Odd()]).problems == ["mail: feature mail_extension is not module.mail"])
    }
}
