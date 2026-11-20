import Foundation

/// The finished agents marked seen here that the hub has not taken yet (#1588). A `seen` over a socket that still says
/// connected but is gone (the hub restarted, the phone just came back) was lost without a word, and the next list from
/// the hub put every one of them back to Unread: Mark All as Read looked like it failed. A mark stays here until a list
/// from the hub shows it recorded (a later `seen_at`), or the agent moved on; until then the session is shown seen and
/// the mark goes again (`AppModel.sendSeen`).
///
/// Only the hub's own times are compared with each other (its `seen_at`, the machine's `updated_at`): the phone's
/// clock only says when to try again and when to give up.
nonisolated struct SeenOutbox {
    /// How long a mark sent over the socket gets before it goes again over HTTP, which answers.
    static let confirmWithin: TimeInterval = 5
    /// Given up after this: the hub was out of reach all along.
    static let keepFor: TimeInterval = 10 * 60
    /// The machine touched the finished agent's transcript without a new turn (the hub's own slack is 2 s).
    static let sameTurn: TimeInterval = 2

    enum Route: Equatable { case socket, http }

    struct Mark: Equatable {
        let id: UInt64
        /// The hub's `seen_at` and the agent's `updated_at` when it was marked.
        var seenBefore: Date?
        var updatedBefore: Date?
        /// On the phone's clock.
        var markedAt: Date
        var sentAt: Date
        var route: Route = .socket
        /// The hub answered the HTTP request: nothing more to send, only the list to wait for.
        var answered = false
    }

    private(set) var marks: [SessionKey: Mark] = [:]
    private var nextID: UInt64 = 0

    /// `session` as the app had it when the user looked at it. Unread and Just finished both wait for the hub.
    mutating func mark(_ key: SessionKey, _ session: Session, at now: Date) {
        guard session.needsSeen() else { return }
        nextID &+= 1
        marks[key] = Mark(id: nextID, seenBefore: session.seenAt, updatedBefore: session.updatedAt, markedAt: now, sentAt: now)
    }

    @discardableResult mutating func sent(_ key: SessionKey, _ route: Route, at now: Date) -> UInt64? {
        guard var mark = marks[key] else { return nil }
        mark.sentAt = now
        mark.route = route
        mark.answered = false
        marks[key] = mark
        return mark.id
    }

    mutating func answered(_ key: SessionKey, markID: UInt64?) {
        guard let markID, marks[key]?.id == markID else { return }
        marks[key]?.answered = true
    }

    /// A forgotten pairing cannot leave receipts for the next owner of that local group.
    mutating func forget(groups: Set<String>? = nil) {
        guard let groups else { marks.removeAll(); return }
        marks = marks.filter { !groups.contains($0.key.group) }
    }

    /// Sent over the socket and not taken within `confirmWithin`: for HTTP now.
    func due(at now: Date) -> [SessionKey] {
        Array(marks.filter { _, m in
            m.route == .socket && !m.answered && now.timeIntervalSince(m.sentAt) >= Self.confirmWithin && !expired(m, now)
        }.keys)
    }

    /// When the next one sent over the socket is due, if any.
    func nextDue(at now: Date) -> Date? {
        marks.values.filter { $0.route == .socket && !$0.answered && !expired($0, now) }
            .map { $0.sentAt.addingTimeInterval(Self.confirmWithin) }.min()
    }

    /// Not answered yet in this group: sent again once its socket is back (an HTTP request that failed waits for that).
    func unanswered(group: String, at now: Date) -> [SessionKey] {
        Array(marks.filter { key, m in key.group == group && !m.answered && !expired(m, now) }.keys)
    }

    /// Lists from the hub landing: marks it has recorded, or that no longer matter, go; the sessions still waiting are
    /// shown seen. Lists of other machines leave their marks alone.
    mutating func land(_ lists: [MachineRef: [Session]], at now: Date) -> [MachineRef: [Session]] {
        guard !marks.isEmpty else { return lists }
        var pending: [MachineRef: [String: (key: SessionKey, mark: Mark)]] = [:]
        for (key, mark) in marks {
            if expired(mark, now) { marks[key] = nil }
            else if lists[key.machineRef] != nil { pending[key.machineRef, default: [:]][key.sessionID] = (key, mark) }
        }

        var out = lists
        for (machine, var remaining) in pending {
            guard var list = lists[machine] else { continue }
            var changed = false
            for i in list.indices {
                if remaining.isEmpty { break }
                guard let (key, mark) = remaining.removeValue(forKey: list[i].sessionID) else { continue }
                let s = list[i]
                let recorded = (s.seenAt ?? .distantPast) > (mark.seenBefore ?? .distantPast)
                let newTurn = (s.updatedAt ?? .distantPast) > (mark.updatedBefore ?? .distantPast).addingTimeInterval(Self.sameTurn)
                if recorded || newTurn || s.state == .working || s.state == .needsYou {
                    marks[key] = nil
                } else if s.needsSeen() {
                    list[i].markLocallySeen()
                    changed = true
                }
            }
            if changed { out[machine] = list }
            for entry in remaining.values { marks[entry.key] = nil } // gone from this machine's list
        }
        return out
    }

    private func expired(_ m: Mark, _ now: Date) -> Bool { now.timeIntervalSince(m.markedAt) > Self.keepFor }
}
