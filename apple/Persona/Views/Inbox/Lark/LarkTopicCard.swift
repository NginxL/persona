import SwiftUI

/// One topic of a Feishu topic group, drawn the way the Lark gateway draws a topic group (#1087): a card per topic with the
/// poster's avatar, name and time on top and the topic under them, folded behind Expand past `Theme.Size.box320`, then
/// its newest replies (`LarkTopicReplies`), its reactions and Add Reaction / Reply.
/// A tap on the topic (not on its links or a card's buttons) or on its replies opens its thread; a long press has the
/// chat's menu. Topics are never run together like a chat's messages (`LarkChatLayout.continues`): where a bot posts a
/// topic a minute, each one still has its own card, name and time. A recalled or edited topic, and a recalled reply,
/// follow Message Tweaks (Keep Recalled Messages, Edit History) as a chat's row does (`LarkMessageDisplay`).
struct LarkTopicCard: View {
    var topic: LarkMessage
    /// The topic group, named under the thread's title.
    var chatName: String
    var feedID: String?
    /// The topic group's newest replies, read as cards come on screen.
    var replies: LarkTopicReplies?
    @Environment(\.larkOpenProfile) private var openProfile
    @Environment(\.larkReact) private var react
    @Environment(\.larkAddPin) private var addPin
    @Environment(\.larkDisplay) private var display
    @Environment(\.larkManager) private var manager
    /// The open thread, and whether its box is focused (Reply).
    @State private var threadOpen: LarkTopicThreadOpening?
    @State private var picking = false
    @State private var showHistory = false
    /// On screen: a topic group lays out all its cards at once (`LarkChatStack`), so appearing is not coming on screen.
    @State private var onScreen = false

    var body: some View {
        let m = topic
        VStack(alignment: .leading, spacing: Theme.Space.s) {
            header
            let fold = LarkTopicFold { LarkTopicContent(message: m) }.onTapGesture { openThread() }
            // A recalled topic whose body is not shown has no menu, as a chat's row has none: Copy would copy what it
            // no longer shows.
            if display.showsBody(of: m) { fold.contextMenu { menu } } else { fold }
            if m.olk == nil { LarkTranslationLine(message: m, feedID: feedID) }
            if !m.recalled && LarkTopicReplies.count(of: m) > 0 {
                LarkTopicReplyPreview(topic: m, replies: replies, open: canOpen ? { openThread() } : nil)
            }
            reactionChips
            if !m.recalled || display.showsBody(of: m) { actions }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .card(padding: Theme.Space.m)
        .accessibilityElement(children: .contain)
        .accessibilityIdentifier("lark.topic")
        .onScrollVisibilityChange(threshold: 0.01) { onScreen = $0 }
        .task(id: readKey) { await readReplies() }
        .sheet(isPresented: $picking) { LarkReactionPicker(message: m) { react?(m, $0) } }
        .sheet(isPresented: $showHistory) { LarkEditHistoryView(message: m) }
        .sheet(item: $threadOpen) { t in
            LarkThreadView(root: m, isGroup: true, feedID: feedID, focusBox: t.focus, chatName: chatName)
        }
    }

    /// Reads the topic's newest replies once its card has been on screen a moment (not one flung past), and again when
    /// its reply count changes; off screen before its turn, it is not read. Nor is a topic whose thread cannot be opened
    /// (recalled): its replies would lead nowhere. The key and the read are a property and a method, not in `body`'s
    /// chain (a closure there is type-checked with the whole chain, docs/lessons/ios.md).
    private var readKey: String { "\(topic.id)#\(LarkTopicReplies.count(of: topic))#\(onScreen)" }

    private func readReplies() async {
        guard onScreen else { replies?.drop(topic.id); return }
        try? await Task.sleep(for: .milliseconds(200))
        if !Task.isCancelled && canOpen { replies?.want(topic) }
    }

    private var menu: some View {
        LarkMessageMenu(message: topic, react: react, reply: canOpen ? { openThread(focus: true) } : nil,
                        pickReaction: { picking = true }, addPin: addPin, showProfile: openProfile == nil ? nil : { showProfile() },
                        manager: manager)
    }

    /// Avatar, then the name (with Bot for a bot) over the time. Avatar and name open the poster's card.
    private var header: some View {
        let m = topic
        return HStack(spacing: Theme.Space.s) {
            Button(action: showProfile) {
                LarkAvatarImage(name: m.senderName, url: m.senderPicture, size: Theme.Size.well)
            }
            .buttonStyle(.plain)
            .accessibilityLabel("\(m.senderName)'s profile")
            VStack(alignment: .leading, spacing: Theme.Space.xxs) {
                HStack(spacing: Theme.Space.xs) {
                    Text(m.senderName).font(Theme.Font.secondaryMedium).lineLimit(1)
                        .onTapGesture { showProfile() }
                    if m.senderType == "bot" { LarkNameTag(text: String(localized: "Bot")) }
                }
                Text(LarkChatLayout.dayLabel(m.createdAt)).font(Theme.Font.label).foregroundStyle(.secondary).lineLimit(1)
            }
        }
    }

    /// None on a recalled topic, as a chat's row.
    @ViewBuilder private var reactionChips: some View {
        let m = topic
        let reactions = m.reactions ?? []
        if !reactions.isEmpty && !m.recalled {
            LarkFlowLayout(spacing: Theme.Space.sm, lineSpacing: Theme.Space.sm) {
                ForEach(reactions, id: \.key) { r in
                    Button { react?(m, r.key) } label: { LarkReactionChip(reaction: r) }
                        .buttonStyle(.plain)
                        .disabled(react == nil)
                }
            }
        }
    }

    /// Add Reaction and Reply under the topic (the Lark gateway's card footer), Recalled and Edited at the other end. A recalled topic
    /// has neither button; it says Recalled when its body shows.
    private var actions: some View {
        HStack(spacing: Theme.Space.s) {
            if react != nil && !topic.recalled {
                action(Theme.Icon.react, String(localized: "Add Reaction"), id: "lark.topic.react") { picking = true }
            }
            if canOpen {
                action(Theme.Icon.reply, String(localized: "Reply"), id: "lark.topic.reply") { openThread(focus: true) }
            }
            Spacer(minLength: 0)
            Group {
                if topic.recalled { Text("Recalled") }
                if topic.edited == true { editedMark }
            }
            .font(Theme.Font.label).foregroundStyle(.secondary).lineLimit(1)
        }
        .padding(.leading, -Theme.Space.s) // the icons, not their tap areas, line up with the text above
    }

    /// Edited: with Edit History on and old versions kept, a button to them (`LarkEditHistoryView`).
    @ViewBuilder private var editedMark: some View {
        if display.showsHistory(of: topic) {
            Button("Edited") { showHistory = true }.buttonStyle(.plain)
                .accessibilityLabel("View edit history").accessibilityIdentifier("lark.editHistory.\(topic.id)")
        } else {
            Text("Edited")
        }
    }

    private func action(_ icon: String, _ label: String, id: String, run: @escaping () -> Void) -> some View {
        Button(action: run) {
            Image(systemName: icon).font(Theme.Font.body).foregroundStyle(.secondary)
                .scaledFrame(width: Theme.Size.well, height: Theme.Size.tapTarget)
                .contentShape(.rect)
        }
        .buttonStyle(.plain)
        .accessibilityLabel(label)
        .accessibilityIdentifier(id)
    }

    /// Nothing to open for a recalled topic or one not in Feishu yet (`LarkReply.route`).
    private var canOpen: Bool { LarkReply.route(for: topic, inTopicGroup: true, inThread: false) != nil }

    private func openThread(focus: Bool = false) {
        guard canOpen else { return }
        threadOpen = LarkTopicThreadOpening(focus: focus)
    }

    private func showProfile() {
        guard let id = topic.senderID, !id.isEmpty else { return }
        openProfile?(LarkPersonRef(id: id, name: topic.senderName, avatar: topic.senderPicture, feedID: feedID))
    }
}

/// A topic's thread opening from its card, with Reply's focus in it. An item, not a flag beside a separate focus flag:
/// the sheet's content was built from the state before the tap that opened it, so the box was never focused.
private struct LarkTopicThreadOpening: Identifiable {
    let id = UUID()
    var focus: Bool
}

/// A topic's newest two replies, a line each, under "View N earlier replies" when there are more (the Lark gateway's topic card).
/// Until they are read, the lines they will take are there already, as placeholders: a card does not grow when its
/// replies come in (in a list anchored at its newest end, cards growing as they come on screen moved the cards around
/// them on and off screen, again and again). A tap opens the thread. A view of its own: a read coming in redraws these
/// lines, not every card on screen.
private struct LarkTopicReplyPreview: View {
    var topic: LarkMessage
    var replies: LarkTopicReplies?
    /// Opens the thread; nil when there is none to open.
    var open: (() -> Void)?
    @Environment(\.larkDisplay) private var display

    var body: some View {
        let count = LarkTopicReplies.count(of: topic)
        let preview = replies?.previews[topic.id]
        let shown = preview?.replies ?? []
        // Not read yet (or the read failed): the lines the replies will take.
        let pending = shown.isEmpty ? min(count, LarkTopicReplies.shown) : 0
        let earlier = max(0, count - max(shown.count, pending))
        Button { open?() } label: {
            VStack(alignment: .leading, spacing: Theme.Space.xs) {
                if earlier > 0 {
                    Text("View \(earlier) earlier replies").font(Theme.Font.label).foregroundStyle(LarkColor.blue).lineLimit(1)
                }
                ForEach(shown) { r in
                    Text("\(Text(r.senderName).font(Theme.Font.secondaryMedium)): \(said(r))")
                        .font(Theme.Font.secondary).lineLimit(1)
                }
                ForEach(0..<pending, id: \.self) { _ in
                    Text("\(Text("Someone [Lark Topic Card 8ea02f]").font(Theme.Font.secondaryMedium)): Reply text goes here")
                        .font(Theme.Font.secondary).lineLimit(1).redacted(reason: .placeholder)
                }
            }
            .padding(.horizontal, Theme.Space.m)
            .padding(.vertical, Theme.Space.s)
            .frame(maxWidth: .infinity, alignment: .leading)
            .background(Color.primary.opacity(0.04), in: .rect(cornerRadius: Theme.Radius.control, style: .continuous))
            .contentShape(.rect)
        }
        .buttonStyle(.plain)
        .disabled(open == nil)
        .accessibilityLabel(spoken(count: count, earlier: earlier, shown: shown))
        .accessibilityIdentifier("lark.topic.replies")
        if preview?.failed == true, shown.isEmpty, let replies {
            HStack(spacing: Theme.Space.s) {
                Text("Couldn't load replies").font(Theme.Font.label).foregroundStyle(.secondary).lineLimit(1)
                Button("Retry [Components b8784c]") { replies.retry(topic) }.font(Theme.Font.labelMedium).lineLimit(1)
            }
        }
    }

    /// VoiceOver reads the preview as one line: what it shows, in order.
    private func spoken(count: Int, earlier: Int, shown: [LarkMessage]) -> String {
        guard !shown.isEmpty else { return String(localized: "\(count) replies, loading") }
        let lines = shown.map { String(localized: "\($0.senderName): \(said($0))") }
        return ((earlier > 0 ? [String(localized: "View \(earlier) earlier replies")] : []) + lines)
            .joined(separator: String(localized: ", ", comment: "Separator between the lines of a spoken summary"))
    }

    private func said(_ r: LarkMessage) -> String { LarkTopicReplies.line(of: r, display: display) }
}

/// A topic's own content on its card: what a chat bubble holds, without the bubble (the card is its frame). A recalled
/// topic shows what it said only with Keep Recalled Messages on and its content kept (`LarkMessageDisplay.showsBody`).
private struct LarkTopicContent: View {
    var message: LarkMessage
    @Environment(\.larkDisplay) private var display

    var body: some View {
        let m = message
        let source = m.parts ?? []
        let parts = display.shown(source)
        VStack(alignment: .leading, spacing: Theme.Space.sm) {
            if !display.showsBody(of: m) {
                Text(m.recallNotice).font(Theme.Font.caption).foregroundStyle(.tertiary).italic()
            } else {
                if let reply = m.replyTo { LarkReplyQuote(reply: reply) }
                if let encryption = m.olk {
                    LarkEncryptedContent(encryption: encryption)
                } else if parts.isEmpty {
                    let text = display.shown(m.text)
                    Text(m.text.isEmpty ? "[\(m.type.lowercased())]" : text).font(Theme.Font.body)
                } else {
                    LarkPartsView(parts: parts)
                        .environment(\.larkContext, LarkRenderContext(messageID: m.recalled ? nil : m.id, images: LarkPartsView.imageURLs(parts)))
                    if let d = m.decorations { LarkDecorationsView(decorations: d) }
                }
            }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
    }
}

/// A topic taller than the fold shows its top under a fade with Expand over it; open, Collapse under it (the Lark gateway folds a
/// topic at 320 px). It is laid out no taller than the fold from the start and measured as it is, so a long topic never
/// shows whole first and then jumps shut. What the fold hides takes no taps.
private struct LarkTopicFold<Content: View>: View {
    @ViewBuilder var content: Content
    @Environment(\.dynamicTypeSize) private var typeSize
    /// The content's own height.
    @State private var height: CGFloat = 0
    @State private var open = false

    var body: some View {
        let fold = Theme.Size.box320 * Theme.sizeScale(typeSize)
        let folded = height > fold && !open
        VStack(spacing: Theme.Space.s) {
            content
                .fixedSize(horizontal: false, vertical: true)
                .onGeometryChange(for: CGFloat.self) { $0.size.height } action: { height = $0 }
                .frame(maxHeight: open ? nil : fold, alignment: .top)
                .clipped()
                .mask {
                    if folded {
                        LinearGradient(stops: [.init(color: .black, location: 0.8), .init(color: .clear, location: 1)],
                                       startPoint: .top, endPoint: .bottom)
                    } else {
                        Rectangle()
                    }
                }
                .contentShape(.rect)
                .overlay(alignment: .bottom) { if folded { toggle } }
            if open && height > fold { toggle }
        }
    }

    private var toggle: some View {
        Button {
            withAnimation(Theme.Motion.layout) { open.toggle() }
        } label: {
            Text(open ? String(localized: "Collapse") : String(localized: "Expand"))
                .font(Theme.Font.labelMedium)
                .lineLimit(1)
                .padding(.horizontal, Theme.Space.l)
                .padding(.vertical, Theme.Space.xs)
                .surface(interactive: true, in: .capsule)
                // The pill stays small, its tap target does not: a finger a little off it must not open the topic. The
                // pill keeps to the topic's edge: at the foot of the fold, right under the open topic.
                .scaledFrame(minHeight: Theme.Size.tapTarget, alignment: open ? .top : .bottom)
                .contentShape(.rect)
        }
        .buttonStyle(.plain)
        .accessibilityIdentifier("lark.topic.fold")
    }
}
