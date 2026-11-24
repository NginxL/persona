import Foundation
import SwiftUI
import Testing
@testable import PersonaModuleAPI

/// A host whose shell hands a page what it shares with its own pages: here, whether a start is under way.
private enum ShellHost: ModuleHost {
    typealias Attachment = String
    struct ShellContext { var disabled: Bool }
}

@MainActor
private struct HealthLike: PersonaModule {
    typealias Host = ShellHost
    var descriptor = ModuleDescriptor(id: "health", hubGated: true, slots: [.shellPlaces])
    let isAvailable = true
    var pageID = "health"

    func register(into registrar: inout ModuleRegistrar<ShellHost>) {
        registrar.shellPage(id: pageID) { c in Text(c.disabled ? "starting" : "home") }
    }
}

@MainActor
private struct Undeclared: PersonaModule {
    typealias Host = ShellHost
    let descriptor = ModuleDescriptor(id: "undeclared", slots: [])
    let isAvailable = true
    func register(into registrar: inout ModuleRegistrar<ShellHost>) {
        registrar.shellPage(id: "elsewhere") { _ in Text("x") }
    }
}

@MainActor
struct ShellPagesTests {
    @Test func anActiveModulesPageIsFoundByItsID() {
        let registry = ModuleRegistry<ShellHost>(modules: [HealthLike()])
        #expect(registry.problems.isEmpty)
        #expect(registry.shellPages.map(\.id) == ["health"])
        #expect(registry.shellPage(id: "health")?.module == "health")
        #expect(registry.shellPage(id: "other") == nil)
        _ = registry.shellPage(id: "health")?.page(.init(disabled: false))
    }

    @Test func anInactiveModuleAddsNoPage() {
        let registry = ModuleRegistry<ShellHost>(modules: [HealthLike()], isEnabled: { _ in false })
        #expect(registry.shellPages.isEmpty)
        #expect(registry.shellPage(id: "health") == nil)
    }

    @Test func theHubTurnsThePageOff() {
        let registry = ModuleRegistry<ShellHost>(modules: [HealthLike()], hub: HubModules(hello: ["modules"]))
        #expect(registry.shellPage(id: "health") == nil)
        #expect(registry.offByHub == ["health"])
        #expect(registry.rebuilt(hub: HubModules(hello: ["modules", "module.health"])).shellPage(id: "health") != nil)
    }

    @Test func anUndeclaredSlotIsRefused() {
        let registry = ModuleRegistry<ShellHost>(modules: [Undeclared()])
        #expect(registry.shellPages.isEmpty)
        #expect(!registry.problems.isEmpty)
    }

    @Test func aTakenIDIsRefused() {
        var second = HealthLike()
        second.descriptor = ModuleDescriptor(id: "health-two", slots: [.shellPlaces])
        let registry = ModuleRegistry<ShellHost>(modules: [HealthLike(), second])
        #expect(registry.shellPages.map(\.module) == ["health"])
        #expect(registry.problems == ["health-two: shell page health is already taken"])
    }
}
