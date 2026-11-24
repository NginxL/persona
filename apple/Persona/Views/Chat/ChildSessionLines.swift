import Foundation
import PersonaWire

/// Puts each folded child into the exchange that was going on when it started (web `placeChildren`), as a step of
/// that exchange's work line. The card does not move the exchange's clock: it takes the time of the step it follows.
enum ChildSessionLines {
    /// Exact transcript location of a folded child card in a parent chat's lines.
    struct Target: Equatable {
        var supervisionID: String?
        var groupID: String
        var stepID: String

        var lineID: String { groupID + "/" + stepID }
    }

    static func insert(_ lines: [ChatLine], places: [ChildSessions.Place], sessions: [SessionKey: Session], complete: Bool) -> [ChatLine] {
        let assigned = ChildSessions.assign(starts(lines), places, complete: complete)
        guard !assigned.isEmpty else { return lines }
        return rewrite(lines, assigned: assigned, sessions: sessions)
    }

    /// Recovers the full `SessionKey` carried by a synthetic child work-step call.
    /// Strictly requires `ChildSessions.callPrefix` so ordinary tool calls whose inputs happen to contain
    /// `machine_id` and `session_id` are never misidentified as synthetic child cards.
    static func childKey(of call: ToolCall) -> SessionKey? {
        guard call.id.hasPrefix(ChildSessions.callPrefix), call.id.count > ChildSessions.callPrefix.count else {
            return nil
        }
        if let machine = call.input["machine_id"]?.string, !machine.isEmpty,
           let session = call.input["session_id"]?.string, !session.isEmpty {
            let group = call.input["hub_group"]?.string ?? ""
            return SessionKey(group: group, machineID: machine, sessionID: session)
        }
        return ChildSessions.sessionKey(fromCallID: call.id)
    }

    /// The work line and step anchor holding `key`'s child card, including one nested inside a supervision round.
    static func target(of key: SessionKey, in lines: [ChatLine], supervisionID: String? = nil) -> Target? {
        for line in lines {
            switch line.content {
            case .work(let wid, let items, _, _, _):
                for item in items {
                    if case .step(.call(let call)) = item, childKey(of: call) == key {
                        return Target(supervisionID: supervisionID, groupID: wid, stepID: call.id)
                    }
                }
            case .supervision(let sid, let inner, _, _):
                if let found = target(of: key, in: inner, supervisionID: supervisionID ?? sid) { return found }
            default:
                break
            }
        }
        return nil
    }

    /// The work line and step anchor matching a `ChildReveal` (full `SessionKey` when present, else `sessionID`).
    static func target(matching reveal: ChildReveal, in lines: [ChatLine], supervisionID: String? = nil) -> Target? {
        if let key = reveal.key { return target(of: key, in: lines, supervisionID: supervisionID) }
        for line in lines {
            switch line.content {
            case .work(let wid, let items, _, _, _):
                for item in items {
                    if case .step(.call(let call)) = item, let ck = childKey(of: call), reveal.matches(ck) {
                        return Target(supervisionID: supervisionID, groupID: wid, stepID: call.id)
                    }
                }
            case .supervision(let sid, let inner, _, _):
                if let found = target(matching: reveal, in: inner, supervisionID: supervisionID ?? sid) { return found }
            default:
                break
            }
        }
        return nil
    }

    /// The work line that holds this child's card, including one inside a supervision round.
    static func group(of key: SessionKey, in lines: [ChatLine]) -> String? {
        target(of: key, in: lines)?.groupID
    }

    private static func starts(_ lines: [ChatLine]) -> [ChildSessions.Exchange] {
        var out: [ChildSessions.Exchange] = []
        for line in lines {
            switch line.content {
            case .work(let id, _, _, let start, _):
                out.append(.init(id: id, start: start))
            case .supervision(_, let inner, _, _):
                out += starts(inner)
            default:
                break
            }
        }
        return out
    }

    private static func rewrite(_ lines: [ChatLine], assigned: [String: [ChildSessions.Place]], sessions: [SessionKey: Session]) -> [ChatLine] {
        lines.map { line in
            switch line.content {
            case .supervision(let id, let inner, let live, let source):
                let next = ChatLine.supervision(id: id, lines: rewrite(inner, assigned: assigned, sessions: sessions),
                                                live: live, source: source)
                return next.preservingMetadata(from: line)
            case .work(let id, let items, let live, let start, let end):
                guard let placed = assigned[id], !placed.isEmpty else { return line }
                let next = ChatLine.work(id: id, items: withChildren(items, placed, sessions: sessions, exchangeStart: start),
                                         live: live, start: start, end: end)
                return next.preservingMetadata(from: line)
            default:
                return line
            }
        }
    }

    private static func withChildren(_ items: [WorkItem], _ places: [ChildSessions.Place], sessions: [SessionKey: Session],
                                     exchangeStart: Date?) -> [WorkItem] {
        var out = items
        for place in places {
            guard let session = sessions[place.key] else { continue }
            let at = place.time ?? .distantFuture
            var index = 0
            for (offset, item) in out.enumerated() {
                if case .step(let step) = item, let time = step.firstTS, time <= at { index = offset + 1 }
            }
            let near: Date? = {
                if index > 0, case .step(let step) = out[index - 1] { return step.firstTS }
                if index < out.count, case .step(let step) = out[index] { return step.firstTS }
                return exchangeStart
            }()
            out.insert(.step(.call(ToolCall(use: synthetic(session, ts: near)))), at: index)
        }
        return out
    }

    /// Enough of the row for the collapsed card, and for the work step's identity to change when the row does.
    private static func synthetic(_ session: Session, ts: Date?) -> Item {
        var fields: [String: JSONValue] = [
            "title": .string(session.displayTitle),
            "state": .string(session.state.rawValue),
            "machine_id": .string(session.machineID),
            "session_id": .string(session.sessionID),
        ]
        if !session.group.isEmpty { fields["hub_group"] = .string(session.group) }
        if let model = session.model, !model.isEmpty { fields["model"] = .string(model) }
        if let effort = session.effort, !effort.isEmpty { fields["effort"] = .string(effort) }
        if let runtime = session.runtime ?? session.agent, !runtime.isEmpty { fields["runtime"] = .string(runtime) }
        return Item(id: ChildSessions.callID(for: session.key), seq: 0, ts: ts, role: "assistant", kind: .toolUse,
                    text: session.lastText, toolUseID: nil, toolName: "Child", toolInput: .object(fields), isError: nil, image: nil)
    }
}

private extension ChatLine {
    func preservingMetadata(from original: ChatLine) -> ChatLine {
        var copy = self
        copy.showsSource = original.showsSource
        copy.hidesPrompt = original.hidesPrompt
        copy.startsExchange = original.startsExchange
        copy.inFoldedRound = original.inFoldedRound
        copy.modelNames = original.modelNames
        return copy
    }
}

/// The prompt, latest reply and last tool of one child, read only while its card is open.
enum ChildTranscript {
    static func load(_ api: APIClient, _ key: SessionKey) async throws -> ChildSessions.Detail {
        let latest = try await api.items(key, beforeSeq: nil)
        var head: [Item] = []
        if latest.hasMore {
            head = (try? await api.itemRange(key, from: 1, through: 40)) ?? []
        }
        var tail: [Item] = []
        if let fold = latest.items.last(where: { $0.kind == .fold }), let through = fold.fold?.throughSeq {
            let lastTool = latest.items.last { $0.kind == .toolUse }?.seq ?? -1
            if fold.seq > lastTool {
                let from = max(fold.seq, through - 8)
                tail = (try? await api.itemRange(key, from: from, through: through)) ?? []
            }
        }
        return ChildSessions.detail(head: head, latest: latest.items, tail: tail)
    }
}
