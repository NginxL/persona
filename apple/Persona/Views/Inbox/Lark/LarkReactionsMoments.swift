import SwiftUI

// MARK: - Reactions

extension EnvironmentValues {
    /// Toggles the user's reaction (key) on a message; set by the chat view. Nil: reactions are read-only.
    @Entry var larkReact: ((LarkMessage, String) -> Void)? = nil
}

/// One emoji from the Feishu catalogue as a picture.
struct LarkEmojiImage: View {
    var key: String
    var size: CGFloat
    @Environment(\.larkManager) private var manager

    var body: some View {
        let media = LarkMedia.shared
        if let url = media.emoji(key, manager: manager)?.url {
            LarkRemoteImage(url: url, maxPixel: size * 3, contentMode: .fit).frame(width: size, height: size)
        } else {
            Text(LarkMedia.labelFallback(key, text: nil)).font(.system(size: size * 0.45)).lineLimit(1).minimumScaleFactor(0.5)
                .frame(width: size, height: size)
        }
    }
}

/// The emoji of the long-press bar: the hub's quick reactions (recent first, then Feishu's defaults), each once, as
/// many as the row shows without scrolling.
nonisolated enum LarkQuickReactions {
    /// Feishu's long-press row holds six or seven; the menu's palette shows six across without scrolling.
    static let count = 6

    static func bar(_ keys: [String]) -> [String] {
        var seen = Set<String>(), out: [String] = []
        for k in keys {
            let key = k.trimmingCharacters(in: .whitespaces)
            guard !key.isEmpty, seen.insert(key.lowercased()).inserted else { continue }
            out.append(key)
            if out.count == count { break }
        }
        return out
    }
}

/// Add Reaction: the user's quick reactions on top, then every group of the catalogue (Feishu's picker).
struct LarkReactionPicker: View {
    var message: LarkMessage
    var pick: (String) -> Void
    @Environment(\.dismiss) private var dismiss
    @Environment(\.larkManager) private var manager
    @Environment(\.larkGroup) private var group

    var body: some View {
        let catalog = LarkMedia.shared.catalog(manager)
        let mine = Set((message.reactions ?? []).filter { $0.mine == true }.map { $0.key.lowercased() })
        NavigationStack {
            ScrollView {
                VStack(alignment: .leading, spacing: Theme.Space.l) {
                    HStack(spacing: 0) {
                        ForEach(Array(catalog.quickReactions.prefix(7)), id: \.self) { key in
                            cell(key, size: 34, selected: mine.contains(key.lowercased()))
                        }
                    }
                    ForEach(catalog.groups, id: \.title) { g in
                        VStack(alignment: .leading, spacing: Theme.Space.s) {
                            Text(g.title).font(Theme.Font.captionStrong).foregroundStyle(.secondary)
                            LazyVGrid(columns: Array(repeating: GridItem(.flexible(), spacing: 4), count: 7), spacing: 8) {
                                ForEach(g.keys, id: \.self) { key in cell(key, size: 30, selected: mine.contains(key.lowercased())) }
                            }
                        }
                    }
                    if catalog.groups.isEmpty { ProgressView().frame(maxWidth: .infinity) }
                }
                .padding(Theme.Space.l)
            }
            .navigationTitle("Add Reaction")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar { SheetClose { dismiss() } }
            .onAppear { LarkMedia.shared.loadEmojiCatalog(manager: manager, using: AppModel.shared.larkAPI(group: group)) }
        }
        .presentationDetents([.medium, .large])
    }

    private func cell(_ key: String, size: CGFloat, selected: Bool) -> some View {
        Button {
            pick(key)
            dismiss()
        } label: {
            LarkEmojiImage(key: key, size: size)
                .frame(maxWidth: .infinity, minHeight: size + 12)
                .background(selected ? LarkColor.blue.opacity(0.15) : .clear, in: .rect(cornerRadius: Theme.Radius.control))
        }
        .buttonStyle(.plain)
        .accessibilityLabel(LarkMedia.shared.emoji(key, manager: manager)?.label ?? key)
    }
}

// MARK: - Moments

nonisolated enum LarkMoments {
    /// The post id of a Moments share link (`applink…/client/moments/detail?postId=…`).
    static func postID(_ link: String?) -> String? {
        guard let link, let c = URLComponents(string: link), c.path.hasSuffix("/client/moments/detail") || c.path.hasSuffix("/moments/detail"),
              let id = c.queryItems?.first(where: { $0.name == "postId" })?.value, !id.isEmpty, id.allSatisfy(\.isNumber) else { return nil }
        return id
    }
}

/// A shared Moments post in a chat: Feishu's link preview card (title, author, excerpt, counts); tap reads the post.
struct LarkMomentsCard: View {
    var part: LarkPart
    @Environment(\.larkContext) private var context
    @Environment(\.larkAccount) private var larkAccount
    @Environment(\.larkManager) private var larkManager
    @Environment(\.larkGroup) private var larkGroup
    @State private var preview: LarkURLPreview?
    @State private var reading = false

    var body: some View {
        let card = preview?.card
        Button { reading = true } label: {
            VStack(alignment: .leading, spacing: Theme.Space.s) {
                HStack(spacing: Theme.Space.sm) {
                    Image(systemName: "circle.hexagongrid.fill").font(Theme.Font.label).foregroundStyle(LarkColor.blue)
                    Text(preview?.title ?? String(localized: "Moments post")).font(Theme.Font.secondaryStrong).lineLimit(1)
                }
                if let author = card?.author {
                    HStack(spacing: Theme.Space.s) {
                        LarkAvatarImage(name: author, url: card?.avatarUrl, size: 28)
                        VStack(alignment: .leading, spacing: Theme.Space.xxs) {
                            Text(author).font(Theme.Font.captionMedium).lineLimit(1)
                            if let sub = card?.subtitle { Text(sub).font(Theme.Font.label).foregroundStyle(.secondary).lineLimit(1) }
                        }
                    }
                }
                if let body = card?.body, !body.isEmpty {
                    Text(body).font(Theme.Font.secondary).foregroundStyle(.primary.opacity(0.85)).lineLimit(5).multilineTextAlignment(.leading)
                } else if preview == nil {
                    Text(part.text ?? "").font(Theme.Font.label).foregroundStyle(.secondary).lineLimit(2)
                }
                if let footer = card?.footer { Text(footer).font(Theme.Font.label).foregroundStyle(.secondary) }
            }
            .padding(Theme.Space.m)
            .frame(maxWidth: .infinity, alignment: .leading)
            .background(LarkColor.cardBackground, in: .rect(cornerRadius: Theme.Radius.card, style: .continuous))
            .overlay(RoundedRectangle(cornerRadius: Theme.Radius.card, style: .continuous).strokeBorder(LarkColor.cardStroke))
            .contentShape(.rect)
        }
        .buttonStyle(.plain)
        .task(id: part.url) {
            guard let mid = context.messageID, let url = part.url, let api = AppModel.shared.larkAPI(group: larkGroup, manager: larkManager, account: larkAccount) else { return }
            preview = try? await api.larkURLPreview(message: mid, url: url)
        }
        .sheet(isPresented: $reading) { if let id = LarkMoments.postID(part.url) { LarkMomentsPostView(postID: id, link: part.url, manager: larkManager) } }
    }
}

/// One Moments post: author, text with Feishu emoji, pictures, reactions, comments.
struct LarkMomentsPostView: View {
    @Environment(AppModel.self) private var model
    @Environment(\.dismiss) private var dismiss
    @Environment(\.openURL) private var openURL
    @Environment(\.larkAccount) private var larkAccount
    @Environment(\.larkGroup) private var larkGroup
    var postID: String
    var link: String?
    /// Pushed as a page (the inbox's Moments item) instead of presented as a sheet: no stack of its own, no Done.
    var pushed = false
    /// The manager the post, its comments and pictures are read through (a chat's or inbox item's); nil: the default's.
    var manager: String? = nil
    @State private var post: LarkMomentsPost?
    @State private var comments: [LarkMomentsComment] = []
    @State private var error: String?
    @State private var gallery: Int?
    @State private var person: LarkPersonRef?

    var body: some View {
        if pushed {
            page
        } else {
            NavigationStack { page }
        }
    }

    private var page: some View {
            ScrollView {
                VStack(alignment: .leading, spacing: Theme.Space.l) {
                    if let error { Label(error, systemImage: Theme.Icon.error).font(Theme.Font.caption).foregroundStyle(Theme.danger) }
                    if let p = post {
                        author(name: p.userName ?? "", avatar: p.userAvatarUrl, id: p.userId, time: p.createTime)
                        LarkInlineText(runs: Self.runs(p.content), base: LarkTextBase(size: Theme.Level.body.size)).textSelection(.enabled)
                        images(p.images ?? [])
                        if let groups = p.reactionGroups, !groups.isEmpty {
                            LarkFlowLayout(spacing: 6, lineSpacing: 6) {
                                ForEach(groups, id: \.type) { g in
                                    HStack(spacing: Theme.Space.xs) {
                                        LarkEmojiImage(key: g.type, size: 17)
                                        Text("\(g.count)").font(Theme.Font.label).foregroundStyle(g.selfInvolved == true ? LarkColor.blue : .secondary)
                                    }
                                    .padding(.horizontal, Theme.Space.s).padding(.vertical, Theme.Space.xs)
                                    .background(Color.primary.opacity(0.05), in: .capsule)
                                }
                            }
                        }
                        Divider()
                        Text("\(p.commentCount ?? comments.count) comments").font(Theme.Font.secondaryStrong)
                        ForEach(comments) { c in
                            VStack(alignment: .leading, spacing: Theme.Space.sm) {
                                author(name: c.userName ?? "", avatar: c.userAvatarUrl, id: c.userId, time: c.createTime, small: true)
                                LarkInlineText(runs: Self.runs(c.content), base: LarkTextBase(size: Theme.Level.secondary.size)).padding(.leading, 40)
                            }
                            .padding(.vertical, Theme.Space.xs)
                        }
                    } else if error == nil {
                        ProgressView().scaledFrame(maxWidth: .infinity, minHeight: Theme.Size.box200)
                    }
                }
                .padding(Theme.Space.l)
            }
            .navigationTitle("Moments")
            .navigationBarTitleDisplayMode(.inline)
            .pageToolbar {
                if !pushed {
                    SheetClose { dismiss() }
                }
                if let link, let u = URL(string: link) {
                    ToolbarItem(placement: .primaryAction) {
                        Button { openURL(u) } label: { Label("Open in Lark [Feishu Doc Reader 76d0eb]", systemImage: Theme.Icon.openExternal) }
                    }
                }
            }
            .task {
                await model.readMessagingSource(group: larkGroup, manager: manager)
                guard let api = model.larkAPI(group: larkGroup, manager: manager, account: larkAccount) else { return }
                LarkMedia.shared.loadEmojiCatalog(manager: manager, using: model.larkAPI(group: larkGroup))
                async let c = api.larkMomentsComments(postID)
                do { post = try await api.larkMomentsPost(postID) } catch { self.error = error.localizedDescription }
                comments = (try? await c) ?? []
            }
            .fullScreenCover(item: Binding(get: { gallery.map { GalleryIndex(index: $0) } }, set: { gallery = $0?.index })) { g in
                ImageViewer(urls: (post?.images ?? []).compactMap(\.url).map(media), index: g.index)
            }
            .sheet(item: $person) { LarkProfileView(person: $0) }
            // A pushed page does not inherit the inbox's environment: its emoji, reactions and the author's profile are
            // read through the post's manager.
            .environment(\.larkManager, manager)
    }

    private struct GalleryIndex: Identifiable { var index: Int; var id: Int { index } }

    private func media(_ url: String) -> String { LarkMedia.url(url, manager: manager) }

    private func author(name: String, avatar: String?, id: String?, time: Double?, small: Bool = false) -> some View {
        Button { if let id { person = LarkPersonRef(id: id, name: name, avatar: avatar) } } label: {
            HStack(spacing: Theme.Space.m) {
                LarkAvatarImage(name: name, url: avatar, size: small ? 30 : 40)
                    .environment(\.larkManager, manager)
                VStack(alignment: .leading, spacing: Theme.Space.xxs) {
                    Text(name).font(small ? Theme.Font.captionStrong : Theme.Font.secondaryStrong).lineLimit(1)
                    if let time { Text(Date(timeIntervalSince1970: time / 1000).formatted(date: .abbreviated, time: .shortened)).font(Theme.Font.label).foregroundStyle(.secondary) }
                }
            }
        }
        .buttonStyle(.plain)
    }

    @ViewBuilder private func images(_ imgs: [LarkMomentsPost.Image]) -> some View {
        let urls = imgs.compactMap(\.url)
        if urls.count == 1, let img = imgs.first, let u = img.url {
            let size = LarkImagePart.displaySize(width: img.width ?? 0, height: img.height ?? 0, maxW: 300, maxH: 360)
            LarkRemoteImage(url: media(u), maxPixel: 1200).frame(width: size.width, height: size.height)
                .clipShape(.rect(cornerRadius: Theme.Radius.control, style: .continuous)).onTapGesture { gallery = 0 }
        } else if !urls.isEmpty {
            LazyVGrid(columns: Array(repeating: GridItem(.flexible(), spacing: 4), count: 3), spacing: 4) {
                ForEach(Array(urls.enumerated()), id: \.offset) { i, u in
                    Color.clear.aspectRatio(1, contentMode: .fit)
                        .overlay { LarkRemoteImage(url: media(u), maxPixel: 600) }
                        .clipShape(.rect(cornerRadius: Theme.Radius.badge, style: .continuous))
                        .onTapGesture { gallery = i }
                }
            }
        }
    }

    /// Post text: plain runs and Feishu emoji.
    static func runs(_ content: LarkMomentsPost.Content?) -> [LarkInline] {
        guard let segs = content?.segments, !segs.isEmpty else { return [.text(content?.text ?? "", LarkRunStyle())] }
        return segs.map { s in
            if s.kind == "emoji", let key = s.key ?? s.emojiKey { return .emoji(key: key, fallback: s.text ?? "[\(key)]") }
            return .text(s.text ?? "", LarkRunStyle())
        }
    }
}
