import Foundation
import SwiftUI
import UserNotifications

/// A pairing receipt push (docs/protocol/protocol.md「Computer pairing codes」, #4199): `aps.category` is `pair_receipt` or
/// `computer_joining`, and the ids the buttons act on are only in the push's `pair` object, never in its text.
/// A push without both (an older hub) is not one: it stays a plain notification.
nonisolated struct PairPush: Sendable, Equatable {
    enum Kind: String, Sendable {
        /// A new credential paired: `device_id` is the one to revoke.
        case device
        /// A device made a code for a computer: `device_id` made it, `machine` is the computer (still pending).
        case computerCode = "computer_code"
        /// A pending computer traded its code: `machine` is the one to confirm or reject.
        case computerJoining = "computer_joining"
        /// The app's own follow-up (not the hub's): a computer that was confirmed already, its page to open.
        case machine
    }

    var kind: Kind
    var deviceID: String?
    var machine: String?
    /// The hub that sent it (`hub_id`) and the group whose key opened it (`persona_group`): which group's hub to ask.
    var hubID: String?
    var keyGroup: String?

    init(kind: Kind, deviceID: String? = nil, machine: String? = nil, hubID: String? = nil, keyGroup: String? = nil) {
        self.kind = kind
        self.deviceID = deviceID
        self.machine = machine
        self.hubID = hubID
        self.keyGroup = keyGroup
    }

    /// The push's `pair`, when it has one the app knows with the ids its kind needs; nil for anything else.
    init?(_ info: [AnyHashable: Any]) {
        guard let pair = info["pair"] as? [String: Any], let kind = (pair["kind"] as? String).flatMap(Kind.init) else { return nil }
        func id(_ key: String) -> String? { (pair[key] as? String).flatMap { $0.isEmpty ? nil : $0 } }
        self.init(kind: kind, deviceID: id("device_id"), machine: id("machine"),
                  hubID: info["hub_id"] as? String, keyGroup: PushCrypto.group(from: info))
        switch kind {
        case .device: if deviceID == nil { return nil }
        case .computerCode, .computerJoining, .machine: if machine == nil { return nil }
        }
    }

    /// The group whose hub this is about: the hub that sent it, else the group whose key opened it; nil: not known
    /// (the first group is asked, as for the other notification buttons).
    func group(groupOfHub: (String) -> String? = { HubStore.group(hubID: $0) }) -> String? {
        hubID.flatMap(groupOfHub) ?? keyGroup.flatMap { $0.isEmpty ? nil : $0 }
    }

    var userInfo: [String: Any] {
        var pair: [String: Any] = ["kind": kind.rawValue]
        if let deviceID { pair["device_id"] = deviceID }
        if let machine { pair["machine"] = machine }
        var info: [String: Any] = ["pair": pair]
        if let hubID { info["hub_id"] = hubID }
        if let keyGroup { info[PushCrypto.groupKey] = keyGroup }
        return info
    }
}

/// The buttons on pairing receipts and what they do. Every one of them changes who can reach the hub, so each needs
/// the phone unlocked (`.authenticationRequired`); none opens the app.
nonisolated enum PairReceiptActions {
    static let receiptCategory = "pair_receipt"
    static let joiningCategory = "computer_joining"
    /// Not you? Revoke: revokes the new device's credential, or rejects the computer a code was made for.
    static let revoke = "PAIR_REVOKE"
    static let confirm = "PAIR_CONFIRM_COMPUTER"
    static let reject = "PAIR_REJECT_COMPUTER"

    static var categories: [UNNotificationCategory] {
        let receipt = UNNotificationCategory(
            identifier: receiptCategory,
            actions: [
                UNNotificationAction(identifier: revoke, title: String(localized: "Not you? Revoke"), options: [.destructive, .authenticationRequired],
                                     icon: UNNotificationActionIcon(systemImageName: Theme.Icon.close)),
            ],
            intentIdentifiers: [], options: [])
        let joining = UNNotificationCategory(
            identifier: joiningCategory,
            actions: [
                UNNotificationAction(identifier: confirm, title: String(localized: "It's Mine"), options: [.authenticationRequired],
                                     icon: UNNotificationActionIcon(systemImageName: Theme.Icon.done)),
                UNNotificationAction(identifier: reject, title: String(localized: "Not Mine"), options: [.destructive, .authenticationRequired],
                                     icon: UNNotificationActionIcon(systemImageName: Theme.Icon.close)),
            ],
            intentIdentifiers: [], options: [])
        return [receipt, joining]
    }

    /// The request a button makes, by the push's kind; nil when the button is not one of these or does not fit the
    /// kind (an It's Mine on a device's receipt).
    enum Request: Equatable, Sendable {
        case revokeDevice(String)
        case rejectComputer(String)
        case confirmComputer(String)
    }

    static func request(action: String, _ push: PairPush) -> Request? {
        switch (action, push.kind) {
        case (revoke, .device): push.deviceID.map(Request.revokeDevice)
        case (revoke, .computerCode), (reject, .computerJoining): push.machine.map(Request.rejectComputer)
        case (confirm, .computerJoining): push.machine.map(Request.confirmComputer)
        default: nil
        }
    }

    /// How it went, for the local notification that says so (the user is not looking at the app).
    enum Outcome: Equatable, Sendable {
        case revoked, rejected, confirmed
        /// The computer was confirmed already (409 `not_pending`): its page opens instead, to decide there.
        case alreadyConfirmed(MachineRef)
        case failed(title: String, reason: String)
    }

    static func perform(_ request: Request, client: APIClient, host: String? = nil) async -> Outcome {
        do {
            switch request {
            case .revokeDevice(let id):
                try await client.revokeToken(id)
                return .revoked
            case .rejectComputer(let id):
                try await client.rejectMachine(id)
                return .rejected
            case .confirmComputer(let id):
                try await client.confirmMachine(id)
                return .confirmed
            }
        } catch let e as APIError where e.code == "not_pending" {
            if case .rejectComputer(let id) = request { return .alreadyConfirmed(MachineRef(group: client.group, id: id)) }
            return .failed(title: failureTitle(request), reason: reason(e, request, host: host))
        } catch {
            return .failed(title: failureTitle(request), reason: reason(error, request, host: host))
        }
    }

    static func failureTitle(_ request: Request) -> String {
        switch request {
        case .revokeDevice: String(localized: "Couldn't revoke")
        case .rejectComputer: String(localized: "Couldn't deny")
        case .confirmComputer: String(localized: "Couldn't confirm")
        }
    }

    /// Why, in words: a device or computer that is gone, else what any other button says (no network, unpaired…).
    static func reason(_ error: Error, _ request: Request, host: String?) -> String {
        if let e = error as? APIError, e.status == 404 {
            if case .revokeDevice = request { return String(localized: "This device was already revoked or is no longer there.") }
            return String(localized: "This computer is no longer there.")
        }
        return IntentFailure.sentence(for: error, host: host)
    }

    /// The local notification for an outcome; a confirmed-already computer's opens its page when tapped.
    static func content(_ outcome: Outcome, request: Request, push: PairPush) -> UNMutableNotificationContent {
        let c = UNMutableNotificationContent()
        switch outcome {
        case .revoked:
            c.title = String(localized: "Pairing revoked")
            c.body = String(localized: "That device can no longer connect to the hub.")
        case .rejected:
            c.title = String(localized: "Computer rejected")
            c.body = String(localized: "It won't join.")
        case .confirmed:
            c.title = String(localized: "Computer confirmed")
            c.body = String(localized: "It's ready to work.")
        case .alreadyConfirmed(let ref):
            c.title = String(localized: "This computer is already confirmed")
            c.body = String(localized: "Tap to open its details and decide whether to remove it.")
            c.userInfo = PairPush(kind: .machine, machine: ref.id, hubID: push.hubID, keyGroup: push.keyGroup).userInfo
        case .failed(let title, let reason):
            c.title = title
            c.body = reason
        }
        return c
    }

    /// Runs a button in the background and posts how it went.
    static func run(_ request: Request, _ push: PairPush, id: String) async {
        let group = push.group()
        let outcome: Outcome
        do {
            let client = try HubStore.client(group: group)
            outcome = await perform(request, client: client, host: client.config.url.host())
        } catch {
            outcome = .failed(title: failureTitle(request), reason: IntentFailure.sentence(for: error))
        }
        let content = content(outcome, request: request, push: push)
        try? await UNUserNotificationCenter.current().add(UNNotificationRequest(identifier: "pair-\(id)", content: content, trigger: nil))
    }

    /// Where tapping the notification itself goes.
    enum Destination: Equatable, Sendable {
        /// Confirm Computer for this computer (M3's screen).
        case confirmComputer(MachineRef)
        /// Settings › Devices.
        case devices
        /// The computer's own page.
        case machine(MachineRef)
    }

    static func destination(_ push: PairPush, group: String?) -> Destination {
        let ref = { MachineRef(group: group ?? "", id: push.machine ?? "") }
        switch push.kind {
        case .computerJoining: return .confirmComputer(ref())
        case .machine: return .machine(ref())
        case .device, .computerCode: return .devices
        }
    }
}

extension AppModel {
    /// Opens what a tapped pairing receipt is about (`PairReceiptActions.destination`).
    func openPair(_ destination: PairReceiptActions.Destination) {
        switch destination {
        case .confirmComputer(let ref):
            openConfirmComputer(ref)
        case .devices:
            settingsPath = NavigationPath([SettingsPage.devices])
            selectedTab = .settings
        case .machine(let ref):
            var path = NavigationPath()
            path.append(MachineListRoute())
            path.append(MachineDetailRoute(ref))
            settingsPath = path
            selectedTab = .settings
        }
    }
}
