import Foundation
import PersonaWire

/// Puts each folded child into the exchange that was going on when it started (web `placeChildren`), as a step of
/// that exchange's work line. The card does not move the exchange's clock: it takes the time of the step it follows.
enum ChildSessionLines {
    static func insert(_ lines: [ChatLine], places: [ChildSessions.Place], sessions: [String: Session], complete: Bool) -> [ChatLine] {
        let assigned = ChildSessions.assign(starts(lines), places, complete: complete)
        guard !assigned.isEmpty else { return lines }
        return rewrite(lines, assigned: assigned, sessions: sessions)
    }

    /// The work line that holds this child's card, including one inside a supervision round.
    static func group(of sessionID: String, in lines: [ChatLine]) -> String? {
        let id = ChildSessions.callPrefix + sessionID
        for line in lines {
            switch line.content {
            case .work(let wid, let items, _, _, _):
                if items.contains(where: { item in
                    if case .step(.call(let call)) = item { return call.id == id }
                    return false
                }) { return wid }
            case .supervision(_, let inner, _, _):
                if let found = group(of: sessionID, in: inner) { return found }
            default:
                break
            }
        }
        return nil
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

    private static func rewrite(_ lines: [ChatLine], assigned: [String: [ChildSessions.Place]], sessions: [String: Session]) -> [ChatLine] {
        lines.map { line in
            switch line.content {
            case .supervision(let id, let inner, let live, let source):
                var next = ChatLine.supervision(id: id, lines: rewrite(inner, assigned: assigned, sessions: sessions),
                                                live: live, source: source)
                next.showsSource = line.showsSource
                next.hidesPrompt = line.hidesPrompt
                return next
            case .work(let id, let items, let live, let start, let end):
                guard let placed = assigned[id], !placed.isEmpty else { return line }
                var next = ChatLine.work(id: id, items: withChildren(items, placed, sessions: sessions, exchangeStart: start),
                                         live: live, start: start, end: end)
                next.showsSource = line.showsSource
                next.hidesPrompt = line.hidesPrompt
                return next
            default:
                return line
            }
        }
    }

    private static func withChildren(_ items: [WorkItem], _ places: [ChildSessions.Place], sessions: [String: Session],
                                     exchangeStart: Date?) -> [WorkItem] {
        var out = items
        for place in places {
            guard let session = sessions[place.sessionID] else { continue }
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
        ]
        if let model = session.model, !model.isEmpty { fields["model"] = .string(model) }
        if let effort = session.effort, !effort.isEmpty { fields["effort"] = .string(effort) }
        if let runtime = session.runtime ?? session.agent, !runtime.isEmpty { fields["runtime"] = .string(runtime) }
        return Item(id: ChildSessions.callPrefix + session.sessionID, seq: 0, ts: ts, role: "assistant", kind: .toolUse,
                    text: session.lastText, toolUseID: nil, toolName: "Child", toolInput: .object(fields), isError: nil, image: nil)
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
