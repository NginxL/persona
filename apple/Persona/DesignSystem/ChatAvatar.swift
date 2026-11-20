import SwiftUI

/// A person's or a conversation's picture from the hub's avatars route (an item's `avatar_url`, docs/protocol/protocol.md
/// "Avatars", #10923): the initial on its disc at once and while the picture loads, the picture over it once it is in,
/// and the initial when there is none or it cannot be loaded. The picture is asked at the size drawn (points × the
/// display's scale, rounded up to the sizes the route draws), decoded off the main thread and kept by `LarkMedia` in
/// memory and on disk, so a row scrolled back draws it without asking the hub again (docs/apple/performance.md P8).
/// Hidden from VoiceOver: the row beside it names whose it is.
struct ChatAvatar: View {
    /// The hub path (`avatar_url`); nil or empty: the initial alone.
    var url: String?
    /// Whose initial, and its disc's hue (`Theme.monogram`).
    var name: String
    var size: CGFloat = Theme.Size.avatar
    /// The hub and manager owning this generic messaging row.
    var origin: APIClient? = nil
    @Environment(\.displayScale) private var displayScale
    private struct Request: Equatable {
        let path: String?
        let edge: Int
        let hub: URL?
        let group: String?
    }
    @State private var loaded: (request: Request, image: UIImage)?

    var body: some View {
        let disc = Theme.monogram(name)
        let edge = Self.edge(pixels: size * displayScale)
        let request = Request(path: url, edge: edge, hub: origin?.config.url, group: origin?.group)
        Monogram(name: name, size: size, fill: disc.fill, ink: disc.ink, font: LarkAvatar.font(size))
            .overlay {
                if let loaded, loaded.request == request {
                    Image(uiImage: loaded.image).resizable().scaledToFill()
                        .clipShape(.circle)
                        .overlay(Circle().strokeBorder(Color.primary.opacity(0.06)))
                }
            }
            .task(id: request) {
                guard let path = request.path, !path.isEmpty else {
                    loaded = nil
                    return
                }
                if let kept = LarkMedia.shared.cached(path, size: request.edge, using: origin) {
                    loaded = (request, kept)
                    return
                }
                loaded = nil
                if let image = await LarkMedia.shared.image(path, size: request.edge, maxPixel: CGFloat(request.edge), using: origin), !Task.isCancelled {
                    loaded = (request, image)
                }
            }
            .accessibilityHidden(true)
    }

    /// The sizes the avatars route draws (remoteimage's Avatars profile): a picture is asked at the smallest that
    /// covers the pixels drawn, so each picture has one of a few keys in the caches.
    nonisolated static let edges = [64, 128, 256, 512]

    nonisolated static func edge(pixels: CGFloat) -> Int {
        edges.first { CGFloat($0) >= pixels } ?? edges[edges.count - 1]
    }
}
