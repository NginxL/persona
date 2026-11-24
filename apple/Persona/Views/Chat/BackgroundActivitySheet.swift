import SwiftUI

/// The main conversation's completed silent rounds, opened from its menu or from Inbox. `ChatView`'s activity mode
/// shares the real store, quiet-round renderer, expansion and older-page loading; it never presents a composer and
/// never calls the conversation's seen or visible-chat paths.
struct BackgroundActivitySheet: View {
    @Environment(\.dismiss) private var dismiss
    var key: SessionKey

    var body: some View {
        NavigationStack {
            ChatView(key: key, style: .assistant, backgroundActivity: true)
                .toolbar { SheetClose { dismiss() } }
        }
        .presentationDetents([.large])
        .presentationDragIndicator(.visible)
    }
}
