import SwiftUI

extension EnvironmentValues {
    @Entry var larkAddPin: ((LarkMessage) -> Void)? = nil
}

struct LarkPinPresentation: ViewModifier {
    let pins: LarkChatPinsModel?
    @Binding var show: Bool
    @Binding var target: LarkChatPinTarget?
    @Binding var thread: LarkChatPinTarget?
    @Binding var adding: LarkMessage?
    var conversation: LarkConversation
    var display: LarkMessageDisplay
    var api: APIClient?
    var finish: () -> Void
    var add: (LarkMessage) -> Void

    func body(content: Content) -> some View {
        content
            .sheet(isPresented: $show, onDismiss: finish) {
                if let pins {
                    LarkChatPinsView(pins: pins, feed: conversation.id) { target = $0; show = false }
                        .environment(\.larkDisplay, display)
                }
            }
            .sheet(item: $thread) { target in
                if let root = target.root {
                    LarkThreadView(root: root, isGroup: conversation.isGroup, feedID: conversation.id, pinTarget: target, pinAPI: api)
                        .environment(\.larkDisplay, display)
                }
            }
            .accessibleConfirmationDialog("Pin This Message?", isPresented: Binding(get: { adding != nil }, set: { if !$0 { adding = nil } }), titleVisibility: .visible, hasCancel: true) {
                if let adding { Button("Pin [Lark Chat Pins View 638562]") { add(adding) } }
                Button("Cancel", role: .cancel) {}
            } message: { Text("Chat members can find this message in the pinned list.") }
    }
}

struct LarkChatPinsView: View {
    @Environment(\.dismiss) private var dismiss
    @Environment(\.larkDisplay) private var display
    let pins: LarkChatPinsModel
    var feed: String
    var open: (LarkChatPinTarget) -> Void
    @State private var removing: LarkChatPin?

    var body: some View {
        NavigationStack {
            List {
                if let problem = pins.problem { problemSection(problem) }
                if pins.items.isEmpty {
                    if pins.loading {
                        ProgressView(String(localized: "Loading Pinned Messages…")).frame(maxWidth: .infinity).padding(.vertical, Theme.Space.xl)
                    } else if pins.loaded && pins.problem == nil {
                        EmptyStateView(symbol: Theme.Icon.pin, title: String(localized: "No pinned messages yet"), message: String(localized: "Touch and hold a message in the chat, then choose Pin."))
                    }
                } else {
                    Section {
                        ForEach(pins.items) { pin in pinRow(pin) }
                    } header: {
                        Text("\(pins.total) pins").font(Theme.Font.caption)
                    }
                    if let problem = pins.pageProblem {
                        Section {
                            Text(problem).font(Theme.Font.secondary).foregroundStyle(.secondary)
                            Button("Retry [Components b8784c]") { Task { await pins.more() } }.lineLimit(1).disabled(pins.busy)
                                .accessibilityIdentifier("lark.pins.moreRetry")
                        }
                    } else if pins.nextCursor != nil {
                        ProgressView().frame(maxWidth: .infinity)
                            .id(pins.nextCursor)
                            // Changing the cursor must not cancel a coalesced live refresh.
                            .onAppear { Task { await pins.more() } }
                            .onChange(of: pins.loading) { _, loading in
                                if !loading { Task { await pins.more() } }
                            }
                    }
                }
            }
            .themedGroupedPage()
            .navigationTitle("Pinned Messages")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                SheetClose(id: "lark.pins.close") { dismiss() }
            }
            .accessibleConfirmationDialog("Unpin This Message?", isPresented: Binding(get: { removing != nil }, set: { if !$0 { removing = nil } }), titleVisibility: .visible, hasCancel: true) {
                if let pin = removing {
                    Button("Unpin [Lark Chat Pins View f13ef8]", role: .destructive) { Task { _ = await pins.change(message: pin.messageID, pin: pin.id) } }
                }
                Button("Cancel", role: .cancel) {}
            } message: { Text("The message stays in the chat.") }
            .tabActiveTask {
                let changes = LarkSync.shared.changes(of: feed)
                await pins.refresh()
                for await _ in changes { await pins.refresh() }
            }
            .onDisappear { pins.stop() }
        }
    }

    private func problemSection(_ text: String) -> some View {
        Section {
            Text(text).font(Theme.Font.secondary).foregroundStyle(.secondary)
            if !pins.blocked {
                Button(pins.requiresRecheck ? String(localized: "View Latest List") : String(localized: "Reload [Lark Chat Pins View 7bdd5c]")) { Task { await pins.refresh() } }
                    .lineLimit(1).disabled(pins.busy).accessibilityIdentifier("lark.pins.retry")
            }
        }
    }

    private func pinRow(_ pin: LarkChatPin) -> some View {
        HStack(alignment: .top, spacing: Theme.Space.m) {
            Button {
                Task { if let target = await pins.locate(pin) { open(target) } }
            } label: {
                VStack(alignment: .leading, spacing: Theme.Space.s) {
                    HStack(spacing: Theme.Space.s) {
                        if let m = pin.message {
                            LarkAvatarImage(name: m.senderName, url: m.senderPicture, size: Theme.Size.iconL)
                                .dynamicTypeSize(...DynamicTypeSize.xxxLarge).accessibilityHidden(true)
                            Text(m.senderName).font(Theme.Font.labelMedium).lineLimit(1)
                        }
                        Spacer(minLength: Theme.Space.s)
                        if pins.locating == pin.id { ProgressView().controlSize(.small) }
                    }
                    Text(summary(pin)).font(Theme.Font.body).foregroundStyle(.primary).lineLimit(3)
                        .multilineTextAlignment(.leading)
                    if let m = pin.message, m.olk != nil {
                        Label(m.olk?.isOpen == true ? String(localized: "Encrypted message") : String(localized: "Encrypted message · Public text"), systemImage: Theme.Icon.locked)
                            .font(Theme.Font.caption).foregroundStyle(.secondary).lineLimit(1)
                    }
                    if let time = pin.createdAt {
                        Text(time, format: .dateTime.month().day().hour().minute())
                            .font(Theme.Font.caption).foregroundStyle(.secondary)
                    }
                }
                .frame(maxWidth: .infinity, alignment: .leading)
                .contentShape(.rect)
            }
            .buttonStyle(.plain)
            .disabled(pins.busy || pins.blocked || pin.message == nil || pin.message?.deleted == true)
            .accessibilityIdentifier("lark.pin.\(pin.id)")
            if pin.canRemove {
                Menu {
                    Button("Unpin [Lark Chat Pins View f13ef8]", systemImage: Theme.Icon.unpin, role: .destructive) { removing = pin }
                } label: {
                    Image(systemName: Theme.Icon.more).font(Theme.Font.body)
                        .scaledFrame(width: Theme.Size.well, height: Theme.Size.well)
                }
                .accessibilityLabel("Pin actions").accessibilityIdentifier("lark.pin.menu.\(pin.id)")
                .disabled(pins.busy || pins.blocked || pins.requiresRecheck)
            }
        }
        .padding(.vertical, Theme.Space.s)
        .alignmentGuide(.listRowSeparatorLeading) { _ in 0 }
    }

    private func summary(_ pin: LarkChatPin) -> String {
        guard let m = pin.message, m.deleted != true else { return String(localized: "The original message is no longer available") }
        let policy = display.accountID == pins.accountID ? display : .hidden
        guard policy.showsBody(of: m) else { return String(localized: "Message recalled") }
        let body = policy.summary(m.displayText.trimmingCharacters(in: .whitespacesAndNewlines))
        let fallback = m.type == "IMAGE" ? String(localized: "[Image]") : m.type == "FILE" ? String(localized: "[File] [Lark Chat Pins View 54e20f]") : String(localized: "Messages [Quote Views 4da199]", comment: "Summary of a pinned message that has no text")
        let text = body.isEmpty ? fallback : body
        return m.recalled ? String(localized: "\(text) · Recalled") : text
    }
}
