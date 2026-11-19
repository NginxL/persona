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
    @Environment(\.displayScale) private var displayScale
    @State private var image: UIImage?

    var body: some View {
        let disc = Theme.monogram(name)
        Monogram(name: name, size: size, fill: disc.fill, ink: disc.ink, font: LarkAvatar.font(size))
            .overlay {
                if let image {
                    Image(uiImage: image).resizable().scaledToFill()
                        .clipShape(.circle)
                        .overlay(Circle().strokeBorder(Color.primary.opacity(0.06)))
                }
            }
            .task(id: url) {
                guard let url, !url.isEmpty else {
                    image = nil
                    return
                }
                let edge = Self.edge(pixels: size * displayScale)
                if let kept = LarkMedia.shared.cached(url, size: edge) {
                    image = kept
                    return
                }
                image = nil
                image = await LarkMedia.shared.image(url, size: edge, maxPixel: CGFloat(edge))
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
