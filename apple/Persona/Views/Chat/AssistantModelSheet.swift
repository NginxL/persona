import SwiftUI

/// Pure decisions behind the main chat's model sheet (#6542; the hub's per-conversation pick is #5033): what the next
/// message runs on, where that comes from, and the pick a row keeps. No UI types, so the logic is unit-tested on its own.
nonisolated enum AssistantModelChoice {
    /// Where the setup the next message runs on comes from.
    enum Source: Equatable {
        /// The conversation's own pick (`model_pick`).
        case pick
        /// Model configuration › Main chat's effective value (the picker's `default`).
        case defaultValue
        /// Only what the running process last reported, while the picker has not answered.
        case running
    }

    /// What the conversation's next message runs on.
    struct Effective: Equatable {
        var launcher: String
        /// Nil: the launcher's own default model.
        var model: String?
        var effort: String?
        var source: Source
        var picked: Bool { source == .pick }
    }

    /// The setup the next message runs on, as the hub decides it (the web's `assistantSetup` for the main chat).
    /// A pick wins; with none, the Main chat default (`response.default`) names the launcher and its model; before the
    /// picker answers, the running session is all there is.
    static func effective(session: Session?, response: APIClient.LaunchersResponse?) -> Effective {
        effective(launcher: session?.launcher, model: session?.model, effort: session?.effort,
                  pick: session?.modelPick, response: response)
    }

    /// Core resolution on plain fields, so the rule is unit-tested without building a whole `Session`.
    static func effective(launcher: String?, model: String?, effort: String?, pick: ModelPick?,
                          response: APIClient.LaunchersResponse?) -> Effective {
        if let pick, !pick.launcher.isEmpty {
            return Effective(launcher: pick.launcher, model: pick.model?.emptyAsNil,
                             effort: pick.effort?.emptyAsNil ?? effort, source: .pick)
        }
        let def = response?.default
        let provider = def?.launcher?.emptyAsNil
            ?? launcher?.emptyAsNil
            ?? "claude"
        // The default's model only names a running one while the session is on the same launcher, the web's rule too.
        let defaultModel = def?.model?.emptyAsNil
            ?? (launcher == provider ? model : nil)
        return Effective(launcher: provider, model: defaultModel,
                         effort: effort ?? def?.effort?.emptyAsNil,
                         source: response == nil ? .running : .defaultValue)
    }

    /// The model id ticked for `launcher` in the sheet: nil when the setup runs on another launcher, "" on its Default
    /// row, else the picked model's id. A hub-resolved model also ticks its alias row.
    static func selectedModel(_ e: Effective, launcher: String, aliases: [String: String] = [:],
                              configuredModel: String? = nil) -> String? {
        guard e.launcher == launcher else { return nil }
        let model = e.model?.emptyAsNil
        guard let model else { return configuredModel?.emptyAsNil ?? "" }
        return aliases[model.lowercased()] ?? model
    }

    /// Resolved-ID → row-ID lookup, built once when a catalog lands rather than searched in a view's body.
    static func selectionAliases(_ list: LauncherModels?) -> [String: String] {
        var out: [String: String] = [:]
        for model in list?.models ?? [] {
            let row = model.isDefault ? "" : model.id
            let key = model.id.lowercased()
            if out[key] == nil { out[key] = row }
            if let resolved = model.resolved {
                // Default and its concrete alias often resolve to the same ID: the concrete alias wins, as on the web.
                let resolvedKey = resolved.lowercased()
                if out[resolvedKey] == nil || !model.isDefault { out[resolvedKey] = row }
            }
        }
        return out
    }

    /// The engine the existing conversation actually runs on, including Codex threads identified by their source.
    static func sessionEngine(_ session: Session?) -> String? {
        session?.isCodex == true ? "codex" : session?.agent
    }

    /// An existing Claude/Codex conversation keeps its engine, matching the hub and web. Unknown runtimes stay
    /// selectable because refusing them would be a guess; the hub remains authoritative.
    static func isOtherEngine(sessionAgent: String?, launcherAgent: String?) -> Bool {
        guard let session = engine(sessionAgent), let launcher = engine(launcherAgent, nilIsClaude: true) else { return false }
        return session != launcher
    }

    /// A locked launcher keeps only its default entry, as on the web. Its lock explains why concrete model IDs are absent;
    /// choosing no model remains valid and the hub accepts it.
    static func visibleModels(_ list: LauncherModels?) -> [AgentModel] {
        (list?.models ?? []).filter { list?.locked == nil || $0.isDefault }
    }

    /// The configured-model row for an empty catalog. A locked launcher's configured model is sent as Default (nil),
    /// because the hub accepts the launcher's own fixed model only without an overriding model ID.
    struct ConfiguredFallback: Equatable {
        var id: String
        var sendsDefault: Bool
    }

    /// The configured-model row for a launcher with no readable catalog or no models of its own.
    static func configuredFallback(_ launcher: Launcher, models: [AgentModel], locked: String?) -> ConfiguredFallback? {
        guard models.isEmpty, let id = launcher.model?.emptyAsNil, id != "默认" else { return nil } // i18n-ignore: legacy wire value, never displayed
        return ConfiguredFallback(id: id, sendsDefault: locked != nil)
    }

    /// The user-facing destination in the cross-engine warning, normalized from `codex-cli` / case variants.
    static func otherEngineName(_ runtimeAgent: String?) -> String {
        engine(runtimeAgent, nilIsClaude: true) == "codex" ? "Codex" : "Claude Code"
    }

    private static func engine(_ raw: String?, nilIsClaude: Bool = false) -> String? {
        guard let normalized = raw?.trimmingCharacters(in: .whitespacesAndNewlines).lowercased() else {
            return nilIsClaude ? "claude" : nil
        }
        switch normalized {
        case "", "claude", "claude-code", "claudecode": return "claude"
        case "codex", "codex-cli": return "codex"
        default: return nil
        }
    }

    /// The pick a row keeps: nil for the setup already in force (the hub would store a no-op). A Default row sends no
    /// model; `effort` carries the setup's current effort so the pick keeps it. Resolved aliases and a configured
    /// fallback are compared by what runs, not just by their spelling, so tapping the current row never pins a default.
    static func pick(launcher: String, model: String?, effort: String?, current: Effective,
                     aliases: [String: String] = [:], configuredModel: String? = nil,
                     configuredIsDefault: Bool = false) -> ModelPick? {
        let sentModel = model?.emptyAsNil
        if current.launcher == launcher {
            let selected = selectedModel(current, launcher: launcher, aliases: aliases, configuredModel: configuredModel)
            if selected == (sentModel ?? "") { return nil }
            // No catalog: the configured model is what this launcher's default already runs.
            if let sentModel, current.model?.emptyAsNil == nil, sentModel == configuredModel?.emptyAsNil { return nil }
            // A locked configured row is the same Default choice, not an override with a concrete ID.
            if configuredIsDefault, sentModel == nil, selected == configuredModel?.emptyAsNil { return nil }
        }
        return ModelPick(launcher: launcher, model: sentModel, effort: effort?.emptyAsNil)
    }

    /// The short name the composer button shows for the effective setup: the picked/configured model, else the
    /// launcher's own model, else just the launcher.
    static func displayName(_ e: Effective, response: APIClient.LaunchersResponse?) -> String? {
        let id = e.model?.emptyAsNil
            ?? response?.launchers.first { $0.name == e.launcher }?.model?.emptyAsNil
        return id.flatMap { SessionModes.modelName($0) } ?? e.launcher
    }
}

private extension String {
    // Read by the nonisolated `AssistantModelChoice`; the app target's default would make it main-actor only.
    nonisolated var emptyAsNil: String? { isEmpty ? nil : self }
}

/// The main chat's model sheet model (#6542): the assistant computer's picker (`?purpose=assistant`), each launcher's
/// `/model` list, and the picks kept on the hub (#5033).
@Observable
final class AssistantModelPanelModel {
    struct Deps {
        var launchers: (_ machine: MachineRef, _ refresh: Bool) async throws -> APIClient.LaunchersResponse
        var models: (_ machine: MachineRef, _ launcher: String, _ refresh: Bool) async throws -> LauncherModels
        var keep: (_ key: SessionKey, _ pick: ModelPick) async -> Bool
        var drop: (_ key: SessionKey) async -> Bool
        static var live: Deps {
            Deps(
                launchers: { try await AppModel.shared.assistantLaunchers($0, refresh: $1) },
                models: { try await AppModel.shared.api($0.group)?.launcherModels(machine: $0.id, launcher: $1, refresh: $2, purpose: "assistant")
                    ?? LauncherModels(agent: "", models: [], locked: nil, machine: nil) },
                keep: { await AppModel.shared.keepConversationModel($0, pick: $1) },
                drop: { await AppModel.shared.dropConversationModel($0) })
        }
    }

    let key: SessionKey
    let machine: MachineRef
    private(set) var response: APIClient.LaunchersResponse?
    private(set) var offeredLauncherNames: Set<String> = []
    private(set) var unavailableLauncherNames: Set<String> = []
    /// Each launcher's `/model` list, by launcher name.
    private(set) var lists: [String: LauncherModels] = [:]
    /// Rows the sheet may offer and resolved-model → row-ID aliases, derived once per answer (P4: not in `body`).
    private(set) var visibleLists: [String: [AgentModel]] = [:]
    private(set) var resolvedAliases: [String: [String: String]] = [:]
    /// Why one launcher's list could not be read (a 404 from an older hub/node leaves it empty, not an error).
    private(set) var listErrors: [String: String] = [:]
    private(set) var loading = false
    private(set) var loadError: String?
    /// The launcher a pick is in flight for, so its rows can dim; nil when idle.
    private(set) var busyLauncher: String?
    @ObservationIgnored private let deps: Deps

    init(key: SessionKey, machine: MachineRef, deps: Deps = .live) {
        self.key = key
        self.machine = machine
        self.deps = deps
    }

    func list(_ launcher: String) -> LauncherModels? { lists[launcher] }
    func models(_ launcher: String) -> [AgentModel] { visibleLists[launcher] ?? [] }
    func selectionAliases(_ launcher: String) -> [String: String] { resolvedAliases[launcher] ?? [:] }
    /// A current provider absent from both lists needs its own explanation and recovery row.
    func isMissing(_ launcher: String) -> Bool {
        response != nil && !offeredLauncherNames.contains(launcher) && !unavailableLauncherNames.contains(launcher)
    }
    func error(_ launcher: String) -> String? { listErrors[launcher] }
    var unavailable: [UnavailableLauncher] { response?.unavailable ?? [] }

    /// Reads the picker once, then each launcher's model list in turn (the node serves a cached list at once, so this
    /// stays quick); each answer lands as it arrives.
    func load(refresh: Bool = false) async {
        guard !loading else { return }
        loading = true
        loadError = nil
        defer { loading = false }
        do {
            let r = try await deps.launchers(machine, refresh)
            response = r
            offeredLauncherNames = Set(r.launchers.map(\.name))
            unavailableLauncherNames = Set((r.unavailable ?? []).map(\.name))
        } catch {
            guard !error.isCancellation else { return }
            loadError = error.shown
            return
        }
        for launcher in response?.launchers ?? [] {
            await readList(launcher, refresh: refresh)
        }
    }

    private func readList(_ launcher: Launcher, refresh: Bool = false) async {
        let name = launcher.name
        do {
            let answer = try await deps.models(machine, name, refresh)
            lists[name] = answer
            visibleLists[name] = AssistantModelChoice.visibleModels(answer)
            resolvedAliases[name] = AssistantModelChoice.selectionAliases(answer)
            listErrors[name] = nil
        } catch {
            if let e = error as? APIError, e.status == 404 {
                // Older hub/node: no catalog, but the launcher's configured model is still a concrete choice.
                lists[name] = LauncherModels(agent: launcher.runtimeAgent ?? "claude",
                                             models: [], locked: nil, machine: nil)
                visibleLists[name] = []
                resolvedAliases[name] = [:]
                listErrors[name] = nil
                return
            }
            guard !error.isCancellation else { return }
            // Keep the last good rows selectable; the section shows the refresh failure and Retry beside them.
            listErrors[name] = error.shown
        }
    }

    /// Keeps `model` (nil: its default) for `launcher`; returns whether the hub took it.
    func choose(launcher: String, model: String?, session: Session?) async -> Bool {
        let current = AssistantModelChoice.effective(session: session, response: response)
        let launcherInfo = response?.launchers.first { $0.name == launcher }
        let fallback = launcherInfo.flatMap {
            AssistantModelChoice.configuredFallback($0, models: models(launcher), locked: list(launcher)?.locked)
        }
        guard let pick = AssistantModelChoice.pick(launcher: launcher, model: model, effort: current.effort, current: current,
                                                   aliases: selectionAliases(launcher), configuredModel: fallback?.id,
                                                   configuredIsDefault: fallback?.sendsDefault == true) else { return true }
        busyLauncher = launcher
        defer { busyLauncher = nil }
        return await deps.keep(key, pick)
    }

    /// The conversation follows Model configuration › Main chat again.
    func followDefault() async -> Bool {
        busyLauncher = ""
        defer { busyLauncher = nil }
        return await deps.drop(key)
    }
}

/// The sheet opened from the main chat composer's model button (#6542): every launcher the assistant can use on its
/// machine, each one's models, the ones that can't run here with the hub's reason, and the way back to the default.
/// A pick is kept for this conversation only (#5033) and holds from the next message.
struct AssistantModelSheet: View {
    @Environment(AppModel.self) private var model
    let session: Session?
    var panel: AssistantModelPanelModel
    @Environment(\.dismiss) private var dismiss
    @State private var typingFor: String?

    private var effective: AssistantModelChoice.Effective {
        AssistantModelChoice.effective(session: session, response: panel.response)
    }

    var body: some View {
        NavigationStack {
            List {
                if let problem = panel.loadError {
                    Section {
                        Label(problem, systemImage: Theme.Icon.error)
                            .font(Theme.Font.secondary).foregroundStyle(Theme.cautionText)
                        Button { Task { await panel.load(refresh: true) } } label: {
                            Label(String(localized: "Retry"), systemImage: Theme.Icon.error)
                        }
                    }
                }
                if effective.picked {
                    Section {
                        Button(role: .destructive) {
                            Task {
                                if await panel.followDefault() { dismiss() }
                            }
                        } label: {
                            Label(String(localized: "Follow Main Chat default"), systemImage: Theme.Icon.clearContext)
                                .lineLimit(1)
                        }
                        .disabled(panel.busyLauncher != nil)
                    } footer: {
                        Text(String(localized: "This conversation has its own model. Following the default makes its next messages use Model configuration › Main chat again."))
                    }
                }
                if panel.isMissing(effective.launcher) {
                    Section {
                        VStack(alignment: .leading, spacing: Theme.Space.xxs) {
                            Text(effective.launcher).font(Theme.Font.heading).foregroundStyle(.secondary)
                            if let model = effective.model {
                                Text(SessionModes.modelName(model) ?? model)
                                    .font(Theme.Font.label).foregroundStyle(.secondary)
                            }
                            Text("This conversation's launcher is no longer available. Follow the Main Chat default or choose another launcher.")
                                .font(Theme.Font.label).foregroundStyle(.secondary)
                        }
                        .accessibilityIdentifier("assistantModel.missing.\(effective.launcher)")
                    }
                }
                ForEach(panel.response?.launchers ?? []) { launcher in
                    section(launcher)
                }
                if !panel.unavailable.isEmpty {
                    Section(String(localized: "Unavailable here")) {
                        ForEach(panel.unavailable) { item in
                            UnavailableLauncherRow(item: item)
                                .accessibilityIdentifier("assistantModel.unavailable.\(item.name)")
                        }
                    }
                }
            }
            .themedPage()
            .navigationTitle(String(localized: "Model [Assistant View c98e11]"))
            #if os(iOS) || os(visionOS)
            .navigationBarTitleDisplayMode(.inline)
            #endif
            .toolbar {
                SheetClose { dismiss() }
            }
            .refreshable { await panel.load(refresh: true) }
            .task { await panel.load() }
            .sheet(item: Binding(get: { typingFor.map { TypingTarget(launcher: $0) } }, set: { typingFor = $0?.launcher })) { target in
                TypedModelSheet(machine: panel.machine, launcher: target.launcher, purpose: "assistant",
                                note: String(localized: "Used for this conversation's next messages, on every device.")) { id in
                    Task {
                        if await panel.choose(launcher: target.launcher, model: id, session: session) { dismiss() }
                    }
                }
            }
        }
        .presentationDetents([.medium, .large])
        .presentationDragIndicator(.visible)
    }

    @ViewBuilder
    private func section(_ launcher: Launcher) -> some View {
        let list = panel.list(launcher.name)
        let locked = list?.locked
        let models = panel.models(launcher.name)
        let configured = AssistantModelChoice.configuredFallback(launcher, models: models, locked: locked)
        let selected = AssistantModelChoice.selectedModel(
            effective, launcher: launcher.name, aliases: panel.selectionAliases(launcher.name), configuredModel: configured?.id)
        let otherEngine = effective.launcher != launcher.name && AssistantModelChoice.isOtherEngine(
            sessionAgent: AssistantModelChoice.sessionEngine(session), launcherAgent: launcher.runtimeAgent)
        Section {
            if otherEngine {
                let to = AssistantModelChoice.otherEngineName(launcher.runtimeAgent)
                Label(String(localized: "Switching from \(session?.agentLabel ?? "Claude Code") to \(to) needs a new conversation"),
                      systemImage: Theme.Icon.locked)
                    .font(Theme.Font.secondary).foregroundStyle(.secondary)
                    .accessibilityIdentifier("assistantModel.otherEngine.\(launcher.name)")
            } else {
                // A real Default row comes from the catalog. Add one only when the current setup has no such row.
                if selected != nil, configured == nil, !models.contains(where: { $0.isDefault }) {
                    row(launcher: launcher.name, modelID: "", title: String(localized: "Default [Prompt Composer 844b8c]"),
                        detail: launcher.model == "默认" ? nil : launcher.model, ticked: selected == "", locked: nil) // i18n-ignore: legacy wire value, never displayed
                }
                if let locked {
                    Label(locked, systemImage: Theme.Icon.locked)
                        .font(Theme.Font.secondary).foregroundStyle(.secondary)
                        .accessibilityIdentifier("assistantModel.locked.\(launcher.name)")
                }
                if let problem = panel.error(launcher.name), list != nil {
                    HStack(spacing: Theme.Space.s) {
                        Label(problem, systemImage: Theme.Icon.error)
                            .font(Theme.Font.secondary).foregroundStyle(Theme.cautionText)
                        Spacer(minLength: Theme.Space.s)
                        Button(String(localized: "Retry")) { Task { await panel.load(refresh: true) } }
                            .lineLimit(1)
                    }
                }
                if let list {
                    if let configured {
                        // No catalog (a direct API or an older node): its configured model is still a concrete choice.
                        // A locked launcher sends Default (nil), never an overriding concrete model ID.
                        row(launcher: launcher.name, modelID: configured.sendsDefault ? "" : configured.id,
                            title: SessionModes.modelName(configured.id) ?? configured.id,
                            detail: nil, ticked: selected == configured.id || (configured.sendsDefault && selected == ""), locked: nil)
                    } else {
                        ForEach(models) { m in
                            row(launcher: launcher.name, modelID: m.isDefault ? "" : m.id,
                                title: m.displayName, detail: m.detail,
                                ticked: selected == (m.isDefault ? "" : m.id), locked: nil, custom: m.custom)
                        }
                    }
                    if list.takesTypedModel, model.offersTypedModels(panel.key.group) {
                        Button { typingFor = launcher.name } label: {
                            Label(String(localized: "Other Model…"), systemImage: Theme.Icon.keyboard).lineLimit(1)
                        }
                        .accessibilityIdentifier("assistantModel.other.\(launcher.name)")
                    }
                } else if panel.error(launcher.name) != nil {
                    Button { Task { await panel.load(refresh: true) } } label: {
                        Label(String(localized: "Retry"), systemImage: Theme.Icon.error).lineLimit(1)
                    }
                } else {
                    HStack(spacing: Theme.Space.s) {
                        ProgressView().controlSize(.small)
                        Text(String(localized: "Reading models…")).font(Theme.Font.secondary).foregroundStyle(.secondary)
                    }
                }
            }
        } header: {
            Text(launcher.displayName?.emptyAsNil ?? launcher.name)
        }
    }

    private func row(launcher: String, modelID: String, title: String, detail: String?, ticked: Bool,
                     locked: String?, custom: Bool = false) -> some View {
        let second = launcherModelRowDetail(owner: launcher, detail: detail)
        return Button {
            Haptics.tap()
            Task {
                if await panel.choose(launcher: launcher, model: modelID.isEmpty ? nil : modelID, session: session) {
                    dismiss()
                }
            }
        } label: {
            HStack(spacing: Theme.Space.m) {
                VStack(alignment: .leading, spacing: Theme.Space.xxs) {
                    Text(title).font(custom ? Theme.Font.heading.monospaced() : Theme.Font.heading)
                        .foregroundStyle(.primary).lineLimit(1)
                    if let second {
                        Text(second).font(Theme.Font.label.monospaced()).foregroundStyle(.secondary).lineLimit(1)
                    }
                }
                Spacer(minLength: Theme.Space.s)
                if ticked { ThemedIcon(Theme.Icon.done).foregroundStyle(Theme.accent).fontWeight(.semibold) }
            }
            .contentShape(.rect)
        }
        .disabled(locked != nil || panel.busyLauncher != nil)
        .accessibilityIdentifier("assistantModel.\(launcher).\(modelID.isEmpty ? "default" : modelID)")
    }

    private struct TypingTarget: Identifiable { let launcher: String; var id: String { launcher } }
}
