import Foundation
import Observation
import UIKit

/// The app's voice call: at most one at a time, whichever page or surface started it (docs/assistant/voice.md 6.3). ChatView only
/// asks `isBound(to:)`; the bar and the thread's tail read `current` themselves.
///
/// Rules (docs/assistant/voice.md 6.5): a new call ends the one before. On iOS the phone's voice chat (the fallback where
/// a system call can't be used: the iPad, a refused Start; #9887 one button) goes on when the app goes to the background
/// or the phone locks, and leaving its page leaves it with the bar back to it at the top (`away`), as a system call to
/// Persona does (#9290, `PersonaCalls` runs that one); in the background only its audio and socket work (the voice's
/// levels stop). On Apple Vision Pro the background and leaving the page end it, as before. Starting a recording or
/// dictation, three failed reconnects or ten idle minutes end it from inside (`VoiceConversation`); an interruption (a
/// phone call) pauses it, and it goes on when the system says it may (a system call holds through it instead).
@MainActor @Observable final class VoiceCalls {
    static let shared = VoiceCalls(watchesApp: true)

    /// The call, live or ended. An ended one stays (「语音已结束 · 继续」) until its page goes, unless the user ended it.
    private(set) var current: VoiceConversation?
    /// The phone's voice chat is on and its page is not (#9887): the bar back to it shows at the top (`CallWindow`).
    private(set) var away = false
    /// The bar back to the voice chat was tapped: its conversation, until its page shows again (`returned`). That page
    /// closes what covers it (a full-screen picture, the terminal: `ChatView`); the bar stays until it shows, so a cover
    /// that stayed up would still have the bar over it.
    private(set) var returning: SessionKey?

    /// The phone's voice chat goes on away from its page and in the background (iOS; on Apple Vision Pro it ends there,
    /// with no bar to come back by).
    nonisolated static var keepsChatAway: Bool {
        #if os(iOS)
        true
        #else
        false
        #endif
    }

    /// The app is in the background: a call's levels (the orb's, 20 a second) stop until it is back.
    @ObservationIgnored private(set) var inBackground = false

    @ObservationIgnored private var observers: [NSObjectProtocol] = []
    /// Persona's urgent call rings or is on (#9290): no voice chat starts or is picked up again under it, from any
    /// page, CarPlay or Continue (its audio session is the system's). `PersonaCalls` answers it once it exists; before
    /// that there is no urgent call.
    @ObservationIgnored var urgentCallOn: @MainActor () -> Bool = { false }
    /// A system call to Persona that is on ends before a voice chat from any other surface opens the app's own audio, and
    /// that waits until the system let the call's audio go: its late deactivation would stop the new audio (#9290,
    /// `PersonaCalls.endForVoiceChat`). Here, for every entry (a page's Voice Chat, CarPlay, Continue), so none skips it.
    @ObservationIgnored var endSystemCall: @MainActor () async -> Void = {}
    /// The assistant's name while a system call to Persona is on, else nil: nothing ends that call by itself (#9290
    /// follow-up), so a voice chat from another surface is refused unless the user said to end the call
    /// (`start(endingCall:)`, after the page's confirmation). `PersonaCalls` answers once it exists.
    @ObservationIgnored var systemCallName: @MainActor () -> String? = { nil }

    /// `watchesApp`: end the phone's call when the app goes to the background (the shared one; the tests drive
    /// `appDidEnterBackground` themselves).
    /// The app's own (`shared`): it watches the app's lifecycle, and a phone's voice chat that ends lets its group's
    /// background tunnels go (`AppModel.callEnded`).
    @ObservationIgnored private let watchesApp: Bool

    init(watchesApp: Bool = false) {
        self.watchesApp = watchesApp
        guard watchesApp else { return }
        observers.append(NotificationCenter.default.addObserver(forName: UIApplication.didEnterBackgroundNotification, object: nil,
                                                                queue: .main) { @Sendable [weak self] _ in
            MainActor.assumeIsolated { self?.appDidEnterBackground() }
        })
        observers.append(NotificationCenter.default.addObserver(forName: UIApplication.willEnterForegroundNotification, object: nil,
                                                                queue: .main) { @Sendable [weak self] _ in
            MainActor.assumeIsolated { self?.appWillEnterForeground() }
        })
    }

    /// Starts a call bound to `key`. The same conversation's call from the same surface is left as it is (or picked up
    /// again when it ended); another one is ended first, a call from another surface too (a voice chat on the main
    /// conversation while a call to Persona is on it is a call of its own). Returns once the call sent its `start`, or ended;
    /// or at once with why not, touching nothing (no call ends, no audio opens), while Persona's urgent call is on, or
    /// while a system call to Persona is on and `endingCall` is not set (the page asked the user first and they said end
    /// it). With `endingCall`, that call ends first and the new one waits for its audio to go (`endSystemCall`).
    @discardableResult
    func start(_ key: SessionKey, extensionID: String, surface: VoiceSurface, recent: [VoiceLine] = [], deps: VoiceDeps,
               endingCall: Bool = false) async -> VoiceUnavailableNote? {
        if let refused = refusal(surface) ?? callRefusal(surface, endingCall: endingCall) { return refused }
        if let refused = await handOff(surface) { return refused }
        if let call = current {
            // A system call that ended has no system call behind it any more: it is never picked up again.
            if call.key == key, call.extensionID == extensionID, call.surface == surface, surface != .call {
                if call.phase.isEnded { await call.restart() }
                return nil
            }
            call.end(.replaced)
            current = nil
        }
        let call = VoiceConversation(key: key, extensionID: extensionID, surface: surface, recent: recent, deps: deps)
        current = call
        away = false
        call.appInBackground(inBackground)
        // The phone's voice chat goes on in the background, through its group's tunnels (`AppModel.callTunnelGroup`):
        // once it ends there, they close.
        if watchesApp, surface == .phone { call.onEnded = { [group = key.group] _ in AppModel.shared.callEnded(group: group) } }
        await call.begin()
        return nil
    }

    /// A call made elsewhere becomes the app's call and begins (a system call: `PersonaCalls` holds it for the system's
    /// audio before it begins). The one before ends. One that ended already (its system call was over before it began)
    /// never begins.
    func begin(_ call: VoiceConversation) async {
        guard !call.phase.isEnded else { return }
        if let before = current, before !== call {
            before.end(.replaced)
        }
        current = call
        away = false
        call.appInBackground(inBackground)
        await call.begin()
    }

    /// Why a voice chat from `surface` may not start now: Persona's urgent call is on (a system call to Persona is
    /// placed by `PersonaCalls`, which refuses it itself).
    private func refusal(_ surface: VoiceSurface) -> VoiceUnavailableNote? {
        surface != .call && urgentCallOn() ? .urgentCall : nil
    }

    /// Why a voice chat from `surface` may not start now: a system call to Persona is on and nobody said to end it.
    private func callRefusal(_ surface: VoiceSurface, endingCall: Bool) -> VoiceUnavailableNote? {
        guard surface != .call, !endingCall, let name = systemCallName() else { return nil }
        return .systemCallOn(name)
    }

    /// A system call that ended goes away at once: no Continue (there is no system call to pick up again).
    func release(_ call: VoiceConversation) {
        if current === call { clear() }
    }

    private func clear() {
        current = nil
        if away { away = false }
        if returning != nil { returning = nil }
    }

    /// Continue on the ended call bound to `key` (not a system call's). Not while Persona's urgent call is on, nor while
    /// a system call to Persona is on unless `endingCall` (the user said end it): why not, and the call stays ended.
    @discardableResult
    func resume(_ key: SessionKey, endingCall: Bool = false) async -> VoiceUnavailableNote? {
        guard let call = current, call.key == key, call.phase.isEnded, call.surface != .call else { return nil }
        if let refused = refusal(call.surface) ?? callRefusal(call.surface, endingCall: endingCall) { return refused }
        if let refused = await handOff(call.surface) { return refused }
        // Something else became the call while the system call's audio went.
        guard current === call, call.phase.isEnded else { return nil }
        await call.restart()
        return nil
    }

    /// A voice chat's audio is the app's: a system call to Persona that is on ends first and lets its audio go
    /// (`endSystemCall`). The wait can see an urgent call ring: refused then, touching nothing more.
    private func handOff(_ surface: VoiceSurface) async -> VoiceUnavailableNote? {
        guard surface != .call else { return nil }
        await endSystemCall()
        return refusal(surface)
    }

    /// Ends the call started from `surface`; a call another surface started goes on (CarPlay only stops its own).
    /// Stop (`.user`) ends whichever it is: the user tapped it on the call itself.
    func end(_ reason: VoiceEnd, from surface: VoiceSurface) {
        guard let call = current, reason == .user || call.surface == surface else { return }
        call.end(reason)
        if reason.byUser { clear() }
        // Ended by itself while its page was away: the bar goes (its Continue waits on the page).
        if away, call.phase.isEnded { away = false }
    }

    /// Whether the call (live or ended) is bound to this conversation: ChatView reads only this.
    func isBound(to key: SessionKey) -> Bool { current?.key == key }

    /// The page bound to `key` went away or was hidden. A system call goes on (it is `PersonaCalls`'), but once it is
    /// over its line about how it ended goes with the page too. The phone's voice chat goes on with the bar back to it
    /// at the top (iOS, #9887), or ends (Apple Vision Pro); an ended one's Continue row goes with the page.
    func leave(_ key: SessionKey, from surface: VoiceSurface = .phone) {
        guard let call = current, call.key == key else { return }
        if call.surface == .call {
            if surface == .phone, call.phase.isEnded { clear() }
            return
        }
        guard call.surface == surface else { return }
        if surface == .phone, Self.keepsChatAway, !call.phase.isEnded {
            away = true
            return
        }
        call.end(.left)
        clear()
    }

    /// The bar back to the voice chat bound to `key` was tapped (#9887): its page is asked to close what covers it
    /// (`returning`). The bar goes only once that page shows again (`returned`), not at the tap: the conversation may be
    /// the one already open under a full-screen picture, which opening it again does not close.
    func returnTo(_ key: SessionKey) {
        guard away, current?.key == key else { return }
        returning = key
    }

    /// The page bound to `key` shows again: the bar back to its voice chat goes.
    func returned(_ key: SessionKey) {
        if returning == key { returning = nil }
        guard away, current?.key == key else { return }
        away = false
    }

    /// The phone's voice chat whose page is away, live: what the bar back to it shows (`CallWindow`).
    var awayChat: VoiceConversation? {
        guard away, let call = current, call.surface == .phone, !call.phase.isEnded else { return nil }
        return call
    }

    /// The app went to the background. On iOS the phone's voice chat goes on (#9887 one button: switching away never
    /// stops it), with its audio session, its socket and the idle rule; the voice's levels, which only the orb reads,
    /// stop until the app is back, so nothing but the audio works there (iOS's background CPU limit). The idle rule
    /// takes whether its conversation works from the voice connection there, not from the app's sessions, which are not
    /// updated in the background (`VoiceConversation.appInBackground`). On Apple Vision Pro it ends, as before; the
    /// car's goes on.
    func appDidEnterBackground() {
        inBackground = true
        current?.appInBackground(true)
        if !Self.keepsChatAway { end(.background, from: .phone) }
    }

    /// The app is coming back to the front: the levels again, and the app's sessions for the idle rule.
    func appWillEnterForeground() {
        inBackground = false
        current?.appInBackground(false)
    }
}
