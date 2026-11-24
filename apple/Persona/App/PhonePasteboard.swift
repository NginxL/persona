import Foundation
import UniformTypeIdentifiers
import UIKit

/// The phone's own clipboard, for sending it to a computer and putting a computer's on it (#3064). Closures, so the
/// clipboard model's tests hand in a pasteboard of their own.
struct PhonePasteboard {
    /// What is on it now, or nil when it is empty. On iOS reading what another app copied asks "Allow Paste?" first
    /// (unless Settings › Persona › Paste from Other Apps is Allow); refused, it reads as nil.
    var read: @MainActor () -> ClipboardContent?
    /// Replaces it (no prompt).
    var write: @MainActor (ClipboardContent) -> Void
    /// Whether something is on it, without reading it (never prompts).
    var hasContent: @MainActor () -> Bool

    static let live = PhonePasteboard(
        read: {
            let board = UIPasteboard.general
            if board.hasImages, let image = board.image, let encoded = PhonePasteboard.encode(image) { return .image(encoded) }
            if let text = board.string, !text.isEmpty { return .text(text) }
            if let url = board.url { return .text(url.absoluteString) }
            return nil
        },
        write: { content in
            let board = UIPasteboard.general
            switch content.kind {
            case .text: board.string = content.text ?? ""
            case .image:
                if let bytes = content.image?.bytes {
                    if let type = PhonePasteboard.rawImageType(bytes) {
                        board.setData(bytes, forPasteboardType: type.identifier)
                    } else if let image = UIImage(data: bytes) {
                        board.image = image
                    }
                }
            case .empty, .concealed: break
            }
        },
        hasContent: {
            let board = UIPasteboard.general
            return board.hasStrings || board.hasImages || board.hasURLs
        }
    )

    /// PNG and JPEG go onto the pasteboard as their original bytes. Handing the pasteboard a UIImage instead leaves
    /// the pasteboard service encoding it after the setter returns, so the next pasteboard call on the main thread,
    /// our observer before or the system paste button now, waits for that work. The unchanged bytes leave it nothing
    /// to encode (#8198); every other format keeps UIKit's decode-and-image path.
    private nonisolated static func rawImageType(_ bytes: Data) -> UTType? {
        if bytes.starts(with: [0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a]) { return .png }
        if bytes.starts(with: [0xff, 0xd8, 0xff]) { return .jpeg }
        return nil
    }

    /// An image as the hub takes it: PNG, or JPEG at falling quality when the PNG is too big (`ClipboardLimits`). Still
    /// too big, the smallest JPEG comes back anyway: sending it is refused as too large, said in words.
    static func encode(_ image: UIImage) -> ClipboardImage? {
        if let png = image.pngData(), png.count <= ClipboardLimits.imageBytes { return ClipboardImage(mime: "image/png", bytes: png) }
        var smallest: Data?
        for quality in ClipboardLimits.jpegQualities {
            guard let jpeg = image.jpegData(compressionQuality: quality) else { continue }
            if jpeg.count <= ClipboardLimits.imageBytes { return ClipboardImage(mime: "image/jpeg", bytes: jpeg) }
            smallest = jpeg
        }
        return smallest.map { ClipboardImage(mime: "image/jpeg", bytes: $0) }
    }

    /// What the system Paste button (`PasteButton`) handed over: text first, then an image, then a link; nil when none
    /// of them could be read.
    static func content(of providers: [NSItemProvider]) async -> ClipboardContent? {
        for p in providers where p.canLoadObject(ofClass: String.self) {
            if let text = try? await p.loadString(), !text.isEmpty { return .text(text) }
        }
        for p in providers where p.hasItemConformingToTypeIdentifier(UTType.image.identifier) {
            if let data = try? await p.loadData(UTType.image),
               let encoded = UIImage(data: data).flatMap(encode) {
                return .image(encoded)
            }
        }
        for p in providers where p.hasItemConformingToTypeIdentifier(UTType.url.identifier) {
            if let data = try? await p.loadData(UTType.url), let url = URL(dataRepresentation: data, relativeTo: nil) {
                return .text(url.absoluteString)
            }
        }
        return nil
    }
}

private extension NSItemProvider {
    nonisolated func loadString() async throws -> String {
        try await withCheckedThrowingContinuation { (done: CheckedContinuation<String, Error>) in
            _ = loadObject(ofClass: String.self) { text, error in
                if let text { done.resume(returning: text) } else { done.resume(throwing: error ?? CocoaError(.fileReadUnknown)) }
            }
        }
    }

    nonisolated func loadData(_ type: UTType) async throws -> Data {
        try await withCheckedThrowingContinuation { (done: CheckedContinuation<Data, Error>) in
            _ = loadDataRepresentation(for: type) { data, error in
                if let data { done.resume(returning: data) } else { done.resume(throwing: error ?? CocoaError(.fileReadUnknown)) }
            }
        }
    }
}
