import Foundation
import Testing
@testable import Persona
import PersonaWire

/// A card its task's supervisor holds (`Interaction.supervisorHeld`: `delegating` on a permission, a plan or a terminal
/// dialog, D11 of docs/plans/plan-derived-counts.md, confirmed by the owner on 2026-11-23): it is the supervisor's until
/// `ask_escalate` hands it to the user, so it is no row of the Agents tab's Needs You, counts nowhere, and nothing on the
/// phone answers it (its card, Siri's Approve First, the watch, a notification's buttons). A `delegating` question (the
/// assistant deciding whether it may answer for the user) stays listed and answerable. Made-up values.
@MainActor
struct SupervisorHeldCardTests {
    private func session(_ id: String, state: String = "needs_you", manager: String? = nil) throws -> Session {
        let stamp = manager.map { #","manager":"\#($0)""# } ?? ""
        return try JSON.decoder.decode(Session.self, from: Data(
            #"{"machine_id":"studio","session_id":"\#(id)","title":"\#(id)","cwd":"/home/user/project","status":"idle","state":"\#(state)","updated_at":"2026-11-10T08:00:00Z","state_since":"2026-11-10T08:00:00Z"\#(stamp)}"#.utf8))
    }

    /// A card of `kind` on `session` ("" for none), created `minute` minutes past eight, `delegating` when held.
    private func card(_ id: String, session: String, kind: String, minute: Int = 0, delegating: Bool? = nil,
                      manager: String? = nil) throws -> Interaction {
        var body: String
        switch kind {
        case "question": body = #","questions":[{"question":"Which port?","options":[{"label":"8080"},{"label":"9090"}]}]"#
        case "screen":
            body = #","screen":"Do you want to proceed?\n\n❯ 1. Yes\n  2. No\n","dialog":{"question":"Do you want to proceed?","options":[{"key":"1","label":"Yes","keys":["1"]},{"key":"2","label":"No","keys":["2"]}],"rev":"5f0c2a9d41b7"}"#
        case "plan": body = ##","plan":"# Plan\n\nRemove the build folder.""##
        default: body = #","tool_name":"Bash","summary":"rmdir build""#
        }
        if let delegating { body += #","delegating":\#(delegating)"# }
        if let manager { body += #","manager":"\#(manager)""# }
        let created = String(format: "2026-11-10T08:%02d:00Z", minute)
        return try JSON.decoder.decode(Interaction.self, from: Data(
            #"{"id":"\#(id)","machine_id":"studio","session_id":"\#(session)","kind":"\#(kind)","created_at":"\#(created)"\#(body)}"#.utf8))
    }

    // MARK: The rule

    @Test func onlyADelegatingCardThatIsNoQuestionIsHeld() throws {
        for kind in ["permission", "plan", "screen"] {
            let held = try card("ix", session: "s", kind: kind, delegating: true)
            #expect(held.supervisorHeld, "\(kind)")
            #expect(!held.answerableHere, "\(kind)")
            #expect(!held.isDelegating, "\(kind): not the assistant deciding about a question")
            let handed = try card("ix", session: "s", kind: kind, delegating: false)
            #expect(!handed.supervisorHeld && handed.answerableHere, "\(kind) handed to the user")
            let never = try card("ix", session: "s", kind: kind)
            #expect(!never.supervisorHeld, "\(kind) never delegated")
        }
        // A delegating question is the assistant's to decide about: listed and answerable, as before.
        let looking = try card("ix", session: "s", kind: "question", delegating: true)
        #expect(looking.isDelegating && !looking.supervisorHeld && looking.answerableHere)
        // The snapshot cache writes cards back: the hold survives it.
        let held = try card("ix", session: "s", kind: "screen", delegating: true)
        #expect(try JSON.decoder.decode(Interaction.self, from: JSON.encoder.encode(held)).supervisorHeld)
    }

    // MARK: Needs You

    @Test func aSessionWhoseCardsAreAllHeldIsNoRow() throws {
        let waiting = AgentsNeedsYou.waiting([try session("t")])
        #expect(waiting.map(\.sessionID) == ["t"], "its state still says needs_you")
        let rows = AgentsNeedsYou.rows(cards: [try card("ix-terminal", session: "t", kind: "screen", delegating: true)], waiting: waiting)
        #expect(rows.isEmpty)
        #expect(AgentsNeedsYou.badge(rows) { _ in .agents } == 0)
        // Two held cards, a plan and a permission: still no row.
        let two = [try card("ix-plan", session: "t", kind: "plan", delegating: true),
                   try card("ix-perm", session: "t", kind: "permission", minute: 1, delegating: true)]
        #expect(AgentsNeedsYou.rows(cards: two, waiting: waiting).isEmpty)
        // Handed to the user (`ask_escalate` clears delegating), it is a row again.
        let handed = AgentsNeedsYou.rows(cards: [try card("ix-terminal", session: "t", kind: "screen", delegating: false)], waiting: waiting)
        #expect(handed.map(\.id) == ["agent:studio/t"])
        #expect(handed.first?.cards.map(\.id) == ["ix-terminal"])
    }

    @Test func aRowHoldsOnlyTheCardsThatAreNotHeld() throws {
        let waiting = AgentsNeedsYou.waiting([try session("p")])
        let cards = [try card("ix-held-permission", session: "p", kind: "permission", delegating: true),
                     try card("ix-asked", session: "p", kind: "question", minute: 1)]
        let rows = AgentsNeedsYou.rows(cards: cards, waiting: waiting)
        #expect(rows.map(\.id) == ["agent:studio/p"])
        #expect(rows.first?.cards.map(\.id) == ["ix-asked"])
        #expect(AgentsNeedsYou.badge(rows) { _ in .agents } == 1)
        // A delegating question beside a held permission is listed, as held questions are (D11).
        let looking = [try card("ix-held-permission", session: "p", kind: "permission", delegating: true),
                       try card("ix-looking", session: "p", kind: "question", minute: 1, delegating: true)]
        #expect(AgentsNeedsYou.rows(cards: looking, waiting: waiting).first?.cards.map(\.id) == ["ix-looking"])
    }

    @Test func aHeldCardOfASessionNotWaitingOrOnNoSessionIsNoRow() throws {
        let working = AgentsNeedsYou.waiting([try session("w", state: "working")])
        #expect(AgentsNeedsYou.rows(cards: [try card("ix", session: "w", kind: "permission", delegating: true)], waiting: working).isEmpty)
        #expect(AgentsNeedsYou.rows(cards: [try card("ix", session: "", kind: "permission", delegating: true)], waiting: []).isEmpty)
        // Another session waiting in its terminal, no card read, stays a row.
        let other = AgentsNeedsYou.waiting([try session("t"), try session("o")])
        let rows = AgentsNeedsYou.rows(cards: [try card("ix", session: "t", kind: "screen", delegating: true)], waiting: other)
        #expect(rows.map(\.id) == ["agent:studio/o"])
    }

    @Test func theSwitcherLeavesOutASessionWhoseCardsAreAllHeld() throws {
        let roster = ManagerRoster(managers: [HubManager(id: "assistant", name: "助手", online: true, isDefault: true, builtin: true)],
                                   defaultID: "assistant")
        func waiting(_ sessions: [Session], _ cards: [Interaction]) -> [String: Int] {
            AssistantBadges.waiting(roster: roster, group: "", sessions: sessions, interactions: cards) { _, _ in true }
        }
        let t = try session("t", manager: "assistant")
        #expect(waiting([t], [try card("ix", session: "t", kind: "screen", delegating: true, manager: "assistant")]).isEmpty)
        #expect(waiting([], [try card("ix", session: "", kind: "permission", delegating: true, manager: "assistant")]).isEmpty)
        #expect(waiting([t], [try card("ix", session: "t", kind: "screen", delegating: true, manager: "assistant"),
                              try card("ix-asked", session: "t", kind: "question", minute: 1, manager: "assistant")]) == ["assistant": 1])
        #expect(waiting([t], []) == ["assistant": 1], "in Needs You with no card read: still its own prompt")
    }

    // MARK: No way to answer it

    @Test func siriNeitherReadsOutNorApprovesAHeldCard() throws {
        let held = try card("held", session: "a", kind: "permission", delegating: true)
        let open = try card("open", session: "b", kind: "permission", minute: 1)
        let sessions = [try session("a"), try session("b")]
        #expect(IntentPicker.visible([held, open], sessions: sessions, mutes: [:]).map(\.id) == ["open"])
        #expect(ApproveFirstIntent.target([held, open], sessions: sessions, mutes: [:])?.id == "open")
        #expect(ApproveFirstIntent.target([held], sessions: sessions, mutes: [:]) == nil)
    }

    @Test func theWatchLeavesOutAHeldCard() throws {
        let cards = [try card("held", session: "a", kind: "screen", delegating: true), try card("open", session: "a", kind: "permission")]
        #expect(WatchInteractions.leavingMuted(cards).map(\.id) == ["open"])
    }

    @Test func aNotificationsButtonDoesNotSettleAHeldCard() throws {
        let cards = [try card("held", session: "a", kind: "permission", delegating: true), try card("open", session: "a", kind: "plan")]
        #expect(!NotificationActions.answerable("held", among: cards))
        #expect(NotificationActions.answerable("open", among: cards))
        #expect(NotificationActions.answerable("unknown", among: cards), "a card the app has not loaded is left to the hub")
    }
}
