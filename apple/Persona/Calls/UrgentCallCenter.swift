#if os(iOS)
import Foundation
import os
import UIKit
import UserNotifications

/// Rings the phone for an urgent call (docs/assistant/urgent-call.md): a VoIP push becomes an incoming call reported to
/// LiveCommunicationKit at once (iOS ends an app that does not report one), with a silent time-sensitive companion
/// notification beside it. On some iPhones sold in mainland China the call shows only as a banner or in the Dynamic
/// Island; the notification stays after the call's banner goes, and tapping it answers. Answering opens the call page
/// (`UrgentCallSession`); declining, a ring nobody answers in 45 seconds, and the call settled elsewhere end it, and
/// the manager hears each. The system's side is the app's one call manager's (`PersonaCalls`, #9290), shared with calls
/// to Persona: this keeps what is the ring's (the push, the companion notification, the ring's 45 s, the reports).
@MainActor @Observable final class UrgentCallCenter {
    static let shared = UrgentCallCenter()
    private static let log = Logger(subsystem: "persona", category: "urgent-call")

    /// The call the page shows: answered, or opened from its notification (`UrgentCallPresenter`, which the cover reads).
    private(set) var session: UrgentCallSession? {
        get { UrgentCallPresenter.shared.session }
        set { UrgentCallPresenter.shared.session = newValue }
    }

    /// How long a ring lasts before it counts as missed (the manager rings once more after its retry wait).
    static let ringFor: Duration = .seconds(45)
    /// The companion notification's id for a call.
    nonisolated static func noteID(_ call: String) -> String { "urgent-call-" + call }
    /// The key a companion notification carries, so a tap finds its call.
    nonisolated static let callKey = "urgent_call_id"

    private struct Ringing {
        var ring: UrgentCallRing
        var session: UrgentCallSession
        var answered = false
    }

    @ObservationIgnored private var ringing: [UUID: Ringing] = [:]

    private init() {}

    /// The session of the call the system knows as `uuid`.
    func session(for uuid: UUID) -> UrgentCallSession? { ringing[uuid]?.session }

    // MARK: Ringing

    /// The name the incoming call shows: "Urgent: <title>" while the lock screen may show details, else only that
    /// it is Persona's urgent call (the notifications' privacy rule).
    nonisolated static func callName(title: String?, details: Bool) -> String {
        guard details, let title, !title.isEmpty else { return String(localized: "Persona Urgent Call") }
        return String(localized: "Urgent: \(title)")
    }

    /// A VoIP push came: report it as a call now, whatever it holds (iOS requires it), then `done`. One that does not
    /// open or is too old is reported and ended at once.
    func incoming(_ payload: [AnyHashable: Any], done: @escaping () -> Void) {
        let uuid = UUID()
        let ring = PushCrypto.notificationInfo(payload).flatMap { UrgentCallRing(info: $0) }
        // "Show Details on Lock Screen" (Settings › Notifications), as the app keeps it for the Live Activity: off (or not
        // read yet), the call and its notification say only that Persona is calling, not what about.
        let details = UserDefaults.standard.bool(forKey: LiveActivityController.detailsKey)
        let name = Self.callName(title: ring?.title, details: details)
        if let ring {
            let session = UrgentCallSession(ring: ring) { [weak self] ended in self?.sessionEnded(ended) }
            ringing[uuid] = Ringing(ring: ring, session: session)
            Task { await session.prepare() }
        }
        let calls = PersonaCalls.shared
        Task {
            do {
                // Its audio category first, where nothing else set it (`PersonaCalls.ring`).
                try await calls.ring(uuid, callID: ring?.id ?? "", name: name)
            } catch {
                // Focus or Do Not Disturb can refuse the call: the companion notification is what the owner sees then, and
                // the call refuses nothing else until it is answered from there.
                Self.log.notice("urgent call: the call was not shown: \(error.localizedDescription, privacy: .public)")
            }
            done()
            guard let ring else {
                calls.settle(uuid, .failed)
                return
            }
            await companion(ring, missed: false, details: details)
            try? await Task.sleep(for: Self.ringFor)
            if let r = ringing[uuid], !r.answered { missed(uuid) }
        }
    }

    /// The owner answered (on the call, or by tapping its notification while it rang): `PersonaCalls` on Join.
    func answered(_ uuid: UUID) {
        guard var r = ringing[uuid], !r.answered else { return }
        r.answered = true
        ringing[uuid] = r
        session = r.session
        removeCompanion(r.ring.id)
        report(r.ring, .answered)
    }

    /// The call ended on the phone: declined while it rang, or hung up after it was answered (`PersonaCalls` on End).
    func endedOnPhone(_ uuid: UUID) {
        guard let r = ringing.removeValue(forKey: uuid) else { return }
        removeCompanion(r.ring.id)
        if r.answered {
            r.session.hangUp()
        } else {
            report(r.ring, .declined)
        }
    }

    /// Nobody answered in time: the call ends, the notification says it was missed, and the manager rings again later
    /// (or pushes the matter).
    private func missed(_ uuid: UUID) {
        guard let r = ringing.removeValue(forKey: uuid) else { return }
        PersonaCalls.shared.settle(uuid, .unanswered)
        report(r.ring, .missed)
        let details = UserDefaults.standard.bool(forKey: LiveActivityController.detailsKey)
        Task { await companion(r.ring, missed: true, details: details) }
    }

    /// The manager says where a call is (`urgent_call`): a phone still ringing for one answered, declined or settled
    /// elsewhere stops.
    func changed(id: String, state: String) {
        guard state != "ringing", let (uuid, r) = ringing.first(where: { $0.value.ring.id == id }), !r.answered else { return }
        ringing[uuid] = nil
        PersonaCalls.shared.settle(uuid, state == "answered" ? .answeredElsewhere : state == "declined" ? .declinedElsewhere : .remoteEnded)
        removeCompanion(id)
    }

    /// The companion notification was tapped: answer the call while it rings, else open its conversation.
    func opened(callID: String, group: String) {
        if let (uuid, r) = ringing.first(where: { $0.value.ring.id == callID }), !r.answered {
            Task {
                let calls = PersonaCalls.shared
                if await calls.answer(uuid) { return }
                // No call to join (the system refused it): the page opens and the call talks all the same, a call to
                // Persona on hold meanwhile. One the system would not hold stays on, and the conversation opens instead.
                if await calls.answeredHere(uuid) { return }
                self.openConversation(callID, group: group)
            }
            return
        }
        openConversation(callID, group: group)
    }

    /// The urgent call's conversation, its matter in writing.
    private func openConversation(_ callID: String, group: String) {
        removeCompanion(callID)
        guard let api = AppModel.shared.api(group) else { return }
        Task {
            guard let info = try? await api.urgentCall(callID), let machine = info.machineID, let conversation = info.conversationID else { return }
            try? await api.reportUrgentCall(callID, .seen)
            AppModel.shared.open(SessionKey(group: group, machineID: machine, sessionID: conversation))
        }
    }

    #if DEBUG
    /// `-showUrgentCallSample YES`: the call page for the mock hub's call `call_mock`, without ringing or audio.
    /// `held`: the system call behind it on hold after an interruption its audio did not come back from, so the page offers
    /// Resume beside Mute and Speaker (`PersonaCalls.showUrgentSample`, a sample too: nothing reaches the system).
    /// `title` stands in for the call's title (`-showUrgentCallSampleTitle`: a long one, for the screenshots).
    func showSample(held: Bool = false, title: String? = nil) {
        guard let group = AppModel.shared.hub?.group else { return }
        let ring = UrgentCallRing(id: "call_mock", title: title ?? "", manager: "", conversationID: "", attempt: 1, group: group)
        let s = UrgentCallSession(ring: ring) { [weak self] _ in self?.session = nil }
        session = s
        if held {
            let uuid = UUID()
            ringing[uuid] = Ringing(ring: ring, session: s, answered: true)
            PersonaCalls.shared.showUrgentSample(uuid, callID: ring.id)
        }
        Task { await s.sample(title: title) }
    }
    #endif

    // MARK: Ending

    /// The page's Hang Up: ends the call with the system, which ends the session.
    func hangUp() {
        guard let session else { return }
        if let (uuid, _) = ringing.first(where: { $0.value.session === session }) {
            PersonaCalls.shared.hangUp(uuid)
        } else {
            session.hangUp()
        }
    }

    private func sessionEnded(_ ended: UrgentCallSession) {
        if let (uuid, _) = ringing.first(where: { $0.value.session === ended }) {
            ringing[uuid] = nil
            PersonaCalls.shared.urgentEnded(uuid)
        }
        if session === ended { session = nil }
    }

    // MARK: Reports and the companion notification

    private func report(_ ring: UrgentCallRing, _ state: UrgentCallReport) {
        guard let api = AppModel.shared.api(ring.group)?.manager(ring.manager.isEmpty ? nil : ring.manager) else { return }
        Task {
            for attempt in 0..<3 {
                do {
                    try await api.reportUrgentCall(ring.id, state)
                    return
                } catch {
                    Self.log.notice("urgent call \(ring.id, privacy: .public): report \(state.rawValue, privacy: .public): \(error.localizedDescription, privacy: .public)")
                    try? await Task.sleep(for: .seconds(1 << attempt))
                }
            }
        }
    }

    /// A silent time-sensitive notification beside the call, which stays when the call's banner goes: "Persona is calling
    /// you", or after the ring "Missed urgent call".
    private func companion(_ ring: UrgentCallRing, missed: Bool, details: Bool) async {
        let content = UNMutableNotificationContent()
        content.title = missed ? String(localized: "Missed urgent call") : String(localized: "Persona is calling you")
        content.body = details ? ring.title : ""
        content.interruptionLevel = .timeSensitive
        content.sound = nil // the call rings; the notification only stays
        content.threadIdentifier = "urgent-call"
        content.userInfo = [Self.callKey: ring.id, PushCrypto.groupKey: ring.group]
        let request = UNNotificationRequest(identifier: Self.noteID(ring.id), content: content, trigger: nil)
        do {
            try await UNUserNotificationCenter.current().add(request)
        } catch {
            Self.log.notice("urgent call: companion notification: \(error.localizedDescription, privacy: .public)")
        }
    }

    private func removeCompanion(_ call: String) {
        let id = Self.noteID(call)
        UNUserNotificationCenter.current().removeDeliveredNotifications(withIdentifiers: [id])
        UNUserNotificationCenter.current().removePendingNotificationRequests(withIdentifiers: [id])
    }

}

#endif
