#if os(iOS)
import ImageIO
import MarkdownUI
import PersonaKit
import SwiftUI
import UIKit
import UniformTypeIdentifiers

// Export Image (#9468): the picked messages drawn as the chat shows them into a picture as wide as the phone, at the
// screen's scale, light or dark as the phone is: the chat's title and date on top (Title and Date), your messages as
// bubbles, replies with their Markdown, code, tables, cards, formulas and pictures, and Persona's mark at the foot.
// Each block (the heading, a bubble, a reply's parts) is drawn offscreen on its own, so no one bitmap is huge; the
// blocks are stacked into pages of at most `maxPixels` (and `maxBytes` to decode), broken between blocks, a block taller
// than a page cut at a blank row of its pixels. Pages are PNG files: the preview shows small copies, Share hands the
// files to the system's share sheet (Save Image, Messages…), so the app never asks for Photos.

/// What Export Image draws: the chat's title and who answers in it, the picked messages without and with the questions
/// they bring, how to read your messages' pictures, and the phone's width.
struct ExportRequest: Identifiable {
    let id = UUID()
    var title: String
    var agent: String
    var pieces: (_ questions: Bool) -> [ForwardDraft.Piece]
    var loadImage: (String) async -> UIImage?
    var width: CGFloat
    var prefersLetterMark = false
}

/// The choices the preview offers, and the appearance the picture is drawn in.
struct ExportOptions: Hashable, Sendable {
    var questions: Bool
    var heading: Bool
    var dark: Bool
}

/// One page of the picture: its PNG file and its size in pixels.
struct ExportPage: Identifiable, Sendable {
    var id: Int
    var file: URL
    var pixels: CGSize
}

@MainActor
enum ChatExportRenderer {
    /// The picture's width: the phone's, in points (an iPad draws a phone's).
    static func width(_ screen: CGFloat) -> CGFloat { min(max(screen, 360), 430) }
    /// Sharp on every phone: 2× or 3×.
    static func scale(_ screen: CGFloat) -> CGFloat { min(max(screen.rounded(), 2), 3) }
    /// A page is at most this tall, in pixels…
    nonisolated static let maxPixels: CGFloat = 16_000
    /// …and at most this much to decode (4 bytes a pixel).
    nonisolated static let maxBytes: CGFloat = 64 * 1024 * 1024
    /// Where the files go; emptied before each export.
    static var folder: URL { URL.temporaryDirectory.appending(path: "chat-export", directoryHint: .isDirectory) }

    /// A page's height at most, in points, for a picture `width` points wide at `scale`.
    nonisolated static func pageHeight(width: CGFloat, scale: CGFloat) -> CGFloat {
        min(maxPixels, maxBytes / (4 * width * scale)) / scale
    }

    /// The messages as they go: the whole of each (read when the chat holds it cut), as a forward carries it.
    static func messages(_ pieces: [ForwardDraft.Piece], prefersLetterMark: Bool = false) async throws -> [ExportMessage] {
        var out: [ExportMessage] = []
        for p in pieces {
            let raw = try await p.full?() ?? p.raw
            out.append(ExportMessage(id: p.source.item, role: p.source.role,
                                     author: p.source.role == .user ? String(localized: "Me") : p.source.author,
                                     text: ForwardMessage.body(raw, limit: ExportMessage.limit), time: p.source.time, images: p.images,
                                     prefersLetterMark: p.source.role == .agent && prefersLetterMark))
        }
        return out
    }

    /// One thing to draw and the room above it.
    private struct Block {
        var view: AnyView
        var gap: CGFloat
    }

    /// A drawn block, kept as a PNG until its page is put together; the rows where it may be cut, in points.
    nonisolated private struct Drawn: Sendable {
        var png: Data
        var size: CGSize
        var blanks: [CGFloat]
    }

    /// Draws the picture's pages for `request` with `options` at `scale`; checks for cancellation between blocks.
    static func render(_ request: ExportRequest, options: ExportOptions, scale: CGFloat) async throws -> [ExportPage] {
        let messages = try await messages(request.pieces(options.questions), prefersLetterMark: request.prefersLetterMark)
        try Task.checkCancellation()
        let width = width(request.width)
        let column = width - 2 * Layout.margin
        let assets = await ExportAssets.load(messages, dark: options.dark, column: column, scale: scale, loadImage: request.loadImage)
        try Task.checkCancellation()

        var blocks: [Block] = []
        if options.heading {
            blocks.append(Block(view: AnyView(ExportHeading(title: request.title, subtitle: subtitle(messages, agent: request.agent))), gap: 0))
        }
        for m in messages {
            let gap = blocks.isEmpty ? 0 : Layout.message
            if m.isUser {
                blocks.append(Block(view: AnyView(ExportBubble(message: m, photos: assets.photos[m.id] ?? [], column: column)), gap: gap))
                continue
            }
            let parts = ExportParts.of(m.text)
            for (i, part) in parts.enumerated() {
                let view = ExportPartView(part: part, assets: assets, column: column)
                // The author goes with the first part, never alone at the foot of a page.
                // A long code block's pieces follow each other closely: one block, cut where a page could be.
                let follows = i > 0 && ExportParts.isCodePiece(parts[i - 1]) && ExportParts.isCodePiece(part)
                blocks.append(i == 0
                    ? Block(view: AnyView(VStack(alignment: .leading, spacing: Theme.Space.xs) { ExportAuthor(message: m); view }), gap: gap)
                    : Block(view: AnyView(view), gap: follows ? Theme.Space.xxs : Layout.part))
            }
            if parts.isEmpty { blocks.append(Block(view: AnyView(ExportAuthor(message: m)), gap: gap)) }
        }

        // Each block drawn, then kept as a PNG with its blank rows, off the main actor.
        let room = pageHeight(width: width, scale: scale) - Layout.top - Layout.bottom - Layout.footerGap
            - (draw(ExportFooter(page: "9/9"), width: width, scale: scale, dark: options.dark).map { CGFloat($0.height) / scale } ?? 0)
        var drawn: [Drawn] = []
        for b in blocks {
            guard let cg = draw(b.view, width: column, scale: scale, dark: options.dark) else { continue }
            let tall = CGFloat(cg.height) / scale > room
            drawn.append(await Task.detached(priority: .userInitiated) { Self.keep(cg, scale: scale, blanks: tall) }.value)
            try Task.checkCancellation()
            await Task.yield()
        }
        let gaps = blocks.map(\.gap)
        let kept = drawn
        let pages = ExportPages.split(kept.map(\.size.height), gaps: gaps, room: room) { i, from, limit in
            kept[i].blanks.last { $0 > from + ExportPages.least && $0 <= limit } ?? limit
        }

        // The pages, each with its own footer ("2/3"), put together and written off the main actor.
        let dir = folder
        await Task.detached(priority: .utility) {
            try? FileManager.default.removeItem(at: dir)
            try? FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
        }.value
        let background = UIColor.systemBackground.resolvedColor(with: UITraitCollection(userInterfaceStyle: options.dark ? .dark : .light)).cgColor
        var out: [ExportPage] = []
        for (n, slices) in pages.enumerated() {
            let label = pages.count > 1 ? "\(n + 1)/\(pages.count)" : nil
            guard let footer = draw(ExportFooter(page: label), width: width, scale: scale, dark: options.dark) else { continue }
            let file = dir.appending(path: "Persona-\(n + 1).png")
            let page = try await Task.detached(priority: .userInitiated) {
                try Self.compose(slices, drawn: kept, gaps: gaps, footer: footer, width: width, scale: scale,
                                 background: background, to: file)
            }.value
            out.append(ExportPage(id: n + 1, file: file, pixels: page))
            try Task.checkCancellation()
        }
        return out
    }

    /// The heading's second line: the date (or the days) the messages are from, and who answers.
    static func subtitle(_ messages: [ExportMessage], agent: String) -> String {
        let times = messages.compactMap(\.time)
        let first = times.min() ?? .now, last = times.max() ?? first
        let day = Date.FormatStyle.dateTime.year().month().day()
        let date = Calendar.current.isDate(first, inSameDayAs: last) ? first.formatted(day) : first.formatted(day) + " – " + last.formatted(day)
        return [date, agent].filter { !$0.isEmpty }.joined(separator: " · ")
    }

    /// `view` drawn `width` points wide at `scale`, transparent around it, in the picture's appearance and text size.
    static func draw(_ view: some View, width: CGFloat, scale: CGFloat, dark: Bool) -> CGImage? {
        let r = ImageRenderer(content: view
            .frame(width: width, alignment: .leading)
            .environment(\.colorScheme, dark ? .dark : .light)
            .environment(\.dynamicTypeSize, .large) // reading size: the picture is for others to read too
            .environment(AppModel.shared))
        r.proposedSize = ProposedViewSize(width: width, height: nil)
        r.scale = scale
        r.isOpaque = false
        return r.cgImage
    }

    /// A drawn block as a PNG, with the rows it may be cut at when it is taller than a page.
    nonisolated private static func keep(_ cg: CGImage, scale: CGFloat, blanks: Bool) -> Drawn {
        let png = UIImage(cgImage: cg).pngData() ?? Data()
        let rows = blanks ? ExportSlicer.blankRows(cg).map { CGFloat($0) / scale } : []
        return Drawn(png: png, size: CGSize(width: CGFloat(cg.width) / scale, height: CGFloat(cg.height) / scale), blanks: rows)
    }

    /// One page: its background, its slices of blocks with their gaps, and its footer, written to `file` as a PNG.
    nonisolated private static func compose(_ slices: [ExportPages.Slice], drawn: [Drawn], gaps: [CGFloat], footer: CGImage,
                                            width: CGFloat, scale: CGFloat, background: CGColor, to file: URL) throws -> CGSize {
        let footerHeight = CGFloat(footer.height) / scale
        var height = Layout.top + Layout.bottom + Layout.footerGap + footerHeight
        for (i, s) in slices.enumerated() { height += s.height + (i == 0 ? 0 : gaps[s.block]) }
        height = height.rounded(.up)
        let format = UIGraphicsImageRendererFormat()
        format.scale = scale
        format.opaque = true
        let image = UIGraphicsImageRenderer(size: CGSize(width: width, height: height), format: format).image { ctx in
            ctx.cgContext.setFillColor(background)
            ctx.cgContext.fill(CGRect(x: 0, y: 0, width: width, height: height))
            var y = Layout.top
            var decoded: (Int, CGImage)?
            for (i, s) in slices.enumerated() {
                if i > 0 { y += gaps[s.block] }
                if decoded?.0 != s.block, let cg = ExportSlicer.decode(drawn[s.block].png) { decoded = (s.block, cg) }
                if let (_, cg) = decoded {
                    let rect = CGRect(x: 0, y: (s.from * scale).rounded(), width: CGFloat(cg.width), height: (s.height * scale).rounded())
                    if let part = cg.cropping(to: rect) {
                        UIImage(cgImage: part, scale: scale, orientation: .up)
                            .draw(in: CGRect(x: Layout.margin, y: y, width: CGFloat(part.width) / scale, height: CGFloat(part.height) / scale))
                    }
                }
                y += s.height
            }
            UIImage(cgImage: footer, scale: scale, orientation: .up)
                .draw(in: CGRect(x: 0, y: height - Layout.bottom - footerHeight, width: width, height: footerHeight))
        }
        guard let data = image.pngData() else { throw CocoaError(.fileWriteUnknown) }
        try data.write(to: file, options: .atomic)
        return CGSize(width: width * scale, height: height * scale)
    }

    /// The picture's margins and gaps, in points.
    nonisolated enum Layout {
        static let margin = Theme.Space.l
        static let top = Theme.Space.xl
        static let bottom = Theme.Space.l
        static let footerGap = Theme.Space.xl
        /// Between messages, and between the parts of one reply.
        static let message = Theme.Space.l
        static let part = Theme.Space.m
    }
}

/// What the picture draws that loads later in the chat, loaded first: formulas and diagrams (`MarkdownRenderer`), the
/// pictures in replies, your messages' photos.
struct ExportAssets {
    /// By address.
    var images: [String: UIImage] = [:]
    var photos: [String: [UIImage]] = [:]
    var column: CGFloat = 0
    /// Your message's photos an exported page draws: a layout choice, not the pictures a message may carry (#12973).
    static let photosDrawn = 4

    static func load(_ messages: [ExportMessage], dark: Bool, column: CGFloat, scale: CGFloat,
                     loadImage: (String) async -> UIImage?) async -> ExportAssets {
        var out = ExportAssets(column: column)
        for m in messages {
            if m.isUser {
                var photos: [UIImage] = []
                for path in m.images.prefix(Self.photosDrawn) {
                    if let img = await loadImage(path) { photos.append(img) }
                }
                out.photos[m.id] = photos
                continue
            }
            for part in ExportParts.of(m.text) {
                switch part {
                case .images(let pictures):
                    // As the chat loads them (through the hub when it fetches pictures, #9473), at the picture's size.
                    let size = ReplyImageLayout.size(points: max(column, Theme.Size.replyPicture), scale: scale)
                    for p in pictures where out.images[p.url] == nil {
                        let img: UIImage?
                        if p.isLocal {
                            // The same session-scoped file loader used for message photos; relative paths use the session cwd.
                            img = await loadImage(p.localPath ?? p.url)
                        } else {
                            img = await LarkMedia.shared.image(ReplyPictureSource.path(p.url, size: size), maxPixel: CGFloat(size))
                        }
                        if let img { out.images[p.url] = img }
                    }
                case .mathText(let md):
                    for tex in ExportMath.formulas(md) {
                        _ = try? await MarkdownRenderer.shared.image(.inlineMath, tex, dark: false, points: ExportRenderedFence.textPoints)
                    }
                case .markdown(let md):
                    for (kind, source) in ExportMath.fences(md) {
                        _ = try? await MarkdownRenderer.shared.image(kind, source, dark: ExportRenderedFence.dark(kind, dark ? .dark : .light),
                                                                     points: ExportRenderedFence.points(kind))
                    }
                case .card:
                    break
                }
            }
        }
        return out
    }
}

/// Formulas in a part's Markdown (after `MathMarkdown.rewrite`): its inline ones' TeX, its ```math and ```mermaid fences.
nonisolated enum ExportMath {
    static func formulas(_ md: String) -> [String] {
        var out: [String] = []
        var rest = Substring(md)
        let marker = "](\(MathMarkdown.scheme):"
        while let hit = rest.range(of: marker), let close = rest[hit.upperBound...].firstIndex(of: ")") {
            if let tex = String(rest[hit.upperBound..<close]).removingPercentEncoding { out.append(tex) }
            rest = rest[rest.index(after: close)...]
        }
        return out
    }

    static func fences(_ md: String) -> [(MarkdownRenderer.Kind, String)] {
        var out: [(MarkdownRenderer.Kind, String)] = []
        var open: (MarkdownFence, MarkdownRenderer.Kind?)?
        var lines: [String] = []
        for line in md.components(separatedBy: "\n") {
            let t = line.trimmingCharacters(in: .whitespaces)
            if let (fence, kind) = open {
                if fence.isClosed(by: t) {
                    if let kind { out.append((kind, lines.joined(separator: "\n"))) }
                    open = nil
                    lines = []
                } else {
                    lines.append(line)
                }
            } else if let fence = MarkdownFence.opening(t) {
                let info = t.drop { $0 == "`" || $0 == "~" }.trimmingCharacters(in: .whitespaces)
                open = (fence, RenderedFence.kind(info.split(separator: " ").first.map(String.init)))
            }
        }
        return out
    }
}

/// Where a tall block may be cut: rows of its pixels that are all one color (the gaps between lines, a code block's
/// background between its lines), preferring wholly clear ones.
nonisolated enum ExportSlicer {
    static func blankRows(_ cg: CGImage) -> [Int] {
        let w = cg.width, h = cg.height
        guard w > 0, h > 0, let ctx = CGContext(data: nil, width: w, height: h, bitsPerComponent: 8, bytesPerRow: w * 4,
                                                 space: CGColorSpaceCreateDeviceRGB(),
                                                 bitmapInfo: CGImageAlphaInfo.premultipliedLast.rawValue),
              let base = ctx.data else { return [] }
        ctx.draw(cg, in: CGRect(x: 0, y: 0, width: w, height: h))
        let px = base.bindMemory(to: UInt32.self, capacity: w * h)
        var rows: [Int] = []
        // The context's first row in memory is the image's top row.
        for y in 0..<h {
            let row = px + y * w
            let first = row[0]
            var same = true
            for x in 1..<w where row[x] != first {
                same = false
                break
            }
            if same { rows.append(y) }
        }
        return rows
    }

    static func decode(_ png: Data) -> CGImage? {
        guard let src = CGImageSourceCreateWithData(png as CFData, nil) else { return nil }
        return CGImageSourceCreateImageAtIndex(src, 0, [kCGImageSourceShouldCacheImmediately: true] as CFDictionary)
    }

    /// A page's small copy for the preview, `width` pixels wide.
    static func thumbnail(_ file: URL, pixels: CGSize, width: CGFloat) -> UIImage? {
        guard pixels.width > 0, let src = CGImageSourceCreateWithURL(file as CFURL, nil) else { return nil }
        let longest = max(width, width * pixels.height / pixels.width)
        let opts: [CFString: Any] = [kCGImageSourceCreateThumbnailFromImageAlways: true, kCGImageSourceThumbnailMaxPixelSize: longest,
                                     kCGImageSourceCreateThumbnailWithTransform: true]
        return CGImageSourceCreateThumbnailAtIndex(src, 0, opts as CFDictionary).map { UIImage(cgImage: $0) }
    }
}

// MARK: What the picture draws

/// The picture's heading: the chat's title, then the day and who answers; a hairline under them.
struct ExportHeading: View {
    var title: String
    var subtitle: String

    var body: some View {
        VStack(alignment: .leading, spacing: Theme.Space.xs) {
            if !title.isEmpty {
                Text(verbatim: title).font(Theme.Font.heading).fixedSize(horizontal: false, vertical: true)
            }
            Text(verbatim: subtitle).font(Theme.Font.caption).foregroundStyle(.secondary)
            Divider().padding(.top, Theme.Space.s)
        }
        .frame(maxWidth: .infinity, alignment: .leading)
    }
}

/// Who wrote a reply, over it: the Agent's mark (Persona's face for Persona) and its name.
struct ExportAuthor: View {
    var message: ExportMessage

    var body: some View {
        HStack(spacing: Theme.Space.xs) {
            mark
            Text(verbatim: message.author).font(Theme.Font.labelStrong).foregroundStyle(.secondary)
        }
    }

    @ViewBuilder private var mark: some View {
        if message.role == .persona {
            PersonaAvatar(size: Theme.Size.markM)
        } else if !message.prefersLetterMark, let brand = AgentKind.brand(named: message.author) {
            Image(brand).resizable().scaledToFit().foregroundStyle(.primary)
                .padding(Theme.Space.xxs)
                .frame(width: Theme.Size.markM, height: Theme.Size.markM)
                .background(Color(.secondarySystemBackground), in: .circle)
        } else {
            Text(verbatim: AgentKind(brand: nil, name: message.author, surface: nil).letter)
                .font(Theme.Font.label).foregroundStyle(.secondary)
                .frame(width: Theme.Size.markM, height: Theme.Size.markM)
                .background(Color(.secondarySystemBackground), in: .circle)
        }
    }
}

/// Your message: its photos, three to a row, and its words in your bubble, on the right.
struct ExportBubble: View {
    var message: ExportMessage
    var photos: [UIImage]
    var column: CGFloat

    var body: some View {
        let widest = column * 0.8
        VStack(alignment: .trailing, spacing: Theme.Space.xs) {
            if !photos.isEmpty { ExportGrid(images: photos.map(Optional.some), width: widest, trailing: true) }
            if !message.text.isEmpty {
                Text(verbatim: message.text)
                    .font(Theme.Font.secondary)
                    .foregroundStyle(Theme.userBubbleText)
                    .fixedSize(horizontal: false, vertical: true)
                    .padding(.horizontal, Theme.Space.m)
                    .padding(.vertical, Theme.Space.s)
                    .background(Theme.userBubble, in: .rect(cornerRadius: Theme.Radius.bubble, style: .continuous))
                    .frame(maxWidth: widest, alignment: .trailing)
            }
        }
        .frame(maxWidth: .infinity, alignment: .trailing)
    }
}

/// One part of a reply as the picture draws it.
struct ExportPartView: View {
    var part: ExportPart
    var assets: ExportAssets
    var column: CGFloat

    var body: some View {
        switch part {
        case .markdown(let md):
            Markdown(Links.markdown(md))
                .markdownTheme(.export)
                .fixedSize(horizontal: false, vertical: true)
                .frame(maxWidth: .infinity, alignment: .leading)
        case .mathText(let md):
            ExportMathText(markdown: md)
        case .images(let pictures):
            ExportGrid(images: pictures.map { assets.images[$0.url] }, width: column, trailing: false)
        case .card(let card):
            ShareCardPartView(part: card)
        }
    }
}

/// A paragraph with formulas in it: its words as text and each formula as the picture drawn first, in the line.
struct ExportMathText: View {
    var markdown: String
    /// How much of a formula's picture is under its baseline (KaTeX's strut, about a quarter of an em of its height).
    static let depth: CGFloat = 0.2

    var body: some View {
        segments.reduce(Text(verbatim: "")) { $0 + $1 }
            .font(Theme.Font.secondary)
            .fixedSize(horizontal: false, vertical: true)
            .frame(maxWidth: .infinity, alignment: .leading)
    }

    private var segments: [Text] {
        var out: [Text] = []
        var rest = Substring(markdown)
        let marker = "](\(MathMarkdown.scheme):"
        while let hit = rest.range(of: marker), let open = rest[..<hit.lowerBound].range(of: "![", options: .backwards),
              let close = rest[hit.upperBound...].firstIndex(of: ")") {
            out.append(words(rest[..<open.lowerBound]))
            let tex = String(rest[hit.upperBound..<close]).removingPercentEncoding ?? ""
            if let img = MarkdownRenderer.shared.cached(.inlineMath, tex, dark: false, points: ExportRenderedFence.textPoints) {
                // KaTeX's box reaches below the baseline: set it down by that much, or it rides above the words.
                out.append(Text(Image(uiImage: img).renderingMode(.template)).baselineOffset(-img.size.height * Self.depth))
            } else {
                out.append(Text(verbatim: tex).font(Theme.Font.code))
            }
            rest = rest[rest.index(after: close)...]
        }
        out.append(words(rest))
        return out
    }

    private func words(_ s: Substring) -> Text {
        let attributed = (try? AttributedString(markdown: String(s), options: .init(interpretedSyntax: .inlineOnlyPreservingWhitespace)))
            ?? AttributedString(String(s))
        return Text(attributed)
    }
}

/// Pictures in a reply or your photos, laid out as the chat lays out a reply's (`ReplyImageLayout`, #9474): one alone
/// `Theme.Size.replyPicture` high and as wide as its shape allows, never larger than itself; several as squares, two
/// side by side, three to a row past that (four 2 × 2), and all of them: a picture cannot open "+3". One that did not
/// load is a tile with the broken-picture mark.
struct ExportGrid: View {
    var images: [UIImage?]
    var width: CGFloat
    var trailing: Bool

    var body: some View {
        let align: HorizontalAlignment = trailing ? .trailing : .leading
        if images.count == 1, let one = images[0] {
            let pixels = CGSize(width: one.size.width * one.scale, height: one.size.height * one.scale)
            let size = ReplyImageLayout.single(pixels: pixels, column: width, height: Theme.Size.replyPicture)
            tile(one, size: size)
                .frame(maxWidth: .infinity, alignment: trailing ? .trailing : .leading)
        } else {
            let grid = ReplyImageLayout.grid(count: images.count, column: width, widest: Theme.Size.replyPictures,
                                             gap: Theme.Space.replyPictureGap)
            let columns = grid.columns, side = grid.tile
            VStack(alignment: align, spacing: Theme.Space.replyPictureGap) {
                ForEach(Array(stride(from: 0, to: images.count, by: columns)), id: \.self) { start in
                    HStack(spacing: Theme.Space.replyPictureGap) {
                        ForEach(start..<min(start + columns, images.count), id: \.self) { i in
                            tile(images[i], size: CGSize(width: side, height: side))
                        }
                    }
                }
            }
            .frame(maxWidth: .infinity, alignment: trailing ? .trailing : .leading)
        }
    }

    @ViewBuilder private func tile(_ image: UIImage?, size: CGSize) -> some View {
        Group {
            if let image {
                Image(uiImage: image).resizable().scaledToFill()
            } else {
                ThemedIcon(Theme.Icon.brokenImage).font(Theme.Font.heading).foregroundStyle(.secondary)
                    .frame(maxWidth: .infinity, maxHeight: .infinity)
                    .background(Theme.codeBackground)
            }
        }
        .frame(width: size.width, height: size.height)
        .clipShape(.rect(cornerRadius: Theme.Radius.control, style: .continuous))
    }
}

/// The foot of each page: Persona's mark, and which page it is when there are several.
struct ExportFooter: View {
    var page: String?

    var body: some View {
        HStack(spacing: Theme.Space.xs) {
            PersonaBrandLockup(size: .mark)
            if let page {
                Text(verbatim: "· " + page).font(Theme.Font.label).foregroundStyle(.tertiary).monospacedDigit()
            }
        }
        .frame(maxWidth: .infinity)
    }
}
#endif
