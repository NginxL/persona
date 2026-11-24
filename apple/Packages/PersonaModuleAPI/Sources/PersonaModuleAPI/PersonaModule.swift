import Foundation
import SwiftUI

/// The app that hosts modules: the types a module hands back to it. The iOS and Vision apps' host is `PersonaHostApp`
/// (#13430 decision 4); tests use their own.
public protocol ModuleHost {
    /// What a composer action's sheet hands back to the composer: a picture in the apps.
    associatedtype Attachment
    /// The places in the host's pages where a module may draw a view (`ModuleRegistrar.view(at:)`): a chat's menu, its
    /// footer, under a tool call… Each belongs to one slot (`slot(for:)`), which the module's descriptor must declare.
    associatedtype Place: Hashable = Never
    /// What the host hands a module's view at a place: the chat, the machine, the tool call it is drawn for.
    associatedtype PlaceContext = Void
    /// What the host's Settings lists need of an entry (title, symbol, group, search words); the module fills it in.
    associatedtype SettingsInfo = Void
    /// What the host hands a module's shell page (`ModuleRegistrar.shellPage`): the bindings and actions it shares with
    /// the host's own pages.
    associatedtype ShellContext = Void
    /// The slot a place belongs to.
    static func slot(for place: Place) -> ModuleSlot
}

public extension ModuleHost where Place == Never {
    static func slot(for place: Never) -> ModuleSlot {}
}

/// A view a module draws at one of the host's places (`ModuleRegistrar.view(at:)`).
public struct ModuleView<Host: ModuleHost>: Identifiable {
    /// The module and its registration's position: stable for the registry's life.
    public let id: String
    public let module: ModuleID
    private let shows: @MainActor (Host.PlaceContext) -> Bool
    private let make: @MainActor (Host.PlaceContext) -> AnyView

    init(id: String, module: ModuleID, shows: @escaping @MainActor (Host.PlaceContext) -> Bool,
         make: @escaping @MainActor (Host.PlaceContext) -> AnyView) {
        self.id = id
        self.module = module
        self.shows = shows
        self.make = make
    }

    /// Whether the view has anything to draw here: a host that lays out around it (spacing, a divider) asks first.
    @MainActor public func shows(_ context: Host.PlaceContext) -> Bool { shows(context) }
    @MainActor public func view(_ context: Host.PlaceContext) -> AnyView { make(context) }
}

/// A page a module adds to the host's shell (`ModuleSlot.shellPlaces`): the host routes to it by `id` and lists its row
/// only while a module provides it.
public struct ModuleShellPage<Host: ModuleHost>: Identifiable {
    /// Unique among all shell pages; the host's route names it.
    public let id: String
    public let module: ModuleID
    private let make: @MainActor (Host.ShellContext) -> AnyView

    init(id: String, module: ModuleID, make: @escaping @MainActor (Host.ShellContext) -> AnyView) {
        self.id = id
        self.module = module
        self.make = make
    }

    @MainActor public func page(_ context: Host.ShellContext) -> AnyView { make(context) }
}

/// An entry in the host's Settings (`ModuleSlot.settings`): what its lists show (`info`), its row and its page.
public struct ModuleSettingsEntry<Host: ModuleHost>: Identifiable {
    /// Unique among all settings entries; the host routes to the page by it.
    public let id: String
    public let module: ModuleID
    public let info: Host.SettingsInfo
    private let makeRow: (@MainActor () -> AnyView)?
    private let makePage: @MainActor () -> AnyView

    init(id: String, module: ModuleID, info: Host.SettingsInfo, row: (@MainActor () -> AnyView)?, page: @escaping @MainActor () -> AnyView) {
        self.id = id
        self.module = module
        self.info = info
        self.makeRow = row
        self.makePage = page
    }

    /// The module's own row, when it draws one (a value read live, a row only shown sometimes); nil for the host's plain link.
    @MainActor public func row() -> AnyView? { makeRow?() }
    @MainActor public func page() -> AnyView { makePage() }
}

/// A feature module. Its type is named in one place only, the app's module list; the core reaches it through the
/// `ModuleRegistry` built from that list.
@MainActor
public protocol PersonaModule<Host> {
    associatedtype Host: ModuleHost
    var descriptor: ModuleDescriptor { get }
    /// The module is built into this binary (its SDK compiled in, its build overlay on). An unavailable module is
    /// never active, whatever else says so.
    var isAvailable: Bool { get }
    /// Adds the module's entries. Called once, when the registry is built; do no work here beyond describing entries.
    func register(into registrar: inout ModuleRegistrar<Host>)
}

/// An entry in the composer's + (`ModuleSlot.composerActions`): a row with a title and an icon that opens a sheet;
/// the sheet hands its result to `deliver`.
public struct ComposerAction<Host: ModuleHost>: Identifiable {
    /// Unique among all composer actions. The composer uses it for the prompt bar's source id and, as `attach-<id>`,
    /// for the + panel's row (and its accessibility identifier).
    public let id: String
    public let module: ModuleID
    /// The row's title and the prompt bar's subtitle, resolved where they are drawn, as before modules.
    public let title: LocalizedStringResource
    public let detail: LocalizedStringResource
    /// A design-system symbol name (`Theme.Icon`), never a literal SF Symbol in the module.
    public let symbol: String
    private let makeSheet: @MainActor (@escaping (Host.Attachment) -> Void) -> AnyView

    public init(id: String, module: ModuleID, title: LocalizedStringResource, detail: LocalizedStringResource, symbol: String,
                sheet: @escaping @MainActor (@escaping (Host.Attachment) -> Void) -> AnyView) {
        self.id = id
        self.module = module
        self.title = title
        self.detail = detail
        self.symbol = symbol
        self.makeSheet = sheet
    }

    /// The sheet's content; `deliver` takes what it produced.
    @MainActor public func sheet(deliver: @escaping (Host.Attachment) -> Void) -> AnyView { makeSheet(deliver) }
}
