import SwiftUI

#if os(iOS)
/// Search over the whole window, the open iPhone Duo's (#3705), as ChatGPT's there: the field at the window's foot, over
/// the keyboard, × beside it; the conversations above, the latest first, and what matches as it is typed. A row opens its
/// conversation. The Sidebar layout's one search (#4206, the owner), from its foot's Search at every width: two scopes,
/// Persona's chats and the Agents, starting on the one in front. The shell's drawer and the Separate layout's iPad keep
/// the sidebar's own search, its field at the sidebar's top.
struct AssistantFullSearch: View {
    /// What it looks through.
    enum Scope: Hashable {
        case chats, agents
    }

    var open: (SessionKey) -> Void
    /// Opens an Agent's chat; nil: Persona's chats only, no scopes.
    /// Not `openAgent`: a stored property's name stays in the Release binary, and that is a DEBUG launch switch
    /// (scripts/test/test_apple_release_switch_names.py, #8306).
    var onOpenAgent: ((SessionKey) -> Void)? = nil
    @Environment(AppModel.self) private var model
    @Environment(\.dismiss) private var dismiss
    @State private var query = ""
    @State private var scope: Scope
    /// Where the system puts its bars down the side (the open iPhone Duo on its side): nothing is above the page there.
    @State private var barEdge: HorizontalEdge?
    @FocusState private var focused: Bool
    /// What was said in the chats, found by the manager (#6284): merged into the title / last-message matches.
    @State private var chatSearch: AssistantChatSearch? // made on the first query (P3: no work in the initial value)

    init(scope: Scope = .chats, open: @escaping (SessionKey) -> Void, onOpenAgent: ((SessionKey) -> Void)? = nil) {
        self.open = open
        self.onOpenAgent = onOpenAgent
        _scope = State(initialValue: onOpenAgent == nil ? .chats : scope)
    }

    /// The sidebar's conversations (the latest first), read here: the sidebar under the cover is off the window, and what
    /// it handed in would not follow a conversation that comes or is renamed meanwhile.
    private var hits: [AssistantSearchMerge.Row] {
        let local = AssistantSidebarSearch.filter(model.assistantConversations, query)
        guard let answer = chatSearch?.answer(for: query) else { return local.map { AssistantSearchMerge.Row(session: $0) } }
        return AssistantSearchMerge.rows(local: local, answer: answer, known: model.assistantConversations + model.archivedConversations)
    }

    /// The Agents found (a list of agents, `AllSessionsReadersTests`): every Agent's chat but sub-agents' and archived
    /// ones, by the words in their title, what they were asked, their last words, folder, branch and machine
    /// (`Session.matchesScope(.all)`, the Agents list's search); the latest first. With no words, the latest.
    private var agentHits: [Session] {
        let terms = AgentParsedQuery.parse(query).terms
        return model.allSessions
            .filter { !$0.assistant && $0.isSubagent != true && $0.archived != true && (terms.isEmpty || $0.matchesScope(.all, terms: terms)) }
            .sorted { $0.sortDate > $1.sortDate }
            .prefix(Self.agentsShown).map { $0 }
    }

    /// How many Agents it lists at most: the latest, a screen or two.
    private static let agentsShown = 50

    var body: some View {
        List {
            switch scope {
            case .chats:
                Section {
                    ForEach(hits) { row($0.session, said: $0.hit) }
                } header: {
                    heading(query.isEmpty ? String(localized: "Recent") : String(localized: "\(hits.count) Chats [Assistant Sidebar a72a2a]"))
                }
                .listRowSeparator(.hidden)
                .listRowBackground(Color.clear)
            case .agents:
                let agents = agentHits
                Section {
                    ForEach(agents) { agentRow($0) }
                } header: {
                    heading(query.isEmpty ? String(localized: "Recent") : String(localized: "\(agents.count) Agents [Full Search]"))
                }
                .listRowSeparator(.hidden)
                .listRowBackground(Color.clear)
            }
        }
        .listStyle(.plain)
        .scrollContentBackground(.hidden)
        .scrollDismissesKeyboard(.interactively)
        .accessibilityIdentifier("full-search") // the list's own: on the whole page it named the bar's field and × too
        .overlay {
            if !query.isEmpty, scope == .chats ? hits.isEmpty : agentHits.isEmpty {
                VStack(spacing: Theme.Space.sm) {
                    ThemedIcon(Theme.Icon.search).font(Theme.Font.title).foregroundStyle(.tertiary)
                    Text("No Results for “\(query)”").font(Theme.Font.secondary).foregroundStyle(.secondary)
                }
            }
        }
        // Which to look through, at the top (#4206); only where Agents can be opened from here.
        .safeAreaInset(edge: .top, spacing: 0) { if onOpenAgent != nil { scopes } }
        .readsVerticalBarEdge($barEdge)
        // At the foot, over the keyboard (the inset rides on it), as ChatGPT's.
        .safeAreaInset(edge: .bottom, spacing: 0) { bar }
        .background(Theme.pageBackground.ignoresSafeArea())
        .onAppear { focused = true }
        .onChange(of: query) { _, q in
            if chatSearch == nil, !q.isEmpty { chatSearch = AssistantChatSearch() }
            chatSearch?.search(q, api: model.api, on: model.features(model.hub?.group ?? "").contains(AssistantChatSearch.feature))
        }
        .accessibilityAction(.escape) { dismiss() } // VoiceOver's scrub, as on a sheet
    }

    private var bar: some View {
        HStack(spacing: Theme.Space.m) {
            HStack(spacing: Theme.Space.s) {
                ThemedIcon(Theme.Icon.search).foregroundStyle(.secondary)
                TextField(scope == .chats ? String(localized: "Search Chats [Assistant Sidebar 55c9e4]") : String(localized: "Search Agents [Full Search]"), text: $query)
                    .font(Theme.Font.body)
                    .focused($focused)
                    .submitLabel(.search)
                    .autocorrectionDisabled()
                    .accessibilityIdentifier("full-search-field")
                if !query.isEmpty {
                    // Hit as a finger-sized square over the field's end, the glyph where it was (#6577).
                    Button { query = "" } label: { ThemedIcon(Theme.Icon.clear).foregroundStyle(.tertiary).tapTargetOutset(.all) }
                        .buttonStyle(.plain)
                        .accessibilityLabel("Clear Search")
                        .accessibilityIdentifier("full-search-clear")
                }
            }
            .padding(.horizontal, Theme.Space.l)
            .scaledFrame(minHeight: Theme.Size.tapTarget)
            .modifier(ComposerSurface())
            Button { dismiss() } label: {
                ThemedIcon(Theme.Icon.close)
                    .font(Theme.Font.heading)
                    .foregroundStyle(Color.primary)
                    .scaledFrame(width: Theme.Size.tapTarget, height: Theme.Size.tapTarget)
                    .modifier(ComposerSurface())
            }
            .buttonStyle(.plain)
            .keyboardShortcut(.cancelAction) // Esc: a cover has no swipe down
            .accessibilityLabel("Close")
            .accessibilityIdentifier("full-search-close")
        }
        .padding(.horizontal, Theme.Space.l)
        .padding(.vertical, Theme.Space.s)
    }

    /// Chats | Agents, the scope's segments.
    private var scopes: some View {
        SegmentedChoice(String(localized: "Search In [Full Search]"), [(value: Scope.chats, label: String(localized: "Chats")),
                                                                         (value: Scope.agents, label: String(localized: "Agents"))],
                        selection: $scope)
            .accessibilityIdentifier("full-search-scope")
            .padding(.horizontal, Theme.Space.l)
            .padding(.vertical, Theme.Space.s)
            // Under side bars nothing is above it: off the display's edge, as the sidebar's columns there (#4853).
            .padding(.top, barEdge == nil ? Theme.Space.flush : Theme.Space.s)
    }

    private func heading(_ text: String) -> some View {
        Text(text)
            .font(Theme.Font.secondaryMedium)
            .foregroundStyle(Color.primary)
            .textCase(nil)
    }

    /// An Agent found: its mark in its state's color, its title, and where the words were found (else its state and
    /// computer, as the Agents list says them). Opens its chat.
    private func agentRow(_ s: Session) -> some View {
        let snippet = query.isEmpty ? nil : s.evidenceSnippet(query: query)
        let machine = model.machine(s.machineRef)
        let title = s.title.isEmpty ? URL(fileURLWithPath: s.cwd).lastPathComponent : s.title
        return Button {
            onOpenAgent?(s.key)
        } label: {
            HStack(spacing: Theme.Space.m) {
                SessionAvatar(machine: machine, state: s.countedState, size: Theme.Size.tapTarget)
                VStack(alignment: .leading, spacing: Theme.Space.xxs) {
                    Text(title).font(Theme.Font.body).foregroundStyle(Color.primary).lineLimit(1)
                    Group {
                        if let snippet, snippet.role != .title {
                            Text(snippet.before) + Text(snippet.match).bold() + Text(snippet.after)
                        } else {
                            Text([s.countedState.label, machine?.name].compactMap { $0 }.joined(separator: " · "))
                        }
                    }
                    .font(Theme.Font.secondary)
                    .foregroundStyle(.secondary)
                    .lineLimit(1)
                }
                Spacer(minLength: 0)
            }
            .contentShape(.rect)
        }
        .buttonStyle(.plain)
        .accessibilityElement(children: .combine)
        .accessibilityIdentifier("full-search-agent")
    }

    /// `said`: the message a search found it by (#6284): its sentence in place of the last message, and opened at.
    private func row(_ s: Session, said: AssistantSearchAnswer.Hit? = nil) -> some View {
        let isMain = s.key == model.assistantMain
        let title = isMain ? String(localized: "Main Chat")
            : s.title.isEmpty || s.title == "Claude Code" ? String(localized: "New Chat [Quick Actions ea4b29]") : s.title
        let preview = s.lastText.map { Fmt.plain($0).replacingOccurrences(of: "\n", with: " ") } ?? ""
        return Button {
            if let said { model.messageJump = MessageJump(key: s.key, item: said.itemID) }
            open(s.key)
        } label: {
            HStack(spacing: Theme.Space.m) {
                ThemedIcon(isMain ? Theme.Icon.home : Theme.Icon.conversation) // the main conversation's own, as its sidebar row (#9973)
                    .font(Theme.Font.body)
                    .foregroundStyle(.secondary)
                    .scaledFrame(width: Theme.Size.tapTarget, height: Theme.Size.tapTarget)
                    .background(RoundedRectangle(cornerRadius: Theme.Radius.control, style: .continuous).fill(Theme.Fill.well))
                VStack(alignment: .leading, spacing: Theme.Space.xxs) {
                    Text(title).font(Theme.Font.body).foregroundStyle(Color.primary).lineLimit(1)
                    if let said {
                        SearchSaidLine(hit: said)
                    } else if !preview.isEmpty {
                        Text(preview).font(Theme.Font.secondary).foregroundStyle(.secondary).lineLimit(1)
                    }
                }
                Spacer(minLength: 0)
            }
            .contentShape(.rect)
        }
        .buttonStyle(.plain)
        .accessibilityElement(children: .combine)
        .accessibilityIdentifier("full-search-row")
    }
}
#endif
