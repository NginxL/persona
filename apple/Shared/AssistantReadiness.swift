import Foundation

/// What the main conversation still needs (docs/assistant/onboarding.md 4.4, 5.1; docs/protocol/protocol.md「Main chat readiness」): the hub works it
/// out (`GET /api/v1/assistant/readiness`, feature `assistant_readiness`) and every client draws the same thing from it —
/// in `setup` a card over the composer with the first missing step, the send button saying what to do first, example
/// questions that open local help. Every enum-like field is a String: a newer hub's value decodes and is shown as
/// 「还差一步」 with its title (docs/assistant/onboarding.md section 5「总规矩」). The web's lib/readiness.ts is the same logic.
nonisolated struct AssistantReadiness: Codable, Hashable, Sendable {
    nonisolated struct Machine: Codable, Hashable, Sendable {
        var id: String
        var name: String
    }

    nonisolated struct Action: Codable, Hashable, Sendable {
        var id: String
    }

    nonisolated struct Step: Codable, Hashable, Sendable {
        var id: String
        var state: String
        var code: String?
        var machine: String?
        var launcher: String?
        var version: String?
        var title: String?
        var detail: String?
        /// A rate limit's main turn time, health-only reset window, or other newest evidence time.
        var occurrence: String?
        var action: Action?
        var altActions: [Action]?

        enum CodingKeys: String, CodingKey {
            case id, state, code, machine, launcher, version, title, detail, action
            case occurrence
            case altActions = "alt_actions"
        }

        /// Nothing to do for it: done, not needed, or not known yet (unknown / checking never block, 5.1).
        var passes: Bool { ["ok", "skipped", "unknown", "checking"].contains(state) }
    }

    nonisolated struct Suggestion: Codable, Hashable, Sendable {
        var id: String
        var title: String
        /// What tapping it sends (full mode); none for a `local` one.
        var prompt: String?
        /// Opens help on this device, never sent to the model.
        var local: Bool?
        /// The phone data it reads (phone_calendar…): the system asks for that permission first.
        var needs: [String]?
    }

    nonisolated struct Probe: Codable, Hashable, Sendable {
        var ok: Bool?
        var agent: String?
        var model: String?
        var answer: String?
        var millis: Int?
        var error: String?
    }

    var version: Int?
    var mode: String
    var ready: Bool?
    var next: String?
    var machine: Machine?
    var launcher: String?
    var steps: [Step]
    var suggestions: [Suggestion]?
    var probe: Probe?
    var dismissed: Bool?
    var checkedAt: String?

    enum CodingKeys: String, CodingKey {
        case version, mode, ready, next, machine, launcher, steps, suggestions, probe, dismissed
        case checkedAt = "checked_at"
    }

    init(mode: String, next: String? = nil, machine: Machine? = nil, launcher: String? = nil, steps: [Step],
         suggestions: [Suggestion]? = nil, probe: Probe? = nil, dismissed: Bool? = nil) {
        self.version = 1
        self.mode = mode
        self.ready = mode == "full"
        self.next = next
        self.machine = machine
        self.launcher = launcher
        self.steps = steps
        self.suggestions = suggestions
        self.probe = probe
        self.dismissed = dismissed
    }

    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        version = try c.decodeIfPresent(Int.self, forKey: .version)
        mode = try c.decodeIfPresent(String.self, forKey: .mode) ?? "full"
        ready = try c.decodeIfPresent(Bool.self, forKey: .ready)
        next = try c.decodeIfPresent(String.self, forKey: .next)
        machine = try c.decodeIfPresent(Machine.self, forKey: .machine)
        launcher = try c.decodeIfPresent(String.self, forKey: .launcher)
        steps = try c.decodeIfPresent([Step].self, forKey: .steps) ?? []
        suggestions = try c.decodeIfPresent([Suggestion].self, forKey: .suggestions)
        probe = try c.decodeIfPresent(Probe.self, forKey: .probe)
        dismissed = try c.decodeIfPresent(Bool.self, forKey: .dismissed)
        checkedAt = try c.decodeIfPresent(String.self, forKey: .checkedAt)
    }

    static let feature = "assistant_readiness"

    /// The steps that decide the mode, in order; Try a Message never blocks (5.1).
    static let blocking = ["computer", "claude", "model"]

    func step(_ id: String) -> Step? { steps.first { $0.id == id } }

    /// The step the card shows. In `setup`, the first of computer / claude / model that is todo or a problem (a rate
    /// limit passes by itself); a `setup` with none of them missing shows the step `next` names (a newer hub's).
    /// In `full`, a problem that does not block (rate_limited): a card, the send button as it is.
    var cardStep: Step? {
        if mode == "setup" { return blockingStep }
        return Self.blocking.lazy.compactMap { step($0) }.first { $0.state == "problem" && $0.code == "rate_limited" }
    }

    /// What holds the send button: only in `setup` (`full` sends; `chat_only` is P1 and sends as before).
    var blockingStep: Step? {
        guard mode == "setup" else { return nil }
        for id in Self.blocking {
            guard let s = step(id), !s.passes, s.code != "rate_limited" else { continue }
            return s
        }
        guard let next, !(Self.blocking + ["first_message"]).contains(next), let s = step(next), s.state != "ok", s.state != "skipped" else { return nil }
        return s
    }

    /// The steps of computer / claude / model still to do (Settings's 「还有 N 步」): only the ones that block, like
    /// `blockingStep` and the hub's readinessBlocks; unknown / checking and a rate limit leave nothing to do (5.1).
    var stepsToDo: [Step] { Self.blocking.compactMap { step($0) }.filter { !$0.passes && $0.code != "rate_limited" } }

    var stepsLeft: Int { stepsToDo.count }

    /// The main conversation already runs: its computer, Claude Code and model all check out.
    var claudeWorks: Bool { Self.blocking.allSatisfy { step($0)?.state == "ok" } }

    /// The action ids a card draws a button for (5.1); any other id is text only (5 General Rules).
    static let knownActions: Set<String> = ["add_computer", "confirm_computer", "wake_computer", "install_claude", "login_claude",
                                            "connect_model", "edit_provider", "test_assistant", "enable_tls", "open_admin"]

    static func knows(action id: String?) -> Bool { id.map(knownActions.contains) ?? false }
}

/// A start or a send the hub refused with a code (docs/assistant/onboarding.md 5.2, docs/protocol/protocol.md「Error body」): drawn as a card
/// with the hub's words and a button for its code, never a toast. Anything else keeps today's toast.
nonisolated struct CodedFailure: Hashable, Sendable, Identifiable {
    var code: String
    /// The hub's words, in the request's language.
    var error: String
    /// The original text (a shell's error…): only under Details.
    var detail: String?
    var action: String?
    var machine: String?
    var launcher: String?
    var requestID: String?
    var id: String { "\(code)|\(error)|\(requestID ?? "")" }

    /// The codes a failure card is drawn for.
    static let codes: Set<String> = ["no_machine", "machine_pending", "claude_missing", "launcher_missing", "node_unreachable",
                                     "assistant_offline", "node_outdated"]

    init(code: String, error: String, detail: String? = nil, action: String? = nil, machine: String? = nil,
         launcher: String? = nil, requestID: String? = nil) {
        self.code = code
        self.error = error
        self.detail = detail
        self.action = action
        self.machine = machine
        self.launcher = launcher
        self.requestID = requestID
    }

    /// The card for a coded refusal, or nil when the error is not one (keep the toast).
    init?(_ error: Error) {
        guard let api = error as? APIError, let code = api.code, Self.codes.contains(code) else { return nil }
        let f = api.fields
        func str(_ k: String) -> String? { f[k].flatMap { $0.isEmpty ? nil : $0 } }
        var action = str("action")
        if action == nil, code == "node_unreachable" { action = "wake_computer" }
        if action == nil, code == "no_machine" { action = "add_computer" }
        if action == nil, code == "machine_pending" { action = "confirm_computer" }
        self.init(code: code, error: api.message, detail: str("detail"), action: action, machine: str("machine_name"),
                  launcher: str("launcher"), requestID: str("request_id") ?? api.requestID)
    }

    /// The assistant restarting answers 503 assistant_offline for a few seconds: tried again quietly (4.6 rule 4).
    static func passesBySelf(_ error: Error) -> Bool { (error as? APIError)?.code == "assistant_offline" }
}

/// A turn of Persona's conversation that ended on a model error the user has to fix (the session's `turn_error_code`):
/// the card's words and its button, instead of Claude Code's 「Not logged in」 as an answer (4.4「模型出错」).
nonisolated struct TurnErrorFix: Hashable, Sendable {
    var title: String
    var detail: String
    var action: String

    init?(code: String?, launcher: String? = nil) {
        let owner = launcher?.trimmingCharacters(in: .whitespacesAndNewlines)
        if let owner, !owner.isEmpty {
            switch code {
            case "not_logged_in":
                self.init(title: String(localized: "Model not connected: \(owner) isn't signed in"), detail: String(localized: "The Claude desktop app's sign-in doesn't count for it. Run claude auth login in a terminal on that computer, or use an API key instead."), action: "connect_model")
                return
            case "auth_failed":
                self.init(title: String(localized: "Model not connected: the key for \(owner) is wrong"), detail: String(localized: "The key is wrong or has expired; use one that works."), action: "edit_provider")
                return
            case "model_route":
                self.init(title: String(localized: "Model not connected: this model isn't available on \(owner)"), detail: String(localized: "The model name is wrong, or the model isn't available right now; try another one."), action: "connect_model")
                return
            default:
                return nil
            }
        }
        switch code {
        case "not_logged_in":
            // The CLI the conversation runs, not the Claude desktop app, whose sign-in it does not share (#9366).
            self.init(title: String(localized: "Model not connected: Claude Code CLI isn't signed in"),
                      detail: String(localized: "The Claude desktop app's sign-in doesn't count for it. Run claude auth login in a terminal on that computer, or use an API key instead."),
                      action: "connect_model")
        case "auth_failed":
            self.init(title: String(localized: "Model not connected: the key is wrong"), detail: String(localized: "The key is wrong or has expired; use one that works."), action: "edit_provider")
        case "model_route":
            self.init(title: String(localized: "Model not connected: this model can't be used"), detail: String(localized: "The model name is wrong, or the model isn't available right now; try another one."), action: "connect_model")
        default:
            return nil
        }
    }

    init(title: String, detail: String, action: String) {
        self.title = title
        self.detail = detail
        self.action = action
    }
}
