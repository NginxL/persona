import AVKit
import CryptoKit
import ImageIO
import QuickLook
import SwiftUI
import PersonaWire
import UIKit

/// Loads Feishu media through the hub (`/api/v1/lark/media/...`, bearer auth) or straight from a CDN URL.
/// Media keys never change content, so decoded images live in memory and the raw bytes on disk.
@MainActor @Observable
final class LarkMedia {
    static let shared = LarkMedia()

    /// Bumped whenever an inline emoji image arrives, so text that drew `[label]` redraws with the picture.
    private(set) var emojiVersion = 0
    /// Each manager's emoji, by manager ("" the default's): each manager reads its own Lark (docs/assistant/managers.md
    /// "Clients").
    private(set) var catalogs: [String: EmojiCatalog] = [:]

    /// What the emoji of one manager's Lark are.
    struct EmojiCatalog {
        var emojis: [String: APIClient.LarkEmojis.Emoji] = [:]
        /// The picker's sections (Default, provider…), keys in order.
        var groups: [APIClient.LarkEmojis.Group] = []
        /// The user's quick reactions (most used first).
        var quickReactions: [String] = ["THUMBSUP", "OK", "DONE", "HEART", "SMILE", "LAUGH", "SURPRISED"]
    }

    @ObservationIgnored private let images = NSCache<NSString, UIImage>()
    @ObservationIgnored private var inflight: [String: Task<UIImage?, Never>] = [:]
    @ObservationIgnored private var emojiImages: [String: UIImage] = [:]
    @ObservationIgnored private var emojiLoading = Set<String>()
    @ObservationIgnored private var catalogTasks: [String: Task<Void, Never>] = [:]
    @ObservationIgnored private let dir = URL.cachesDirectory.appending(path: "lark-media", directoryHint: .isDirectory)

    /// The app uses `shared` only; tests make their own.
    init() { images.totalCostLimit = 120 << 20 }

    /// Settings › Storage cleared the files: drop the decoded copies too (emoji stay, they are tiny and on every page).
    func clearMemory() { images.removeAllObjects() }

    // MARK: Bytes

    /// Raw bytes of a media URL (hub-relative or absolute), from disk when seen before.
    func data(_ url: String, size: Int? = nil, using origin: APIClient? = nil) async throws -> Data {
        let key = Self.cacheKey(url, size: size, origin: origin)
        let file = dir.appending(path: key)
        if let d = await Task.detached(priority: .userInitiated, operation: { try? Data(contentsOf: file) }).value { return d }
        let d: Data
        if url.hasPrefix("http://") || url.hasPrefix("https://") {
            guard let u = URL(string: url) else { throw URLError(.badURL) }
            let (body, resp) = try await APIClient.uncachedSession.data(from: u) // kept in `dir`, not twice
            guard ((resp as? HTTPURLResponse)?.statusCode ?? 200) < 400 else { throw URLError(.badServerResponse) }
            d = body
        } else {
            guard var api = origin ?? AppModel.shared.api else { throw URLError(.userAuthenticationRequired) }
            api.session = APIClient.uncachedSession
            var path = url
            var query: [String: String] = [:]
            if let q = url.firstIndex(of: "?") {
                path = String(url[..<q])
                for item in URLComponents(string: url)?.queryItems ?? [] { query[item.name] = item.value ?? "" }
            }
            if let size { query["size"] = String(size) }
            d = try await api.request("GET", path, query: query, timeout: 120)
        }
        let dir = dir
        await Task.detached(priority: .utility) {
            try? FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
            try? d.write(to: file, options: .atomic)
        }.value
        return d
    }

    /// A local file for QuickLook / AVPlayer, named after the original.
    func file(_ url: String, name: String) async throws -> URL {
        let d = try await data(url)
        let folder = URL.temporaryDirectory.appending(path: "lark-" + cacheKey(url, size: nil), directoryHint: .isDirectory)
        let safe = name.replacingOccurrences(of: "/", with: "_")
        let f = folder.appending(path: safe.isEmpty ? "file" : safe)
        try await Task.detached(priority: .userInitiated) {
            try FileManager.default.createDirectory(at: folder, withIntermediateDirectories: true)
            if !FileManager.default.fileExists(atPath: f.path) { try d.write(to: f) }
        }.value
        return f
    }

    // MARK: Images

    func cached(_ url: String, size: Int? = nil, using origin: APIClient? = nil) -> UIImage? {
        images.object(forKey: Self.cacheKey(url, size: size, origin: origin) as NSString)
    }

    /// Decoded image, downsampled to `maxPixel` on its long edge (thumbnails are fetched at `size` when the hub can).
    func image(_ url: String, size: Int? = nil, maxPixel: CGFloat = 1600, using origin: APIClient? = nil) async -> UIImage? {
        let key = Self.cacheKey(url, size: size, origin: origin)
        if let img = images.object(forKey: key as NSString) { return img }
        if let t = inflight[key] { return await t.value }
        let t = Task<UIImage?, Never> {
            guard let d = try? await self.data(url, size: size, using: origin) else { return nil }
            return await Task.detached(priority: .userInitiated) { Self.decode(d, maxPixel: maxPixel) }.value
        }
        inflight[key] = t
        let img = await t.value
        inflight[key] = nil
        if let img { images.setObject(img, forKey: key as NSString, cost: Int(img.size.width * img.size.height * img.scale * img.scale * 4)) }
        return img
    }

    nonisolated static func decode(_ data: Data, maxPixel: CGFloat) -> UIImage? {
        guard let src = CGImageSourceCreateWithData(data as CFData, nil) else { return UIImage(data: data) }
        let opts: [CFString: Any] = [kCGImageSourceCreateThumbnailFromImageAlways: true, kCGImageSourceCreateThumbnailWithTransform: true,
                                     kCGImageSourceThumbnailMaxPixelSize: maxPixel, kCGImageSourceShouldCacheImmediately: true]
        guard let cg = CGImageSourceCreateThumbnailAtIndex(src, 0, opts as CFDictionary) else { return UIImage(data: data) }
        return UIImage(cgImage: cg)
    }

    private func cacheKey(_ url: String, size: Int?) -> String { Self.cacheKey(url, size: size) }

    /// The name the bytes of `url` are kept under: a URL that names a manager (`url(_:manager:)`) is its own entry.
    nonisolated static func cacheKey(_ url: String, size: Int?, origin: APIClient? = nil) -> String {
        let resource = url + "#" + String(size ?? 0)
        let key: String
        if let origin {
            key = [origin.group, origin.config.url.absoluteString, origin.managerID ?? "", resource].joined(separator: "\0")
        } else {
            key = resource
        }
        let digest = SHA256.hash(data: Data(key.utf8))
        return digest.prefix(16).map { String(format: "%02x", $0) }.joined()
    }

    /// `url` as manager `manager` reads it (docs/assistant/managers.md "Clients"): a hub path gets `manager=`, as a picture
    /// or file loaded by URL can't send the header. As it is for nil (the default's, nothing named), a full URL (a CDN's)
    /// and one that names a manager already.
    nonisolated static func url(_ url: String, manager: String?) -> String {
        guard let manager, !manager.isEmpty, url.hasPrefix("/"), !url.hasPrefix("//"),
              URLComponents(string: url)?.queryItems?.contains(where: { $0.name == "manager" }) != true else { return url }
        let hash = url.firstIndex(of: "#") ?? url.endIndex
        var head = String(url[..<hash])
        head += head.contains("?") ? "&" : "?"
        let value = manager.addingPercentEncoding(withAllowedCharacters: unreserved) ?? manager
        return head + "manager=" + value + String(url[hash...])
    }

    /// Where the player streams `url` from: a hub path under the hub's address with its query kept, a named manager's as
    /// `url(_:manager:)` names it (AVPlayer sends no header but the bearer), through the hub's tunnel or proxy when the
    /// app routes it there (HubTransport); a full URL as it is.
    nonisolated static func streamURL(_ url: String, hub: URL, manager: String?) -> URL {
        guard url.hasPrefix("/") else { return URL(string: url) ?? hub }
        guard !url.hasPrefix("//"), let named = URLComponents(string: Self.url(url, manager: manager)),
              var c = URLComponents(url: hub, resolvingAgainstBaseURL: false) else { return HubTransport.resolve(hub.appending(path: url), hub: hub) }
        var base = c.percentEncodedPath
        while base.hasSuffix("/") { base.removeLast() }
        c.percentEncodedPath = base + named.percentEncodedPath
        c.percentEncodedQuery = named.percentEncodedQuery
        return HubTransport.resolve(c.url ?? hub.appending(path: url), hub: hub)
    }

    private nonisolated static let unreserved = CharacterSet(charactersIn: "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-._~")

    // MARK: Emoji

    /// Loads manager `manager`'s emoji catalogue once (key → image URL), so reactions and inline emoji can be drawn; nil:
    /// the default's. Read through `api` (the group the page talks to), the hub-level link's when nil.
    @discardableResult
    func loadEmojiCatalog(manager: String? = nil, using api: APIClient? = nil) -> Task<Void, Never>? {
        let slot = manager ?? ""
        if let t = catalogTasks[slot] { return t }
        guard let api = (api ?? AppModel.shared.api)?.manager(manager) else { return nil }
        let t = Task {
            guard let r = try? await api.larkEmojis() else { catalogTasks[slot] = nil; return }
            catalogs[slot, default: EmojiCatalog()].emojis = r.emojis
            catalogs[slot]?.groups = r.groups ?? []
            emojiVersion += 1
            if let q = try? await api.larkQuickReactions(), !q.isEmpty { catalogs[slot]?.quickReactions = q }
            // The long-press bar's pictures, ahead of the first long press: a context menu is drawn once when it opens,
            // so one opened before they loaded showed [HEART]-style codes, and only the three of them that fit (#1627).
            for key in LarkQuickReactions.bar(catalog(manager).quickReactions) { _ = emojiImage(key, points: LarkMedia.barPoints, manager: manager) }
        }
        catalogTasks[slot] = t
        return t
    }

    /// The long-press bar's emoji size (LarkChatView's menu).
    static let barPoints: CGFloat = 28

    /// Manager `manager`'s catalogue (nil: the default's); empty, with the stock quick reactions, until it loads.
    func catalog(_ manager: String?) -> EmojiCatalog { catalogs[manager ?? ""] ?? EmojiCatalog() }

    func emoji(_ key: String, manager: String? = nil) -> APIClient.LarkEmojis.Emoji? { catalog(manager).emojis[key.lowercased()] }

    /// An emoji of manager `manager`'s catalogue drawn at `points` for inline text, or nil while it loads (a redraw follows
    /// via `emojiVersion`).
    func emojiImage(_ key: String, points: CGFloat, manager: String? = nil) -> UIImage? {
        let k = key.lowercased() + "@" + String(Int(points)) + (manager.map { "@" + $0 } ?? "")
        if let img = emojiImages[k] { return img }
        guard let url = emoji(key, manager: manager)?.url, !emojiLoading.contains(k) else { return nil }
        emojiLoading.insert(k)
        Task {
            if let img = await image(Self.url(url, manager: manager), maxPixel: 96) {
                emojiImages[k] = Self.fitted(img, points: points)
                emojiVersion += 1
            }
        }
        return nil
    }

    /// Redraws an image at a fixed point size so `Text(Image(uiImage:))` lays it out like a glyph.
    static func fitted(_ img: UIImage, points: CGFloat) -> UIImage {
        let size = CGSize(width: points, height: points)
        let ratio = min(points / max(img.size.width, 1), points / max(img.size.height, 1))
        let draw = CGSize(width: img.size.width * ratio, height: img.size.height * ratio)
        return UIGraphicsImageRenderer(size: size).image { _ in
            img.draw(in: CGRect(x: (points - draw.width) / 2, y: (points - draw.height) / 2, width: draw.width, height: draw.height))
        }
    }
}

/// An image from Feishu with a quiet placeholder while it loads and a broken-image mark when it fails. In a chat, a hub
/// path is loaded through the chat's manager (`larkManager`, `LarkMedia.url(_:manager:)`), avatars too.
struct LarkRemoteImage: View {
    var url: String
    var size: Int? = nil
    var maxPixel: CGFloat = 1600
    var contentMode: ContentMode = .fill
    /// Called when the image cannot be loaded, for callers that draw something else instead of the broken mark.
    var onFailure: (() -> Void)? = nil
    @Environment(\.larkManager) private var manager
    @State private var image: UIImage?
    @State private var failed = false

    var body: some View {
        let url = LarkMedia.url(self.url, manager: manager)
        ZStack {
            if let image {
                Image(uiImage: image).resizable().aspectRatio(contentMode: contentMode)
            } else if failed {
                Rectangle().fill(Color.primary.opacity(0.05))
                    .overlay { Image(systemName: Theme.Icon.brokenImage).font(Theme.Font.title).foregroundStyle(.tertiary) }
            } else {
                Rectangle().fill(Color.primary.opacity(0.05)).overlay { ProgressView().controlSize(.small) }
            }
        }
        .task(id: url) {
            if let img = LarkMedia.shared.cached(url, size: size) { image = img; return }
            image = await LarkMedia.shared.image(url, size: size, maxPixel: maxPixel)
            failed = image == nil
            if failed { onFailure?() }
        }
    }
}

/// Hub thumbnails come in steps of 24 px up to 624 (the Lark gateway's `MEDIA_THUMBNAIL_EDGES`); larger is the original.
nonisolated func larkThumbEdge(points: CGFloat) -> Int? {
    let edge = Int((points * 3 / 24).rounded(.up)) * 24
    return edge > 0 && edge <= 624 ? edge : nil
}

/// A Feishu row owns the account, hub and manager used to resolve its picture. ChatAvatar draws and caches it.
struct LarkAvatarImage: View {
    var name: String
    var url: String?
    var account: Int?
    var size: CGFloat = Theme.Size.wellL
    var origin: APIClient?
    @Environment(\.dynamicTypeSize) private var typeSize
    @Environment(\.larkAccount) private var chatAccount
    @Environment(\.larkManager) private var manager
    @Environment(\.larkGroup) private var group

    var body: some View {
        let slot = Self.slot(account) ?? Self.slot(chatAccount)
        let api = origin ?? AppModel.shared.larkAPI(group: group, manager: manager, account: slot)
        let picture = api.flatMap { api in url.flatMap { Self.cachedPicture($0, using: api, account: slot) } }
        ChatAvatar(url: picture, name: name, size: size * Theme.sizeScale(typeSize), origin: api)
    }

    /// Cached rows from older hubs can contain a gateway path or a signed Feishu CDN link. Resolve the key through
    /// this row's hub. Registered messaging sources retain their own media and account namespaces.
    static func cachedPicture(_ raw: String, using api: APIClient, account: Int? = nil) -> String? {
        guard !raw.isEmpty, let url = URLComponents(string: raw), url.fragment == nil else { return nil }
        let path: String?
        if raw.hasPrefix("/api/v1/avatars/lark/"), let slot = url.queryItems?.first(where: { $0.name == "lark_account" }) {
            let key = url.percentEncodedPath.dropFirst("/api/v1/avatars/lark/".count)
            guard let number = Self.slot(slot.value.flatMap(Int.init)), !key.isEmpty, key.allSatisfy({ $0.isASCII && ($0.isLetter || $0.isNumber || $0 == "-" || $0 == "_") }) else { return nil }
            path = url.percentEncodedPath + "?lark_account=" + String(number)
        } else if !(raw.hasPrefix("media/") && api.messagingSource != nil),
                  let legacy = legacyPath(raw, url: url, account: account ?? api.larkAccount) {
            // Native keys and known Feishu CDN references can share a manager with a messaging source.
            path = legacy
        } else if let source = api.messagingSource {
            if raw.hasPrefix("/api/v1/messaging/" + source + "/media/") || raw.hasPrefix("/api/v1/avatars/" + source + "/") {
                // A stored row's manager is supplied by the owning API client on every request.
                var named = url
                named.queryItems = url.queryItems?.filter { $0.name != "manager" && $0.name != "size" }
                if named.queryItems?.isEmpty == true { named.queryItems = nil }
                path = named.string
            } else {
                path = MessagingShape.avatar(["avatar": raw], source: source, account: account.map(String.init))
            }
        } else {
            return nil
        }
        return path.map { LarkMedia.url($0, manager: api.managerID) }
    }

    private static func slot(_ number: Int?) -> Int? {
        guard let number, (1...999_999_999).contains(number) else { return nil }
        return number
    }

    private static let legacyMediaPrefixes = ["/api/v1/lark/media/", "/agent/v1/media/", "/api/media/"]

    private static func legacyPath(_ raw: String, url: URLComponents, account: Int?) -> String? {
        let key: String
        var slot = Self.slot(account)
        if let prefix = legacyMediaPrefixes.first(where: raw.hasPrefix) {
            guard let value = String(url.percentEncodedPath.dropFirst(prefix.count)).removingPercentEncoding else { return nil }
            key = value
            if slot == nil, let value = url.queryItems?.first(where: { $0.name == "ol_account" })?.value { slot = Self.slot(Int(value)) }
        } else if url.scheme?.lowercased() == "https", url.user == nil, url.password == nil, let host = url.host?.lowercased(),
                  ["feishucdn.com", "larksuitecdn.com", "feishu.cn", "larksuite.com", "larkoffice.com"].contains(where: { host == $0 || host.hasSuffix("." + $0) }),
                  let range = url.percentEncodedPath.range(of: "/static-resource/v1/"),
                  let value = url.percentEncodedPath[range.upperBound...].prefix(while: { $0 != "/" && $0 != "~" }).removingPercentEncoding {
            key = value
        } else {
            key = raw
        }
        let parts = key.split(separator: "/", omittingEmptySubsequences: false)
        guard !key.isEmpty, key.utf8.count <= 500, parts.allSatisfy({ !$0.isEmpty && $0 != "." && $0 != ".." }),
              key.allSatisfy({ $0.isASCII && ($0.isLetter || $0.isNumber || "_./:+-".contains($0)) }),
              let encoded = key.addingPercentEncoding(withAllowedCharacters: .urlPathAllowed.subtracting(CharacterSet(charactersIn: "/"))) else { return nil }
        return "/api/v1/lark/media/" + encoded + (slot.map { "?ol_account=\($0)" } ?? "")
    }
}

// MARK: - Viewers

/// Plays a Feishu video or voice message through the hub (the bearer header rides on the asset request), in a chat
/// through the chat's manager (`larkManager`).
struct LarkPlayerView: View {
    var url: String
    @Environment(\.dismiss) private var dismiss
    @Environment(\.larkManager) private var manager
    @State private var player: AVPlayer?

    var body: some View {
        ZStack(alignment: .topLeading) {
            Color.black.ignoresSafeArea()
            if let player {
                VideoPlayer(player: player).ignoresSafeArea()
            } else {
                ProgressView().tint(.white).frame(maxWidth: .infinity, maxHeight: .infinity)
            }
            Button { dismiss() } label: {
                ThemedIcon(Theme.Icon.close).font(Theme.Font.heading).sheetBarLabel()
                    .scaledFrame(width: Theme.Size.avatar, height: Theme.Size.avatar)
            }
                .themedButton()
                .clipShape(.circle)
                .padding(Theme.Space.l)
                .accessibilityLabel("Close")
        }
        .task {
            guard let cfg = AppModel.shared.config else { return }
            let full = LarkMedia.streamURL(url, hub: cfg.url, manager: manager)
            var auth = URLRequest(url: full)
            cfg.authorize(&auth) // the Access token only when `full` is on the hub's own address
            let asset = AVURLAsset(url: full, options: ["AVURLAssetHTTPHeaderFieldsKey": auth.allHTTPHeaderFields ?? [:]])
            let p = AVPlayer(playerItem: AVPlayerItem(asset: asset))
            player = p
            p.play()
        }
        .onDisappear { player?.pause() }
    }
}

/// Downloads a Feishu file and opens it in Quick Look (which also offers share / save to Files), in a chat through the
/// chat's manager (`larkManager`).
struct LarkFileOpener: ViewModifier {
    @Binding var request: (url: String, name: String)?
    @Environment(\.larkManager) private var manager
    @State private var local: URL?
    @State private var loading = false

    func body(content: Content) -> some View {
        content
            .quickLookPreview($local)
            .overlay {
                if loading {
                    ProgressView(String(localized: "Downloading…")).padding(Theme.Space.xl).surface(in: .rect(cornerRadius: Theme.Radius.card))
                }
            }
            .onChange(of: request?.url) {
                guard let r = request else { return }
                request = nil
                loading = true
                let url = LarkMedia.url(r.url, manager: manager)
                Task {
                    defer { loading = false }
                    do { local = try await LarkMedia.shared.file(url, name: r.name) } catch { AppModel.shared.show(error) }
                }
            }
    }
}
