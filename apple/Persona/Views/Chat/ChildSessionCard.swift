import SwiftUI

/// Folded child rows for the open parent, keyed by full `SessionKey` so cross-machine children sharing a `sessionID`
/// never collide. The work step reads one, not the whole list.
struct ChildSessionRowsKey: EnvironmentKey {
    static let defaultValue: [SessionKey: Session] = [:]
}

/// Agent chats name what runs the child. Persona's own chats do not (web `childMeta`).
struct ChildSessionHarnessKey: EnvironmentKey {
    static let defaultValue = true
}

/// Opens a parent session focused on Session Overview's Subagents section without passing a changing closure into
/// the environment (P5, matching `SpaceOpener`).
@MainActor final class SubagentsOverviewOpener {
    private var open: (SessionKey) -> Void = { _ in }

    func set(_ open: @escaping (SessionKey) -> Void) -> SubagentsOverviewOpener {
        self.open = open
        return self
    }

    func callAsFunction(_ key: SessionKey) { open(key) }
}

/// Opens a folded child session's full detail page from an inline `ChildSessionCard` (P5, reference object).
@MainActor final class ChildSessionOpener {
    private var open: (SessionKey) -> Void = { _ in }

    func set(_ open: @escaping (SessionKey) -> Void) -> ChildSessionOpener {
        self.open = open
        return self
    }

    func callAsFunction(_ key: SessionKey) { open(key) }
}

extension EnvironmentValues {
    var childSessionRows: [SessionKey: Session] {
        get { self[ChildSessionRowsKey.self] }
        set { self[ChildSessionRowsKey.self] = newValue }
    }

    var showsChildHarness: Bool {
        get { self[ChildSessionHarnessKey.self] }
        set { self[ChildSessionHarnessKey.self] = newValue }
    }

    @Entry var openSubagentsOverview: SubagentsOverviewOpener? = nil
    @Entry var openChildSession: ChildSessionOpener? = nil
}

/// A folded child inside its parent's work summary. Opening it reads the child's transcript and offers View full
/// to inspect its complete conversation.
struct ChildSessionCard: View {
    var key: SessionKey
    @Environment(AppModel.self) private var model
    @Environment(\.childSessionRows) private var rows
    @Environment(\.showsChildHarness) private var showsHarness
    @Environment(\.openChildSession) private var openChildSession
    @Environment(\.dynamicTypeSize) private var typeSize
    @State private var expanded = false
    @State private var detail: ChildSessions.Detail?
    @State private var fullResult: String?

    private var session: Session? { rows[key] }

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            Button {
                Haptics.tap()
                expanded.toggle()
            } label: {
                header
            }
            .buttonStyle(.plain)
            if expanded, let session {
                details(session)
            }
        }
        .onAppear { revealIfAsked() }
        .onChange(of: model.revealChild) { _, _ in revealIfAsked() }
        .task(id: expanded ? session?.updatedAt : nil) {
            guard expanded, let session else { return }
            guard let api = model.api(session.key.group) else { return }
            detail = try? await ChildTranscript.load(api, session.key)
        }
    }

    private var header: some View {
        let status = session.map(ChildSessions.status) ?? .completed
        return HStack(alignment: .top, spacing: Theme.Space.m) {
            ZStack {
                Circle().fill(tint(status).opacity(0.14)).scaledFrame(width: Theme.Size.icon, height: Theme.Size.icon)
                ThemedIcon(Theme.Icon.subagent)
                    .font(Theme.Font.label)
                    .foregroundStyle(tint(status))
            }
            VStack(alignment: .leading, spacing: Theme.Space.xxs) {
                Text(title).font(Theme.Font.secondaryStrong).lineLimit(1).minimumScaleFactor(0.8)
                statusLabel(status)
            }
            Spacer(minLength: Theme.Space.xs)
            ThemedIcon(Theme.Icon.disclosure)
                .font(Theme.Font.label)
                .foregroundStyle(.tertiary)
                .rotationEffect(.degrees(expanded ? 90 : 0))
        }
        .padding(.vertical, Theme.Space.m)
        .padding(.leading, WorkCardSlice.iconLeading)
        .padding(.trailing, WorkCardSlice.inset)
        .contentShape(.rect)
        .accessibilityElement(children: .combine)
    }

    @ViewBuilder private func details(_ session: Session) -> some View {
        VStack(alignment: .leading, spacing: Theme.Space.s) {
            if let prompt = detail?.prompt, !prompt.isEmpty {
                Text(prompt).font(Theme.Font.secondary).foregroundStyle(.secondary).lineLimit(4)
            } else if let standIn = session.lastText, !standIn.isEmpty, detail == nil {
                Text(standIn).font(Theme.Font.secondary).foregroundStyle(.secondary).lineLimit(4)
            }
            if let result = fullResult ?? detail?.result, !result.isEmpty {
                Text(result).font(Theme.Font.secondary).lineLimit(fullResult == nil ? 4 : nil)
            }
            if fullResult == nil, let id = detail?.resultID, detail?.fullSize != nil {
                Button {
                    Task { await loadFull(session, id: id) }
                } label: {
                    Text("Show full result [Child Session]").font(Theme.Font.label).lineLimit(1).minimumScaleFactor(0.8)
                }
                .buttonStyle(.plain)
            }
            if let tool = detail?.lastTool {
                Text("Last tool: \(tool) [Child Session]").font(Theme.Font.label).foregroundStyle(.secondary).lineLimit(1)
            }
            if let open = openChildSession {
                Button {
                    Haptics.tap()
                    open(session.key)
                } label: {
                    Label("View full [Session Overview]", systemImage: Theme.Icon.open)
                        .font(Theme.Font.labelStrong)
                        .lineLimit(1)
                }
                .buttonStyle(.plain)
                .foregroundStyle(Theme.accent)
            }
        }
        .padding(.leading, WorkCardSlice.contentLeading(typeSize))
        .padding(.trailing, WorkCardSlice.inset)
        .padding(.bottom, Theme.Space.m)
    }

    private var title: String {
        let name = session?.displayTitle.trimmingCharacters(in: .whitespacesAndNewlines) ?? ""
        return name.isEmpty ? String(localized: "Subagent task [Child Session]") : name
    }

    @ViewBuilder private func statusLabel(_ status: ChildSessions.Status) -> some View {
        let line = { (now: Date) in
            Text(self.statusLine(status, now: now))
                .font(Theme.Font.label)
                .foregroundStyle(status == .failed ? Theme.danger : Color.secondary)
                .lineLimit(1).minimumScaleFactor(0.8)
        }
        if status == .running, session?.startedAt != nil {
            VisibleTimelineView(interval: 30) { line($0) }
        } else {
            line(.now)
        }
    }

    private func revealIfAsked() {
        if model.revealChild?.matches(key) == true { expanded = true }
    }

    private func statusLine(_ status: ChildSessions.Status, now: Date) -> String {
        var parts = [word(status)]
        // What runs it by name, as its mark names it (`AgentKind`, the web's `childMeta`): Claude Code, Codex, a Paseo
        // provider's label; never the id of the engine that runs it ("paseo", #8144).
        if showsHarness, let session {
            parts.append(AgentKind(session).name)
        }
        if let model = session?.model, !model.isEmpty { parts.append(model) }
        if let effort = session?.effort, Self.efforts.contains(effort) { parts.append(effort) }
        if status == .running, let start = session?.startedAt {
            let seconds = max(0, Int(now.timeIntervalSince(start)))
            parts.append(String(localized: "Running for \(TaskNoteText.took(seconds)) [Child Session]"))
        }
        return parts.joined(separator: " · ")
    }

    private func word(_ status: ChildSessions.Status) -> String {
        switch status {
        case .running: String(localized: "Running [Child Session]")
        case .completed: String(localized: "Completed [Child Session]")
        case .failed: String(localized: "Failed [Child Session]")
        case .stopped: String(localized: "Stopped [Child Session]")
        }
    }

    private func tint(_ status: ChildSessions.Status) -> Color {
        switch status {
        case .running: Theme.busy
        case .completed: Theme.positive
        case .failed: Theme.danger
        case .stopped: .secondary
        }
    }

    private func loadFull(_ session: Session, id: String) async {
        guard let api = model.api(session.key.group), let item = try? await api.fullItem(session.key, id: id) else { return }
        fullResult = item.text
    }

    private static let efforts: Set<String> = ["minimal", "low", "medium", "high", "xhigh", "max", "ultra"]
}

/// Single-line parent-row subagent rollup (#27):
/// - Waiting for user: `[agent icon] N need you` in attention color.
/// - Else running: `[working mark] N running` in secondary color.
/// - All ended: hidden (quiet).
/// Tapping opens the parent session and focuses Session Overview's Subagents section.
struct RunningChildrenCaption: View {
    var session: Session
    @Environment(\.openSubagentsOverview) private var openOverview

    var body: some View {
        let rollup = session.childRollup
        if let text = rollup.subtitle {
            Button {
                Haptics.tap()
                if let openOverview {
                    openOverview(session.key)
                } else {
                    #if os(iOS)
                    AgentMenuRouting.shared.open(.subagents, at: session.key)
                    #endif
                    AppModel.shared.open(session.key)
                }
            } label: {
                captionRow(rollup: rollup, text: text)
            }
            .buttonStyle(.plain)
            .accessibilityLabel(rollup.spoken ?? text)
            .accessibilityIdentifier("agents.row.child-rollup")
        }
    }

    @ViewBuilder private func captionRow(rollup: ChildSessions.Rollup, text: String) -> some View {
        switch rollup.kind {
        case .needsYou:
            HStack(spacing: Theme.Space.xs) {
                ThemedIcon(Theme.Icon.subagent)
                    .font(Theme.Font.label)
                    .foregroundStyle(StatusPalette.needsYou.text)
                Text(text)
                    .font(Theme.Font.captionMedium)
                    .foregroundStyle(StatusPalette.needsYou.text)
                    .lineLimit(1)
                    .truncationMode(.tail)
            }
        case .running:
            HStack(spacing: Theme.Space.xs) {
                StatusDot(state: .working, size: Theme.Size.dotXS)
                    .accessibilityHidden(true)
                Text(text)
                    .font(Theme.Font.caption)
                    .foregroundStyle(.secondary)
                    .lineLimit(1)
                    .truncationMode(.tail)
            }
        case .quiet:
            EmptyView()
        }
    }
}
