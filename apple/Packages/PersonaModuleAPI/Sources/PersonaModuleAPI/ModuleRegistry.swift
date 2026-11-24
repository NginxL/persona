import Foundation
import SwiftUI

/// What one module hands to `register(into:)`: one method per served slot. An entry for a slot the module's descriptor
/// does not list, or a composer action id already taken, is dropped and recorded in `ModuleRegistry.problems`.
@MainActor
public struct ModuleRegistrar<Host: ModuleHost> {
    let module: ModuleDescriptor
    var launchHooks: [() -> Void] = []
    var urlHandlers: [(URL) -> Bool] = []
    var composerActions: [ComposerAction<Host>] = []
    var views: [(Host.Place, ModuleView<Host>)] = []
    var settingsEntries: [ModuleSettingsEntry<Host>] = []
    var shellPages: [ModuleShellPage<Host>] = []
    var capabilities: [ObjectIdentifier: Any] = [:]
    var problems: [String] = []

    init(module: ModuleDescriptor) { self.module = module }

    private mutating func allows(_ slot: ModuleSlot) -> Bool {
        if module.slots.contains(slot) { return true }
        problems.append("\(module.id): registers into \(slot.rawValue), which its descriptor does not declare")
        return false
    }

    /// Runs once at launch, after the core's own launch work (`ModuleSlot.launchHooks`). Keep it cheap: no IO before
    /// the first frame (docs/apple/performance.md, P10).
    public mutating func onLaunch(_ run: @escaping () -> Void) {
        guard allows(.launchHooks) else { return }
        launchHooks.append(run)
    }

    /// Claims the URLs the module owns (`ModuleSlot.deepLinks`): return true for one it took, false to pass it on.
    public mutating func handleURLs(_ handle: @escaping (URL) -> Bool) {
        guard allows(.deepLinks) else { return }
        urlHandlers.append(handle)
    }

    /// Adds an entry to the composer's + (`ModuleSlot.composerActions`).
    public mutating func composerAction<Sheet: View>(id: String, title: LocalizedStringResource, detail: LocalizedStringResource,
                                                     symbol: String,
                                                     @ViewBuilder sheet: @escaping (_ deliver: @escaping (Host.Attachment) -> Void) -> Sheet) {
        guard allows(.composerActions) else { return }
        composerActions.append(ComposerAction(id: id, module: module.id, title: title, detail: detail, symbol: symbol) { deliver in
            AnyView(sheet(deliver))
        })
    }

    /// Draws a view at one of the host's places (the place's slot: `ModuleHost.slot(for:)`): a menu item, a chat's
    /// accessory, an entry under a tool call. `when` says whether it has anything to draw for a context, so the host can
    /// leave its spacing out; by default it always has.
    public mutating func view<Content: View>(at place: Host.Place, when shows: @escaping @MainActor (Host.PlaceContext) -> Bool = { _ in true },
                                             @ViewBuilder _ make: @escaping @MainActor (Host.PlaceContext) -> Content) {
        guard allows(Host.slot(for: place)) else { return }
        let entry = ModuleView<Host>(id: "\(module.id)#\(views.count)", module: module.id, shows: shows) { AnyView(make($0)) }
        views.append((place, entry))
    }

    /// Adds an entry to the host's Settings (`ModuleSlot.settings`), with its page; `row` draws the module's own row
    /// instead of the host's plain link.
    public mutating func settingsEntry<Page: View>(id: String, info: Host.SettingsInfo, row: (@MainActor () -> AnyView)? = nil,
                                                   @ViewBuilder page: @escaping @MainActor () -> Page) {
        guard allows(.settings) else { return }
        settingsEntries.append(ModuleSettingsEntry(id: id, module: module.id, info: info, row: row) { AnyView(page()) })
    }

    /// Adds a page to the host's shell (`ModuleSlot.shellPlaces`), which the host routes to by `id`.
    public mutating func shellPage<Page: View>(id: String, @ViewBuilder _ page: @escaping @MainActor (Host.ShellContext) -> Page) {
        guard allows(.shellPlaces) else { return }
        shellPages.append(ModuleShellPage(id: id, module: module.id) { AnyView(page($0)) })
    }

    /// Provides an interface the core declares and asks for (`ModuleSlot.capabilities`): the core does without it when
    /// no active module provides one. One provider per interface.
    public mutating func provide<Capability>(_ type: Capability.Type, _ value: Capability) {
        guard allows(.capabilities) else { return }
        capabilities[ObjectIdentifier(type)] = value
    }
}

/// The active modules and their entries, built from the app's module list. A module is active when it is in the list,
/// `isEnabled` says so and the hub allows it (`HubModules`); an inactive module registers nothing, so it leaves no row,
/// no handler and no hook. When the hub's answer changes the app builds the registry again (`rebuilt(hub:)`).
///
/// Lookups are plain array reads over what was built: cheap and stable enough to call from `body`
/// (docs/apple/performance.md, P4 / P5). The registry is a value with no observable state.
@MainActor
public struct ModuleRegistry<Host: ModuleHost> {
    /// The active modules' descriptors, in list order.
    public let active: [ModuleDescriptor]
    /// Modules listed and enabled that the hub turned off (`HubModules.allows`), in list order.
    public let offByHub: [ModuleID]
    /// The hub answer this registry was built with.
    public let hub: HubModules
    /// Every composer action of the active modules, in list order, then registration order.
    public let composerActions: [ComposerAction<Host>]
    /// Registrations that were refused: an undeclared slot, a duplicate module id or composer action id, a composer
    /// action id the core uses itself, a malformed id. Empty in a correct build; the app asserts it in DEBUG and the tests check it.
    public let problems: [String]
    /// Every settings entry of the active modules, in list order, then registration order.
    public let settingsEntries: [ModuleSettingsEntry<Host>]
    /// Every shell page of the active modules, in list order.
    public let shellPages: [ModuleShellPage<Host>]
    private let launchHooks: [(ModuleID, () -> Void)]
    private let urlHandlers: [(URL) -> Bool]
    private let placeViews: [Host.Place: [ModuleView<Host>]]
    private let capabilities: [ObjectIdentifier: Any]
    private let modules: [any PersonaModule<Host>]
    private let isEnabled: @MainActor (any PersonaModule<Host>) -> Bool

    /// The composer's own attach source ids (photos, the camera, files, voice): a module's composer action may not take one.
    public static var coreComposerIDs: Set<String> { ["photo", "camera", "file", "voice"] }

    /// - Parameters:
    ///   - modules: the module list, in the order entries should appear.
    ///   - isEnabled: whether a listed module is switched on. The default takes every module built into this binary.
    ///   - hub: what the hub says about its modules; the default (an older hub, or none heard) allows every module.
    public init(modules: [any PersonaModule<Host>], isEnabled: @escaping @MainActor (any PersonaModule<Host>) -> Bool = { $0.isAvailable },
                hub: HubModules = .everyOn) {
        var active: [ModuleDescriptor] = []
        var offByHub: [ModuleID] = []
        var actions: [ComposerAction<Host>] = []
        var problems: [String] = []
        var hooks: [(ModuleID, () -> Void)] = []
        var handlers: [(URL) -> Bool] = []
        var placeViews: [Host.Place: [ModuleView<Host>]] = [:]
        var settings: [ModuleSettingsEntry<Host>] = []
        var settingIDs = Set<String>()
        var shellPages: [ModuleShellPage<Host>] = []
        var shellIDs = Set<String>()
        var capabilities: [ObjectIdentifier: Any] = [:]
        var seen = Set<ModuleID>()
        var actionIDs = Set<String>()
        for module in modules {
            let descriptor = module.descriptor
            guard descriptor.id.isWellFormed else {
                problems.append("\(descriptor.id): not a module id (lower-case words joined by hyphens)")
                continue
            }
            guard seen.insert(descriptor.id).inserted else {
                problems.append("\(descriptor.id): listed twice")
                continue
            }
            if let feature = descriptor.feature, feature != ModuleDescriptor.hubFeature(for: descriptor.id) {
                problems.append("\(descriptor.id): feature \(feature) is not \(ModuleDescriptor.hubFeature(for: descriptor.id))")
            }
            guard isEnabled(module) else { continue }
            guard hub.allows(descriptor) else {
                offByHub.append(descriptor.id)
                continue
            }
            var registrar = ModuleRegistrar<Host>(module: descriptor)
            module.register(into: &registrar)
            problems += registrar.problems
            for action in registrar.composerActions {
                if Self.coreComposerIDs.contains(action.id) {
                    problems.append("\(descriptor.id): composer action \(action.id) is one of the core's own")
                } else if actionIDs.insert(action.id).inserted {
                    actions.append(action)
                } else {
                    problems.append("\(descriptor.id): composer action \(action.id) is already taken")
                }
            }
            hooks += registrar.launchHooks.map { (descriptor.id, $0) }
            handlers += registrar.urlHandlers
            for (place, view) in registrar.views { placeViews[place, default: []].append(view) }
            for page in registrar.shellPages {
                if shellIDs.insert(page.id).inserted { shellPages.append(page) } else {
                    problems.append("\(descriptor.id): shell page \(page.id) is already taken")
                }
            }
            for entry in registrar.settingsEntries {
                if settingIDs.insert(entry.id).inserted { settings.append(entry) } else {
                    problems.append("\(descriptor.id): settings entry \(entry.id) is already taken")
                }
            }
            for (key, value) in registrar.capabilities {
                if capabilities[key] == nil { capabilities[key] = value } else {
                    problems.append("\(descriptor.id): provides a capability another module provides")
                }
            }
            active.append(descriptor)
        }
        self.active = active
        self.offByHub = offByHub
        self.hub = hub
        self.composerActions = actions
        self.problems = problems
        self.launchHooks = hooks
        self.urlHandlers = handlers
        self.placeViews = placeViews
        self.settingsEntries = settings
        self.shellPages = shellPages
        self.capabilities = capabilities
        self.modules = modules
        self.isEnabled = isEnabled
    }

    /// The same list and switch with another hub answer: what the app builds when a hello changes it.
    public func rebuilt(hub: HubModules) -> ModuleRegistry {
        ModuleRegistry(modules: modules, isEnabled: isEnabled, hub: hub)
    }

    /// The module is in the list and switched on.
    public func isActive(_ id: ModuleID) -> Bool { active.contains { $0.id == id } }

    /// Runs the launch hooks of the active modules not in `done`, in list order, and returns the modules whose hooks
    /// have now run (`done` included). Call it at launch with nothing done, and again after a rebuild so a module the
    /// hub turned on later is set up once; a module turned off keeps what its hook set up and registers nothing.
    @discardableResult
    public func launch(after done: Set<ModuleID> = []) -> Set<ModuleID> {
        for (id, hook) in launchHooks where !done.contains(id) { hook() }
        return done.union(active.map(\.id))
    }

    /// Offers a URL to the active modules in list order; true when one took it.
    public func handle(_ url: URL) -> Bool {
        urlHandlers.contains { $0(url) }
    }

    /// The composer action with this id, nil when no active module offers it.
    public func composerAction(id: String) -> ComposerAction<Host>? {
        composerActions.first { $0.id == id }
    }

    /// The active modules' views at a place, in list order: a dictionary read.
    public func views(at place: Host.Place) -> [ModuleView<Host>] { placeViews[place] ?? [] }

    /// The shell page with this id, nil when no active module provides it.
    public func shellPage(id: String) -> ModuleShellPage<Host>? { shellPages.first { $0.id == id } }

    /// The settings entry with this id, nil when no active module offers it.
    public func settingsEntry(id: String) -> ModuleSettingsEntry<Host>? { settingsEntries.first { $0.id == id } }

    /// The active module's implementation of a core interface, nil when none provides it.
    public func capability<Capability>(_ type: Capability.Type) -> Capability? {
        capabilities[ObjectIdentifier(type)] as? Capability
    }
}
