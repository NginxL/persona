import Foundation
import Observation
import PersonaModuleAPI
import SwiftUI
import UIKit

// The app's feature modules (#13432, part of #13430; docs/apple/ios.md, "Modules"). This file is the seed of the
// `PersonaHostApp` layer (#13430 decision 4) and the only core file that names a module's type: everything else reads
// `Modules.registry`. scripts/test/test_apple_module_refs.py keeps it that way.

/// The host the modules plug into: what their entries hand back to the app, and where they may draw.
enum PersonaHostApp: ModuleHost {
    /// A composer action's sheet hands the composer a picture.
    typealias Attachment = UIImage
    /// A Settings entry of a module is listed, searched and drawn as the core's own (`SettingsCatalog`).
    typealias SettingsInfo = SettingsEntry

    /// What a module's shell page shares with the assistant's own pages (`ModuleSlot.shellPlaces`): a new conversation's
    /// launcher, model, mode and effort as picked in the shell composer, whether a start is under way, and how to start one.
    struct ShellContext {
        /// A conversation is being started.
        var disabled: Bool
        @Binding var launcher: String?
        @Binding var launcherModel: String?
        @Binding var mode: String
        @Binding var effort: String
        /// Starts a conversation in the page's space with the message.
        var send: (String, [ImagePayload]) -> Void
    }

    /// Where a module's views go, each in one slot.
    nonisolated enum Place: Hashable {
        /// A chat's ··· menu and the assistant's top-bar menu (`ModuleSlot.toolbarItems`).
        case chatMenu
        /// A chat's footer above the composer, and the assistant's task dock (`ModuleSlot.chatAccessories`).
        case chatFooter
        /// Under a tool call (`ModuleSlot.chatCardKinds`).
        case toolCall
    }

    /// What the core hands a view at a place: whichever of these the place has.
    struct PlaceContext {
        /// A chat's own machine (an agent's), or the machine a page is about.
        var machine: MachineRef? = nil
        /// The machine the chat's agent operated last (the assistant's).
        var recent: MachineRef? = nil
        /// The chat the place is in.
        var from: SessionKey? = nil
        /// The chat's latest computer step (`ComputerActivity.latest`).
        var activity: ComputerActivity? = nil
        /// The tool call the place is under.
        var call: ToolCall? = nil
    }

    nonisolated static func slot(for place: Place) -> ModuleSlot {
        switch place {
        case .chatMenu: .toolbarItems
        case .chatFooter: .chatAccessories
        case .toolCall: .chatCardKinds
        }
    }
}

/// A view of an active module at one of the app's places.
typealias ModulePlaceView = ModuleView<PersonaHostApp>

/// A page of an active module in the app's shell (`ModuleSlot.shellPlaces`).
typealias ModuleShellPlace = ModuleShellPage<PersonaHostApp>

/// The active modules' views at `place` for `context`, those with something to draw (`ModuleView.shows`).
struct ModuleViews: View {
    let place: PersonaHostApp.Place
    var context = PersonaHostApp.PlaceContext()

    var body: some View {
        ForEach(Modules.registry.views(at: place).filter { $0.shows(context) }) { $0.view(context) }
    }

    /// Whether any active module draws at `place` for `context`: a layout that spaces around them asks first.
    @MainActor static func any(_ place: PersonaHostApp.Place, _ context: PersonaHostApp.PlaceContext = .init()) -> Bool {
        Modules.registry.views(at: place).contains { $0.shows(context) }
    }
}

#if DEBUG
/// The DEBUG sample sheets of the modules compiled into this build, for screenshots (`-showHealthTrends`). The one place
/// the core names a module's DEBUG modifier; a build without the module has none.
struct ModuleDebugSamples: ViewModifier {
    func body(content: Content) -> some View {
        #if PERSONA_MODULE_HEALTH && os(iOS)
        content.healthTrendsSample()
        #else
        content
        #endif
    }
}
#endif

/// A composer action of an active module (`ModuleSlot.composerActions`).
typealias ModuleComposerAction = ComposerAction<PersonaHostApp>

enum Modules {
    /// The built-in modules, in the order their entries appear. A module is active only when it is listed here,
    /// enabled (`isEnabled`) and allowed by the hub (`HubModules`).
    static var builtIn: [any PersonaModule<PersonaHostApp>] {
        var list: [any PersonaModule<PersonaHostApp>] = [GlassesModule()]
        // A module in a directory of its own (apple/Modules/<Name>) is compiled in by apple/modules.yml, which sets its
        // condition; apple/modules-core-only.yml (PERSONA_CORE_ONLY=YES) leaves every one of them out.
        #if PERSONA_MODULE_SCREEN_SHARE
        list.append(ScreenShareModule())
        #endif
        #if PERSONA_MODULE_HEALTH
        list.append(HealthModule())
        #endif
        return list
    }

    /// The registry the core reads. Observed: a view that reads it draws again when a hello turns a module on or off.
    /// Reads are array reads, cheap in `body` (docs/apple/performance.md, P4 / P5).
    static var registry: ModuleRegistry<PersonaHostApp> { ModuleState.shared.registry }

    /// The switch. A module is enabled when its build includes it: the build-time overlay or config that compiles its
    /// SDK in (for glasses `apple/meta-wearables.yml`, `app.meta_wearables.enabled`). There is no everyday setting; the
    /// hub can still turn a module with a `feature` off (`HubModules`).
    /// DEBUG builds take `-showModules <ids>`: the comma-separated modules to turn on, all others off (`none` for
    /// none), so screenshots and tests can show a module on or off in any build; an entry of a module whose SDK is
    /// not built in then opens its sheet with the module's own "not supported" state.
    static func isEnabled(_ module: any PersonaModule<PersonaHostApp>) -> Bool {
        #if DEBUG
        if let list = UserDefaults.standard.string(forKey: "showModules") {
            return list.split(separator: ",").contains { $0.trimmingCharacters(in: .whitespaces) == module.descriptor.id.rawValue }
        }
        #endif
        return module.isAvailable
    }
}

/// What each hub last said about its modules (`HubModules.stored`), one UserDefaults key per hub group, written only
/// when it changes (a hello rarely changes it; docs/apple/performance.md, P2), as `SideFold.remember` does.
enum HubModuleMemory {
    static func remembered(_ group: String, in defaults: UserDefaults = .standard) -> HubModules {
        HubModules(stored: defaults.stringArray(forKey: key(group)))
    }

    /// Keeps what `group`'s hello said. A hello without features says nothing and keeps the last answer. True when the
    /// remembered answer changed.
    @discardableResult
    static func remember(hello features: Set<String>, group: String, in defaults: UserDefaults = .standard) -> Bool {
        guard !features.isEmpty else { return false }
        let heard = HubModules(hello: features)
        guard heard != remembered(group, in: defaults) else { return false }
        if let stored = heard.stored { defaults.set(stored, forKey: key(group)) } else { defaults.removeObject(forKey: key(group)) }
        return true
    }

    private static func key(_ group: String) -> String { "hubModules." + group }
}

/// The live registry: built at its first read at launch from what the hub-level hub said last time, and built again
/// when that hub's hello, or the pick of hub, changes the answer (#13849). Nothing else is observed.
@MainActor @Observable
final class ModuleState {
    static let shared = ModuleState(group: UserDefaults.standard.string(forKey: AppModel.hubGroupKey) ?? "")

    private(set) var registry: ModuleRegistry<PersonaHostApp>
    /// The hub group whose answer the registry follows.
    @ObservationIgnored private(set) var group: String
    /// The modules whose launch hooks ran; nil before launch.
    @ObservationIgnored private var launched: Set<ModuleID>?

    init(group: String, defaults: UserDefaults = .standard,
         modules: [any PersonaModule<PersonaHostApp>] = Modules.builtIn,
         isEnabled: @escaping @MainActor (any PersonaModule<PersonaHostApp>) -> Bool = Modules.isEnabled) {
        self.group = group
        registry = ModuleRegistry(modules: modules, isEnabled: isEnabled, hub: HubModuleMemory.remembered(group, in: defaults))
        assert(registry.problems.isEmpty, "module registrations refused: \(registry.problems)")
    }

    /// Runs the active modules' launch hooks, once, at launch.
    func launch() {
        guard launched == nil else { return }
        launched = registry.launch()
    }

    /// A hello from `group`'s hub: kept for that hub, and applied when it is the hub-level one.
    func heard(_ features: Set<String>, from group: String, isHub: Bool, defaults: UserDefaults = .standard) {
        HubModuleMemory.remember(hello: features, group: group, in: defaults)
        guard isHub else { return }
        self.group = group
        apply(features.isEmpty ? HubModuleMemory.remembered(group, in: defaults) : HubModules(hello: features))
    }

    /// The hub-level hub changed: its answer is the one heard on its socket, else the remembered one.
    func follow(_ group: String, features: Set<String>, defaults: UserDefaults = .standard) {
        self.group = group
        apply(features.isEmpty ? HubModuleMemory.remembered(group, in: defaults) : HubModules(hello: features))
    }

    private func apply(_ hub: HubModules) {
        guard hub != registry.hub else { return }
        registry = registry.rebuilt(hub: hub)
        assert(registry.problems.isEmpty, "module registrations refused: \(registry.problems)")
        if let done = launched { launched = registry.launch(after: done) }
    }
}
