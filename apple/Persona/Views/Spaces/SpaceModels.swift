import Foundation
import Observation

// Domain spaces' state and what they ask the hub (#13500, docs/apple/ios.md "Domain spaces"; MVVM, docs/apple/ios.md
// "Logic-layer choice"): the tracks the sidebar lists, a space's home, the SPACE.md editor, the new space form, a
// conversation's categories and what an extension may read. Each view draws what is here and calls its methods, so the
// tests run them against a `FakeHub`. Every request is the default manager's (`APIClient` track calls).

/// The tracks the app knows, for the sidebar's Spaces section and the spaces' pages: read on every hello (one that
/// failed while the hub was away, tracks another device changed), cleared for another hub.
@MainActor @Observable final class SpaceStore {
    static let shared = SpaceStore()

    /// Every track (nil until read, or on an assistant without them).
    private(set) var tracks: [AssistantTrack]?
    /// Whether the assistant has tracks: nil until known; false hides the Spaces section.
    private(set) var supported: Bool?
    /// What the Spaces section lists, worked out when the tracks change, not in a view's body (P4).
    private(set) var sidebar: [AssistantTrack] = []
    @ObservationIgnored private var hub: String?
    @ObservationIgnored private let api: () -> APIClient?

    init(api: @escaping () -> APIClient? = { AppModel.shared.api }) {
        self.api = api
    }

    /// Reads the tracks again. A failed read keeps what was known; an assistant without tracks hides the section.
    func load(hub: String?) async {
        if hub != self.hub {
            self.hub = hub
            set(nil, supported: nil)
        }
        guard let api = api() else { return }
        do {
            let list = try await api.tracks()
            guard self.hub == hub else { return }
            set(list, supported: true)
        } catch is TracksUnsupported {
            if self.hub == hub { set(nil, supported: false) }
        } catch let e as APIError where e.status == 404 || e.status == 405 {
            if self.hub == hub { set(nil, supported: false) }
        } catch {
            // the hub away, or a read that failed: what was known stays
        }
    }

    /// A track this device just created or changed, shown at once (the hub's answer), before the next read.
    func note(_ track: AssistantTrack) {
        var list = tracks ?? []
        if let at = list.firstIndex(where: { $0.slug == track.slug }) { list[at] = track } else { list.append(track) }
        set(list, supported: true)
    }

    func track(_ slug: String) -> AssistantTrack? {
        tracks?.first { $0.slug == slug }
    }

    /// The slugs taken, archived spaces' included: a new space never reuses one.
    var taken: Set<String> { Set((tracks ?? []).map(\.slug)) }

    private func set(_ list: [AssistantTrack]?, supported: Bool?) {
        if tracks != list { tracks = list }
        if self.supported != supported { self.supported = supported }
        let shown = Tracks.sidebarSpaces(list ?? [])
        if sidebar != shown { sidebar = shown }
    }
}

/// A space's home: its conversations (`GET /api/v1/assistant?space=<slug>`), its SPACE.md, and archiving it.
@Observable final class SpaceHomeModel {
    nonisolated enum Entry: Equatable { case new, open(SessionKey) }
    let slug: String
    private(set) var fetched: [Session] = []
    private(set) var loaded = false
    /// Why the list could not be read; shown only while there is nothing to show.
    private(set) var error: String?
    private(set) var spaceMd: Loadable<SpaceMd> = .loading
    /// The parts of SPACE.md the user wrote (`SpaceMarkdown.body`), worked out when the file or the space's name
    /// changes rather than on every redraw (docs/apple/performance.md P4).
    private(set) var written = ""
    /// The space's name as the page shows it; a leading heading that repeats it is left out of `written`.
    var name = "" {
        didSet { if name != oldValue { rewrite() } }
    }
    private(set) var archiving = false
    @ObservationIgnored private let deps: AssistantDeps

    init(slug: String, deps: AssistantDeps = .live) {
        self.slug = slug
        self.deps = deps
    }

    func load() async {
        await loadConversations()
        await loadSpaceMd()
    }

    func loadConversations() async {
        guard let api = deps.api() else { return }
        do {
            fetched = try await api.spaceConversations(slug)
            error = nil
        } catch {
            self.error = error.shown ?? self.error
        }
        loaded = true
    }

    func loadSpaceMd() async {
        guard let api = deps.api() else { return }
        do {
            spaceMd = .loaded(try await api.spaceMd(slug))
            rewrite()
        } catch {
            // a space without one, or a computer that can't read it now: the card says so plainly
            if spaceMd.value == nil { spaceMd = .failed(SpaceMdWords.unavailable) }
        }
    }

    /// What the editor saved, shown at once.
    func saved(_ md: SpaceMd) {
        spaceMd = .loaded(md)
        rewrite()
    }

    private func rewrite() {
        let next = spaceMd.value.map { SpaceMarkdown.body($0.content, title: name) } ?? ""
        if next != written { written = next }
    }

    /// The space's open conversations, newest first: the hub's list with what the pushes say since over it. An
    /// archived one leaves the list.
    nonisolated static func merge(_ fetched: [Session], live: [Session], slug: String) -> [Session] {
        var by: [SessionKey: Session] = [:]
        for s in fetched { by[s.key] = s }
        for s in live where s.assistantSpace == slug { by[s.key] = s }
        return by.values.filter { $0.archived != true }
            .sorted { ($0.updatedAt ?? .distantPast) > ($1.updatedAt ?? .distantPast) }
    }

    /// Where entering a space lands: back in its latest conversation, else its home.
    nonisolated static func entry(_ fetched: [Session], live: [Session], slug: String) -> Entry {
        merge(fetched, live: live, slug: slug).first.map { .open($0.key) } ?? .new
    }

    static func entry(slug: String, deps: AssistantDeps, live: [Session]) async -> Entry {
        let fetched = (try? await deps.api()?.spaceConversations(slug)) ?? []
        return entry(fetched, live: live, slug: slug)
    }

    /// Archives the space (`PUT {revision, archived: true}`); the store shows it at once. Whether it went.
    func archive(_ track: AssistantTrack, store: SpaceStore) async -> Bool {
        guard let api = deps.api(), !archiving else { return false }
        archiving = true
        defer { archiving = false }
        do {
            store.note(try await api.changeTrack(track.slug, TrackChange(revision: track.revision, archived: true)))
            deps.toast(Toast(String(localized: "\(track.name) archived [Spaces]"), style: .success))
            return true
        } catch {
            deps.toast(Toast(String(localized: "Couldn't archive the space: \(error.localizedDescription)"), style: .error))
            return false
        }
    }
}

/// The plain words for SPACE.md's states.
enum SpaceMdWords {
    static var unavailable: String {
        String(localized: "Can't be read right now. The assistant's computer may be offline or need an update.")
    }

    static func text(_ kind: SpaceMdError, detail: String?) -> String {
        switch kind {
        case .stale: String(localized: "This was changed somewhere else while you were editing. Load the latest version and make your change again; what you typed here will be replaced.")
        case .managed: String(localized: "The part Persona keeps up to date can't be changed here. Edit only your own text.")
        case .missing: String(localized: "This space has no page to edit yet.")
        case .offline: String(localized: "The assistant's computer is offline or needs an update. Try again later.")
        case .other: String(localized: "Couldn't save: \(detail ?? "")")
        }
    }
}

/// The SPACE.md editor: the parts the user wrote are fields, the blocks Persona keeps go back unchanged
/// (`SpaceMarkdown`). Saving names the version the edit started from; changed meanwhile (409) or a kept block touched
/// (400) it says so plainly and keeps what was typed.
@Observable final class SpaceMdEditorModel {
    let slug: String
    private(set) var md: SpaceMd?
    private(set) var shape: SpaceMarkdown.Shape?
    /// What the fields hold, by part index; a part not here shows as the file has it.
    var edits: [Int: String] = [:]
    private(set) var readError: String?
    private(set) var saveError: SpaceMdError?
    private(set) var saveDetail: String?
    private(set) var saving = false
    @ObservationIgnored private let deps: AssistantDeps

    init(slug: String, deps: AssistantDeps = .live) {
        self.slug = slug
        self.deps = deps
    }

    func load() async {
        guard let api = deps.api() else { return }
        readError = nil
        do {
            take(try await api.spaceMd(slug))
            saveError = nil
            saveDetail = nil
        } catch {
            readError = SpaceMdWords.text(SpaceMdError(error), detail: error.localizedDescription)
        }
    }

    private func take(_ md: SpaceMd) {
        self.md = md
        shape = SpaceMarkdown.editableParts(SpaceMarkdown.split(md.content))
        edits = [:]
    }

    /// The file with what was typed.
    var content: String? {
        shape.map { SpaceMarkdown.assemble($0.parts, edits: edits) }
    }

    /// Something was typed that changes the file.
    var changed: Bool {
        guard !edits.isEmpty, let md, let content else { return false }
        return Array(content.utf8) != Array(md.content.utf8)
    }

    /// The words a field shows: what was typed, else the file's.
    func text(_ index: Int) -> String {
        if let typed = edits[index] { return typed }
        return shape.map { $0.parts.indices.contains(index) ? $0.parts[index].text : "" } ?? ""
    }

    /// Saves; the saved file, or nil when it was refused (`saveError` says why, what was typed stays).
    func save() async -> SpaceMd? {
        guard let api = deps.api(), let md, let content, !saving else { return nil }
        // a field that now holds a block's marker would change what Persona keeps: refused here as the hub would
        guard SpaceMarkdown.sameManagedBlocks(md.content, content) else {
            saveError = .managed
            return nil
        }
        saving = true
        saveError = nil
        saveDetail = nil
        defer { saving = false }
        do {
            let r = try await api.saveSpaceMd(slug, content: content, baseDigest: md.digest)
            take(r)
            return r
        } catch {
            saveError = SpaceMdError(error)
            saveDetail = error.localizedDescription
            return nil
        }
    }

    var saveErrorText: String? { saveError.map { SpaceMdWords.text($0, detail: saveDetail) } }
}

/// New space: a name, a line about it, an icon, the personal information it holds and, with more than one on offer,
/// its kind. Its address comes from the name and is never shown (an Assistant page for everyone).
@Observable final class NewSpaceModel {
    var title = ""
    var about = ""
    var icon = SpaceIcon.tokens[0]
    private(set) var categories: [String] = []
    private(set) var kinds: [AssistantTrackKind] = []
    var kind = Tracks.defaultKind
    private(set) var busy = false
    private(set) var error: String?
    @ObservationIgnored private let deps: AssistantDeps

    init(deps: AssistantDeps = .live) {
        self.deps = deps
    }

    /// The kinds on offer; the default kind may be off or missing on this assistant, then the first on offer.
    func loadKinds() async {
        guard let api = deps.api(), let list = try? await api.trackKinds() else { return }
        kinds = Tracks.spaceKinds(list)
        kind = Tracks.reconciledKind(kind, in: kinds)
    }

    var ready: Bool { !title.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty && !busy }

    func toggle(_ category: String) {
        if let at = categories.firstIndex(of: category) { categories.remove(at: at) } else { categories.append(category) }
    }

    /// Creates the space; nil when it could not (`error` says why). An address another space took meanwhile (409) gets
    /// the next one, once.
    func create(taken: Set<String>) async -> AssistantTrack? {
        guard ready, let api = deps.api() else { return nil }
        busy = true
        error = nil
        defer { busy = false }
        let name = title.trimmingCharacters(in: .whitespacesAndNewlines)
        var used = taken
        for attempt in 0..<2 {
            let slug = Tracks.suggestSlug(title: name, taken: used)
            let body = TrackCreate(slug: slug, kind: kind, title: name, icon: icon,
                                   description: about.trimmingCharacters(in: .whitespacesAndNewlines),
                                   defaultCategories: InfoCategory.all.filter { categories.contains($0) })
            do {
                return try await api.createTrack(body)
            } catch let e as APIError where e.status == 409 && attempt == 0 {
                used.insert(slug)
            } catch {
                self.error = String(localized: "Couldn't create the space: \(error.localizedDescription)")
                return nil
            }
        }
        error = String(localized: "Another space already uses this name. Try another one.")
        return nil
    }
}

/// A conversation's information categories (#13500, docs/assistant/health.md "Using information by scene"): the kinds of
/// personal information it holds, which decide how carefully it is used outside its space. The user may take one away
/// (the space's own ones stay) and put a taken one back; only the user loosens.
@Observable final class ConversationCategoriesModel {
    let sessionID: String
    /// nil: an assistant without categories, or one that could not be read: no chips.
    private(set) var categories: ConversationCategories?
    /// The category being changed.
    private(set) var busy: String?
    @ObservationIgnored private let deps: AssistantDeps

    init(sessionID: String, deps: AssistantDeps = .live) {
        self.sessionID = sessionID
        self.deps = deps
    }

    func load() async {
        guard let api = deps.api() else { return }
        categories = try? await api.conversationCategories(sessionID)
    }

    /// Whether there is anything to show.
    var shows: Bool {
        guard let categories else { return false }
        return !categories.categories.isEmpty || !categories.removed.isEmpty
    }

    func removable(_ category: String) -> Bool {
        !(categories?.floor.contains(category) ?? true)
    }

    func remove(_ category: String) async {
        guard removable(category) else { return }
        await change(category, add: false)
    }

    func restore(_ category: String) async {
        await change(category, add: true)
    }

    /// Shown at once, put back when the hub refuses.
    private func change(_ category: String, add: Bool) async {
        guard let api = deps.api(), let was = categories, busy == nil else { return }
        var next = was
        if add {
            next.removed.removeAll { $0 == category }
            if !next.categories.contains(category) { next.categories.append(category) }
        } else {
            next.categories.removeAll { $0 == category }
            if !next.removed.contains(category) { next.removed.append(category) }
        }
        categories = next
        busy = category
        defer { busy = nil }
        do {
            categories = try await api.changeConversationCategories(sessionID, add: add ? [category] : [], remove: add ? [] : [category])
            let name = InfoCategoryWords.name(category)
            deps.toast(Toast(add ? String(localized: "\(name) added back") : String(localized: "\(name) removed"), style: .success))
        } catch {
            categories = was
            deps.toast(Toast(String(localized: "Couldn't change it: \(error.localizedDescription)"), style: .error))
        }
    }
}

/// What an extension may read of the user's personal information (#13500, `GET / PUT /api/v1/assistant/extension-trust`):
/// "This is my own extension" lets it read everything; otherwise only the kinds turned on. The default manager's.
@Observable final class ExtensionTrustModel {
    let extensionID: String
    /// nil: an assistant without grants (an older one), or not read yet: nothing shows.
    private(set) var trust: ExtensionTrust?
    private(set) var busy = false
    @ObservationIgnored private let deps: AssistantDeps

    init(extensionID: String, deps: AssistantDeps = .live) {
        self.extensionID = extensionID
        self.deps = deps
    }

    func load() async {
        guard let api = deps.api() else { return }
        guard let items = try? await api.extensionTrust() else {
            trust = nil
            return
        }
        trust = Tracks.trustOf(items, id: extensionID)
    }

    func setOwner(_ on: Bool) async {
        guard let trust else { return }
        await save(ExtensionTrust(extension: extensionID, trust: on ? ExtensionTrust.owner : ExtensionTrust.thirdParty,
                                  categories: trust.categories))
    }

    /// Lets it read `category` or not. Not named `setCategory`: `CallSourceTests` reads that call as an audio-session
    /// change that needs a verdict for a live call.
    func allowCategory(_ category: String, on: Bool) async {
        guard let trust else { return }
        var list = trust.categories.filter { $0 != category }
        if on { list.append(category) }
        await save(ExtensionTrust(extension: extensionID, trust: ExtensionTrust.thirdParty, categories: list))
    }

    /// Shown at once, put back when the hub refuses.
    func save(_ next: ExtensionTrust) async {
        guard let api = deps.api(), let was = trust, !busy else { return }
        trust = next
        busy = true
        defer { busy = false }
        do {
            let r = try await api.setExtensionTrust(extensionID, next)
            trust = ExtensionTrust(extension: extensionID, trust: r.trust, categories: r.categories)
        } catch {
            trust = was
            deps.toast(Toast(String(localized: "Couldn't change what it may read: \(error.localizedDescription)"), style: .error))
        }
    }
}
