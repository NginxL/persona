import PersonaWire
import SwiftUI

/// New conversations inherit their effective model. A missing catalog is unknown, not proof that a saved effort is invalid.
/// `model`: the model picked under `launcher` (#11472); with none, Main chat's model when it is Main chat's launcher (the
/// hub's `applyModel` runs that one), else the launcher's own.
nonisolated enum AssistantStarterEffort {
    static func knownOptions(launcher: String, model picked: String? = nil,
                             response: APIClient.LaunchersResponse, list: LauncherModels?) -> [SessionModes.Option]? {
        guard let list else { return nil }
        let configured = response.default?.launcher == launcher ? response.default?.model : nil
        let own = response.launchers.first { $0.name == launcher }?.model
        let named = picked.flatMap { $0.isEmpty ? nil : $0 }
            ?? configured.flatMap { $0.isEmpty ? nil : $0 }
            ?? own.flatMap { ModelID.isValid($0) ? $0 : nil }
        let model: AgentModel?
        if let named {
            model = list.models.first { $0.id == named } ?? list.models.first { $0.resolved == named }
        } else {
            model = list.models.first(where: \.isDefault)
        }
        guard let levels = model?.efforts else { return nil }
        var seen = Set<String>()
        return levels.filter { $0 != "auto" && seen.insert($0).inserted }.map { id in
            SessionModes.effort(id) ?? SessionModes.Option(id: id, title: id, detail: "", symbol: Theme.Icon.effort)
        }
    }

    static func options(launcher: String, model: String? = nil,
                        response: APIClient.LaunchersResponse, list: LauncherModels?) -> [SessionModes.Option] {
        knownOptions(launcher: launcher, model: model, response: response, list: list) ?? []
    }

    static func retained(_ effort: String, options: [SessionModes.Option]) -> String {
        options.contains { $0.id == effort } ? effort : ""
    }
}

/// A new conversation's model pick (#11472), as the web's new-conversation model panel makes it: a launcher and, under it,
/// one of its models (nil: its default entry). With nothing picked the conversation follows Model configuration › Main
/// chat, and the start names neither, so the hub keeps no pick of its own over that default (`keepStartPick`).
nonisolated enum AssistantStarterChoice {
    /// What a new conversation starts on: the pick; else Main chat's default, launcher and model (`default` of the
    /// `?purpose=assistant` launcher list); else, before that list answers, the launcher Persona settings name
    /// (`fallback`). Nil when none is known yet (#4200).
    static func effective(launcher: String?, model: String?, fallback: String?,
                          mainDefault: LauncherDefault?) -> AssistantModelChoice.Effective? {
        if let launcher = launcher.flatMap({ $0.isEmpty ? nil : $0 }) {
            return .init(launcher: launcher, model: model.flatMap { $0.isEmpty ? nil : $0 }, effort: nil, source: .pick)
        }
        if let main = mainDefault?.launcher.flatMap({ $0.isEmpty ? nil : $0 }) {
            return .init(launcher: main, model: mainDefault?.model.flatMap { $0.isEmpty ? nil : $0 }, effort: nil, source: .defaultValue)
        }
        guard let fallback = fallback.flatMap({ $0.isEmpty ? nil : $0 }) else { return nil }
        return .init(launcher: fallback, model: nil, effort: nil, source: .running)
    }

    /// What the start request names (`APIClient.startConversation`): nothing without a pick; else the launcher and the
    /// model picked under it, none for its default entry. A model never goes without its launcher.
    static func request(launcher: String?, model: String?) -> (launcher: String?, model: String?) {
        guard let launcher = launcher.flatMap({ $0.isEmpty ? nil : $0 }) else { return (nil, nil) }
        return (launcher, model.flatMap { $0.isEmpty ? nil : $0 })
    }

    /// The pick a menu row makes: `launcher` and its row's model (nil or "": the default entry). Nil when the row is what
    /// the new conversation starts on already (`AssistantModelChoice.pick`, the running chat's rule and the web's), so
    /// the default's own row leaves the conversation following the default.
    static func pick(launcher: String, model: String?, current: AssistantModelChoice.Effective?,
                     aliases: [String: String] = [:], configuredModel: String? = nil,
                     configuredIsDefault: Bool = false) -> (launcher: String, model: String?)? {
        let row = model.flatMap { $0.isEmpty ? nil : $0 }
        guard let current else { return (launcher, row) }
        return AssistantModelChoice.pick(launcher: launcher, model: row, effort: nil, current: current, aliases: aliases,
                                         configuredModel: configuredModel, configuredIsDefault: configuredIsDefault)
            .map { ($0.launcher, $0.model) }
    }
}

/// Lives with the paired AppModel. Sending only consults this bounded cache; it never starts a catalog request.
@MainActor final class AssistantStarterCache {
    struct Entry {
        var launchers: [Launcher]
        var efforts: [SessionModes.Option]?
        /// Main chat's default (launcher and model), what a start with nothing picked runs on (#11472).
        var mainDefault: LauncherDefault? = nil
    }
    private var entries: [String: Entry] = [:]
    /// Each launcher's model list by catalog (`AssistantStarterModels.catalog`: machine, manager, settings revision), for
    /// the starter menu's rows (#11472).
    private var lists: [String: [String: LauncherModels]] = [:]

    func entry(for source: String) -> Entry? { entries[source] }

    func keep(_ entry: Entry, for source: String) {
        if entries[source] == nil && entries.count >= 64 { entries.removeAll() }
        entries[source] = entry
    }

    func lists(for catalog: String) -> [String: LauncherModels] { lists[catalog] ?? [:] }

    func keep(_ list: LauncherModels, launcher: String, catalog: String) {
        if lists[catalog] == nil && lists.count >= 16 { lists.removeAll() }
        lists[catalog, default: [:]][launcher] = list
    }

    func retained(_ effort: String, source: String) -> String {
        guard let options = entries[source]?.efforts else { return effort }
        return AssistantStarterEffort.retained(effort, options: options)
    }

    func clear() {
        entries.removeAll()
        lists.removeAll()
    }
}

/// Prefetches the current starter's choices. Manager and settings revision are part of every request and cache identity.
@Observable final class AssistantStarterModels {
    struct Deps {
        var launchers: (MachineRef, String?, Bool) async throws -> APIClient.LaunchersResponse
        var api: (String) -> APIClient?
        var cache: AssistantStarterCache? = nil
        static var live: Deps {
            .init(launchers: { machine, manager, refresh in
                guard let api = AppModel.shared.api(machine.group)?.manager(manager) else {
                    throw APIError(status: 0, message: String(localized: "Not connected to the hub yet"))
                }
                return try await AppModel.shared.assistantLaunchers(machine, refresh: refresh, using: api)
            }, api: { AppModel.shared.api($0) })
        }
    }
    private(set) var launchers: [Launcher] = []
    private(set) var efforts: [SessionModes.Option] = []
    /// Main chat's default (launcher and model) from the same answer as `launchers`; nil until it answers, or from an
    /// older hub (#11472).
    private(set) var mainDefault: LauncherDefault?
    /// Each listed launcher's models (`?purpose=assistant`, as the running chat's model sheet reads them), by launcher name.
    private(set) var lists: [String: LauncherModels] = [:]
    /// Rows each launcher's menu offers and resolved-model → row-ID aliases, derived once per list, not in `body`
    /// (as `AssistantModelPanelModel`).
    private(set) var visibleModels: [String: [AgentModel]] = [:]
    private(set) var aliases: [String: [String: String]] = [:]
    /// Launchers whose list could not be read this time: their menu offers Retry instead of Reading models….
    private(set) var listFailures: Set<String> = []
    private(set) var key = ""
    @ObservationIgnored private let deps: Deps
    @ObservationIgnored private var requestID = 0
    @ObservationIgnored private var catalogKey = ""
    private var cache: AssistantStarterCache { deps.cache ?? AppModel.shared.assistantStarterCache }

    init(deps: Deps = .live) { self.deps = deps }

    /// `model`: the model picked under `launcher` (#11472), which decides the effort levels offered.
    static func source(_ machine: MachineRef, launcher: String?, model: String? = nil, fallback: String?, manager: String? = nil,
                       revision: Int = 0) -> String {
        "\(machine.path)|\(manager ?? "")|\(launcher ?? "")|\(launcher == nil ? "" : model ?? "")|\(fallback ?? "")|\(revision)"
    }

    /// Whose model lists `lists` holds: the launchers' computer, the manager asked, and the settings revision.
    static func catalog(_ machine: MachineRef, manager: String?, revision: Int) -> String {
        "\(machine.path)|\(manager ?? "")|\(revision)"
    }

    func options(for source: String) -> [SessionModes.Option] { source == key ? efforts : [] }
    func retained(_ effort: String, source: String) -> String { cache.retained(effort, source: source) }

    @discardableResult
    func load(_ machine: MachineRef, launcher: String?, model: String? = nil, fallback: String?, manager: String? = nil,
              revision: Int = 0, refresh: Bool = false) async -> Bool {
        let source = Self.source(machine, launcher: launcher, model: model, fallback: fallback, manager: manager, revision: revision)
        requestID += 1
        let request = requestID
        key = source
        showCatalog(Self.catalog(machine, manager: manager, revision: revision))
        let previous = cache.entry(for: source)
        launchers = previous?.launchers ?? []
        efforts = previous?.efforts ?? []
        mainDefault = previous?.mainDefault ?? mainDefault
        if previous != nil && !refresh { return true }
        do {
            let response = try await deps.launchers(machine, manager, refresh)
            guard requestID == request, !Task.isCancelled else { return false }
            let listed = response.launchers.filter(\.runsAssistant)
            // Available launchers remain selectable even if this launcher's model catalog is temporarily unavailable.
            launchers = listed
            mainDefault = response.default
            guard let name = launcher ?? response.default?.launcher ?? fallback,
                  let api = deps.api(machine.group)?.manager(manager) else { return false }
            let list = try await api.launcherModels(machine: machine.id, launcher: name, refresh: refresh, purpose: "assistant")
            guard requestID == request, !Task.isCancelled else { return false }
            keepList(list, launcher: name)
            let known = AssistantStarterEffort.knownOptions(launcher: name, model: launcher == nil ? nil : model,
                                                            response: response, list: list)
            efforts = known ?? previous?.efforts ?? []
            if known != nil || previous?.efforts != nil {
                cache.keep(.init(launchers: listed, efforts: known ?? previous?.efforts, mainDefault: response.default), for: source)
            }
            return true
        } catch {
            // The already verified choice stays intact during a transient failure; the server still validates a send.
            return false
        }
    }

    /// Reads the model list of every listed launcher not read yet for this catalog, one after another (the node answers a
    /// cached list at once), for the starter menu's rows (#11472). Each lands as it arrives; a failure leaves that
    /// launcher's default entry pickable. Starting a conversation never waits for these.
    func loadLists(_ machine: MachineRef, manager: String? = nil, revision: Int = 0, refresh: Bool = false) async {
        let catalog = Self.catalog(machine, manager: manager, revision: revision)
        showCatalog(catalog)
        guard let api = deps.api(machine.group)?.manager(manager) else { return }
        for launcher in launchers.map(\.name) where refresh || lists[launcher] == nil {
            listFailures.remove(launcher) // Reading models… again while it is asked
            do {
                let list = try await api.launcherModels(machine: machine.id, launcher: launcher, refresh: refresh, purpose: "assistant")
                guard catalogKey == catalog, !Task.isCancelled else { return }
                keepList(list, launcher: launcher)
            } catch {
                guard catalogKey == catalog, !Task.isCancelled, !error.isCancellation else { return }
                if let e = error as? APIError, e.status == 404 {
                    // An older hub or node: no list, the launcher's own model is still a choice (the model sheet's rule).
                    keepList(LauncherModels(agent: "", models: [], locked: nil), launcher: launcher)
                } else {
                    listFailures.insert(launcher)
                }
            }
        }
    }

    /// Another computer, manager or settings revision: its own lists, read from the shared cache.
    private func showCatalog(_ catalog: String) {
        guard catalog != catalogKey else { return }
        catalogKey = catalog
        lists = [:]
        visibleModels = [:]
        aliases = [:]
        for (launcher, list) in cache.lists(for: catalog) { show(list, launcher: launcher) }
        listFailures = []
        mainDefault = nil // the other catalog's; its own comes with its launchers
    }

    private func keepList(_ list: LauncherModels, launcher: String) {
        show(list, launcher: launcher)
        listFailures.remove(launcher)
        cache.keep(list, launcher: launcher, catalog: catalogKey)
    }

    private func show(_ list: LauncherModels, launcher: String) {
        lists[launcher] = list
        visibleModels[launcher] = AssistantModelChoice.visibleModels(list)
        aliases[launcher] = AssistantModelChoice.selectionAliases(list)
    }
}
