import Foundation
import PersonaWire

// The assistant's tracks and domain spaces (#13500, docs/assistant/health.md "Tracks and kinds", docs/apple/ios.md "Domain
// spaces"): the wire types of the track API, SPACE.md, a conversation's information categories and what each extension
// may read, the pure rules the Spaces section and the new space form follow, and the requests. Every field may be
// missing or of a newer hub's shape: decoding keeps going with its default. Every request here is the default manager's
// (the track routes name no manager, docs/assistant/managers.md "Clients"), as the health space's are.

/// The information categories (server/internal/assistant/track_registry.go infoCategories), in the order shown.
nonisolated enum InfoCategory {
    static let all = ["health", "finance", "relationships", "emotion", "legal_id"]
}

/// assistantwire.TrackSidebar: where a kind lists its tracks in the sidebar.
nonisolated struct TrackSidebar: Decodable, Hashable, Sendable {
    var group = ""
    var order = 0
    var icon = ""

    init(group: String = "", order: Int = 0, icon: String = "") {
        self.group = group
        self.order = order
        self.icon = icon
    }

    private enum CodingKeys: String, CodingKey { case group, order, icon }

    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        group = (try? c.decodeIfPresent(String.self, forKey: .group)) ?? ""
        order = (try? c.decodeIfPresent(Int.self, forKey: .order)) ?? 0
        icon = (try? c.decodeIfPresent(String.self, forKey: .icon)) ?? ""
    }
}

/// assistantwire.TrackKindConfig, the part the apps read: the rest (rhythms, usage, attention…) is the hub's.
nonisolated struct TrackKindConfig: Decodable, Hashable, Sendable {
    var schema = 0
    var defaultCategories: [String] = []
    var sidebar = TrackSidebar()

    init(schema: Int = 0, defaultCategories: [String] = [], sidebar: TrackSidebar = TrackSidebar()) {
        self.schema = schema
        self.defaultCategories = defaultCategories
        self.sidebar = sidebar
    }

    private enum CodingKeys: String, CodingKey {
        case schema, sidebar
        case defaultCategories = "default_categories"
    }

    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        schema = (try? c.decodeIfPresent(Int.self, forKey: .schema)) ?? 0
        defaultCategories = (try? c.decodeIfPresent([String].self, forKey: .defaultCategories)) ?? []
        sidebar = (try? c.decodeIfPresent(TrackSidebar.self, forKey: .sidebar)) ?? TrackSidebar()
    }
}

/// assistantwire.Track: one track with its effective config.
nonisolated struct AssistantTrack: Decodable, Hashable, Sendable, Identifiable {
    var slug: String
    var kind = ""
    var title = ""
    var icon = ""
    var description = ""
    var defaultCategories: [String] = []
    var template = ""
    var config = TrackKindConfig()
    var builtin = false
    var archived = false
    var revision: Int64 = 0

    var id: String { slug }
    /// The name the space goes by: its title, else its slug.
    var name: String { title.isEmpty ? slug : title }

    init(slug: String, kind: String = "", title: String = "", icon: String = "", description: String = "",
         defaultCategories: [String] = [], template: String = "", config: TrackKindConfig = TrackKindConfig(),
         builtin: Bool = false, archived: Bool = false, revision: Int64 = 0) {
        self.slug = slug
        self.kind = kind
        self.title = title
        self.icon = icon
        self.description = description
        self.defaultCategories = defaultCategories
        self.template = template
        self.config = config
        self.builtin = builtin
        self.archived = archived
        self.revision = revision
    }

    private enum CodingKeys: String, CodingKey {
        case slug, kind, title, icon, description, template, config, builtin, archived, revision
        case defaultCategories = "default_categories"
    }

    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        slug = (try? c.decodeIfPresent(String.self, forKey: .slug)) ?? ""
        kind = (try? c.decodeIfPresent(String.self, forKey: .kind)) ?? ""
        title = (try? c.decodeIfPresent(String.self, forKey: .title)) ?? ""
        icon = (try? c.decodeIfPresent(String.self, forKey: .icon)) ?? ""
        description = (try? c.decodeIfPresent(String.self, forKey: .description)) ?? ""
        defaultCategories = (try? c.decodeIfPresent([String].self, forKey: .defaultCategories)) ?? []
        template = (try? c.decodeIfPresent(String.self, forKey: .template)) ?? ""
        config = (try? c.decodeIfPresent(TrackKindConfig.self, forKey: .config)) ?? TrackKindConfig()
        builtin = (try? c.decodeIfPresent(Bool.self, forKey: .builtin)) ?? false
        archived = (try? c.decodeIfPresent(Bool.self, forKey: .archived)) ?? false
        revision = (try? c.decodeIfPresent(Int64.self, forKey: .revision)) ?? 0
    }
}

/// assistantwire.TrackKind. `disabled`: why its tracks are left out ("schema", "invalid"); empty while it is on.
nonisolated struct AssistantTrackKind: Decodable, Hashable, Sendable, Identifiable {
    var slug: String
    var title = ""
    var config = TrackKindConfig()
    var builtin = false
    var revision: Int64 = 0
    var disabled = ""
    var warnings: [String] = []

    var id: String { slug }

    init(slug: String, title: String = "", builtin: Bool = false, disabled: String = "") {
        self.slug = slug
        self.title = title
        self.builtin = builtin
        self.disabled = disabled
    }

    private enum CodingKeys: String, CodingKey { case slug, title, config, builtin, revision, disabled, warnings }

    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        slug = (try? c.decodeIfPresent(String.self, forKey: .slug)) ?? ""
        title = (try? c.decodeIfPresent(String.self, forKey: .title)) ?? ""
        config = (try? c.decodeIfPresent(TrackKindConfig.self, forKey: .config)) ?? TrackKindConfig()
        builtin = (try? c.decodeIfPresent(Bool.self, forKey: .builtin)) ?? false
        revision = (try? c.decodeIfPresent(Int64.self, forKey: .revision)) ?? 0
        disabled = (try? c.decodeIfPresent(String.self, forKey: .disabled)) ?? ""
        warnings = (try? c.decodeIfPresent([String].self, forKey: .warnings)) ?? []
    }
}

/// assistantwire.TrackCreate.
nonisolated struct TrackCreate: Encodable, Equatable, Sendable {
    var slug: String
    var kind: String
    var title: String
    var icon: String
    var description: String
    var defaultCategories: [String]?

    private enum CodingKeys: String, CodingKey {
        case slug, kind, title, icon, description
        case defaultCategories = "default_categories"
    }
}

/// assistantwire.TrackChange: the fields given change (nil ones are left out); `revision` is the one it was made against.
nonisolated struct TrackChange: Encodable, Equatable, Sendable {
    var revision: Int64
    var title: String? = nil
    var icon: String? = nil
    var description: String? = nil
    var defaultCategories: [String]? = nil
    var archived: Bool? = nil

    private enum CodingKeys: String, CodingKey {
        case revision, title, icon, description, archived
        case defaultCategories = "default_categories"
    }
}

/// GET / PUT /assistant/tracks/{slug}/space-md: the whole file, its digest, and the managed state block's text.
nonisolated struct SpaceMd: Decodable, Equatable, Sendable {
    var content = ""
    var digest = ""
    var state = ""

    init(content: String = "", digest: String = "", state: String = "") {
        self.content = content
        self.digest = digest
        self.state = state
    }

    private enum CodingKeys: String, CodingKey { case content, digest, state }

    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        content = (try? c.decodeIfPresent(String.self, forKey: .content)) ?? ""
        digest = (try? c.decodeIfPresent(String.self, forKey: .digest)) ?? ""
        state = (try? c.decodeIfPresent(String.self, forKey: .state)) ?? ""
    }
}

/// assistantwire.ConversationCategories: what a conversation holds, the space's own ones it always keeps (`floor`), and
/// the ones the user took away.
nonisolated struct ConversationCategories: Decodable, Equatable, Sendable {
    var categories: [String] = []
    var floor: [String] = []
    var removed: [String] = []

    init(categories: [String] = [], floor: [String] = [], removed: [String] = []) {
        self.categories = categories
        self.floor = floor
        self.removed = removed
    }

    private enum CodingKeys: String, CodingKey { case categories, floor, removed }

    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        categories = (try? c.decodeIfPresent([String].self, forKey: .categories)) ?? []
        floor = (try? c.decodeIfPresent([String].self, forKey: .floor)) ?? []
        removed = (try? c.decodeIfPresent([String].self, forKey: .removed)) ?? []
    }
}

/// assistantwire.ExtensionTrust: "owner" reads everything; "third_party" only the categories it was granted.
nonisolated struct ExtensionTrust: Codable, Equatable, Sendable {
    static let owner = "owner"
    static let thirdParty = "third_party"

    var `extension` = ""
    var trust = Self.thirdParty
    var categories: [String] = []

    var isOwner: Bool { trust == Self.owner }

    init(extension id: String = "", trust: String = Self.thirdParty, categories: [String] = []) {
        self.extension = id
        self.trust = trust
        self.categories = categories
    }

    private enum CodingKeys: String, CodingKey { case `extension`, trust, categories }

    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        `extension` = (try? c.decodeIfPresent(String.self, forKey: .extension)) ?? ""
        let t = (try? c.decodeIfPresent(String.self, forKey: .trust)) ?? ""
        trust = t == Self.owner ? Self.owner : Self.thirdParty
        categories = (try? c.decodeIfPresent([String].self, forKey: .categories)) ?? []
    }

    /// What a PUT sends: the trust and the categories, never the id (it is in the path).
    struct Change: Encodable, Equatable, Sendable {
        var trust: String
        var categories: [String]
    }
}

/// The list was answered without tracks: an assistant from before them.
nonisolated struct TracksUnsupported: Error, Equatable {}

/// How a SPACE.md read or write failed, for the plain words the editor shows.
nonisolated enum SpaceMdError: Equatable, Sendable {
    /// Changed somewhere else while it was edited (409).
    case stale
    /// A block Persona keeps would change (400).
    case managed
    /// The space has no SPACE.md (404).
    case missing
    /// The assistant's computer is offline or older (503).
    case offline
    case other

    init(status: Int?) {
        switch status {
        case 409: self = .stale
        case 400: self = .managed
        case 404: self = .missing
        case 503: self = .offline
        default: self = .other
        }
    }

    init(_ error: Error) {
        self.init(status: (error as? APIError)?.status)
    }
}

/// The rules the Spaces section and the new space form follow (the web's lib/tracks.ts, kept in step).
nonisolated enum Tracks {
    /// The built-in health space: it keeps its own entry and pages, never a row under Spaces.
    static let health = "health"
    /// The sidebar group a kind lists its tracks under to appear in the Spaces section.
    static let spacesGroup = "spaces"
    /// The kind a new space defaults to.
    static let defaultKind = "space"
    /// The main chat's own way of working: not a place, never offered for a new space.
    static let orchestration = "orchestration"
    private static let reserved: Set<String> = ["main", "health", "assistant", "guest", "spaces", "tracks"]
    /// Slugs the server takes but the clients keep for a page of their own (the web's `/tracks/new`).
    private static let pageWords: Set<String> = ["new"]

    /// The tracks the Spaces section lists: not archived, in the spaces group, the health space left out. Ordered by the
    /// kind's sidebar order, then by title, then by slug.
    static func sidebarSpaces(_ tracks: [AssistantTrack]) -> [AssistantTrack] {
        tracks.filter { !$0.archived && $0.slug != health && $0.config.sidebar.group == spacesGroup }
            .sorted { a, b in
                if a.config.sidebar.order != b.config.sidebar.order { return a.config.sidebar.order < b.config.sidebar.order }
                let byTitle = a.title.localizedCompare(b.title)
                if byTitle != .orderedSame { return byTitle == .orderedAscending }
                return a.slug < b.slug
            }
    }

    /// The kinds a new space may be of: enabled ones, the main chat's own left out.
    static func spaceKinds(_ kinds: [AssistantTrackKind]) -> [AssistantTrackKind] {
        kinds.filter { $0.disabled.isEmpty && $0.slug != orchestration }
    }

    /// The kind a new space starts on: `current` when the assistant offers it, else the first it offers.
    static func reconciledKind(_ current: String, in kinds: [AssistantTrackKind]) -> String {
        guard let first = kinds.first, !kinds.contains(where: { $0.slug == current }) else { return current }
        return first.slug
    }

    /// identity.ValidTrackSlug: a lowercase letter, then 1 to 31 lowercase letters, digits or dashes; no reserved name.
    static func validSlug(_ slug: String) -> Bool {
        let bytes = Array(slug.utf8)
        guard (2...32).contains(bytes.count), let first = bytes.first, isLower(first) else { return false }
        guard bytes.dropFirst().allSatisfy({ isLower($0) || isDigit($0) || $0 == UInt8(ascii: "-") }) else { return false }
        return !reserved.contains(slug)
    }

    /// Whether the new space form takes `slug`: valid for the server, and not a word the clients keep.
    static func formSlug(_ slug: String) -> Bool {
        validSlug(slug) && !pageWords.contains(slug)
    }

    /// A slug suggested from a title: its Latin letters and digits, dashes between words; a title with none of them
    /// (Chinese, Japanese) gets "space". A reserved or taken one gets a number.
    static func suggestSlug(title: String, taken: Set<String> = []) -> String {
        // NFKD, then the combining marks left out: "Café" is "cafe".
        let folded = title.decomposedStringWithCompatibilityMapping.unicodeScalars
            .filter { !(0x0300...0x036F).contains($0.value) }
        var out: [UInt8] = []
        var dash = false
        for scalar in String(String.UnicodeScalarView(folded)).lowercased().unicodeScalars {
            if scalar.isASCII, isLower(UInt8(scalar.value)) || isDigit(UInt8(scalar.value)) {
                if dash, !out.isEmpty { out.append(UInt8(ascii: "-")) }
                dash = false
                // a slug starts with a letter: leading digits and dashes go
                if out.isEmpty, isDigit(UInt8(scalar.value)) { continue }
                out.append(UInt8(scalar.value))
            } else {
                dash = true
            }
        }
        var base = String(decoding: out.prefix(28), as: UTF8.self)
        while base.hasSuffix("-") { base.removeLast() }
        if base.count < 2 { base = "space" }
        if formSlug(base), !taken.contains(base) { return base }
        for n in 2..<1000 {
            let s = "\(base)-\(n)"
            if formSlug(s), !taken.contains(s) { return s }
        }
        return base
    }

    /// An extension's trust as read from the list: no row is a third party's with no grant.
    static func trustOf(_ items: [ExtensionTrust], id: String) -> ExtensionTrust {
        guard let row = items.first(where: { $0.extension == id }) else { return ExtensionTrust(extension: id) }
        return ExtensionTrust(extension: id, trust: row.isOwner ? ExtensionTrust.owner : ExtensionTrust.thirdParty, categories: row.categories)
    }

    private static func isLower(_ b: UInt8) -> Bool { b >= UInt8(ascii: "a") && b <= UInt8(ascii: "z") }
    private static func isDigit(_ b: UInt8) -> Bool { b >= UInt8(ascii: "0") && b <= UInt8(ascii: "9") }
}

nonisolated extension APIClient {
    private struct TrackList: Decodable { var tracks: [AssistantTrack]? }
    private struct KindList: Decodable { var kinds: [AssistantTrackKind]? }
    private struct TrustList: Decodable { var items: [ExtensionTrust]? }
    private struct SpaceMdWrite: Encodable {
        var content: String
        var baseDigest: String
        enum CodingKeys: String, CodingKey { case content; case baseDigest = "base_digest" }
    }
    private struct CategoriesChange: Encodable {
        var add: [String]?
        var remove: [String]?
    }

    /// One path segment, escaped once (a slug, a conversation's or an extension's id).
    private static func segment(_ s: String) -> String {
        let allowed = CharacterSet.urlPathAllowed.subtracting(CharacterSet(charactersIn: "/?#%"))
        return s.addingPercentEncoding(withAllowedCharacters: allowed) ?? ""
    }

    /// The default manager's client, whichever manager the Persona page shows: tracks are its.
    private var tracksAPI: APIClient { manager(nil) }

    /// Every track, built-in and archived ones included. An answer without a list is an assistant without tracks
    /// (`TracksUnsupported`); a 404 or 405 is thrown as it came.
    func tracks() async throws -> [AssistantTrack] {
        let list = try await tracksAPI.get("/api/v1/assistant/tracks", as: TrackList.self)
        guard let tracks = list.tracks else { throw TracksUnsupported() }
        return tracks
    }

    /// Every kind; an assistant without them answers none.
    func trackKinds() async throws -> [AssistantTrackKind] {
        try await tracksAPI.get("/api/v1/assistant/track-kinds", as: KindList.self).kinds ?? []
    }

    func createTrack(_ body: TrackCreate) async throws -> AssistantTrack {
        try JSON.decoder.decode(AssistantTrack.self, from: try await tracksAPI.request("POST", "/api/v1/assistant/tracks", body: body))
    }

    func changeTrack(_ slug: String, _ change: TrackChange) async throws -> AssistantTrack {
        try JSON.decoder.decode(AssistantTrack.self, from: try await tracksAPI.request("PUT", "/api/v1/assistant/tracks/\(Self.segment(slug))", body: change))
    }

    func spaceMd(_ slug: String) async throws -> SpaceMd {
        try await tracksAPI.get("/api/v1/assistant/tracks/\(Self.segment(slug))/space-md", as: SpaceMd.self)
    }

    /// Writes SPACE.md; `baseDigest` is the digest of the content the edit started from (409 when it changed since).
    func saveSpaceMd(_ slug: String, content: String, baseDigest: String) async throws -> SpaceMd {
        let data = try await tracksAPI.request("PUT", "/api/v1/assistant/tracks/\(Self.segment(slug))/space-md",
                                               body: SpaceMdWrite(content: content, baseDigest: baseDigest))
        return try JSON.decoder.decode(SpaceMd.self, from: data)
    }

    /// A space's conversations (`GET /api/v1/assistant?space=<slug>`), as `healthConversations` reads the health space's:
    /// each on the list's computer when it names none, marked with the space.
    func spaceConversations(_ slug: String) async throws -> [Session] {
        let st = try await tracksAPI.get("/api/v1/assistant", query: ["space": slug], as: AssistantState.self)
        return st.conversations.map { s in
            var s = s
            if s.machineID.isEmpty { s.machineID = st.machineID }
            if s.assistantSpace == nil { s.assistantSpace = slug }
            return stamp(s)
        }
    }

    func conversationCategories(_ sessionID: String) async throws -> ConversationCategories {
        try await tracksAPI.get("/api/v1/assistant/conversations/\(Self.segment(sessionID))/categories", as: ConversationCategories.self)
    }

    func changeConversationCategories(_ sessionID: String, add: [String] = [], remove: [String] = []) async throws -> ConversationCategories {
        let data = try await tracksAPI.request("PUT", "/api/v1/assistant/conversations/\(Self.segment(sessionID))/categories",
                                               body: CategoriesChange(add: add.isEmpty ? nil : add, remove: remove.isEmpty ? nil : remove))
        return try JSON.decoder.decode(ConversationCategories.self, from: data)
    }

    /// What every extension the user decided about may read; nil from an assistant without grants.
    func extensionTrust() async throws -> [ExtensionTrust]? {
        try await tracksAPI.get("/api/v1/assistant/extension-trust", as: TrustList.self).items
    }

    func setExtensionTrust(_ id: String, _ trust: ExtensionTrust) async throws -> ExtensionTrust {
        let data = try await tracksAPI.request("PUT", "/api/v1/assistant/extension-trust/\(Self.segment(id))",
                                               body: ExtensionTrust.Change(trust: trust.trust, categories: trust.categories))
        return try JSON.decoder.decode(ExtensionTrust.self, from: data)
    }
}
