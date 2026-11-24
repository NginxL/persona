import SwiftUI

/// The pure part of Blocked Config Folders (web `lib/agent-settings.ts` does the same): what a row says, and the rules after a
/// change. Blocking a row adds its path; unblocking lifts the rule that blocks it (a glob lifts every directory it covers).
nonisolated enum ClaudeDirsLogic {
    typealias Dir = APIClient.ClaudeDir
    typealias State = APIClient.ClaudeDirsState

    /// The path as the user reads it: the machine's home as ~.
    static func shortPath(_ path: String, home: String?) -> String {
        guard let home, !home.isEmpty, path == home || path.hasPrefix(home + "/") else { return path }
        return "~" + path.dropFirst(home.count)
    }

    static func samePath(_ pattern: String, _ path: String, home: String?) -> Bool {
        pattern == path || pattern == shortPath(path, home: home)
    }

    /// Under the path: where it came from, how many sessions, how many claudes run from it, the rule blocking it.
    static func meta(_ d: Dir, home: String?) -> String {
        var parts: [String] = []
        if d.isDefault == true { parts.append(String(localized: "Default folder; can't be blocked")) }
        else if d.source == "config" { parts.append(String(localized: "Set in hub.json")) }
        else { parts.append(String(localized: "Found automatically")) }
        if let n = d.sessions, n > 0 { parts.append(String(localized: "\(n) Chats [Claude Dirs Section 537bee]")) }
        if d.running > 0 { parts.append(String(localized: "\(d.running) running")) }
        if d.blocked == true, let by = d.blockedBy, !by.isEmpty, !samePath(by, d.path, home: home) { parts.append(String(localized: "Blocked by rule \(by)")) }
        return parts.joined(separator: " · ")
    }

    static func toggled(_ s: State, _ d: Dir) -> [String] {
        if d.blocked != true {
            return s.blocked.contains(d.path) ? s.blocked : s.blocked + [shortPath(d.path, home: s.home)]
        }
        let rule = d.blockedBy ?? d.path
        return s.blocked.filter { $0 != rule }
    }

    /// Rules that block none of the listed directories: listed on their own, to be deleted.
    static func looseRules(_ s: State) -> [String] {
        s.blocked.filter { p in !s.dirs.contains { $0.blocked == true && ($0.blockedBy == p || samePath(p, $0.path, home: s.home)) } }
    }

    static func cleaned(_ input: String) -> String {
        var p = input.trimmingCharacters(in: .whitespacesAndNewlines)
        while p.count > 1, p.hasSuffix("/") { p.removeLast() }
        return p
    }

    /// Why a typed rule cannot be added (nil = it can).
    static func problem(_ input: String, existing: [String]) -> String? {
        let p = cleaned(input)
        if p.isEmpty { return String(localized: "Enter a folder or pattern, e.g. ~/.automation/*") }
        if !p.hasPrefix("/") && !p.hasPrefix("~/") { return String(localized: "Must start with / or ~/ (~ is that computer's home folder)") }
        if p == "~" || p == "/" { return String(localized: "Can't block the whole home or root folder") }
        if existing.contains(p) { return String(localized: "Already blocked") }
        return nil
    }

    /// What the section says when the directories could not be loaded, with Retry under it; nil for a cancelled load
    /// (the page went away). A timeout or no network is said too: the page has nothing else to show in their place,
    /// and saying nothing left it spinning for good (#744).
    static func loadFailure(_ error: Error) -> String? {
        if error.isCancellation { return nil }
        if error.isTimeout || error.isOffline { return String(localized: "Can't reach the hub to load this computer's config folders") }
        return String(localized: "Couldn't load this computer's config folders: \(error.localizedDescription)")
    }

    /// No connection to this machine's hub at all (not paired, or its group gone).
    static var noHub: String { String(localized: "Not connected to this computer's hub yet") }

    /// A blocked list built from one computer's rows may only be sent to that same computer (#9276).
    /// Another computer's folders must never drive Block / Unblock / Add for the one just picked.
    static func canApply(blockedFrom sourceId: String, to targetId: String) -> Bool {
        !sourceId.isEmpty && sourceId == targetId
    }
}

/// Settings › Claude Code › Blocked Config Folders (docs/integrations/claude-integration.md「Blocking configuration directories」): the machine's Claude Code config
/// directories, each with Block / Unblock, and rules typed by hand (a path or a glob such as ~/.automation/*). A blocked
/// directory is not scanned or remembered and its sessions leave Agents; the files are not touched. Sections of the
/// page's List.
struct ClaudeDirsSection: View {
    @Environment(AppModel.self) private var model
    var ref: MachineRef
    @State private var state: APIClient.ClaudeDirsState?
    @State private var error: String?
    @State private var busy: String?
    @State private var input = ""
    @State private var loading = false

    private typealias L = ClaudeDirsLogic
    private static var fieldTitle: String { String(localized: "~/.automation/* or a path") }

    var body: some View {
        Section {
            if let error {
                Text(error).foregroundStyle(Theme.danger).font(Theme.Font.caption)
                if state == nil {
                    Button { Task { await load() } } label: { Label("Retry [Components b8784c]", systemImage: Theme.Icon.retry).lineLimit(1) }
                        .formButton()
                        .disabled(loading)
                }
            }
            if let state {
                // Capture the computer these rows belong to with the action; Block / Unblock / Add must not
                // send another computer's list to the one just picked (#9276).
                let sourceId = ref.id
                ForEach(state.dirs) { d in
                    row(L.shortPath(d.path, home: state.home), meta: L.meta(d, home: state.home), blocked: d.blocked == true) {
                        if d.isDefault != true {
                            Button(d.blocked == true ? String(localized: "Unblock") : String(localized: "Block")) {
                                let label = L.shortPath(d.path, home: state.home)
                                Task { await save(L.toggled(state, d), from: sourceId, key: d.path, done: d.blocked == true ? String(localized: "Unblocked \(d.blockedBy ?? label)") : String(localized: "Blocked \(label)")) }
                            }
                            .lineLimit(1)
                            .formButton()
                            .disabled(busy != nil || loading)
                        }
                    }
                }
                ForEach(L.looseRules(state), id: \.self) { p in
                    row(p, meta: String(localized: "Rule"), blocked: true) {
                        Button("Delete", role: .destructive) {
                            Task { await save(state.blocked.filter { $0 != p }, from: sourceId, key: p, done: String(localized: "Deleted rule \(p)")) }
                        }
                        .lineLimit(1)
                        .formButton()
                        .disabled(busy != nil || loading)
                    }
                }
                addRow(state, sourceId: sourceId)
            } else if error == nil {
                ProgressView().frame(maxWidth: .infinity)
            }
        } header: {
            Text("Blocked Config Folders")
        } footer: {
            VStack(alignment: .leading, spacing: Theme.Space.xs) {
                Text("Blocked folders aren't scanned or remembered, and their sessions don't appear in Agents (the files aren't touched). Unblock to bring them back. Useful for automation that runs claude with its own CLAUDE_CONFIG_DIR. The default folder can't be blocked.")
                if state?.live == false { Text("This computer is offline or its node is older: these are the folders the hub remembers. All of them show up once it connects.") }
            }
        }
        .task(id: ref.path) { await load() }
    }

    private func row<Trailing: View>(_ title: String, meta: String, blocked: Bool, @ViewBuilder trailing: () -> Trailing) -> some View {
        HStack(spacing: Theme.Space.m) {
            VStack(alignment: .leading, spacing: Theme.Space.xxs) {
                Text(verbatim: title)
                    .font(Theme.Font.code)
                    .strikethrough(blocked)
                    .foregroundStyle(blocked ? .secondary : .primary)
                    .lineLimit(1)
                    .truncationMode(.middle)
                Text(meta).font(Theme.Font.label).foregroundStyle(.secondary)
            }
            Spacer(minLength: Theme.Space.s)
            trailing()
        }
        .padding(.vertical, Theme.Space.xxs)
    }

    @ViewBuilder private func addRow(_ state: APIClient.ClaudeDirsState, sourceId: String) -> some View {
        let problem = L.problem(input, existing: state.blocked)
        VStack(alignment: .leading, spacing: Theme.Space.xs) {
            HStack(spacing: Theme.Space.m) {
                TextField(Self.fieldTitle, text: $input, prompt: Text("~/.automation/* or a path"))
                    .font(Theme.Font.code)
                    .autocorrectionDisabled()
                    #if os(iOS) || os(visionOS)
                    .textInputAutocapitalization(.never)
                    #endif
                    .submitLabel(.done)
                    .onSubmit { Task { await add(state, from: sourceId) } }
                Button { Task { await add(state, from: sourceId) } } label: {
                    Label("Block", systemImage: Theme.Icon.add).lineLimit(1)
                }
                .formButton()
                .disabled(input.isEmpty || problem != nil || busy != nil || loading)
            }
            if !input.isEmpty, let problem {
                Text(problem).font(Theme.Font.label).foregroundStyle(.secondary)
            }
        }
    }

    private func add(_ state: APIClient.ClaudeDirsState, from sourceId: String) async {
        guard L.problem(input, existing: state.blocked) == nil else { return }
        let p = L.cleaned(input)
        if await save(state.blocked + [p], from: sourceId, key: "+", done: String(localized: "Blocked \(p)")) { input = "" }
    }

    private func load() async {
        guard let api = model.api(ref.group) else {
            error = L.noHub
            return
        }
        // Computer changes reset via `.id(ref)` in ClaudeSettingsView; do not clear state / busy here —
        // this also runs on navigation back and Retry, and wiping busy would allow a double submit (#9276).
        let target = ref.id
        loading = true
        defer { loading = false }
        do {
            let next = try await api.claudeDirs(machine: target)
            guard target == ref.id else { return }
            state = next
            error = nil
        } catch {
            guard target == ref.id else { return }
            if let message = L.loadFailure(error) { self.error = message }
        }
    }

    @discardableResult private func save(_ blocked: [String], from sourceId: String, key: String, done: String) async -> Bool {
        // The list and the computer must be the same one's; never send another computer's folders to the one shown (#9276).
        guard L.canApply(blockedFrom: sourceId, to: ref.id), let api = model.api(ref.group) else { return false }
        busy = key
        defer { busy = nil }
        do {
            let next = try await api.setBlockedClaudeDirs(machine: sourceId, blocked: blocked)
            guard L.canApply(blockedFrom: sourceId, to: ref.id) else { return false }
            state = next
            error = nil
            Haptics.success()
            model.toast = Toast(done, style: .success)
            return true
        } catch {
            if !error.isCancellation {
                Haptics.error()
                self.error = String(localized: "Couldn't change it: \(error.shown ?? error.localizedDescription) [Claude Dirs Section b11ba8]")
            }
            return false
        }
    }
}
