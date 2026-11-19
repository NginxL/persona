import SwiftUI

/// A thread: its first message, then the replies. Opens on the latest page; older pages come with Load earlier replies
/// (like the chat's Load Earlier Messages), and a change in the chat while it is open brings the newest replies in.
/// The box at the bottom replies in the thread: the reply shows at once, goes out in the background, can be sent
/// again when it fails, and the newest page comes in once it is sent.
struct LarkThreadView: View {
    @Environment(AppModel.self) private var model
    @Environment(\.dismiss) private var dismiss
    @Environment(\.larkAccount) private var account
    @Environment(\.larkManager) private var manager
    @Environment(\.larkGroup) private var group
    var root: LarkMessage
    var isGroup = true
    var feedID: String? = nil
    /// Opened by Reply on a topic group's thread: the box is focused.
    var focusBox = false
    var pinTarget: LarkChatPinTarget? = nil
    var pinAPI: APIClient? = nil
    private var api: APIClient? { pinAPI ?? model.larkAPI(group: group, manager: manager, account: account) }
    /// Opened from a topic group's card: Topic Details, From <the group> (the Lark gateway's topic page).
    var chatName: String? = nil
    @State private var person: LarkPersonRef?
    /// The replies loaded so far (never the root) and where paging stands (LarkPager).
    @State private var pager = LarkPager()
    @State private var loading = true
    /// The first follower start relies on the initial load; later starts catch up once (#10337).
    @State private var followed = false
    @State private var loadingOlder = false
    /// Why the replies did not load: the replies' placeholder while none are shown, a line over them otherwise
    /// (LarkLoadProblem, as on the chat page).
    @State private var problem: LarkLoadProblem?
    @State private var fileRequest: (url: String, name: String)?
    /// What is typed in the box, kept as the thread's draft (LarkDrafts) until it is sent.
    @State private var text = ""
    /// Picked with the @ picker since the last send.
    @State private var mentions: [LarkMention] = []
    @FocusState private var boxFocused: Bool
    /// Replies on their way (LarkOutgoing, `thread` true), under the loaded ones until their copies arrive.
    @State private var outgoing: [LarkOutgoing] = []
    @State private var sendChain: Task<Void, Never>?
    /// Pictures and files picked for the box: sent as replies in this thread through the chat's durable queue (#1035).
    @State private var attachments: [LarkAttachment] = []
    @State private var showPhotos = false
    @State private var showFiles = false
    @State private var preparingAttachment = false
    @State private var checkingQueued: Set<UUID> = []
    @Environment(\.larkChatOutbox) private var chatOutbox
    @State private var position = ScrollPosition()
    /// The list stays at its newest reply as it changes (larkFollowsEnd): after sending, or once scrolled to the end.
    @State private var following = false
    @State private var usedPinTarget = false
    @State private var flash: QuoteFlashMark?

    private var replies: [LarkMessage] { pager.messages }
    /// Copies of mine that took over their outgoing rows: message id → row id, while the sheet is open (LarkChatRows).
    @State private var adopted: [String: String] = [:]
    /// Feishu's copies of mine on their way that it already has: outgoing id → message id (LarkOutbox.copies).
    /// Mine on their way here, by when they were written: text replies (`outgoing`) and the queued pictures and files.
    private var mineOnTheWay: [LarkOutgoing] { (outgoing + queued).sorted { $0.written < $1.written } }
    private var copies: [UUID: String] { LarkOutbox.copies(mineOnTheWay, messages: pager.messages) }

    private var react: (LarkMessage, String) -> Void {
        { m, key in
            guard let i = pager.messages.firstIndex(where: { $0.id == m.id }) else { return }
            let (next, remove) = m.togglingReaction(key, me: "我") // i18n-ignore
            pager.messages[i] = next
            Task { do { try await api?.larkReact(m.id, key: key, remove: remove) } catch { model.show(error) } }
        }
    }

    var body: some View {
        NavigationStack {
            ScrollView {
                LazyVStack(alignment: .leading, spacing: 0) {
                    LarkTimeSeparator(date: root.createdAt)
                    LarkMessageRow(message: root, grouped: false, isGroup: isGroup, feedID: feedID, inThread: true)
                        .overlay { if flash?.line == root.id { QuoteFlash().id(flash?.id) } }
                        .id(root.id)
                    HStack {
                        Text(loading ? String(localized: "Loading replies…") : hasReplies || problem == nil ? String(localized: "\(replies.count) replies") : String(localized: "Reply"))
                            .font(Theme.Font.labelMedium).foregroundStyle(.secondary)
                        Rectangle().fill(Color.primary.opacity(0.08)).frame(height: Theme.Size.line)
                    }
                    .padding(.vertical, Theme.Space.m)
                    if let state = LarkChatPlaceholder.of(hasContent: hasReplies, loading: loading, problem: problem) {
                        placeholder(state)
                    } else if let line = problem?.inline {
                        Label(line, systemImage: Theme.Icon.error).font(Theme.Font.label).foregroundStyle(Theme.danger)
                    }
                    if pager.hasOlder {
                        LarkLoadOlderButton(title: String(localized: "Load earlier replies"), loading: loadingOlder) { Task { await loadOlder() } }
                    }
                    // Mine on their way are in the same list, where they were written (LarkChatRows).
                    // Pictures and files wait in the chat's outbox (`queued`), text here; both by when they were written.
                    let queuedIDs = Set(queued.map(\.id))
                    let rows = LarkChatRows.rows(messages: replies, outgoing: mineOnTheWay, like: (replies + [root]).last(where: \.isMine),
                                                 copies: copies, adopted: adopted)
                    ForEach(Array(rows.enumerated()), id: \.element.id) { i, row in
                        let m = row.message
                        // The first loaded reply's time counts from the root's (under the "N replies" line); it does not
                        // join the root, one of mine on its way can.
                        let prev = i > 0 ? rows[i - 1].message : root
                        let grouped = (i > 0 || row.outgoing != nil) && LarkChatLayout.continues(m, after: prev)
                        if LarkChatLayout.showsTimeSeparator(m, after: prev) { LarkTimeSeparator(date: m.createdAt) }
                        // One view either way: when Feishu's copy takes the row over, only what it shows changes.
                        LarkMessageRow(message: m, grouped: grouped, isGroup: isGroup, feedID: row.outgoing == nil ? feedID : nil,
                                       inThread: true,
                                       sending: row.outgoing.map { o in
                                           queuedIDs.contains(o.id)
                                               ? LarkSendMark(state: o.state, retry: { resendQueued(o.id) }, edit: { putBack(o) },
                                                              discard: { discardQueued(o.id) },
                                                              canRetry: o.canRetry, canRemoveSent: o.attachment != nil && o.messageID == nil)
                                               : LarkSendMark(state: o.state, retry: { resend(o.id) },
                                                              edit: {
                                                                  discard(o.id)
                                                                  text = text.isEmpty ? o.text : text + "\n" + o.text
                                                                  mentions += o.mentions
                                                              },
                                                              discard: { discard(o.id) },
                                                              canRetry: o.canRetry, canRemoveSent: o.attachment != nil && o.messageID == nil)
                                       },
                                       localAttachment: row.outgoing?.attachment)
                            .environment(\.larkReact, row.outgoing == nil ? react : nil) // not in Feishu yet: nothing to react to
                            .padding(.top, LarkChatLayout.gap(grouped: grouped))
                            .overlay { if flash?.line == m.id { QuoteFlash().id(flash?.id) } }
                            .id(row.id)
                    }
                }
                .padding(Theme.Space.m)
            }
            .scrollPosition($position)
            .larkFollowsEnd($position, following: $following, active: !loading)
            .safeAreaInset(edge: .bottom) {
                VStack(alignment: .leading, spacing: Theme.Space.s) {
                    if !attachments.isEmpty { LarkDraftAttachments(attachments: $attachments) }
                    if preparingAttachment {
                        HStack(spacing: Theme.Space.s) {
                            ProgressView().controlSize(.small)
                            Text("Preparing attachments…").font(Theme.Font.label).foregroundStyle(.secondary)
                        }
                        .padding(.horizontal, Theme.Space.m)
                    }
                    LarkComposer(placeholder: String(localized: "Reply to topic"), text: $text, focused: $boxFocused, boxID: "lark.thread.box", sendID: "lark.thread.send",
                                 attachmentPicker: canAttach ? { photo in if photo { showPhotos = true } else { showFiles = true } } : nil,
                                 canAddAttachments: attachments.count < LarkAttachment.selectionLimit,
                                 hasAttachments: !attachments.isEmpty, preparingAttachment: preparingAttachment,
                                 mentionFeed: isGroup ? feedID : nil, me: (replies + [root]).last(where: \.isMine)?.senderID,
                                 mentions: $mentions, send: send)
                }
                // Picked attachments stand on the page, not over the replies (as in the chat's box).
                .background {
                    if !attachments.isEmpty || preparingAttachment { Theme.pageBackground.ignoresSafeArea(edges: .bottom) }
                }
            }
            .modifier(LarkAttachmentPicker(attachments: $attachments, photos: $showPhotos, files: $showFiles, preparing: $preparingAttachment))
            .onChange(of: pager.messages) { _, now in
                settleOutgoing(now)
                // The chat's own checks do not see thread replies: settle this thread's queued ones here.
                if let chatOutbox, !queued.isEmpty { Task { try? await chatOutbox.settle(now) } }
            }
            .onChange(of: copies) { _, now in adopted = LarkChatRows.adopting(now, into: adopted) }
            .onAppear {
                guard text.isEmpty else { return }
                let draft = LarkDrafts.shared[.thread(root.id, manager: manager)]
                text = draft.text
                mentions = draft.mentions ?? []
            }
            .onChange(of: LarkDraft(text: text, mentions: mentions.isEmpty ? nil : mentions)) { _, draft in
                LarkDrafts.shared.set(draft, for: .thread(root.id, manager: manager))
            }
            .onDisappear { LarkDrafts.shared.flush() }
            .environment(\.larkReply, LarkReplyHandler(inThread: true) { _ in boxFocused = true })
            .environment(\.larkOpenFile, { url, name in fileRequest = (url, name) })
            .environment(\.larkOpenProfile, { person = $0 })
            .environment(\.larkReact, react)
            .modifier(LarkFileOpener(request: $fileRequest))
            .sheet(item: $person) { LarkProfileView(person: $0) }
            .modifier(LarkThreadTitle(chatName: chatName))
            .navigationBarTitleDisplayMode(.inline)
            .pageToolbar { SheetClose { dismiss() } }
            .task {
                if focusBox {
                    // Once the sheet is up: focusing while it slides in is dropped.
                    Task {
                        try? await Task.sleep(for: .milliseconds(350))
                        boxFocused = true
                    }
                }
                await load()
                #if DEBUG
                if canAttach, attachments.isEmpty { attachments = await LarkAttachmentFixture.loadForThread(config: api?.config) }
                #endif
            }
            // A reply is a change in its chat: follow only in front, and catch up once after the first start (#10337).
            .tabActiveTask(id: feedID) {
                guard let feedID else { return }
                let changes = LarkSync.shared.changes(of: LarkConversation.key(feedID, account: account))
                if followed {
                    await refreshLatest()
                    guard !Task.isCancelled else { return }
                }
                followed = true
                for await _ in changes {
                    await refreshLatest()
                    guard !Task.isCancelled else { return }
                }
            }
        }
    }

    /// Replies, or mine on their way: a problem is then one line over them rather than the placeholder.
    private var hasReplies: Bool { !replies.isEmpty || !outgoing.isEmpty || !queued.isEmpty }

    /// Pictures and files can go into a thread when the hub carries a thread placement for them and the chat's queue,
    /// bound to its Feishu account, is there to keep them (a thread opened outside a chat has none).
    private var canAttach: Bool {
        model.larkFeatures.attachmentReplies && feedID != nil && chatOutbox?.ready == true && chatOutbox?.identity != nil
    }

    /// This thread's pictures and files on their way (in the chat's queue).
    private var queued: [LarkOutgoing] { chatOutbox?.items.filter { $0.thread && $0.threadRoot == root.id } ?? [] }

    /// Under the "N replies" line while there are no replies to show: loading, none yet, or what went wrong with Retry
    /// (the chat page's states; BeautifulUI draws its own).
    @ViewBuilder private func placeholder(_ state: LarkChatPlaceholder) -> some View {
        switch state {
        case .loading:
            LoadableContent(state: Loadable<Void>.loading, retry: nil) { _ in EmptyView() }
                .padding(.vertical, Theme.Space.xl)
        case .empty:
            EmptyStateView(symbol: Theme.Icon.conversation, title: String(localized: "No replies yet"), message: String(localized: "Reply below to start this topic."))
        case .failed(let p):
            EmptyStateView(symbol: p.symbol, title: p.title, message: p.message, action: (title: String(localized: "Retry [Components b8784c]"), run: retry))
        }
    }

    private func load() async {
        defer { loading = false }
        if let target = pinTarget, !usedPinTarget {
            usedPinTarget = true
            pager.replace(with: target.page.replies(of: root.id))
            loading = false
            try? await Task.sleep(for: .milliseconds(80))
            guard !Task.isCancelled else { return }
            position.scrollTo(id: target.id, anchor: .center)
            flash = QuoteFlashMark(line: target.id)
            return
        }
        guard let api = api else { return }
        do {
            pager.replace(with: try await api.larkThread(root.id).replies(of: root.id))
            problem = nil
        } catch {
            problem = LarkLoadProblem.of(error, subject: String(localized: "this topic"))
        }
    }

    private func retry() {
        problem = nil
        loading = true
        Task { await load() }
    }

    private func loadOlder() async {
        guard let api = api, let cursor = pager.olderRequest, pager.hasOlder, !loadingOlder else { return }
        loadingOlder = true
        defer { loadingOlder = false }
        guard let r = try? await api.larkThread(root.id, before: cursor) else { return }
        pager.prepend(r.replies(of: root.id))
    }

    // MARK: Sending

    private func send() {
        let t = text.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !t.isEmpty || !attachments.isEmpty else { return }
        if !attachments.isEmpty { sendAttachments() }
        guard !t.isEmpty else { return }
        let o = LarkOutgoing(text: t, replyTo: LarkReply.quote(root), thread: true, mentions: LarkMentions.mentions(in: t, picked: mentions))
        // One animation for the box shrinking back to a line and the row coming in: the list's bottom inset moves with
        // the row, so the list does not drop first and then rise.
        withAnimation(Theme.Motion.standard) {
            text = ""
            mentions = []
            outgoing.append(o)
        }
        Haptics.tap()
        deliver(o.id)
        following = true // the reply shows right above the box (larkFollowsEnd)
    }

    /// The picked pictures and files go into the chat's durable queue as replies in this thread (#1035): they survive a
    /// restart, keep their place and account, and are checked before any retry.
    private func sendAttachments() {
        guard canAttach, let queue = chatOutbox, let api, let feed = feedID else {
            model.toast = Toast(String(localized: "Update the Lark extension on the hub to send images and files in topics."), style: .error); return
        }
        let picked = attachments
        let batch = picked.map { LarkOutgoing(text: "", replyTo: LarkReply.quote(root), thread: true, threadRoot: root.id, attachment: $0) }
        withAnimation(Theme.Motion.standard) { attachments = [] }
        Task {
            do { try await queue.enqueue(batch) } catch {
                attachments = picked + attachments
                model.toast = Toast(String(localized: "Attachment not sent: couldn't save it on this device. Check storage."), style: .error); return
            }
            Haptics.tap()
            for item in batch { queue.deliver(item.id, using: api, feed: feed) { await refreshLatest() } }
            following = true
        }
    }

    /// Resend a queued picture or file: the thread's newest replies first, so one that went out is not sent twice.
    private func resendQueued(_ id: UUID) {
        guard !checkingQueued.contains(id), let queue = chatOutbox, let api, let feed = feedID else { return }
        checkingQueued.insert(id)
        Task {
            defer { checkingQueued.remove(id) }
            do {
                try await queue.checkAccount(using: api)
                try await queue.settle(try await api.larkThread(root.id).messages)
                guard let item = queue.items.first(where: { $0.id == id }) else { return }
                guard item.canRetry else {
                    model.toast = Toast(String(localized: "Not yet confirmed whether the file was sent. Check again later."), style: .error); return
                }
                try await queue.retry(id)
                queue.deliver(id, using: api, feed: feed) { await refreshLatest() }
            } catch { model.toast = Toast(String(localized: "Can't retry right now. Check the connection and this device's storage."), style: .error) }
        }
    }

    private func discardQueued(_ id: UUID) {
        Task {
            do { try await chatOutbox?.discard(id) }
            catch { model.toast = Toast(String(localized: "Couldn't delete. Check this device's storage."), style: .error) }
        }
    }

    /// Back to Composer: the picture or file back among the picked ones, out of the queue.
    private func putBack(_ o: LarkOutgoing) {
        guard let attachment = o.attachment, attachments.count < LarkAttachment.selectionLimit else { return }
        Task {
            do {
                try await chatOutbox?.discard(o.id)
                attachments.append(attachment)
            } catch { model.toast = Toast(String(localized: "Couldn't move it back. Check this device's storage."), style: .error) }
        }
    }

    private func resend(_ id: UUID) {
        guard let i = outgoing.firstIndex(where: { $0.id == id }) else { return }
        outgoing[i].retry()
        deliver(id)
    }

    private func discard(_ id: UUID) {
        withAnimation(Theme.Motion.standard) { outgoing.removeAll { $0.id == id } }
    }

    /// Replies go out in the order written: the chain holds only the send itself. Waiting for each one's copy runs on
    /// its own, so the next reply does not wait behind the one before for its refreshes and `copyWait` (#851: the
    /// second of three replies went out 24 s after it was sent), as the chat's outbox does.
    private func deliver(_ id: UUID) {
        let before = sendChain
        sendChain = Task {
            await before?.value // in the order written
            guard let api = api, let o = outgoing.first(where: { $0.id == id }), o.state == .sending else { return }
            do {
                try await LarkOutbox.deliver(o, feed: feedID ?? "", api: api)
                setState(id, .sent)
            } catch {
                setState(id, .failed(error.shown ?? error.localizedDescription))
                Haptics.error()
                return
            }
            Task { await awaitCopy() }
        }
    }

    /// The sent reply's copy replaces it (settleOutgoing) when the newest page brings it.
    private func awaitCopy() async {
        await refreshLatest()
        try? await Task.sleep(for: .seconds(2))
        await refreshLatest()
        try? await Task.sleep(for: .seconds(LarkOutbox.copyWait))
        settleOutgoing(pager.messages)
    }

    private func setState(_ id: UUID, _ state: LarkOutgoing.State) {
        guard let i = outgoing.firstIndex(where: { $0.id == id }) else { return }
        outgoing[i].state = state
        settleOutgoing(pager.messages)
    }

    private func settleOutgoing(_ messages: [LarkMessage]) {
        // The copies keep their outgoing rows' ids before those go (in the same update, so onChange(of: copies) may
        // never see the pair).
        adopted = LarkChatRows.adopting(LarkOutbox.copies(mineOnTheWay, messages: messages), into: adopted)
        let left = LarkOutbox.remaining(outgoing, messages: messages)
        if left != outgoing { outgoing = left }
    }

    private func refreshLatest() async {
        guard !Task.isCancelled, !loading, let api = api,
              let r = try? await api.larkThread(root.id), !Task.isCancelled else { return }
        problem = nil // it answers again (the Lark gateway signed in again, the network is back)
        let fresh = pager.refresh(with: r.replies(of: root.id))
        guard !fresh.isEmpty else { return }
        withAnimation(Theme.Motion.standard) { pager.add(fresh) }
    }
}

/// Topic for a thread in a chat; a topic group's topic is Topic Details with the group it is from under it.
private struct LarkThreadTitle: ViewModifier {
    var chatName: String?

    func body(content: Content) -> some View {
        if let chatName {
            content.navigationTitle("Topic Details").navigationSubtitleWhenSupported(String(localized: "From \(chatName)"))
        } else {
            content.navigationTitle("Topic")
        }
    }
}

extension APIClient.LarkMessages {
    /// A thread page without its root (the sheet shows the root above the replies).
    func replies(of rootID: String) -> Self {
        var r = self
        r.messages.removeAll { $0.id == rootID }
        return r
    }
}
