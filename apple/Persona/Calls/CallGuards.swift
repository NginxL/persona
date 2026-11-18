#if os(iOS)
import Foundation
import WebKit

// Nothing in the app ends a live call by itself (docs/apple/calls.md, "Nothing in the app ends a call"): while a call to
// Persona or an urgent call is live, what would take the microphone or set up the audio session under it is refused
// with a note, asks first, or carries on without touching the session. The call manager answers for the app here.

extension PersonaCalls {
    /// A system call is live for the app's guards: a call to Persona from "Calling…" to its end, an urgent call ringing
    /// (one the system showed) or answered. DEBUG samples are pictures, not calls: they guard nothing.
    var guardsLive: Bool { calls.contains(where: \.counts) }

    /// Who a live call is with as the pages name it (from "Calling…"), else nil: the assistant's name for the main
    /// conversation, the conversation's title for another (#9887 one button). What the question elsewhere says ("End the
    /// call with <name>…?"), and CarPlay's that it is on the phone.
    var liveOwnerCallName: String? {
        guard let call = ownerCall, !call.sample, call.phase.isLive else { return nil }
        return call.title
    }

    /// `key` is the conversation the live call talks in: its phone icon shows the call screen instead of starting
    /// anything. While the call is still finding the main conversation ("Calling…", no engine yet, from Recents or Siri),
    /// the selected hub's main conversation is taken for it.
    func isOwnerCallConversation(_ key: SessionKey) -> Bool {
        guard let call = ownerCall, call.phase.isLive else { return false }
        if let owner = call.engine as? OwnerCallEngine { return owner.key == key }
        guard case .owner(let group, let target) = call.kind else { return false }
        if let target { return target == key }
        return key.group == group && AppModel.shared.assistantMain == key
    }

    /// Why disconnecting the hubs of `groups` (nil: every hub) waits, or nil when it may go ahead: a live call (one the
    /// guards count) talks through one of them, and the disconnect would revoke its credential and close its tunnels, so
    /// its voice would end as unreachable with no word. Refused until the call ends (Settings' Disconnect, Devices' Revoke
    /// and Unpair: `AppModel.signingOut`). A call whose group is not known yet counts for every group.
    func disconnectRefusal(groups: Set<String>?) -> String? {
        for call in calls where call.counts {
            if let groups, let group = callGroup(call), !groups.contains(group) { continue }
            return String(localized: "On a call with \(call.title). End the call before disconnecting.")
        }
        return nil
    }

    /// The hub group a live call talks through: a call to Persona's conversation's group (its kind's while it is still
    /// finding it), an urgent call's ring's. Nil when not known yet.
    private func callGroup(_ call: PersonaCall) -> String? {
        switch call.kind {
        case .owner(let group, let key):
            if let owner = call.engine as? OwnerCallEngine { return owner.key.group }
            if let key { return key.group }
            return group.isEmpty ? nil : group
        case .urgent:
            return call.urgentSession?.ring.group
        }
    }

    /// The app's own guards ask this manager (the shared one only: a test's manager guards nothing of the app's).
    func guardTheApp() {
        LiveCallGate.isLive = { [weak self] in self?.guardsLive ?? false }
        LiveCallGate.disconnectRefusal = { [weak self] groups in self?.disconnectRefusal(groups: groups) }
        onLiveChange = { live in
            LiveCallGate.callLiveChanged(live)
            CallMediaGuard.shared.callLive(live)
        }
    }
}

extension PersonaCall {
    /// What the app's guards count as live (`PersonaCalls.guardsLive`): a call to Persona from "Calling…" to its end, an
    /// urgent call ringing (one the system showed) or answered. DEBUG samples are pictures, not calls.
    var counts: Bool {
        guard !sample, phase.isLive else { return false }
        return !kind.isUrgent || !(unshown && phase == .ringing)
    }
}

/// Web pages' media stays still while a system call is live: WebKit sets up its own audio for a page's video or sound,
/// which would interrupt the call's session (the call goes on hold). Every web view the app makes that could play
/// media registers here; while a call is live their media playback is suspended (`setAllMediaPlaybackSuspended`), and
/// it is let go when the last call ends.
@MainActor final class CallMediaGuard {
    static let shared = CallMediaGuard()
    private let views = NSHashTable<WKWebView>.weakObjects()
    private(set) var suspended = false

    /// A web view that can play media: held weakly, suspended at once while a call is live.
    func register(_ web: WKWebView) {
        views.add(web)
        if suspended { web.setAllMediaPlaybackSuspended(true) }
    }

    func callLive(_ live: Bool) {
        guard live != suspended else { return }
        suspended = live
        for web in views.allObjects { web.setAllMediaPlaybackSuspended(live) }
    }
}
#endif
