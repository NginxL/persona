import SwiftUI

/// Settings › Agent › Agent Folders: per computer, folders whose sessions (every runtime) are blocked (that computer stops
/// collecting them) or hidden (recorded, out of Agents and the assistant's view). A new rule blocks unless Hide is
/// chosen (#10053). Kept with the computer beside its Blocked Config Folders (Claude Code settings); offline computers
/// can be set up too. Each change saves the computer's whole list.
struct HiddenAgentDirsView: View {
    @Environment(AppModel.self) private var model
    private let scope = AssistantSettingsRequestScope(AppModel.shared)
    @State private var state: HiddenAgentDirs?
    @State private var machine = ""
    @State private var pattern = ""
    @State private var action: AgentDirAction = .block
    @State private var busy = false
    @State private var error: String?

    var body: some View {
        SettingsForm {
            Section {
                Picker(String(localized: "picker.machine", defaultValue: "Machine"), selection: $machine) {
                    ForEach(scope.machines(model), id: \.id) { m in Text(m.title).tag(m.id) }
                }
            }
            Section {
                if let error {
                    Text(error).font(Theme.Font.caption).foregroundStyle(Theme.danger)
                    if state == nil {
                        Button("Retry [Components b8784c]") { Task { await load() } }.formButton().lineLimit(1)
                    }
                }
                if let state {
                    ForEach(state.rules) { rule in
                        // Two lines: the folder gets the row's width beside its switch (one line with the action and
                        // Delete cut it to "~/…nc" at the largest text sizes), the action and Delete under it.
                        VStack(alignment: .leading, spacing: Theme.Space.s) {
                            Toggle(isOn: Binding(get: { rule.enabled }, set: { enabled in
                                var replacement = rule
                                replacement.enabled = enabled
                                Task { await save(state.replacing(rule, with: replacement)) }
                            })) {
                                Text(verbatim: rule.pattern).font(Theme.Font.code).lineLimit(2).truncationMode(.middle)
                            }
                            HStack(spacing: Theme.Space.m) {
                                Picker(String(localized: "agentDirs.action", defaultValue: "Action"), selection: Binding(get: { rule.effectiveAction }, set: { action in
                                    var replacement = rule
                                    replacement.action = action
                                    Task { await save(state.replacing(rule, with: replacement)) }
                                })) {
                                    ForEach(AgentDirAction.allCases, id: \.self) { Text(Self.title(of: $0)).tag($0) }
                                }
                                .labelsHidden().pickerStyle(.menu).fixedSize()
                                .accessibilityLabel(Text(verbatim: "\(String(localized: "agentDirs.action", defaultValue: "Action")): \(rule.pattern)"))
                                Spacer(minLength: 0)
                                Button("Delete", role: .destructive) {
                                    Task { await save(state.replacing(rule, with: nil)) }
                                }.formButton().lineLimit(1)
                            }
                        }
                    }
                    Picker(String(localized: "agentDirs.action", defaultValue: "Action"), selection: $action) {
                        ForEach(AgentDirAction.allCases, id: \.self) { Text(Self.title(of: $0)).tag($0) }
                    }
                    TextField("Folder path or glob", text: $pattern)
                        .font(Theme.Font.code)
                        .autocorrectionDisabled()
                        #if os(iOS) || os(visionOS)
                        .textInputAutocapitalization(.never)
                        #endif
                    if let problem = state.problem(pattern) {
                        Text(problem).font(Theme.Font.caption).foregroundStyle(.secondary)
                    }
                    Button(String(localized: "agentDirs.add", defaultValue: "Add Folder Rule")) { Task { await add() } }
                        .formButton().lineLimit(1)
                        .disabled(HiddenAgentDirs.cleaned(pattern).isEmpty || state.problem(pattern) != nil || machine.isEmpty)
                } else if error == nil {
                    ProgressView()
                }
            } header: {
                Text(String(localized: "agentDirs.title", defaultValue: "Agent Folders"))
            } footer: {
                // A String, not a key: Text would read *name* as Markdown italics and drop the asterisks the rule needs.
                Text(String(localized: "agentDirs.footer", defaultValue: "Sessions working in these folders, with any Agent. Block: this computer stops collecting them, so nothing in Persona shows or counts them, and their permission requests are left to the agent's own interface. Hide: they stay recorded but leave Agents and the assistant's view; active supervised tasks stay visible, and the Agents menu can show them for a moment. A path covers that folder and everything in it; ~ is this computer's home; *name* matches a folder name anywhere. Turn a rule off to show its sessions again. To stop collecting a Claude Code config folder altogether, use Blocked Config Folders in Claude Code Settings."))
            }
            .disabled(busy)
        }
        .themedGroupedPage()
        .detailTitle(String(localized: "agentDirs.title", defaultValue: "Agent Folders"))
        .task(id: machine) { await load() }
    }

    private func load() async {
        if machine.isEmpty {
            machine = scope.machines(model).first?.id ?? ""
            if machine.isEmpty { return }
        }
        guard let api = scope.api(model) else { error = ClaudeDirsLogic.noHub; return }
        let shown = machine
        state = nil
        busy = true
        defer { busy = false }
        do {
            let loaded = try await api.hiddenAgentDirs(machine: shown)
            guard shown == machine else { return }
            state = loaded
            error = nil
        } catch {
            if !error.isCancellation { self.error = error.localizedDescription }
        }
    }

    private func add() async {
        guard var next = state, next.problem(pattern) == nil else { return }
        let clean = HiddenAgentDirs.cleaned(pattern)
        guard !machine.isEmpty, !clean.isEmpty else { return }
        next.rules.append(HiddenAgentDirRule(pattern: clean, enabled: true, action: action))
        if await save(next) { pattern = "" }
    }

    static func title(of action: AgentDirAction) -> String {
        switch action {
        case .block: String(localized: "agentDirs.action.block", defaultValue: "Block")
        case .hide: String(localized: "agentDirs.action.hide", defaultValue: "Hide")
        }
    }

    @discardableResult private func save(_ next: HiddenAgentDirs) async -> Bool {
        guard !busy, let api = scope.api(model), let previous = state else { return false }
        let shown = machine
        busy = true
        state = next
        error = nil
        defer { busy = false }
        do {
            let saved = try await api.setHiddenAgentDirs(machine: shown, rules: next.rules)
            if shown == machine { state = saved }
            Haptics.success()
            return true
        } catch {
            if shown == machine { state = previous }
            if !error.isCancellation { self.error = error.localizedDescription; Haptics.error() }
            return false
        }
    }
}
