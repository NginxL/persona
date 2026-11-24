#if os(iOS)
import SwiftUI
import UIKit
import VisionKit

/// Scan Document in the composer's + (#3720): VisionKit's document camera, which finds the page, crops it and flattens
/// it. The pages go to the next message as its photos when they fit (the hub's pictures, `ImageLimits`), else as one PDF uploaded to
/// the computer as a picked file is (`Delivery`). Shown where the device can scan (`isAvailable`).
struct DocumentScanner: UIViewControllerRepresentable {
    var done: ([UIImage]) -> Void
    @Environment(\.dismiss) private var dismiss

    static var isAvailable: Bool {
        #if DEBUG
        // Simulator screenshots: `-showCameraRows YES` lists it as a phone does (the simulator can't scan).
        if UserDefaults.standard.bool(forKey: "showCameraRows") { return true }
        #endif
        return VNDocumentCameraViewController.isSupported
    }

    /// How a scan goes with the message: its pages as photos while they fit in the room the composer has left, else one PDF
    /// (a document's pages stay together and in order, rather than the first few going and the rest being dropped).
    nonisolated enum Delivery: Equatable {
        case photos
        case pdf
    }

    nonisolated static func delivery(pages: Int, room: Int) -> Delivery { pages <= max(room, 0) ? .photos : .pdf }

    /// The scan into the composer: `add` takes a photo, `upload` a file; a PDF that can't be written says so. The PDF is
    /// drawn and written off the main actor (a long scan is a JPEG pass a page and a file, P1 / P8 in
    /// docs/apple/performance.md), then uploaded.
    static func deliver(_ pages: [UIImage], room: Int, model: AppModel, add: (UIImage) -> Void, upload: @escaping ([URL]) -> Void) {
        guard !pages.isEmpty else { return }
        switch delivery(pages: pages.count, room: room) {
        case .photos:
            pages.forEach(add)
        case .pdf:
            let side = MessagePhotos.maxSide
            Task {
                let url = await Task.detached(priority: .userInitiated) { DocumentScanner.pdf(pages, maxSide: side) }.value
                if let url { upload([url]) } else { model.toast = Toast(String(localized: "Couldn't save the scan."), style: .error) }
            }
        }
    }

    /// One PDF, a page per scanned page at its own size (at most `maxSide`), in a directory of its own under tmp
    /// (`scan-<time>.pdf`). The pages go in as JPEG, as the photos a message carries do (`MessagePhotos`), so a long
    /// document stays small. Off the main actor (`deliver`).
    nonisolated static func pdf(_ pages: [UIImage], maxSide: CGFloat) -> URL? {
        let dir = FileManager.default.temporaryDirectory.appending(path: UUID().uuidString)
        guard (try? FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)) != nil else { return nil }
        let stamp = DateFormatter()
        stamp.locale = Locale(identifier: "en_US_POSIX")
        stamp.dateFormat = "yyyyMMdd-HHmmss"
        let url = dir.appending(path: "scan-\(stamp.string(from: .now)).pdf")
        let images = pages.map { page in
            let scaled = page.scaledDown(maxSide: maxSide)
            return scaled.jpegData(compressionQuality: 0.75).flatMap(UIImage.init(data:)) ?? scaled
        }
        let first = CGRect(origin: .zero, size: images[0].size)
        do {
            try UIGraphicsPDFRenderer(bounds: first).writePDF(to: url) { context in
                for image in images {
                    let page = CGRect(origin: .zero, size: image.size)
                    context.beginPage(withBounds: page, pageInfo: [:])
                    image.draw(in: page)
                }
            }
            return url
        } catch {
            return nil
        }
    }

    func makeCoordinator() -> Coordinator { Coordinator(self) }

    func makeUIViewController(context: Context) -> VNDocumentCameraViewController {
        let vc = VNDocumentCameraViewController()
        vc.delegate = context.coordinator
        return vc
    }

    func updateUIViewController(_ vc: VNDocumentCameraViewController, context: Context) {}

    final class Coordinator: NSObject, VNDocumentCameraViewControllerDelegate {
        let parent: DocumentScanner
        init(_ parent: DocumentScanner) { self.parent = parent }

        func documentCameraViewController(_ controller: VNDocumentCameraViewController, didFinishWith scan: VNDocumentCameraScan) {
            let pages = (0..<scan.pageCount).map { scan.imageOfPage(at: $0) }
            parent.dismiss()
            parent.done(pages)
        }

        func documentCameraViewControllerDidCancel(_ controller: VNDocumentCameraViewController) { parent.dismiss() }

        func documentCameraViewController(_ controller: VNDocumentCameraViewController, didFailWithError error: any Error) { parent.dismiss() }
    }
}
#endif
