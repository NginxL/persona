import SwiftUI

extension EnvironmentValues {
    /// The edge the system puts its bars on, down the side, under the shell (iPhone Duo's outer display, and its inner
    /// display on its side, docs/apple/ios.md「iPhone Duo」): the shell's top bars become a rail there (`ShellSideRail`). nil:
    /// bars across the top, as on every other iPhone and on the inner display upright.
    @Entry var shellRailEdge: HorizontalEdge?
}

#if os(iOS) || os(visionOS)
extension View {
    /// Where the system presents bars vertically (`toolbarVerticalEdge`, iOS 27.1), nil when it does not: always nil
    /// before iOS 27.1 and when built with Xcode 26 (Swift ≤ 6.3 has no such key path).
    func readsVerticalBarEdge(_ edge: Binding<HorizontalEdge?>) -> some View { modifier(VerticalBarEdgeReader(edge: edge)) }
}

private struct VerticalBarEdgeReader: ViewModifier {
    @Binding var edge: HorizontalEdge?

    func body(content: Content) -> some View {
        #if compiler(>=6.4) && os(iOS)
        if #available(iOS 27.1, *) {
            content.modifier(SystemVerticalBarEdge(edge: $edge))
        } else {
            content
        }
        #else
        content
        #endif
    }
}

#if compiler(>=6.4) && os(iOS)
@available(iOS 27.1, *)
private struct SystemVerticalBarEdge: ViewModifier {
    @Environment(\.toolbarVerticalEdge) private var system
    @Binding var edge: HorizontalEdge?

    func body(content: Content) -> some View {
        content.onChange(of: system, initial: true) { _, e in edge = e }
    }
}
#endif

/// The shell's top bar where the system puts its bars down the side (iPhone Duo, `shellRailEdge`): the column beside the
/// status bar, with the switch as the Duo's tab bar (#4231) in the column's lower middle, as a tab bar down the side of the
/// outer display stands, not a small block at the foot (the owner's reference), and the page's buttons (the face, the
/// page's action) under it at the foot, where the thumb of the hand holding the phone reaches them (#1057). Where there
/// are no buttons (the open Duo's tabs) their room stays empty (`ShellRailButtonRoom`), so the tab bar stands at the same
/// height above the column's foot on both displays and stays put when the Duo folds or unfolds (the column's values:
/// `Theme.Shell.sideRailFoot`, `sideRailSpacing`, `sideTabBarGap`). All of it is centred on the status bar (the column less
/// the strip at the screen's edge, `Theme.Shell.sideColumnEdge`). The column is the side's safe area, so the content stays
/// clear of it; its top is the status bar's and the camera's.
struct ShellSideRail<Buttons: View, Switcher: View>: View {
    let edge: HorizontalEdge
    @ViewBuilder var buttons: Buttons
    @ViewBuilder var switcher: Switcher
    /// The pages under it keep their own buttons in the column clear of the tab bar (#9837): its top goes there.
    @Environment(\.sideColumn) private var sideColumn
    @State private var id = UUID()

    var body: some View {
        // The column is what the page gives up on that side: its width with the side's safe area, less its width
        // without (a reader that ignores the safe area reports no inset of its own there).
        GeometryReader { inside in
            GeometryReader { whole in
                let column = max(0, whole.size.width - inside.size.width)
                VStack(spacing: Theme.Shell.sideRailSpacing) {
                    switcher
                        // From the column's top (the safe area's), where the system's own items are measured from.
                        .onGeometryChange(for: CGFloat.self) { $0.frame(in: .named(Self.column)).minY } action: { top in
                            sideColumn?.report(id, top: top, windowWidth: whole.size.width)
                        }
                        .onDisappear { sideColumn?.report(id, top: nil, windowWidth: whole.size.width) }
                        .padding(.bottom, Theme.Shell.sideTabBarGap)
                    if Buttons.self == EmptyView.self { ShellRailButtonRoom() } else { buttons }
                }
                .frame(width: TabSwitchLayout.sideColumn(inset: column))
                // Off the strip at the screen's edge, as the status bar is.
                .padding(edge == .trailing ? .trailing : .leading, max(0, column - TabSwitchLayout.sideColumn(inset: column)))
                .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: edge == .trailing ? .bottomTrailing : .bottomLeading)
                .padding(.bottom, Theme.Shell.sideRailFoot)
            }
            .coordinateSpace(.named(Self.column))
            .ignoresSafeArea(.container, edges: edge == .trailing ? .trailing : .leading)
        }
    }

    private static var column: String { "shell-side-rail.column" }
}

/// Room in a rail for what the system draws at the column's foot itself (a page's search, #9837): as tall as the rail's
/// round buttons, so the tab bar over it stands where it does on every other page, and the system's own clear of the
/// face over it.
struct ShellRailSlot: View {
    var body: some View {
        Color.clear
            .scaledFrame(width: Theme.Size.tapTarget, height: Theme.Size.tapTarget)
            .accessibilityHidden(true)
    }
}

/// The room the outer display's rail gives its buttons under the tab bar, left empty on the open Duo's tabs: the face
/// (`ShellPersonaButton`) over the page's action (`ShellCircleButton`), each the tap target at the rail's text size,
/// `Theme.Shell.sideRailSpacing` apart (`ShellSideRailTests` lays both rails out and compares). Their frames, not the
/// buttons: nothing to tap or read there.
struct ShellRailButtonRoom: View {
    var body: some View {
        VStack(spacing: Theme.Shell.sideRailSpacing) {
            Color.clear.scaledFrame(width: Theme.Size.tapTarget, height: Theme.Size.tapTarget)
            Color.clear.scaledFrame(width: Theme.Size.tapTarget, height: Theme.Size.tapTarget)
        }
        .allowsHitTesting(false)
        .accessibilityHidden(true)
    }
}
#endif
