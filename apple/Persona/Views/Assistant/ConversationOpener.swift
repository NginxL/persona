import ImageIO
import PersonaWire
import SwiftUI

// A conversation an extension opened, marked with that extension's own icon (#10358; the web's OpenerTile and
// lib/opener-icons.ts, docs/protocol/protocol.md "Origin of assistant chats"). The hub's origin says only how it was
// opened: any extension that opens one with the lark.agent purpose gets `lark_agent`, which was drawn as Lark's. The
// extension itself is the conversation's `opened_by`, else its `origin_ref`, and its icon is the one its package brings,
// which the hub serves this device from the extension's own manager. It is drawn in the box of Lark's mark, as an app's
// icon is (`AppIconShape`). Until it is drawn, and when the extension has no such icon, is not installed (any more) for
// the conversation's manager, is the one the origin's mark already stands for (the Lark extension for Lark /agent) or its
// icon does not load, the origin's own mark stays (`AutoSourceMark`). The words that name the mark (VoiceOver's, and the
// line under the message the conversation was opened with) say "From <the extension's name>" while its icon is drawn,
// and only then, so they never disagree with it.

/// The extension that opened a conversation from outside (Lark /agent's or another extension's), by id, with the manager
/// the conversation belongs to.
nonisolated struct ConversationOpener: Hashable, Sendable {
    var id: String
    /// The conversation's manager (`Session.manager`); nil with one manager and from older hubs.
    var manager: String?

    /// `opened_by`, else `origin_ref`, which for these two origins is that extension too (the hub notes both,
    /// server/internal/assistant/start.go requestOrigin); nil for any other origin, and when neither is there (a
    /// conversation the hub's backfill judged).
    static func of(origin: String?, openedBy: String?, originRef: String?, manager: String? = nil) -> ConversationOpener? {
        guard AutoConversationSource.external(origin) != nil else { return nil }
        let id = [openedBy, originRef].lazy.compactMap { $0?.trimmingCharacters(in: .whitespacesAndNewlines) }.first { !$0.isEmpty }
        return id.map { ConversationOpener(id: $0, manager: manager) }
    }

    static func of(_ s: Session) -> ConversationOpener? {
        of(origin: s.origin, openedBy: s.openedBy, originRef: s.originRef, manager: s.manager)
    }

    /// The icon to draw for it from the installed extensions (`list`): the one of its id that is the conversation's
    /// manager's (`sameManager`, told the extension's manager), when its `icon_url` is one of the hub's icon routes (an
    /// installed or a catalog icon; public artwork is left out, as on the web) and it is not the extension the origin's
    /// mark (`brand`) stands for. nil while the list is not read and in every other case: the origin's mark stays.
    func icon(in list: [OpenerListing]?, brand: String?, sameManager: (String?) -> Bool) -> OpenerIcon? {
        guard let x = list?.first(where: { $0.id == id && sameManager($0.manager) }), let url = x.iconURL,
              Self.isHubIcon(url), !x.stands(for: brand) else { return nil }
        let name = x.name?.trimmingCharacters(in: .whitespacesAndNewlines)
        return OpenerIcon(route: url, manager: x.manager, name: name?.isEmpty == false ? name : nil)
    }

    /// Whether an extension stamped `extensionManager` is the manager's of a conversation stamped `conversationManager`
    /// (nil: the one the Persona page shows, `shown`), as `AppModel.sameManager` tells: with one manager every one is.
    static func sameManager(_ extensionManager: String?, _ conversationManager: String?, shown: String?, roster: ManagerRoster?) -> Bool {
        guard let roster, roster.isMulti else { return true }
        return roster.owner(extensionManager) == roster.owner(conversationManager ?? shown)
    }

    /// The hub's own icon routes, the only ones this device's credentials go to (web lib/extension-logo.ts):
    /// `/api/v1/extensions/<id>/-/icon?v=<version>` (an installed extension's) and
    /// `/api/v1/extensions/-/catalog/<market>/<id>/icon?sha256=<64 hex digits>` (a catalog entry's).
    static func isHubIcon(_ route: String) -> Bool {
        let halves = route.split(separator: "?", maxSplits: 1, omittingEmptySubsequences: false)
        guard halves.count == 2, !route.contains("#") else { return false }
        let p = halves[0].split(separator: "/", omittingEmptySubsequences: false)
        let query = halves[1]
        let allowed = { (c: Character, more: String) in c.isASCII && (c.isLetter || c.isNumber || more.contains(c)) }
        let name = { (s: Substring) in (1...64).contains(s.count) && s.allSatisfy { allowed($0, "_-") } }
        guard p.prefix(4).elementsEqual(["", "api", "v1", "extensions"]) else { return false }
        if p.count == 7, name(p[4]), p[5] == "-", p[6] == "icon", query.hasPrefix("v=") {
            let v = query.dropFirst(2)
            return !v.isEmpty && v.allSatisfy { allowed($0, "_.~%+-") }
        }
        if p.count == 9, p[4] == "-", p[5] == "catalog", name(p[6]), name(p[7]), p[8] == "icon", query.hasPrefix("sha256=") {
            let digest = query.dropFirst(7)
            return digest.count == 64 && digest.allSatisfy { $0.isASCII && $0.isHexDigit }
        }
        return false
    }
}

/// What a mark needs of one installed extension (GET /api/v1/extensions, every manager's, each item stamped with its
/// manager).
nonisolated struct OpenerListing: Decodable, Hashable, Sendable {
    var id: String
    var name: String?
    var manager: String?
    var iconURL: String?
    /// The connectors and messaging sources it brings (`contributes`), whose brands say which origin's mark it stands for.
    var brings: [String] = []

    init(id: String, name: String? = nil, manager: String? = nil, iconURL: String? = nil, brings: [String] = []) {
        self.id = id
        self.name = name
        self.manager = manager
        self.iconURL = iconURL
        self.brings = brings
    }

    private enum CodingKeys: String, CodingKey {
        case id, name, manager, contributes
        case iconURL = "icon_url"
    }

    /// Anything odd in a field the marks read is none, never the whole item: only `id` is needed.
    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        id = try c.decode(String.self, forKey: .id)
        name = try? c.decodeIfPresent(String.self, forKey: .name)
        manager = try? c.decodeIfPresent(String.self, forKey: .manager)
        iconURL = try? c.decodeIfPresent(String.self, forKey: .iconURL)
        let contributes = try? c.decodeIfPresent([String: JSONValue].self, forKey: .contributes)
        brings = ["connectors", "messaging"].flatMap { point in
            (contributes?[point]?.array ?? []).compactMap { (v: JSONValue) -> String? in
                guard case .string(let id) = v else { return nil }
                return id
            }
        }
    }

    /// Whether it is the extension the origin's mark (`brand`) stands for: it brings a connector or a messaging source of
    /// that brand, as the Lark extension brings `lark` (the web's `connectorBrand`). Read from its manifest, not its id.
    func stands(for brand: String?) -> Bool {
        guard let brand else { return false }
        return brings.contains { Self.brand(of: $0) == brand }
    }

    /// A connector's or a messaging source's brand by its id, as web lib/connectors.ts `PROFILES` has it: Lark's for
    /// `lark` and `larkcli` (the app draws those Lark's elsewhere too), else `ConnectorText`'s.
    static func brand(of id: String) -> String? {
        ["lark", "larkcli"].contains(id) ? Theme.Brand.lark : ConnectorText.brand(id)
    }
}

/// GET /api/v1/extensions (/api/v1/plugins on an older hub) as the marks read it: an item that does not decode is left
/// out, not the list.
nonisolated struct OpenerListings: Decodable, Sendable {
    var items: [OpenerListing]

    private enum CodingKeys: String, CodingKey { case extensions, plugins }

    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        let all = try c.decodeIfPresent([Lossy<OpenerListing>].self, forKey: .extensions)
            ?? c.decodeIfPresent([Lossy<OpenerListing>].self, forKey: .plugins) ?? []
        items = all.compactMap(\.value)
    }
}

/// The icon a mark draws for the extension that opened a conversation: its route on the hub (`isHubIcon`), the manager
/// to ask it of, and the extension's name for the words, if it has one.
nonisolated struct OpenerIcon: Hashable, Sendable {
    var route: String
    var manager: String?
    var name: String?

    /// The route's path and its query, as `APIClient.request` takes them.
    var request: (path: String, query: [String: String]) {
        let halves = route.split(separator: "?", maxSplits: 1)
        var query: [String: String] = [:]
        for pair in (halves.count > 1 ? halves[1] : "").split(separator: "&") {
            let kv = pair.split(separator: "=", maxSplits: 1)
            if kv.count == 2 { query[String(kv[0])] = String(kv[1]).removingPercentEncoding ?? String(kv[1]) }
        }
        return (String(halves.first ?? ""), query)
    }

    /// The largest picture read: 2 MiB of PNG, as the hub serves at most (docs/extensions/extensions.md "Bundled catalog artwork").
    static let maxBytes = 2 << 20

    /// The picture `data` decoded at about `pixels` on its longer side, called off the main thread (P8): a PNG of at
    /// most `maxBytes`, else none.
    static func picture(_ data: Data, pixels: Int) -> ExtensionImageCache.Thumbnail? {
        guard data.count <= maxBytes, data.starts(with: [0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A]) else { return nil }
        return ExtensionImageCache.decode(data, maxPixel: pixels)
    }
}

extension AutoConversationSource {
    /// `from`, or 「来自 <name>」 while the mark draws the icon of the extension `name` names (#10358).
    func from(opener name: String?) -> String { name.map { String(localized: "From \($0)") } ?? from }

    /// `markLabel`, or 「来自 <name>」 while the mark draws the icon of the extension `name` names (#10358).
    func markLabel(opener name: String?) -> String { name == nil ? markLabel : from(opener: name) }
}

/// The installed extensions and their icons as those marks need them (the web's lib/opener-icons.ts). The list is read
/// once a mark needs it, again for another hub, and when a mark appears a minute or more after the last read or after
/// Settings › Extensions read its own (an extension installed, upgraded or removed since; a new version's icon is another
/// route); the last one stays meanwhile. Each icon is read once from its extension's manager, decoded off the main
/// thread at about the size drawn and kept, at most `kept` of them (P8, P11); one that did not load is asked again only
/// after the list is read again.
@Observable final class OpenerIcons {
    static let shared = OpenerIcons()

    struct Deps {
        var api: () -> APIClient?
        var features: () -> Set<String>
        /// Whether an extension stamped with a manager is the conversation's (`ConversationOpener.sameManager`).
        var sameManager: (_ extensionManager: String?, _ opener: ConversationOpener) -> Bool
        var list: (APIClient) async throws -> [OpenerListing]
        var picture: (APIClient, OpenerIcon) async throws -> Data
        var now: () -> Date = { .now }

        static var live: Deps {
            .init(api: { AppModel.shared.api }, features: { AppModel.shared.hubFeatures },
                  sameManager: { ConversationOpener.sameManager($0, $1.manager, shown: AppModel.shared.assistantManagerID,
                                                                roster: AppModel.shared.managerRoster) },
                  list: { api in try await api.manager(nil).getDecodedOffMain(api.extensionPath(""), as: OpenerListings.self).items },
                  picture: { api, icon in
                      let r = icon.request
                      return try await api.manager(icon.manager).request("GET", r.path, query: r.query)
                  })
        }
    }

    /// One icon as the marks draw it, observed on its own: a picture that comes in redraws only the marks and words of it.
    @Observable final class Drawn {
        fileprivate(set) var image: CGImage?
        @ObservationIgnored fileprivate var pixels = 0
        @ObservationIgnored fileprivate var loading = false
        @ObservationIgnored fileprivate var failed = false
    }

    /// At most this many icons are kept (a few each, about the size of a row's mark).
    static let kept = 64
    /// A list this old is read again when a mark appears.
    static let fresh: TimeInterval = 60

    /// The installed extensions of the hub on screen; nil until read, and on a hub without extensions.
    private(set) var list: [OpenerListing]?
    @ObservationIgnored private let deps: Deps
    @ObservationIgnored private var hub: String?
    @ObservationIgnored private var readAt: Date?
    @ObservationIgnored private var reading: Task<Void, Never>?
    @ObservationIgnored private var icons: [String: Drawn] = [:]

    init(deps: Deps = .live) { self.deps = deps }

    /// Whether the hub on screen lists extensions (its hello's features): a mark drawn before the hello asks again after it.
    var hubListsExtensions: Bool {
        let features = deps.features()
        return features.contains("extensions") || features.contains("plugins")
    }

    /// What the mark of a conversation `opener` opened draws in place of the origin's (`brand`): nil for the origin's.
    func icon(for opener: ConversationOpener, brand: String?) -> OpenerIcon? {
        opener.icon(in: list, brand: brand) { deps.sameManager($0, opener) }
    }

    /// The extension's name, which the words for that mark say while its icon is drawn; nil otherwise (the origin's words).
    func name(for opener: ConversationOpener?, source: AutoConversationSource?) -> String? {
        guard let opener, let source, let icon = icon(for: opener, brand: source.brand) else { return nil }
        return drawn(icon).image == nil ? nil : icon.name
    }

    /// The entry of `icon` on the hub on screen, made when first asked for (unobserved: nothing redraws for it).
    func drawn(_ icon: OpenerIcon) -> Drawn {
        let key = [hub ?? "", icon.manager ?? "", icon.route].joined(separator: "\u{0}")
        if let d = icons[key] { return d }
        if icons.count >= Self.kept { icons.removeAll() }
        let d = Drawn()
        icons[key] = d
        return d
    }

    /// Reads the list for the hub on screen when there is none yet, or the last read is `fresh` old. A hub without
    /// extensions has none, and nothing is asked of it.
    func ensure() {
        guard let api = deps.api() else { return }
        let hub = api.config.url.absoluteString + "|" + api.group
        if hub != self.hub {
            self.hub = hub
            readAt = nil
            reading?.cancel()
            reading = nil
            icons = [:]
            if list != nil { list = nil }
        }
        guard hubListsExtensions, reading == nil,
              readAt.map({ deps.now().timeIntervalSince($0) >= Self.fresh }) ?? true else { return }
        let read = deps.list
        reading = Task {
            let next = try? await read(api)
            guard !Task.isCancelled, self.hub == hub else { return }
            reading = nil
            readAt = deps.now()
            guard let next else { return } // kept as it was; asked again a minute later
            for d in icons.values where d.failed { d.failed = false }
            if next != list { list = next }
        }
    }

    /// Settings › Extensions read its list (after an install, an upgrade or a removal too): the marks read theirs again
    /// the next time one appears.
    func stale() { readAt = nil }

    /// Reads `icon`'s picture for a mark of `pixels` (on its longer side), unless it is there at that size, on its way, or
    /// did not load since the list was read. The read is the store's, not the mark's: it goes on when the mark goes.
    func load(_ icon: OpenerIcon, pixels: Int) {
        let d = drawn(icon)
        guard d.pixels < pixels, !d.loading, !d.failed, let api = deps.api() else { return }
        d.loading = true
        let picture = deps.picture
        Task { [weak d] in
            let data = try? await picture(api, icon)
            let decoded = await Task.detached(priority: .utility) { data.flatMap { OpenerIcon.picture($0, pixels: pixels) } }.value
            guard let d else { return }
            d.loading = false
            if let decoded {
                d.pixels = pixels
                d.image = decoded.image
            } else if d.image == nil {
                d.failed = true
            }
        }
    }
}

/// The mark of a conversation `opener` opened: the extension's own icon once it is drawn, in the box of Lark's mark with
/// an app icon's corner and edge (`BrandMark`'s), else the origin's mark (`fallback`).
struct OpenerMark<Fallback: View>: View {
    var opener: ConversationOpener
    /// The origin's brand (Lark's for Lark /agent): the extension that brings it keeps the origin's mark.
    var brand: String?
    var side: CGFloat
    @ViewBuilder var fallback: Fallback
    @Environment(\.displayScale) private var displayScale
    @Environment(\.dynamicTypeSize) private var typeSize

    /// What the mark asks of the store: the list once the hub says it has extensions, then the icon at the size drawn.
    private struct Want: Hashable {
        var icon: OpenerIcon?
        var pixels: Int
        var listed: Bool
    }

    var body: some View {
        let icons = OpenerIcons.shared
        let icon = icons.icon(for: opener, brand: brand)
        let want = Want(icon: icon, pixels: Int((side * Theme.sizeScale(typeSize) * displayScale).rounded(.up)), listed: icons.hubListsExtensions)
        Group {
            if let icon, let image = icons.drawn(icon).image {
                Image(decorative: image, scale: displayScale).resizable().interpolation(.high).scaledToFill()
                    .scaledFrame(width: side, height: side)
                    .clipShape(AppIconShape())
                    .overlay(AppIconShape().stroke(Theme.cardStroke, lineWidth: Theme.Size.hairline))
                    .accessibilityHidden(true)
            } else {
                fallback
            }
        }
        .task(id: want) {
            icons.ensure()
            if let icon = want.icon { icons.load(icon, pixels: want.pixels) }
        }
    }
}

extension View {
    /// A row's VoiceOver label: `before`, the words of its source's mark (`AutoConversationSource.markLabel`, the name of
    /// the extension that opened it while that one's icon is drawn, #10358) and `after`, the ones there joined by
    /// `separator`.
    func sourceLabel(_ before: [String?], source: AutoConversationSource?, opener: ConversationOpener?, _ after: [String?],
                     separator: String) -> some View {
        modifier(SourceAccessibilityLabel(before: before, source: source, opener: opener, after: after, separator: separator))
    }
}

/// `sourceLabel`'s: the icon's state is read here, not by the row's page.
private struct SourceAccessibilityLabel: ViewModifier {
    var before: [String?]
    var source: AutoConversationSource?
    var opener: ConversationOpener?
    var after: [String?]
    var separator: String

    func body(content: Content) -> some View {
        let words = source.map { $0.markLabel(opener: OpenerIcons.shared.name(for: opener, source: $0)) }
        content.accessibilityLabel((before + [words] + after).compactMap { $0 }.joined(separator: separator))
    }
}
