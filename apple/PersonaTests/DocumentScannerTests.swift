import Foundation
import Testing
import UIKit
@testable import Persona

/// Scan Document in the composer's + (#3720): a scan's pages go as the next message's photos while they fit in the room
/// the composer has left, else as one PDF with a page per scanned page.
@MainActor struct DocumentScannerTests {
    @Test func pagesThatFitGoAsPhotos() {
        #expect(DocumentScanner.delivery(pages: 1, room: ImageLimits.standard.perMessage) == .photos)
        #expect(DocumentScanner.delivery(pages: 4, room: 4) == .photos)
        #expect(DocumentScanner.delivery(pages: 2, room: 3) == .photos)
    }

    @Test func morePagesThanTheRoomGoAsOnePDF() {
        #expect(DocumentScanner.delivery(pages: 5, room: 4) == .pdf)
        #expect(DocumentScanner.delivery(pages: 2, room: 1) == .pdf)
        #expect(DocumentScanner.delivery(pages: 1, room: 0) == .pdf)
        #expect(DocumentScanner.delivery(pages: 1, room: -1) == .pdf)
    }

    @Test func thePDFHasAPagePerScannedPage() throws {
        let page = UIGraphicsImageRenderer(size: CGSize(width: 300, height: 400)).image { ctx in
            UIColor.white.setFill()
            ctx.fill(CGRect(x: 0, y: 0, width: 300, height: 400))
        }
        let url = try #require(DocumentScanner.pdf([page, page, page], maxSide: MessagePhotos.maxSide))
        defer { try? FileManager.default.removeItem(at: url.deletingLastPathComponent()) }
        #expect(url.lastPathComponent.hasPrefix("scan-") && url.pathExtension == "pdf")
        let doc = try #require(CGPDFDocument(url as CFURL))
        #expect(doc.numberOfPages == 3)
    }
}
