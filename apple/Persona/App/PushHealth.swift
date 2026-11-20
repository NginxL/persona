import Foundation
import Observation
import PersonaWire

/// Whether this device gets background pushes, and if not why (#5998). A relay that refused this app, moved or could
/// not be reached used to leave one log line; on a hub without its own APNs key nothing came, and nothing said so.
/// Kept from the last registration so Settings › Notifications and the main screen can say it.
nonisolated struct PushHealth: Codable, Equatable, Sendable {
    /// How the last registration with the push relay went.
    enum Relay: Codable, Equatable, Sendable {
        case registered
        /// The relay answered without a grant: its status and words ("403 topic not served").
        case refused(status: Int, message: String)
        /// The relay's address redirects (an old address after a move: builds up to 190). Not followed: a redirected
        /// POST comes back a GET and could never register.
        case moved(status: Int, location: String)
        /// No answer: the network's error.
        case unreachable(String)
        /// This build names no relay.
        case notConfigured
    }

    /// Notifications allowed in Settings (nil: not asked yet).
    var notificationsAllowed: Bool?
    /// Why APNs gave this device no token.
    var apnsError: String?
    var relay: Relay?
    /// Per hub group, the way the hub said it pushes here; missing for an older hub, which does not say.
    var routes: [String: APIClient.PushRoute] = [:]
    /// Groups whose current hub answered that this credential has no push route. Optional for caches from before #9612.
    /// Separate from `routes`: assigning nil there would otherwise look like an older hub that supplied no route data.
    var missingRoutes: Set<String>?
    /// Per hub group, why its route could not be read just now (the registration or Settings › Notifications' check
    /// failed), in the error's words: the page then says it couldn't check, not an older route as if current (#1859).
    /// The next read clears it.
    var unchecked: [String: String]?

    /// What the hub is told when there is no grant: the relay's words in one line.
    var relayError: String? {
        switch relay {
        case .refused(let status, let message): "\(status) \(message)".trimmingCharacters(in: .whitespaces)
        case .moved(let status, let location): "\(status) \(location)"
        case .unreachable(let error): error
        case .registered, .notConfigured, nil: nil
        }
    }
}

/// What Settings › Notifications says about background push.
nonisolated struct PushStatus: Equatable, Sendable {
    enum Level: Sendable { case available, lastFailed, unavailable, unknown }
    var level: Level
    var title: String
    var reason: String?
    var action: String?
}

/// The everyday-language version of an unavailable push status shown outside Settings (#9612 item 4).
/// Diagnostics stay in Settings; the main screen says what is affected and the next useful step.
nonisolated struct PushNoticeContent: Equatable, Sendable {
    enum Repair: Sendable { case notificationSettings, systemSettings }
    var text: String
    var actionTitle: String
    var repair: Repair
}

extension PushHealth {
    /// A confirmed outage only: unknown or unchecked hubs and a route whose latest push merely failed stay quiet.
    func mainNotice(group: String, allowed: Bool?) -> PushNoticeContent? {
        guard status(group: group, allowed: allowed).level == .unavailable else { return nil }
        if (allowed ?? notificationsAllowed) == false {
            return PushNoticeContent(text: String(localized: "Notifications can't reach this device. Allow them in Settings."),
                                     actionTitle: String(localized: "Open Settings"), repair: .systemSettings)
        }
        let settings = String(localized: "Persona Notification Settings")
        if missingRoutes?.contains(group) == true {
            return PushNoticeContent(text: String(localized: "Notifications can't reach this device. Keep Persona open while it tries again."),
                                     actionTitle: settings, repair: .notificationSettings)
        }
        if let route = routes[group], route.reason == "relay_failed" || route.reason == "no_relay" {
            switch relay {
            case .moved:
                return PushNoticeContent(text: String(localized: "Notifications can't reach this device. Update Persona to turn them back on."),
                                         actionTitle: settings, repair: .notificationSettings)
            case .refused, .notConfigured, nil:
                return PushNoticeContent(text: String(localized: "Notifications can't reach this device. Ask the person who set up Persona to finish notification setup."),
                                         actionTitle: settings, repair: .notificationSettings)
            case .registered, .unreachable:
                break
            }
        }
        if routes[group]?.reason == "no_token" || relay?.isUnreachable == true {
            return PushNoticeContent(text: String(localized: "Notifications can't reach this device. Check your network. Persona will try again."),
                                     actionTitle: settings, repair: .notificationSettings)
        }
        return PushNoticeContent(text: String(localized: "Notifications can't reach this device. Keep Persona open while it tries again."),
                                 actionTitle: settings, repair: .notificationSettings)
    }
}

private extension PushHealth.Relay {
    var isUnreachable: Bool {
        if case .unreachable = self { return true }
        return false
    }
}

extension PushHealth {
    /// The status at the hub of `group`, with notifications `allowed` in Settings now (nil: unknown).
    func status(group: String, allowed: Bool?) -> PushStatus {
        if (allowed ?? notificationsAllowed) == false {
            return PushStatus(level: .unavailable, title: String(localized: "push.unavailable", defaultValue: "Unavailable"),
                              reason: String(localized: "Notifications for Persona are off in Settings."),
                              action: String(localized: "Allow notifications for Persona in Settings."))
        }
        if let why = unchecked?[group] {
            return PushStatus(level: .unknown, title: String(localized: "push.unknown", defaultValue: "Unknown"),
                              reason: String(localized: "Couldn't check with the hub (\(why)).", comment: "%@: the network's or the hub's error"),
                              action: String(localized: "Persona checks again when you open this page."))
        }
        if missingRoutes?.contains(group) == true {
            return PushStatus(level: .unavailable, title: String(localized: "push.unavailable", defaultValue: "Unavailable"),
                              reason: String(localized: "The hub has no push registration for this device."),
                              action: String(localized: "Open Persona again to register once more."))
        }
        guard let route = routes[group] else { return unknownStatus }
        if route.reaches {
            let title = route.route == "direct" ? String(localized: "Available · Direct to APNs") : String(localized: "Available · Through the push relay")
            guard route.reason == "push_failed" else { return PushStatus(level: .available, title: title) }
            return PushStatus(level: .lastFailed, title: title,
                              reason: String(localized: "The last push didn't go through (\(route.detail ?? "")).", comment: "%@: APNs' or the relay's answer"),
                              action: String(localized: "The hub tries again with the next one. If it keeps failing, check the hub's log."))
        }
        switch route.reason {
        case "no_token":
            let reason = apnsError.map { String(localized: "Apple's push service gave this device no token (\($0)).", comment: "%@: the system's error") }
                ?? String(localized: "This device has no push token yet.")
            return PushStatus(level: .unavailable, title: String(localized: "push.unavailable", defaultValue: "Unavailable"), reason: reason,
                              action: String(localized: "Check the network and open Persona again."))
        case "relay_failed", "no_relay":
            return relayStatus(hubDetail: route.detail)
        default:
            return PushStatus(level: .unavailable, title: String(localized: "push.unavailable", defaultValue: "Unavailable"), reason: route.detail, action: nil)
        }
    }

    /// No pushes for a reason on the relay's side, in the phone's words when it has them.
    private func relayStatus(hubDetail: String?) -> PushStatus {
        let title = String(localized: "push.unavailable", defaultValue: "Unavailable")
        switch relay {
        case .refused(let status, let message) where status == 403 && message.contains("topic"):
            return PushStatus(level: .unavailable, title: title,
                              reason: String(localized: "The push relay doesn't serve this app (\(relayError ?? "")).", comment: "%@: the relay's status and words"),
                              action: String(localized: "The push relay hasn't been set up for this app yet; its publisher has to do that. Or give the hub its own APNs key (apns.json)."))
        case .refused:
            return PushStatus(level: .unavailable, title: title,
                              reason: String(localized: "The push relay refused to register this device (\(relayError ?? "")).", comment: "%@: the relay's status and words"),
                              action: String(localized: "Persona tries again the next time it opens. If it keeps failing, give the hub its own APNs key (apns.json)."))
        case .moved(_, let location):
            return PushStatus(level: .unavailable, title: title,
                              reason: String(localized: "The push relay has moved (to \(location)).", comment: "%@: the new address"),
                              action: String(localized: "Install the latest Persona from TestFlight: this build has the relay's old address."))
        case .unreachable(let error):
            return PushStatus(level: .unavailable, title: title,
                              reason: String(localized: "Couldn't reach the push relay (\(error)).", comment: "%@: the network's error"),
                              action: String(localized: "Check the network. Persona tries again the next time it opens."))
        case .registered:
            return PushStatus(level: .unavailable, title: title,
                              reason: String(localized: "The hub can't use this device's relay registration (\(hubDetail ?? "")).", comment: "%@: what the hub says"),
                              action: String(localized: "Open Persona again to register once more."))
        case .notConfigured, nil:
            return PushStatus(level: .unavailable, title: title,
                              reason: String(localized: "This build has no push relay, and the hub has no APNs key of its own."),
                              action: String(localized: "Give the hub its own APNs key (apns.json), or install Persona from TestFlight."))
        }
    }

    /// The hub did not say (an older one, or not registered there yet): what this device knows.
    private var unknownStatus: PushStatus {
        let title = String(localized: "push.unknown", defaultValue: "Unknown")
        let action = String(localized: "Update the hub to see which way its pushes take.")
        if let apnsError {
            return PushStatus(level: .unknown, title: title,
                              reason: String(localized: "Apple's push service gave this device no token (\(apnsError)).", comment: "%@: the system's error"),
                              action: String(localized: "Check the network and open Persona again."))
        }
        switch relay {
        case .registered:
            return PushStatus(level: .unknown, title: title, reason: String(localized: "Registered with the push relay; the hub hasn't said which way it pushes."), action: action)
        case .refused, .moved, .unreachable:
            var s = relayStatus(hubDetail: nil)
            s.level = .unknown
            s.title = title
            return s
        case .notConfigured, nil:
            return PushStatus(level: .unknown, title: title, reason: String(localized: "The hub hasn't said which way it pushes to this device."), action: action)
        }
    }
}

extension PushHealth {
    /// The current hub's token list answered for group: the way it pushes to this device (nil: none it knows of),
    /// nothing left unchecked. Unlike a registration response, a missing route here is a confirmed outage.
    mutating func read(_ route: APIClient.PushRoute?, group: String) {
        apply(route, group: group, confirmedMissing: route == nil)
    }

    /// A registration response carries no route on an older hub; that says nothing about a confirmed outage (#9612).
    mutating func readRegistration(_ route: APIClient.PushRoute?, group: String) {
        apply(route, group: group, confirmedMissing: false)
    }

    private mutating func apply(_ route: APIClient.PushRoute?, group: String, confirmedMissing: Bool) {
        routes[group] = route
        if confirmedMissing {
            missingRoutes = (missingRoutes ?? []).union([group])
        } else if route != nil {
            missingRoutes?.remove(group)
            if missingRoutes?.isEmpty == true { missingRoutes = nil }
        }
        unchecked?[group] = nil
        if unchecked?.isEmpty == true { unchecked = nil }
    }

    /// The route at group could not be read: why, in the error's words.
    mutating func readFailed(_ why: String, group: String) {
        var all = unchecked ?? [:]
        all[group] = why
        unchecked = all
    }
}

#if DEBUG
extension PushHealth {
    /// `-showPushHealth <name>`: what Settings › Notifications shows in each case, for screenshots.
    static func sample(_ name: String, group: String) -> PushHealth {
        var h = PushHealth(notificationsAllowed: true)
        let none = { (reason: String, detail: String?) in APIClient.PushRoute(route: "none", reason: reason, detail: detail) }
        switch name {
        case "moved":
            h.relay = .moved(status: 301, location: "https://relay.example.com/relay/v1/register")
            h.routes[group] = none("relay_failed", h.relayError)
        case "unreachable":
            h.relay = .unreachable("The request timed out.")
            h.routes[group] = none("relay_failed", h.relayError)
        case "relay":
            h.relay = .registered
            h.routes[group] = APIClient.PushRoute(route: "relay")
        case "direct":
            h.relay = .registered
            h.routes[group] = APIClient.PushRoute(route: "direct")
        case "failed":
            h.relay = .registered
            h.routes[group] = APIClient.PushRoute(route: "relay", reason: "push_failed", detail: "502 relay: APNs unreachable", failedAt: .now)
        case "unknown":
            h.relay = .registered
        case "unchecked":
            h.relay = .registered
            h.routes[group] = APIClient.PushRoute(route: "relay")
            h.unchecked = [group: "The request timed out."]
        default:
            h.relay = .refused(status: 403, message: "topic not served")
            h.routes[group] = none("relay_failed", h.relayError)
        }
        return h
    }
}
#endif

/// The device's PushHealth, kept on disk (StateCache: it changes at most a few times a launch; only the small push-health
/// readers on Settings › Notifications and Persona's main screen observe it).
@MainActor @Observable
final class PushHealthStore {
    static let shared = PushHealthStore()
    private static let cacheName = "push-health"

    private(set) var health: PushHealth

    init(health: PushHealth? = nil) {
        self.health = health ?? StateCache.load(Self.cacheName) ?? PushHealth()
    }

    func update(_ change: (inout PushHealth) -> Void) {
        var h = health
        change(&h)
        guard h != health else { return }
        health = h
        StateCache.save(Self.cacheName, h)
    }
}
