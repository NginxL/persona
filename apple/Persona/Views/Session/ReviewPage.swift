import SwiftUI

// Review an Agent's changes (#151, docs/web/review.md「Web: the Review panel」, the approved iOS plan): every changed file's
// diff in one page, comments on picked lines kept as drafts under them, sent as one message of `Code comment N`
// blocks and a summary. iOS only: the Mac app is frozen (docs/apple/mac.md).

#if os(iOS)
/// The review page asked for: from ⋯ (the scope the session last used) or from the Git pane (Uncommitted).
struct ReviewRequest: Identifiable {
    let id = UUID()
    var scope: GitReviewScope?
}

struct ReviewPage: View {
    @Environment(AppModel.self) private var model
    @Environment(\.dismiss) private var dismiss
    @State private var review: ReviewModel
    @State private var note = ""
    @State private var finishing = false
    @State private var summary = ""
    @State private var confirmDiscard = false
    /// Files the reader folded, and generated or big ones they opened.
    @State private var folded: Set<String> = []
    @State private var opened: Set<String> = []
    @State private var pickingCommit = false

    init(key: SessionKey, cwd: String, scope: GitReviewScope?, api: @escaping () -> APIClient?) {
        _review = State(initialValue: ReviewModel(key: key, cwd: cwd, scope: scope, api: api))
    }

    /// The chat takes no messages: the review is read-only.
    private var canSend: Bool { model.session(review.key)?.canSend ?? true }

    var body: some View {
        NavigationStack {
            ScrollView {
                LazyVStack(alignment: .leading, spacing: Theme.Space.flush) {
                    scopeBar
                    if review.stale { staleBar }
                    content
                }
            }
            .themedPage()
            .navigationTitle(Text("Review Changes"))
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                SheetClose(id: "review.close") { dismiss() }
                ToolbarItem(placement: .principal) { title }
            }
            .safeAreaInset(edge: .bottom) { bottomBar }
        }
        .task { await review.load() }
        .fsLive(group: review.key.group, machineID: review.key.machineID, cwd: review.cwd, paths: [], git: true) { change in
            if change.git, review.review != nil { review.stale = true }
        }
        .sheet(isPresented: $finishing) { finishSheet }
        .accessibleConfirmationDialog(Text("Delete the review's \(review.drafts.count) comments?"), isPresented: $confirmDiscard, titleVisibility: .visible) {
            Button(role: .destructive) {
                review.drafts = []
                finishing = false
            } label: { Text("Discard Drafts").lineLimit(1) }
        }
    }

    // MARK: Top

    private var title: some View {
        VStack(spacing: Theme.Space.flush) {
            Text("Review Changes").font(Theme.Font.labelStrong).lineLimit(1)
            if let r = review.review {
                Text("\(r.files.count) files").font(Theme.Font.caption).foregroundStyle(.secondary).lineLimit(1)
            }
        }
    }

    private enum Segment: Hashable { case uncommitted, branch, since }

    private var segment: Binding<Segment> {
        Binding(get: {
            switch review.scope {
            case .uncommitted: .uncommitted
            case .branch: .branch
            case .commit: .since
            }
        }, set: { s in
            switch s {
            case .uncommitted: Task { await review.select(.uncommitted) }
            case .branch: Task { await review.select(.branch) }
            case .since: pickingCommit = true
            }
        })
    }

    private var scopeBar: some View {
        VStack(alignment: .leading, spacing: Theme.Space.s) {
            Picker(selection: segment) {
                Text("Uncommitted").tag(Segment.uncommitted)
                Text("Branch [Review]").tag(Segment.branch)
                Text("Since a Commit").tag(Segment.since)
            } label: { Text("What to Review") }
            .pickerStyle(.segmented)
            .accessibilityIdentifier("review.scope")
            if let line = against {
                Text(line).font(Theme.Font.caption).foregroundStyle(.secondary).lineLimit(2)
            }
        }
        .padding(.horizontal, Theme.Space.page)
        .padding(.vertical, Theme.Space.m)
        .sheet(isPresented: $pickingCommit) { commitPicker }
    }

    /// What the files on disk are compared with.
    private var against: String? {
        guard let r = review.review, let base = r.base else { return nil }
        let sha = base.short ?? String(base.hash.prefix(8))
        switch review.scope {
        case .uncommitted:
            return base.empty == true ? String(localized: "No commit yet: every file is new")
                : String(localized: "Files on disk against HEAD \(sha)")
        case .branch:
            return String(localized: "Files on disk against where it left \(base.ref ?? "") (\(sha))")
        case .commit:
            return String(localized: "Since \(sha) \(base.subject ?? "")")
        }
    }

    private var commitPicker: some View {
        NavigationStack {
            List(review.commits) { c in
                Button {
                    pickingCommit = false
                    Task { await review.select(.commit(c.hash)) }
                } label: { GitCommitRow(commit: c, isHead: false) }
                .buttonStyle(.plain)
            }
            .navigationTitle(Text("Pick a Commit"))
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                SheetClose { pickingCommit = false }
            }
            .overlay { commitsState }
        }
        .task { await review.loadCommits() }
        .presentationDetents([.medium, .large])
    }

    /// The commit list until it has rows: reading, why it can't be read (the web's toast), or no commit yet.
    @ViewBuilder private var commitsState: some View {
        if let why = review.commitsFailure {
            EmptyStateView(symbol: Theme.Icon.error, title: String(localized: "Couldn't read the commits"), message: why,
                           action: (String(localized: "Retry"), { Task { await review.loadCommits() } }), tint: Theme.caution)
        } else if !review.commitsRead {
            ProgressView()
        } else if review.commits.isEmpty {
            EmptyStateView(symbol: Theme.Icon.commit, title: String(localized: "No commits yet"), message: "")
        }
    }

    private var staleBar: some View {
        HStack(spacing: Theme.Space.s) {
            ThemedIcon(Theme.Icon.info).foregroundStyle(Theme.caution)
            Text("The files changed since this was read.").font(Theme.Font.secondary).lineLimit(2)
            Spacer(minLength: 0)
            Button { Task { await review.load() } } label: { Text("Refresh").lineLimit(1) }
                .themedButton()
                .accessibilityIdentifier("review.refresh")
        }
        .padding(Theme.Space.m)
        .surface(in: .rect(cornerRadius: Theme.Radius.card, style: .continuous))
        .padding(.horizontal, Theme.Space.page)
        .padding(.bottom, Theme.Space.s)
    }

    // MARK: States and files

    @ViewBuilder private var content: some View {
        switch review.phase {
        case .loading:
            ProgressView { Text("Reading the changes…") }
                .frame(maxWidth: .infinity)
                .padding(.top, Theme.Space.xxl)
        case .failed(let code, let message):
            failed(code, message)
        case .loaded(let r):
            if r.files.isEmpty {
                clean
            } else {
                if !canSend {
                    Text("This chat doesn't take messages, so comments can't be sent from here.")
                        .font(Theme.Font.caption).foregroundStyle(.secondary)
                        .padding(.horizontal, Theme.Space.page).padding(.bottom, Theme.Space.s)
                }
                ForEach(review.files) { f in file(f) }
                if r.truncated == true {
                    Text("Showing the first \(r.files.count) files.").font(Theme.Font.caption).foregroundStyle(.secondary)
                        .padding(Theme.Space.page)
                }
            }
        }
    }

    private var clean: some View {
        let (title, other): (String, (String, GitReviewScope)?) = switch review.scope {
        case .uncommitted: (String(localized: "No uncommitted changes"), (String(localized: "Review the Branch"), .branch))
        case .branch: (String(localized: "Nothing changed since this branch left \(review.review?.base?.ref ?? "")"),
                       (String(localized: "Review Uncommitted"), .uncommitted))
        case .commit: (String(localized: "Nothing changed since \(review.review?.base?.short ?? "")"), nil)
        }
        return EmptyStateView(symbol: Theme.Icon.success, title: title, message: "",
                              action: other.map { o in (o.0, { reselect(o.1) }) })
            .padding(.top, Theme.Space.xxl)
    }

    /// Each refusal in words of its own, as the web's Review panel says it (#8733): the folder named when it is the
    /// reason, the scope that still works offered, Retry only when trying again can help.
    private func failed(_ code: String?, _ message: String) -> some View {
        let title: String
        var detail = ""
        var offer: (String, GitReviewScope)?
        var retry = false
        switch code {
        case "not_git":
            title = String(localized: "This folder isn't a Git repository")
            detail = MentionRows.shortPath(review.cwd)
        case "no_folder":
            title = String(localized: "This folder is no longer there")
            detail = MentionRows.shortPath(review.cwd)
        case "node_outdated": title = String(localized: "Update this computer to review changes")
        case "no_default_branch":
            title = String(localized: "No default branch to compare with")
            offer = (String(localized: "Review Uncommitted"), .uncommitted)
        case "no_merge_base":
            title = String(localized: "This branch has nothing in common with the default branch")
            offer = (String(localized: "Review Uncommitted"), .uncommitted)
        case "bad_base":
            title = String(localized: "That commit isn't in this repository")
            offer = (String(localized: "Review Uncommitted"), .uncommitted)
        default:
            title = String(localized: "Couldn't read the changes")
            detail = message
            retry = true
        }
        var action: (title: String, run: () -> Void)?
        if let offer {
            action = (offer.0, { reselect(offer.1) })
        } else if retry {
            action = (String(localized: "Retry"), { reload() })
        }
        return EmptyStateView(symbol: Theme.Icon.error, title: title, message: detail, action: action, tint: Theme.caution)
            .padding(.top, Theme.Space.xxl)
    }

    private func reselect(_ s: GitReviewScope) { Task { await review.select(s) } }
    private func reload() { Task { await review.load() } }

    /// A file: its header (a tap folds it), then what can be shown of it.
    @ViewBuilder private func file(_ f: GitReview.File) -> some View {
        let quiet = f.generated == true || f.big == true
        let isFolded = quiet ? !opened.contains(f.path) : folded.contains(f.path)
        CodeDiffFileHeader(path: f.path, oldPath: f.oldPath, status: f.status, additions: f.added, deletions: f.deleted, folded: isFolded) {
            withAnimation(Theme.Motion.state) {
                if quiet { opened.formSymmetricDifference([f.path]) } else { folded.formSymmetricDifference([f.path]) }
            }
        }
        .accessibilityIdentifier("review.file")
        if f.refused == true {
            note(String(localized: "Not shown: this file holds secrets."))
        } else if f.binary == true {
            note(String(localized: "binary"))
        } else if isFolded {
            if f.generated == true { note(String(localized: "Generated file, folded")) }
        } else if f.big == true, review.loadedBig[f.path] == nil {
            Button { Task { await review.loadBig(f.path) } } label: { Text("Load Diff").lineLimit(1) }
                .themedButton()
                .padding(.horizontal, Theme.Space.page).padding(.vertical, Theme.Space.s)
        } else if (f.hunks ?? []).isEmpty {
            note(f.status == "added" ? String(localized: "Empty file") : String(localized: "Only its file mode changed"))
        } else {
            lines(f)
        }
    }

    private func note(_ text: String) -> some View {
        Text(text).font(Theme.Font.caption).foregroundStyle(.secondary)
            .padding(.horizontal, Theme.Space.page).padding(.vertical, Theme.Space.s)
    }

    @ViewBuilder private func lines(_ f: GitReview.File) -> some View {
        let rows = ReviewLogic.rows(f)
        let digits = String(rows.compactMap { r -> Int? in if case .line(let l) = r.kind { return max(l.old ?? 0, l.new ?? 0) } else { return nil } }.max() ?? 0).count
        let language = SyntaxLanguage.detect(name: f.path)
        let drafts = review.drafts.filter { $0.path == f.path }
        ForEach(Array(rows.enumerated()), id: \.offset) { _, row in
            switch row.kind {
            case .hunk(let header):
                CodeHunkHeader(header: header, expand: nil)
            case .line(let l):
                let picked = review.picking?.path == f.path && review.picking!.range.contains(row.index)
                CodeDiffRow(line: l, language: language, digits: digits, comment: canSend ? {
                    Haptics.tap()
                    withAnimation(Theme.Motion.state) { review.tap(f.path, row.index) }
                } : nil)
                .overlay { if picked { Rectangle().fill(Theme.accent.opacity(Theme.Quote.fillOpacity)).allowsHitTesting(false) } }
                .accessibilityIdentifier("review.line")
                ForEach(drafts.filter { endsAt($0, l) }) { d in draftCard(d) }
                // The comment being written sits under the last picked line, as a reviewer writes one on GitHub.
                if let p = review.picking, p.path == f.path, row.index == p.range.upperBound { composer }
            }
        }
    }

    /// A draft is drawn after its last line.
    private func endsAt(_ d: ReviewDraft, _ l: CodeDiffLine) -> Bool {
        d.old ? (l.kind == .del && l.old == d.to) : (l.kind != .del && l.new == d.to)
    }

    private func draftCard(_ d: ReviewDraft) -> some View {
        let outdated = review.diff.map { ReviewLogic.outdated(d, now: $0) } ?? false
        return VStack(alignment: .leading, spacing: Theme.Space.xs) {
            HStack(spacing: Theme.Space.s) {
                Chip(text: outdated ? String(localized: "Outdated") : String(localized: "Draft"), symbol: Theme.Icon.edit,
                     look: outdated ? .tint(Theme.caution) : .neutral)
                Text(verbatim: "\(d.path):\(MessageComments.range(d.from, d.to))").font(Theme.Font.code).foregroundStyle(.secondary)
                    .lineLimit(1).truncationMode(.middle)
                Spacer(minLength: 0)
                Button(role: .destructive) { withAnimation(Theme.Motion.state) { review.remove(d) } } label: { Text("Delete").lineLimit(1) }
                    .themedButton()
                    .accessibilityIdentifier("review.draft.delete")
            }
            Text(d.note).font(Theme.Font.body).frame(maxWidth: .infinity, alignment: .leading)
        }
        .padding(Theme.Space.m)
        .surface(in: .rect(cornerRadius: Theme.Radius.card, style: .continuous))
        .padding(.horizontal, Theme.Space.s).padding(.vertical, Theme.Space.xs)
        .accessibilityElement(children: .contain)
        .accessibilityIdentifier("review.draft")
    }

    // MARK: Bottom

    @ViewBuilder private var bottomBar: some View {
        if review.picking == nil, !review.drafts.isEmpty {
            HStack(spacing: Theme.Space.m) {
                Text("\(review.drafts.count) review drafts").font(Theme.Font.labelStrong).lineLimit(1)
                Spacer(minLength: 0)
                Button { finishing = true } label: { Text("Finish Review").lineLimit(1) }
                    .themedButton(prominent: true)
                    .disabled(!canSend)
                    .accessibilityIdentifier("review.finish")
            }
            .padding(Theme.Space.m)
            .surface(in: .rect(cornerRadius: Theme.Radius.panel, style: .continuous))
            .padding(.horizontal, Theme.Space.page).padding(.bottom, Theme.Space.s)
        }
    }

    /// The comment on the picked lines, under them: which lines (another tap on a number moves the range's end), the
    /// note, Suggest Change, and into the review (a draft) or straight to the message box as a card.
    private var composer: some View {
        let d = review.draft(note: "")
        return VStack(alignment: .leading, spacing: Theme.Space.s) {
            if let d {
                VStack(alignment: .leading, spacing: Theme.Space.flush) {
                    Text(d.from == d.to ? String(localized: "Comment on line \(d.from)") : String(localized: "Comment on lines \(d.from)–\(d.to)"))
                        .font(Theme.Font.labelStrong).lineLimit(1)
                    Text("Tap another line number to change the range").font(Theme.Font.caption).foregroundStyle(.secondary).lineLimit(2)
                }
            }
            CodeComposerView(placeholder: String(localized: "Leave a comment"), text: $note, buttons: commentButtons,
                             running: nil, identifier: "review.note")
            HStack(spacing: Theme.Space.s) {
                if let d, !d.old {
                    Button { note += suggestion(d) } label: { Label("Suggest Change", systemImage: Theme.Icon.edit).lineLimit(1) }
                        .themedButton()
                }
                Spacer(minLength: 0)
                Button { cancelComment() } label: { Text("Cancel").lineLimit(1) }
                    .themedButton()
                    .accessibilityIdentifier("review.cancel")
            }
        }
        .padding(Theme.Space.m)
        .surface(in: .rect(cornerRadius: Theme.Radius.card, style: .continuous))
        .padding(.horizontal, Theme.Space.s).padding(.vertical, Theme.Space.xs)
        .accessibilityElement(children: .contain)
        .accessibilityIdentifier("review.composer")
    }

    private func cancelComment() {
        withAnimation(Theme.Motion.state) { review.picking = nil }
        note = ""
    }

    private var commentButtons: [(title: String, tone: String?, id: String, run: () -> Void)] {
        let empty = note.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty
        guard !empty else { return [] }
        return [
            (review.drafts.isEmpty ? String(localized: "Start a Review") : String(localized: "Add to Review"), "primary", "review.add", {
                if let d = review.draft(note: note) { withAnimation(Theme.Motion.state) { review.add(d) } }
                note = ""
            }),
            (String(localized: "Comment Now"), nil, "review.now", {
                if let d = review.draft(note: note) {
                    model.composerComments[review.key, default: []].append(d.comment)
                    review.picking = nil
                }
                note = ""
                dismiss()
            }),
        ]
    }

    /// A suggestion block with the new side of the picked lines, to edit into what they should be.
    private func suggestion(_ d: ReviewDraft) -> String {
        let lines = d.code.filter { !$0.hasPrefix("-") }.map { String($0.dropFirst()) }
        return (note.isEmpty || note.hasSuffix("\n") ? "" : "\n") + "```suggestion\n" + lines.joined(separator: "\n") + "\n```\n"
    }

    /// Finish: the summary, then Send Review, Add to Message Box or Discard Drafts.
    private var finishSheet: some View {
        NavigationStack {
            VStack(alignment: .leading, spacing: Theme.Space.m) {
                CodeComposerView(placeholder: String(localized: "Leave a summary for the agent (optional)"), text: $summary,
                                 buttons: [], running: nil, identifier: "review.summary")
                Button { send() } label: { Text("Send Review").frame(maxWidth: .infinity).lineLimit(1) }
                    .themedButton(prominent: true)
                    .accessibilityIdentifier("review.send")
                Button { compose() } label: { Text("Add to Message Box").frame(maxWidth: .infinity).lineLimit(1) }
                    .themedButton()
                    .accessibilityIdentifier("review.compose")
                Button(role: .destructive) { confirmDiscard = true } label: { Text("Discard Drafts").frame(maxWidth: .infinity).lineLimit(1) }
                    .themedButton()
                Spacer(minLength: 0)
            }
            .padding(Theme.Space.page)
            .navigationTitle(Text("Finish Review"))
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                SheetClose { finishing = false }
            }
        }
        .presentationDetents([.medium, .large])
    }

    /// One message: the drafts as `Code comment N` blocks in order, then the summary. A failed send keeps it in the
    /// chat to resend, so the drafts go.
    private func send() {
        guard model.allowSend(review.key) else { return }
        let n = review.drafts.count
        model.sendNow(review.key, text: review.message(summary: summary))
        review.drafts = []
        summary = ""
        finishing = false
        model.toast = Toast(n == 1 ? String(localized: "Review sent with 1 comment") : String(localized: "Review sent with \(n) comments"))
        dismiss()
    }

    /// The drafts as cards over the message box and the summary in it, to edit first.
    private func compose() {
        let comments = ReviewLogic.ordered(review.drafts, files: review.files).map(\.comment)
        model.composerComments[review.key, default: []].append(contentsOf: comments)
        let s = summary.trimmingCharacters(in: .whitespacesAndNewlines)
        if !s.isEmpty { model.drafts[review.key] = [model.drafts[review.key] ?? "", s].filter { !$0.isEmpty }.joined(separator: "\n\n") }
        review.drafts = []
        summary = ""
        finishing = false
        dismiss()
    }
}
#endif
