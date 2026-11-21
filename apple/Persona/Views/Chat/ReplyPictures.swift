import MarkdownUI
import PersonaKit
import SwiftUI
#if canImport(UIKit)
import UIKit
#endif

// The pictures in a reply, drawn to fit the text (#9474; docs/design/DESIGN_SYSTEM.md "Pictures in replies"; the web's
// are the same rules, #9475). `ReplyImages` turns each run of pictures on their own lines into a `persona-images`
// fence, which the theme's code block draws as `ReplyPictureRun`: one picture a fixed height and its shape's width, or
// squares (two side by side, three to a row, at most two rows, the sixth counting the rest). The box is its final size
// before the picture arrives; a tap opens the viewer on every picture of the reply. Pictures load through the hub
// (`ReplyPictureSource`), the size they are drawn.

/// Where a reply's pictures are loaded from: the hub's remote image route when it has one (`GET /api/v1/remote-image`,
/// #9473; the host sees the hub, never this device, and the hub sends the size asked for), else the address itself,
/// as before. `LarkMedia` loads either (a hub path with the device's credentials) and keeps them.
enum ReplyPictureSource {
    static let feature = "remote_images"
    /// The size the viewer asks for.
    static let viewerSize = 2048

    /// What `LarkMedia` loads for `url` drawn at `size` pixels on its long side.
    static func path(_ url: String, size: Int) -> String {
        guard ReplyImages.isWeb(url), AppModel.shared.hubFeatures.contains(feature) else { return url }
        return "/api/v1/remote-image?url=\(escape(url))&size=\(size)"
    }

    /// Every character but the unreserved ones escaped, so `&`, `=`, `#` and `+` in the address reach the hub as
    /// written (`APIClient` escapes the query again, but leaves `+`; the hub keeps it as a plus).
    nonisolated static func escape(_ s: String) -> String {
        s.addingPercentEncoding(withAllowedCharacters: unreserved) ?? s
    }

    private nonisolated static let unreserved = CharacterSet(charactersIn: "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-._~")

    /// View and task identity: the same relative path in another chat or working directory is another image.
    struct Identity: Hashable {
        var url: String
        var scope: FileScope?
    }
    struct TaskIdentity: Hashable {
        var image: Identity
        var path: String
        var file: FileRef?
        var size: Int
        var attempt: Int
    }

    /// The machine file behind a local Markdown destination. A local path without a scope never becomes a hub-relative
    /// LarkMedia request: scope-less surfaces keep their unsupported-image fallback instead.
    static func file(_ image: ReplyImage, scope: FileScope?) -> FileRef? {
        guard let path = image.localPath, let scope else { return nil }
        return FileRef(group: scope.machine.group, machineID: scope.machine.id, path: path, cwd: scope.cwd)
    }

    /// The site a picture comes from, for its menu and the viewer.
    nonisolated static func host(_ url: String) -> String { URL(string: url)?.host() ?? "" }

    /// The viewer's pages for a reply's pictures: each at the viewer's size, its words as the title, its site below.
    static func viewerItems(_ images: [ReplyImage], scope: FileScope?) -> [ViewerImage] {
        images.map { image in
            if image.isLocal {
                if let ref = file(image, scope: scope) {
                    // `file` keeps Copy Path / Open in Files; `download` selects the complete machine-file image loader.
                    return ViewerImage(file: ref, download: ref, title: image.alt.isEmpty ? nil : image.alt, detail: image.url)
                }
                // Never turn a scope-less local path into a hub-relative URL request.
                return ViewerImage(title: image.alt.isEmpty ? nil : image.alt, detail: image.url)
            }
            return ViewerImage(url: path(image.url, size: viewerSize), title: image.alt.isEmpty ? nil : image.alt, detail: host(image.url))
        }
    }
}

/// A run of a reply's pictures (`ReplyImageBlock`): one, or squares. A tap opens the viewer on all of the reply's.
struct ReplyPictureRun: View {
    var block: ReplyImageBlock
    @Environment(\.asksBeforePictures) private var asks
    @Environment(\.fileScope) private var scope
    @State private var asked = false
    @State private var opened: Opened?
    @Namespace private var zoom

    private struct Opened: Identifiable {
        let index: Int
        var id: Int { index }
    }

    var body: some View {
        Group {
            if asks, !asked {
                AskedPictures(block: block) { asked = true }
            } else if block.shown.count == 1 {
                let image = block.all[block.shown.lowerBound]
                let identity = ReplyPictureSource.Identity(url: image.url, scope: scope)
                ReplySinglePicture(image: image, scope: scope) { opened = Opened(index: block.shown.lowerBound) }
                    .id(identity)
                    .matchedTransitionSource(id: block.shown.lowerBound, in: zoom)
            } else {
                ReplyPictureGrid(block: block, scope: scope, zoom: zoom) { opened = Opened(index: $0) }
                    .id(ReplyPictureSource.Identity(url: block.all.map(\.url).joined(separator: "\u{1F}"), scope: scope))
            }
        }
        .fullScreenCover(item: $opened) { o in
            ImageViewer(items: ReplyPictureSource.viewerItems(block.all, scope: scope), index: o.index)
                .navigationTransition(.zoom(sourceID: o.index, in: zoom))
        }
    }
}

/// A lone picture: `Theme.Size.replyPicture` tall, as wide as its shape from 3:4 to the column; smaller pictures keep
/// their size; one that did not load is a small tile that retries.
private struct ReplySinglePicture: View {
    var image: ReplyImage
    var scope: FileScope?
    var open: () -> Void
    @Environment(\.displayScale) private var scale
    @State private var arrived: CGSize?
    @State private var failed = false
    @State private var failure: String?

    /// The hub's size: the 4:3 box at the screen's scale (a wider picture is cut to the column, a narrower one is shorter).
    private var size: Int { ReplyImageLayout.size(points: Theme.Size.replyPicture * ReplyImageLayout.placeholder, scale: scale) }

    var body: some View {
        let path = ReplyPictureSource.path(image.url, size: size)
        let file = ReplyPictureSource.file(image, scope: scope)
        let cached = file.flatMap { ChatImageLoader.cached($0, side: CGFloat(size))?.pixelSize }
            ?? (image.isLocal ? nil : LarkMedia.shared.cached(path).map(\.pixelSize))
        VStack(alignment: .leading, spacing: Theme.Space.s) {
            ReplySingleLayout(pixels: arrived ?? cached, failed: failed) {
                ReplyPictureTile(image: image, path: path, file: file, identity: ReplyPictureSource.Identity(url: image.url, scope: scope), size: size, open: open) { pixels, why in
                    arrived = pixels
                    failed = pixels == nil
                    failure = why
                }
            }
            if let failure {
                Text(verbatim: failure)
                    .font(Theme.Font.label)
                    .foregroundStyle(.secondary)
                    .fixedSize(horizontal: false, vertical: true)
                    .accessibilityIdentifier("reply-picture-failure-reason")
            }
        }
        .animation(Theme.Motion.state, value: arrived)
    }
}

/// Two or more pictures as squares (`ReplyImageLayout.grid`); the last one drawn counts those that are not.
private struct ReplyPictureGrid: View {
    var block: ReplyImageBlock
    var scope: FileScope?
    var zoom: Namespace.ID
    var open: (Int) -> Void
    @Environment(\.displayScale) private var scale
    @State private var failures: [Int: String] = [:]

    var body: some View {
        let grid = ReplyImageLayout.grid(count: block.shown.count, column: Theme.Size.replyPictures, widest: Theme.Size.replyPictures, gap: Theme.Space.replyPictureGap)
        // The size for the largest square a column can give (two side by side), a square's picture filling it.
        let size = ReplyImageLayout.size(points: grid.tile * ReplyImageLayout.placeholder, scale: scale)
        VStack(alignment: .leading, spacing: Theme.Space.s) {
            ReplyGridLayout(count: block.shown.count) {
                ForEach(Array(block.shown.prefix(grid.shown).enumerated()), id: \.element) { n, index in
                    let image = block.all[index]
                    let identity = ReplyPictureSource.Identity(url: image.url, scope: scope)
                    ReplyPictureTile(image: image, path: ReplyPictureSource.path(image.url, size: size),
                                     file: ReplyPictureSource.file(image, scope: scope), identity: identity, size: size,
                                     open: { open(index) }, arrived: { _, why in failures[index] = why })
                        .id(identity)
                        .overlay {
                            if n == grid.shown - 1, grid.more > 0 { MoreTile(count: grid.more) }
                        }
                        .matchedTransitionSource(id: index, in: zoom)
                }
            }
            // Small squares cannot carry a sentence, especially at accessibility text sizes. Keep their retry action,
            // and put every complete machine-file reason below the unchanged grid.
            ForEach(failures.sorted(by: { $0.key < $1.key }), id: \.key) { index, reason in
                Text(verbatim: reason)
                    .font(Theme.Font.label)
                    .foregroundStyle(.secondary)
                    .fixedSize(horizontal: false, vertical: true)
                    .accessibilityIdentifier("reply-picture-failure-reason-\(index)")
            }
        }
    }
}

/// "+N" over the last square drawn.
private struct MoreTile: View {
    var count: Int

    var body: some View {
        ZStack {
            Theme.Fill.morePictures
            Text(verbatim: "+\(count)").font(Theme.Font.title).foregroundStyle(.white)
        }
        .clipShape(.rect(cornerRadius: Theme.Radius.control, style: .continuous))
        .allowsHitTesting(false)
        .accessibilityIdentifier("reply-picture-more")
        .accessibilityLabel(Text("\(count) more"))
    }
}

/// One picture filling the frame it is given: the well's color while it comes, then the picture (cut to the frame);
/// one that did not load says why and retries only after a tap. A tap on a picture opens the viewer; a long press offers
/// the original on its site or in the machine's file viewer.
private struct ReplyPictureTile: View {
    var image: ReplyImage
    var path: String
    var file: FileRef?
    var identity: ReplyPictureSource.Identity
    var size: Int
    var open: () -> Void
    /// Its pixels once it arrived, and a machine-file failure reason when it did not (the lone picture's frame follows them).
    var arrived: ((CGSize?, String?) -> Void)? = nil
    @Environment(\.openURL) private var openURL
    @State private var picture: UIImage?
    @State private var failed = false
    @State private var failure: String?
    @State private var attempt = 0
    private let retries = ImageRetries.shared

    private var fileAttempt: Int { file.map { retries.attempt($0) } ?? attempt }
    private var isFailed: Bool { file.map { retries.didFail($0) } ?? failed }
    private var failureReason: String? { file.flatMap { retries.failure($0) } ?? failure }
    private var retryHint: Text {
        if let failureReason { return Text(verbatim: failureReason) }
        return Text("Image didn't load. Tap to retry")
    }

    var body: some View {
        let shape = RoundedRectangle(cornerRadius: Theme.Radius.control, style: .continuous)
        let cached = file.flatMap { ChatImageLoader.cached($0, side: CGFloat(size)) }
            ?? (image.isLocal ? nil : LarkMedia.shared.cached(path))
        let shown = picture ?? cached
        Button {
            if isFailed {
                if let file {
                    if retries.retry(file) {
                        failed = false
                        failure = nil
                    }
                } else {
                    failed = false
                    failure = nil
                    attempt += 1
                }
            } else if shown != nil {
                open()
            }
        } label: {
            // The well's color takes the frame it is given; the picture fills it from an overlay and is cut to it (in a
            // ZStack a filled picture is as large as it fills, and a square was as tall as a portrait picture).
            Theme.Fill.well
                .overlay {
                    if let shown {
                        Image(uiImage: shown).resizable().scaledToFill()
                    } else if isFailed {
                        ImageNotLoaded(canRetry: true)
                    }
                }
                .clipShape(shape)
            .overlay(shape.stroke(Theme.cardStroke, lineWidth: Theme.Size.hairline))
            .contentShape(shape)
        }
        .buttonStyle(.plain)
        .accessibilityIdentifier(isFailed ? "reply-picture-failed" : "reply-picture")
        .accessibilityLabel(image.alt.isEmpty ? Text("Photo") : Text(verbatim: image.alt))
        .accessibilityValue(isFailed ? Text(verbatim: failureReason ?? "") : Text(""))
        .accessibilityHint(isFailed ? retryHint : Text("Image, tap to enlarge"))
        .contextMenu {
            if let url = URL(string: image.url) {
                Button { openURL(url) } label: { Label("Open original", systemImage: Theme.Icon.safari) }
            }
        }
        .animation(Theme.Motion.state, value: picture != nil)
        .task(id: ReplyPictureSource.TaskIdentity(image: identity, path: path, file: file, size: size, attempt: fileAttempt)) {
            if let file {
                guard !retries.didFail(file) else {
                    // A recreated failure tile shows the remembered reason without another machine-file request.
                    picture = nil
                    failed = true
                    failure = retries.failure(file)
                    arrived?(nil, failure)
                    return
                }
                do {
                    let loaded = try await ChatImageLoader.image(file, side: CGFloat(size))
                    guard !Task.isCancelled else { return }
                    let why = loaded == nil ? String(localized: "Can't show this image") : nil
                    retries.loaded(file, reason: why)
                    picture = loaded
                    failed = loaded == nil
                    failure = why
                    arrived?(loaded?.pixelSize, why)
                } catch {
                    guard !Task.isCancelled else { return }
                    let why = error.shown ?? String(localized: "Image didn't load. Tap to retry")
                    retries.loaded(file, reason: why)
                    picture = nil
                    failed = true
                    failure = why
                    arrived?(nil, why)
                }
                return
            }
            if image.isLocal {
                guard !failed else { return }
                // A local destination outside a conversation has no machine to read from; never send it to LarkMedia.
                let why = String(localized: "Can't show this image")
                picture = nil
                failed = true
                failure = why
                arrived?(nil, why)
                return
            }
            let loaded = await LarkMedia.shared.image(path, maxPixel: CGFloat(size))
            guard !Task.isCancelled else { return }
            picture = loaded
            failed = loaded == nil
            failure = nil
            arrived?(loaded?.pixelSize, nil)
        }
    }
}

/// A lone picture's frame from the width it is offered (`ReplyImageLayout.single`): its final height before it
/// arrives, the failed tile when it did not.
private struct ReplySingleLayout: Layout {
    var pixels: CGSize?
    var failed: Bool

    func sizeThatFits(proposal: ProposedViewSize, subviews: Subviews, cache: inout ()) -> CGSize {
        // The full reason lives below this layout, so even an accessibility-size sentence is not clipped by a tile.
        if failed { return CGSize(width: Theme.Size.replyPictureFailed, height: Theme.Size.replyPictureFailed) }
        return ReplyImageLayout.single(pixels: pixels, column: proposal.width ?? Theme.Size.replyPictures, height: Theme.Size.replyPicture)
    }

    func placeSubviews(in bounds: CGRect, proposal: ProposedViewSize, subviews: Subviews, cache: inout ()) {
        subviews.first?.place(at: bounds.origin, proposal: ProposedViewSize(bounds.size))
    }
}

/// Squares from the width they are offered (`ReplyImageLayout.grid`): their final size before any picture arrives.
private struct ReplyGridLayout: Layout {
    var count: Int

    private func grid(_ proposal: ProposedViewSize) -> ReplyImageLayout.Grid {
        ReplyImageLayout.grid(count: count, column: proposal.width ?? Theme.Size.replyPictures, widest: Theme.Size.replyPictures, gap: Theme.Space.replyPictureGap)
    }

    func sizeThatFits(proposal: ProposedViewSize, subviews: Subviews, cache: inout ()) -> CGSize {
        let g = grid(proposal)
        let gap = Theme.Space.replyPictureGap
        let columns = min(g.columns, max(1, g.shown))
        return CGSize(width: CGFloat(columns) * g.tile + CGFloat(columns - 1) * gap, height: CGFloat(g.rows) * g.tile + CGFloat(max(0, g.rows - 1)) * gap)
    }

    func placeSubviews(in bounds: CGRect, proposal: ProposedViewSize, subviews: Subviews, cache: inout ()) {
        let g = grid(ProposedViewSize(width: bounds.width, height: nil))
        let step = g.tile + Theme.Space.replyPictureGap
        for (i, view) in subviews.enumerated() {
            let at = CGPoint(x: bounds.minX + CGFloat(i % g.columns) * step, y: bounds.minY + CGFloat(i / g.columns) * step)
            view.place(at: at, proposal: ProposedViewSize(width: g.tile, height: g.tile))
        }
    }
}

/// A web or machine-local picture in Markdown that is not in a run (`ReplyImages`): a picture inside a quote, a note,
/// a page. Drawn as a lone reply picture. Pages with their own pictures set their own provider (a file's relative images,
/// Feishu documents); other schemes keep MarkdownUI's default.
struct WebMarkdownImages: ImageProvider {
    func makeImage(url: URL?) -> some View {
        if let url, ReplyImages.isWeb(url.absoluteString) || ReplyImages.isLocal(url.absoluteString) {
            ReplyPictureRun(block: ReplyImageBlock(all: [ReplyImage(url: url.absoluteString, alt: "")], shown: 0 ..< 1))
        } else {
            DefaultImageProvider.default.makeImage(url: url)
        }
    }
}

extension EnvironmentValues {
    /// The pictures here are in what other people wrote: each run waits for a tap (`asksBeforePictures()`).
    @Entry var asksBeforePictures = false
}

extension View {
    /// Pictures in what other people wrote (the Inbox's threads: Lark, GitHub, connectors) load only when asked: one
    /// may be a tracking pixel that tells its sender when it was read. A run is its final size with a Load button
    /// until then; pictures inside a line of text do not load.
    func asksBeforePictures() -> some View {
        environment(\.asksBeforePictures, true).markdownInlineImageProvider(MathInlineImages(web: false))
    }
}

/// A run of pictures not loaded yet (`asksBeforePictures`): its boxes in the well's color, the site and Load over them.
private struct AskedPictures: View {
    var block: ReplyImageBlock
    var load: () -> Void

    var body: some View {
        let shape = RoundedRectangle(cornerRadius: Theme.Radius.control, style: .continuous)
        Button(action: load) {
            Group {
                if block.shown.count == 1 {
                    ReplySingleLayout(pixels: nil, failed: false) { shape.fill(Theme.Fill.well) }
                } else {
                    ReplyGridLayout(count: block.shown.count) {
                        ForEach(Array(block.shown.prefix(ReplyImageLayout.mostShown)), id: \.self) { _ in shape.fill(Theme.Fill.well) }
                    }
                }
            }
            .overlay {
                VStack(spacing: Theme.Space.xxs) {
                    Group {
                        if block.shown.count == 1 {
                            Label("Load Image", systemImage: Theme.Icon.photo)
                        } else {
                            Label("Load Images", systemImage: Theme.Icon.photo)
                        }
                    }
                    .font(Theme.Font.secondary).lineLimit(1).minimumScaleFactor(0.8)
                    Text(verbatim: ReplyPictureSource.host(block.all[block.shown.lowerBound].url))
                        .font(Theme.Font.caption).foregroundStyle(.secondary).lineLimit(1)
                }
                .padding(Theme.Space.s)
            }
            .contentShape(.rect)
        }
        .buttonStyle(.plain)
        .accessibilityIdentifier("reply-pictures-asked")
    }
}

private extension UIImage {
    /// Its size in pixels.
    var pixelSize: CGSize { CGSize(width: size.width * scale, height: size.height * scale) }
}
