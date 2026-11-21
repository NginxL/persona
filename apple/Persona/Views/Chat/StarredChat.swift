#if os(iOS)
import SwiftUI

/// The actual transcript behind this chat's rows. Menu actions keep this context across their async work.
struct StarredChatContext {
    let key: SessionKey
    let conversation: StarredSource.Conversation
    let title: String
    let speaker: String
    let manager: String?
    let api: APIClient?

    func source(_ item: Item) -> StarredSource? {
        guard item.kind == .text, ["user", "assistant"].contains(item.role), item.injected == nil,
              item.wake != true, item.narration != true else { return nil }
        let source = StarredSource(machine: key.machineID, session: key.sessionID, item: item.id,
                                   conversation: conversation, fingerprint: item.sourceFingerprint)
        return source.isVerified ? source : nil
    }

    func action(for bar: MessageBarInfo) -> StarredMenuAction? {
        let store = StarredStore.shared
        guard !bar.pending, let item = bar.starItem, let source = source(item), store.group == key.group,
              store.canWrite else { return nil }
        let scope = store.scope
        switch store.mark(source, group: key.group) {
        case .saving: return nil
        case .saved:
            guard let id = store.referenceID(source) else { return nil }
            return StarredMenuAction(title: String(localized: "Unstar"), symbol: Theme.Icon.starredMessageFilled) {
                Task { if scope == store.scope { await store.remove(source, id: id) } }
            }
        case nil:
            guard item.fullSize != nil || !bar.text.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty else { return nil }
            let draft = StarredSnapshot(machine_id: source.machine, session_id: source.session, item_id: source.item,
                                        conversation: source.conversation, source_fingerprint: source.fingerprint,
                                        role: item.role, title: title, speaker: bar.from ?? (bar.isUser ? "" : speaker),
                                        source_manager: manager, written_at: item.ts, text: bar.text)
            return StarredMenuAction(title: String(localized: "Star"), symbol: Theme.Icon.starredMessage) {
                Task<Void, Never> { await add(draft, item: item, source: source, bar: bar, scope: scope, store: store) }
            }
        }
    }

    private func add(_ draft: StarredSnapshot, item: Item, source: StarredSource, bar: MessageBarInfo,
                     scope: Int, store: StarredStore) async {
        guard scope == store.scope else { return }
        var loadText: (() async throws -> String)?
        if item.fullSize != nil {
            loadText = { try await fullText(source, bar: bar) }
        }
        await store.add(draft, loadText: loadText)
    }

    private func fullText(_ source: StarredSource, bar: MessageBarInfo) async throws -> String {
        guard let api else { throw APIError(status: 0, message: String(localized: "Couldn't check the original message. Try again.")) }
        let item = try await api.pinStarredAuthority().fullItem(key, id: source.item)
        guard source.matches(item) else {
            throw APIError(status: 404, message: String(localized: "The original message is unavailable."))
        }
        let text = item.text ?? ""
        return bar.isUser ? UserRowText(text).shown : text
    }
}

extension EnvironmentValues {
    @Entry var starredChat: StarredChatContext?
}

struct StarredChatMark: View {
    @Environment(\.starredChat) private var chat
    let item: Item?

    var body: some View {
        if let chat, let item, let source = chat.source(item), let mark = StarredStore.shared.mark(source, group: chat.key.group) {
            ZStack {
                if mark == .saving {
                    ProgressView().controlSize(.mini).accessibilityLabel("Saving Starred Message")
                } else {
                    ThemedIcon(Theme.Icon.starredMessageFilled)
                        .font(Theme.Font.caption)
                        .foregroundStyle(Theme.accent)
                        .accessibilityLabel("Starred")
                }
            }
            .frame(width: Theme.Space.l, height: Theme.Space.l)
            .allowsHitTesting(false)
            .accessibilityIdentifier("chat-starred-mark")
        }
    }
}
#endif
