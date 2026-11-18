import PersonaKit
import Testing
import UIKit
@testable import Persona

@Suite(.serialized)
@MainActor
struct ImagePagingTests {
    private func picture(_ color: UIColor) -> UIImage {
        UIGraphicsImageRenderer(size: CGSize(width: 80, height: 60)).image { context in
            color.setFill()
            context.fill(CGRect(x: 0, y: 0, width: 80, height: 60))
        }
    }

    @Test func decodeUpdateKeepsExistingPageAndFractionalOffset() throws {
        let pager = PagerScrollView()
        pager.frame = CGRect(x: 0, y: 0, width: 400, height: 800)
        let first = picture(.red)
        pager.update(count: 5, images: [1: first], page: 1)
        pager.layoutIfNeeded()
        let page = try #require(pager.subviews.compactMap { $0 as? PagerPage }.first { $0.frame.minX == 400 })
        let zoom = try #require(page.subviews.compactMap { $0 as? ZoomScrollView }.first)
        pager.contentOffset.x = 530
        let second = picture(.blue)
        pager.update(count: 5, images: [1: second], page: 1)
        #expect(pager.contentOffset.x == 530)
        #expect(page.superview === pager)
        #expect(zoom.superview === page)
        #expect(zoom.imageView.image === second)
        #expect(page.frame.minX == 400)
    }

    @Test func widthChangeKeepsTheCommittedPageExactlyAligned() {
        let pager = PagerScrollView()
        pager.frame = CGRect(x: 0, y: 0, width: 400, height: 800)
        pager.update(count: 5, images: [:], page: 2)
        pager.layoutIfNeeded()
        #expect(pager.contentOffset.x == 800)
        pager.contentOffset.x = 920
        pager.frame = CGRect(x: 0, y: 0, width: 800, height: 600)
        pager.layoutIfNeeded()
        #expect(pager.contentOffset.x == 1600)
        #expect(PagerMath.isAligned(offset: pager.contentOffset.x, width: 800, count: 5))
    }

    @Test func debugLaunchFlagsFollowNamingAndReleaseChecks() throws {
        let root = URL(fileURLWithPath: #filePath).deletingLastPathComponent().deletingLastPathComponent().deletingLastPathComponent()
        // release.sh refuses a Release binary whose `strings` has a line equal to a name in this file (one per line).
        let switches = try String(contentsOf: root.appending(path: "scripts/release/debug_switches.txt"), encoding: .utf8)
        let listed = Set(switches.components(separatedBy: .newlines).map { $0.trimmingCharacters(in: .whitespaces) })
        let viewer = try String(contentsOf: root.appending(path: "apple/Persona/Views/Chat/ImageViewer.swift"), encoding: .utf8)
        let probe = try String(contentsOf: root.appending(path: "apple/Persona/Debug/RuntimeCLI+ImageViewer.swift"), encoding: .utf8)
        for flag in ["showViewerDecodeDelay", "showViewerDecodeDuringPan", "showViewerPagingProbe"] {
            #expect(flag.hasPrefix("show"))
            #expect(listed.contains(flag))
        }
        #expect(!viewer.contains("\"viewerDecodeDelay\""))
        #expect(probe.hasPrefix("#if DEBUG && (os(iOS) || os(visionOS))"))
    }

    @Test func imageUpdateDoesNotResetAnExistingZoomedPage() throws {
        let pager = PagerScrollView()
        pager.frame = CGRect(x: 0, y: 0, width: 400, height: 800)
        let image = picture(.green)
        pager.update(count: 5, images: [1: image], page: 1)
        pager.layoutIfNeeded()
        let page = try #require(pager.subviews.compactMap { $0 as? PagerPage }.first { $0.frame.minX == 400 })
        let zoom = try #require(page.subviews.compactMap { $0 as? ZoomScrollView }.first)
        zoom.layoutIfNeeded()
        zoom.setZoomScale(2, animated: false)
        pager.update(count: 5, images: [1: image, 2: picture(.blue)], page: 1)
        #expect(zoom.zoomScale == 2)
        #expect(pager.contentOffset.x == 400)
    }
}
