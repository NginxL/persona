import Foundation
import UserNotifications

/// Taking back the pushes about interactions that are settled (#3513): an approval or a question answered in the app,
/// on the computer, on the web or the watch, withdrawn by the agent or expired leaves the notification center with
/// it. The pushes carry `interaction_id` (push.go) and the session they are about. The app keeps the notification
/// center in step with its list of pending interactions; while it is suspended the hub's withdrawal push names the
/// cards that are over (#4349, `resolved`). Pure, for the tests; AppModel reads and removes the delivered notifications.
nonisolated enum NotificationWithdrawal {
    /// A delivered notification about an interaction: the interaction's group (the push's, resolved the way a tap
    /// resolves it) and the hub's id, and when it was delivered.
    struct Delivered: Sendable, Equatable {
        var identifier: String
        var group: String
        var interactionID: String
        var date: Date
        /// A push about one of the assistant's own cards (a plan waiting for Start, a decision, a failed start: the
        /// push's `card_id`), never in the list of pending interactions. Only the hub's withdrawal push takes it back:
        /// the list saying nothing of it does not make it stale.
        var ownerCard = false

        /// As `Interaction.uid`: unique across groups, just the id for the default group.
        var uid: String { group.isEmpty ? interactionID : group + "|" + interactionID }
    }

    /// The notifications about interactions that left the list (`gone`, by uid), unless one is pending again by now
    /// (an answer that did not go through puts its card back).
    static func settled(gone: Set<String>, pending: Set<String>, delivered: [Delivered]) -> [String] {
        let over = gone.subtracting(pending)
        return delivered.filter { !$0.ownerCard && over.contains($0.uid) }.map(\.identifier)
    }

    /// The notifications a group's whole list says are over: delivered before that list was asked for (a later push
    /// can be ahead of its own event) and not pending now. Groups without such a list (`listedAt`) are left alone:
    /// the cached list of the last run says nothing about what happened since.
    static func stale(listedAt: [String: Date], pending: Set<String>, delivered: [Delivered]) -> [String] {
        delivered.filter { d in
            guard !d.ownerCard, let at = listedAt[d.group] else { return false }
            return d.date < at && !pending.contains(d.uid)
        }.map(\.identifier)
    }

    /// The notifications of the cards a hub's withdrawal push names (#4349), in that hub's group (nil: the push did
    /// not say whose, so the ids in any group). Pending or not: the hub knows they are over, and a suspended app's list
    /// is the one from before.
    static func resolved(_ ids: [String], group: String?, delivered: [Delivered]) -> [String] {
        let over = Set(ids)
        return delivered.filter { over.contains($0.interactionID) && (group == nil || $0.group == group) }.map(\.identifier)
    }

    /// What the notification center gave, read where it calls back (off the main thread): the pushes about an
    /// interaction, with the session they name (`PushRef`, whose group the app resolves on the main actor).
    struct Raw: Sendable {
        var identifier: String
        var interactionID: String
        var date: Date
        var ref: PushRef
        var ownerCard = false
    }

    static func raw(_ notifications: [UNNotification]) -> [Raw] {
        notifications.compactMap { n in
            let info = n.request.content.userInfo
            // An assistant card's push names its card as card_id (a plan's, from before, as interaction_id only).
            let card = info["card_id"] as? String ?? ""
            let id = card.isEmpty ? info["interaction_id"] as? String ?? "" : card
            guard !id.isEmpty, let ref = PushRef(info) else { return nil }
            return Raw(identifier: n.request.identifier, interactionID: id, date: n.date, ref: ref, ownerCard: !card.isEmpty)
        }
    }
}

extension AppModel {
    /// The list lost these interactions (`uid`s): an `interaction_resolved`, an answer in the app, a group's whole
    /// list replacing it, a group forgotten. Their pushes go too.
    func withdrawSettled(_ gone: Set<String>) {
        guard !gone.isEmpty else { return }
        withdrawDelivered { model, delivered in
            NotificationWithdrawal.settled(gone: gone, pending: Set(model.interactions.keys), delivered: delivered)
        }
    }

    /// A group's whole list came (reconnected, back in front): the pushes delivered before it that it does not have.
    func withdrawStaleNotifications() {
        guard !interactionsListedAt.isEmpty else { return }
        withdrawDelivered { model, delivered in
            NotificationWithdrawal.stale(listedAt: model.interactionsListedAt, pending: Set(model.interactions.keys),
                                         delivered: delivered)
        }
    }

    /// A hub's withdrawal push (#4349): the cards it names are over, answered elsewhere while the app was suspended.
    /// `done` runs on the main queue once their notifications are gone.
    func withdrawResolved(_ ids: [String], group: String?, done: @escaping @Sendable () -> Void) {
        withdrawDelivered({ _, delivered in NotificationWithdrawal.resolved(ids, group: group, delivered: delivered) }, done: done)
    }

    /// Reads what the notification center shows and removes what `pick` names. The center answers on its own queue;
    /// the groups are resolved, and the list read, back on the main actor.
    private func withdrawDelivered(_ pick: @escaping @Sendable @MainActor (AppModel, [NotificationWithdrawal.Delivered]) -> [String],
                                   done: (@Sendable () -> Void)? = nil) {
        UNUserNotificationCenter.current().getDeliveredNotifications { @Sendable list in
            let raw = NotificationWithdrawal.raw(list)
            guard !raw.isEmpty else {
                if let done { DispatchQueue.main.async(execute: done) }
                return
            }
            DispatchQueue.main.async {
                MainActor.assumeIsolated {
                    let model = self
                    let delivered = raw.map {
                        NotificationWithdrawal.Delivered(identifier: $0.identifier, group: $0.ref.key(model).group,
                                                         interactionID: $0.interactionID, date: $0.date, ownerCard: $0.ownerCard)
                    }
                    let ids = pick(model, delivered)
                    if !ids.isEmpty { UNUserNotificationCenter.current().removeDeliveredNotifications(withIdentifiers: ids) }
                    done?()
                }
            }
        }
    }
}
