import Foundation
import SwiftUI
import Testing
@testable import PersonaModuleAPI

private enum TestHost: ModuleHost {
    typealias Attachment = String
}

/// Records what the registry did with it, so a test can tell a hook that ran from one that was never registered.
@MainActor
private final class Log {
    var lines: [String] = []
}

@MainActor
private struct Sample: PersonaModule {
    typealias Host = TestHost
    var descriptor: ModuleDescriptor
    var isAvailable = true
    var log: Log
    var actionID: String?
    var claims: String?

    init(_ id: ModuleID, slots: Set<ModuleSlot> = [.launchHooks, .deepLinks, .composerActions], available: Bool = true,
         hubGated: Bool = false, log: Log, action: String? = nil, claims: String? = nil) {
        descriptor = ModuleDescriptor(id: id, hubGated: hubGated, slots: slots)
        isAvailable = available
        self.log = log
        actionID = action ?? id.rawValue
        self.claims = claims
    }

    func register(into registrar: inout ModuleRegistrar<TestHost>) {
        let id = descriptor.id.rawValue
        let log = log
        let claims = claims
        registrar.onLaunch { log.lines.append("launch \(id)") }
        registrar.handleURLs { url in
            log.lines.append("url \(id)")
            return claims.map { url.host() == $0 } ?? false
        }
        if let actionID {
            registrar.composerAction(id: actionID, title: "Sample", detail: "A sample", symbol: "circle") { deliver in
                Button("Take") { deliver("picked by \(id)") }
            }
        }
    }
}

@MainActor
struct ModuleRegistryTests {
    @Test func anActiveModuleIsServedInEverySlotItDeclares() {
        let log = Log()
        let registry = ModuleRegistry<TestHost>(modules: [Sample("glasses", log: log, claims: "glasses")])
        #expect(registry.problems.isEmpty)
        #expect(registry.active.map(\.id) == ["glasses"])
        #expect(registry.isActive("glasses"))
        #expect(registry.composerActions.map(\.id) == ["glasses"])
        #expect(registry.composerAction(id: "glasses")?.module == "glasses")
        #expect(registry.composerAction(id: "camera") == nil)
        registry.launch()
        #expect(log.lines == ["launch glasses"])
        #expect(registry.handle(URL(string: "persona://glasses?x=1")!))
        #expect(!registry.handle(URL(string: "persona://open/settings")!))
    }

    @Test func aDisabledModuleIsAbsent() {
        let log = Log()
        let registry = ModuleRegistry<TestHost>(modules: [Sample("glasses", log: log, claims: "glasses")], isEnabled: { _ in false })
        #expect(registry.problems.isEmpty)
        #expect(registry.active.isEmpty)
        #expect(!registry.isActive("glasses"))
        #expect(registry.composerActions.isEmpty)
        registry.launch()
        #expect(!registry.handle(URL(string: "persona://glasses")!))
        #expect(log.lines.isEmpty) // no hook ran, no handler was asked
    }

    @Test func anUnavailableModuleIsOffByDefault() {
        let log = Log()
        let registry = ModuleRegistry<TestHost>(modules: [Sample("glasses", available: false, log: log), Sample("screen-share", log: log)])
        #expect(registry.active.map(\.id) == ["screen-share"])
        #expect(registry.composerActions.map(\.id) == ["screen-share"])
    }

    @Test func theSwitchDecidesPerModule() {
        let log = Log()
        let modules = [Sample("glasses", log: log), Sample("health", log: log)]
        let registry = ModuleRegistry<TestHost>(modules: modules, isEnabled: { $0.descriptor.id == "health" })
        #expect(registry.active.map(\.id) == ["health"])
    }

    @Test func entriesKeepTheListsOrderAndHandlersStopAtTheFirstClaim() {
        let log = Log()
        let registry = ModuleRegistry<TestHost>(modules: [Sample("health", log: log, claims: "x"), Sample("glasses", log: log, claims: "x")])
        #expect(registry.composerActions.map(\.id) == ["health", "glasses"])
        registry.launch()
        #expect(registry.handle(URL(string: "persona://x")!))
        #expect(log.lines == ["launch health", "launch glasses", "url health"])
    }

    @Test func anUndeclaredSlotIsRefused() {
        let log = Log()
        let registry = ModuleRegistry<TestHost>(modules: [Sample("glasses", slots: [.launchHooks], log: log)])
        #expect(registry.active.map(\.id) == ["glasses"])
        #expect(registry.composerActions.isEmpty)
        #expect(!registry.handle(URL(string: "persona://glasses")!))
        #expect(registry.problems == ["glasses: registers into deep_links, which its descriptor does not declare",
                                      "glasses: registers into composer_actions, which its descriptor does not declare"])
    }

    @Test func duplicatesAndMalformedIdsAreRefused() {
        let log = Log()
        let registry = ModuleRegistry<TestHost>(modules: [
            Sample("glasses", log: log), Sample("glasses", log: log),
            Sample("health", log: log, action: "glasses"), Sample("Bad_ID", log: log),
        ])
        #expect(registry.active.map(\.id) == ["glasses", "health"])
        #expect(registry.composerActions.map(\.module) == ["glasses"])
        #expect(registry.problems == ["glasses: listed twice", "health: composer action glasses is already taken",
                                      "Bad_ID: not a module id (lower-case words joined by hyphens)"])
    }

    @Test func aComposerActionCannotTakeACoreID() {
        let log = Log()
        let modules = ["photo", "camera", "file", "voice"].enumerated().map { i, id in
            Sample(ModuleID(rawValue: "m\(i)"), log: log, action: id)
        } + [Sample("glasses", log: log)]
        let registry = ModuleRegistry<TestHost>(modules: modules)
        #expect(registry.composerActions.map(\.id) == ["glasses"])
        #expect(registry.problems == ["m0: composer action photo is one of the core's own", "m1: composer action camera is one of the core's own",
                                      "m2: composer action file is one of the core's own", "m3: composer action voice is one of the core's own"])
    }

    @Test func theSheetDeliversToTheComposer() {
        let registry = ModuleRegistry<TestHost>(modules: [Sample("glasses", log: Log())])
        var got: [String] = []
        _ = registry.composerActions[0].sheet { got.append($0) }
        #expect(got.isEmpty) // building the sheet delivers nothing; only the sheet's own action does
    }

    @Test func moduleIDs() {
        #expect(ModuleID("glasses").isWellFormed)
        #expect(ModuleID("screen-share").isWellFormed)
        #expect(ModuleID("phone-extra").isWellFormed)
        #expect(!ModuleID("Glasses").isWellFormed)
        #expect(!ModuleID("screen_share").isWellFormed)
        #expect(!ModuleID("-x").isWellFormed)
        #expect(!ModuleID("").isWellFormed)
        #expect(!ModuleID("glasses\n").isWellFormed)
        #expect(!ModuleID("glasses-").isWellFormed)
        #expect(!ModuleID("a--b").isWellFormed)
        #expect(!ModuleID("1x").isWellFormed)
        #expect(!ModuleID("glässes").isWellFormed)
    }

    @Test func servedSlots() {
        #expect(ModuleSlot.allCases.filter(\.isServed) == [.launchHooks, .deepLinks, .composerActions, .shellPlaces, .settings, .chatCardKinds,
                                                           .chatAccessories, .toolbarItems, .capabilities])
        #expect(ModuleSlot.allCases.count == 16)
        // The contract's names (#13430 section 4) where it has one.
        for name in ["shell_places", "settings", "chat_card_kinds", "composer_actions", "chat_accessories", "frame_handlers", "push_categories"] {
            #expect(ModuleSlot(rawValue: name) != nil, "\(name)")
        }
    }
}
