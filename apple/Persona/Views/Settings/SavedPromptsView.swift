import SwiftUI

/// Assistant Profile › Saved Prompts (#9436, docs/apple/ios.md "Saved prompts"; the web's Settings › Saved Prompts, #7329):
/// the user's own "/" prompts, each by its `/name` and its text's first lines, in the order every chat's "/" list shows
/// them. New Prompt, and per prompt Edit, Move Up / Move Down and Delete (confirmed). The list is the shared
/// `SavedPromptStore`'s, the same the composers read.
struct SavedPromptsView: View {
    @State private var store = SavedPromptStore.shared
    @State private var editing: SavedPromptDraft?
    @State private var deleting: SavedPrompt?

    var body: some View {
        List {
            if !store.prompts.isEmpty {
                Section {
                    ForEach(Array(store.prompts.enumerated()), id: \.element.id) { i, p in row(p, at: i) }
                } footer: {
                    Text("Type / in any chat to put one in the message box. The same list is on your other devices.")
                }
            }
        }
        .themedPage()
        .overlay { emptyState }
        .pageTitle(String(localized: "Saved Prompts"))
        .pageToolbar {
            if store.supported {
                ToolbarItem(placement: .primaryAction) {
                    Button { editing = SavedPromptDraft() } label: { ThemedIcon(Theme.Icon.add) }
                        .accessibilityLabel("New Prompt")
                        .accessibilityIdentifier("saved-prompts-add")
                }
            }
        }
        .sheet(item: $editing) { d in
            NavigationStack { SavedPromptEditor(draft: d, others: store.prompts.filter { $0.id != d.id }, save: store.save) }
        }
        .confirmationDialog(Text("Delete “/\(deleting?.name ?? "")”?"), isPresented: deletingShown, titleVisibility: .visible,
                            presenting: deleting) { p in
            Button("Delete", role: .destructive) { Task { await store.delete(p.id) } }
        } message: { _ in
            Text("It leaves the / list on all your devices.")
        }
        .task { await load() }
        .refreshable { await store.load() }
    }

    @ViewBuilder private var emptyState: some View {
        if !store.loaded {
            ProgressView()
        } else if !store.supported {
            ContentUnavailableView("Not Available Yet", systemImage: Theme.Icon.quickCommands,
                                   description: Text("This assistant doesn't keep saved prompts yet. Update the hub to use them."))
        } else if let error = store.error, store.prompts.isEmpty {
            ContentUnavailableView {
                Label("Couldn't Load Saved Prompts", systemImage: Theme.Icon.error)
            } description: {
                Text(error)
            } actions: {
                Button("Try Again") { Task { await store.load() } }.themedButton()
            }
        } else if store.prompts.isEmpty {
            ContentUnavailableView {
                Label("No Saved Prompts", systemImage: Theme.Icon.quickCommands)
            } description: {
                Text("Keep the instructions you send again and again, like a review checklist, and type / in any chat to use one.")
            } actions: {
                Button("New Prompt") { editing = SavedPromptDraft() }.themedButton(prominent: true)
            }
        }
    }

    private var deletingShown: Binding<Bool> {
        Binding(get: { deleting != nil }, set: { if !$0 { deleting = nil } })
    }

    /// Delete only asks here: the swipe's button is not destructive (a destructive one hides the row before the answer)
    /// and a full swipe does nothing, as Saved Places.
    private func row(_ p: SavedPrompt, at i: Int) -> some View {
        let busy = store.pending.contains(p.id)
        let first = i == 0, last = i == store.prompts.count - 1
        return Button { editing = SavedPromptDraft(p) } label: { SavedPromptRow(prompt: p) }
            .foregroundStyle(.primary)
            .disabled(busy)
            .accessibilityIdentifier("saved-prompt-\(p.name)")
            .swipeActions(edge: .trailing, allowsFullSwipe: false) {
                Button { deleting = p } label: { Label("Delete", systemImage: Theme.Icon.delete) }
                    .tint(Theme.danger)
            }
            .contextMenu {
                Button { editing = SavedPromptDraft(p) } label: { Label("Edit [Message Menu 051836]", systemImage: Theme.Icon.compose) }
                if !first { Button { Task { await store.move(p.id, by: -1) } } label: { Label("Move Up", systemImage: Theme.Icon.up) } }
                if !last { Button { Task { await store.move(p.id, by: 1) } } label: { Label("Move Down", systemImage: Theme.Icon.down) } }
                Button(role: .destructive) { deleting = p } label: { Label("Delete", systemImage: Theme.Icon.delete) }
            }
            .accessibilityActions {
                if !first { Button("Move Up") { Task { await store.move(p.id, by: -1) } } }
                if !last { Button("Move Down") { Task { await store.move(p.id, by: 1) } } }
                Button("Delete") { deleting = p }
            }
    }

    private func load() async {
        await store.load()
        #if DEBUG
        let page = UserDefaults.standard.string(forKey: "openAssistantPage")
        // The app's own reread at launch can supersede this one: wait for the list itself.
        for _ in 0..<50 where page == "prompt" && store.prompts.isEmpty {
            try? await Task.sleep(for: .milliseconds(100))
        }
        switch page {
        case "prompt" where editing == nil: editing = store.prompts.first.map(SavedPromptDraft.init)
        case "newPrompt" where editing == nil: editing = SavedPromptDraft()
        default: break
        }
        #endif
    }
}

/// One saved prompt: its `/name` and its text's first two lines.
struct SavedPromptRow: View {
    let prompt: SavedPrompt

    var body: some View {
        VStack(alignment: .leading, spacing: Theme.Space.xs) {
            Text("/" + prompt.name).font(Theme.Font.code).lineLimit(1)
            Text(SavedPromptRules.preview(prompt.text)).font(Theme.Font.secondary).foregroundStyle(.secondary).lineLimit(2)
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .contentShape(.rect)
        .accessibilityElement(children: .combine)
    }
}

/// What the editor starts from: a prompt to change, or none for a new one.
struct SavedPromptDraft: Identifiable {
    var id: String?
    var name = ""
    var text = ""

    init() {}
    init(_ p: SavedPrompt) { id = p.id; name = p.name; text = p.text }
}

/// Adding or changing a saved prompt: Name (one word after a fixed "/") and Text. It says the hub's rules before asking
/// it (a taken name at once, the others once there is something to check or Save was tapped); the hub's own refusal
/// shows here and keeps what was typed.
struct SavedPromptEditor: View {
    @Environment(\.dismiss) private var dismiss
    @State private var name: String
    @State private var text: String
    @State private var saving = false
    @State private var tried = false
    @State private var refusal: String?
    private let id: String?
    private let others: [SavedPrompt]
    private let save: (String?, String, String) async throws -> SavedPrompt

    init(draft: SavedPromptDraft, others: [SavedPrompt], save: @escaping (String?, String, String) async throws -> SavedPrompt) {
        _name = State(initialValue: draft.name)
        _text = State(initialValue: draft.text)
        id = draft.id
        self.others = others
        self.save = { try await save($0, $1, $2) }
    }

    private var nameProblem: SavedPromptRules.Problem? { SavedPromptRules.nameProblem(name, others: others) }
    private var textProblem: SavedPromptRules.Problem? { SavedPromptRules.textProblem(text) }
    /// A taken name is said at once; the rest once something is typed there or Save was tapped.
    private var shownNameProblem: SavedPromptRules.Problem? {
        guard let p = nameProblem else { return nil }
        return p == .nameTaken || tried || !name.isEmpty ? p : nil
    }
    private var shownTextProblem: SavedPromptRules.Problem? { tried || !text.isEmpty ? textProblem : nil }
    private var count: Int { text.trimmingCharacters(in: .whitespacesAndNewlines).count }

    var body: some View {
        List {
            Section {
                HStack(spacing: Theme.Space.xs) {
                    Text(verbatim: "/").font(Theme.Font.code).foregroundStyle(.secondary).accessibilityHidden(true)
                    TextField("Name", text: $name, prompt: Text("review"))
                        .font(Theme.Font.code)
                        .textInputAutocapitalization(.never)
                        .autocorrectionDisabled()
                        .accessibilityIdentifier("saved-prompt-name")
                }
            } footer: {
                if let p = shownNameProblem { Text(SavedPromptRules.message(p)).foregroundStyle(Theme.danger) }
                else { Text("One word, typed after / in a chat.") }
            }
            Section {
                TextField("Text", text: $text, prompt: Text("What it types into the message box…"), axis: .vertical)
                    .lineLimit(6...16)
                    .accessibilityIdentifier("saved-prompt-text")
            } footer: {
                VStack(alignment: .leading, spacing: Theme.Space.xs) {
                    if let p = shownTextProblem { Text(SavedPromptRules.message(p)).foregroundStyle(Theme.danger) }
                    if count * 5 > SavedPromptRules.textMax * 4 {
                        Text("\(count) of \(SavedPromptRules.textMax)").monospacedDigit()
                    }
                }
            }
            if let refusal {
                Section { Text(refusal).foregroundStyle(Theme.danger).accessibilityIdentifier("saved-prompt-refusal") }
            }
        }
        .themedGroupedPage()
        .pageTitle(id == nil ? String(localized: "New Prompt") : String(localized: "Edit Prompt"))
        .navigationBarTitleDisplayMode(.inline)
        .toolbar {
            ToolbarItem(placement: .cancellationAction) { Button("Cancel") { dismiss() } }
            ToolbarItem(placement: .confirmationAction) {
                Button("Save") { Task { await submit() } }
                    .disabled(saving)
                    .accessibilityIdentifier("saved-prompt-save")
            }
        }
        .interactiveDismissDisabled(saving)
        .onChange(of: name) { refusal = nil }
        .onChange(of: text) { refusal = nil }
    }

    private func submit() async {
        tried = true
        guard nameProblem == nil, textProblem == nil else { return }
        saving = true
        defer { saving = false }
        do {
            _ = try await save(id, name, text)
            dismiss()
        } catch {
            refusal = error.shown ?? String(localized: "Couldn't save it. Try again.")
        }
    }
}
