import Foundation
import PersonaWire

private struct ParsedShellQuestion: Sendable { var question: ShellQuestion? }

/// The start remedies a hub can offer (#10365). Missing fields and missing feature flags preserve the old UI and requests.
nonisolated enum TaskStartRemedy {
    static let retryLauncherFeature = "task_retry_launcher"
    static let continueFeature = "start_continue"
    static let answerFeature = "todo_start_answer"
    static let shellWaiting = "shell_waiting"
    private static let parseMemo = TextMemo<ParsedShellQuestion>(limit: 128)

    /// View bodies can ask on every redraw; parse each distinct argument set once, not the ISO date every time.
    private static func question(_ args: [String: String]?) -> ShellQuestion? {
        let key = (args ?? [:]).sorted { $0.key < $1.key }.map {
            "\($0.key.count):\($0.key)\($0.value.count):\($0.value)"
        }.joined(separator: "|")
        return parseMemo(key) { _ in ParsedShellQuestion(question: ShellQuestion(args: args)) }.question
    }

    /// The to-do's own plan wins; a plan that took it over through `todo_id` is the fallback, unless dismissed.
    static func planTask(of todo: TodoItem, in plans: [String: TaskPlan]) -> PlanTask? {
        if let id = todo.planID, let own = plans[id]?.tasks.first(where: { $0.todoID == todo.id }) { return own }
        for plan in plans.values where plan.state != .dismissed {
            if let task = plan.tasks.first(where: { $0.todoID == todo.id }) { return task }
        }
        return nil
    }

    /// The short, coded reason; any other code keeps the hub's raw text and the existing plain-words fallback.
    static func failureLine(code: String?, args: [String: String]?) -> String? {
        switch code {
        case "launcher_missing":
            guard args?["where"] == "terminal", let launcher = args?["launcher"], !launcher.isEmpty else { return nil }
            return String(localized: "\(launcher) can't start in this computer's terminal")
        case "worktree_path_taken":
            return String(localized: "A workspace from an earlier try is still in the way")
        default:
            return nil
        }
    }

    /// Only the first suggested launcher is offered, and only when this hub takes the override.
    static func retryLauncher(_ task: PlanTask?, features: Set<String>) -> String? {
        guard features.contains(retryLauncherFeature), let launcher = task?.retryWith?.first, !launcher.isEmpty else { return nil }
        return launcher
    }

    static func taskQuestion(_ task: PlanTask?, features: Set<String>, now: Date = .now) -> ShellQuestion? {
        guard features.contains(answerFeature), task?.errorCode == shellWaiting else { return nil }
        return question(task?.errorArgs)?.open(now: now)
    }

    static func sessionQuestion(code: String?, args: [String: String]?, features: Set<String>, now: Date = .now) -> ShellQuestion? {
        guard features.contains(continueFeature), code == shellWaiting else { return nil }
        return question(args)?.open(now: now)
    }
}

/// A shell startup question as the computer listed it. No answer text is accepted here: only one of the offered IDs.
nonisolated struct ShellQuestion: Hashable, Sendable {
    var line: String
    var choices: [String]
    var kind: String
    var screen: String?
    var lineMode: Bool
    var secret: Bool
    var defaultChoice: String?
    var keptUntil: String?
    var keptAt: Date?
    var keptClock: String?

    init?(args: [String: String]?) {
        guard let line = args?["line"], !line.isEmpty else { return nil }
        let choices = (args?["choices"] ?? "").split(separator: ",").map { $0.trimmingCharacters(in: .whitespacesAndNewlines) }.filter { !$0.isEmpty }
        guard !choices.isEmpty else { return nil }
        self.line = line
        self.choices = choices
        kind = args?["kind"] ?? ""
        screen = args?["screen"].flatMap { $0.isEmpty ? nil : $0 }
        lineMode = args?["line_mode"] == "true"
        secret = args?["secret"] == "true"
        defaultChoice = args?["default"].flatMap { $0.isEmpty ? nil : $0 }
        keptUntil = args?["kept_until"].flatMap { $0.isEmpty ? nil : $0 }
        if let keptUntil {
            let formatter = ISO8601DateFormatter()
            formatter.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
            keptAt = formatter.date(from: keptUntil) ?? ISO8601DateFormatter().date(from: keptUntil)
            keptClock = keptAt?.formatted(date: .omitted, time: .shortened)
        }
    }

    /// A missing or malformed expiry stays open, as on the web; only a valid time already past closes the card.
    func open(now: Date = .now) -> ShellQuestion? { (keptAt.map { $0 > now } ?? true) ? self : nil }

    var hint: String {
        if secret { return String(localized: "It's asking for a password, which Persona never types from here. Cancel it, or type it on the computer.") }
        if kind == "ssh_host_key" { return String(localized: "Whether to trust a new host key is decided on the computer; from here you can say no.") }
        return String(localized: "Pick an answer and the agent starts in that tab.")
    }

    /// The answer's plain words, with the key the computer expects. Future kinds use the generic key label.
    func label(_ choice: String) -> String {
        switch choice {
        case "enter":
            if defaultChoice == "y" { return String(localized: "Enter (yes)") }
            if defaultChoice == "n" { return String(localized: "Enter (no)") }
            return String(localized: "Enter [Shell Question]")
        case "cancel": return String(localized: "Cancel (Ctrl-C)")
        case "yes": return String(localized: "Yes [Shell Question]")
        case "no": return String(localized: "No [Shell Question]")
        default: break
        }
        switch "\(kind):\(choice)" {
        case "update:y": return String(localized: "Update (\(choice))")
        case "update:n", "zsh_newuser:q": return String(localized: "Not now (\(choice))")
        case "compinit:y": return String(localized: "Continue anyway (\(choice))")
        case "compinit:n": return String(localized: "Stop compinit (\(choice))")
        case "zsh_newuser:0": return String(localized: "Create an empty .zshrc (\(choice))")
        case "p10k_wizard:q": return String(localized: "Quit the wizard (\(choice))")
        case "rvm_trust:n": return String(localized: "Don't trust (\(choice))")
        case "rvm_trust:c": return String(localized: "Cancel (\(choice))")
        default:
            if choice == "y" { return String(localized: "Yes (\(choice))") }
            if choice == "n" { return String(localized: "No (\(choice))") }
            return String(localized: "Press \(choice)")
        }
    }
}
