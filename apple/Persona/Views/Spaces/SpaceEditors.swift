import SwiftUI

// The domain spaces' editing pages (#13500, docs/apple/ios.md "Domain spaces"): a space's SPACE.md, the new space form,
// a space conversation's categories and what an extension may read of the user's personal information.

/// About this space (SPACE.md): what the user tells the assistant about the space. The parts the user wrote are fields
/// in the body font; the blocks Persona keeps up to date show read-only and go back unchanged.
struct SpaceMdEditorSheet: View {
    @Environment(\.dismiss) private var dismiss
    @State private var page: SpaceMdEditorModel
    var saved: (SpaceMd) -> Void

    init(slug: String, saved: @escaping (SpaceMd) -> Void) {
        _page = State(initialValue: SpaceMdEditorModel(slug: slug))
        self.saved = saved
    }

    var body: some View {
        NavigationStack {
            Form {
                Section {
                    Text("Tell the assistant what this space is for and how to help here. It reads this at the start of every conversation in the space.")
                        .font(Theme.Font.secondary)
                        .foregroundStyle(.secondary)
                }
                if let error = page.readError {
                    Section {
                        Text(error).font(Theme.Font.secondary)
                        Button { Task { await page.load() } } label: { Text("Retry [Spaces]").lineLimit(1) }
                    }
                } else if let shape = page.shape {
                    ForEach(shape.parts.indices, id: \.self) { i in part(shape, i) }
                } else {
                    Section { ProgressView().frame(maxWidth: .infinity) }
                }
                if let text = page.saveErrorText {
                    Section {
                        NoticeBanner(text: text, tone: .caution, lineLimit: nil)
                            .listRowInsets(EdgeInsets())
                            .listRowBackground(Color.clear)
                            .accessibilityIdentifier("space-md-error")
                        if page.saveError == .stale {
                            Button { Task { await page.load() } } label: { Text("Load latest").lineLimit(1) }
                                .accessibilityIdentifier("space-md-reload")
                        }
                    }
                }
            }
            .themedPage()
            .navigationTitle(String(localized: "About this space"))
            .navigationBarTitleDisplayMode(.inline)
            .sheetCancel(unsaved: page.changed) { dismiss() }
            .toolbar {
                SheetConfirm(id: "space-md-save", name: String(localized: "Save [Spaces]"), disabled: !page.changed, busy: page.saving) {
                    Task {
                        if let md = await page.save() {
                            saved(md)
                            Haptics.success()
                            dismiss()
                        }
                    }
                }
            }
        }
        .interactiveDismissDisabled(page.saving || page.changed)
        .task { await page.load() }
    }

    @ViewBuilder private func part(_ shape: SpaceMarkdown.Shape, _ i: Int) -> some View {
        switch shape.parts[i] {
        case .managed(_, _, let body):
            Section {
                Text(body.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty ? String(localized: "Nothing yet [Spaces]") : body.trimmingCharacters(in: .whitespacesAndNewlines))
                    .font(Theme.Font.secondary)
                    .foregroundStyle(.secondary)
                    .textSelection(.enabled)
            } header: {
                Label { Text("Kept up to date by Persona") } icon: { ThemedIcon(Theme.Icon.locked) }
            }
        case .human:
            if shape.editable.contains(i) {
                Section {
                    TextEditor(text: Binding(get: { page.text(i) }, set: { page.edits[i] = $0 }))
                        .font(Theme.Font.body)
                        .scaledFrame(minHeight: shape.editable.first == i ? Theme.Size.box150 : Theme.Size.box56)
                        .overlay(alignment: .topLeading) {
                            // what to write, while the first part is empty (the web's placeholder)
                            if shape.editable.first == i && page.text(i).isEmpty {
                                Text("For example: who is involved, what to keep track of, how you like answers.")
                                    .font(Theme.Font.body)
                                    .foregroundStyle(.secondary)
                                    .padding(.horizontal, Theme.Space.xs)
                                    .padding(.top, Theme.Space.s)
                                    .allowsHitTesting(false)
                                    .accessibilityHidden(true)
                            }
                        }
                        .accessibilityLabel(String(localized: "About this space"))
                        .accessibilityIdentifier("space-md-field")
                }
            }
        }
    }
}

/// New space: a name, a line about it, an icon, which kinds of personal information it holds and, when the assistant
/// offers more than one, its kind. Created, it opens.
struct NewSpaceView: View {
    @State private var page = NewSpaceModel()
    @FocusState private var naming: Bool
    /// Opens the space just made.
    var created: (AssistantTrack) -> Void
    private var store: SpaceStore { SpaceStore.shared }

    var body: some View {
        Form {
            Section {
                Text("A space keeps one part of your life together, like health keeps yours: its own conversations, its own notes, and its own memory.")
                    .font(Theme.Font.secondary)
                    .foregroundStyle(.secondary)
            }
            Section {
                TextField(String(localized: "For example: Family, Money, Home"), text: $page.title)
                    .focused($naming)
                    .accessibilityIdentifier("new-space-title")
            } header: {
                Text("Name [Spaces]")
            }
            Section {
                TextField(String(localized: "Optional [Spaces]"), text: $page.about)
                    .accessibilityIdentifier("new-space-about")
            } header: {
                Text("One line about it")
            }
            Section {
                iconGrid
            } header: {
                Text("Icon [Spaces]")
            }
            Section {
                ForEach(InfoCategory.all, id: \.self) { c in
                    Toggle(InfoCategoryWords.name(c), isOn: Binding(get: { page.categories.contains(c) }, set: { _ in page.toggle(c) }))
                        .accessibilityIdentifier("new-space-category-\(c)")
                }
            } header: {
                Text("Personal information it holds")
            } footer: {
                Text("Optional. Anything made here is then always handled with care outside the space.")
            }
            if page.kinds.count > 1 {
                Section {
                    Picker(String(localized: "Type [Spaces]"), selection: $page.kind) {
                        ForEach(page.kinds) { k in
                            Text(k.slug == Tracks.defaultKind ? String(localized: "Space [Spaces kind]") : (k.title.isEmpty ? k.slug : k.title)).tag(k.slug)
                        }
                    }
                }
            }
            if let error = page.error {
                Section {
                    NoticeBanner(text: error, tone: .danger, lineLimit: nil)
                        .listRowInsets(EdgeInsets())
                        .listRowBackground(Color.clear)
                }
            }
            Section {
                FormAction {
                    Button {
                        Task {
                            if let track = await page.create(taken: store.taken) {
                                store.note(track)
                                Haptics.success()
                                created(track)
                            }
                        }
                    } label: {
                        Group {
                            if page.busy { ProgressView() } else { Text("Create [Spaces]").lineLimit(1) }
                        }
                        .formActionWidth()
                    }
                    .themedButton(prominent: true)
                    .disabled(!page.ready)
                    .accessibilityIdentifier("new-space-create")
                }
            }
        }
        .themedPage()
        .pageTitle(String(localized: "New space"))
        .task { await page.loadKinds() }
        .onAppear { naming = true }
    }

    /// One button per icon, the picked one marked; each read by its name.
    private var iconGrid: some View {
        LazyVGrid(columns: [GridItem(.adaptive(minimum: Theme.Size.tapTarget), spacing: Theme.Space.s)], spacing: Theme.Space.s) {
            ForEach(SpaceIcon.tokens, id: \.self) { token in
                let picked = page.icon == token
                Button { page.icon = token } label: {
                    ThemedIcon(SpaceIcon.symbol(token))
                        .font(Theme.Font.heading)
                        .foregroundStyle(picked ? Theme.accent : Color.secondary)
                        .tapTargetFrame()
                        .background(picked ? Theme.Fill.chip : Theme.Fill.well, in: .rect(cornerRadius: Theme.Radius.card, style: .continuous))
                        .contentShape(.rect)
                }
                .buttonStyle(.plain)
                .accessibilityLabel(SpaceIcon.name(token))
                .accessibilityAddTraits(picked ? .isSelected : [])
                .accessibilityIdentifier("new-space-icon-\(token)")
            }
        }
        .padding(.vertical, Theme.Space.xs)
    }
}

/// A space conversation's information categories as chips under its top bar: a tap on one the user may take away asks
/// to remove it; the space's own ones carry a lock and stay; the ones taken away come back from +.
struct SpaceConversationCategories: View {
    @State private var page: ConversationCategoriesModel

    init(sessionID: String) {
        _page = State(initialValue: ConversationCategoriesModel(sessionID: sessionID))
    }

    var body: some View {
        // A stack, not a Group: it is there (and reads the categories) while it shows nothing.
        VStack(spacing: 0) {
            if let cats = page.categories, page.shows {
                ScrollView(.horizontal) {
                    HStack(spacing: Theme.Space.xs) {
                        ForEach(cats.categories, id: \.self) { c in chip(c) }
                        if !cats.removed.isEmpty {
                            Menu {
                                Section(String(localized: "Add back")) {
                                    ForEach(cats.removed, id: \.self) { c in
                                        Button(InfoCategoryWords.name(c)) { Task { await page.restore(c) } }
                                    }
                                }
                            } label: {
                                ThemedIcon(Theme.Icon.add)
                                    .font(Theme.Font.label)
                                    .foregroundStyle(.secondary)
                                    .tapTargetFrame()
                                    .contentShape(.rect)
                            }
                            .accessibilityLabel(String(localized: "Add back"))
                            .accessibilityIdentifier("space-category-restore")
                        }
                    }
                    .padding(.horizontal, Theme.Space.page)
                }
                .scrollIndicators(.hidden)
                .accessibilityIdentifier("conversation-categories")
            }
        }
        .task(id: page.sessionID) { await page.load() }
    }

    @ViewBuilder private func chip(_ c: String) -> some View {
        let name = InfoCategoryWords.name(c)
        if page.removable(c) {
            Menu {
                Button(role: .destructive) { Task { await page.remove(c) } } label: {
                    Label(String(localized: "Remove \(name)"), systemImage: Theme.Icon.close)
                }
            } label: {
                // the mark before the words, as every Chip draws its symbol (#6578); a finger's height to press
                Chip(text: name, symbol: Theme.Icon.close)
                    .tapTargetHeight()
                    .contentShape(.rect)
            }
            .disabled(page.busy != nil)
            .accessibilityLabel(name)
            .accessibilityHint(String(localized: "Remove \(name)"))
            .accessibilityIdentifier("space-category-\(c)")
        } else {
            Chip(text: name, symbol: Theme.Icon.locked)
                .accessibilityLabel(name)
                .accessibilityValue(String(localized: "Set by this space"))
                .accessibilityIdentifier("space-category-\(c)")
        }
    }
}

/// What an extension may read of the user's personal information, on its page: "This is my own extension" lets it read
/// everything; otherwise one switch per kind. Shown on the default manager's page only; read-only where the extension is
/// set outside the app; nothing on an assistant without grants. The page reads `page` (a section in a form draws nothing
/// while it has no rows, so it cannot read for itself).
struct ExtensionInfoAccessSection: View {
    let page: ExtensionTrustModel
    var editable: Bool

    /// Who may change it on the extension's page: not one set in a config file or under development (the web's rule).
    static func editable(_ d: HubExtensionDetail) -> Bool {
        !d.readOnly && !["dev", "hub.json", "manager.json"].contains(d.plugin.source ?? "")
    }

    var body: some View {
        Group {
            if let trust = page.trust {
                Section {
                    Toggle(isOn: Binding(get: { trust.isOwner }, set: { on in Task { await page.setOwner(on) } })) {
                        VStack(alignment: .leading, spacing: Theme.Space.xxs) {
                            Text("This is my own extension").font(Theme.Font.body)
                            Text("Lets it read everything, health and money included. Turn on only for an extension you trust like the app itself, such as your own memory extension.")
                                .font(Theme.Font.label)
                                .foregroundStyle(.secondary)
                        }
                    }
                    .accessibilityIdentifier("extension-trust-owner")
                    if !trust.isOwner {
                        ForEach(InfoCategory.all, id: \.self) { c in
                            Toggle(InfoCategoryWords.name(c), isOn: Binding(get: { trust.categories.contains(c) },
                                                                             set: { on in Task { await page.allowCategory(c, on: on) } }))
                                .accessibilityIdentifier("extension-trust-\(c)")
                        }
                    }
                } header: {
                    SettingsHeading(String(localized: "Personal information it may read"))
                } footer: {
                    Text(trust.isOwner ? String(localized: "It reads all your information, personal matters included.")
                         : String(localized: "Turned off, it never sees that kind of personal information. Everything else it may read as before."))
                }
                .disabled(!editable || page.busy)
            }
        }
    }
}
