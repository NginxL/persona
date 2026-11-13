import Foundation

/// An agent's closeout (docs/assistant/agent-work.md "Closeout", #3612; the web's lib/closeout.ts): the record it ends
/// a turn with, saying whether the work is done, what changed, whether it is merged and deployed, what it verified and
/// what is missing. It is the agent's own claim. Where the hub looked itself (`checked`), its finding stands beside
/// the claim: confirmed, contradicted or not checked. The session's page draws it as a card (`CloseoutCard`), list
/// rows as a short suffix of their line (`Session.listPreview`).
///
/// Every field but `status` is optional and decoded on its own: one the hub leaves out, or sends in a shape this
/// build does not know, is as if it were not there. Session lists carry no `did` and no `verified`.
nonisolated struct Closeout: Codable, Hashable, Sendable {
    /// What the hub found when it looked at the closeout's pull request: "yes" or "no" where it could tell.
    nonisolated struct Checked: Codable, Hashable, Sendable {
        var merged: String?
        var deployed: String?

        enum CodingKeys: String, CodingKey { case merged, deployed }

        init(merged: String? = nil, deployed: String? = nil) {
            self.merged = merged
            self.deployed = deployed
        }

        init(from decoder: Decoder) throws {
            let c = try decoder.container(keyedBy: CodingKeys.self)
            merged = (try? c.decodeIfPresent(String.self, forKey: .merged)) ?? nil
            deployed = (try? c.decodeIfPresent(String.self, forKey: .deployed)) ?? nil
        }
    }

    /// The statuses this build draws; any other is no closeout to show.
    nonisolated enum Status: String, Sendable {
        case done
        case notDone = "not_done"
        case blocked
    }

    /// The two things an agent claims and the hub can check.
    nonisolated enum Subject: Sendable {
        case merged
        case deployed
    }

    /// How a claim stands against the hub's finding. `found`: the agent did not say, the hub's finding stands alone.
    nonisolated enum ClaimState: Sendable {
        case confirmed
        case contradicted
        case unchecked
        case found
    }

    /// A claim on merged or deployed: what the agent said (nil when it did not), what the hub found (nil when it did
    /// not look or could not tell).
    nonisolated struct Claim: Hashable, Sendable {
        var said: Bool?
        var found: Bool?
        var state: ClaimState
    }

    /// done, not_done or blocked, as the hub sent it.
    var status: String
    /// For a blocked one: who or what it waits for.
    var waiting: String?
    /// What changed, at most three sentences.
    var did: [String]?
    /// "yes" or "no"; nil when it does not apply or was not said.
    var merged: String?
    var deployed: String?
    /// What was run or measured, with its reading.
    var verified: String?
    /// What it still needs from someone.
    var missing: String?
    var checked: Checked?

    enum CodingKeys: String, CodingKey {
        case status, waiting, did, merged, deployed, verified, missing, checked
    }

    init(status: String, waiting: String? = nil, did: [String]? = nil, merged: String? = nil, deployed: String? = nil,
         verified: String? = nil, missing: String? = nil, checked: Checked? = nil) {
        self.status = status
        self.waiting = waiting
        self.did = did
        self.merged = merged
        self.deployed = deployed
        self.verified = verified
        self.missing = missing
        self.checked = checked
    }

    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        status = (try? c.decodeIfPresent(String.self, forKey: .status)) ?? ""
        waiting = (try? c.decodeIfPresent(String.self, forKey: .waiting)) ?? nil
        did = (try? c.decodeIfPresent([String].self, forKey: .did)) ?? nil
        merged = (try? c.decodeIfPresent(String.self, forKey: .merged)) ?? nil
        deployed = (try? c.decodeIfPresent(String.self, forKey: .deployed)) ?? nil
        verified = (try? c.decodeIfPresent(String.self, forKey: .verified)) ?? nil
        missing = (try? c.decodeIfPresent(String.self, forKey: .missing)) ?? nil
        checked = (try? c.decodeIfPresent(Checked.self, forKey: .checked)) ?? nil
    }

    /// The status, when it is one this build knows.
    var known: Status? { Status(rawValue: status) }

    private static func yesNo(_ value: String?) -> Bool? {
        switch value {
        case "yes": true
        case "no": false
        default: nil
        }
    }

    /// The claim on merged or deployed with the hub's finding; nil when neither the agent said nor the hub found.
    func claim(_ subject: Subject) -> Claim? {
        let said = Self.yesNo(subject == .merged ? merged : deployed)
        let found = Self.yesNo(subject == .merged ? checked?.merged : checked?.deployed)
        switch (said, found) {
        case (nil, nil): return nil
        case (nil, let found?): return Claim(said: nil, found: found, state: .found)
        case (let said?, nil): return Claim(said: said, found: nil, state: .unchecked)
        case (let said?, let found?): return Claim(said: said, found: found, state: said == found ? .confirmed : .contradicted)
        }
    }

    /// The hub found a claim of the closeout to be untrue.
    var contradicted: Bool {
        claim(.merged)?.state == .contradicted || claim(.deployed)?.state == .contradicted
    }

    /// One claim in a few words for a row: the hub's finding when it has one ("merged ✓"), else the agent's word as
    /// it said it ("merged"); a "yes" the hub found untrue is marked ("✗ says merged, isn't").
    private func words(_ subject: Subject) -> String? {
        guard let claim = claim(subject) else { return nil }
        let merged = subject == .merged
        if claim.state == .contradicted, claim.said == true {
            return merged ? String(localized: "✗ says merged, isn't") : String(localized: "✗ says deployed, isn't")
        }
        if let found = claim.found {
            if found { return merged ? String(localized: "merged ✓") : String(localized: "deployed ✓") }
            return merged ? String(localized: "not merged") : String(localized: "not deployed")
        }
        if claim.said == true { return merged ? String(localized: "merged [Closeout]") : String(localized: "deployed [Closeout]") }
        return merged ? String(localized: "not merged") : String(localized: "not deployed")
    }

    /// "merged · not deployed": where the closeout stands on merged and deployed, for the end of a row's line; empty
    /// when it says nothing on either and the hub found nothing.
    var suffix: String {
        [words(.merged), words(.deployed)].compactMap { $0 }.joined(separator: " · ")
    }

    /// A row's line from the closeout alone, for a turn the hub gave no line of its own: what a blocked one waits for,
    /// what a not-done one still needs; nil for a status this build does not know.
    var line: String? {
        switch known {
        case .blocked?:
            if let waiting, !waiting.isEmpty { return String(localized: "Blocked on: \(waiting)") }
            return String(localized: "Blocked [Closeout]")
        case .notDone?:
            if let missing, !missing.isEmpty { return String(localized: "Not done: \(missing)") }
            return String(localized: "Not done [Closeout]")
        case .done?:
            return String(localized: "Reported done")
        case nil:
            return nil
        }
    }

    /// The card's head: the status in a word, or what a blocked one waits for.
    var statusLine: String? {
        switch known {
        case .done?: return String(localized: "Done [Closeout]")
        case .notDone?: return String(localized: "Not done [Closeout]")
        case .blocked?:
            if let waiting, !waiting.isEmpty { return String(localized: "Blocked on: \(waiting)") }
            return String(localized: "Blocked [Closeout]")
        case nil: return nil
        }
    }

    // MARK: - The raw block in a reply

    private static let block = try! NSRegularExpression(pattern: #"<closeout\b[^>]*>[\s\S]*?</closeout>"#)
    private static let fenceOpen = try! NSRegularExpression(pattern: #"(^|\n)[ \t]*(`{3,}|~{3,})[^\n`]*\n[ \t]*\z"#)
    private static let fenceClose = try! NSRegularExpression(pattern: #"^[ \t]*\n[ \t]*(`{3,}|~{3,})[ \t]*(?=\n|\z)"#)

    /// `text` without its last closeout block, the one the hub read (it takes the last): the card above the transcript
    /// says it, so the reply does not say it again as raw text. Only the block goes, with the blank lines it leaves
    /// and a code fence that held nothing else; text without a block comes back as it is.
    static func strip(_ text: String) -> String {
        let all = text as NSString
        guard let last = block.matches(in: text, range: NSRange(location: 0, length: all.length)).last else { return text }
        var before = all.substring(to: last.range.location)
        var after = all.substring(from: last.range.location + last.range.length)
        // A fence around the block alone goes with it: an empty code box says nothing.
        let head = before as NSString
        let tail = after as NSString
        if let open = fenceOpen.firstMatch(in: before, range: NSRange(location: 0, length: head.length)),
           let close = fenceClose.firstMatch(in: after, range: NSRange(location: 0, length: tail.length)),
           head.substring(with: open.range(at: 2)) == tail.substring(with: close.range(at: 1)) {
            before = head.substring(to: open.range.location)
            after = tail.substring(from: close.range.location + close.range.length)
        }
        before = Self.trimmingEnd(before)
        after = Self.trimmingStart(after)
        if before.isEmpty { return after }
        if after.isEmpty { return before }
        return before + "\n\n" + after
    }

    /// Whether `text` holds a closeout block.
    static func holdsBlock(_ text: String) -> Bool {
        block.firstMatch(in: text, range: NSRange(location: 0, length: (text as NSString).length)) != nil
    }

    private static func trimmingEnd(_ s: String) -> String {
        var view = Substring(s)
        while let c = view.last, c.isWhitespace { view.removeLast() }
        return String(view)
    }

    private static func trimmingStart(_ s: String) -> String {
        var view = Substring(s)
        while let c = view.first, c.isWhitespace { view.removeFirst() }
        return String(view)
    }
}

/// Something the work produced (`work.artifacts`): a pull request, a commit, a branch, a document, a build or a link.
nonisolated struct WorkArtifact: Codable, Hashable, Sendable {
    /// pr, commit, branch, doc, build or url; kept as the hub sent it.
    var kind: String
    var ref: String

    enum CodingKeys: String, CodingKey { case kind, ref }

    init(kind: String, ref: String) {
        self.kind = kind
        self.ref = ref
    }

    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        kind = (try? c.decodeIfPresent(String.self, forKey: .kind)) ?? ""
        ref = (try? c.decodeIfPresent(String.self, forKey: .ref)) ?? ""
    }

    /// The reference as a web link, when it is one (http or https only).
    var link: URL? {
        let raw = ref.trimmingCharacters(in: .whitespacesAndNewlines)
        guard let url = URL(string: raw), let scheme = url.scheme?.lowercased(), scheme == "http" || scheme == "https",
              url.host?.isEmpty == false else { return nil }
        return url
    }

    /// What the card shows for it: "PR #12" for a pull request's link (GitLab-style merge requests read "MR !12"),
    /// any other link as its address without the scheme, anything else as it is.
    var label: String {
        guard let link else { return ref.trimmingCharacters(in: .whitespacesAndNewlines) }
        let parts = link.path.split(separator: "/")
        if kind == "pr", let number = parts.last, Int(number) != nil, parts.count >= 2 {
            let holder = parts[parts.count - 2]
            if holder == "pull" { return "PR #\(number)" }
            if holder == "merge_requests" { return "MR !\(number)" }
        }
        let text = link.absoluteString
        for scheme in ["https://", "http://"] where text.lowercased().hasPrefix(scheme) {
            return String(text.dropFirst(scheme.count))
        }
        return text
    }
}
