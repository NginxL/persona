#if os(iOS)
import SwiftUI
import Testing
import UIKit
@testable import Persona

/// iPhone Duo's tab bar stands in one place whether the Duo is folded or open on its side (#4231; the owner, 2026-11-17:
/// the two looked out of line). The outer display's rail (the page's face and action under the bar, as Assistant,
/// Agents and Messages draw it) and the open Duo's (`TabSwitchSideRail`, their room left empty) are laid out in windows
/// the two displays' sizes, with the system's column down the trailing side (84 pt) and the home indicator's 34 pt at
/// the foot, as measured on both displays. At every text size the bar is the same size, as far from the screen's edge
/// and from its foot (to within a point), and gets the same text size, so its symbols, names, circle and counts, one
/// view in both, are drawn alike. The bar along the foot upright (`TabSwitchFootBar`) gets that text size too.
///
/// Before: on the open Duo the bar's foot was a quarter of the column above the column's foot (166.7 pt), on the outer
/// display the face and New Chat under it (144.5 pt), so it stood 22 pt higher; and below the default text size the
/// open Duo's names were a size smaller, its rail not scaled as the pages' rails are.
@MainActor struct DuoTabBarAlignmentTests {
    private static let outer = CGSize(width: 466, height: 678)
    private static let side = CGSize(width: 951, height: 669)
    private static let upright = CGSize(width: 669, height: 951)
    private static let column: CGFloat = 84, foot: CGFloat = 34
    /// The app caps the interface at xxxLarge (`Theme.interfaceLargestTypeSize`): every size up to it.
    private static let sizes: [DynamicTypeSize] = [.xSmall, .small, .medium, .large, .xLarge, .xxLarge, .xxxLarge]

    final class Probe {
        var frame: CGRect = .zero
        var typeSize: DynamicTypeSize?
        var well: CGRect = .zero
    }

    /// The switch's stand-in: the side tab bar's size (its items are fixed points), reporting its frame in the window and
    /// the text size it is given.
    private struct Bar: View {
        let probe: Probe
        var vertical = true
        @Environment(\.dynamicTypeSize) private var typeSize

        var body: some View {
            Color.clear
                .frame(width: vertical ? TabSwitchLayout.sideWidth : TabSwitchLayout.bottomWidth(items: 3),
                       height: vertical ? TabSwitchLayout.sideHeight(items: 3) : TabSwitchLayout.bottomHeight)
                .onGeometryChange(for: CGRect.self) { $0.frame(in: .global) } action: { probe.frame = $0 }
                .onChange(of: typeSize, initial: true) { _, size in probe.typeSize = size }
        }
    }

    /// The outer display's rail as the pages draw it (`AssistantTopBar`, `AgentsTopBar`, Messages): the face and the
    /// page's action under the bar, the rail at the shell's text size.
    private struct OuterRail: View {
        let probe: Probe

        var body: some View {
            ShellSideRail(edge: .trailing) {
                ShellPersonaButton(id: "face") {}
                ShellCircleButton(symbol: Theme.Icon.compose, label: "New Chat", id: "new") {}
            } switcher: {
                Bar(probe: probe)
            }
            .shellTopBarScaled()
        }
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

    /// Down the side: the same bar, as far from the screen's trailing edge and from its foot, on both displays, at every
    /// text size; 17 pt from the edge (centred 49 pt in, on the status bar) and the face and action's room under it.
    @Test func theSideBarStandsAlikeFoldedAndOpen() async throws {
        for size in Self.sizes {
            let folded = try await place(size: Self.outer, trailing: Self.column, typeSize: size) { AnyView(OuterRail(probe: $0)) }
            let open = try await place(size: Self.side, trailing: Self.column, typeSize: size) { probe in
                AnyView(TabSwitchSideRail(edge: .trailing) { Bar(probe: probe) })
            }
            let a = folded.frame, b = open.frame
            #expect(a.size == b.size && a.width == TabSwitchLayout.sideWidth, "\(size): the bars differ in size: \(a) vs \(b)")
            let edgeA = Self.outer.width - a.maxX, edgeB = Self.side.width - b.maxX
            #expect(abs(edgeA - edgeB) <= 1 && abs(edgeA - 17) <= 0.5, "\(size): from the screen's edge \(edgeA) vs \(edgeB)")
            let footA = Self.outer.height - a.maxY, footB = Self.side.height - b.maxY
            #expect(abs(footA - footB) <= 1, "\(size): from the screen's foot \(footA) folded vs \(footB) open")
            // the face and action's room under it, as the rail's buttons are at that text size
            let button = Theme.Size.tapTarget * Theme.sizeScale(Self.scaled(size))
            #expect(abs(footA - Self.foot - TabSwitchLayout.sideFoot(button: button)) <= 1, "\(size): foot \(footA)")
            #expect(folded.typeSize != nil && folded.typeSize == open.typeSize,
                    "\(size): the names' text size differs: \(String(describing: folded.typeSize)) vs \(String(describing: open.typeSize))")
        }
    }

    /// Along the foot upright the bar gets the text size it gets down the side, so its names are drawn alike there too.
    @Test func theFootBarHasTheSideBarsTextSize() async throws {
        for size in Self.sizes {
            let rail = try await place(size: Self.side, trailing: Self.column, typeSize: size) { probe in
                AnyView(TabSwitchSideRail(edge: .trailing) { Bar(probe: probe) })
            }
            let foot = try await place(size: Self.upright, trailing: 0, typeSize: size) { probe in
                AnyView(TabSwitchFootBar { Bar(probe: probe, vertical: false) })
            }
            #expect(foot.typeSize != nil && foot.typeSize == rail.typeSize, "\(size): \(String(describing: foot.typeSize)) vs \(String(describing: rail.typeSize))")
            let middle = (foot.insets.left + Self.upright.width - foot.insets.right) / 2 // the window's own column, if any, apart
            #expect(abs(foot.frame.midX - middle) <= 0.5, "\(size): not centred: \(foot.frame) in \(foot.insets)")
            #expect(abs(Self.upright.height - Self.foot - foot.frame.maxY - TabSwitchLayout.bottomGap) <= 0.5, "\(size): \(foot.frame)")
        }
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

    /// The shell's chrome a size up, no larger than the top bars' largest (`shellTopBarScaled`).
    private static func scaled(_ size: DynamicTypeSize) -> DynamicTypeSize {
        let all = DynamicTypeSize.allCases
        return min(all[min((all.firstIndex(of: size) ?? 0) + 1, all.count - 1)], Theme.Shell.topBarLargestTypeSize)
    }
}
#endif
