#if os(iOS)
import SwiftUI
import Testing
import UIKit
@testable import Persona

/// iPhone Duo's tab bar on the outer display (#4231): its count on the symbol's well, laid out in a page whose safe area
/// has the system's column down the trailing side and the home indicator's room at the foot. The open Duo shows the
/// system's tab bar (#10362), so the alignment of a second bar on the inner display is no longer laid out here.
@MainActor struct DuoTabBarAlignmentTests {
    private static let column: CGFloat = 84, foot: CGFloat = 34

    final class Probe {
        var frame: CGRect = .zero
        var typeSize: DynamicTypeSize?
        var well: CGRect = .zero
    }

    /// `make(probe)` laid out in a page `size` big whose safe area has the system's column on the trailing side and the
    /// home indicator's room at the foot, at `typeSize`, once its bar's frame settles: that frame in the page and its text
    /// size. The page is a child of the test's window, laid out away from the window's own bars (a window takes its
    /// scene's size, whatever simulator runs the tests), so its insets are only the Duo's.
    private func place(size: CGSize, trailing: CGFloat, bottom: CGFloat = Self.foot, typeSize: DynamicTypeSize,
                       _ make: @escaping (Probe) -> AnyView) async throws -> (frame: CGRect, typeSize: DynamicTypeSize?, insets: UIEdgeInsets, well: CGRect) {
        let probe = Probe()
        let scene = try #require(UIApplication.shared.connectedScenes.compactMap { $0 as? UIWindowScene }.first)
        let window = UIWindow(windowScene: scene)
        window.windowLevel = .alert + 1
        let root = UIViewController()
        window.rootViewController = root
        window.makeKeyAndVisible()
        defer { window.isHidden = true }
        let host = UIHostingController(rootView: make(probe).environment(\.dynamicTypeSize, typeSize).environment(AppModel.shared))
        root.addChild(host)
        root.view.addSubview(host.view)
        let origin = CGPoint(x: window.bounds.maxX + 200, y: window.bounds.maxY + 200)
        host.view.frame = CGRect(origin: origin, size: size)
        host.didMove(toParent: root)
        // What UIKit gives it there of its own (the window's), then the rest of the Duo's insets on those two sides.
        window.layoutIfNeeded()
        let own = host.view.safeAreaInsets
        host.additionalSafeAreaInsets = UIEdgeInsets(top: 0, left: 0, bottom: max(0, bottom - own.bottom),
                                                     right: max(0, trailing - own.right))
        var last = CGRect.null, steady = 0
        for _ in 0..<60 where steady < 4 {
            try await Task.sleep(for: .milliseconds(50))
            window.layoutIfNeeded()
            steady = probe.frame == last && probe.frame.width > 0 ? steady + 1 : 0
            last = probe.frame
        }
        let insets = try #require(host.view?.safeAreaInsets)
        #expect(insets.bottom == max(bottom, own.bottom) && insets.right == max(trailing, own.right),
                "the page's insets are not the Duo's: \(insets) (its own \(own))")
        let at = host.view.convert(CGPoint.zero, to: window)
        return (probe.frame.offsetBy(dx: -at.x, dy: -at.y), probe.typeSize, insets, probe.well.offsetBy(dx: -at.x, dy: -at.y))
    }

    /// A count rests on the rim of its symbol's well, the system's tab bar badge (the owner, 2026-11-17: it lay inside the
    /// circle's corner, over most of the symbol): `Size.badge` (18 pt) tall and a 1.5 pt ring in the page's color around
    /// it, round for one digit, wider for more; centred 2 pt inside the well's top trailing corner, so its centre is off the
    /// circle and at most a third of it lies over the circle's edge (5.2 pt; centred on the corner itself, its ring reached
    /// into the name of the item above). One view on both displays.
    @Test func theCountRestsOnTheWellsRim() async throws {
        let ring = Theme.Shell.tabBarBadgeRing, tall = Theme.Size.badge + Theme.Shell.tabBarBadgeRing * 2
        for count in [1, 12, 100] {
            let placed = try await place(size: CGSize(width: 160, height: 160), trailing: 0, bottom: 0, typeSize: .large) { probe in
                AnyView(Color.clear
                    .frame(width: Theme.Size.well, height: Theme.Size.well)
                    .onGeometryChange(for: CGRect.self) { $0.frame(in: .global) } action: { probe.well = $0 }
                    .overlay(alignment: .topTrailing) {
                        TabBarCount(count: count)
                            .onGeometryChange(for: CGRect.self) { $0.frame(in: .global) } action: { probe.frame = $0 }
                            .tabBarBadgeSpot()
                    })
            }
            let badge = placed.frame, well = placed.well
            #expect(abs(badge.height - tall) <= 0.5, "\(count): \(badge.height) pt tall with its ring, not \(tall)")
            if count < 10 { #expect(abs(badge.width - badge.height) <= 0.5, "1: not round: \(badge)") }
            else { #expect(badge.width > badge.height + 2, "\(count): not a capsule: \(badge)") }
            #expect(abs(badge.midX - (well.maxX - Theme.Shell.tabBarBadgeInset)) <= 0.5 &&
                    abs(badge.midY - (well.minY + Theme.Shell.tabBarBadgeInset)) <= 0.5, "\(count): \(badge) on \(well)")
            if count < 10 {
                let red = Theme.Size.badge / 2, circle = Theme.Size.well / 2
                let apart = hypot(badge.midX - well.midX, badge.midY - well.midY)
                #expect(apart > circle && circle + red - apart <= Theme.Size.badge / 3 && circle + red + ring - apart > 0,
                        "covers \(circle + red - apart) pt of the circle: \(badge) on \(well)")
            }
        }
    }
}
#endif
