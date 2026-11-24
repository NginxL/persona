import SwiftUI

// A domain space's home (#13500, docs/apple/ios.md "Domain spaces", the web's views/spaces/Space.tsx): a place of its own
// for one part of the user's life, like Health. It says what the space is, asks something to start a conversation in it,
// lists its past conversations, what its SPACE.md says (About this space) and where things stand, and the personal
// information it holds. Its conversations are the default manager's, in a directory of their own (`?space=<slug>`).
// Health keeps its own page (`HealthView`).

extension EnvironmentValues {
    /// What the Persona page draws under its top bar besides the bar (a space conversation's category chips, #13500):
    /// the conversation leaves that much more room at its top. 0 everywhere else.
    @Entry var assistantTopExtra: CGFloat = 0
}

/// Where a space's pages open over the Persona page: a space's home, or the new space form.
enum DomainSpacePage: Hashable, Identifiable {
    case home(String)
    case new

    var id: String {
        switch self {
        case .home(let slug): "home:" + slug
        case .new: "new"
        }
    }
}

struct SpaceHomeView: View {
    @Environment(AppModel.self) private var model
    let slug: String
    /// A conversation is being started (the Persona page shows it coming).
    var disabled: Bool
    @Binding var launcher: String?
    @Binding var launcherModel: String?
    @Binding var mode: String
    @Binding var effort: String
    /// Starts a conversation in the space with the message (`AssistantView.start(…, space:)`).
    var send: (String, [ImagePayload]) -> Void
    /// Back to Persona (the space archived, or not there).
    var leave: () -> Void
    @State private var page: SpaceHomeModel
    @State private var editing = false
    @State private var askArchive = false
    @State private var aboutOpen = false
    /// The written part is taller than the folded height, so it folds and Show more appears (as the web's FoldedMarkdown).
    @State private var aboutTall = false
    @Environment(\.dynamicTypeSize) private var typeSize
    @State private var link: LinkTarget?
    private var store: SpaceStore { SpaceStore.shared }

    init(slug: String, disabled: Bool, launcher: Binding<String?>, launcherModel: Binding<String?>, mode: Binding<String>,
         effort: Binding<String>, send: @escaping (String, [ImagePayload]) -> Void, leave: @escaping () -> Void) {
        self.slug = slug
        self.disabled = disabled
        _launcher = launcher
        _launcherModel = launcherModel
        _mode = mode
        _effort = effort
        self.send = send
        self.leave = leave
        _page = State(initialValue: SpaceHomeModel(slug: slug))
    }

    var body: some View {
        let track = store.track(slug)
        Group {
            if store.supported == false || (store.tracks != nil && (track == nil || track?.archived == true)) {
                gone(archived: track?.archived == true)
            } else {
                home(track)
            }
        }
        .background(Theme.groupedPage)
        .pageTitle(track?.name ?? "")
        .pageToolbar {
            ToolbarItem(placement: .topBarTrailing) {
                if let track, !track.builtin, !track.archived { actions(track) }
            }
        }
        .sheet(isPresented: $editing) {
            SpaceMdEditorSheet(slug: slug) { page.saved($0) }
        }
        .sheet(item: $link) { t in
            if let key = model.assistantMain ?? model.assistantCurrent { LinkSheet(target: t, key: key) }
        }
        .alert(String(localized: "Archive \(track?.name ?? "")?"), isPresented: $askArchive) {
            Button(String(localized: "Cancel [Spaces]"), role: .cancel) {}
            Button(String(localized: "Archive [Spaces]"), role: .destructive) {
                guard let track else { return }
                Task { if await page.archive(track, store: store) { leave() } }
            }
        } message: {
            Text("The space leaves the sidebar. Its conversations and files stay on the assistant's computer.")
        }
        .onChange(of: track?.name ?? "", initial: true) { _, name in page.name = name }
        .task { await page.load() }
        .refreshable { await page.load() }
    }

    // MARK: The page

    private func home(_ track: AssistantTrack?) -> some View {
        let name = track?.name ?? ""
        // The field under the list, not over it, as Health's.
        return VStack(spacing: 0) {
            sections(track, list: SpaceHomeModel.merge(page.fetched, live: model.spaceConversations, slug: slug))
                .themedPage()
            composer(name)
                .padding(.horizontal, Theme.Space.m)
                .padding(.vertical, Theme.Space.s)
                .frame(maxWidth: ChatView.readableWidth)
                .frame(maxWidth: .infinity)
                .background(Theme.groupedPage.ignoresSafeArea(edges: .bottom))
        }
        .keepsHeightUnderUndockedKeyboard()
    }

    @ViewBuilder private func composer(_ name: String) -> some View {
        let placeholder = String(localized: "Ask anything about \(name)")
        VStack(alignment: .leading, spacing: Theme.Space.xs) {
            if ChatComposerKind.padCard {
                // The default manager's launcher, as Health's new conversation (`ShellModes.health`).
                StarterComposer(disabled: disabled,
                                shell: StarterComposer.ShellModes(launcher: $launcher, launcherModel: $launcherModel, mode: $mode, effort: $effort, health: true),
                                placeholder: placeholder, space: slug, send: send)
            } else if AppStyle.current.usesBeautifulUI {
                BeautifulStarterComposer(disabled: disabled, launcher: $launcher, placeholder: placeholder, space: slug, send: send)
            } else {
                StarterComposer(disabled: disabled, placeholder: placeholder, space: slug, send: send)
            }
            // What stays private, in plain words, under the field (the web's privacy line).
            Label {
                Text("Everything here can be used inside this space. Anything about health, money and other personal matters stays out of lock-screen notifications, and Persona asks before sending it to anyone.")
            } icon: {
                ThemedIcon(Theme.Icon.locked)
            }
            .font(Theme.Font.label)
            .foregroundStyle(.secondary)
            .padding(.horizontal, Theme.Space.xs)
            .accessibilityIdentifier("space-privacy")
        }
    }

    private func sections(_ track: AssistantTrack?, list: [Session]) -> some View {
        List {
            Section { hero(track) }
                .listRowBackground(Color.clear)
                .listRowInsets(EdgeInsets())
            Section {
                if !list.isEmpty {
                    ForEach(list) { s in row(s, track: track) }
                } else if !page.loaded {
                    ProgressView().frame(maxWidth: .infinity)
                } else if let error = page.error {
                    Text("Couldn't load this space's conversations: \(error)").font(Theme.Font.secondary).foregroundStyle(.secondary)
                } else {
                    Text("No conversations yet. Ask something below to start the first one.").font(Theme.Font.secondary).foregroundStyle(.secondary)
                }
            } header: {
                Text("Conversations [Spaces]")
            }
            Section {
                about(track)
            } header: {
                Text("About this space")
            }
            Section {
                if let track, !track.defaultCategories.isEmpty {
                    FlowLayoutRow {
                        ForEach(track.defaultCategories, id: \.self) { c in Chip(text: InfoCategoryWords.name(c)) }
                    }
                    .padding(.vertical, Theme.Space.xxs)
                } else {
                    Text("None chosen").font(Theme.Font.secondary).foregroundStyle(.secondary)
                }
            } header: {
                Text("Personal information here")
            } footer: {
                Text("Everything made in this space is treated as holding these kinds of personal information, so it is used carefully outside the space.")
            }
        }
    }

    private func hero(_ track: AssistantTrack?) -> some View {
        VStack(spacing: Theme.Space.s) {
            ThemedIcon(SpaceIcon.symbol(of: track))
                .font(Theme.Font.title)
                .foregroundStyle(Theme.accent)
                .scaledFrame(width: Theme.Size.box56, height: Theme.Size.box56)
                .background(Theme.Fill.well, in: .circle)
                .accessibilityHidden(true)
            Text(track?.name ?? "").font(Theme.Font.title).lineLimit(1)
            if let about = track?.description, !about.isEmpty {
                Text(about)
                    .font(Theme.Font.secondary)
                    .foregroundStyle(.secondary)
                    .multilineTextAlignment(.center)
            }
        }
        .frame(maxWidth: .infinity)
        .padding(.vertical, Theme.Space.l)
    }

    /// What SPACE.md says: the parts the user wrote as formatted text, folded with Show more, then where things stand
    /// (Persona's state block). Persona's other kept blocks stay in the editor.
    @ViewBuilder private func about(_ track: AssistantTrack?) -> some View {
        switch page.spaceMd {
        case .loading:
            Text("Loading… [Spaces]").font(Theme.Font.secondary).foregroundStyle(.secondary)
        case .failed(let why):
            Text(why).font(Theme.Font.secondary).foregroundStyle(.secondary)
        case .loaded(let md):
            let written = page.written
            let fold = Theme.Size.box150 * Theme.sizeScale(typeSize)
            VStack(alignment: .leading, spacing: Theme.Space.s) {
                if written.isEmpty {
                    Text("Nothing written yet.").font(Theme.Font.secondary).foregroundStyle(.secondary)
                } else {
                    ExtensionMarkdown(text: written, link: $link)
                        .fixedSize(horizontal: false, vertical: true)
                        // its natural height, before the fold: only something taller than the fold gets Show more
                        .onGeometryChange(for: Bool.self) { $0.size.height > fold } action: { aboutTall = $0 }
                        .scaledFrame(maxHeight: aboutOpen || !aboutTall ? nil : Theme.Size.box150, alignment: .top)
                        .clipped()
                        .accessibilityIdentifier("space-md")
                }
                if !written.isEmpty && aboutTall {
                    Button {
                        withAnimation(Theme.Motion.standard) { aboutOpen.toggle() }
                    } label: {
                        Text(aboutOpen ? String(localized: "Show less [Spaces]") : String(localized: "Show more [Spaces]"))
                            .font(Theme.Font.labelStrong).lineLimit(1)
                    }
                    .buttonStyle(.plain)
                    .foregroundStyle(Theme.accent)
                    .accessibilityIdentifier("space-md-more")
                }
                let state = md.state.trimmingCharacters(in: .whitespacesAndNewlines)
                if !state.isEmpty {
                    Divider()
                    Text("Where things stand: \(state)")
                        .font(Theme.Font.label)
                        .foregroundStyle(.secondary)
                        .accessibilityIdentifier("space-state")
                }
            }
            .padding(.vertical, Theme.Space.xxs)
        }
    }

    private func row(_ s: Session, track: AssistantTrack?) -> some View {
        let title = s.title.isEmpty || s.title == "Claude Code" ? String(localized: "New Chat [Quick Actions ea4b29]") : s.title
        return Button { model.openAssistantConversation(s.key) } label: {
            HStack(spacing: Theme.Space.m) {
                ThemedIcon(SpaceIcon.symbol(of: track))
                    .font(Theme.Font.secondary)
                    .foregroundStyle(Theme.accent)
                    .scaledFrame(width: Theme.Size.well, height: Theme.Size.well)
                    .background(Theme.Fill.well, in: .circle)
                    .accessibilityHidden(true)
                VStack(alignment: .leading, spacing: Theme.Space.xxs) {
                    Text(title).font(Theme.Font.body).foregroundStyle(Color.primary).lineLimit(1)
                    if let last = s.lastText, !last.isEmpty {
                        Text(last).font(Theme.Font.secondary).foregroundStyle(.secondary).lineLimit(1)
                    }
                }
                Spacer(minLength: Theme.Space.s)
                if let at = s.updatedAt {
                    Text(Fmt.relative(at)).font(Theme.Font.label).foregroundStyle(.secondary).lineLimit(1)
                }
            }
            .contentShape(.rect)
        }
        .buttonStyle(.plain)
        .accessibilityIdentifier("space-conversation")
        .swipeActions(edge: .trailing, allowsFullSwipe: true) {
            Button { Task { await model.archiveConversation(s.key, true); await page.loadConversations() } } label: {
                Label(String(localized: "Archive [Spaces]"), systemImage: Theme.Icon.archive)
            }
        }
    }

    /// Edit and Archive, for a space the user made (a built-in one is the hub's).
    private func actions(_ track: AssistantTrack) -> some View {
        Menu {
            Button { editing = true } label: { Label(String(localized: "Edit About this space"), systemImage: Theme.Icon.edit) }
                .accessibilityIdentifier("space-md-edit")
            Button(role: .destructive) { askArchive = true } label: {
                Label(String(localized: "Archive [Spaces]"), systemImage: Theme.Icon.archive)
            }
            .disabled(page.archiving)
            .accessibilityIdentifier("space-archive")
        } label: {
            ThemedIcon(Theme.Icon.more)
        }
        .accessibilityLabel(String(localized: "Space actions"))
        .accessibilityIdentifier("space-actions")
    }

    /// The space is not on this assistant (removed, a mistyped link) or archived: it says so, with the way back.
    private func gone(archived: Bool) -> some View {
        EmptyStateView(symbol: archived ? Theme.Icon.archive : Theme.Icon.domainSpace,
                       title: archived ? String(localized: "This space is archived") : String(localized: "This space isn't here"),
                       message: archived ? String(localized: "It no longer shows in the sidebar. Its conversations and files are kept.")
                           : String(localized: "It may have been removed, or the address is mistyped."),
                       action: (title: String(localized: "Back to Persona"), run: leave))
            .padding(Theme.Space.page)
            .frame(maxWidth: .infinity, maxHeight: .infinity)
    }
}
