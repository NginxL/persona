import PersonaKit
import SwiftUI

/// Archived: the conversations swiped out of the sidebar (or archived by Persona), newest first. A tap opens one, a swipe
/// or a long press brings it back into the list. Nothing here deletes: archiving only hides (docs/assistant/assistant.md).
struct ArchivedConversationsView: View {
    @Environment(AppModel.self) private var model
    @Environment(\.dismiss) private var dismiss
    var open: (SessionKey) -> Void

    var body: some View {
        let list = model.archivedConversations
        NavigationStack {
            List {
                ForEach(list) { s in row(s).themedRow() }
            }
            .listStyle(.plain)
            .themedPage()
            .overlay {
                if list.isEmpty {
                    EmptyStateView(symbol: Theme.Icon.archive, title: String(localized: "No archived chats"), message: String(localized: "Swipe left on a chat in the list to archive it. It'll show up here."))
                }
            }
            .navigationTitle("Archived")
            #if os(iOS) || os(visionOS)
            .navigationBarTitleDisplayMode(.inline)
            #endif
            .toolbar {
                SheetClose { dismiss() }
            }
        }
    }

    private func row(_ s: Session) -> some View {
        let title = s.title.isEmpty || s.title == "Claude Code" ? String(localized: "New Chat [Quick Actions ea4b29]") : s.title
        return Button { open(s.key) } label: {
            VStack(alignment: .leading, spacing: Theme.Space.xxs) {
                if let t = ConversationTitle(title) {
                    Text("\(Text(t.head).foregroundStyle(Color.secondary))\(t.topic)")
                        .font(Theme.Font.body).foregroundStyle(Color.primary).lineLimit(1)
                } else {
                    Text(title).font(Theme.Font.body).foregroundStyle(Color.primary).lineLimit(1)
                }
                if let at = s.updatedAt {
                    Text(Fmt.relative(at)).font(Theme.Font.label).foregroundStyle(.secondary).lineLimit(1)
                }
            }
            .padding(.vertical, Theme.Space.xs)
            .frame(maxWidth: .infinity, alignment: .leading)
            .contentShape(.rect)
        }
        .buttonStyle(.plain)
        .accessibilityIdentifier("archived-conversation")
        .swipeActions(edge: .trailing, allowsFullSwipe: true) {
            Button { Task { await model.archiveConversation(s.key, false) } } label: {
                Label("Unarchive", systemImage: Theme.Icon.unarchive)
            }
            .tint(Theme.accent)
        }
        .contextMenu {
            Button { Task { await model.archiveConversation(s.key, false) } } label: {
                Label("Unarchive", systemImage: Theme.Icon.unarchive)
            }
        }
    }
}
