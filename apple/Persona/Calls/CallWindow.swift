#if os(iOS)
import SwiftUI
import UIKit

// The call to Persona's own window (docs/design/DESIGN_SYSTEM.md "Call screen" and "Return-to-call bar"): the call
// screen and the bar back to a minimised call live in a window of their own above the app's, so both are there on every
// page and over every sheet, full-screen cover and the + panel, and opening the call screen dismisses nothing the owner
// was doing (it comes over it, and Minimize goes back to it). The window takes touches only where it shows something:
// the whole screen for the call screen and a refusal's alert, the bar alone while minimised, none otherwise.

/// What the call's window shows.
nonisolated enum CallWindowMode: Equatable, Sendable {
    case none
    /// The minimised call's bar at the top: only the bar takes touches.
    case bar
    /// The call screen over everything.
    case screen
    /// Why a call to Persona could not be placed (the voice alert), over everything.
    case refusal

    /// What the window shows, from the call manager's state: the call screen while it is presented (not under an
    /// answered urgent call's page, which goes over the app instead), a refusal's alert, the bar while a call to Persona
    /// is live and minimised, or while the phone's voice chat is on away from its page (`voiceAway`, #9887 one button).
    @MainActor static func make(presented: Bool, owner: PersonaCall?, refusal: Bool, urgentUp: Bool, voiceAway: Bool = false) -> CallWindowMode {
        if presented, owner != nil, !urgentUp { return .screen }
        if refusal { return .refusal }
        if let owner, owner.phase.isLive, !urgentUp { return .bar }
        if voiceAway, !urgentUp { return .bar }
        return .none
    }
}

/// What the window's SwiftUI root reads.
@MainActor @Observable final class CallWindowState {
    var mode: CallWindowMode = .none
    /// The bar as drawn, in the window (for its touches and the room the pages under it get).
    @ObservationIgnored var barFrame: CGRect = .null {
        didSet { if barFrame != oldValue { changed?() } }
    }
    @ObservationIgnored var changed: (() -> Void)?
}

/// The call's window over the shell's: made the first time something shows, on the shell window's scene, at
/// `.normal + 0.75` (above the + panel's window, `.normal + 0.25`, and the screen viewer's, `.normal + 0.5`; under the
/// toasts', `.normal + 1`, and the system's alerts and keyboard).
@MainActor final class CallWindow {
    static let shared = CallWindow()
    static let level = UIWindow.Level(rawValue: UIWindow.Level.normal.rawValue + 0.75)

    let state = CallWindowState()
    private weak var shell: UIWindow?
    private var window: PassThrough?
    /// The window that was key before the call screen took it, given back when it goes.
    private weak var previousKey: UIWindow?
    /// Windows whose content VoiceOver skipped while the call screen was up (it is modal, as a full-screen cover was).
    private var silenced: [Weak] = []
    /// The room given to pages under the bar, by controller: what was set, to take back only that.
    private var given: [ObjectIdentifier: (controller: Weak, top: CGFloat)] = [:]
    private var watching: Task<Void, Never>?
    private var hiding: Task<Void, Never>?
    /// The bar shows (the room under it is looked at while the app is in front).
    private var barShown = false
    /// The app is in front (active): the room watch runs only then.
    private var active = UIApplication.shared.applicationState == .active
    private var observers: [NSObjectProtocol] = []

    private init() {
        state.changed = { [weak self] in self?.barMoved() }
        // The room watch stops as the app leaves the front (Home, the lock, another app: the bar stays, the call goes
        // on, nothing is seen) and starts again with the room put right at once as it comes back.
        let center = NotificationCenter.default
        observers.append(center.addObserver(forName: UIApplication.didBecomeActiveNotification, object: nil, queue: .main) { @Sendable [weak self] _ in
            MainActor.assumeIsolated { self?.foreground(true) }
        })
        observers.append(center.addObserver(forName: UIApplication.willResignActiveNotification, object: nil, queue: .main) { @Sendable [weak self] _ in
            MainActor.assumeIsolated { self?.foreground(false) }
        })
    }

    /// Shows `mode` over `shell` (the window the app's root is in; nil: the last one seen).
    func show(_ mode: CallWindowMode, over shell: UIWindow?) {
        if let shell, shell !== window { self.shell = shell }
        guard mode != .none else { return hide() }
        guard let shell = self.shell, let scene = shell.windowScene else { return }
        hiding?.cancel()
        hiding = nil
        let w = window ?? make(scene, over: shell)
        let wasHidden = w.isHidden
        w.mode = mode
        w.barRect = state.barFrame
        w.isHidden = false
        if wasHidden {
            // Shown first, then what it shows, so the call screen slides in rather than being there at once.
            // Not if something else was asked for meanwhile.
            Task { @MainActor [state, weak w] in if w?.mode == mode { state.mode = mode } }
        } else if state.mode != mode {
            state.mode = mode
        }
        if mode == .screen || mode == .refusal { takeOver(w) } else { giveBack() }
        w.host?.setNeedsStatusBarAppearanceUpdate()
        applyRoom()
        watchRoom(mode == .bar)
    }

    private func hide() {
        guard let w = window, !w.isHidden else { return }
        state.mode = .none
        w.mode = .none
        giveBack()
        watchRoom(false)
        applyRoom()
        w.host?.setNeedsStatusBarAppearanceUpdate()
        hiding?.cancel()
        hiding = Task { @MainActor [weak self] in
            try? await Task.sleep(for: Theme.Motion.callScreenOut)
            guard !Task.isCancelled, let self, self.state.mode == .none else { return }
            self.window?.isHidden = true
        }
    }

    private func make(_ scene: UIWindowScene, over shell: UIWindow) -> PassThrough {
        let w = PassThrough(windowScene: scene)
        w.windowLevel = Self.level
        w.backgroundColor = .clear
        // Settings › Appearance › Light & Dark reaches every window of the scene as it changes (`ColorSchemeChoice`).
        w.overrideUserInterfaceStyle = shell.overrideUserInterfaceStyle
        let host = CallWindowHost(rootView: CallWindowRoot(state: state, model: AppModel.shared))
        host.view.backgroundColor = .clear
        host.shell = { [weak self] in self?.shell }
        host.mode = { [weak w] in w?.mode ?? .none }
        w.rootViewController = host
        w.isHidden = true
        window = w
        return w
    }

    // MARK: Key and VoiceOver

    /// The call screen (or the alert) is the one thing in front: it takes the keyboard's focus (what was typed under it
    /// waits, its keyboard down) and VoiceOver reads only it.
    private func takeOver(_ w: PassThrough) {
        if !w.isKeyWindow {
            let scene = w.windowScene
            previousKey = scene?.windows.first { $0.isKeyWindow && $0 !== w } ?? shell
            previousKey?.endEditing(true)
            w.makeKey()
        }
        guard silenced.isEmpty, let scene = w.windowScene else { return }
        for other in scene.windows where other !== w && other.windowLevel < Self.level && !other.accessibilityElementsHidden {
            other.accessibilityElementsHidden = true
            silenced.append(Weak(other))
        }
    }

    private func giveBack() {
        for entry in silenced { (entry.object as? UIWindow)?.accessibilityElementsHidden = false }
        silenced = []
        guard let w = window, w.isKeyWindow else { return }
        let back = previousKey.flatMap { $0.isHidden ? nil : $0 } ?? shell
        back?.makeKey()
        previousKey = nil
    }

    // MARK: Room under the bar

    private func barMoved() {
        window?.barRect = state.barFrame
        applyRoom()
    }

    /// While the bar shows, what is over the app (a sheet, a cover, the + panel's sheet) can change at any time and UIKit
    /// says nothing: it is looked at again every `callBarRoomCheck`, while the app is in front only (`watchesRoom`). Out
    /// of the front the watch is cancelled, not just idle: nothing wakes the main actor for it through a call in the
    /// background or under the lock.
    private func watchRoom(_ on: Bool) {
        barShown = on
        followRoom()
    }

    /// The app came to the front or left it: back in front, the room is put right at once, then watched again.
    private func foreground(_ on: Bool) {
        guard active != on else { return }
        active = on
        if on, barShown { applyRoom() }
        followRoom()
    }

    private func followRoom() {
        guard Self.watchesRoom(bar: barShown, active: active) else {
            watching?.cancel()
            watching = nil
            return
        }
        guard watching == nil else { return }
        watching = Task { @MainActor [weak self] in
            while !Task.isCancelled {
                try? await Task.sleep(for: Theme.Motion.callBarRoomCheck)
                guard !Task.isCancelled, let self else { return }
                self.applyRoom()
            }
        }
    }

    /// Whether the room under the bar is watched: the bar shows and the app is in front (pure, for the tests).
    nonisolated static func watchesRoom(bar: Bool, active: Bool) -> Bool { bar && active }

    /// Every page under the bar starts below it: the app's root, and every controller presented over it or over another
    /// window under the call's (a sheet, a full-screen cover, the + panel's sheet) whose content would start under the
    /// bar, gets that much more safe area at its top (`additionalSafeAreaInsets`). A form sheet lower down needs none, nor
    /// does an alert (`takesRoom`). The toasts' window above the call's gets it too (`givesRoom`).
    /// Without the bar everything given is taken back.
    private func applyRoom() {
        let bar = window?.mode == .bar ? state.barFrame : .null
        var seen = Set<ObjectIdentifier>()
        if let scene = (shell ?? window)?.windowScene {
            for w in scene.windows where w !== window && !w.isHidden && Self.givesRoom(to: w) {
                var next = w.rootViewController
                while let controller = next {
                    if !controller.isBeingDismissed { give(controller, Self.room(for: controller, under: bar), &seen) }
                    next = controller.presentedViewController
                }
            }
        }
        for (id, entry) in given where !seen.contains(id) {
            if let controller = entry.controller.object as? UIViewController,
               abs(controller.additionalSafeAreaInsets.top - entry.top) < 0.5 {
                controller.additionalSafeAreaInsets.top = 0
            }
            given[id] = nil
        }
    }

    /// The windows whose pages get the bar's room: the app's own under the call's (the shell's, the + panel's, the screen
    /// viewer's) and the toasts' above it (`TakesCallBarRoom`: a toast sits just below the top bars, which the room moved
    /// down); never the system's (the keyboard's, its alerts').
    static func givesRoom(to window: UIWindow) -> Bool {
        (window.windowLevel >= .normal && window.windowLevel < level) || window is TakesCallBarRoom
    }

    private func give(_ controller: UIViewController, _ top: CGFloat, _ seen: inout Set<ObjectIdentifier>) {
        let id = ObjectIdentifier(controller)
        guard top > 0 || given[id] != nil else { return }
        seen.insert(id)
        if abs(controller.additionalSafeAreaInsets.top - top) > 0.5 { controller.additionalSafeAreaInsets.top = top }
        given[id] = (Weak(controller), top)
    }

    /// How much more room at its top `controller` needs so its content starts below `bar` (in window points): where its
    /// view's content starts without that room (its top on screen, as its presentation puts it at rest, plus what of the
    /// window's own safe area, the status bar's, it lies under) against the bar's bottom and a small gap. Read from the
    /// window, not the controller's safe area, which the room given changes a layout pass later.
    static func room(for controller: UIViewController, under bar: CGRect) -> CGFloat {
        guard !bar.isNull, takesRoom(controller), let view = controller.viewIfLoaded, let window = view.window else { return 0 }
        let frame: CGRect
        if let presentation = controller.presentationController, controller.presentingViewController != nil,
           let container = presentation.containerView {
            frame = container.convert(presentation.frameOfPresentedViewInContainerView, to: nil)
        } else {
            frame = view.convert(view.bounds, to: nil)
        }
        let own = max(0, window.safeAreaInsets.top - frame.minY)
        return room(contentTop: frame.minY + own, barBottom: bar.maxY)
    }

    /// Whether `controller` is a page that can start under the bar. An alert or an action sheet (SwiftUI's `alert` and
    /// `confirmationDialog`) is not: its presentation's frame is the whole screen, but it floats in the middle or at the
    /// bottom, clear of the bar, and room at its top only pushed its words down under its own buttons (the voice chat's
    /// question showed its title low and no message).
    static func takesRoom(_ controller: UIViewController) -> Bool {
        !(controller is UIAlertController)
    }

    /// The room a page whose content starts at `contentTop` needs under a bar ending at `barBottom` (pure, for the tests).
    nonisolated static func room(contentTop: CGFloat, barBottom: CGFloat) -> CGFloat {
        max(0, (barBottom + Theme.Space.xs - contentTop).rounded(.up))
    }

    // MARK: The window

    /// Takes touches only where the call shows something: everywhere for the call screen and an alert, the bar alone
    /// while minimised (a little around it), nowhere else: what is under it takes them.
    final class PassThrough: UIWindow {
        var mode: CallWindowMode = .none
        var barRect: CGRect = .null
        var host: CallWindowHost? { rootViewController as? CallWindowHost }

        override func hitTest(_ point: CGPoint, with event: UIEvent?) -> UIView? {
            switch mode {
            case .screen, .refusal:
                return super.hitTest(point, with: event)
            case .bar:
                guard !barRect.isNull, barRect.insetBy(dx: -Theme.Space.xs, dy: -Theme.Space.xs).contains(point) else { return nil }
                return super.hitTest(point, with: event)
            case .none:
                return nil
            }
        }
    }

    private struct Weak {
        weak var object: AnyObject?
        init(_ object: AnyObject) { self.object = object }
    }
}

/// The window's controller: the status bar and the turning follow the app's own page under it while only the bar shows,
/// and the call screen's while it is up.
final class CallWindowHost: UIHostingController<CallWindowRoot> {
    var shell: () -> UIWindow? = { nil }
    var mode: () -> CallWindowMode = { .none }

    /// The app's page on top under the call's window.
    private var under: UIViewController? {
        var top = shell()?.rootViewController
        while let next = top?.presentedViewController, !next.isBeingDismissed { top = next }
        return top
    }

    /// Only the bar (or nothing): the page under it decides.
    private var passing: Bool { mode() == .bar || mode() == .none }

    override var preferredStatusBarStyle: UIStatusBarStyle {
        passing ? (under?.preferredStatusBarStyle ?? .default) : .default
    }

    override var prefersStatusBarHidden: Bool {
        passing ? (under?.prefersStatusBarHidden ?? false) : false
    }

    override var supportedInterfaceOrientations: UIInterfaceOrientationMask {
        under?.supportedInterfaceOrientations ?? super.supportedInterfaceOrientations
    }
}

/// The call's window's content: the bar at the top under the status bar while the call is minimised, the call screen
/// sliding up over everything while it is shown, and a refusal's alert. In the app's look and text size (it is drawn
/// outside the app's root view: `CoverLook`).
struct CallWindowRoot: View {
    let state: CallWindowState
    let model: AppModel
    @State private var calls = PersonaCalls.shared
    @Environment(\.accessibilityReduceMotion) private var reduceMotion

    var body: some View {
        let mode = state.mode
        ZStack(alignment: .top) {
            if mode == .bar, let call = calls.ownerCall, call.phase.isLive {
                CallReturnPill(call: call) { calls.present() }
                    .onGeometryChange(for: CGRect.self) { $0.frame(in: .global) } action: { state.barFrame = $0 } // perf-ok: the bar's frame, reported only when it changes, is the window's touch area
                    .onDisappear { state.barFrame = .null }
                    .padding(.horizontal, Theme.Space.page)
                    .padding(.top, Theme.Space.xs)
                    .transition(.opacity)
            } else if mode == .bar, let chat = calls.awayVoiceChat {
                // The phone's voice chat away from its page (#9887 one button): back to its conversation.
                VoiceReturnPill(chat: chat, model: model) { calls.returnToVoiceChat(chat) }
                .onGeometryChange(for: CGRect.self) { $0.frame(in: .global) } action: { state.barFrame = $0 } // perf-ok: the bar's frame, reported only when it changes, is the window's touch area
                .onDisappear { state.barFrame = .null }
                .padding(.horizontal, Theme.Space.page)
                .padding(.top, Theme.Space.xs)
                .transition(.opacity)
            }
            if mode == .screen, let call = calls.ownerCall {
                CallScreen(call: call)
                    .transition(reduceMotion ? .opacity : .move(edge: .bottom))
                    .zIndex(1)
            }
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .top)
        .animation(reduceMotion ? Theme.Motion.state : Theme.Motion.callScreen, value: mode)
        .voiceUnavailableAlert(Binding(get: { mode == .refusal ? calls.refusal : nil }, set: { if $0 == nil { calls.refusal = nil } }),
                               model: model)
        .modifier(CoverLook(model: model))
        .environment(model)
    }
}

/// Where the app's root is: the call's window goes over that window's scene.
private struct CallWindowHook: UIViewRepresentable {
    let mode: CallWindowMode

    func makeUIView(context: Context) -> Host {
        let view = Host()
        view.isUserInteractionEnabled = false
        view.isAccessibilityElement = false
        return view
    }

    func updateUIView(_ view: Host, context: Context) {
        view.mode = mode
        CallWindow.shared.show(mode, over: view.window)
    }

    final class Host: UIView {
        var mode: CallWindowMode = .none

        override func didMoveToWindow() {
            super.didMoveToWindow()
            // Out of its window while a full-screen cover is up (an urgent call's page): the last window seen stays.
            guard let window else { return }
            CallWindow.shared.show(mode, over: window)
        }
    }
}

/// The call to Persona's page and the bar back to it, in the call's own window over the app (`CallWindow`), and why a
/// call could not be placed (`PersonaCalls.refusal`); the page shown again when the app comes back to the front with the
/// call on. It reads only `PersonaCalls`, which makes no system call manager until a call needs one (#9264). An answered
/// urgent call's page goes over the app instead (`urgentCallCover`): the call to Persona is on hold meanwhile, and its
/// page comes back after.
struct PersonaCallCover: ViewModifier {
    @State private var calls = PersonaCalls.shared
    @State private var urgent = UrgentCallPresenter.shared
    @Environment(\.scenePhase) private var scenePhase
    /// The app went to the background since it was last in front.
    @State private var away = false

    func body(content: Content) -> some View {
        content
            .background {
                CallWindowHook(mode: CallWindowMode.make(presented: calls.presented, owner: calls.ownerCall,
                                                         refusal: calls.refusal != nil, urgentUp: urgent.session != nil,
                                                         voiceAway: calls.awayVoiceChat != nil))
            }
            .onChange(of: scenePhase) { _, phase in
                if phase == .background { away = true }
                if phase == .active, away {
                    away = false
                    calls.returnedToApp()
                }
            }
    }
}

/// What `RootView` gives every screen and a window outside it would miss: the accent as the tint, the style's font
/// design, the app's text size and BeautifulUI's theme.
struct CoverLook: ViewModifier {
    let model: AppModel

    func body(content: Content) -> some View {
        content.tint(Theme.accent).fontDesign(model.style.fontDesign).modifier(AppTextSize(model: model)).buiTheme(.persona())
    }
}

/// The app's text size as `RootView` gives its pages: the system's, moved by Settings › Text Size (Small / Standard /
/// Large), where the interface stops (`Theme.interfaceTypeSize`). For what is drawn outside RootView, which would take
/// the window's size (the system's, capped: `WindowTextSize`) instead: the call's page and the bar back to it.
private struct AppTextSize: ViewModifier {
    let model: AppModel
    /// The system's size, not the window's (which `WindowTextSize` caps).
    @State private var system = WindowTextSize.system

    func body(content: Content) -> some View {
        content
            .dynamicTypeSize(Theme.interfaceTypeSize(model.textSize.applied(to: system)))
            .onReceive(NotificationCenter.default.publisher(for: UIContentSizeCategory.didChangeNotification)) { _ in
                system = WindowTextSize.system
            }
    }
}

extension View {
    /// The call to Persona's page and the bar back to it, in the call's own window (`PersonaCallCover`).
    func personaCallCover() -> some View { modifier(PersonaCallCover()) }
}
#endif
