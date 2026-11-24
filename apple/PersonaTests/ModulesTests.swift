import BeautifulUI
import Foundation
import PersonaModuleAPI
import Testing
@testable import Persona

/// The module list and its switch (#13432, docs/apple/ios.md "Modules"): a module that is switched off leaves no entry in
/// any of the composer's attach sources, and one that is on appears where Glasses Photo always has.
@MainActor
struct ModulesTests {
    private func registry(glasses: Bool) -> ModuleRegistry<PersonaHostApp> {
        ModuleRegistry(modules: Modules.builtIn, isEnabled: { glasses && $0.descriptor.id == "glasses" })
    }

    @Test func theListRegistersWithoutProblems() {
        let all = ModuleRegistry<PersonaHostApp>(modules: Modules.builtIn, isEnabled: { _ in true })
        #expect(all.problems.isEmpty)
        #expect(all.active.map(\.id) == ["glasses", "screen-share", "health"])
        #expect(Modules.registry.problems.isEmpty)
    }

    /// This build has no Meta Wearables SDK (CI never has it): glasses are off unless a DEBUG switch says otherwise.
    @Test func theDefaultFollowsTheBuild() {
        let built = ModuleRegistry<PersonaHostApp>(modules: Modules.builtIn)
        #expect(built.isActive("glasses") == MetaGlasses.isBuiltIn)
    }

    @Test func glassesOffLeavesNoAttachEntry() {
        let off = registry(glasses: false)
        #expect(off.composerActions.isEmpty)
        #expect(off.composerAction(id: "glasses") == nil)
        let sources = PromptItems.attachments(camera: true, modules: off.composerActions).map(\.id)
        #expect(sources == ["photo", "camera", "file"])
        let rows = AttachMoreSection.common(camera: {}, scan: {}, modules: off.composerActions, open: { _ in }, mention: {}).flatMap(\.rows).map(\.id)
        #expect(!rows.contains("attach-glasses"))
        #expect(rows == ["composer-menu-camera", "composer-menu-scan", "composer-menu-mention"])
    }

    @Test func glassesOnAddsTheEntryWhereItWas() {
        let on = registry(glasses: true)
        #expect(on.composerActions.map(\.id) == ["glasses"])
        let sources = PromptItems.attachments(camera: true, modules: on.composerActions)
        #expect(sources.map(\.id) == ["photo", "camera", "glasses", "file"])
        if case .symbol(let name) = sources[2].icon { #expect(name == Theme.Icon.glasses) } else { Issue.record("not a symbol") }
        #expect(sources[2].name == String(localized: "Glasses Photo"))
        var opened: [String] = []
        let rows = AttachMoreSection.common(camera: {}, scan: {}, modules: on.composerActions, open: { opened.append($0.id) }, mention: {}).flatMap(\.rows)
        #expect(rows.map(\.id) == ["composer-menu-camera", "composer-menu-scan", "attach-glasses", "composer-menu-mention"])
        let glasses = rows[2]
        #expect(glasses.title == String(localized: "Glasses Photo"))
        #expect(glasses.symbol == Theme.Icon.glasses)
        glasses.action()
        #expect(opened == ["glasses"])
    }

    @Test func glassesOffHandlesNoURL() {
        let url = URL(string: "persona://?metaWearablesAction=register")!
        #expect(!registry(glasses: false).handle(url))
    }

    // MARK: The hub's module flags (#13849): `modules` marks a hub that publishes them, `module.<id>` keeps a module on.

    /// A UserDefaults suite of its own, removed afterwards.
    private func withDefaults(_ body: (UserDefaults) -> Void) {
        let name = "ModulesTests.\(UUID().uuidString)"
        let d = UserDefaults(suiteName: name)!
        defer { d.removePersistentDomain(forName: name) }
        body(d)
    }

    private func state(_ group: String, _ d: UserDefaults) -> ModuleState {
        ModuleState(group: group, defaults: d, isEnabled: { $0.descriptor.id == "glasses" })
    }

    // Screen share (#14017): the core reaches it only through the registry's places, its settings entry and the
    // `LiveScreen` capability, so with it off there is nothing to draw and nothing to open.
    @Test func screenShareOffLeavesNoEntry() {
        let off = registry(glasses: false)
        #expect(!off.isActive("screen-share"))
        for place in [PersonaHostApp.Place.chatMenu, .chatFooter, .toolCall] { #expect(off.views(at: place).isEmpty) }
        #expect(off.settingsEntry(id: "live-screen") == nil)
        #expect(off.capability((any LiveScreen).self) == nil)
    }

    @Test func screenShareOnServesItsPlaces() {
        let on = ModuleRegistry<PersonaHostApp>(modules: Modules.builtIn, isEnabled: { $0.descriptor.id == "screen-share" })
        #expect(on.problems.isEmpty)
        for place in [PersonaHostApp.Place.chatMenu, .chatFooter, .toolCall] {
            #expect(on.views(at: place).map(\.module) == ["screen-share"])
        }
        #expect(on.settingsEntry(id: "live-screen")?.module == "screen-share")
        #expect(on.capability((any LiveScreen).self) != nil)
    }

    @Test func screenShareFollowsItsHubFlag() {
        #expect(ScreenShareModule().descriptor.feature == "module.screen-share")
        withDefaults { d in
            let s = ModuleState(group: "home", defaults: d, isEnabled: { $0.descriptor.id == "screen-share" })
            s.heard(["file_upload"], from: "home", isHub: true, defaults: d)
            #expect(s.registry.isActive("screen-share"), "an older hub leaves it on")
            s.heard(["modules", "module.screen-share"], from: "home", isHub: true, defaults: d)
            #expect(s.registry.isActive("screen-share"))
            s.heard(["modules"], from: "home", isHub: true, defaults: d)
            #expect(!s.registry.isActive("screen-share"))
            #expect(s.registry.offByHub == ["screen-share"])
            #expect(s.registry.views(at: .chatMenu).isEmpty && s.registry.capability((any LiveScreen).self) == nil)
        }
    }

    // Health (#14278): the core reaches its home only through the shell page `health`, so with it off the sidebar has
    // no row and no route opens a page.
    @Test func healthOffLeavesNoPage() {
        let off = registry(glasses: false)
        #expect(!off.isActive("health"))
        #expect(off.shellPage(id: SidebarRoute.healthPage) == nil)
    }

    @Test func healthOnServesItsPage() {
        let on = ModuleRegistry<PersonaHostApp>(modules: Modules.builtIn, isEnabled: { $0.descriptor.id == "health" })
        #expect(on.problems.isEmpty)
        #expect(on.shellPages.map(\.id) == [SidebarRoute.healthPage])
        #expect(on.shellPage(id: SidebarRoute.healthPage)?.module == "health")
    }

    @Test func healthFollowsItsHubFlag() {
        #expect(HealthModule().descriptor.feature == "module.health")
        withDefaults { d in
            let s = ModuleState(group: "home", defaults: d, isEnabled: { $0.descriptor.id == "health" })
            s.heard(["file_upload"], from: "home", isHub: true, defaults: d)
            #expect(s.registry.isActive("health"), "an older hub leaves it on")
            s.heard(["modules", "module.health"], from: "home", isHub: true, defaults: d)
            #expect(s.registry.shellPage(id: SidebarRoute.healthPage) != nil)
            s.heard(["modules", "module.screen-share"], from: "home", isHub: true, defaults: d)
            #expect(s.registry.offByHub == ["health"])
            #expect(s.registry.shellPage(id: SidebarRoute.healthPage) == nil)
        }
    }

    @Test func glassesAreGatedByTheirHubFlag() {
        #expect(GlassesModule().descriptor.feature == "module.glasses")
    }

    @Test func anOlderHubKeepsGlassesOn() {
        withDefaults { d in
            let s = state("home", d)
            s.heard(["file_upload", "terminal_stream"], from: "home", isHub: true, defaults: d)
            #expect(s.registry.isActive("glasses"))
            #expect(s.registry.composerActions.map(\.id) == ["glasses"])
            #expect(d.object(forKey: "hubModules.home") == nil) // nothing to remember for an older hub
        }
    }

    @Test func aPublishingHubWithTheFlagKeepsGlassesOn() {
        withDefaults { d in
            let s = state("home", d)
            s.heard(["modules", "module.glasses"], from: "home", isHub: true, defaults: d)
            #expect(s.registry.isActive("glasses"))
        }
    }

    @Test func aPublishingHubWithoutTheFlagTurnsGlassesOff() {
        withDefaults { d in
            let s = state("home", d)
            s.heard(["modules", "module.health"], from: "home", isHub: true, defaults: d)
            #expect(!s.registry.isActive("glasses"))
            #expect(s.registry.offByHub == ["glasses"])
            let sources = PromptItems.attachments(camera: true, modules: s.registry.composerActions).map(\.id)
            #expect(sources == ["photo", "camera", "file"])
            #expect(!s.registry.handle(URL(string: "persona://?metaWearablesAction=register")!))
            // And on again with a later hello.
            s.heard(["modules", "module.glasses"], from: "home", isHub: true, defaults: d)
            #expect(s.registry.composerActions.map(\.id) == ["glasses"])
        }
    }

    @Test func theRememberedAnswerHoldsBeforeTheHello() {
        withDefaults { d in
            state("home", d).heard(["modules"], from: "home", isHub: true, defaults: d)
            #expect(d.stringArray(forKey: "hubModules.home") == [])
            // The next launch, before any hello: off, as the hub said last time.
            let next = state("home", d)
            #expect(!next.registry.isActive("glasses"))
            // A hub never heard from: on.
            #expect(state("other", d).registry.isActive("glasses"))
            // A hello with no features says nothing: the remembered answer stays.
            next.heard([], from: "home", isHub: true, defaults: d)
            #expect(!next.registry.isActive("glasses"))
            #expect(d.stringArray(forKey: "hubModules.home") == [])
        }
    }

    @Test func anotherHubsHelloIsKeptButNotApplied() {
        withDefaults { d in
            let s = state("home", d)
            s.heard(["modules"], from: "work", isHub: false, defaults: d)
            #expect(s.registry.isActive("glasses"))
            // Picking that hub applies what it said.
            s.follow("work", features: [], defaults: d)
            #expect(!s.registry.isActive("glasses"))
            s.follow("home", features: [], defaults: d)
            #expect(s.registry.isActive("glasses"))
        }
    }

    @Test func theAnswerIsWrittenOnlyWhenItChanges() {
        withDefaults { d in
            #expect(HubModuleMemory.remember(hello: ["modules", "module.glasses"], group: "home", in: d))
            #expect(!HubModuleMemory.remember(hello: ["module.glasses", "modules", "file_upload"], group: "home", in: d))
            #expect(HubModuleMemory.remember(hello: ["file_upload"], group: "home", in: d)) // the hub went back to an older one
            #expect(d.object(forKey: "hubModules.home") == nil)
            #expect(!HubModuleMemory.remember(hello: ["file_upload"], group: "home", in: d))
        }
    }
}
