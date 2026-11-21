#if os(iOS)
import MarkdownUI
import SwiftUI

struct StarredMessagesPage: View {
    @State private var store = StarredStore.shared
    @State private var query = ""

    private var shown: [StarredSnapshot] {
        let terms = query.split(whereSeparator: \.isWhitespace).map(String.init)
        return terms.isEmpty ? store.snapshots : store.snapshots.filter { star in
            let fields = [star.text, star.title, star.displayedSpeaker]
            return terms.allSatisfy { term in fields.contains { $0.localizedStandardContains(term) } }
        }
    }

    var body: some View {
        let matches = shown
        List {
            ForEach(matches, id: \.id) { star in
                StarredMessageRow(star: star, store: store)
            }
        }
        .themedPage()
        .overlay { if matches.isEmpty { empty } }
        .pageTitle(String(localized: "Starred"))
        .pageSearchable(text: $query, prompt: "Search Starred Messages")
        .refreshable { await store.loadIndex(); await store.loadPage() }
        .onAppear { store.startReading() }
        .onDisappear { store.stopReading() }
        .accessibilityIdentifier("starred-messages-page")
    }

    @ViewBuilder private var empty: some View {
        switch store.state {
        case .idle, .loading: ProgressView()
        case .unsupported:
            ContentUnavailableView("Starred Messages Are Unavailable", systemImage: Theme.Icon.starredMessage,
                                   description: Text("Update the hub to use starred messages with this assistant."))
        case .failed:
            ContentUnavailableView {
                Label("Couldn't Load Starred Messages", systemImage: Theme.Icon.error)
            } actions: {
                Button("Try Again") { Task { await store.loadPage() } }.themedButton()
            }
        case .ready:
            if store.snapshots.isEmpty {
                ContentUnavailableView("No Starred Messages", systemImage: Theme.Icon.starredMessage,
                                       description: Text("Touch and hold a message to star it."))
            } else {
                ContentUnavailableView.search(text: query)
            }
        }
    }
}

private struct StarredMessageRow: View {
    @Environment(AppModel.self) private var model
    let star: StarredSnapshot
    let store: StarredStore
    @State private var expanded = false
    @State private var checking = false
    @State private var original: StarredStore.Original?

    private var hasLongText: Bool { star.text.count > 420 || star.text.split(separator: "\n").count > 6 }
    private var displayText: String {
        guard hasLongText, !expanded else { return star.text }
        return String(star.text.split(separator: "\n", omittingEmptySubsequences: false).prefix(6).joined(separator: "\n").prefix(420)) + "…"
    }

    var body: some View {
        VStack(alignment: .leading, spacing: Theme.Space.s) {
            Text(star.title.isEmpty ? String(localized: "Conversation") : star.title)
                .font(Theme.Font.bodyStrong)
                .accessibilityAddTraits(.isHeader)
            HStack(alignment: .firstTextBaseline, spacing: Theme.Space.s) {
                Text(star.displayedSpeaker)
                if let time = star.written_at ?? star.starred_at {
                    Text(time.formatted(date: .abbreviated, time: .shortened))
                }
            }
            .font(Theme.Font.caption)
            .foregroundStyle(.secondary)

            Markdown(MarkdownMemo.content(displayText))
                .markdownTheme(.persona)
                .accessibilityIdentifier("starred-snapshot-text")
            if hasLongText {
                Button(expanded ? String(localized: "Show Less") : String(localized: "Show More")) { expanded.toggle() }
                    .buttonStyle(.borderless)
            }
            if let omitted = star.omitted_chars, omitted > 0 {
                Text("\(omitted) additional characters")
                    .font(Theme.Font.caption)
                    .foregroundStyle(.secondary)
            }
            if original == .unavailable || !star.source.isVerified {
                Text("The original message is unavailable.")
                    .font(Theme.Font.caption)
                    .foregroundStyle(.secondary)
            } else if original == .failed {
                Text("Couldn't check the original message. Try again.")
                    .font(Theme.Font.caption)
                    .foregroundStyle(.secondary)
            }

            ViewThatFits(in: .horizontal) {
                HStack(spacing: Theme.Space.m) { actions }.fixedSize(horizontal: true, vertical: false)
                VStack(alignment: .leading, spacing: Theme.Space.xs) { actions }
            }
            .buttonStyle(.borderless)
            .font(Theme.Font.secondary)
        }
        .padding(.vertical, Theme.Space.s)
        .accessibilityElement(children: .contain)
        .accessibilityIdentifier("starred-snapshot")
    }

    @ViewBuilder private var actions: some View {
        let scope = store.scope
        if original != .unavailable, star.source.isVerified {
            Button { Task { if scope == store.scope { await openOriginal() } } } label: {
                if checking { ProgressView() } else { Text("Open Original").lineLimit(1).minimumScaleFactor(0.75) }
            }
            .disabled(checking)
            .accessibilityLabel("Open Original")
            .accessibilityIdentifier("starred-open-original")
        }
        Button {
            UIPasteboard.general.string = star.text
            model.toast = Toast(String(localized: "Copied [Selected Messages]"), style: .success)
        } label: { Text("Copy").lineLimit(1) }
        .accessibilityIdentifier("starred-copy")
        if store.canWrite {
            Button { Task { if scope == store.scope { await store.remove(star.source, id: star.id) } } } label: { Text("Unstar").lineLimit(1) }
                .disabled(store.isPending(star.source))
                .accessibilityIdentifier("starred-unstar")
        }
    }

    private func openOriginal() async {
        guard !checking, let group = store.group else { return }
        checking = true
        defer { checking = false }
        let key = star.source.key(group: group)
        let result = await store.verify(star.source, key: key)
        guard result != .retired else { return }
        original = result
        if result == .verified { model.openStarredMessage(star, key: key) }
    }
}

extension StarredSnapshot {
    var displayedSpeaker: String {
        if !speaker.isEmpty { return speaker }
        return role == "user" ? String(localized: "Me") : String(localized: "Assistant")
    }
}
#endif
