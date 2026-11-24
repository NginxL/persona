import SwiftUI
import Testing
@testable import Persona

/// The shell's strip stays `moving` until its slide has really ended (`ShellPager.settle`): at rest each page's own top
/// bar comes back in place of the one drawn over both pages, so the page must be where it rests by then. Ended
/// "logically", the spring still had a few points to go and the bar jumped that far sideways after the switch.
@MainActor
struct ShellPagerSettleTests {
    /// The two pages on the strip, as `ShellView` lays them out: Assistant red, Agents blue.
    private struct Strip: View {
        let shell: ShellState

        var body: some View {
            ZStack {
                Color.red.modifier(ShellPageSlide(shell: shell, index: 0))
                Color.blue.modifier(ShellPageSlide(shell: shell, index: 1))
            }
            .ignoresSafeArea()
        }
    }

    /// How far page `index` is drawn from its place (pt), read off the window's pixels along its middle row.
    private func drawnOffset(of index: Int, in window: UIWindow) -> CGFloat? {
        let size = window.bounds.size
        let format = UIGraphicsImageRendererFormat()
        format.scale = window.screen.scale
        let image = UIGraphicsImageRenderer(size: size, format: format).image { _ in
            window.drawHierarchy(in: window.bounds, afterScreenUpdates: false)
        }
        guard let cg = image.cgImage else { return nil }
        let w = cg.width, row = cg.height / 2
        var px = [UInt8](repeating: 0, count: w * 4)
        guard let ctx = CGContext(data: &px, width: w, height: 1, bitsPerComponent: 8, bytesPerRow: w * 4,
                                  space: CGColorSpaceCreateDeviceRGB(), bitmapInfo: CGImageAlphaInfo.premultipliedLast.rawValue)
        else { return nil }
        ctx.draw(cg, in: CGRect(x: 0, y: -CGFloat(cg.height - row - 1), width: CGFloat(w), height: CGFloat(cg.height)))
        let mine = (0..<w).filter { x in
            let r = Int(px[x * 4]), b = Int(px[x * 4 + 2])
            return index == 0 ? r > 200 && b < 80 : b > 200 && r < 80
        }
        guard let first = mine.first, let last = mine.last else { return nil }
        return (first > 0 ? CGFloat(first) : CGFloat(last + 1 - w)) / format.scale
    }

    @Test(arguments: [1, 0])
    func thePageIsInPlaceWhenTheStripStopsMoving(to page: Int) async {
        let shell = ShellState()
        let pager = shell.pager
        pager.width = 402
        if page == 0 { pager.slide(to: 1, animated: false) }
        // In the host app's scene: a window without one is never drawn.
        let scene = UIApplication.shared.connectedScenes.compactMap { $0 as? UIWindowScene }.first
        let window = scene.map { UIWindow(windowScene: $0) } ?? UIWindow()
        window.frame = CGRect(x: 0, y: 0, width: 402, height: 874)
        window.windowLevel = .alert + 1
        window.rootViewController = UIHostingController(rootView: Strip(shell: shell))
        window.makeKeyAndVisible()
        defer { window.isHidden = true }
        try? await Task.sleep(for: .milliseconds(300))

        let start = Date()
        pager.slide(to: page)
        var sawMoving = false
        var at: CGFloat?
        var seen: [String] = []
        while at == nil, Date().timeIntervalSince(start) < 2 {
            try? await Task.sleep(for: .milliseconds(2))
            let x = drawnOffset(of: page, in: window)
            seen.append(String(format: "%.0fms %@ %@", Date().timeIntervalSince(start) * 1000, pager.moving ? "m" : "-", x.map { String(format: "%.1f", $0) } ?? "nil"))
            if pager.moving { sawMoving = true } else if sawMoving { at = x ?? 99 }
        }
        print("ShellPagerSettle to \(page): \(seen.suffix(14).joined(separator: " | "))")
        #expect(sawMoving)
        let x = try? #require(at, "the slide never ended")
        // The snapshot is the last frame drawn, which under load can be one frame of the spring's tail behind (< 1 pt);
        // ended "logically" the page was still ~5 pt out.
        #expect(abs(x ?? 99) <= 1, "the strip stopped moving with the page still \(x ?? 99) pt from its place")
        #expect(pager.page == page)
    }
}
