import Foundation

// A task the main conversation supervises (#3124, docs/assistant/unified-entry.md ("Supervision")): what done means, where its review
// stands, the reviews so far and what its agent last reported (#5112). Only such todos carry `supervision`; an older hub
// sends none. Mirrors web/src/lib/supervision.ts.

/// A todo's `supervision` (docs/protocol/protocol.md, Todo). Decoded leniently: missing fields take their empty value, a review
/// or a progress that does not decode is left out; `TodoItem` drops a supervision that does not decode at all.
nonisolated struct TaskSupervision: Codable, Hashable, Sendable {
    /// One criterion as a review judged it.
    nonisolated struct Check: Codable, Hashable, Sendable {
        var criterion: String
        var met: Bool
        var evidence: String?
    }

    /// One review by the supervisor: accepted, rework, needs_user or cancelled.
    nonisolated struct Review: Codable, Hashable, Sendable {
        var at: Date?
        var verdict: String
        var round: Int?
        var checks: [Check]
        var note: String?

        init(at: Date? = nil, verdict: String, round: Int? = nil, checks: [Check] = [], note: String? = nil) {
            self.at = at
            self.verdict = verdict
            self.round = round
            self.checks = checks
            self.note = note
        }

        init(from decoder: Decoder) throws {
            let c = try decoder.container(keyedBy: CodingKeys.self)
            at = try? c.decodeIfPresent(Date.self, forKey: .at)
            verdict = try c.decodeIfPresent(String.self, forKey: .verdict) ?? ""
            round = try? c.decodeIfPresent(Int.self, forKey: .round)
            checks = (try? c.decodeIfPresent([Check].self, forKey: .checks)) ?? []
            note = try? c.decodeIfPresent(String.self, forKey: .note)
        }
    }

    /// Its agent's latest report to the supervisor (report_to_supervisor): the step of how many (0 or absent: not
    /// said) and what it said in a line.
    nonisolated struct Progress: Codable, Hashable, Sendable {
        var kind: String
        var step: Int?
        var steps: Int?
        var text: String
        var at: Date?

        init(kind: String, step: Int? = nil, steps: Int? = nil, text: String, at: Date? = nil) {
            self.kind = kind
            self.step = step
            self.steps = steps
            self.text = text
            self.at = at
        }

        init(from decoder: Decoder) throws {
            let c = try decoder.container(keyedBy: CodingKeys.self)
            kind = try c.decodeIfPresent(String.self, forKey: .kind) ?? ""
            step = try? c.decodeIfPresent(Int.self, forKey: .step)
            steps = try? c.decodeIfPresent(Int.self, forKey: .steps)
            text = try c.decodeIfPresent(String.self, forKey: .text) ?? ""
            at = try? c.decodeIfPresent(Date.self, forKey: .at)
        }
    }

    var supervisor: String
    var doneWhen: [String]
    /// 0 or nil: not given.
    var expectMinutes: Int?
    /// The hub's state: "" going on, pending_review, rework, needs_user, accepted, cancelled.
    var state: String
    var round: Int
    var reviews: [Review]
    var updatedAt: Date?
    /// When the task entered `pending_review`; nil in every other state, and from an older hub.
    var pendingSince: Date?
    var progress: Progress?

    enum CodingKeys: String, CodingKey {
        case supervisor, state, round, reviews, progress
        case doneWhen = "done_when", expectMinutes = "expect_minutes", updatedAt = "updated_at", pendingSince = "pending_since"
    }

    init(supervisor: String = "", doneWhen: [String] = [], expectMinutes: Int? = nil, state: String = "", round: Int = 0,
         reviews: [Review] = [], updatedAt: Date? = nil, pendingSince: Date? = nil, progress: Progress? = nil) {
        self.supervisor = supervisor
        self.doneWhen = doneWhen
        self.expectMinutes = expectMinutes
        self.state = state
        self.round = round
        self.reviews = reviews
        self.updatedAt = updatedAt
        self.pendingSince = pendingSince
        self.progress = progress
    }

    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        supervisor = try c.decodeIfPresent(String.self, forKey: .supervisor) ?? ""
        doneWhen = try c.decodeIfPresent([String].self, forKey: .doneWhen) ?? []
        expectMinutes = try? c.decodeIfPresent(Int.self, forKey: .expectMinutes)
        state = try c.decodeIfPresent(String.self, forKey: .state) ?? ""
        round = (try? c.decodeIfPresent(Int.self, forKey: .round)) ?? 0
        reviews = (try? c.decodeIfPresent([Review].self, forKey: .reviews)) ?? []
        updatedAt = try? c.decodeIfPresent(Date.self, forKey: .updatedAt)
        pendingSince = try? c.decodeIfPresent(Date.self, forKey: .pendingSince)
        progress = try? c.decodeIfPresent(Progress.self, forKey: .progress)
    }

    /// The latest review, if any.
    var latestReview: Review? { reviews.last }

    /// Whether its acceptance still stands: the latest review accepted it and no review came since. That holds even
    /// when the hub reopened the task because its agent was sent more work (#3156, docs/assistant/todo-status-sop.md
    /// "Accepted tasks"): it is accepted until a newer review says otherwise.
    var acceptanceStands: Bool { state == "accepted" && latestReview?.verdict == "accepted" }

    /// Each criterion with how the latest review that judged it found it; criteria a review judged that are not in
    /// `doneWhen` (a task given none) follow, in the order they were first judged. Once the acceptance stands, a
    /// criterion no review judged (one added after it) is `unchecked`, not open: nothing waits on it any more.
    var checklist: [ReviewCheckItem] {
        let settled = acceptanceStands
        var latest: [String: Check] = [:]
        var order: [String] = []
        for r in reviews {
            for c in r.checks {
                if latest[c.criterion] == nil { order.append(c.criterion) }
                latest[c.criterion] = c
            }
        }
        func evidence(_ c: Check?) -> String? {
            guard let e = c?.evidence?.trimmingCharacters(in: .whitespacesAndNewlines), !e.isEmpty else { return nil }
            return e
        }
        var out: [ReviewCheckItem] = []
        var seen = Set<String>()
        for criterion in doneWhen {
            seen.insert(criterion)
            let c = latest[criterion]
            out.append(ReviewCheckItem(criterion: criterion, mark: c.map { $0.met ? .met : .missing } ?? (settled ? .unchecked : .open), evidence: evidence(c)))
        }
        for criterion in order where !seen.contains(criterion) {
            let c = latest[criterion]
            out.append(ReviewCheckItem(criterion: criterion, mark: c?.met == true ? .met : .missing, evidence: evidence(c)))
        }
        return out
    }

    /// "2/3": criteria met of all, for a chip; "" without criteria. Criteria left unchecked by a standing acceptance
    /// are not counted (an accepted task reads "5/5", not "5/8").
    var checkCount: String {
        let list = checklist.filter { $0.mark != .unchecked }
        guard !list.isEmpty else { return "" }
        return "\(list.filter { $0.mark == .met }.count)/\(list.count)"
    }

    /// "Step 3/5 · …", "Asked Persona: …": the agent's latest report in a line; nil when it has none.
    var progressLine: String? {
        guard let p = progress, !p.text.isEmpty else { return nil }
        return ReviewText.reportLine(kind: p.kind, text: p.text, step: p.step, steps: p.steps)
    }
}

/// The review state that holds now (`TodoItem.reviewState`).
nonisolated enum ReviewState: String, Sendable {
    case ongoing = "", pendingReview = "pending_review", userCheck = "user_check", rework, needsUser = "needs_user", accepted, cancelled
}

/// How a review state is colored: pending / rework caution, needs_user danger, accepted positive, the rest neutral.
nonisolated enum ReviewTone: Sendable {
    case caution, danger, positive, neutral
}

/// A supervised task's chip: its state in words, its tone, and the criteria met ("1/2", "" without criteria).
nonisolated struct ReviewBadge: Hashable, Sendable {
    var label: String
    var tone: ReviewTone
    var count: String

    /// "Rework round 1 · 1/2", or the label alone without criteria.
    var text: String { count.isEmpty ? label : "\(label) · \(count)" }

    /// What a task's row among the tasks in progress says while its supervisor's review is under way and not yet the
    /// owner's (docs/assistant/unified-entry.md "Needs you and In progress"); "Awaiting review" is the owner's.
    static var supervisorReview: String { String(localized: "Supervisor reviewing") }
}

/// One criterion's line in the checklist.
nonisolated struct ReviewCheckItem: Hashable, Sendable {
    /// `unchecked`: added after an acceptance that still stands; no review judged it and none waits on it.
    enum Mark: Sendable { case met, missing, open, unchecked }
    var criterion: String
    var mark: Mark
    var evidence: String?
}

nonisolated extension TodoItem {
    /// The review state that holds now: a cancelled task reopened goes on again; an accepted one stays accepted while
    /// its acceptance stands (`TaskSupervision.acceptanceStands`, #3156).
    var reviewState: ReviewState? {
        guard let supervision else { return nil }
        let st = ReviewState(rawValue: supervision.state) ?? .ongoing
        if doneAt != nil, st == .userCheck { return .accepted }
        if doneAt == nil, st == .accepted, !supervision.acceptanceStands { return .ongoing }
        if doneAt == nil, st == .cancelled { return .ongoing }
        return st
    }

    /// Its supervisor's acceptance still stands (see `TaskSupervision.acceptanceStands`).
    var acceptanceStands: Bool { supervision?.acceptanceStands == true }

    /// Its review's chip; nil for a task that is not supervised.
    var reviewBadge: ReviewBadge? {
        guard let supervision, let state = reviewState else { return nil }
        let (label, tone): (String, ReviewTone) = switch state {
        case .pendingReview: (String(localized: "Awaiting review"), .caution)
        case .userCheck: (String(localized: "Awaiting your verification"), .caution)
        case .rework: (ReviewText.rework(round: supervision.round), .caution)
        case .needsUser: (String(localized: "Needs your call"), .danger)
        case .accepted: (String(localized: "Accepted"), .positive)
        case .cancelled: (String(localized: "Canceled"), .neutral)
        case .ongoing: (String(localized: "Supervised"), .neutral)
        }
        return ReviewBadge(label: label, tone: tone, count: supervision.checkCount)
    }

    /// Its supervisor closed it after reviewing the work (done_by review); reopening undoes it.
    var reviewDone: Bool { doneAt != nil && doneBy == "review" }

    /// Why the hub closed it (auto-complete or a review), when it did.
    var closedReason: String? {
        guard autoDone || reviewDone, let r = doneReason?.trimmingCharacters(in: .whitespacesAndNewlines), !r.isEmpty else { return nil }
        return r
    }
}

/// The words of reviews and reports, shared by the card, the todo pages and the feed.
nonisolated enum ReviewText {
    static func rework(round: Int) -> String {
        let n = max(round, 1)
        return String(localized: "Rework round \(n)")
    }

    /// A review in the conversation's feed (kind `review`), in a line.
    static func verdict(_ verdict: String, round: Int?) -> String {
        switch verdict {
        case "accepted": String(localized: "Accepted")
        case "rework": rework(round: round ?? 1)
        case "needs_user": String(localized: "Needs your call")
        case "cancelled": String(localized: "Canceled")
        default: String(localized: "Review")
        }
    }

    /// A report (on a card, or a feed item of kind `report`) in a line.
    static func reportLine(kind: String, text: String, step: Int? = nil, steps: Int? = nil) -> String {
        switch kind {
        case "question": return String(localized: "Asked Persona: \(text)")
        case "blocked": return String(localized: "Stuck: \(text)")
        case "done": return String(localized: "Says it is done: \(text)")
        default: break
        }
        if let step, step > 0 {
            if let steps, steps > 0 { return String(localized: "Step \(step)/\(steps) · \(text)") }
            return String(localized: "Step \(step) · \(text)")
        }
        return text
    }

    /// A question or a block wants the supervisor's (and the user's) eye.
    static func reportWantsAttention(_ kind: String) -> Bool { kind == "question" || kind == "blocked" }
}
