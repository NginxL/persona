import SwiftUI

// Agents › ⋯ › Terminals (#4754, docs/integrations/engines.md「Agents run in a terminal」): every pane of each computer's
// terminal engine, the Agents' and plain terminals alike, to open in the terminal (watch and type) and close. A plain pane
// opens by its `pane:` address; an Agent's, and a stopped Agent's left behind, by its session, whose terminal page offers
// Resume and closes as any (#4328). The list is the hub's `GET /machines/{mid}/panes`; nothing on a screen is read for it.

/// The Terminals page: one computer's (`MachineRef.path`), or every computer's when nil.
nonisolated struct TerminalsRoute: Hashable {
    var machine: String? = nil
}

struct TerminalsView: View {
    @Environment(AppModel.self) private var model
    let only: String?
    /// Each computer's answer and its request, by `MachineRef.path`.
    @State private var loads: [String: TerminalsLoad] = [:]
    @State private var open: TerminalTarget?
    @State private var closing: CloseTarget?
    @State private var finished: FinishedTarget?

    /// A terminal to show over the page.
    struct TerminalTarget: Identifiable { let key: SessionKey; var id: String { key.group + "|" + key.machineID + "|" + key.sessionID } }
    /// A pane to close, asked first.
    struct CloseTarget: Identifiable { let machine: MachineRef; let pane: TerminalPanes.Pane; let title: String; var id: String { machine.path + pane.pane } }
    /// The terminals stopped Agents left on a computer, as the node lists them now (preview), to close once confirmed.
    struct FinishedTarget: Identifiable { let machine: MachineRef; let name: String; let panes: [FinishedTerminal]; var id: String { machine.path } }

    private var machines: [MachineChoice] { model.machineChoices.filter { only == nil || $0.id == only } }

    var body: some View {
        List {
            ForEach(machines) { m in
                let ref = MachineRef(path: m.id)
                Section {
                    content(ref)
                } header: {
                    header(m, ref)
                }
            }
        }
        .listStyle(.plain)
        .themedPage()
        .overlay {
            if machines.isEmpty {
                EmptyStateView(symbol: Theme.Icon.terminal, title: String(localized: "No computers"),
                               message: String(localized: "Terminals on your computers show up here once one is connected."))
            }
        }
        .navigationTitle("Terminals")
        #if os(iOS) || os(visionOS)
        .navigationBarTitleDisplayMode(.inline)
        #endif
        // Again when the computers change: right after the app connects the list is still empty.
        .task(id: machines.map(\.id)) { await load() }
        .refreshable { await load() }
        #if os(iOS)
        .fullScreenCover(item: $open) { t in TerminalCover(key: t.key).presentationBackground(.clear) }
        #else
        .sheet(item: $open) { t in TerminalCover(key: t.key) }
        #endif
        .confirmationDialog(closing.map { String(localized: "Close \"\($0.title)\"?") } ?? "", isPresented: Binding(get: { closing != nil }, set: { if !$0 { closing = nil } }),
                            titleVisibility: .visible, presenting: closing) { c in
            Button(String(localized: "Close Terminal"), role: .destructive) { close(c) }
            Button("Cancel", role: .cancel) {}
        } message: { _ in
            Text("Whatever runs in it stops. This can't be undone.")
        }
        .confirmationDialog(finished.map { String(localized: "Close \($0.panes.count) finished terminals on \($0.name)?") } ?? "",
                            isPresented: Binding(get: { finished != nil }, set: { if !$0 { finished = nil } }),
                            titleVisibility: .visible, presenting: finished) { f in
            Button(String(localized: "Close Terminals"), role: .destructive) { closeFinished(f) }
            Button("Cancel", role: .cancel) {}
        } message: { f in
            Text(f.panes.map { title(finished: $0.sessionID, on: f.machine) }.joined(separator: "\n"))
        }
    }

    // MARK: - Rows

    @ViewBuilder private func content(_ ref: MachineRef) -> some View {
        let load = loads[ref.path] ?? TerminalsLoad()
        switch load.shown {
        case .failed(let error):
            problem(error, ref)
        case .late(let request):
            problem(TerminalsLoad.withRequest(String(localized: "This computer hasn't answered yet."), request), ref)
        case .rows(let list):
            let rows = TerminalsList.rows(list)
            if rows.isEmpty { empty(list, ref) }
            ForEach(rows) { r in row(r, on: ref, in: list).themedRow() }
        case .waiting:
            // Its panes are on their way; no answer within `patience` says so, with Retry (#6153). The row's own task
            // times it, so the page on screen can't sit on a bare spinner even if no load of its own ran.
            ProgressView().frame(maxWidth: .infinity).themedRow()
                .task(id: load.request) {
                    try? await Task.sleep(for: TerminalsLoad.patience)
                    if !Task.isCancelled { loads[ref.path, default: TerminalsLoad()].expire(load.request) }
                }
        }
    }

    /// A computer with no terminals: New Terminal when its engine can open one (in its home folder when it has no
    /// workspace yet), otherwise where terminals come from.
    @ViewBuilder private func empty(_ list: TerminalPanes, _ ref: MachineRef) -> some View {
        if let space = TerminalsList.spaces(list, newSpace: hubHasNewSpace(ref)).first {
            HStack(spacing: Theme.Space.m) {
                Text("No terminals open on this computer.").font(Theme.Font.label).foregroundStyle(.secondary)
                Spacer(minLength: 0)
                Button("New Terminal") { newTerminal(on: ref, space: space) }
                    .foregroundStyle(Theme.accent).lineLimit(1).accessibilityIdentifier("terminals.new")
            }
            .themedRow()
        } else {
            Text("Terminals open here when an Agent starts on this computer.").font(Theme.Font.label).foregroundStyle(.secondary).themedRow()
        }
    }

    /// Why a computer's terminals aren't listed, with Retry.
    private func problem(_ text: String, _ ref: MachineRef) -> some View {
        HStack(spacing: Theme.Space.m) {
            Text(text).font(Theme.Font.label).foregroundStyle(.secondary)
            Spacer(minLength: 0)
            Button("Retry") { Task { await load([ref.path]) } }.foregroundStyle(Theme.accent).lineLimit(1).accessibilityIdentifier("terminals.retry")
        }
        .themedRow()
    }

    private func row(_ r: TerminalsList.Row, on ref: MachineRef, in list: TerminalPanes) -> some View {
        let name = rowTitle(r, on: ref, in: list)
        return Button { show(r, on: ref) } label: {
            HStack(spacing: Theme.Space.m) {
                ThemedIcon(r.kind == .agent ? Theme.Icon.agents : Theme.Icon.terminal)
                    .font(Theme.Font.body)
                    .foregroundStyle(r.kind == .finished ? .secondary : .primary)
                    .scaledFrame(width: Theme.Size.icon) // one column: the titles and separators line up
                VStack(alignment: .leading, spacing: Theme.Space.xxs) {
                    Text(name).font(Theme.Font.body).foregroundStyle(Color.primary).lineLimit(1)
                    Text(subtitle(r, on: ref, title: name)).font(Theme.Font.label).foregroundStyle(.secondary).lineLimit(1)
                }
                Spacer(minLength: 0)
            }
            .padding(.vertical, Theme.Space.xs)
            .frame(maxWidth: .infinity, alignment: .leading)
            .contentShape(.rect)
        }
        .buttonStyle(.plain)
        .accessibilityIdentifier("terminals.row")
        .swipeActions(edge: .trailing, allowsFullSwipe: false) {
            if r.kind != .agent {
                Button(role: .destructive) { closing = CloseTarget(machine: ref, pane: r.pane, title: name) } label: {
                    Label("Close Terminal", systemImage: Theme.Icon.end)
                }
            }
        }
        .contextMenu {
            Button { show(r, on: ref) } label: { Label("Open Terminal", systemImage: Theme.Icon.terminal) }
            if r.kind != .agent {
                Button(role: .destructive) { closing = CloseTarget(machine: ref, pane: r.pane, title: name) } label: {
                    Label("Close Terminal…", systemImage: Theme.Icon.end)
                }
            }
        }
    }

    @ViewBuilder private func header(_ m: MachineChoice, _ ref: MachineRef) -> some View {
        HStack(spacing: Theme.Space.sm) {
            ThemedIcon(m.symbol)
            Text(m.name).lineLimit(1)
            Spacer(minLength: 0)
            if let list = loads[ref.path]?.list {
                Menu {
                    ForEach(TerminalsList.spaces(list, newSpace: hubHasNewSpace(ref)), id: \.id) { s in
                        Button { newTerminal(on: ref, space: s) } label: {
                            Label(String(localized: "New Terminal in \(s.label)"), systemImage: Theme.Icon.add)
                        }
                    }
                    if TerminalsList.hasFinished(list) {
                        Divider()
                        Button(role: .destructive) { askCloseFinished(ref, name: m.name) } label: {
                            Label("Close Finished Terminals…", systemImage: Theme.Icon.cleanUpAll)
                        }
                    }
                } label: {
                    // A finger's square, the glyph at its trailing edge where it was. A menu is hit only in its label's
                    // frame, and the header's cell holds no more than it lays out: the square takes `Space.s` above and
                    // below as room, so the header grows 10 pt rather than 26, and all of the square is inside it (#6577).
                    ThemedIcon(Theme.Icon.more).tapTargetFrame(alignment: .trailing)
                }
                .padding(.vertical, -Theme.Space.s)
                .accessibilityLabel(String(localized: "Terminal actions for \(m.name)"))
                .accessibilityIdentifier("terminals.more")
            }
        }
        .font(Theme.Font.secondaryStrong)
    }

    private func rowTitle(_ r: TerminalsList.Row, on ref: MachineRef, in list: TerminalPanes) -> String {
        switch r.kind {
        case .agent:
            let s = r.pane.sessionID.flatMap { model.session(SessionKey(group: ref.group, machineID: ref.id, sessionID: $0)) }
            return s?.title.nilIfEmpty ?? r.pane.title?.nilIfEmpty ?? NodeMenu.shellTitle(r.pane, in: list)
        case .finished:
            return title(finished: r.pane.agentSession ?? "", on: ref)
        case .plain:
            return NodeMenu.shellTitle(r.pane, in: list)
        }
    }

    private func title(finished id: String, on ref: MachineRef) -> String {
        let s = model.session(SessionKey(group: ref.group, machineID: ref.id, sessionID: id))
        let name = s?.title.nilIfEmpty ?? String(id.prefix(8))
        return String(localized: "Finished: \(name)")
    }

    private func subtitle(_ r: TerminalsList.Row, on ref: MachineRef, title: String) -> String {
        let dir = TerminalsList.folder(r.pane.cwd)
        switch r.kind {
        case .agent:
            // The name the app gives the Agent everywhere (Session.agentLabel), not the runtime's id.
            let s = r.pane.sessionID.flatMap { model.session(SessionKey(group: ref.group, machineID: ref.id, sessionID: $0)) }
            return [s?.agentLabel ?? TerminalsList.agentName(r.pane.agent), dir].filter { !$0.isEmpty }.joined(separator: " · ")
        case .finished, .plain:
            return ([String(localized: "Terminal")] + TerminalsList.plainDetails(title: title, foreground: r.pane.foreground, folder: dir))
                .joined(separator: " · ")
        }
    }

    // MARK: - Actions

    private func show(_ r: TerminalsList.Row, on ref: MachineRef) {
        let session: String
        switch r.kind {
        case .agent: session = r.pane.sessionID ?? r.pane.terminal
        case .finished: session = r.pane.agentSession ?? r.pane.terminal // its session's terminal: the shell it left, Resume
        case .plain: session = r.pane.terminal
        }
        open = TerminalTarget(key: SessionKey(group: ref.group, machineID: ref.id, sessionID: session))
    }

    /// A new request for each computer (`only`: those), under its own short id (`X-Client-Request-Id`, which the hub's
    /// log keeps), so a report of a stuck page can be matched to the hub's log.
    private func load(_ only: Set<String>? = nil) async {
        await withTaskGroup(of: (String, String, Result<TerminalPanes, Error>).self) { group in
            for m in machines where only?.contains(m.id) ?? true {
                let ref = MachineRef(path: m.id)
                var api = model.api(ref.group)
                #if DEBUG
                if UserDefaults.standard.bool(forKey: "showTerminalsUnlinked") { api = nil } // screenshots of the state below
                #endif
                guard let api else {
                    loads[m.id] = TerminalsLoad(failure: String(localized: "Not connected to this computer's hub yet"))
                    continue
                }
                let id = TerminalsLoad.newRequest()
                loads[m.id, default: TerminalsLoad()].start(id)
                group.addTask {
                    do { return (m.id, id, .success(try await api.terminalPanes(machine: ref.id, requestID: id))) } catch { return (m.id, id, .failure(error)) }
                }
            }
            for await (path, id, result) in group {
                switch result {
                case .success(let list): loads[path]?.answer(id, list)
                case .failure(let error) where error.isCancellation: break // left: no answer runs late, with Retry
                case .failure(let error): loads[path]?.fail(id, TerminalsLoad.withRequest(error.shown ?? error.localizedDescription, id))
                }
            }
        }
    }

    /// The hub checks the node can open a workspace for New Terminal (`pane_new_space`, #6362); an unknown list offers it.
    private func hubHasNewSpace(_ ref: MachineRef) -> Bool {
        let f = model.features(ref.group)
        return f.isEmpty || f.contains("pane_new_space")
    }

    private func newTerminal(on ref: MachineRef, space: TerminalsList.Space) {
        guard let api = model.api(ref.group) else { return }
        Task {
            do {
                // no workspace yet: the node opens one (new_space: the hub answers node_outdated for a node that can't)
                var body: [String: JSONValue] = space.id.isEmpty ? ["cwd": .string("~"), "new_space": .bool(true)] : ["workspace_id": .string(space.id)]
                if !space.kind.isEmpty { body["kind"] = .string(space.kind) }
                let data = try await api.nodeAction(machine: ref.id, "panes", body)
                await load()
                if let made = try? JSONDecoder().decode(NewShell.self, from: data), !made.terminal.isEmpty {
                    open = TerminalTarget(key: SessionKey(group: ref.group, machineID: ref.id, sessionID: made.terminal))
                }
            } catch {
                NodeActions.shared.fail(String(localized: "Couldn't open a terminal"), error)
            }
        }
    }

    private func close(_ c: CloseTarget) {
        guard let api = model.api(c.machine.group) else { return }
        Task {
            do {
                try await api.nodeAction(machine: c.machine.id, "panes/close", ["pane": c.pane.pane])
                model.toast = Toast(String(localized: "Terminal closed"), style: .success)
                await load()
            } catch {
                NodeActions.shared.fail(String(localized: "Couldn't close the terminal"), error)
            }
        }
    }

    private func askCloseFinished(_ ref: MachineRef, name: String) {
        guard let api = model.api(ref.group) else { return }
        Task {
            do {
                let data = try await api.nodeAction(machine: ref.id, "panes/close_finished", ["preview": true], timeout: 70)
                let got = try JSONDecoder().decode(FinishedAnswer.self, from: data)
                if got.closed.isEmpty {
                    model.toast = Toast(String(localized: "No finished terminals to close"), style: .info)
                } else {
                    finished = FinishedTarget(machine: ref, name: name, panes: got.closed)
                }
            } catch {
                NodeActions.shared.fail(String(localized: "Couldn't list finished terminals"), error)
            }
        }
    }

    private func closeFinished(_ f: FinishedTarget) {
        guard let api = model.api(f.machine.group) else { return }
        Task {
            do {
                let data = try await api.nodeAction(machine: f.machine.id, "panes/close_finished",
                                                    ["panes": f.panes.map(\.pane).joined(separator: ",")], timeout: 70)
                let got = try JSONDecoder().decode(FinishedAnswer.self, from: data)
                model.toast = Toast(String(localized: "Closed \(got.closed.count) terminals"), style: .success)
                await load()
            } catch {
                NodeActions.shared.fail(String(localized: "Couldn't close the terminals"), error)
            }
        }
    }
}

/// What `panes/close_finished` answers (`docs/protocol/protocol.md`).
nonisolated struct FinishedAnswer: Decodable, Sendable {
    var closed: [FinishedTerminal]
    enum CodingKeys: String, CodingKey { case closed }
    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        closed = try c.decodeIfPresent([FinishedTerminal].self, forKey: .closed) ?? []
    }
}

nonisolated struct FinishedTerminal: Decodable, Sendable, Equatable {
    var pane: String
    var sessionID: String
    enum CodingKeys: String, CodingKey { case pane, sessionID = "session_id" }
}

/// What `POST /machines/{mid}/panes` opened.
nonisolated struct NewShell: Decodable, Sendable {
    var terminal: String
}

/// The rows of the Terminals page from a computer's panes: pure, so it is tested without a hub.
nonisolated enum TerminalsList {
    enum Kind: Equatable { case agent, finished, plain }

    struct Row: Identifiable, Equatable {
        var pane: TerminalPanes.Pane
        var kind: Kind
        var id: String { pane.pane }
    }

    struct Space: Equatable {
        var id: String
        var label: String
        var kind: String
    }

    /// Every pane of every host, Agents' first within a tab, in the hosts' order of workspaces and tabs.
    static func rows(_ list: TerminalPanes) -> [Row] {
        list.hosts.flatMap { h in
            h.panes.map { p in
                let kind: Kind = !(p.agent ?? "").isEmpty || !(p.sessionID ?? "").isEmpty ? .agent
                    : !(p.agentSession ?? "").isEmpty ? .finished : .plain
                return Row(pane: p, kind: kind)
            }
        }
    }

    /// The workspaces a new terminal can open in: those of hosts that open tabs. A host that opens tabs but has no
    /// workspace yet offers its home folder (id "") when the hub has `pane_new_space` (`newSpace`): the node opens a new
    /// workspace there.
    static func spaces(_ list: TerminalPanes, newSpace: Bool) -> [Space] {
        list.hosts.filter(\.newTab).flatMap { h in
            h.workspaces.isEmpty ? (newSpace ? [Space(id: "", label: String(localized: "Home Folder"), kind: h.kind)] : [])
                : h.workspaces.map { Space(id: $0.id, label: $0.label.isEmpty ? $0.id : $0.label, kind: h.kind) }
        }
    }

    static func hasFinished(_ list: TerminalPanes) -> Bool {
        list.hosts.contains { $0.panes.contains { !($0.agentSession ?? "").isEmpty } }
    }

    /// An Agent's name as the app shows it (Session.agentLabel) from a pane's runtime id, when no session row says.
    static func agentName(_ raw: String?) -> String {
        switch (raw ?? "").lowercased() {
        case "", "claude": "Claude"
        case "codex": "Codex"
        case "traex": "TRAEX"
        default: raw ?? ""
        }
    }

    /// What follows "Terminal" under a plain pane: the program in front unless its title already names it, then the
    /// folder.
    static func plainDetails(title: String, foreground: String?, folder: String) -> [String] {
        var out: [String] = []
        if let fg = foreground, !fg.isEmpty, !title.localizedCaseInsensitiveContains(fg) { out.append(fg) }
        if !folder.isEmpty { out.append(folder) }
        return out
    }

    /// A pane's folder by its last name ("" when not known): the computer's paths mean little on the phone.
    static func folder(_ cwd: String?) -> String {
        guard let cwd, !cwd.isEmpty else { return "" }
        return (cwd as NSString).lastPathComponent
    }
}

/// One computer's answer on the Terminals page and the request on its way (#6153): pure, so it is tested without a
/// hub or a clock. A computer always ends up listed, failed or late (with Retry), never on a bare spinner.
nonisolated struct TerminalsLoad {
    var list: TerminalPanes?
    var failure: String?
    /// The latest request's short id; an answer to an older one is ignored, the newer one answers or runs late.
    var request: String?
    /// No answer to `request` within `patience`.
    var late = false

    /// How long a computer's spinner turns before the page says it hasn't answered (the request itself waits 30 s).
    static let patience: Duration = .seconds(15)

    enum Shown: Equatable { case waiting, rows(TerminalPanes), failed(String), late(String?) }

    var shown: Shown {
        if let failure { return .failed(failure) }
        if let list { return .rows(list) }
        return late ? .late(request) : .waiting
    }

    /// A new request: the old failure and lateness go; the list stays until the answer.
    mutating func start(_ id: String) {
        request = id
        failure = nil
        late = false
    }

    mutating func answer(_ id: String, _ list: TerminalPanes) {
        guard id == request else { return }
        self.list = list
        failure = nil
        late = false
    }

    mutating func fail(_ id: String, _ message: String) {
        guard id == request else { return }
        failure = message
    }

    /// `patience` ran out for `id` (nil: the spinner showed with no request of this page's on its way).
    mutating func expire(_ id: String?) {
        guard id == request, list == nil, failure == nil else { return }
        late = true
    }

    /// An id as `APIClient` makes one: 8 lowercase hex characters.
    static func newRequest() -> String { String(UUID().uuidString.prefix(8)).lowercased() }

    /// `text` naming request `id`, unless it does already (a server error says its request).
    static func withRequest(_ text: String, _ id: String?) -> String {
        guard let id, !text.contains(id) else { return text }
        return String(localized: "\(text) (request \(id))")
    }
}

private extension String {
    var nilIfEmpty: String? { isEmpty ? nil : self }
}
