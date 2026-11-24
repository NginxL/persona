import CoreLocation
import Foundation
#if os(iOS)
import UIKit
#endif

// Location while Persona isn't open (#13200, docs/apple/phone-tools.md "Location when Persona isn't open"). With While
// Using, iOS gives an app its location only while it is in use: in front, and a few seconds after it leaves. A silent
// push that wakes Persona for `phone_location_current` later gets nothing. With Always, and while Persona holds a
// `CLServiceSession(authorization: .always)` (WWDC24 10212: from iOS 18 Always is effective only while one is held), the
// push-woken read takes its one fix as in front. The significant-change service lets iOS launch Persona again after the
// phone moves about 500 m, when the app was ended; nothing it reports is kept or sent. No continuous updates and no
// `location` background mode.

/// What iOS allows Persona for location, as this feature reads it.
nonisolated enum LocationLevel: Sendable, Equatable {
    case notDetermined, denied, whileUsing, always

    init(_ status: CLAuthorizationStatus) {
        switch status {
        case .authorizedAlways: self = .always
        case .authorizedWhenInUse: self = .whileUsing
        case .denied, .restricted: self = .denied
        case .notDetermined: self = .notDetermined
        @unknown default: self = .notDetermined
        }
    }
}

/// The offer of Always, as remembered on the device: a few flags written when something happens, never per frame.
nonisolated struct LocationAlwaysState: Equatable, Sendable {
    /// A read failed while Persona was not in front, with only While Using allowed: the next time it is opened it offers
    /// Always.
    var pending = false
    /// The explanation sheet was shown. It is shown once.
    var shown = false
    /// iOS was asked for Always. iOS asks once; after that only its Settings can change it.
    var asked = false
}

/// The decisions, without Core Location: tests check them with any state.
nonisolated enum BackgroundLocationPolicy {
    /// Whether Persona holds the Always session and runs the significant-change service: only with Always granted and
    /// Location on in Settings › Connectors › This iPhone. A session whose goal is Always makes iOS ask for Always as soon
    /// as Persona is in front, so holding one for a user who hasn't chosen it would ask at launch (#13200).
    static func runs(_ level: LocationLevel?, sourceOn: Bool) -> Bool {
        level == .always && sourceOn
    }

    /// Whether Persona offers Always now: in front after a read failed in the background for While Using, the sheet not
    /// shown and iOS not asked before, and the level still While Using with Location on.
    static func offers(_ state: LocationAlwaysState, level: LocationLevel?, sourceOn: Bool, inFront: Bool) -> Bool {
        inFront && sourceOn && level == .whileUsing && state.pending && !state.shown && !state.asked
    }

    /// Whether Allow (the sheet's, or the Location row's Allow Always) can still ask iOS: While Using, not asked before.
    /// Otherwise the answer is in iOS Settings.
    static func canAsk(_ level: LocationLevel?, asked: Bool) -> Bool {
        level == .whileUsing && !asked
    }

    /// The line under Location in Settings › Connectors › This iPhone, and its one button. nil: no line (not asked yet,
    /// refused: the switch says those, as for every source).
    static func line(_ level: LocationLevel?, asked: Bool) -> LocationLevelLine? {
        switch level {
        case .whileUsing: LocationLevelLine(level: .whileUsing, action: asked ? .openSettings : .allowAlways)
        case .always: LocationLevelLine(level: .always, action: .openSettings)
        case .notDetermined, .denied, nil: nil
        }
    }
}

/// What the Location row says about when Persona may read the location, and what its button does.
nonisolated struct LocationLevelLine: Equatable, Sendable {
    enum Action: Equatable, Sendable { case allowAlways, openSettings }
    var level: LocationLevel
    var action: Action

    var text: String {
        level == .always
            ? String(localized: "Also when Persona isn't open")
            : String(localized: "Only while Persona is open")
    }

    var actionTitle: String {
        switch action {
        case .allowAlways: String(localized: "Allow Always [Location]")
        case .openSettings: String(localized: "Open Settings")
        }
    }
}

/// The explanation sheet's points, in order. Pure, so tests count them without the view.
nonisolated enum LocationAlwaysPoint: CaseIterable, Identifiable, Sendable {
    /// Read only when the assistant needs it to answer.
    case readWhenNeeded
    /// iOS may wake Persona after a long move; nothing records where the user goes.
    case noTrail

    var id: Self { self }

    var symbol: String {
        switch self {
        case .readWhenNeeded: Theme.Icon.currentLocation
        case .noTrail: Theme.Icon.privacy
        }
    }

    var text: String {
        switch self {
        case .readWhenNeeded:
            String(localized: "The assistant reads your location only when it needs it to answer you, for example when you ask “How far am I from home?” while Persona isn't open.")
        case .noTrail:
            String(localized: "When you move a long way, iOS may wake Persona in the background, but it doesn't record where you go.")
        }
    }
}

#if os(iOS)
/// What Persona asks of Core Location for this feature. The app's is `CoreLocationSystem`; tests count calls on their own.
protocol BackgroundLocationSystem: AnyObject {
    /// Starts hearing the level: `heard` gets it soon after (iOS reports it to a new manager) and on every change.
    func start(heard: @escaping (LocationLevel) -> Void)
    /// iOS's Always question. It shows the dialog only while Persona is in front, and only once.
    func requestAlways()
    /// Holds `CLServiceSession(authorization: .always)`; the closure ends it.
    func holdAlwaysSession() -> () -> Void
    func significantChanges(_ on: Bool)
}

/// Location while Persona isn't open: holds the Always session and the significant-change service while they apply,
/// remembers a read refused in the background, and offers Always once when Persona is next opened.
@Observable final class BackgroundLocation {
    static let shared = BackgroundLocation(system: CoreLocationSystem())

    /// What iOS allows; nil until Core Location has said (soon after `launch`).
    private(set) var level: LocationLevel?
    /// The explanation sheet is up (the root's `locationAlwaysOffer()`).
    var offering = false
    private(set) var state: LocationAlwaysState

    @ObservationIgnored private let system: any BackgroundLocationSystem
    @ObservationIgnored private let defaults: UserDefaults
    @ObservationIgnored private let sourceOn: () -> Bool
    @ObservationIgnored private let inFront: () -> Bool
    @ObservationIgnored private var started = false
    @ObservationIgnored private var endSession: (() -> Void)?
    @ObservationIgnored private var monitoring = false
    #if DEBUG
    /// `-showLocationLevel`: the level the screenshots show, whatever the simulator allows.
    @ObservationIgnored private var pinned = false
    #endif

    init(system: any BackgroundLocationSystem, defaults: UserDefaults = .standard,
         sourceOn: @escaping () -> Bool = { PhoneDataSource.location.isEnabled },
         inFront: @escaping () -> Bool = { Foreground.isActive }) {
        self.system = system
        self.defaults = defaults
        self.sourceOn = sourceOn
        self.inFront = inFront
        state = LocationAlwaysState(pending: defaults.bool(forKey: Keys.pending), shown: defaults.bool(forKey: Keys.shown),
                                    asked: defaults.bool(forKey: Keys.asked))
    }

    /// First thing at every launch, in front or in the background: Core Location keeps a relaunched app's Always session
    /// and significant-change service only a few seconds (WWDC24 10212). Asks nothing: the session and the service start
    /// only once iOS says Always.
    func launch() {
        guard !started else { return }
        started = true
        #if DEBUG
        if pinDebugLevel() { return }
        #endif
        system.start { [weak self] level in self?.heard(level) }
    }

    /// Core Location's answer, at launch and on every change (Settings, the Always dialog, back from the background).
    func heard(_ level: LocationLevel) {
        #if DEBUG
        if pinned { return }
        #endif
        self.level = level
        if level != .whileUsing, state.pending { save { $0.pending = false } } // nothing left to offer
        apply()
        considerOffer()
    }

    /// Holds or ends the session and the service as the level and the Location switch say now.
    func apply() {
        let runs = BackgroundLocationPolicy.runs(level, sourceOn: sourceOn())
        if runs, endSession == nil {
            endSession = system.holdAlwaysSession()
        } else if !runs, let end = endSession {
            end()
            endSession = nil
        }
        if runs != monitoring {
            system.significantChanges(runs)
            monitoring = runs
        }
    }

    /// Persona came to the front.
    func cameToFront() { considerOffer() }

    /// A `phone_location_current` read failed while Persona was not in front with only While Using allowed.
    func noteRefusedInBackground() {
        guard !state.pending else { return }
        save { $0.pending = true }
    }

    /// The sheet is on screen: it is not offered again.
    func offerShown() {
        guard !state.shown else { return }
        save { $0.shown = true }
    }

    /// The owner tapped Allow (the sheet's, or the Location row's Allow Always): iOS asks now, once. Its answer arrives
    /// through `heard`; Keep Only While Using reports nothing, and the row then says Open Settings. False when iOS can't
    /// be asked any more: the caller opens iOS Settings.
    @discardableResult func allowAlways() -> Bool {
        guard BackgroundLocationPolicy.canAsk(level, asked: state.asked) else { return false }
        save {
            $0.asked = true
            $0.pending = false
        }
        system.requestAlways()
        return true
    }

    private func considerOffer() {
        guard !offering,
              BackgroundLocationPolicy.offers(state, level: level, sourceOn: sourceOn(), inFront: inFront()) else { return }
        offering = true
    }

    private func save(_ change: (inout LocationAlwaysState) -> Void) {
        change(&state)
        defaults.set(state.pending, forKey: Keys.pending)
        defaults.set(state.shown, forKey: Keys.shown)
        defaults.set(state.asked, forKey: Keys.asked)
    }

    private enum Keys {
        static let pending = "location.always.pending"
        static let shown = "location.always.shown"
        static let asked = "location.always.asked"
    }

    #if DEBUG
    /// `-showLocationLevel always|whileUsing|whileUsingAsked` pins the Location row's line for screenshots, and
    /// `-showLocationAlwaysOffer YES` shows the sheet; neither asks iOS anything.
    private func pinDebugLevel() -> Bool {
        let d = UserDefaults.standard
        if d.bool(forKey: "showLocationAlwaysOffer") {
            pinned = true
            level = .whileUsing
            offering = true
            return true
        }
        switch d.string(forKey: "showLocationLevel") {
        case "always": level = .always
        case "whileUsing": level = .whileUsing
        case "whileUsingAsked":
            level = .whileUsing
            state.asked = true
        default: return false
        }
        pinned = true
        return true
    }
    #endif
}

/// Core Location for `BackgroundLocation`: one manager, made on the main thread at launch, whose delegate hears the level.
final class CoreLocationSystem: BackgroundLocationSystem {
    private var manager: CLLocationManager?
    private let listener = Listener()

    func start(heard: @escaping (LocationLevel) -> Void) {
        listener.heard = heard
        let manager = CLLocationManager()
        manager.delegate = listener // iOS reports the level to it now, and on every change
        self.manager = manager
    }

    func requestAlways() { manager?.requestAlwaysAuthorization() }

    func holdAlwaysSession() -> () -> Void {
        let session = CLServiceSession(authorization: .always)
        return { session.invalidate() }
    }

    func significantChanges(_ on: Bool) {
        guard let manager, CLLocationManager.significantLocationChangeMonitoringAvailable() else { return }
        if on {
            manager.startMonitoringSignificantLocationChanges()
        } else {
            manager.stopMonitoringSignificantLocationChanges()
        }
    }

    /// The manager's delegate. Its events come on the main thread, where the manager was made.
    private final class Listener: NSObject, CLLocationManagerDelegate {
        var heard: ((LocationLevel) -> Void)?

        func locationManagerDidChangeAuthorization(_ manager: CLLocationManager) {
            heard?(LocationLevel(manager.authorizationStatus))
        }

        /// A significant change only wakes Persona: the fix is not kept or sent anywhere (#13200).
        func locationManager(_ manager: CLLocationManager, didUpdateLocations locations: [CLLocation]) {}

        func locationManager(_ manager: CLLocationManager, didFailWithError error: any Error) {}
    }
}
#endif
