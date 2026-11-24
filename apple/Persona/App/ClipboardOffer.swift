import Foundation
import PersonaWire
import UIKit

/// "Send what you just copied to MacBook?" (#3064): back in the app after copying something elsewhere, a toast offers to
/// send it to a computer that shares its clipboard. Opt-in (Settings › Phone Data), off by default. Only the pasteboard's
/// change count and whether it holds anything are looked at, which never prompts; it is read only when Send is tapped.
nonisolated enum ClipboardOffer {
    /// Settings › Phone Data › Offer to Send What You Copied.
    static let enabledKey = "clipboard.offerOnReturn"
    /// The computer last sent to (`MachineRef.path`), offered first. Written on a send: rarely.
    static let lastMachineKey = "clipboard.lastMachine"
    /// The pasteboard's change count when the app last went to the background (`StateCache`).
    static let seenCache = "clipboard-seen"

    /// The computer to offer: the one last sent to if it still can take it, else the first online one that shares its
    /// clipboard; nil when none does.
    static func target(_ machines: [Machine], preferred: String?) -> Machine? {
        let ready = machines.filter { $0.online && !$0.isPending && $0.clipboard?.usable == true }
        return ready.first { $0.ref.path == preferred } ?? ready.first
    }

    /// Offer only when it is on, the pasteboard changed while the app was away and it holds something; never while
    /// VoiceOver or Switch Control runs (`assistive`): the offer is a toast whose button they can't reach before it goes
    /// (docs/design/HIG_AUDIT.md HIG-005), and the computer page's Send Clipboard does the same.
    static func shouldOffer(enabled: Bool, seen: Int?, now: Int, hasContent: Bool, assistive: Bool = false) -> Bool {
        guard enabled, !assistive, let seen else { return false }
        return seen != now && hasContent
    }
}

#if os(iOS)
/// Watches the app come and go (`PersonaApp`'s scene phase) for `ClipboardOffer`.
@MainActor final class ClipboardOfferWatcher {
    static let shared = ClipboardOfferWatcher()
    private var seen: Int? = StateCache.load(ClipboardOffer.seenCache, as: Int.self)
    private var offering: Task<Void, Never>?
    /// Away since the last offer: a launch, or the app went to the background. A system alert over the app (the paste
    /// prompt, a permission) only makes it inactive for a moment; coming back from that offers nothing, so what the app
    /// itself copied (Get Clipboard) is never offered back.
    private var away = true

    /// Gone to the background: what is on the pasteboard now is what the user already had here.
    func left() {
        away = true
        let now = UIPasteboard.general.changeCount
        guard now != seen else { return }
        seen = now
        StateCache.save(ClipboardOffer.seenCache, now)
    }

    /// Back in front: offer what was copied meanwhile, once.
    func returned(_ app: AppModel) {
        let board = UIPasteboard.general
        let now = board.changeCount
        defer {
            if seen != now {
                seen = now
                StateCache.save(ClipboardOffer.seenCache, now)
            }
        }
        guard away else { return }
        away = false
        let enabled = UserDefaults.standard.bool(forKey: ClipboardOffer.enabledKey)
        let assistive = UIAccessibility.isVoiceOverRunning || UIAccessibility.isSwitchControlRunning
        guard ClipboardOffer.shouldOffer(enabled: enabled, seen: seen, now: now, hasContent: PhonePasteboard.live.hasContent(),
                                         assistive: assistive) else { return }
        offering?.cancel()
        offering = Task { [weak app] in
            // After the app has settled in front (a toast posted during the switch is easily missed), and on a cold
            // launch once the machines are in.
            let preferred = UserDefaults.standard.string(forKey: ClipboardOffer.lastMachineKey)
            var machine: Machine?
            for _ in 0..<10 {
                try? await Task.sleep(for: .milliseconds(600))
                guard !Task.isCancelled, let app else { return }
                machine = ClipboardOffer.target(app.machines, preferred: preferred)
                if machine != nil || !app.machines.isEmpty { break }
            }
            guard let machine, let app else { return }
            app.toast = Toast(String(localized: "Send what you just copied to \(machine.name)?"), style: .info,
                              action: Toast.Action(title: String(localized: "Send")) { Self.send(to: machine, app) })
        }
    }

    private static func send(to machine: Machine, _ app: AppModel) {
        let ref = machine.ref
        let model = ClipboardModel(deps: ClipboardSection.deps(app, ref))
        Task {
            let outcome = await model.sendPasteboard(machineID: ref.id)
            ClipboardSection.report(outcome, machine: machine.name, ref: ref, app: app)
        }
    }
}
#endif
