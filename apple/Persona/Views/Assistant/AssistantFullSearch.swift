import SwiftUI

#if os(iOS)
/// Search over the whole window, the open iPhone Duo's (#3705), as ChatGPT's there: the field at the window's foot, over
/// the keyboard, × beside it; the conversations above, the latest first, and what matches as it is typed. A row opens its
/// conversation. The shell's drawer and the iPad keep the sidebar's own search, its field at the sidebar's top.
struct AssistantFullSearch: View {
    var open: (SessionKey) -> Void
    @Environment(AppModel.self) private var model
    @Environment(\.dismiss) private var dismiss
    @State private var query = ""
    @FocusState private var focused: Bool

    /// The sidebar's conversations (the latest first), read here: the sidebar under the cover is off the window, and what
    /// it handed in would not follow a conversation that comes or is renamed meanwhile.
    private var hits: [Session] { AssistantSidebarSearch.filter(model.assistantConversations, query) }

    var body: some View {
        List {
            Section {
                ForEach(hits) { row($0) }
            } header: {
                Text(query.isEmpty ? String(localized: "Recent") : String(localized: "\(hits.count) Chats [Assistant Sidebar a72a2a]"))
                    .font(Theme.Font.secondaryMedium)
                    .foregroundStyle(Color.primary)
                    .textCase(nil)
            }
            .listRowSeparator(.hidden)
            .listRowBackground(Color.clear)
        }
        .listStyle(.plain)
        .scrollContentBackground(.hidden)
        .scrollDismissesKeyboard(.interactively)
        .accessibilityIdentifier("full-search") // the list's own: on the whole page it named the bar's field and × too
        .overlay {
            if hits.isEmpty, !query.isEmpty {
                VStack(spacing: Theme.Space.sm) {
                    ThemedIcon(Theme.Icon.search).font(Theme.Font.title).foregroundStyle(.tertiary)
                    Text("No Results for “\(query)”").font(Theme.Font.secondary).foregroundStyle(.secondary)
                }
            }
        }
        // At the foot, over the keyboard (the inset rides on it), as ChatGPT's.
        .safeAreaInset(edge: .bottom, spacing: 0) { bar }
        .background(Theme.pageBackground.ignoresSafeArea())
        .onAppear { focused = true }
        .accessibilityAction(.escape) { dismiss() } // VoiceOver's scrub, as on a sheet
    }

    private var bar: some View {
        HStack(spacing: Theme.Space.m) {
            HStack(spacing: Theme.Space.s) {
                ThemedIcon(Theme.Icon.search).foregroundStyle(.secondary)
                TextField(String(localized: "Search Chats [Assistant Sidebar 55c9e4]"), text: $query)
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

    private func row(_ s: Session) -> some View {
        let title = s.key == model.assistantMain ? AssistantSidebar.mainDisplayName(model)
            : s.title.isEmpty || s.title == "Claude Code" ? String(localized: "New Chat [Quick Actions ea4b29]") : s.title
        let preview = s.lastText.map { Fmt.plain($0).replacingOccurrences(of: "\n", with: " ") } ?? ""
        return Button { open(s.key) } label: {
            HStack(spacing: Theme.Space.m) {
                ThemedIcon(Theme.Icon.conversation)
                    .font(Theme.Font.body)
                    .foregroundStyle(.secondary)
                    .scaledFrame(width: Theme.Size.tapTarget, height: Theme.Size.tapTarget)
                    .background(RoundedRectangle(cornerRadius: Theme.Radius.control, style: .continuous).fill(Theme.Fill.well))
                VStack(alignment: .leading, spacing: Theme.Space.xxs) {
                    Text(title).font(Theme.Font.body).foregroundStyle(Color.primary).lineLimit(1)
                    if !preview.isEmpty {
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
