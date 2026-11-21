import SwiftUI
import PersonaWire

// What the user corrects on an entry by hand (docs/assistant/knowledge.md "Corrections, locks and unlinking"), as the
// web's entry page does (`ConfirmedFacts.tsx`, `Relationships.tsx`, `lib/kb-relationships.ts`): the confirmed facts
// Persona keeps as true about it, and a link to another entry that is wrong. Tags and importance are `KBUpdate`.

/// The fields of the hub's `kb.Update` a correction writes.
nonisolated struct KBCorrection: Encodable, Hashable, Sendable {
    /// nil keeps the confirmed facts; "" clears them.
    var pinned: String?
    var removePeople: [String]?
    var removeChats: [String]?
    var removeProjects: [String]?
    var lockLists: Bool?
    var allowPeople: [String]?
    var allowChats: [String]?
    var allowProjects: [String]?

    enum CodingKeys: String, CodingKey {
        case pinned
        case removePeople = "remove_people"
        case removeChats = "remove_chats"
        case removeProjects = "remove_projects"
        case lockLists = "lock_lists"
        case allowPeople = "allow_people"
        case allowChats = "allow_chats"
        case allowProjects = "allow_projects"
    }
}

extension APIClient {
    func kbCorrect(_ kind: String, id: String, _ c: KBCorrection) async throws {
        _ = try await request("PUT", "/api/v1/kb/\(kind)/\(Self.kbID(id))", body: c)
    }
}

/// An entry's confirmed facts: what the user said is true about it. Persona's own updates read them and never change them.
nonisolated enum KBFacts {
    static func text(_ meta: JSONValue?) -> String {
        if case .string(let s)? = meta?["pinned"] { return s }
        return ""
    }

    /// The detail with these facts, shown before the hub answers; reloading replaces it with what the hub has.
    static func with(_ text: String, in response: KBDetailResponse) -> KBDetailResponse {
        var r = response
        var meta = r.detail.meta?.object ?? [:]
        meta["pinned"] = .string(text)
        r.detail.meta = .object(meta)
        return r
    }
}

/// A link held in each entry's own list. Detail.links also includes related entries from provenance, which can't be removed.
nonisolated struct KBRelationship: Identifiable, Hashable, Sendable {
    enum List: String, Sendable {
        case people, chats, projects, items

        /// What the list is called in a sentence ("the people linked to …").
        var title: String {
            switch self {
            case .people: String(localized: "people [KB Unlink]")
            case .chats: String(localized: "chats [KB Unlink]")
            case .projects: String(localized: "projects [KB Unlink]")
            case .items: String(localized: "linked items [KB Unlink]")
            }
        }
    }

    /// One of the two entries: the id requests carry, the one its folder has (`canonicalID`), its list and the other
    /// entry as that list names it.
    struct End: Hashable, Sendable {
        var kind: String
        var id: String
        var canonicalID: String
        var name: String
        var field: List
        var member: String
    }

    var first: End
    var second: End
    /// Project items are written from the item's side, whose list is `projects`.
    private var reverse: Bool
    var target: End { reverse ? second : first }
    var id: String { "\(second.kind)/\(second.id)" }

    private static let items: Set<String> = ["docs", "meetings", "events", "recordings"]
    private static let unsafe = try! NSRegularExpression(pattern: #"[^\p{L}\p{N}._@+-]+"#)

    /// The hub's `SafeID`, the name of an entry's folder: requests and lists still carry the id it came with.
    static func canonicalID(kind: String, id: String) -> String {
        let source = kind == KBKind.recordings.rawValue ? id.lowercased() : id
        let safe = unsafe.stringByReplacingMatches(in: source, range: NSRange(source.startIndex..., in: source), withTemplate: "_")
            .trimmingCharacters(in: CharacterSet(charactersIn: "._"))
        var out = String.UnicodeScalarView()
        var bytes = 0
        for s in safe.unicodeScalars {
            bytes += String(s).utf8.count
            if bytes > 120 { break }
            out.append(s)
        }
        return String(out)
    }

    static func removal(_ detail: KBDetail, name: String, linked: KBEntry) -> Self? {
        guard let pair = Self.pair(detail, name: name, linked: linked),
              strings(detail.meta?[pair.first.field.rawValue])?.contains(pair.first.member) == true else { return nil }
        return pair
    }

    static func allowance(_ detail: KBDetail, name: String, removed: KBEntry) -> Self? {
        guard let pair = Self.pair(detail, name: name, linked: removed),
              strings(detail.meta?["excluded"]?[pair.first.field.rawValue])?.contains(pair.first.member) == true else { return nil }
        return pair
    }

    private static func pair(_ detail: KBDetail, name: String, linked: KBEntry) -> Self? {
        let a = detail, b = linked
        let aid = string(a.meta?["id"]) ?? a.id
        let af: List, bf: List
        var reverse = false
        let projects = KBKind.projects.rawValue, people = KBKind.people.rawValue, chats = KBKind.chats.rawValue
        if a.kind == projects && (b.kind == people || b.kind == chats) {
            af = List(rawValue: b.kind)!; bf = .projects
        } else if a.kind == projects && items.contains(b.kind) {
            af = .items; bf = .projects; reverse = true
        } else if b.kind == projects && (a.kind == people || a.kind == chats || items.contains(a.kind)) {
            af = .projects; bf = a.kind == people || a.kind == chats ? List(rawValue: a.kind)! : .items
        } else if a.kind == people && b.kind == chats {
            af = .chats; bf = .people
        } else if a.kind == chats && b.kind == people {
            af = .people; bf = .chats
        } else {
            return nil
        }
        let bid = canonicalID(kind: b.kind, id: b.id)
        let am = af == .items ? "\(b.kind)/\(bid)" : b.id
        let bm = bf == .items ? "\(a.kind)/\(a.id)" : aid
        return Self(first: End(kind: a.kind, id: aid, canonicalID: a.id, name: name, field: af, member: am),
                    second: End(kind: b.kind, id: b.id, canonicalID: bid, name: b.name, field: bf, member: bm),
                    reverse: reverse)
    }

    func removalUpdate(lockLists: Bool) -> KBCorrection {
        var update = KBCorrection(lockLists: lockLists)
        switch target.field {
        case .people: update.removePeople = [target.member]
        case .chats: update.removeChats = [target.member]
        case .projects, .items: update.removeProjects = [target.member]
        }
        return update
    }

    var allowanceUpdate: KBCorrection {
        switch target.field {
        case .people: KBCorrection(allowPeople: [target.member])
        case .chats: KBCorrection(allowChats: [target.member])
        case .projects, .items: KBCorrection(allowProjects: [target.member])
        }
    }

    static func matches(_ detail: KBDetail, _ end: End) -> Bool {
        detail.kind == end.kind && detail.id == end.canonicalID
    }

    /// Both ends must keep the link out. Older hubs that only lock the lists cannot confirm the removal.
    func removalConfirmed(_ firstDetail: KBDetail?, _ secondDetail: KBDetail?, lockLists: Bool) -> Bool {
        Self.removalConfirmed(firstDetail, end: first, lockLists: lockLists)
            && Self.removalConfirmed(secondDetail, end: second, lockLists: lockLists)
    }

    private static func removalConfirmed(_ detail: KBDetail?, end: End, lockLists: Bool) -> Bool {
        let field = end.field.rawValue
        guard let detail, matches(detail, end),
              omits(end.member, from: detail.meta?[field]),
              strings(detail.meta?["excluded"]?[field])?.contains(end.member) == true else { return false }
        if lockLists, strings(detail.meta?["locked_fields"])?.contains(field) != true { return false }
        // Failed attribution cleanup can leave an extension in control of the list.
        switch detail.meta?["provenance"] {
        case nil, .null?: return true
        case .object(let provenance)?: return provenance[field] == nil
        default: return false
        }
    }

    func allowanceConfirmed(_ detail: KBDetail) -> Bool {
        guard Self.matches(detail, first), let meta = detail.meta?.object,
              detail.removed?.contains(where: { $0.kind == second.kind && $0.id == second.id }) != true else { return false }
        switch meta["excluded"] {
        case nil, .null?: return true
        case .object(let excluded)?: return Self.omits(first.member, from: excluded[first.field.rawValue])
        default: return false
        }
    }

    var question: String {
        String(localized: "Remove the link between “\(first.name)” and “\(second.name)”?")
    }

    var lockExplanation: String {
        String(localized: "Persona will stop changing the \(first.field.title) linked to “\(first.name)” and the \(second.field.title) linked to “\(second.name)” on its own.")
    }

    private static func omits(_ member: String, from value: JSONValue?) -> Bool {
        switch value {
        case nil, .null?: return true
        default: return strings(value).map { !$0.contains(member) } ?? false
        }
    }

    private static func string(_ v: JSONValue?) -> String? {
        if case .string(let s)? = v { return s }
        return nil
    }

    /// A list of strings only; anything else in it, or not a list, is nil.
    private static func strings(_ v: JSONValue?) -> [String]? {
        guard case .array(let a)? = v else { return nil }
        var out: [String] = []
        for x in a {
            guard case .string(let s) = x else { return nil }
            out.append(s)
        }
        return out
    }
}

extension APIClient {
    func kbReadback(_ end: KBRelationship.End, query: String? = nil) async -> (response: KBDetailResponse?, error: (any Error)?) {
        do { return (try await kbDetail(end.kind, id: end.id, query: query), nil) }
        catch { return (nil, error) }
    }
}

/// The choice belongs to this presentation. Submitting captures it before the sheet closes.
struct KBRemoveLinkSheet: View {
    @Environment(\.dismiss) private var dismiss
    let relationship: KBRelationship
    let remove: (Bool) -> Void
    @State private var lockLists = false
    @State private var submitted = false
    @State private var promptHeight: CGFloat = 0
    @State private var actionsHeight: CGFloat = 0

    var body: some View {
        VStack(spacing: Theme.Space.s) {
            ScrollView {
                prompt
                    .padding(.horizontal, Theme.Space.xl)
                    .padding(.top, Theme.Space.xl)
                    .onGeometryChange(for: CGFloat.self, of: { $0.size.height.rounded(.up) }) { promptHeight = $0 }
            }
            .scrollBounceBehavior(.basedOnSize)
            actions
                .padding(.horizontal, Theme.Space.xl)
                .padding(.bottom, Theme.Space.m)
                .onGeometryChange(for: CGFloat.self, of: { $0.size.height.rounded(.up) }) { actionsHeight = $0 }
        }
        .disabled(submitted)
        .presentationDetents(promptHeight > 0 && actionsHeight > 0
                             ? [.height(promptHeight + Theme.Space.s + actionsHeight)] : [.medium])
        .presentationDragIndicator(.visible)
    }

    private var prompt: some View {
        VStack(alignment: .leading, spacing: Theme.Space.m) {
            Text(relationship.question)
                .font(Theme.Font.title)
                .accessibilityAddTraits(.isHeader)
            Group {
                if lockLists {
                    Text("Both entries are kept.")
                } else {
                    Text("Both entries are kept. Persona won’t make this link again on its own. This leaves other links free to update.")
                }
            }
            .font(Theme.Font.secondary)
            .foregroundStyle(.secondary)
            .readingContent()
            Toggle("Also stop automatic changes to both lists", isOn: $lockLists)
                .toggleStyle(.switch)
                .font(Theme.Font.body)
                .scaledFrame(minHeight: Theme.Size.tapTarget)
                .accessibilityIdentifier("kb.unlink.lockLists")
            if lockLists {
                Text(relationship.lockExplanation)
                    .font(Theme.Font.secondary)
                    .foregroundStyle(.secondary)
                    .readingContent()
                    .accessibilityIdentifier("kb.unlink.lockExplanation")
            }
        }
    }

    private var actions: some View {
        VStack(spacing: Theme.Space.s) {
            Button(role: .destructive) {
                guard !submitted else { return }
                submitted = true
                let choice = lockLists
                dismiss()
                remove(choice)
            } label: {
                sheetButton("Remove Link")
            }
            .themedButton(prominent: true)
            .tint(Theme.danger)
            .accessibilityIdentifier("kb.unlink.confirm")
            Button { dismiss() } label: { sheetButton("Cancel") }
                .themedButton()
                .keyboardShortcut(.cancelAction)
                .accessibilityIdentifier("kb.unlink.cancel")
        }
        .controlSize(.large)
    }

    private func sheetButton(_ title: LocalizedStringKey) -> some View {
        Text(title)
            .font(Theme.Font.heading)
            .buttonLabelLine()
            .scaledFrame(maxWidth: .infinity, minHeight: Theme.Size.tapTarget)
    }
}

nonisolated struct KBRemovedLink: Identifiable, Sendable {
    let entry: KBEntry
    let relationship: KBRelationship
    var id: String { relationship.id }

    static func rows(in detail: KBDetail, name: String) -> [Self] {
        var seen = Set<String>()
        return (detail.removed ?? []).compactMap { entry in
            guard let relationship = KBRelationship.allowance(detail, name: name, removed: entry),
                  seen.insert(relationship.id).inserted else { return nil }
            return Self(entry: entry, relationship: relationship)
        }
    }
}

struct KBRemovedLinksSection: View {
    @Environment(\.dynamicTypeSize) private var typeSize
    let rows: [KBRemovedLink]
    let changingLink: String?
    let allow: (KBRelationship) -> Void

    var body: some View {
        if !rows.isEmpty {
            Section {
                ForEach(rows) { row in
                    let busy = changingLink == row.id
                    let layout = typeSize >= Theme.interfaceLargestTypeSize
                        ? AnyLayout(VStackLayout(alignment: .leading, spacing: Theme.Space.s))
                        : AnyLayout(HStackLayout(spacing: Theme.Space.s))
                    layout {
                        KBLinkRow(entry: row.entry).frame(maxWidth: .infinity, alignment: .leading)
                        Button { allow(row.relationship) } label: {
                            Text("Allow again")
                                .font(Theme.Font.secondaryMedium)
                                .buttonLabelLine()
                                .scaledFrame(minWidth: Theme.Size.tapTarget, minHeight: Theme.Size.tapTarget)
                                .opacity(busy ? 0 : 1)
                                .overlay { if busy { ProgressView() } }
                        }
                        .themedButton()
                        .layoutPriority(1)
                        .disabled(changingLink != nil)
                        .accessibilityLabel("Allow Persona to link \(row.entry.name) again")
                        .accessibilityValue(busy ? Text("Updating…") : Text(verbatim: ""))
                        .accessibilityIdentifier("kb.link.allow.\(row.id)")
                    }
                }
            } header: {
                Text("Removed by you")
            } footer: {
                Text("Allowing a link again lets Persona make it in a future update.")
            }
        }
    }
}

/// The confirmed facts as the entry page shows them: the text, or a way to add some; Edit and Delete beside it.
struct KBFactsSection: View {
    var facts: String
    var edit: () -> Void
    var delete: () -> Void

    var body: some View {
        Section {
            if facts.isEmpty {
                Button(action: edit) {
                    Label("Add Confirmed Facts", systemImage: Theme.Icon.add).lineLimit(1)
                }
                .accessibilityIdentifier("kb.facts.add")
            } else {
                Text(facts)
                    .font(Theme.Font.body)
                    .readingContent()
                    .textSelection(.enabled)
                    .frame(maxWidth: .infinity, alignment: .leading)
                    .padding(.vertical, Theme.Space.xxs)
                    .accessibilityIdentifier("kb.facts")
                    .contextMenu {
                        Button(action: edit) { Label("Edit [KB Facts]", systemImage: Theme.Icon.edit) }
                        Button(role: .destructive, action: delete) { Label("Delete", systemImage: Theme.Icon.delete) }
                    }
                    .swipeActions(edge: .trailing) {
                        Button(action: delete) { Label("Delete", systemImage: Theme.Icon.delete) }.tint(Theme.danger)
                        Button(action: edit) { Label("Edit [KB Facts]", systemImage: Theme.Icon.edit) }
                    }
                Button(action: edit) {
                    Label("Edit Confirmed Facts", systemImage: Theme.Icon.edit).lineLimit(1)
                }
                .accessibilityIdentifier("kb.facts.edit")
            }
        } header: {
            Text("Confirmed Facts")
        } footer: {
            Text("Your corrections are kept when Persona updates this entry.")
        }
    }
}

/// Writing the confirmed facts: Save sends them, an emptied text deletes them (the page offers Undo then).
struct KBFactsEditor: View {
    @Environment(\.dismiss) private var dismiss
    var name: String
    var initial: String
    /// Writes the facts; throws to keep the sheet open with the draft.
    var save: (String) async throws -> Void
    /// Deletes them: the sheet closes first, the page does it and offers Undo.
    var delete: () -> Void
    @State private var draft: String
    @State private var saving = false
    @State private var error: String?
    @FocusState private var focused: Bool

    init(name: String, initial: String, save: @escaping (String) async throws -> Void, delete: @escaping () -> Void) {
        self.name = name
        self.initial = initial
        self.save = save
        self.delete = delete
        _draft = State(initialValue: initial)
    }

    private var trimmed: String { draft.trimmingCharacters(in: .whitespacesAndNewlines) }
    private var changed: Bool { trimmed != initial.trimmingCharacters(in: .whitespacesAndNewlines) }

    var body: some View {
        NavigationStack {
            Form {
                Section {
                    TextEditor(text: $draft)
                        .font(Theme.Font.body)
                        .focused($focused)
                        .scaledFrame(minHeight: Theme.Size.box140)
                        .disabled(saving)
                        .accessibilityLabel("Confirmed Facts")
                        .accessibilityIdentifier("kb.facts.editor")
                } footer: {
                    Text("Write what Persona should always take as true about “\(name)”. Persona reads this but never changes it.")
                }
                if !initial.isEmpty {
                    Section {
                        Button(role: .destructive) {
                            dismiss()
                            delete()
                        } label: {
                            Label("Delete Confirmed Facts", systemImage: Theme.Icon.delete).lineLimit(1)
                        }
                        .disabled(saving)
                        .accessibilityIdentifier("kb.facts.delete")
                    }
                }
                if let error {
                    Section {
                        Label(String(localized: "Couldn't save. Your text is kept, try again: \(error)"), systemImage: Theme.Icon.error)
                            .foregroundStyle(Theme.danger).font(Theme.Font.caption)
                    }
                }
            }
            .navigationTitle("Confirmed Facts")
            .navigationBarTitleDisplayMode(.inline)
            .sheetCancel(unsaved: changed) { dismiss() }
            .toolbar {
                ToolbarItem(placement: .confirmationAction) {
                    Button { Task { await submit() } } label: {
                        if saving { ProgressView() } else { Text("Save").lineLimit(1) }
                    }
                    .disabled(saving || !changed)
                    .accessibilityIdentifier("kb.facts.save")
                }
            }
            .interactiveDismissDisabled(saving || changed)
            .onAppear { if initial.isEmpty { focused = true } }
        }
        .presentationDetents([.medium, .large])
        #if os(macOS)
        .macSheet()
        #endif
    }

    private func submit() async {
        guard !saving, changed else { return }
        if trimmed.isEmpty, !initial.isEmpty {
            dismiss()
            delete()
            return
        }
        saving = true
        error = nil
        do {
            try await save(trimmed)
            Haptics.success()
            dismiss()
        } catch {
            self.error = error.shown ?? error.localizedDescription
            Haptics.error()
        }
        saving = false
    }
}
