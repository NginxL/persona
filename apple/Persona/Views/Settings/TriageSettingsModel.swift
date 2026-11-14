import Foundation
import Observation

/// Settings › Session Triage (docs/assistant/agent-work.md「Switches」, the web's TriageSettings, Android's TriageSettingsPage): when an agent
/// stops, the hub can read its last reply, say whether it is done or what it waits for, and act on it. All off until
/// turned on here: the triage calls a model on every stopped turn, and Continue After Errors / Wake on Time run a turn of the agent on
/// its own model. Saved on the hub (`PUT /api/v1/agents/triage`), so it is the same on every device.
@Observable final class TriageSettingsModel {
    struct Deps {
        var load: () async throws -> TriageSwitches
        var save: (TriageSwitches) async throws -> TriageSwitches
    }

    /// Nil until the hub answered (and on a hub without the triage).
    private(set) var switches: TriageSwitches?
    private(set) var saving = false
    /// Why the switches could not be read or the last change did not save.
    private(set) var error: String?
    /// The hub has no `/api/v1/agents/triage` (404): it is older than the triage.
    private(set) var unsupported = false
    @ObservationIgnored private let deps: Deps

    init(deps: Deps) { self.deps = deps }

    /// The status-afterwards row exists when the hub names its optional switch, including an explicit false.
    var showsCloseoutAsk: Bool { switches?.closeoutAsk != nil }

    func load() async {
        do {
            switches = try await deps.load()
            error = nil
            unsupported = false
        } catch let e as APIError where e.status == 404 {
            unsupported = true
        } catch {
            // A timeout or no network has no `shown` text, and the page had nothing else to show: it spun for good
            // with every switch greyed out (#3485). A cancelled read (the page went away) says nothing.
            if switches == nil, !error.isCancellation {
                let why = error.shown ?? String(localized: "Can't reach the hub")
                self.error = String(localized: "Couldn't load: \(why)")
            }
        }
    }

    /// The triage itself and Remind When Seen, Unanswered (rules only, no model) can always be flipped; what the triage may do only
    /// while it is on.
    func canChange(_ key: WritableKeyPath<TriageSwitches, Bool>) -> Bool {
        guard let switches, !saving else { return false }
        return key == \TriageSwitches.enabled || key == \TriageSwitches.replyReminder || switches.enabled
    }

    /// Shows the new value at once; a failed save puts the old one back and says why.
    func set(_ key: WritableKeyPath<TriageSwitches, Bool>, _ on: Bool) async {
        guard canChange(key), let before = switches, before[keyPath: key] != on else { return }
        var next = before
        next[keyPath: key] = on
        switches = next
        saving = true
        error = nil
        defer { saving = false }
        do {
            switches = try await deps.save(next)
        } catch {
            switches = before
            let why = error.shown ?? String(localized: "Can't reach the hub")
            self.error = String(localized: "Couldn't change it: \(why) [Messages Views 87cfe2]")
        }
    }
}
