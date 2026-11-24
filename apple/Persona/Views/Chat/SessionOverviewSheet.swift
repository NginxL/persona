import SwiftUI

/// Sections of the unified Session Overview (#27, #14322), strictly ordered:
/// Environment → Context → Subagents → Background tasks.
nonisolated enum SessionOverviewSection: String, Hashable, Sendable {
    case environment
    case context
    case subagents
    case background
}

/// Presentation state for one chat's unified Session Overview and Subagent Detail navigation.
/// Kept as a single reference on `ChatView` with a trivial init (P3, `MainPagesStackTests`).
@MainActor @Observable final class SessionOverviewUI {
    var showOverview = false
    var focusSection: SessionOverviewSection?
    /// Full-screen pushed subagent detail (iPhone) or inspector-expanded detail (iPad).
    var detailTarget: SubagentOverview.Target?
    /// Restores the Session Overview sheet on iPhone after popping back from `SubagentDetailPage`.
    var reopenOverviewOnReturn = false
    /// Preserves the collapsed/expanded state of the Ended subagents group across detail round-trips.
    var endedExpanded = false

    /// Callbacks hosted on this reference so `SessionOverviewPresentation` carries neither a giant `Session?` value
    /// nor `ChatView`-capturing closures on `ChatView.body`'s stack frame (`MainPagesStackTests`).
    @ObservationIgnored var onOpenOutput: ((BackgroundTask) -> Void)?
    @ObservationIgnored var onShowInChat: ((SubagentOverview.Target) -> Void)?
    @ObservationIgnored var canShowInChat: ((SubagentOverview.Target) -> Bool)?

    func setHandlers(
        openOutput: @escaping (BackgroundTask) -> Void,
        showInChat: @escaping (SubagentOverview.Target) -> Void,
        canShowInChat: @escaping (SubagentOverview.Target) -> Bool
    ) {
        onOpenOutput = openOutput
        onShowInChat = showInChat
        self.canShowInChat = canShowInChat
    }

    func openOverview(focus: SessionOverviewSection? = nil) {
        focusSection = focus
        showOverview = true
    }

    func openDetail(_ target: SubagentOverview.Target, fromOverview: Bool) {
        if fromOverview {
            reopenOverviewOnReturn = true
            showOverview = false
        }
        detailTarget = target
    }

    func detailDismissed() {
        detailTarget = nil
        if reopenOverviewOnReturn {
            reopenOverviewOnReturn = false
            focusSection = .subagents
            showOverview = true
        }
    }
}

/// Unified Session Overview (#27, #14322): replaces the split Session Info, Subagent Tree, and Background Activity
/// sheets with one view strictly ordered **Environment → Context → Subagents → Background tasks**.
/// Presented as a `.medium`/`.large` sheet on iPhone and as a trailing `.inspector` on iPad.
struct SessionOverviewSheet: View {
    @Environment(AppModel.self) private var model
    @Environment(\.dismiss) private var dismiss
    let key: SessionKey
    var session: Session?
    /// Opened from the Persona tab, where the conversation may not be in the fleet's list yet (`session` is nil then).
    var assistant = false
    var inInspector = false
    var focusSection: SessionOverviewSection?
    @Binding var endedExpanded: Bool
    var onSelectSubagent: ((SubagentOverview.Target) -> Void)?
    var onOpenOutput: ((BackgroundTask) -> Void)?
    var onOpenBackgroundActivity: (() -> Void)?
    var onDismiss: (() -> Void)?

    @State private var info: APIClient.SessionInfo?
    @State private var error: String?
    @State private var showTodo = false
    @State private var showLocalBackgroundActivity = false
    @State private var localDetailTarget: SubagentOverview.Target?
    @State private var loadingChildren = false
    @State private var subagentsSnapshot = SubagentOverview()
    @State private var grants = AlwaysGrantsModel()
    @State private var delegatedHistory = DelegatedHistoryModel()

    private var liveSession: Session? { model.session(key) ?? session }
    private var isAssistant: Bool { assistant || liveSession?.assistant == true }

    /// Equatable change token so `SubagentOverview.build` runs only when subagent inputs or `ChatStore.tasks`
    /// (embedded completion/failure statuses) change (P4), never per body.
    private var subagentInputStamp: SubagentOverview.InputStamp {
        SubagentOverview.inputStamp(
            session: liveSession,
            children: model.childRows(key),
            pending: model.interactions(for: key),
            tasks: model.chat(key).tasks
        )
    }

    var body: some View {
        ScrollViewReader { proxy in
            List {
                // 1. Environment (header, runtime, location, grants/history, copy actions)
                environmentSections

                // 2. Context (tokens, remaining bar, compactions, turns, output, started)
                contextSection

                // 3. Subagents (embedded + same-machine folded + cross-machine A2A / dispatch)
                subagentsSection

                // 4. Background tasks (/loop, running background commands, assistant silent activity)
                backgroundSection
            }
            .listStyle(.insetGrouped)
            .listSectionSpacing(.compact)
            .navigationTitle(String(localized: "Session overview [Session Overview]"))
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                SheetClose { closeOverview() }
            }
            .onAppear { scrollIfFocused(proxy) }
            .onChange(of: focusSection) { _, _ in scrollIfFocused(proxy) }
            .onChange(of: subagentsSnapshot.totalCount) { _, _ in
                if focusSection == .subagents { scrollIfFocused(proxy) }
            }
        }
        .task(id: key) {
            rebuildSubagents()
            async let infoLoad: Void = loadInfo()
            async let childrenLoad: Void = loadChildren()
            _ = await (infoLoad, childrenLoad)
        }
        .onChange(of: subagentInputStamp) { _, _ in
            rebuildSubagents()
        }
        .task(id: isAssistant ? key : nil) {
            if isAssistant { await grants.load(conversation: key.sessionID, api: model.api(key.group)) }
        }
        .task(id: isAssistant ? nil : key) {
            if !isAssistant { await delegatedHistory.load(key, manager: liveSession?.manager, api: model.api(key.group)) }
        }
        .sheet(isPresented: $showLocalBackgroundActivity) {
            BackgroundActivitySheet(key: key)
        }
        .navigationDestination(item: $localDetailTarget) { target in
            SubagentDetailPage(
                parent: key,
                target: target,
                inInspector: inInspector,
                onBack: { localDetailTarget = nil },
                onExpand: nil,
                onShowInChat: nil
            )
        }
    }

    private func closeOverview() {
        if let onDismiss { onDismiss() } else { dismiss() }
    }

    private func scrollIfFocused(_ proxy: ScrollViewProxy) {
        guard let focusSection else { return }
        Task { @MainActor in
            withAnimation(Theme.Motion.standard) {
                proxy.scrollTo(focusSection, anchor: .top)
            }
        }
    }

    private func loadInfo() async {
        do { info = try await model.api(key.group)?.sessionInfo(key) } catch { self.error = error.shown }
    }

    private func loadChildren() async {
        loadingChildren = true
        await model.refreshOverviewChildren(key)
        loadingChildren = false
        rebuildSubagents()
    }

    private func rebuildSubagents() {
        let s = liveSession
        let tasks = model.chat(key).tasks
        let works = s?.state == .working || s?.state == .needsYou
        let embedded = (s?.subagents ?? []).map { $0.shown(in: tasks, sessionWorks: works) }
        let children = model.childRows(key)
        let pending = model.interactions(for: key)
        let built = SubagentOverview.build(
            parent: s,
            parentKey: key,
            embedded: embedded,
            foldedChildren: children,
            pendingInteractions: pending,
            machineName: { ref in model.namedMachine(ref)?.title },
            agentStatuses: tasks.agentStatuses
        )
        if subagentsSnapshot != built {
            subagentsSnapshot = built
        }
    }

    // MARK: 1. Environment

    @ViewBuilder private var environmentSections: some View {
        Section {
            header
        }
        .id(SessionOverviewSection.environment)
        .listRowBackground(Color.clear)
        .listRowInsets(EdgeInsets(top: Theme.Space.flush, leading: Theme.Space.xs, bottom: Theme.Space.xs, trailing: Theme.Space.xs))

        if let link = liveSession?.externalLink {
            Section { FeishuLink(url: link) }
        }

        if let info {
            Section(String(localized: "Environment [Session Overview]")) {
                row(Theme.Icon.model, String(localized: "Model [Assistant View c98e11]"), Fmt.model(info.model) ?? info.model)
                if let l = info.launcher ?? liveSession?.launcher, !l.isEmpty {
                    row(Theme.Icon.tryOut, String(localized: "Launcher"), l)
                }
                if let r = AssistantRuntime.status(runtime: liveSession?.runtime, fallback: liveSession?.runtimeFallback) {
                    row(Theme.Icon.terminal, String(localized: "How It Runs"), r)
                }
                row(Theme.Icon.agents, agent, info.version)
                if let route = liveSession?.codexRoute {
                    routeRow(route, note: liveSession?.codexRouteNote)
                }
                row(Theme.Icon.permissions, String(localized: "Permissions"), SessionModes.infoLabel(info.permissionMode, session: liveSession))
                row(Theme.Icon.effort, String(localized: "Thinking Effort"), SessionModes.effortLabel(info.effort))
                if let s = liveSession, Ultracode.infoShown(s, features: model.features(key.group)) {
                    row(Theme.Icon.workflow, Ultracode.name, Ultracode.stateWord(s.ultracode))
                        .accessibilityIdentifier("session-ultracode")
                }
                if let s = liveSession, let todo = s.todo,
                   TodosLogic.isOnTodosHub(sessionGroup: s.key.group, hubGroup: model.hub?.group) {
                    Button { showTodo = true } label: {
                        row(Theme.Icon.todo, String(localized: "To-dos"), todo.title)
                    }
                    .sheet(isPresented: $showTodo) {
                        NavigationStack { TodoDetailView(todoID: todo.id).sheetDone { showTodo = false } }
                            #if os(iOS)
                            .presentationSizing(.page)
                            #endif
                    }
                }
                row(Theme.Icon.folder, String(localized: "Folder [Task Views 52daa7]"),
                    Fmt.path(info.cwd ?? liveSession?.cwd ?? "", home: model.namedMachine(key.machineRef)?.home), mono: true)
                if let b = info.gitBranch, !b.isEmpty {
                    row(Theme.Icon.gitBranch, String(localized: "Branch"), b, mono: true)
                }
            }
            if isAssistant {
                AlwaysGrantsSection(page: grants, conversation: key.sessionID, api: model.api(key.group))
            } else {
                DelegatedHistorySection(page: delegatedHistory, key: key, manager: liveSession?.manager, api: model.api(key.group))
            }
            Section {
                copyButton(String(localized: "Copy Session ID [Chat View e63671]"), symbol: Theme.Icon.id,
                           text: info.sessionID, toast: String(localized: "Session ID copied"))
                if liveSession?.canTerminal == true {
                    copyButton(String(localized: "Copy Resume Command"), symbol: Theme.Icon.terminal,
                               text: Session.resumeLine(cwd: info.cwd ?? liveSession?.cwd,
                                                        command: liveSession?.resumeCommand ?? "claude --resume",
                                                        id: liveSession?.sourceThreadID ?? info.sessionID),
                               toast: String(localized: "Resume command copied"))
                }
            } footer: {
                Text(info.sessionID).font(Theme.Font.label.monospaced()).textSelection(.enabled)
            }
        } else if let error {
            Section(String(localized: "Environment [Session Overview]")) {
                Label(error, systemImage: Theme.Icon.error)
                    .font(Theme.Font.caption)
                    .foregroundStyle(Theme.danger)
            }
        } else {
            Section(String(localized: "Environment [Session Overview]")) {
                ProgressView()
                    .frame(maxWidth: .infinity)
                    .padding(.vertical, Theme.Space.xl)
            }
            .listRowBackground(Color.clear)
        }
    }

    // MARK: 2. Context

    @ViewBuilder private var contextSection: some View {
        if let info {
            let counted = info.usageUnknown != true
            let usage = ContextUsage(liveSession)
            let started = info.startedAt.flatMap { $0.isMeaningful ? $0 : nil }
            if counted || usage != nil || started != nil {
                Section(String(localized: "Context [Session Overview]")) {
                    if counted, info.contextTokens > 0 {
                        row(Theme.Icon.context, String(localized: "Context"), SessionInfoSheet.tokens(info.contextTokens))
                    }
                    if let usage {
                        ContextUsageRow(usage: usage)
                    }
                    if counted, info.compactions > 0 {
                        row(Theme.Icon.collapseAll, String(localized: "Compactions"), String(localized: "\(info.compactions) times"))
                    }
                    if counted {
                        row(Theme.Icon.conversation, String(localized: "Chat"), String(localized: "\(info.turns) turns"))
                        row(Theme.Icon.text, String(localized: "Output"), SessionInfoSheet.tokens(info.outputTokens))
                    }
                    if let started {
                        row(Theme.Icon.time, String(localized: "Started"), started.formatted(date: .abbreviated, time: .shortened))
                    }
                }
                .id(SessionOverviewSection.context)
            }
        }
    }

    // MARK: 3. Subagents

    @ViewBuilder private var subagentsSection: some View {
        if !subagentsSnapshot.isEmpty || loadingChildren {
            Section {
                if loadingChildren && subagentsSnapshot.isEmpty {
                    HStack(spacing: Theme.Space.s) {
                        ProgressView().controlSize(.small)
                        Text("Loading subagents… [Session Overview]")
                            .font(Theme.Font.caption)
                            .foregroundStyle(.secondary)
                            .lineLimit(1)
                    }
                }

                // Running · N (Needs you pinned first, then newest start; hidden completely when 0)
                if !subagentsSnapshot.running.isEmpty {
                    Text("Running · \(subagentsSnapshot.running.count) [Session Overview]")
                        .font(Theme.Font.sectionHeader)
                        .foregroundStyle(.secondary)
                        .lineLimit(1)
                        .listRowSeparator(.hidden)
                        .accessibilityIdentifier("overview.subagents.running-header")
                    ForEach(subagentsSnapshot.running) { item in
                        SubagentOverviewRow(item: item) { openItem(item) }
                    }
                }

                // Ended · N (collapsed by default with up to 4 stacked harness marks + "N ended" + chevron)
                if !subagentsSnapshot.ended.isEmpty {
                    endedToggleRow
                    if endedExpanded {
                        ForEach(subagentsSnapshot.ended) { item in
                            SubagentOverviewRow(item: item) { openItem(item) }
                        }
                    }
                }
            } header: {
                subagentsSectionHeader
            }
            .id(SessionOverviewSection.subagents)
        }
    }

    private var subagentsSectionHeader: some View {
        HStack(spacing: Theme.Space.xs) {
            ThemedIcon(Theme.Icon.subagent)
                .font(Theme.Font.captionStrong)
                .foregroundStyle(.secondary)
            Text("Subagents [Session Overview]")
                .font(Theme.Font.sectionHeader)
                .lineLimit(1)
            Spacer(minLength: Theme.Space.xs)
            if let summary = subagentCountSummary {
                Text(summary)
                    .font(Theme.Font.label.monospacedDigit())
                    .foregroundStyle(.secondary)
                    .lineLimit(1)
            }
        }
        .accessibilityElement(children: .combine)
        .accessibilityIdentifier("overview.subagents.header")
    }

    private var subagentCountSummary: String? {
        var parts: [String] = []
        if !subagentsSnapshot.running.isEmpty {
            parts.append(String(localized: "\(subagentsSnapshot.running.count) running [Child Rollup]"))
        }
        if !subagentsSnapshot.ended.isEmpty {
            parts.append(String(localized: "\(subagentsSnapshot.ended.count) ended [Session Overview]"))
        }
        return parts.isEmpty ? nil : parts.joined(separator: " · ")
    }

    private var endedToggleRow: some View {
        Button {
            Haptics.tap()
            withAnimation(Theme.Motion.standard) {
                endedExpanded.toggle()
            }
        } label: {
            HStack(spacing: Theme.Space.s) {
                if !endedExpanded {
                    StackedBrandMarks(harnesses: subagentsSnapshot.endedPreviewHarnesses)
                    Text("\(subagentsSnapshot.ended.count) ended [Session Overview]")
                        .font(Theme.Font.secondaryMedium)
                        .foregroundStyle(.secondary)
                        .lineLimit(1)
                } else {
                    Text("Ended · \(subagentsSnapshot.ended.count) [Session Overview]")
                        .font(Theme.Font.sectionHeader)
                        .foregroundStyle(.secondary)
                        .lineLimit(1)
                }
                Spacer(minLength: Theme.Space.xs)
                ThemedIcon(endedExpanded ? Theme.Icon.collapse : Theme.Icon.expand)
                    .font(Theme.Font.labelStrong)
                    .foregroundStyle(.tertiary)
            }
            .contentShape(.rect)
        }
        .buttonStyle(.plain)
        .accessibilityLabel(String(localized: "Ended · \(subagentsSnapshot.ended.count) [Session Overview]"))
        .accessibilityHint(endedExpanded ? String(localized: "Collapse") : String(localized: "Expand"))
        .accessibilityIdentifier("overview.subagents.ended-toggle")
    }

    private func openItem(_ item: SubagentOverview.Item) {
        Haptics.tap()
        if let onSelectSubagent {
            onSelectSubagent(item.target)
        } else {
            localDetailTarget = item.target
        }
    }

    // MARK: 4. Background tasks

    @ViewBuilder private var backgroundSection: some View {
        let loop = liveSession?.loop
        let background = liveSession?.background ?? []
        let showsSilentActivity = isAssistant && key == model.assistantMain
        if loop != nil || !background.isEmpty || showsSilentActivity {
            Section {
                if let loop {
                    LoopStrip(loop: loop, stop: { model.stopLoop(key) }, expanded: true)
                        .listRowInsets(EdgeInsets(top: Theme.Space.xs, leading: Theme.Space.flush, bottom: Theme.Space.xs, trailing: Theme.Space.flush))
                        .listRowBackground(Color.clear)
                }
                if !background.isEmpty {
                    BackgroundTasksStrip(tasks: background, openOutput: { task in
                        onOpenOutput?(task)
                    }, expanded: true)
                        .listRowInsets(EdgeInsets(top: Theme.Space.xs, leading: Theme.Space.flush, bottom: Theme.Space.xs, trailing: Theme.Space.flush))
                        .listRowBackground(Color.clear)
                }
                if showsSilentActivity {
                    Button {
                        Haptics.tap()
                        if let onOpenBackgroundActivity {
                            onOpenBackgroundActivity()
                        } else {
                            showLocalBackgroundActivity = true
                        }
                    } label: {
                        HStack(spacing: Theme.Space.s) {
                            ThemedIcon(Theme.Icon.backgroundTasks)
                                .font(Theme.Font.secondary)
                                .foregroundStyle(.secondary)
                            Text("Background Activity [Session Overview]")
                                .font(Theme.Font.secondary)
                                .foregroundStyle(Color.primary)
                                .lineLimit(1)
                            Spacer(minLength: Theme.Space.xs)
                            ThemedIcon(Theme.Icon.disclosure)
                                .font(Theme.Font.labelStrong)
                                .foregroundStyle(.tertiary)
                        }
                    }
                    .buttonStyle(.plain)
                }
            } header: {
                HStack(spacing: Theme.Space.xs) {
                    ThemedIcon(Theme.Icon.backgroundTask)
                        .font(Theme.Font.captionStrong)
                        .foregroundStyle(.secondary)
                    Text("Background tasks [Session Overview]")
                        .font(Theme.Font.sectionHeader)
                        .lineLimit(1)
                }
            }
            .id(SessionOverviewSection.background)
        }
    }

    // MARK: Shared row helpers

    private var header: some View {
        let s = liveSession
        let place = [model.namedMachine(key.machineRef)?.title ?? key.machineID, s?.spaceName, s?.slotLabel]
            .compactMap { $0 }.filter { !$0.isEmpty }.joined(separator: " › ")
        return VStack(alignment: .leading, spacing: Theme.Space.sm) {
            Text(s?.displayTitle ?? String(localized: "Session"))
                .font(Theme.Font.title)
                .lineLimit(2)
            HStack(spacing: Theme.Space.sm) {
                if let st = s?.state {
                    HStack(spacing: Theme.Space.xs) {
                        StatusDot(state: st, size: Theme.Size.dotS)
                        Text(st.label)
                            .font(Theme.Font.labelStrong)
                            .foregroundStyle(ChatView.quiet(st) ? Color.secondary : st.tone.text)
                            .lineLimit(1)
                    }
                    .padding(.horizontal, Theme.Space.s)
                    .padding(.vertical, Theme.Space.xs)
                    .background(Theme.Fill.chip, in: .capsule)
                }
                Text(place)
                    .font(Theme.Font.label)
                    .foregroundStyle(.secondary)
                    .lineLimit(1)
                    .truncationMode(.middle)
            }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
    }

    private func row(_ symbol: String, _ label: String, _ value: String?, mono: Bool = false) -> some View {
        LabeledContent {
            Text(value.flatMap { $0.isEmpty ? nil : $0 } ?? "—")
                .font(mono ? Theme.Font.code : Theme.Font.secondary)
                .lineLimit(2)
                .truncationMode(.middle)
                .multilineTextAlignment(.trailing)
                .textSelection(.enabled)
        } label: {
            Label(label, systemImage: symbol).lineLimit(1)
        }
    }

    private func routeRow(_ route: String, note: String?) -> some View {
        VStack(alignment: .leading, spacing: Theme.Space.xs) {
            row(Theme.Icon.link, String(localized: "Connect [V N C Page a55741]"),
                route == "app_server" ? String(localized: "app-server (structured)") : String(localized: "Terminal (screen reading)"))
            if route != "app_server", let note, !note.isEmpty {
                Text(note)
                    .font(Theme.Font.caption)
                    .foregroundStyle(.secondary)
                    .fixedSize(horizontal: false, vertical: true)
            }
        }
        .accessibilityIdentifier("session-codex-route")
    }

    private func copyButton(_ title: String, symbol: String, text: String, toast: String) -> some View {
        Button {
            UIPasteboard.general.string = text
            Haptics.success()
            model.toast = Toast(toast, style: .success)
        } label: {
            Label(title, systemImage: symbol).lineLimit(1)
        }
    }

    private var agent: String { liveSession.map { $0.isClaude ? "Claude Code" : $0.agentLabel } ?? "Claude Code" }
}

// MARK: - Subagent Row & Stacked Brand Marks

/// One subagent row in Session Overview (#27):
/// `[20pt harness brand mark] Title (1-line) [origin chip] … Status ›`
/// Second line ONLY for running items: latest tool summary (1-line truncated).
private struct SubagentOverviewRow: View {
    let item: SubagentOverview.Item
    var onSelect: () -> Void

    var body: some View {
        Button(action: onSelect) {
            HStack(alignment: .center, spacing: Theme.Space.s) {
                if item.depth > 0 {
                    ThemedIcon(Theme.Icon.followOn)
                        .font(Theme.Font.label)
                        .foregroundStyle(.tertiary)
                }
                AgentMark(kind: AgentKind(item.harness), side: Theme.Size.iconXS)
                VStack(alignment: .leading, spacing: Theme.Space.xxs) {
                    HStack(spacing: Theme.Space.xs) {
                        Text(item.title)
                            .font(Theme.Font.secondaryMedium)
                            .foregroundStyle(Color.primary)
                            .lineLimit(1)
                            .truncationMode(.tail)
                        if let origin = item.originChip {
                            Chip(text: origin, look: .neutral, compact: true)
                        }
                        Spacer(minLength: Theme.Space.xs)
                        statusView
                        ThemedIcon(Theme.Icon.disclosure)
                            .font(Theme.Font.labelStrong)
                            .foregroundStyle(.tertiary)
                    }
                    if item.status.isRunningGroup, let summary = item.toolSummary, !summary.isEmpty {
                        Text(summary)
                            .font(Theme.Font.label)
                            .foregroundStyle(.secondary)
                            .lineLimit(1)
                            .truncationMode(.tail)
                    }
                }
            }
            .padding(.leading, CGFloat(min(item.depth, SubagentOverview.maxNestingDepth)) * Theme.Space.l)
            .contentShape(.rect)
        }
        .buttonStyle(.plain)
        .accessibilityElement(children: .combine)
        .accessibilityIdentifier("overview.subagents.row")
    }

    @ViewBuilder private var statusView: some View {
        switch item.status {
        case .needsYou:
            HStack(spacing: Theme.Space.xs) {
                StatusDot(state: .needsYou, size: Theme.Size.dotXS)
                    .accessibilityHidden(true)
                Text("Needs you [Session Overview]")
                    .font(Theme.Font.labelStrong)
                    .foregroundStyle(StatusPalette.needsYou.text)
                    .lineLimit(1)
            }
            .fixedSize()
        case .running:
            HStack(spacing: Theme.Space.xs) {
                StatusDot(state: .working, size: Theme.Size.dotXS)
                    .accessibilityHidden(true)
                if let start = item.startedAt {
                    VisibleTimelineView(interval: 30) { now in
                        Text(BackgroundTasksStrip.elapsed(from: start, to: now))
                            .font(Theme.Font.label.monospacedDigit())
                            .foregroundStyle(.secondary)
                            .lineLimit(1)
                    }
                } else {
                    Text("Running [Child Session]")
                        .font(Theme.Font.label)
                        .foregroundStyle(.secondary)
                        .lineLimit(1)
                }
            }
            .fixedSize()
        case .failed:
            Text("Failed [Session Overview]")
                .font(Theme.Font.labelStrong)
                .foregroundStyle(Theme.danger)
                .lineLimit(1)
                .fixedSize()
        case .ended:
            if let time = item.updatedAt ?? item.startedAt {
                Text(Fmt.relative(time, now: .now))
                    .font(Theme.Font.label.monospacedDigit())
                    .foregroundStyle(.secondary)
                    .lineLimit(1)
                    .fixedSize()
            }
        }
    }
}

/// Up to 4 stacked 20pt harness brand marks for the collapsed Ended subagents row (#27).
private struct StackedBrandMarks: View {
    let harnesses: [SubagentOverview.HarnessInfo]

    var body: some View {
        HStack(spacing: -Theme.Space.sm) {
            ForEach(Array(harnesses.prefix(SubagentOverview.maxEndedPreviewMarks).enumerated()), id: \.offset) { _, harness in
                AgentMark(kind: AgentKind(harness), side: Theme.Size.iconXS)
                    .padding(Theme.Size.rule)
                    .background(Theme.groupedCard, in: Circle())
            }
        }
        .accessibilityHidden(true)
    }
}

// MARK: - Full Subagent Detail Page

/// Full subagent conversation detail opened from Session Overview or a TaskCard (#27, #14322):
/// reuses `ChatView` for both cross-machine / same-machine `SessionKey` rows (interactive when `canSend`, else
/// read-only) and embedded `parent@agent` subagents (read-only), displaying cached transcript + offline notice if
/// the computer is offline.
struct SubagentDetailPage: View {
    @Environment(AppModel.self) private var model
    let parent: SessionKey
    let target: SubagentOverview.Target
    var inInspector = false
    var onBack: () -> Void
    var onExpand: (() -> Void)?
    var onShowInChat: ((SubagentOverview.Target) -> Void)?

    private var childKey: SessionKey { target.sessionKey }
    private var childSession: Session? {
        switch target {
        case .session(let key): model.session(key) ?? model.childIndex.row(key)
        case .embedded: nil
        }
    }
    private var isComputerOffline: Bool {
        model.machine(childKey.machineRef)?.online == false
    }
    private var displayTitle: String {
        switch target {
        case .embedded(_, let agent):
            return agent.title
        case .session(let key):
            let title = childSession?.displayTitle.trimmingCharacters(in: .whitespacesAndNewlines) ?? ""
            return title.isEmpty ? key.sessionID : title
        }
    }
    private var hostMenu: AnyView? {
        let canStop = childSession?.status.isActive == true && childSession?.canInterrupt == true
        let hasJump = onShowInChat != nil
        let hasExpand = inInspector && onExpand != nil
        guard canStop || hasJump || hasExpand else { return nil }
        return AnyView(Group {
            if canStop, case .session(let key) = target {
                Button {
                    Task { await model.interrupt(key) }
                } label: {
                    Label("Interrupt [Composer Buttons f9d193]", systemImage: Theme.Icon.interrupt).lineLimit(1)
                }
            }
            if let onShowInChat {
                Button {
                    onShowInChat(target)
                } label: {
                    Label("Show in chat [Session Overview]", systemImage: Theme.Icon.conversation).lineLimit(1)
                }
            }
            if inInspector, let onExpand {
                Button {
                    onExpand()
                } label: {
                    Label("Open as page [Session Overview]", systemImage: Theme.Icon.enlarge).lineLimit(1)
                }
            }
        })
    }

    var body: some View {
        let host = HostedChat(
            title: displayTitle,
            note: String(localized: "Session overview [Session Overview]"),
            closes: false,
            leave: onBack,
            agents: [],
            select: { _ in },
            menu: hostMenu,
            openInAgents: { _ in onBack() },
            expand: inInspector ? onExpand.map { expand in { _ in expand() } } : nil
        )
        VStack(spacing: Theme.Space.flush) {
            if isComputerOffline {
                offlineNotice
            }
            switch target {
            case .session(let key):
                ChatView(key: key, style: .dev, sidePage: true)
                    .id(key)
                    .environment(\.hostedChat, host)
            case .embedded(let parentKey, let agent):
                ChatView(key: parentKey, style: .dev, sidePage: true, initialSubagent: agent)
                    .id(target)
                    .environment(\.hostedChat, host)
            }
        }
        .toolbar(.hidden, for: .navigationBar)
        .accessibilityIdentifier("subagent.detail-page")
    }

    private var offlineNotice: some View {
        HStack(spacing: Theme.Space.s) {
            ThemedIcon(Theme.Icon.offline)
                .font(Theme.Font.captionStrong)
                .foregroundStyle(Theme.cautionText)
            Text("Computer offline · Showing cached transcript [Session Overview]")
                .font(Theme.Font.caption)
                .foregroundStyle(.secondary)
                .lineLimit(1)
                .truncationMode(.tail)
            Spacer(minLength: Theme.Space.xs)
        }
        .padding(.horizontal, Theme.Space.l)
        .padding(.vertical, Theme.Space.s)
        .background(Theme.Fill.well)
        .accessibilityIdentifier("subagent.detail.offline-notice")
    }
}

// MARK: - Unified Overview Presentation (iPhone Sheet + Push / iPad Inspector + Internal Push + Expand)

/// Attaches the unified Session Overview and Subagent Detail navigation to `ChatView` behind lightweight,
/// type-erased presentation boundaries (matching `SideSessionsPresentation` so `MainPagesStackTests` stays well
/// under the 512 KB Debug first-render stack budget). Neither a `Session?` value nor `ChatView`-capturing closures
/// live on this modifier: `SessionOverviewSheet` reads `model.session(parent)` when presented and callbacks stay on
/// `ui`.
///
/// iOS's `.inspector` modifier wraps the page in an internal inspector host that adds tens of KB to `ChatView`'s
/// Debug first-render stack even when dismissed, so it is attached only on iPad (`userInterfaceIdiom == .pad`,
/// invariant for the process lifetime — never changing view identity), while iPhone uses the `.medium`/`.large`
/// sheet + pushed `SubagentDetailPage`.
struct SessionOverviewPresentation: ViewModifier {
    let parent: SessionKey
    var assistant: Bool
    var on: Bool
    let ui: SessionOverviewUI

    @Environment(\.horizontalSizeClass) private var widthClass
    @State private var inspectorPath: [SubagentOverview.Target] = []
    /// Held while the iPhone sheet finishes dismissing, then pushed onto the chat's NavigationStack (matching
    /// `SideSessionsPresentation` so SwiftUI never drops a push started mid-dismissal).
    @State private var pendingDetail: SubagentOverview.Target?

    private var usesInspector: Bool {
        #if os(iOS)
        widthClass == .regular
        #else
        false
        #endif
    }

    func body(content: Content) -> some View {
        content
            .inspector(isPresented: Binding(get: { on && usesInspector && ui.showOverview },
                                            set: { ui.showOverview = $0 })) {
                SessionOverviewInspectorContent(
                    parent: parent,
                    assistant: assistant,
                    ui: ui,
                    inspectorPath: $inspectorPath
                )
            }
            .stackSegment()
            .sheet(isPresented: Binding(get: { on && !usesInspector && ui.showOverview },
                                        set: { ui.showOverview = $0 }),
                   onDismiss: {
                if let target = pendingDetail {
                    pendingDetail = nil
                    ui.detailTarget = target
                }
            }) {
                SessionOverviewCompactContent(
                    parent: parent,
                    assistant: assistant,
                    ui: ui,
                    onPickSubagent: { target in
                        pendingDetail = target
                        ui.reopenOverviewOnReturn = true
                        ui.showOverview = false
                    }
                )
            }
            .stackSegment()
            .navigationDestination(item: Binding(get: { on ? ui.detailTarget : nil },
                                                 set: { next in
                if next == nil { ui.detailDismissed() } else { ui.detailTarget = next }
            })) { target in
                SessionOverviewFullDetailContent(parent: parent, target: target, ui: ui)
            }
            .onChange(of: parent) { _, _ in
                inspectorPath.removeAll()
                pendingDetail = nil
                ui.showOverview = false
                ui.detailTarget = nil
                ui.reopenOverviewOnReturn = false
            }
    }
}

private struct SessionOverviewCompactContent: View {
    let parent: SessionKey
    var assistant: Bool
    let ui: SessionOverviewUI
    var onPickSubagent: (SubagentOverview.Target) -> Void

    var body: some View {
        let endedBinding = Binding(get: { ui.endedExpanded }, set: { ui.endedExpanded = $0 })
        NavigationStack {
            SessionOverviewSheet(
                key: parent,
                session: nil,
                assistant: assistant,
                inInspector: false,
                focusSection: ui.focusSection,
                endedExpanded: endedBinding,
                onSelectSubagent: onPickSubagent,
                onOpenOutput: { task in
                    ui.showOverview = false
                    ui.onOpenOutput?(task)
                },
                onDismiss: { ui.showOverview = false }
            )
        }
        .presentationDetents([.medium, .large])
        .presentationDragIndicator(.visible)
    }
}

private struct SessionOverviewInspectorContent: View {
    let parent: SessionKey
    var assistant: Bool
    let ui: SessionOverviewUI
    @Binding var inspectorPath: [SubagentOverview.Target]

    var body: some View {
        let endedBinding = Binding(get: { ui.endedExpanded }, set: { ui.endedExpanded = $0 })
        NavigationStack(path: $inspectorPath) {
            SessionOverviewSheet(
                key: parent,
                session: nil,
                assistant: assistant,
                inInspector: true,
                focusSection: ui.focusSection,
                endedExpanded: endedBinding,
                onSelectSubagent: { target in
                    inspectorPath.append(target)
                },
                onOpenOutput: { task in
                    ui.onOpenOutput?(task)
                },
                onDismiss: {
                    inspectorPath.removeAll()
                    ui.showOverview = false
                }
            )
            .navigationDestination(for: SubagentOverview.Target.self) { target in
                let showInChat: ((SubagentOverview.Target) -> Void)? = (ui.canShowInChat?(target) == true)
                    ? { target in ui.onShowInChat?(target) }
                    : nil
                SubagentDetailPage(
                    parent: parent,
                    target: target,
                    inInspector: true,
                    onBack: {
                        if !inspectorPath.isEmpty { inspectorPath.removeLast() }
                    },
                    onExpand: {
                        inspectorPath.removeAll()
                        ui.showOverview = false
                        ui.openDetail(target, fromOverview: false)
                    },
                    onShowInChat: showInChat
                )
            }
        }
    }
}

private struct SessionOverviewFullDetailContent: View {
    let parent: SessionKey
    let target: SubagentOverview.Target
    let ui: SessionOverviewUI

    var body: some View {
        let showInChat: ((SubagentOverview.Target) -> Void)? = (ui.canShowInChat?(target) == true)
            ? { target in
                ui.reopenOverviewOnReturn = false
                ui.detailTarget = nil
                ui.onShowInChat?(target)
            }
            : nil
        SubagentDetailPage(
            parent: parent,
            target: target,
            inInspector: false,
            onBack: { ui.detailDismissed() },
            onExpand: nil,
            onShowInChat: showInChat
        )
    }
}
