import SwiftUI
#if os(iOS)
import UIKit
#endif

/// Settings › Appearance & Display › Persona Banner in Screenshots (#9508), on by default and this device's only: a
/// screenshot of a chat shows the assistant's avatar and name, and GitHub's mark with Persona's repository at the
/// right (`PersonaBrandLockup`, #10172), where the top bar is, while the screen keeps the bar. Off, the bar is drawn as it always was, on screen and in screenshots alike.
nonisolated enum ScreenshotBannerPrefs {
    static let enabledKey = "ui.screenshotBanner"
    static let enabledByDefault = true
}

extension View {
    /// This view on screen, `banner` in screenshots, screen recordings and mirroring (#9508, docs/apple/ios.md
    /// 「Screenshot banner」). The view goes into the layer iOS keeps out of every capture (a secure text field's, the
    /// one that keeps passwords out of screenshots) and the banner is drawn under it, both over `backdrop`: an opaque
    /// fill as tall as `solid` (the whole view when nil) and out over `fade`, carried on to the screen's edges where the
    /// view meets them, so neither shows through the other. Off in Settings, or on a system without that layer, the
    /// view is drawn as it is and screenshots show it.
    func screenshotBanner<Backdrop: View, Banner: View>(solid: CGFloat? = nil, fade: CGFloat = 0,
                                                        @ViewBuilder backdrop: () -> Backdrop,
                                                        @ViewBuilder banner: () -> Banner) -> some View {
        let fill = backdrop()
        return ScreenshotSwap(content: self, backdrop: fill, bannerBackdrop: fill, banner: banner(), solid: solid, fade: fade)
    }

    /// As above, with the banner over a fill of its own: a plain color where the bar's is a material, so the banner
    /// costs no second blur.
    func screenshotBanner<Backdrop: View, BannerBackdrop: View, Banner: View>(solid: CGFloat? = nil, fade: CGFloat = 0,
                                                                              @ViewBuilder backdrop: () -> Backdrop,
                                                                              @ViewBuilder bannerBackdrop: () -> BannerBackdrop,
                                                                              @ViewBuilder banner: () -> Banner) -> some View {
        ScreenshotSwap(content: self, backdrop: backdrop(), bannerBackdrop: bannerBackdrop(), banner: banner(), solid: solid, fade: fade)
    }
}

/// The banner a chat's screenshot shows where its bar is: the brand lockup across it between the page margins (the
/// face and the name at the leading one, the repository at the trailing one), centred in `row` (the bar's row of
/// buttons) or in the whole height; slim where the large one does not fit (a folded header, large text).
struct ScreenshotBannerContent: View {
    /// The row the lockup stands in: from the top by `top`, `height` tall; nil centres it in the whole banner.
    var row: (top: CGFloat, height: CGFloat)?

    var body: some View {
        PersonaBrandLockup(size: .banner)
            .padding(.horizontal, Theme.Space.l)
            .frame(maxWidth: .infinity, alignment: .leading)
            .frame(height: row?.height)
            .padding(.top, row?.top ?? 0)
            .frame(maxHeight: .infinity, alignment: row == nil ? .center : .top)
            .clipped()
            .allowsHitTesting(false)
            .accessibilityHidden(true)
    }
}

private struct ScreenshotSwap<Content: View, Backdrop: View, BannerBackdrop: View, Banner: View>: View {
    let content: Content
    let backdrop: Backdrop
    let bannerBackdrop: BannerBackdrop
    let banner: Banner
    let solid: CGFloat?
    let fade: CGFloat
    @StoredValue(ScreenshotBannerPrefs.enabledKey) private var enabled = ScreenshotBannerPrefs.enabledByDefault
    /// The content's own height as it last reported it (nil until it has): the host is as tall.
    @State private var height: CGFloat?

    var body: some View {
        #if os(iOS)
        if ScreenshotBanner.wraps(enabled) {
            CaptureSwapHost(content: content, backdrop: backdrop, bannerBackdrop: bannerBackdrop, banner: banner, solid: solid, fade: fade) { h in
                if h != height { height = h }
            }
            .frame(height: height)
        } else {
            content
        }
        #else
        content
        #endif
    }
}

#if os(iOS)
/// The capture layer's switch for the whole app: what turns it off where the app takes pictures of its own window.
@MainActor enum ScreenshotBanner {
    /// The text fields that keep the bars' layers out of captures (one per bar on screen).
    private static var fields = NSHashTable<UITextField>.weakObjects()
    private static var reveals = 0
    private static var probed: Bool?

    /// Whether a bar goes into the capture layer: the switch is on and the system has the layer. A bar whose own
    /// background the backdrop replaces leaves it out when this is true.
    static func wraps(_ enabled: Bool) -> Bool { enabled && isAvailable }

    /// Whether this system still has the layer (iOS gives a secure text field a drawing view of its own).
    static var isAvailable: Bool {
        if let probed { return probed }
        let found = SecureCanvas.make() != nil
        probed = found
        return found
    }

    /// Draws the bars into in-app pictures of the window until `endReveal()`: `snapshotView` and `drawHierarchy`
    /// leave the capture layer out like a screenshot, so the iPad's sidebar drag (its picture of the page) and DEBUG
    /// screenshots would show the banner instead of the bar. Nested calls count. Pictures taken at once after it need
    /// `afterScreenUpdates: true` (`hasLayers`).
    static func beginReveal() {
        reveals += 1
        if reveals == 1 { apply() }
    }

    static func endReveal() {
        reveals = max(0, reveals - 1)
        if reveals == 0 { apply() }
    }

    /// Whether a bar is in the capture layer now. A picture taken right after `beginReveal()` must then be taken after
    /// the screen updates (`afterScreenUpdates: true`): one of the frame already drawn still leaves the bars out.
    static var hasLayers: Bool { !fields.allObjects.isEmpty }

    fileprivate static func register(_ field: UITextField) {
        fields.add(field)
        field.isSecureTextEntry = reveals == 0
    }

    private static func apply() {
        for field in fields.allObjects { field.isSecureTextEntry = reveals == 0 }
        CATransaction.flush()
    }
}

/// A secure text field's drawing view, taken out of the field: what it holds is on screen and left out of
/// screenshots, screen recordings and mirroring. Public API only; the field stays alive, since switching its
/// `isSecureTextEntry` is what turns that on and off.
@MainActor enum SecureCanvas {
    static func make() -> (field: UITextField, canvas: UIView)? {
        let field = UITextField()
        field.isSecureTextEntry = true
        guard let canvas = field.subviews.first else { return nil }
        canvas.removeFromSuperview()
        canvas.subviews.forEach { $0.removeFromSuperview() }
        canvas.isUserInteractionEnabled = true
        canvas.clipsToBounds = false
        canvas.backgroundColor = .clear
        return (field, canvas)
    }
}

/// How far past the host's bounds its backdrop reaches: to the screen's edge where the host meets the safe area (the
/// status bar over a top bar), as the bars' fogs do.
@MainActor @Observable private final class CaptureSwapGeometry {
    var outset = EdgeInsets()
}

private struct CaptureSwapHost<Content: View, Backdrop: View, BannerBackdrop: View, Banner: View>: UIViewControllerRepresentable {
    let content: Content
    let backdrop: Backdrop
    let bannerBackdrop: BannerBackdrop
    let banner: Banner
    let solid: CGFloat?
    let fade: CGFloat
    let measured: (CGFloat) -> Void

    func makeUIViewController(context: Context) -> CaptureSwapController {
        let vc = CaptureSwapController()
        vc.show(cover: cover(vc.geometry), banner: bannerRoot(vc.geometry), environment: context.environment)
        return vc
    }

    func updateUIViewController(_ vc: CaptureSwapController, context: Context) {
        vc.show(cover: cover(vc.geometry), banner: bannerRoot(vc.geometry), environment: context.environment)
    }

    /// Before the content has reported its height (the first layout): ask the bar's hosting controller.
    func sizeThatFits(_ proposal: ProposedViewSize, uiViewController vc: CaptureSwapController, context: Context) -> CGSize? {
        guard let width = proposal.width, width.isFinite else { return nil }
        if let h = proposal.height, h.isFinite { return CGSize(width: width, height: h) }
        let fit = vc.coverHost.sizeThatFits(in: CGSize(width: width, height: .greatestFiniteMagnitude))
        return CGSize(width: width, height: ceil(fit.height))
    }

    private func cover(_ geometry: CaptureSwapGeometry) -> AnyView {
        let measured = measured
        return AnyView(
            content
                .fixedSize(horizontal: false, vertical: true)
                .onGeometryChange(for: CGFloat.self, of: { ceil($0.size.height) }) { measured($0) }
                .background(alignment: .top) { CaptureBackdrop(fill: backdrop, solid: solid, fade: fade, geometry: geometry) }
                .frame(maxHeight: .infinity, alignment: .top)
        )
    }

    private func bannerRoot(_ geometry: CaptureSwapGeometry) -> AnyView {
        AnyView(
            banner
                .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .top)
                .background(alignment: .top) { CaptureBackdrop(fill: bannerBackdrop, solid: solid, fade: fade, geometry: geometry) }
        )
    }
}

/// The opaque fill under the bar and under the banner alike: `solid` tall (or the whole host) and out over `fade`,
/// carried on past the host's bounds by the outset.
private struct CaptureBackdrop<Fill: View>: View {
    let fill: Fill
    let solid: CGFloat?
    let fade: CGFloat
    let geometry: CaptureSwapGeometry

    var body: some View {
        GeometryReader { g in
            let o = geometry.outset
            let height = max(1, o.top + (solid ?? g.size.height) + fade)
            fill
                .frame(width: g.size.width + o.leading + o.trailing, height: height)
                .mask {
                    LinearGradient(stops: EasedFade.stops(.black, from: (height - fade) / height, over: fade / height),
                                   startPoint: .top, endPoint: .bottom)
                }
                .offset(x: -o.leading, y: -o.top)
        }
        .allowsHitTesting(false)
        .accessibilityHidden(true)
    }
}

/// The page's environment for the bar's own hosting controller: the layout island's values (`IslandEnvironment`) and
/// the ones the bars read besides (the shell's, the tab's, the chat's link and port openers).
private struct CaptureEnvironment: ViewModifier {
    let env: EnvironmentValues

    init(_ env: EnvironmentValues) { self.env = env }

    func body(content: Content) -> some View {
        content
            .modifier(IslandEnvironment(env))
            .environment(\.shellRailEdge, env.shellRailEdge)
            .environment(\.shellPillFollowsStrip, env.shellPillFollowsStrip)
            .environment(\.tabIsActive, env.tabIsActive)
            .environment(\.openURL, env.openURL)
            .environment(\.openPort, env.openPort)
            .environment(\.openLink, env.openLink)
    }
}

/// The banner's hosting controller under a secure canvas with the bar's: on screen the bar covers the banner, in a
/// capture the canvas is gone and the banner shows. The banner takes no touches and is out of VoiceOver's reach.
private final class CaptureSwapController: UIViewController {
    let geometry = CaptureSwapGeometry()
    let coverHost = UIHostingController(rootView: AnyView(EmptyView()))
    private let bannerHost = UIHostingController(rootView: AnyView(EmptyView()))
    private var field: UITextField?
    /// The bar and the banner as the page last handed them in, and the page's environment for both.
    private var cover = AnyView(EmptyView())
    private var banner = AnyView(EmptyView())
    private var environment = EnvironmentValues()
    /// The window whose look the bar and the banner follow, and the registration for its changes.
    private weak var watched: UIWindow?
    private var watching: UITraitChangeRegistration?

    override func loadView() {
        let root = CaptureSwapView()
        root.geometry = geometry
        root.movedToWindow = { [weak self] in self?.watchWindow() }
        root.clipsToBounds = false
        for host in [bannerHost, coverHost] {
            host.safeAreaRegions = []
            host.sizingOptions = []
            host.view.backgroundColor = .clear
            host.view.clipsToBounds = false
        }
        addChild(bannerHost)
        bannerHost.view.frame = root.bounds
        bannerHost.view.autoresizingMask = [.flexibleWidth, .flexibleHeight]
        bannerHost.view.isUserInteractionEnabled = false
        bannerHost.view.accessibilityElementsHidden = true
        root.addSubview(bannerHost.view)
        bannerHost.didMove(toParent: self)

        addChild(coverHost)
        let canvas: UIView
        if let secure = SecureCanvas.make() {
            field = secure.field
            ScreenshotBanner.register(secure.field)
            canvas = secure.canvas
        } else {
            canvas = UIView() // no capture layer: the bar is drawn as usual and screenshots show it
        }
        canvas.frame = root.bounds
        canvas.autoresizingMask = [.flexibleWidth, .flexibleHeight]
        root.addSubview(canvas)
        coverHost.view.frame = canvas.bounds
        coverHost.view.autoresizingMask = [.flexibleWidth, .flexibleHeight]
        canvas.addSubview(coverHost.view)
        coverHost.didMove(toParent: self)
        view = root
    }

    func show(cover: AnyView, banner: AnyView, environment: EnvironmentValues) {
        self.cover = cover
        self.banner = banner
        self.environment = environment
        render()
    }

    /// In a window, another one or none: follow its look.
    private func watchWindow() {
        let window = viewIfLoaded?.window
        if window !== watched {
            if let watching, let watched { watched.unregisterForTraitChanges(watching) }
            watched = window
            watching = window?.registerForTraitChanges([UITraitUserInterfaceStyle.self]) { [weak self] (_: UIWindow, _) in
                self?.render()
            }
        }
        render()
    }

    /// The page's environment with the window's look in it, and both hosting controllers' traits on that look, for what
    /// UIKit and the glass draw (#9829). The copy of the page's environment is only as new as the last update SwiftUI
    /// sent, and SwiftUI hands the same stale look to this controller's traits: an Agent chat opened for the first time
    /// drew its header and banner light above the dark chat (the drawer's sidebar had the same, #4496). The bars are in
    /// pages drawn in their window's look. Not in a window yet: the page's.
    private func render() {
        var look = environment
        let style = viewIfLoaded?.window?.traitCollection.userInterfaceStyle ?? .unspecified
        if style == .dark { look.colorScheme = .dark } else if style == .light { look.colorScheme = .light }
        for host in [coverHost, bannerHost] where host.overrideUserInterfaceStyle != style {
            host.overrideUserInterfaceStyle = style
        }
        coverHost.rootView = AnyView(cover.modifier(CaptureEnvironment(look)))
        bannerHost.rootView = AnyView(banner.modifier(CaptureEnvironment(look)))
    }
}

private final class CaptureSwapView: UIView {
    weak var geometry: CaptureSwapGeometry?
    /// It went into a window, or out of one.
    var movedToWindow: () -> Void = {}

    override func layoutSubviews() {
        super.layoutSubviews()
        guard let window else { return }
        let f = convert(bounds, to: window)
        let safe = window.safeAreaInsets
        let w = window.bounds.width
        // Only where the view meets the safe area's edge: a bar under the status bar reaches up to the screen's top.
        let outset = EdgeInsets(top: f.minY > 0 && f.minY <= safe.top + 0.5 ? f.minY : 0,
                                leading: f.minX > 0 && f.minX <= safe.left + 0.5 ? f.minX : 0,
                                bottom: 0,
                                trailing: w - f.maxX > 0 && w - f.maxX <= safe.right + 0.5 ? w - f.maxX : 0)
        if geometry?.outset != outset { geometry?.outset = outset }
    }

    override func didMoveToWindow() {
        super.didMoveToWindow()
        setNeedsLayout()
        movedToWindow()
    }
}
#endif
