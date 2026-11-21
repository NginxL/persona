import SwiftUI

/// An Agent's chat opened from inside a page is a drill-down (#9971, #12006): it goes on top of that page with Back to
/// it, and the sidebar's selection does not move (`AppModel.openInPage`). How it is pushed depends on the page:
/// - the conversation, the stack's root (a Brief card, a card's link): on the stack's path, as a task card's chat is
///   (`conversationAgentPush`, through `pushInboxChat`);
/// - a page that was itself pushed (a knowledge entry, a to-do, a meeting): its own destination (`pushedAgentChat`). Some of
///   those are pushed by view, and a value pushed over a page pushed by view lands under it (docs/lessons/ios.md).
@MainActor func conversationAgentPush(_ chat: ((InboxChatRoute) -> Void)?) -> ((SessionKey) -> Void)? {
    guard let chat else { return nil }
    return { chat(InboxChatRoute(key: $0, manager: nil)) }
}

/// A page's own push of the Agents it opens: nil in a sheet (it closes, and the Agent opens as before) and on the Mac,
/// whose shell opens a chat in its own column.
@MainActor func pageAgentPush(inSheet: Bool, _ set: @escaping (SessionKey) -> Void) -> ((SessionKey) -> Void)? {
    #if os(iOS)
    return inSheet ? nil : set
    #else
    return nil
    #endif
}

extension View {
    /// The chat of the Agent `key` names, pushed over this page while it is set (`pageAgentPush`). Never a split's first
    /// page: a split reads that from its path, which a push by view leaves as it was, and the chat would draw no Back.
    func pushedAgentChat(_ key: Binding<SessionKey?>) -> some View {
        navigationDestination(item: key) { k in
            ChatView(key: k).id(k).transformEnvironment(\.splitDetail) { $0?.isFirstPage = false }
        }
    }
}
