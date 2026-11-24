import Foundation

/// A module's id: lower-case and hyphenated, the ids of #13430 section 2 (`glasses`, `screen-share`, `voice`). The same
/// id names the module on every platform.
public struct ModuleID: RawRepresentable, Hashable, Comparable, Sendable, ExpressibleByStringLiteral, CustomStringConvertible {
    public let rawValue: String
    public init(rawValue: String) { self.rawValue = rawValue }
    public init(stringLiteral value: String) { self.rawValue = value }
    public var description: String { rawValue }
    public static func < (a: ModuleID, b: ModuleID) -> Bool { a.rawValue < b.rawValue }

    /// Lower-case letters, digits and single hyphens, starting with a letter: `glasses`, `screen-share`.
    public var isWellFormed: Bool {
        // Every character is checked: a regex `$` would also accept a trailing newline.
        let words = rawValue.split(separator: "-", omittingEmptySubsequences: false)
        guard let first = rawValue.unicodeScalars.first, ("a"..."z").contains(first) else { return false }
        return words.allSatisfy { word in
            !word.isEmpty && word.unicodeScalars.allSatisfy { ("a"..."z").contains($0) || ("0"..."9").contains($0) }
        }
    }
}

/// The places a module can plug into the app's core. The raw values are the field names of the module contract
/// (#13430 section 4) where one exists, and of the extension points of #13400 / #13403 they will share.
///
/// Only the slots with `isServed` have a registration API and a reader in the core today; the rest are declared so a
/// module's descriptor can name them and the list is one place, but nothing serves them yet (docs/apple/ios.md, "Modules").
public enum ModuleSlot: String, CaseIterable, Sendable {
    /// Work at app launch, after the core's own (`application(_:didFinishLaunchingWithOptions:)`). Served.
    case launchHooks = "launch_hooks"
    /// URLs the app is opened with (`onOpenURL`): a module claims the ones it owns before the core routes the rest. Served.
    case deepLinks = "deep_links"
    /// Entries in the composer's + (the panel's More tab, the + menu and the prompt bar's sources), each with the sheet
    /// it opens and the attachment that sheet hands back (#13400 `composer_source`). Served.
    case composerActions = "composer_actions"

    /// Pages in the shell: a page the core routes to by id and lists a row for only while a module provides it
    /// (`ModuleRegistrar.shellPage`). Served.
    case shellPlaces = "shell_places"
    /// Entries in Settings (#13400 `settings_section`, #13395's settings registry), each with its page. Served.
    case settings = "settings"
    /// What a chat draws for an item of a kind: views at the host's tool-call places. Served (`view(at:)`).
    case chatCardKinds = "chat_card_kinds"
    /// Views next to a conversation (bars, banners above the composer, the task dock). Served (`view(at:)`).
    case chatAccessories = "chat_accessories"
    /// Items in a page's toolbar and menus (a chat's ··· menu). Served (`view(at:)`).
    case toolbarItems = "toolbar_items"
    /// Fields a module adds to, or reads from, the hub's hello. Declared, not served.
    case helloHooks = "hello_hooks"
    /// Hub frames a module handles (`AppModel`'s hub-event switch). Declared, not served.
    case frameHandlers = "frame_handlers"
    /// Work when the hub session connects or ends. Declared, not served.
    case hubSessionLifecycle = "hub_session_lifecycle"
    /// Phone tools the hub can call. Declared, not served.
    case phoneTools = "phone_tools"
    /// App Intents and Shortcuts. Declared, not served (App Intents are found at build time, so this is a list for the
    /// docs and the switch, not a runtime registration).
    case intents = "intents"
    /// Notification categories and their actions. Declared, not served.
    case pushCategories = "push_categories"
    /// Interfaces the core declares and asks for, which a module implements (`ModuleRegistrar.provide`): the core does
    /// without one when no active module provides it. A client-side slot, as `launch_hooks`. Served.
    case capabilities = "capabilities"
    /// Leases on the shared audio session through the core's `AudioSessionBroker` (#13432's remaining work). Declared,
    /// not served.
    case audioLeases = "audio_leases"

    /// True when the core reads this slot today.
    public var isServed: Bool {
        switch self {
        case .launchHooks, .deepLinks, .composerActions, .shellPlaces, .settings, .chatCardKinds, .chatAccessories,
             .toolbarItems, .capabilities: true
        default: false
        }
    }
}

/// What a module is: its id, the hub feature flag it depends on, and the slots it uses.
public struct ModuleDescriptor: Hashable, Sendable {
    public let id: ModuleID
    /// The wire feature flag (`hello.features`) through which the hub can turn the module off: `module.<id>`
    /// (`hubFeature(for:)`), or nil for a module the hub never gates. `ModuleRegistry` enforces it with `HubModules`.
    public let feature: String?
    /// The slots the module registers into. Registering into a slot that is not listed here is refused.
    public let slots: Set<ModuleSlot>

    public init(id: ModuleID, feature: String? = nil, slots: Set<ModuleSlot>) {
        self.id = id
        self.feature = feature
        self.slots = slots
    }

    /// A module the hub can turn off: its feature is `module.<id>` (`HubModules`).
    public init(id: ModuleID, hubGated: Bool, slots: Set<ModuleSlot>) {
        self.init(id: id, feature: hubGated ? Self.hubFeature(for: id) : nil, slots: slots)
    }

    /// The hub feature flag of module `id`: `module.<id>`.
    public static func hubFeature(for id: ModuleID) -> String { HubModules.prefix + id.rawValue }
}
