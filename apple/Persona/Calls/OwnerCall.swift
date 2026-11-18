#if os(iOS)
import AVFoundation
import Foundation
import Intents
import UIKit

// Placing a call (#9290; any conversation since #9887 one button): the call page in "Calling…" at once, then what can
// refuse it (an urgent call, a recording, no hub, no voice extension, the microphone), the conversation (the phone icon's,
// or the main conversation as CarPlay finds it but on the selected hub) and its voice extension, the system's Start, and
// the voice once the system started the call. A redial from the Phone app's Recents comes here as an `INStartCallIntent`
// (or `INStartAudioCallIntent`) user activity, and always calls the main conversation.

extension PersonaCalls {
    /// Calls Persona on the main conversation of `group`'s hub (nil: the selected hub): Recents' redial, Siri and
    /// Shortcuts, CarPlay's Talk to Persona without a car, the DEBUG switch. A call already on is shown instead.
    func callPersona(group: String? = nil, origin: CallOrigin = .app) async {
        await call(nil, group: group, origin: origin)
    }

    /// Calls the conversation `key` (#9887 one button: the composer's phone icon on the iPhone): the main conversation,
    /// another assistant conversation or an Agent session; its voice starts bound to it, as a voice chat there would. A
    /// call already on with it is shown instead; one with another conversation too, unless `endingCall` (the owner said
    /// End and Call to the question): it ends first, and its audio goes, before this one is placed.
    func callConversation(_ key: SessionKey, endingCall: Bool = false) async {
        await call(key, group: key.group, origin: .app, endingCall: endingCall)
    }

    /// Places a call to `target` (nil: the main conversation of `group`'s hub, found here). Refused with the page's note
    /// (and the page goes); when the system will not place the call, a voice chat in the app on the same conversation
    /// instead, with a line that says why.
    private func call(_ target: SessionKey?, group: String?, origin: CallOrigin, endingCall: Bool = false) async {
        let model = AppModel.shared
        // Whatever page or sheet the app was left on stays: the call screen comes over it in its own window
        // (`CallWindow`), and Minimize goes back to it.
        if let live = ownerCall, live.phase.isLive {
            guard endingCall, let target, !isOwnerCallConversation(target) else {
                presented = true
                return
            }
            Self.log.notice("call \(live.id.uuidString, privacy: .public): ends for a call to another conversation")
            await endForVoiceChat()
            if let still = ownerCall, still.phase.isLive {
                presented = true
                return
            }
        }
        // Feedback first, before anything slow.
        let call = add(kind: .owner(group: target?.group ?? group ?? model.hub?.group ?? "", key: target), direction: .outgoing)
        if let target { call.conversation = CallConversation.make(target, model: model) }
        presented = true
        Self.log.notice("call \(call.id.uuidString, privacy: .public): calling \(target == nil ? "Persona" : call.conversation == nil ? "the main conversation" : "another conversation", privacy: .public) (\(origin.rawValue, privacy: .public))")
        // From Recents or Siri the app may have just launched: its hubs come first.
        if origin != .app { await Self.waitForHub(group) }
        guard call.phase.isLive else { return }
        if let note = admission() { return refuse(call, note: note) }
        guard let group = target?.group ?? group ?? model.hub?.group, let api = model.api(group) else { return refuse(call, note: Self.noHubNote) }

        // Who the call is with is decided with its conversation (#9887 review): the look, the name the main conversation's
        // top bar says and whose main conversation it is (the hub and the assistant picked there), read before the main
        // conversation is asked for. The call is bound to that assistant: another one picked while it waits for the hub,
        // the extension or the microphone renames nothing (`place` takes the name again only for this one).
        let who = (look: assistant(), name: assistantName(), owner: assistantOwner())
        let key: SessionKey
        if let target {
            key = target
        } else if let main = model.assistantMain, main.group == group {
            key = main
        } else {
            do {
                guard let confirmed = try await api.assistant().mainKey(group: api.group) else { return refuse(call, note: Self.noMainNote) }
                key = confirmed
            } catch {
                Self.log.notice("call \(call.id.uuidString, privacy: .public): the main conversation: \(error.localizedDescription, privacy: .public)")
                return refuse(call, note: Self.unreachableNote)
            }
        }
        call.assistant.retake(who.look, name: who.name, owner: who.owner)
        // The same checks as the voice chat's, so the reasons and Open Settings match (`VoiceEntry`): read-only, a
        // meeting's room, no voice extension or one not set up. It waits for the group's probe even when the chat's page
        // asked first (a cold launch from Recents).
        await VoiceSupport.shared.refresh(group: group, api: api)
        guard call.phase.isLive else { return }
        let availability = model.session(key).map { VoiceSupport.shared.availability(for: $0, group: group) }
            ?? VoiceSupport.shared.extensionAvailability(group: group)
        guard case .available(let extensionID) = availability else {
            return refuse(call, note: VoiceUnavailableNote.make(availability, extensionID: VoiceSupport.shared.voiceExtensionID(group: group))
                          ?? Self.unreachableNote)
        }
        // The microphone asked for here, in the app: a locked phone mid-call cannot show the prompt.
        switch AVAudioApplication.shared.recordPermission {
        case .granted: break
        case .undetermined:
            guard await AVAudioApplication.requestRecordPermission() else { return refuse(call, note: Self.microphoneNote) }
        default:
            return refuse(call, note: Self.microphoneNote)
        }
        guard call.phase.isLive else { return }
        // An urgent call or a recording may have come while the hub, the extension or the microphone prompt were asked.
        if let note = admission() { return refuse(call, note: note) }

        // An in-app voice chat ends first: its audio would end the call's.
        endVoiceChat(for: .replaced)
        // The call's category before Start (the system activates the session; the app never does).
        await prepareAudio(call)
        let recent = VoiceLine.recent(model.chat(key).items)
        let ownerDeps = ownerDeps
        let engine = OwnerCallEngine(key: key, extensionID: extensionID, recent: recent, audio: ownerAudio(), calls: voiceCalls,
                                     deps: { audio, events in ownerDeps(key.group, audio, events) })
        attach(engine, to: call)
        await place(call, origin: origin) { [weak self] why in
            self?.chatInstead(key, extensionID: extensionID, recent: recent, why: why)
        }
    }

    /// Asks the system to start the call. Too early (the app still coming to the front): once more half a second later.
    /// Refused otherwise: `instead` (the in-app voice chat), and the call ends. Who the call is with is taken once more
    /// just before the Start, after every wait (a cold launch's roster and look may have come meanwhile): the look and the
    /// name the main conversation's top bar says, together, fixed from then on as the system's handle is; only while the
    /// same assistant is picked on the same hub as when its conversation was decided (`CallAssistant.Owner`): another one
    /// picked meanwhile keeps the call's own name and look (#9887 review).
    func place(_ call: PersonaCall, origin: CallOrigin, instead: (@MainActor (CallSystemError) -> Void)? = nil) async {
        if origin != .app { await Self.waitUntilActive() }
        await readySystem()
        let system = system
        var again = true
        while call.phase.isLive {
            // Asked again just before each Start: an urgent call that rang meanwhile goes first.
            if let note = admission() { return refuse(call, note: note) }
            if !call.assistant.retake(assistant(), name: assistantName(), owner: assistantOwner()) {
                Self.log.notice("call \(call.id.uuidString, privacy: .public): another assistant was picked meanwhile; the call keeps its own")
            }
            do {
                try await system.start(call.id, handle: .outgoing(name: call.name, kind: handleKind(), conversation: call.conversation?.title))
                // Hung up while the system took it: its call ends too.
                if call.phase.isEnded { try? await system.perform(.end, for: call.id) }
                return
            } catch {
                let why = CallSystemError(error)
                Self.log.notice("call \(call.id.uuidString, privacy: .public): the system did not start it: \(why.description, privacy: .public)")
                if why.isTooEarly, again {
                    again = false
                    try? await clock.sleep(for: .milliseconds(500))
                    continue
                }
                guard call.phase.isLive else { return }
                call.note = Self.insteadLine(why)
                dispatch(call, .settled(.failed))
                instead?(why)
                return
            }
        }
    }

    /// What refuses a call to Persona now: an urgent call ringing or on (its audio is the system's), a recording or
    /// dictation on the microphone. Asked first and again after every wait before the system's Start.
    func admission() -> VoiceUnavailableNote? {
        if urgentInProgress { return Self.urgentNote }
        if voiceCalls.isRecording(RecordingMicrophone.shared) { return Self.recordingNote }
        return nil
    }

    /// The system would not place the call: the voice chat in the app on the same conversation, its page open. It goes
    /// on away from its page, in the background and locked, as the call would have (`VoiceCalls`).
    private func chatInstead(_ key: SessionKey, extensionID: String, recent: [VoiceLine], why: CallSystemError) {
        let model = AppModel.shared
        model.toast = Toast(Self.insteadLine(why))
        presented = false
        model.open(key)
        let calls = voiceCalls
        var deps = VoiceDeps.live(group: key.group)
        #if DEBUG
        if VoiceSample.silentAudio { deps.audio = { VoiceSample.SilentAudio() } }
        #endif
        Task { await calls.start(key, extensionID: extensionID, surface: .phone, recent: recent, deps: deps) }
    }

    nonisolated static func insteadLine(_ why: CallSystemError) -> String {
        why.isAtCapacity ? String(localized: "Another call is on, so this is a voice chat in the app instead")
            : String(localized: "This device didn't place the call, so this is a voice chat in the app instead")
    }

    // MARK: Recents

    /// The user activities a redial comes as (Recents may hand either).
    nonisolated static let activityTypes = ["INStartCallIntent", "INStartAudioCallIntent"]

    nonisolated static func isCall(_ activity: NSUserActivity) -> Bool {
        activityTypes.contains(activity.activityType) || activity.interaction?.intent is INStartCallIntent
    }

    /// The handle the redial names (the name the main conversation's top bar said when it was called, `CallHandle.name`, Developing Persona's
    /// placeholder address, or whatever an older entry had): any calls the current hub's main conversation.
    nonisolated static func handle(_ activity: NSUserActivity) -> String? {
        (activity.interaction?.intent as? INStartCallIntent)?.contacts?.first?.personHandle?.value
    }

    /// A redial from Recents: calls Persona once the app is in front. True when the activity was one. The same tap can
    /// come twice (the scene's delegate and SwiftUI's `onContinueUserActivity`): once is taken.
    @discardableResult func continueActivity(_ activity: NSUserActivity) -> Bool {
        guard Self.isCall(activity) else { return false }
        let now = clock.now
        if let last = lastRedial, now.timeIntervalSince(last) < 2 { return true }
        lastRedial = now
        let handle = Self.handle(activity)
        let named = handle == CallHandle.name(assistantName()) ? "the assistant's name"
            : handle == CallHandle.placeholderAddress ? "the placeholder address" : "another value"
        Self.log.notice("call: a redial (\(activity.activityType, privacy: .public), \(named, privacy: .public))")
        Task { await callPersona(origin: .recents) }
        return true
    }

    // MARK: Waiting

    /// A launch from Recents: the hubs read and the main conversation known (at most 8 s), so the call goes to them.
    private static func waitForHub(_ group: String?) async {
        let model = AppModel.shared
        for _ in 0..<32 {
            if let g = group ?? model.hub?.group, model.api(g) != nil, model.assistantMain != nil { return }
            try? await Task.sleep(for: .milliseconds(250))
        }
    }

    /// The system refuses a Start while the app is still coming to the front (at most 5 s).
    private static func waitUntilActive() async {
        for _ in 0..<50 where UIApplication.shared.applicationState != .active {
            try? await Task.sleep(for: .milliseconds(100))
        }
    }

    // MARK: Notes

    static var urgentNote: VoiceUnavailableNote { .urgentCall }

    static var recordingNote: VoiceUnavailableNote {
        VoiceUnavailableNote(title: String(localized: "Finish the recording or dictation first"),
                             message: String(localized: "A recording or dictation is using the microphone."), settings: nil)
    }

    static var noHubNote: VoiceUnavailableNote {
        VoiceUnavailableNote(title: String(localized: "Not paired with a hub yet"),
                             message: String(localized: "Pair this device with a hub to call Persona."), settings: nil)
    }

    static var noMainNote: VoiceUnavailableNote {
        VoiceUnavailableNote(title: String(localized: "Persona's main chat isn't set up yet"),
                             message: String(localized: "Open Persona's chat once, then call again."), settings: nil)
    }

    static var unreachableNote: VoiceUnavailableNote {
        VoiceUnavailableNote(title: String(localized: "Can't reach the hub"),
                             message: String(localized: "Try again once connected to the hub."), settings: nil)
    }

    static var microphoneNote: VoiceUnavailableNote {
        VoiceUnavailableNote(title: String(localized: "Microphone access is needed"),
                             message: String(localized: "Allow Persona to use the microphone in Settings, then call again."), settings: .system)
    }
}

extension VoiceCalls {
    /// A recording or dictation has the microphone (not a voice chat, which gives it way to a call).
    func isRecording(_ microphone: RecordingMicrophone) -> Bool {
        guard !microphone.isFree else { return false }
        guard let call = current, !call.phase.isEnded, call.surface != .call else { return true }
        return false
    }
}
#endif
