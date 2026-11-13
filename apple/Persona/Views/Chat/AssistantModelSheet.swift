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
    /// row, else the picked model's id.
    static func selectedModel(_ e: Effective, launcher: String) -> String? {
        guard e.launcher == launcher else { return nil }
        return e.model?.emptyAsNil ?? ""
    }

    /// The pick a row keeps: nil for the setup already in force (the hub would store a no-op). A Default row sends no
    /// model; `effort` carries the setup's current effort so the pick keeps it.
    static func pick(launcher: String, model: String?, effort: String?, current: Effective) -> ModelPick? {
        let sentModel = model?.emptyAsNil
        if current.launcher == launcher, (current.model?.emptyAsNil ?? nil) == sentModel { return nil }
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
        var models: (_ machine: MachineRef, _ launcher: String) async throws -> LauncherModels
        var keep: (_ key: SessionKey, _ pick: ModelPick) async -> Bool
        var drop: (_ key: SessionKey) async -> Bool
        static var live: Deps {
            Deps(
                launchers: { try await AppModel.shared.assistantLaunchers($0, refresh: $1) },
                models: { try await AppModel.shared.api($0.group)?.launcherModels(machine: $0.id, launcher: $1)
                    ?? LauncherModels(agent: "", models: [], locked: nil, machine: nil) },
                keep: { await AppModel.shared.keepConversationModel($0, pick: $1) },
                drop: { await AppModel.shared.dropConversationModel($0) })
        }
    }

    let key: SessionKey
    let machine: MachineRef
    private(set) var response: APIClient.LaunchersResponse?
    /// Each launcher's `/model` list, by launcher name.
    private(set) var lists: [String: LauncherModels] = [:]
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
        } catch {
            guard !error.isCancellation else { return }
            loadError = error.shown
            return
        }
        for launcher in response?.launchers ?? [] {
            await readList(launcher.name)
        }
    }

    private func readList(_ name: String, refresh: Bool = false) async {
        do {
            let answer = try await deps.models(machine, name)
            lists[name] = answer
            listErrors[name] = nil
        } catch {
            if let e = error as? APIError, e.status == 404 { return } // older hub/node: no list, the launcher row stands alone
            guard !error.isCancellation else { return }
            listErrors[name] = error.shown
        }
    }

    /// Keeps `model` (nil: its default) for `launcher`; returns whether the hub took it.
    func choose(launcher: String, model: String?, session: Session?) async -> Bool {
        let current = AssistantModelChoice.effective(session: session, response: response)
        guard let pick = AssistantModelChoice.pick(launcher: launcher, model: model, effort: current.effort, current: current) else { return true }
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
                if panel.loadError != nil, panel.response == nil {
                    Section {
                        Label(panel.loadError ?? "", systemImage: Theme.Icon.error)
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
                ForEach(panel.response?.launchers ?? []) { launcher in
                    section(launcher)
                }
                if !panel.unavailable.isEmpty {
                    Section(String(localized: "Unavailable here")) {
                        ForEach(panel.unavailable) { item in
                            HStack(alignment: .top, spacing: Theme.Space.m) {
                                ThemedIcon(Theme.Icon.locked).foregroundStyle(.secondary)
                                VStack(alignment: .leading, spacing: Theme.Space.xxs) {
                                    Text(item.displayName?.isEmpty == false ? item.displayName! : item.name)
                                        .font(Theme.Font.heading).foregroundStyle(.secondary).lineLimit(1)
                                    Text(item.reason).font(Theme.Font.label).foregroundStyle(.tertiary)
                                }
                            }
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
                ToolbarItem(placement: .cancellationAction) {
                    Button(String(localized: "Close")) { dismiss() }.lineLimit(1)
                }
            }
            .refreshable { await panel.load(refresh: true) }
            .task { await panel.load() }
            .sheet(item: Binding(get: { typingFor.map { TypingTarget(launcher: $0) } }, set: { typingFor = $0?.launcher })) { target in
                TypedModelSheet(machine: panel.machine, launcher: target.launcher,
                                note: String(localized: "Used for this conversation's next messages, on every device.")) { id in
                    Task {
                        if await panel.choose(launcher: target.launcher, model: id, session: session) { dismiss() }
                    }
                }
            }
        }
        .presentationDetents([.medium, .large])
    }

    @ViewBuilder
    private func section(_ launcher: Launcher) -> some View {
        let selected = AssistantModelChoice.selectedModel(effective, launcher: launcher.name)
        Section {
            if selected != nil {
                row(launcher: launcher.name, modelID: "", title: String(localized: "Default [Prompt Composer 844b8c]"),
                    detail: launcher.model, ticked: selected == "", locked: panel.list(launcher.name)?.locked)
            }
            if let locked = panel.list(launcher.name)?.locked {
                Label(locked, systemImage: Theme.Icon.locked)
                    .font(Theme.Font.secondary).foregroundStyle(.secondary)
                    .accessibilityIdentifier("assistantModel.locked.\(launcher.name)")
            } else if let list = panel.list(launcher.name) {
                ForEach(list.models) { m in
                    row(launcher: launcher.name, modelID: m.id, title: m.displayName, detail: m.detail,
                        ticked: selected == m.id, locked: nil, custom: m.custom)
                }
                if list.takesTypedModel, model.keepsConversationModelPick(panel.key.group) {
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
        } header: {
            Text(launcher.name)
        }
    }

    private func row(launcher: String, modelID: String, title: String, detail: String?, ticked: Bool,
                     locked: String?, custom: Bool = false) -> some View {
        Button {
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
                    if let detail {
                        Text(detail).font(Theme.Font.label.monospaced()).foregroundStyle(.secondary).lineLimit(1)
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
