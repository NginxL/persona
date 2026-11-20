import SwiftUI

/// A chat in the Messages list (#392): avatar, name and time, the last message and its unread count. The iPhone's tab
/// (`LarkConversationsView`) and the Mac's Messages section (`MacLarkMessagesPage`, #3162) draw the same row.
struct LarkConversationRow: View {
    let conversation: LarkConversation
    let display: LarkMessageDisplay

    var body: some View {
        HStack(spacing: Theme.Space.m) {
            LarkAvatarImage(name: conversation.name, url: conversation.picture, account: conversation.account, size: Theme.Size.avatarXL)
            VStack(alignment: .leading, spacing: Theme.Space.xs) {
                HStack(spacing: Theme.Space.s) {
                    Text(conversation.name).font(Theme.Font.bodyStrong).foregroundStyle(.primary).lineLimit(1)
                    Spacer(minLength: 0)
                    if let date = conversation.updatedAt {
                        Text(Fmt.relative(date)).font(Theme.Font.caption).foregroundStyle(.secondary).lineLimit(1)
                    }
                }
                HStack(spacing: Theme.Space.s) {
                    Text(display.summary(conversation.preview)).font(Theme.Font.secondary).foregroundStyle(.secondary).lineLimit(1)
                    Spacer(minLength: 0)
                    if conversation.badge > 0 { CountBadge(count: conversation.badge) }
                    if conversation.muted { Image(systemName: Theme.Icon.muted).font(Theme.Font.caption).foregroundStyle(.secondary) }
                }
            }
        }
        .padding(.vertical, Theme.Space.s)
    }
}

/// One chip per signed-in account, its unread count beside the name; the chosen one is filled.
struct LarkAccountChips: View {
    let choice: LarkAccountChoice

    var body: some View {
        ScrollView(.horizontal) {
            HStack(spacing: Theme.Space.s) {
                ForEach(choice.accounts) { account in
                    FilterChip(title: account.title, count: account.unread > 0 ? account.unread : nil,
                               on: account.id == choice.selectedID) { choice.select(account.id) }
                        .accessibilityIdentifier("feishu-account-\(account.id)")
                }
            }
            .padding(.horizontal, Theme.Space.page)
        }
        .accessibilityLabel("Lark account")
    }
}
