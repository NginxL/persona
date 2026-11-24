import Foundation
import SwiftUI
import Testing
@testable import PersonaModuleAPI

/// A host with places, as the apps have: a chat's menu (toolbar items), its footer (chat accessories) and the space
/// under a tool call (chat card kinds).
private enum PlaceHost: ModuleHost {
    typealias Attachment = String
    enum Place: Hashable { case chatMenu, chatFooter, toolCall }
    struct PlaceContext { var machine: String? }
    struct SettingsInfo { var title: String }
    static func slot(for place: Place) -> ModuleSlot {
        switch place {
        case .chatMenu: .toolbarItems
        case .chatFooter: .chatAccessories
        case .toolCall: .chatCardKinds
        }
    }
}

/// What the core asks a module for, declared by the core.
@MainActor private protocol Screen: AnyObject { func canShow(_ machine: String) -> Bool }
@MainActor private final class FakeScreen: Screen { func canShow(_ machine: String) -> Bool { machine == "mac" } }

@MainActor private final class Log { var lines: [String] = [] }

@MainActor
private struct ScreenModule: PersonaModule {
    typealias Host = PlaceHost
    var descriptor = ModuleDescriptor(id: "screen-share", hubGated: true,
                                      slots: [.toolbarItems, .chatAccessories, .chatCardKinds, .settings, .capabilities])
    let isAvailable = true
    let log: Log

    func register(into registrar: inout ModuleRegistrar<PlaceHost>) {
        registrar.view(at: .chatMenu) { c in Text("VNC · \(c.machine ?? "?")") }
        registrar.view(at: .chatFooter, when: { $0.machine != nil }) { _ in Text("Controlling") }
        registrar.view(at: .toolCall) { _ in Text("VNC") }
        registrar.settingsEntry(id: "live-screen", info: .init(title: "Live Screen")) { Text("page") }
        registrar.provide(Screen.self, FakeScreen())
    }
}

@MainActor
struct PlaceSlotsTests {
    @Test func viewsAreServedAtTheirPlaces() {
        let r = ModuleRegistry<PlaceHost>(modules: [ScreenModule(log: Log())])
        #expect(r.problems.isEmpty)
        #expect(r.views(at: .chatMenu).map(\.module) == ["screen-share"])
        #expect(r.views(at: .toolCall).count == 1)
        let footer = r.views(at: .chatFooter)
        #expect(footer.count == 1)
        #expect(!footer[0].shows(.init(machine: nil)))
        #expect(footer[0].shows(.init(machine: "mac")))
        #expect(Set(r.views(at: .chatMenu).map(\.id) + footer.map(\.id)).count == 2)
    }

    @Test func settingsEntriesAndCapabilities() {
        let log = Log()
        let r = ModuleRegistry<PlaceHost>(modules: [ScreenModule(log: log)])
        #expect(r.settingsEntries.map(\.id) == ["live-screen"])
        #expect(r.settingsEntry(id: "live-screen")?.info.title == "Live Screen")
        #expect(r.settingsEntry(id: "live-screen")?.row() == nil) // the host's plain link
        #expect(r.settingsEntry(id: "other") == nil)
        #expect(r.capability(Screen.self)?.canShow("mac") == true)
    }

    /// A module the hub turned off, or switched off, leaves no view, entry, hook or capability.
    @Test func anInactiveModuleLeavesNothing() {
        let log = Log()
        for r in [ModuleRegistry<PlaceHost>(modules: [ScreenModule(log: log)], isEnabled: { _ in false }),
                  ModuleRegistry<PlaceHost>(modules: [ScreenModule(log: log)], hub: HubModules(hello: ["modules"]))] {
            #expect(r.views(at: .chatMenu).isEmpty && r.views(at: .chatFooter).isEmpty && r.views(at: .toolCall).isEmpty)
            #expect(r.settingsEntries.isEmpty)
            #expect(r.capability(Screen.self) == nil)
        }
    }

    @Test func undeclaredSlotsAndDuplicatesAreRefused() {
        var m = ScreenModule(log: Log())
        m.descriptor = ModuleDescriptor(id: "screen-share", slots: [.toolbarItems])
        let r = ModuleRegistry<PlaceHost>(modules: [m])
        #expect(r.views(at: .chatMenu).count == 1)
        #expect(r.views(at: .chatFooter).isEmpty)
        #expect(r.settingsEntries.isEmpty)
        #expect(r.capability(Screen.self) == nil)
        #expect(r.problems.count == 4)

        struct Twin: PersonaModule {
            typealias Host = PlaceHost
            let descriptor = ModuleDescriptor(id: "twin", slots: [.settings, .capabilities])
            let isAvailable = true
            func register(into registrar: inout ModuleRegistrar<PlaceHost>) {
                registrar.settingsEntry(id: "live-screen", info: .init(title: "Again")) { EmptyView() }
                registrar.provide(Screen.self, FakeScreen())
            }
        }
        let both = ModuleRegistry<PlaceHost>(modules: [ScreenModule(log: Log()), Twin()])
        #expect(both.settingsEntries.map(\.info.title) == ["Live Screen"])
        #expect(both.problems == ["twin: settings entry live-screen is already taken", "twin: provides a capability another module provides"])
    }
}
