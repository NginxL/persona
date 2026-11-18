import Foundation
import os

/// A system call is live on the iPhone: a call to Persona or an urgent call (docs/apple/calls.md, "Nothing in the app
/// ends a call"). Nothing in the app may take the microphone or set up the audio session under it, so what would
/// (dictation, a recording, the screen viewer's picture in picture, disconnecting the call's hub) asks here first: it is
/// refused with the line given, or carries on without touching the session, and the call goes on untouched. The
/// iPhone's call manager answers (`PersonaCalls.guardTheApp`, from the moment it exists: before that no call can be
/// live); on the watch, Vision Pro and in the extensions nothing has a system call and every answer is "no call".
@MainActor enum LiveCallGate {
    /// Whether a system call is live now. Set once by the iPhone's call manager.
    static var isLive: @MainActor () -> Bool = { false }

    /// Why disconnecting the hubs of these groups (nil: every hub) waits: a live call talks through one of them, and
    /// the disconnect would end it with no word (`PersonaCalls.disconnectRefusal`). Nil when it may go ahead.
    static var disconnectRefusal: @MainActor (_ groups: Set<String>?) -> String? = { _ in nil }

    static var callIsLive: Bool { isLive() }

    /// Why dictation does not start now, or nil when it may: its own audio session would cut the call's microphone.
    static var dictationRefusal: String? {
        callIsLive ? dictationLine : nil
    }

    /// Why a recording (a meeting, a note, all day) does not start or go on now, or nil when it may.
    static var recordingRefusal: String? {
        callIsLive ? recordingLine : nil
    }

    nonisolated static var dictationLine: String { String(localized: "On a call — dictation is off until it ends") }
    nonisolated static var recordingLine: String { String(localized: "End the call first") }

    // MARK: Off the main actor

    /// The audio session's own queue: dictation opens and lets go of its session here, and the call sets its category
    /// here (`PersonaCalls.setCallCategory`), so one never runs in the middle of the other. A block that would set up
    /// the session for dictation asks `liveNow` on this queue first: the call's category, queued once the call went live,
    /// always comes after it.
    nonisolated static let sessionQueue = DispatchQueue(label: "persona.audio-session")

    nonisolated private static let live = OSAllocatedUnfairLock(initialState: false)

    /// `callIsLive` for code off the main actor (the session queue): the call manager sets it, on the main actor, as a
    /// call becomes live and before anything sets up the session for it.
    nonisolated static var liveNow: Bool { live.withLock { $0 } }

    /// The call manager says a call became live, or the last one ended (`PersonaCalls.noteLive`).
    static func callLiveChanged(_ on: Bool) {
        live.withLock { $0 = on }
    }
}
