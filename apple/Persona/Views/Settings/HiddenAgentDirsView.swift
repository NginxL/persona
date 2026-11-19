import SwiftUI

/// Settings › Agent › Hidden Agent Folders: per computer, folders whose sessions (every runtime) stay recorded but leave
/// Agents and the assistant's view. Kept with the computer beside its Blocked Config Folders (Claude Code settings);
/// offline computers can be set up too. Each change saves the computer's whole list.
struct HiddenAgentDirsView: View {
    @Environment(AppModel.self) private var model
    private let scope = AssistantSettingsRequestScope(AppModel.shared)
    @State private var state: HiddenAgentDirs?
    @State private var machine = ""
    @State private var pattern = ""
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
                        HStack(spacing: Theme.Space.m) {
                            Toggle(isOn: Binding(get: { rule.enabled }, set: { enabled in
                                var replacement = rule
                                replacement.enabled = enabled
                                Task { await save(state.replacing(rule, with: replacement)) }
                            })) {
                                Text(verbatim: rule.pattern).font(Theme.Font.code).lineLimit(1).truncationMode(.middle)
                            }
                            Button("Delete", role: .destructive) {
                                Task { await save(state.replacing(rule, with: nil)) }
                            }.formButton().lineLimit(1)
                        }
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
                    Button("Add hidden folder") { Task { await add() } }
                        .formButton().lineLimit(1)
                        .disabled(HiddenAgentDirs.cleaned(pattern).isEmpty || state.problem(pattern) != nil || machine.isEmpty)
                } else if error == nil {
                    ProgressView()
                }
            } header: {
                Text("Hidden Agent Folders")
            } footer: {
                // A String, not a key: Text would read *name* as Markdown italics and drop the asterisks the rule needs.
                Text(String(localized: "Sessions working in these folders, with any Agent, stay recorded but leave Agents and the assistant's view. A path covers that folder and everything in it; ~ is this computer's home; *name* matches a folder name anywhere. Active supervised tasks stay visible. Turn a rule off to show its sessions again, or show them for a moment from the Agents menu. To stop collecting a Claude Code config folder altogether, use Blocked Config Folders in Claude Code Settings."))
            }
            .disabled(busy)
        }
        .themedGroupedPage()
        .detailTitle(String(localized: "Hidden Agent Folders"))
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
        next.rules.append(HiddenAgentDirRule(pattern: clean, enabled: true))
        if await save(next) { pattern = "" }
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
