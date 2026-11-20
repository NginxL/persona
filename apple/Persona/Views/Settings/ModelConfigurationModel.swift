import Foundation
import Observation

/// Whose model settings a page edits: the current assistant's (Settings' Models and what links there from its pages), or
/// the default assistant's, for what runs on it whichever assistant is current (translation, voice calls, the inbox's
/// triage).
nonisolated enum ModelSettingsOwner: Equatable, Sendable {
    case current, defaultAssistant

    /// The client a page of this owner's sends with, made from the app's: the current assistant's as that client names
    /// it (none for the default's), or the default assistant's, naming none.
    func client(_ api: APIClient) -> APIClient {
        switch self {
        case .current: api.manager(api.managerID ?? api.shownManager)
        case .defaultAssistant: api.manager(nil)
        }
    }
}

/// One source of model choices: the hub's purposes, bindings and effective values.
@Observable final class ModelConfigurationModel {
    struct Deps {
        var api: () -> APIClient?
        var updated: () async -> Void = {}

        static var live: Deps {
            let app = AppModel.shared
            // A saved use moves the pickers' 「默认：…」 too: they read the machines' launchers again.
            let scope = AssistantSettingsRequestScope(app)
            return .init(api: { scope.api(app) }, updated: { app.launcherDefaultsChanged(); await app.refreshAssistant() })
        }
    }

    private(set) var purposes: [ModelPurpose] = []
    private(set) var providers: [ModelProvider] = []
    private(set) var purpose: ModelPurpose?
    private(set) var loaded = false
    private(set) var loading = false
    private(set) var saving = false
    private(set) var error: String?
    private(set) var conflict = false
    var draft = ModelBinding()
    struct CandidateRow: Identifiable {
        let id = UUID()
        var choice: ModelCandidateSelection.Choice
    }
    var candidateMode = "extension"
    var candidateRows: [CandidateRow] = []
    private(set) var candidateProviders: [ModelProvider] = []
    @ObservationIgnored private let deps: Deps
    @ObservationIgnored private var loadedPurposeID: String?
    @ObservationIgnored private(set) var requestAPI: APIClient?
    /// Whose model settings the page edits (`ModelSettingsOwner`); its page sets it before the first request, which keeps
    /// the client (`requestAPI`).
    @ObservationIgnored var owner: ModelSettingsOwner = .current

    init(deps: Deps = .live) { self.deps = deps }

    var dirty: Bool { purpose.map { draft != $0.override || candidateChange != nil } ?? false }

    private static func directProviders(in detail: ModelPurposeDetail) -> [ModelProvider] {
        guard let policy = detail.purpose.extensionCandidates, let ids = detail.purpose.providerIDs else { return [] }
        let allowed = Set(ids)
        let directIDs = policy.directProviderIDs.map { Set($0) }
        return detail.providers.filter { p in
            guard allowed.contains(p.id), p.hidden != true else { return false }
            if let directIDs { return directIDs.contains(p.id) }
            return p.id.hasPrefix("launcher.") && p.hasKey == true &&
                (p.kind == "api" || p.kind == "launcher" && (p.api == nil || p.api == "" || p.api == "messages"))
        }
    }

    private var candidateChange: ModelCandidateSelection? {
        guard let original = purpose?.extensionCandidates?.selection else { return nil }
        let choices = candidateMode == "custom" ? candidateRows.map {
            ModelCandidateSelection.Choice(provider: $0.choice.provider, model: Self.trim($0.choice.model) ?? "")
        } : []
        let selection = ModelCandidateSelection(mode: candidateMode, custom: choices)
        return selection == original ? nil : selection
    }

    var hasDuplicateCandidates: Bool { candidateChange?.hasDuplicates == true }

    var validCandidateChange: Bool {
        guard let change = candidateChange else { return true }
        guard let policy = purpose?.extensionCandidates, policy.modes.contains(change.mode) else { return false }
        guard change.mode == "custom" else { return change.mode == "extension" || change.mode == "off" }
        let allowed = Set(candidateProviders.map(\.id))
        return !change.custom.isEmpty && change.custom.count <= policy.most && !change.hasDuplicates &&
            change.custom.allSatisfy { allowed.contains($0.provider) }
    }

    func addCandidate() {
        guard let policy = purpose?.extensionCandidates, candidateMode == "custom", candidateRows.count < policy.most else { return }
        let providers = candidateProviders
        let next = providers.first { provider in
            !candidateRows.contains { $0.choice.provider == provider.id && $0.choice.model.isEmpty }
        }
        if let provider = next ?? providers.first { candidateRows.append(.init(choice: .init(provider: provider.id, model: ""))) }
    }

    func moveCandidate(_ id: UUID, by offset: Int) {
        guard let from = candidateRows.firstIndex(where: { $0.id == id }), candidateRows.indices.contains(from + offset) else { return }
        let row = candidateRows.remove(at: from)
        candidateRows.insert(row, at: from + offset)
    }

    private func accept(_ detail: ModelPurposeDetail) {
        purpose = detail.purpose
        providers = detail.providers
        candidateProviders = Self.directProviders(in: detail)
        draft = detail.purpose.override
        let selection = detail.purpose.extensionCandidates?.selection
        candidateMode = selection?.mode ?? "extension"
        candidateRows = selection?.custom.map { CandidateRow(choice: $0) } ?? []
    }

    var categories: [ModelPurpose] { purposes.filter { $0.uses != nil } }
    var independentPurposes: [ModelPurpose] {
        purposes.filter { $0.uses == nil && $0.categoryID != nil && $0.categoryID != $0.id && $0.hasWritableFields }
    }

    var isLarkAgent: Bool { purpose?.id == "lark.agent" }

    /// A use of Agents the hub lets the user set apart (Lark /agent, the Inbox's AI twin): fields of its own, following
    /// Agents while they are empty. An older hub sends no fields for it, and it stays a read-only use of Agents.
    static func followsAgents(_ p: ModelPurpose) -> Bool { p.parent == "agent.new" && p.hasWritableFields }

    var isAgentsOverride: Bool { purpose.map(Self.followsAgents) ?? false }

    /// The background supervisor's use (#5083, docs/assistant/model-config.md): a hub since #5806 lists it in the
    /// catalog, and the list reads it there. A hub before that leaves it out and answers for it by its own id, so the
    /// list asks for it apart; a hub from before the use has none (404) and lists none.
    static let supervise = "assistant.supervise"

    /// The catalog lacks the use (`supervise`): only then is it asked for by its id.
    static func asksApart(_ catalog: [ModelPurpose]) -> Bool { !catalog.contains { $0.id == supervise } }

    /// The background supervisor's use with fields of its own: it follows Main Chat while they are empty.
    static func followsMainChat(_ p: ModelPurpose) -> Bool { p.id == supervise && p.hasWritableFields }

    var isMainChatOverride: Bool { purpose.map(Self.followsMainChat) ?? false }

    /// A use set apart from its category (Agents or Main Chat), which it follows while its own fields are empty.
    static func followsCategory(_ p: ModelPurpose) -> Bool { followsAgents(p) || followsMainChat(p) }

    /// 「Follow Agents」 or 「Follow Main Chat」: what the shown use follows while its fields are empty; nil for a category.
    var followLabel: String? {
        if isMainChatOverride { return String(localized: "Follow Main Chat") }
        return isAgentsOverride ? String(localized: "Follow Agents") : nil
    }

    func inheritanceStatus(_ p: ModelPurpose) -> String {
        guard p.override.isEmpty else { return String(localized: "Separately configured") }
        return Self.followsMainChat(p) ? String(localized: "Follow Main Chat") : String(localized: "Follow Agents")
    }

    /// Launchers translate effort to their runtime; direct API calls still require Responses.
    var allowsEffort: Bool {
        guard let purpose, purpose.supports("effort") else { return false }
        let id = draft.provider ?? purpose.effective.provider
        guard let provider = providers.first(where: { $0.id == id }) else { return true }
        return purpose.kind == "launcher" || provider.kind != "api" || provider.api == "responses"
    }

    func modelSummary(_ p: ModelPurpose) -> String {
        let model = p.effective.model
        let effort = p.effective.effort.map { value in SessionModes.effortLevels.first { $0.id == value }?.title ?? value }
        let result = [model, effort].compactMap { $0 }.filter { !$0.isEmpty }.joined(separator: " · ")
        if !result.isEmpty { return result }
        return p.effective.provider == nil ? String(localized: "Not set up") : String(localized: "Default [Prompt Composer 844b8c]")
    }

    func summary(_ p: ModelPurpose) -> String {
        let provider = p.effective.provider.flatMap { id in providers.first { $0.id == id }?.name ?? id }
        return [provider, p.effective.model, p.effective.effort].compactMap { $0 }.filter { !$0.isEmpty }.joined(separator: " · ")
    }

    var candidates: [ModelProvider] {
        guard let purpose else { return [] }
        return providers.filter { p in
            if p.hidden == true && p.id != draft.provider { return false }
            if let ids = purpose.providerIDs { return ids.contains(p.id) || p.id == draft.provider }
            return p.kind == purpose.kind || p.id == draft.provider
        }
    }

    /// Main Chat's runtimes (How It Runs); empty for every other use and on an older hub.
    var runtimes: [String] {
        guard let purpose, purpose.supports("runtime") else { return [] }
        return purpose.runtimes ?? []
    }

    /// Another provider clears the model and the effort; the runtime is not the provider's and stays.
    func chooseProvider(_ id: String) {
        let selected = id.isEmpty ? nil : id
        guard selected != draft.provider else { return }
        draft = ModelBinding(provider: selected, runtime: draft.runtime)
    }

    func restoreInheritance() { draft = ModelBinding() }

    /// Returning from a model picker must not reload the server binding over the draft.
    func loadIfNeeded(purposeID: String) async {
        guard loadedPurposeID != purposeID else { return }
        await load(purposeID: purposeID)
    }

    func load(purposeID: String? = nil) async {
        guard !loading && !saving else { return }
        loading = true
        defer { loading = false; loaded = true }
        do {
            guard let api = requestAPI ?? deps.api().map({ owner.client($0) }) else {
                throw APIError(status: 0, message: String(localized: "Not connected to the hub yet"))
            }
            requestAPI = api
            if let purposeID {
                var result = try await api.modelPurpose(purposeID)
                if !result.purpose.hasWritableFields, let category = result.purpose.categoryID, category != result.purpose.id {
                    result = try await api.modelPurpose(category)
                }
                guard result.purpose.kind == "plugin" || (result.purpose.categoryID != nil &&
                    (result.purpose.hasWritableFields || (result.purpose.categoryID == result.purpose.id && result.purpose.uses != nil))) else {
                    purpose = nil
                    throw APIError(status: 0, message: String(localized: "This hub doesn't support model configuration by category yet. Update the server first."))
                }
                accept(result)
                loadedPurposeID = purposeID
            } else {
                let result = try await api.modelConfiguration()
                // A hub that lists the background supervisor's use is not asked for it again (#5806); one from before
                // answers for it by its id only, and its row goes after the catalog as before.
                var apart: ModelPurposeDetail?
                if Self.asksApart(result.purposes) { apart = try? await api.modelPurpose(Self.supervise) }
                purposes = Self.listing(result.purposes, apart: apart?.purpose)
                providers = result.providers
                if !purposes.isEmpty && categories.isEmpty {
                    error = String(localized: "This hub doesn't support model configuration by category yet. Update the server first.")
                    return
                }
            }
            error = nil
            conflict = false
        } catch {
            self.error = Self.message(error, singlePurpose: purposeID != nil)
        }
    }

    func save() async -> Bool {
        guard !saving && !loading && !conflict && validCandidateChange, let purpose,
              purpose.hasWritableFields || (purpose.fields == nil && (purpose.kind == "plugin" || purpose.uses != nil)) else { return false }
        saving = true
        defer { saving = false }
        do {
            guard let api = requestAPI ?? deps.api().map({ owner.client($0) }) else {
                throw APIError(status: 0, message: String(localized: "Not connected to the hub yet"))
            }
            requestAPI = api
            let binding = ModelBinding(provider: Self.trim(draft.provider), model: Self.trim(draft.model), effort: Self.trim(draft.effort),
                                       runtime: Self.trim(draft.runtime))
            var fields = purpose.fields ?? ["provider", "model"]
            if purpose.isExtensionPurpose {
                let changed = [("provider", draft.provider != purpose.override.provider), ("model", draft.model != purpose.override.model),
                               ("effort", draft.effort != purpose.override.effort), ("runtime", draft.runtime != purpose.override.runtime)]
                fields = fields.filter { field in changed.contains { $0.0 == field && $0.1 } }
            }
            let patch = ModelConfigurationPatch(revision: purpose.revision, binding: binding, fields: fields, candidates: candidateChange)
            let result = try await api.saveModelPurpose(purpose.id, patch: patch)
            accept(result)
            error = nil
            conflict = false
            await deps.updated()
            return true
        } catch {
            conflict = (error as? APIError)?.status == 409
            self.error = conflict ? String(localized: "This setting was changed elsewhere. Your edits are still here; reload, then save again.") : Self.message(error, singlePurpose: true)
            return false
        }
    }

    /// The catalog with the use the hub answers for apart (`supervise`) after it: only one that follows a listed
    /// category with fields of its own, and never twice once a hub lists it itself.
    static func listing(_ catalog: [ModelPurpose], apart: ModelPurpose?) -> [ModelPurpose] {
        guard let apart, followsMainChat(apart), let category = apart.categoryID, category != apart.id,
              catalog.contains(where: { $0.id == category }), !catalog.contains(where: { $0.id == apart.id }) else { return catalog }
        return catalog + [apart]
    }

    private static func trim(_ value: String?) -> String? {
        guard let value = value?.trimmingCharacters(in: .whitespacesAndNewlines), !value.isEmpty else { return nil }
        return value
    }

    private static func message(_ error: Error, singlePurpose: Bool = false) -> String {
        if (error as? APIError)?.status == 404 {
            return singlePurpose ? String(localized: "This use no longer exists, or the hub doesn't support unified model configuration yet. Refresh the list of uses or update the server.")
                : String(localized: "This hub doesn't support unified model configuration yet. Update the server first.")
        }
        return error.localizedDescription
    }
}
