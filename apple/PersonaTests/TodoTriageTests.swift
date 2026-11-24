import Foundation
import Testing
@testable import Persona
import PersonaWire

/// The To-dos page's status sections (docs/assistant/todo-status-sop.md, #3206): the same cases as
/// web/src/lib/todo-triage.test.ts, so the clients cannot drift.
@Suite(.serialized)
@MainActor
struct TodoTriageTests {
    private let now = ISO8601DateFormatter().date(from: "2026-11-10T12:00:00Z")!
    private func ago(_ h: Double) -> Date { now.addingTimeInterval(-h * 3600) }

    private func session(_ id: String, state: AgentState, updatedHoursAgo: Double = 1, turnError: String? = nil,
                         workOn: String? = nil) -> Session {
        let error = turnError.map { #","turn_error":"\#($0)""# } ?? ""
        let work = workOn.map { #","work":{"phase":"blocked","on":"\#($0)","wait":"Replacement will resume"}"# } ?? ""
        let json = """
        {"machine_id":"m","session_id":"\(id)","title":"\(id)","cwd":"/tmp","status":"\(state == .working ? "working" : "idle")",
         "state":"\(state.rawValue)","updated_at":"\(ISO8601DateFormatter().string(from: ago(updatedHoursAgo)))"\(error)\(work)}
        """
        do { return try JSON.decoder.decode(Session.self, from: Data(json.utf8)) }
        catch { fatalError("bad fixture: \(error)") }
    }

    private func agent(_ sid: String) -> TodoItem.Agent {
        TodoItem.Agent(machineID: "m", sessionID: sid, linkedAt: ago(200))
    }

    private func todo(_ id: String, title: String? = nil, updated: Double = 1, done: Double? = nil, agents: [String] = [],
                      origin: String? = "conv", kind: String? = nil,
                      build: (inout TodoItem) -> Void = { _ in }) -> TodoItem {
        var t = TodoItem(id: id, title: title ?? id, doneAt: done.map(ago), createdAt: ago(300), updatedAt: ago(updated),
                         origin: origin, agents: agents.map(agent))
        build(&t)
        guard let kind else { return t }
        do {
            var object = try #require(JSONSerialization.jsonObject(with: JSON.encoder.encode(t)) as? [String: Any])
            object["kind"] = kind
            return try JSON.decoder.decode(TodoItem.self, from: JSONSerialization.data(withJSONObject: object))
        } catch {
            fatalError("bad kind fixture: \(error)")
        }
    }

    /// An accepted task the hub reopened when its agent was sent more work: eight criteria, the review judged five.
    private func acceptedReopened() -> TaskSupervision {
        let when = (1...8).map { "c\($0)" }
        return TaskSupervision(supervisor: "conv", doneWhen: when, state: "accepted", round: 1,
                               reviews: [.init(at: ago(5), verdict: "accepted", checks: when.prefix(5).map { .init(criterion: $0, met: true) })])
    }

    private var sessions: [Session] {
        [
            session("ask", state: .needsYou), session("run", state: .working), session("unseen", state: .unread),
            session("idle", state: .idle, updatedHoursAgo: 2), session("old", state: .idle, updatedHoursAgo: 100),
            session("old2", state: .idle, updatedHoursAgo: 120),
        ]
    }

    private var lookup: TodosLogic.SessionLookup { TodosLogic.makeSessionLookup(sessions) }

    private func at(_ t: TodoItem) -> TodoTriage { TodoTriageLogic.triage(t, lookup: lookup, now: now) }
    private func ask(_ outcome: String?, verdict: String = "v") -> TodoAsk {
        TodoAsk(outcome: outcome, verdict: verdict, recommend: nil, askedAt: ISO8601DateFormatter().string(from: ago(1)))
    }
    private func prFacts(_ url: String, state: String = "open", draft: Bool = false,
                         checkedHoursAgo: Double = 0) -> TodoPRStatus {
        TodoPRStatus(url: url, state: state, draft: draft, checkedAt: ISO8601DateFormatter().string(from: ago(checkedHoursAgo)))
    }

    @Test func agentAskingIsWaitingWhateverElseHolds() {
        let t = at(todo("t", agents: ["ask", "run"]))
        #expect(t.section == .waiting && t.rule == .agentAsks)
    }

    @Test func interruptedAgentDoesNotHideAnotherAgentsBackgroundWait() throws {
        var cut = session("cut", state: .idle)
        cut.turnError = "stopped"
        var blocked = session("blocked", state: .idle)
        blocked.work = try JSON.decoder.decode(WorkState.self, from: Data(#"{"phase":"blocked","on":"external"}"#.utf8))
        let td = todo("t", agents: ["cut", "blocked"])
        let lookup = TodosLogic.makeSessionLookup([cut, blocked])
        #expect(TodoTriageLogic.agentsStatus(td, lookup: lookup) == .waiting)
        #expect(TodoTriageLogic.triage(td, lookup: lookup, now: now).section == .working)
    }

    @Test func ownerDecisionIsWaiting() {
        #expect(at(todo("t", agents: ["idle"]) { $0.supervision = .init(supervisor: "c", state: "needs_user") }).rule == .ownerDecides)
        #expect(at(todo("t") { $0.ask = ask("decide") }).section == .waiting)
    }

    @Test func pleaseVerifyOrDoneConclusionIsJustFinished() {
        let verify = at(todo("t", agents: ["idle"]) { $0.ask = ask(nil, verdict: "done, deployed: verify below") })
        #expect(verify.section == .justDone && verify.rule == .finishedUnseen)
        #expect(at(todo("t", agents: ["idle"]) { $0.ask = ask("done") }).section == .justDone)
    }

    @Test func runningAgentIsWorkingEvenOnAnOldTodo() {
        let t = at(todo("t", updated: 400, agents: ["run"]))
        #expect(t.section == .working && t.rule == .agentRunning)
    }

    @Test func blockedAgentWaitingIsWorkingEvenOnAnOldTodo() {
        var blocked = session("blocked", state: .idle, updatedHoursAgo: 100)
        blocked.work = try! JSON.decoder.decode(WorkState.self, from: Data(#"{"phase":"blocked","on":"external"}"#.utf8))
        let result = TodoTriageLogic.triage(todo("t", updated: 400, agents: ["blocked"]),
                                            lookup: TodosLogic.makeSessionLookup([blocked]), now: now)
        #expect(result.section == .working && result.rule == .agentRunning)
    }

    /// A previous attempt's stopped error is historical once a replacement is running, even after its own newer activity
    /// (#10399, AC4). The separate To-dos surface must not put the task back in Needs You.
    @Test func oldInterruptionDoesNotOutrankWorkingReplacement() {
        let old = session("cut", state: .idle, updatedHoursAgo: 4, turnError: "Context unavailable")
        let replacement = session("replacement", state: .working, updatedHoursAgo: 0.5)
        let lookup = TodosLogic.makeSessionLookup([old, replacement])
        let item = todo("t", updated: 0.25, agents: ["cut", "replacement"])
        #expect(TodoTriageLogic.agentsStatus(item, lookup: lookup) == .working)
        let t = TodoTriageLogic.triage(item, lookup: lookup, now: now)
        #expect(t.section == .working && t.rule == .agentRunning)
    }

    /// Waiting on another Agent, an external event, or a transient failure is also current work. No background activity
    /// is required for that replacement to suppress the old interruption; #5732 exposes that wait as Waiting / Working.
    @Test(arguments: ["agent", "external", "transient"])
    func oldInterruptionDoesNotOutrankWaitingReplacement(waitingOn: String) {
        let old = session("cut", state: .idle, updatedHoursAgo: 4, turnError: "Context unavailable")
        let replacement = session("replacement", state: .idle, updatedHoursAgo: 0.5, workOn: waitingOn)
        #expect(replacement.waitsToBeWoken && !replacement.backgroundWorking && !replacement.backgroundWaiting)
        let lookup = TodosLogic.makeSessionLookup([old, replacement])
        let item = todo("t", updated: 0.25, agents: ["cut", "replacement"])
        #expect(TodoTriageLogic.agentsStatus(item, lookup: lookup) == .waiting)
        let t = TodoTriageLogic.triage(item, lookup: lookup, now: now)
        #expect(t.section == .working && t.rule == .agentRunning)
    }

    @Test func oldInterruptionDoesNotOutrankTransientWaitingReplacementWithTurnError() {
        var staleInvalid = session("stale", state: .idle, turnError: "Context unavailable", workOn: "transient")
        staleInvalid.turnErrorKind = "unrecoverable"
        let staleItem = todo("stale", agents: ["stale"])
        let staleLookup = TodosLogic.makeSessionLookup([staleInvalid])
        #expect(TodoTriageLogic.agentsStatus(staleItem, lookup: staleLookup) == .interrupted)

        let old = session("cut", state: .idle, updatedHoursAgo: 4, turnError: "Context unavailable")
        var replacement = session("replacement", state: .idle, updatedHoursAgo: 0.5,
                                  turnError: "API Error: 503 Service Unavailable", workOn: "transient")
        replacement.turnErrorKind = "transient"
        #expect(replacement.waitsToBeWoken && !replacement.backgroundWorking && !replacement.backgroundWaiting)
        let lookup = TodosLogic.makeSessionLookup([old, replacement])
        let item = todo("t", updated: 0.25, agents: ["cut", "replacement"])
        #expect(TodoTriageLogic.agentsStatus(item, lookup: lookup) == .waiting)
        let t = TodoTriageLogic.triage(item, lookup: lookup, now: now)
        #expect(t.section == .working && t.rule == .agentRunning)
    }

    /// #10399: legacy sessions have no structured error kind/code, so the raw sealed-compaction error must win over
    /// stale transient work left behind by the interrupted turn.
    @Test func legacyAccountSealedCompactionWithoutErrorMetadataIsInterrupted() {
        let sealed = session(
            "sealed",
            state: .idle,
            turnError: "This conversation was compacted natively by <account>: the compaction is sealed (encrypted) to that account and can't go to it now",
            workOn: "transient"
        )
        let item = todo("sealed", agents: ["sealed"])
        let lookup = TodosLogic.makeSessionLookup([sealed])
        #expect(TodoTriageLogic.agentsStatus(item, lookup: lookup) == .interrupted)
        #expect(TodoTriageLogic.triage(item, lookup: lookup, now: now).section == .waiting)
    }

    @Test func parkedOrClosedTaskDoesNotReviveOldInterruption() {
        let old = session("cut", state: .idle, updatedHoursAgo: 4, turnError: "Context unavailable")
        let lookup = TodosLogic.makeSessionLookup([old])
        let parked = todo("parked", updated: 0.25, agents: ["cut"]) {
            $0.supervision = .init(supervisor: "conv", state: "parked")
        }
        #expect(TodoTriageLogic.agentsStatus(parked, lookup: lookup) == .followUp)
        #expect(TodoTriageLogic.triage(parked, lookup: lookup, now: now).section == .other)

        let closed = todo("closed", updated: 0.25, done: 0.25, agents: ["cut"])
        #expect(TodoTriageLogic.agentsStatus(closed, lookup: lookup) == .done)
        #expect(TodoTriageLogic.triage(closed, lookup: lookup, now: now).section == .done)
    }

    @Test func closedIsImmediatelyDone() {
        #expect(at(todo("t", done: 3)).section == .done)
        #expect(at(todo("t", updated: 30, done: 30)).section == .done)
    }

    @Test func freshlyReportedConcernStaysJustFinished() {
        let fresh = todo("concern", done: 1, kind: "concern") { $0.doneBy = "reported" }
        #expect(at(fresh).section == .justDone && at(fresh).rule == .closed)
        let old = todo("concern", done: 30, kind: "concern") { $0.doneBy = "reported" }
        #expect(at(old).section == .done && at(old).rule == .closed)
        let seen = todo("concern", done: 1, kind: "concern") { $0.doneBy = "reported"; $0.seenAt = ago(0.5) }
        #expect(at(seen).section == .done && at(seen).rule == .closed)
        #expect(TodoSection.justDone.title == StateName.justFinished.text)
    }

    @Test func justFinishedTitleReservesAwaitingReviewForFinishedUnseen() {
        let justFinished = StateName.justFinished.text
        let awaiting = String(localized: "Delivered · Awaiting Review [To-do Sections]")
        #expect(TodoSection.justDone.title(rules: [.finishedUnseen]) == awaiting)
        #expect(TodoSection.justDone.title(rules: [.finishedUnseen, .finishedUnseen]) == awaiting)
        #expect(TodoSection.justDone.title(rules: [.closed]) == justFinished)
        #expect(TodoSection.justDone.title(rules: [.closed, .finishedUnseen]) == justFinished)
        #expect(TodoSection.justDone.title(rules: []) == justFinished)
    }

    @Test func acceptedTaskReopenedByMoreWorkIsFinishedNotFollowUp() {
        let td = todo("t", agents: ["idle"]) {
            $0.reopenedAt = ago(4)
            $0.supervision = acceptedReopened()
        }
        let t = at(td)
        #expect(t.section == .done && t.rule == .accepted)
        let status = TodosLogic.todoStatus(td, lookup: lookup)
        #expect(td.statusLabel(status) == "已验收")
        #expect(td.shownState(status) == .done)
        #expect(td.reviewState == .accepted && td.reviewBadge?.tone == .positive)
    }

    @Test func acceptedTaskWhoseAgentRunsAgainIsWorking() {
        #expect(at(todo("t", agents: ["run"]) { $0.supervision = acceptedReopened() }).section == .working)
    }

    @Test func liveDeliveryWaitsForReviewUntilAccepted() {
        let recent = todo("t") { $0.delivery = .init(state: "live", at: ago(2)) }
        #expect(at(recent).section == .justDone && at(recent).rule == .finishedUnseen)
        #expect(TodoTriageLogic.countsAsOpen(recent, at(recent)))
        let old = todo("t", updated: 300) { $0.delivery = .init(state: "live", at: ago(300)) }
        #expect(at(old).section == .justDone && at(old).rule == .finishedUnseen)
        #expect(TodoTriageLogic.countsAsOpen(old, at(old)))
    }

    @Test func detailLabelsMatchDeliveryAndOwnerWait() {
        let delivered = todo("delivery") {
            $0.supervision = .init(supervisor: "main", state: "pending_review")
            $0.delivery = .init(state: "live", at: ago(1))
        }
        #expect(delivered.statusLabel(.notStarted) == "已交付·待看")
        #expect(delivered.shownState(.notStarted) == .unread)

        let waiting = todo("waiting") { $0.supervision = .init(supervisor: "main", state: "needs_user") }
        #expect(waiting.statusLabel(.followup) == StateName.needsYou.text)
        #expect(waiting.shownState(.followup) == .needsYou)

        let done = todo("done", done: 1) { $0.supervision = .init(supervisor: "main", state: "needs_user") }
        #expect(done.statusLabel(.done) == "已完成")
        #expect(done.shownState(.done) == .done)
    }

    @Test func finishedTurnNobodySawIsJustFinished() {
        let t = at(todo("t", agents: ["unseen"]))
        #expect(t.section == .justDone && t.rule == .finishedUnseen)
    }

    // #5707 + #5732: Seen files ordinary turns, never closure, acceptance of an unaccepted delivery
    @Test func seenFilesOrdinaryTurnsButNotAcceptanceOrAnUnacceptedDelivery() {
        let closed = { (seen: Double) in self.at(self.todo("t", done: 3) { $0.seenAt = self.ago(seen) }) }
        #expect(closed(2).section == .done && closed(2).rule == .closed)
        #expect(closed(3).section == .done)
        #expect(closed(4).section == .done && closed(4).rule == .closed)
        let accepted = at(todo("t", agents: ["idle"]) { $0.supervision = acceptedReopened(); $0.seenAt = ago(4) })
        #expect(accepted.section == .done && accepted.rule == .accepted)
        let deployed = at(todo("t") { $0.delivery = .init(state: "live", at: ago(2)); $0.seenAt = ago(1) })
        #expect(deployed.section == .justDone && deployed.rule == .finishedUnseen)
        let supervisedTodo = todo("t", agents: ["unseen"]) { $0.supervision = TaskSupervision(supervisor: "main"); $0.seenAt = ago(0.5) }
        let supervised = at(supervisedTodo)
        #expect(supervised.section == .justDone && supervised.rule == .finishedUnseen)
        #expect(TodoTriageLogic.seenCanFile(todo("ordinary", agents: ["unseen"])))
        #expect(!TodoTriageLogic.seenCanFile(supervisedTodo))
        #expect(!TodoTriageLogic.seenCanFile(todo("live") { $0.delivery = .init(state: "live", at: ago(2)) }))
        let failed = todo("f", done: 2) { $0.startError = "x"; $0.seenAt = ago(1) }
        let superseded = TodoTriageLogic.triage(failed, lookup: lookup, now: now, superseded: true)
        #expect(superseded.section == .done && superseded.rule == .superseded)
    }

    @Test func finishedTurnSeenSinceItLastMovedIsDoneUnlessItsPRIsOpen() {
        // the unseen session last moved an hour ago
        let seen = at(todo("t", agents: ["unseen"]) { $0.seenAt = ago(0.5) })
        #expect(seen.section == .done && seen.rule == .seen)
        let movedSince = at(todo("t", agents: ["unseen"]) { $0.seenAt = ago(2) })
        #expect(movedSince.section == .justDone && movedSince.rule == .finishedUnseen)
        let pr = at(todo("t", agents: ["unseen"]) {
            $0.seenAt = ago(0.5)
            $0.prURL = "https://example.com/pull/1"
            $0.prStatus = prFacts("https://example.com/pull/1")
        })
        #expect(pr.section == .pr && pr.rule == .prOpen)
        // seen, it is no longer open work: every count reads it the same way
        #expect(TodoTriageLogic.bucket(todo("t", agents: ["unseen"]) { $0.seenAt = ago(0.5) }, lookup: lookup, now: now) == .closed)
        #expect(TodoTriageLogic.bucket(todo("t", agents: ["unseen"]), lookup: lookup, now: now) == .open)
    }

    @Test func unfinishedSectionsComeFirstAndOpenShelvedAndDoneFoldedDoneLast() {
        #expect(TodoSection.allCases == [.waiting, .working, .pr, .other, .justDone, .shelved, .done, .cancelled])
        #expect(TodoSection.allCases.filter(\.startsFolded) == [.shelved, .done, .cancelled])
    }

    @Test func unfinishedTodosLeadARowAndComeBeforeJustFinishedSeenOnesAreDone() {
        let look = TodosLogic.makeSessionLookup([session("unseen", state: .unread), session("unseen2", state: .unread),
                                                 session("idle", state: .idle, updatedHoursAgo: 2)])
        let list = [
            todo("fix", title: "Fix #512 crash on launch", agents: ["unseen"]),
            todo("test", title: "#512 crash: add a regression test", updated: 2, agents: ["idle"]),
            todo("notes", title: "Write the release notes", updated: 3, done: 3) { $0.seenAt = ago(2.5) },
            todo("speed", title: "Speed up the web page", agents: ["unseen2"]) { $0.seenAt = ago(0.5) },
            todo("logs", title: "Rotate the logs", updated: 3, done: 3),
        ]
        let r = TodoTriageLogic.triageTodos(list, lookup: look, now: now)
        #expect(r.groups(.other).map { $0.members.map(\.id) } == [["test", "fix"]])
        #expect(r.groups(.justDone).isEmpty)
        #expect(r.groups(.done).map(\.lead.id) == ["speed", "logs", "notes"])
        #expect(r.open == 2 && r.shelved == 0 && r.finished == 3)
    }

    @Test func freshOpenPRIsPRToMergeHoweverOldMergedIsNot() {
        #expect(at(todo("t", updated: 300, agents: ["old"]) {
            $0.delivery = .init(state: "pr")
            $0.prURL = "https://example.com/pull/1"
            $0.prStatus = prFacts("https://example.com/pull/1")
        }).section == .pr)
        #expect(at(todo("t", updated: 300, agents: ["old"]) {
            $0.prURL = "https://example.com/pull/1"
            $0.prStatus = prFacts("https://example.com/pull/1")
        }).section == .pr)
        #expect(at(todo("t", updated: 300, agents: ["old"]) {
            $0.prURL = "https://example.com/pull/1"
            $0.prStatus = prFacts("https://example.com/pull/1", state: "merged")
            $0.delivery = .init(state: "merged")
        }).section == .shelved)
    }

    @Test func onlyFreshMatchingOpenNonDraftFactsFormPROpen() {
        let url = "https://example.com/pull/1"
        #expect(at(todo("open", agents: ["idle"]) {
            $0.prURL = url
            $0.prStatus = prFacts(url)
        }).section == .pr)

        // Absent, stale, draft, mismatched, merged and future facts never stand in for an open PR.
        let negative: [(String, (inout TodoItem) -> Void)] = [
            ("absent", { $0.prURL = url }),
            ("stale", { $0.prURL = url; $0.prStatus = prFacts(url, checkedHoursAgo: 0.5) }),
            ("draft", { $0.prURL = url; $0.prStatus = prFacts(url, draft: true) }),
            ("mismatch", { $0.prURL = url; $0.prStatus = prFacts("https://example.com/pull/2") }),
            ("merged", { $0.prURL = url; $0.prStatus = prFacts(url, state: "merged") }),
            ("future", { $0.prURL = url; $0.prStatus = prFacts(url, checkedHoursAgo: -1) }),
        ]
        for (name, build) in negative {
            let result = at(todo(name, agents: ["idle"], build: build))
            #expect(result.section == .other && result.rule == .rest, "\(name) must not form pr_open")
        }

        let earlier = at(todo("earlier", agents: ["idle"]) {
            $0.prURL = url
            $0.prStatus = prFacts(url, state: "merged")
            $0.otherPRs = [prFacts("https://example.com/pull/2")]
        })
        #expect(earlier.section == .pr && earlier.rule == .prOpen)
        let allMerged = at(todo("all-merged", agents: ["idle"]) {
            $0.prURL = url
            $0.prStatus = prFacts(url, state: "merged")
            $0.otherPRs = [prFacts("https://example.com/pull/2", state: "merged")]
        })
        #expect(allMerged.section == .other && allMerged.rule == .rest)
    }

    @Test func noMoveFor48HoursIsShelvedANewerAgentMoveKeepsIt() {
        let t = at(todo("t", updated: 100, agents: ["old"]))
        #expect(t.section == .shelved && t.rule == .stale)
        #expect(at(todo("t", updated: 100, agents: ["idle"])).section == .other)
    }

    /// #3331: the assistant's Progress entry counts by the same bucket as the To-dos page's "Not done".
    @Test func bucketMatchesTheHeaderCountShelvedAndAcceptedAreNotOpen() {
        let stale = todo("stale", updated: 100, agents: ["old"])
        let accepted = todo("accepted", agents: ["idle"]) { $0.reopenedAt = ago(4); $0.supervision = acceptedReopened() }
        let open = todo("open", agents: ["idle"])
        let done = todo("done", done: 3)
        #expect(TodoTriageLogic.bucket(stale, lookup: lookup, now: now) == .shelved)
        #expect(TodoTriageLogic.bucket(stale, lookup: lookup, now: now, restoredAt: ago(1)) == .open)
        #expect(TodoTriageLogic.bucket(accepted, lookup: lookup, now: now) == .closed)
        #expect(TodoTriageLogic.bucket(open, lookup: lookup, now: now) == .open)
        #expect(TodoTriageLogic.bucket(done, lookup: lookup, now: now) == .closed)
        let all = [stale, accepted, open, done]
        let header = TodoTriageLogic.triageTodos(all, lookup: lookup, now: now, restored: [:])
        // the action bar's two entries count the same open to-dos, the shelved one folded and not counted (#3387)
        let entries = AssistantEntries.split(conversation: "conv", main: "conv", todos: all, sessions: sessions, interactions: [],
                                             plans: [:], feed: [], now: now)
        let tray = ActionTrayContent(entries)
        // (its other rows are the sessions here that belong to no task: Agents opened by hand)
        let tasks = (tray.needsYou + tray.running).map(\.id).filter { $0.hasPrefix("task:") }
        #expect(tasks.count == header.open)
        #expect(tasks == ["task:open"])
        #expect(entries.shelved.map(\.id) == ["task:stale"])
        #expect(!(tray.needsYou + tray.running).contains { $0.id == "task:accepted" || $0.id == "task:done" })
    }

    @Test func cancelledIsNeitherOpenNorFinished() {
        let cancelled = todo("cancelled") { $0.cancelledAt = ago(1) }
        let alone = TodoTriageLogic.triageTodos([cancelled], lookup: lookup, now: now)
        #expect(alone.open == 0 && alone.cancelled == 1 && alone.finished == 0)
        let controls = TodoTriageLogic.triageTodos([cancelled, todo("open"), todo("done", done: 2)], lookup: lookup, now: now)
        #expect(controls.open == 1 && controls.cancelled == 1 && controls.finished == 1)
    }

    @Test func ownTodoWithoutAgentsOrDueAheadNeverShelves() {
        #expect(at(todo("t", updated: 100, origin: nil)).section == .other)
        #expect(at(todo("t", updated: 100, agents: ["old"]) { $0.dueDate = "2026-12-01" }).section == .other)
    }

    @Test func restoreCountsAsAMove() {
        let td = todo("t", updated: 100, agents: ["old"])
        #expect(TodoTriageLogic.triage(td, lookup: lookup, now: now, restoredAt: ago(1)).section == .other)
        #expect(TodoTriageLogic.triage(td, lookup: lookup, now: now,
                                       restoredAt: now.addingTimeInterval(-TodoTriageLogic.shelveAfter - 1)).section == .shelved)
    }

    @Test func lastActivityTakesTheLatestOfTodoSupervisionAndAgents() {
        let td = todo("t", updated: 50, agents: ["idle"]) {
            $0.supervision = .init(supervisor: "c", progress: .init(kind: "progress", text: "x", at: ago(1)))
        }
        #expect(TodoTriageLogic.lastActivity(td, lookup: lookup) == ago(1))
    }

    @Test func criteriaAddedAfterAStandingAcceptanceAreUncheckedAndNotCounted() {
        let sup = acceptedReopened()
        #expect(sup.checklist.map(\.mark) == [.met, .met, .met, .met, .met, .unchecked, .unchecked, .unchecked])
        #expect(sup.checkCount == "5/5")
    }

    @Test func beforeAnyAcceptanceCriteriaStayOpenAndCount() {
        var sup = acceptedReopened()
        sup.state = "rework"
        sup.reviews = [.init(at: ago(5), verdict: "rework", checks: [.init(criterion: "c1", met: true)])]
        #expect(sup.checklist.filter { $0.mark == .open }.count == 7)
        #expect(sup.checkCount == "1/8")
    }

    @Test func mergesSameIssuePRTitleOrPlanLedByTheMostUrgent() {
        let list = [
            todo("fix", title: "Fix #412 lost pushes", agents: ["run"]),
            todo("test", title: "#412 lost pushes: regression test", updated: 100, agents: ["old"]),
            todo("pr-a", title: "A", updated: 120, agents: ["old2"]) {
                $0.prURL = "https://example.com/pull/9"
                $0.prStatus = prFacts("https://example.com/pull/9")
            },
            todo("pr-b", title: "B", updated: 121, agents: ["old2"]) {
                $0.prURL = "https://example.com/pull/9/"
                $0.prStatus = prFacts("https://example.com/pull/9")
            },
            todo("lone", title: "Something else"),
        ]
        let r = TodoTriageLogic.triageTodos(list, lookup: lookup, now: now)
        #expect(r.groups(.working).map { [$0.lead.id] + $0.related.map(\.id) } == [["fix", "test"]])
        #expect(r.groups(.pr).map { $0.members.map(\.id) } == [["pr-a", "pr-b"]])
        #expect(r.groups(.other).map(\.lead.id) == ["lone"])
    }

    @Test func neverMergesAClosedTodoIntoAnOpenRow() {
        let r = TodoTriageLogic.triageTodos([todo("a", title: "Same title"), todo("b", title: "Same title", done: 1)], lookup: lookup, now: now)
        #expect(r.groups(.other).count == 1)
        #expect(r.groups(.justDone).isEmpty)
        #expect(r.groups(.done).count == 1)
    }

    /// The same case as `failed_start_superseded_by_finished_work` in spec/fixtures/triage/groups.json (#3713): a failed
    /// start whose row's work really finished goes with it to Just finished, behind it, not counted open; failed starts
    /// with nothing finished beside them still wait on the owner. The checkbox reads them so.
    @Test func failedStartSupersededByFinishedWorkLeavesWaitingOnYou() {
        let failed = todo("f1", title: "Deploy the self-hosted panel", done: 1) { $0.planID = "p1"; $0.startError = "no launcher" }
        let done = todo("done1", title: "Panel: verify it on the hub", done: 2) { $0.planID = "p1"; $0.doneBy = "review" }
        let other = todo("f2", title: "Rotate the backup keys", done: 3) { $0.planID = "p2"; $0.startError = "no launcher" }
        let twin = todo("f3", title: "Rotate the backup keys", done: 4) { $0.startError = "no launcher" }
        let list = [failed, done, other, twin]
        #expect(TodoTriageLogic.supersededFailedStarts(list) == ["f1"])
        let r = TodoTriageLogic.triageTodos(list, lookup: lookup, now: now)
        #expect(r.groups(.justDone).isEmpty)
        #expect(r.groups(.done).map { $0.members.map(\.id) } == [["done1", "f1"]])
        #expect(r.groups(.waiting).map { $0.members.map(\.id) } == [["f2", "f3"]])
        #expect(r.open == 2 && r.finished == 2)
        #expect(TodoTriageLogic.bucket(failed, lookup: lookup, now: now, superseded: true) == .closed)
        #expect(TodoTriageLogic.bucket(failed, lookup: lookup, now: now) == .open)
        // the checkbox: open while it waits on the owner, done once superseded
        #expect(TodoTriageLogic.checksAsOpen(other, r.groups(.waiting)[0].triage["f2"]))
        #expect(!TodoTriageLogic.checksAsOpen(failed, r.groups(.done)[0].triage["f1"]))
        #expect(!TodoTriageLogic.checksAsOpen(done, r.groups(.done)[0].triage["done1"]))
    }

    /// A checkbox outside the list (a meeting's action item, #6563) reads a to-do as the list does: a failed start still
    /// waiting on the owner is open, so a tick closes it rather than reopening it; superseded, or simply closed, it is
    /// done. The other to-dos and the sessions are read only for a closed failed start.
    @Test func aCheckboxOutsideTheListReadsAFailedStartAsTheListDoes() {
        let failed = todo("f1", title: "Deploy the self-hosted panel", done: 1) { $0.planID = "p1"; $0.startError = "no launcher" }
        let finished = todo("done1", title: "Panel: verify it on the hub", done: 2) { $0.planID = "p1"; $0.doneBy = "review" }
        let lone = todo("f2", title: "Rotate the backup keys", done: 3) { $0.startError = "no launcher" }
        let open = todo("o1"), done = todo("d1", done: 1)
        var reads = 0
        func list(_ todos: [TodoItem]) -> () -> [TodoItem] { { reads += 1; return todos } }
        func opened(_ t: TodoItem, _ all: [TodoItem]) -> Bool {
            TodoTriageLogic.checksAsOpen(t, among: list(all), lookup: { lookup }, now: now)
        }
        #expect(opened(lone, [lone]), "a failed start waiting on the owner reads open")
        #expect(opened(lone, [lone]) == TodoTriageLogic.checksAsOpen(lone, at(lone)))
        #expect(!opened(failed, [failed, finished]), "superseded by its finished work, it is done")
        #expect(opened(failed, [failed]))
        reads = 0
        #expect(opened(open, [open]) && !opened(done, [done]) && !opened(finished, [finished]))
        #expect(reads == 0, "the other to-dos were read for a to-do that is not a closed failed start")
    }

    @Test func failedStartWaitingOnTheOwnerCountsAsOpen() {
        let r = TodoTriageLogic.triageTodos([todo("f", done: 1) { $0.startError = "no launcher" }], lookup: lookup, now: now)
        #expect(r.groups(.waiting).map(\.lead.id) == ["f"])
        #expect(r.open == 1)
    }

    @Test func quietTasksListedOnThePageLeftOutOtherwise() {
        let list = [todo("q") { $0.quiet = true }]
        #expect(TodoTriageLogic.triageTodos(list, lookup: lookup, now: now).open == 1)
        #expect(TodoTriageLogic.triageTodos(list, lookup: lookup, now: now, quiet: false).open == 0)
    }

    @Test func countsShelvedAndFinishedApartFromOpen() {
        let r = TodoTriageLogic.triageTodos([
            todo("run", agents: ["run"]),
            todo("stale", updated: 100, agents: ["old"]),
            todo("closed", done: 2),
            todo("accepted", agents: ["old"]) { $0.supervision = acceptedReopened() },
            todo("quiet") { $0.quiet = true },
        ], lookup: lookup, now: now, quiet: false)
        // Each its own: the array literal against the three counts no longer type-checks (ambiguous `==`, Xcode 27.1).
        #expect(r.open == 1)
        #expect(r.shelved == 1)
        #expect(r.finished == 2)
    }

    @Test func relatedKeysIgnoreCaseSpacesPunctuationAndATrailingSlash() {
        let t = todo("a", title: "Fix: the  Widget!") { $0.prURL = "https://Example.com/pull/3/" }
        #expect(TodoTriageLogic.relatedKeys(t) == ["pr:https://example.com/pull/3", "title:fixthewidget"])
    }

    @Test func restoresKeptOnThisDeviceDroppingOnesThatWouldHaveShelvedAgain() {
        let defaults = UserDefaults(suiteName: "TodoTriageTests")!
        defaults.removePersistentDomain(forName: "TodoTriageTests")
        TodoRestores.restore(["old"], defaults, now: now.addingTimeInterval(-TodoTriageLogic.shelveAfter - 1))
        TodoRestores.restore(["new"], defaults, now: now)
        #expect(TodoRestores.load(defaults, now: now) == ["new": now])
    }

    @Test func decodesDeliveryAndReopenedAt() throws {
        let json = #"{"id":"t","title":"x","reopened_at":"2026-11-10T08:00:00Z","delivery":{"state":"pr","gate":"review","at":"2026-11-10T09:00:00Z"}}"#
        let t = try JSON.decoder.decode(TodoItem.self, from: Data(json.utf8))
        #expect(t.delivery?.state == "pr" && t.delivery?.gate == "review" && t.reopenedAt != nil)
        let bad = try JSON.decoder.decode(TodoItem.self, from: Data(#"{"id":"t","title":"x","delivery":"live"}"#.utf8))
        #expect(bad.delivery == nil)
    }

    @Test func decodesCancellationAndLineage() throws {
        let json = #"{"id":"old","title":"Old card","done_at":"2026-11-10T08:00:00Z","done_by":"review","cancelled_at":"2026-11-10T08:00:00Z","cancelled_reason":"No longer needed","lineage":[{"todo_id":"old","title":"Old card","started":"2026-11-09T08:00:00Z","ended":"2026-11-10T08:00:00Z","superseded_by":"new","current":true},{"todo_id":"new","title":"New card","started":"2026-11-10T09:00:00Z","continues":"old"}]}"#
        let t = try JSON.decoder.decode(TodoItem.self, from: Data(json.utf8))
        #expect(t.isCancelled && t.cancellationReason == "No longer needed")
        #expect(t.statusLabel(.done) == String(localized: "Canceled"))
        #expect(t.lineage?.count == 2)
        #expect(t.lineage?[0].supersededBy == "new")
        #expect(t.lineage?[1].continues == "old")
        let legacy = try JSON.decoder.decode(TodoItem.self, from: Data(#"{"id":"t","title":"x","done_at":"2026-11-10T08:00:00Z","done_by":"review","done_reason":"Not needed","supervision":{"supervisor":"main","state":"cancelled"}}"#.utf8))
        #expect(legacy.isCancelled && legacy.cancellationReason == "Not needed")
        let old = try JSON.decoder.decode(TodoItem.self, from: Data(#"{"id":"t","title":"x"}"#.utf8))
        #expect(!old.isCancelled && old.lineage == nil)
    }

    @Test func cancelledReopenedAndExplicitlyCompletedIsNotCancelled() throws {
        for json in [
            #"{"id":"reopened-user","done_at":"2026-11-13T08:00:00Z","reopened_at":"2026-11-13T07:00:00Z","supervision":{"supervisor":"main","state":"cancelled"}}"#,
            #"{"id":"reopened-auto","done_at":"2026-11-13T08:00:00Z","done_by":"auto","reopened_at":"2026-11-13T07:00:00Z","supervision":{"supervisor":"main","state":"cancelled"}}"#,
        ] {
            let completed = try JSON.decoder.decode(TodoItem.self, from: Data(json.utf8))
            #expect(!completed.isCancelled)
            #expect(completed.cancellationReason == nil)
        }
    }

    @Test func decodesSeenAtAndABadOneFilesNothing() throws {
        let t = try JSON.decoder.decode(TodoItem.self, from: Data(#"{"id":"t","title":"x","seen_at":"2026-11-10T08:00:00Z"}"#.utf8))
        #expect(t.seenAt == ISO8601DateFormatter().date(from: "2026-11-10T08:00:00Z"))
        // a mark that does not parse reads as the epoch (JSON.decoder): it files nothing
        let bad = try JSON.decoder.decode(TodoItem.self, from: Data(#"{"id":"t","title":"x","seen_at":"soon","supervision":{"supervisor":"main","state":"pending_review"}}"#.utf8))
        #expect(at(bad).section == .justDone)
    }

    @Test func kindDecodesAndSurvivesTheTodoCacheRoundTrip() throws {
        // The unversioned todo-items cache is the same [TodoItem] JSON as the wire: an old entry has no kind.
        let cached = Data(#"[{"id":"old","title":"old"},{"id":"c","title":"c","kind":"concern"},{"id":"new","title":"new","kind":"future"}]"#.utf8)
        let decoded = try JSON.decoder.decode([TodoItem].self, from: cached)
        #expect(decoded.map(\.kind) == [nil, "concern", "future"])
        #expect(decoded.map(\.isConcern) == [false, true, false])
        let roundTrip = try JSON.decoder.decode([TodoItem].self, from: JSON.encoder.encode(decoded))
        #expect(roundTrip.map(\.kind) == decoded.map(\.kind))
        let malformed = try JSON.decoder.decode(TodoItem.self, from: Data(#"{"id":"bad","title":"bad","kind":7}"#.utf8))
        #expect(malformed.kind == nil && !malformed.isConcern)
    }
}

extension TodoTriageTests {
    @Test func r1FailedStartOwnerWaitAndRunningRulesApplyToConcerns() {
        let failed = at(todo("failed", kind: "concern") { $0.startError = "no launcher" })
        #expect(failed.section == .waiting && failed.rule == .failedStart)
        #expect(at(todo("asking", agents: ["ask"], kind: "concern")).rule == .agentAsks)
        #expect(at(todo("decision", kind: "concern") { $0.ask = ask("decide") }).rule == .ownerDecides)
        #expect(at(todo("running", agents: ["run"], kind: "concern")).rule == .agentRunning)

        var starting = session("starting", state: .idle)
        starting.starting = true
        let startingResult = TodoTriageLogic.triage(todo("starting", agents: ["starting"], kind: "concern"),
                                                    lookup: TodosLogic.makeSessionLookup([starting]), now: now)
        #expect(startingResult.section == .working && startingResult.rule == .agentRunning)
    }

    @Test func r2ConcernFailedStartAfter48HoursIsOtherOpen() {
        let concern = todo("failed", updated: 100, kind: "concern") { $0.startError = "no launcher" }
        let result = at(concern)
        #expect(result.section == .other && result.rule == .rest)
        #expect(TodoTriageLogic.bucket(concern, lookup: lookup, now: now) == .open)
    }

    @Test func r3ConcernSkipsAcceptedAndDeployed() {
        let accepted = at(todo("accepted", agents: ["idle"], kind: "concern") { $0.supervision = acceptedReopened() })
        #expect(accepted.section == .other && accepted.rule == .rest)
        let deployed = at(todo("deployed", kind: "concern") { $0.delivery = .init(state: "live", at: ago(2)) })
        #expect(deployed.section == .other && deployed.rule == .rest)
    }

    @Test func liveConcernDoesNotUseOrdinaryDeliveryStatusOrAcceptance() {
        let live = todo("live", kind: "concern") { $0.delivery = .init(state: "live", at: ago(2)) }
        #expect(!live.deliveredAwaitingReview(.notStarted))
        #expect(live.statusLabel(.notStarted) == String(localized: "Not started"))
        #expect(live.shownState(.notStarted) == .notStarted)

        let seen = todo("seen-live", agents: ["unseen"], kind: "concern") {
            $0.delivery = .init(state: "live", at: ago(2))
            $0.seenAt = ago(0.5)
        }
        #expect(at(seen).section == .other && at(seen).rule == .rest)
        #expect(TodoTriageLogic.seenCanFile(seen))
        #expect(!seen.deliveredAwaitingReview(.unread))
    }

    @Test func pendingReviewAndSupervisedConcernsStillRequireAcceptance() {
        let pending = todo("pending", kind: "concern") {
            $0.supervision = .init(supervisor: "main", state: "pending_review")
            $0.seenAt = now
        }
        #expect(pending.deliveredAwaitingReview(.notStarted))
        #expect(pending.statusLabel(.notStarted) == String(localized: "Delivered · Awaiting Review [To-do Sections]"))
        #expect(!TodoTriageLogic.seenCanFile(pending))
        #expect(at(pending).section == .justDone && at(pending).rule == .finishedUnseen)

        let supervised = todo("supervised", agents: ["unseen"], kind: "concern") {
            $0.supervision = .init(supervisor: "main", state: "reviewing")
            $0.seenAt = now
        }
        #expect(!TodoTriageLogic.seenCanFile(supervised))
        #expect(at(supervised).section == .justDone && at(supervised).rule == .finishedUnseen)
    }

    @Test func r4ConcernKeepsFinishedUnseenAndPROpen() {
        let unseen = at(todo("unseen", agents: ["unseen"], kind: "concern"))
        #expect(unseen.section == .justDone && unseen.rule == .finishedUnseen)
        let pr = at(todo("pr", updated: 100, agents: ["old"], kind: "concern") {
            $0.prURL = "https://example.com/pull/3582"
            $0.prStatus = prFacts("https://example.com/pull/3582")
        })
        #expect(pr.section == .pr && pr.rule == .prOpen)
    }

    @Test func r5SeenFinishedConcernStaysOtherOpen() {
        let concern = todo("seen", agents: ["unseen"], kind: "concern") { $0.seenAt = ago(0.5) }
        let result = at(concern)
        #expect(result.section == .other && result.rule == .rest)
        #expect(TodoTriageLogic.bucket(concern, lookup: lookup, now: now) == .open)
    }

    @Test func r6QuietConcernNeverShelves() {
        let concern = todo("quiet", updated: 100, agents: ["old"], kind: "concern")
        #expect(!TodoTriageLogic.shelvable(concern, now: now))
        let result = at(concern)
        #expect(result.section == .other && result.rule == .rest)
        #expect(at(todo("ordinary", updated: 100, agents: ["old"])).section == .shelved)
    }

    @Test func r7OtherwiseOpenConcernIsOtherOpen() {
        let result = at(todo("open", kind: "concern"))
        #expect(result.section == .other && result.rule == .rest)
    }

    @Test func closedConcernFreshIsJustFinished() {
        let result = at(todo("closed", done: 3, kind: "concern"))
        #expect(result.section == .justDone && result.rule == .closed)
    }

    @Test func closedConcernAfter24HoursIsDone() {
        let result = at(todo("closed", updated: 30, done: 30, kind: "concern"))
        #expect(result.section == .done && result.rule == .closed)
    }

    @Test func openConcernAndRelatedOpenTaskHaveTwoRows() {
        let result = TodoTriageLogic.triageTodos([
            todo("concern", kind: "concern") { $0.planID = "shared" },
            todo("task") { $0.planID = "shared" },
        ], lookup: lookup, now: now)
        #expect(result.groups(.other).map { $0.members.map(\.id) } == [["concern"], ["task"]])
        #expect(result.open == 2)
    }

    @Test func twoRelatedOpenConcernsHaveTwoRows() {
        let result = TodoTriageLogic.triageTodos([
            todo("first", kind: "concern") { $0.planID = "shared" },
            todo("second", kind: "concern") { $0.planID = "shared" },
        ], lookup: lookup, now: now)
        #expect(result.groups(.other).map { $0.members.map(\.id) } == [["first"], ["second"]])
        #expect(result.open == 2)
    }

    @Test func openConcernCannotJoinARowTransitively() {
        let result = TodoTriageLogic.triageTodos([
            // Only the concern shares the issue with one task and the plan with the other: it must not bridge their rows.
            todo("concern", title: "Investigate #3582", kind: "concern") { $0.planID = "shared" },
            todo("task", title: "Fix #3582"),
            todo("followup") { $0.planID = "shared" },
        ], lookup: lookup, now: now)
        let rows = Set(result.groups(.other).map { Set($0.members.map(\.id)) })
        #expect(rows == Set([Set(["concern"]), Set(["task"]), Set(["followup"])]))
        #expect(result.open == 3)
    }

    @Test func closedConcernGroupsWithRelatedClosedTodo() {
        let result = TodoTriageLogic.triageTodos([
            todo("concern", done: 1, kind: "concern") { $0.planID = "shared" },
            todo("task", done: 2) { $0.planID = "shared" },
        ], lookup: lookup, now: now)
        #expect(result.groups(.justDone).map { $0.members.map(\.id) } == [["concern", "task"]])
        #expect(result.finished == 2)
    }

    @Test func unknownKindBehavesAsAnOrdinaryTodo() {
        #expect(at(todo("future", updated: 100, agents: ["old"], kind: "future")).section == .shelved)
    }

    @Test func issueNumberMatchesTheWebPattern() {
        #expect(TodoTriageLogic.issueNumber("Fix #412 lost pushes") == "412")
        #expect(TodoTriageLogic.issueNumber("#7 too short, #12345678 too long, then #99") == "99")
        #expect(TodoTriageLogic.issueNumber("#123abc") == nil)
        #expect(TodoTriageLogic.issueNumber("修复 #3206。") == "3206")
    }
}

/// Native counterparts of the concern cases added to spec/fixtures/triage on main (#3582). Apple tests do not load those
/// fixture files, so each test below carries the case's exact JSON and names it; the coordinator's shared fixtures stay canonical.
extension TodoTriageTests {
    private func sharedTodo(_ json: String) throws -> TodoItem {
        try JSON.decoder.decode(TodoItem.self, from: Data(json.utf8))
    }

    private func sharedLookup(_ sessions: [String]) throws -> TodosLogic.SessionLookup {
        TodosLogic.makeSessionLookup(try sessions.map { try JSON.decoder.decode(Session.self, from: Data($0.utf8)) })
    }

    private func sharedAt(_ todo: String, sessions: [String] = []) throws -> TodoTriage {
        TodoTriageLogic.triage(try sharedTodo(todo), lookup: try sharedLookup(sessions), now: now)
    }

    private func sharedGroups(_ todos: [String], sessions: [String] = []) throws -> TodoTriageResult {
        TodoTriageLogic.triageTodos(try todos.map(sharedTodo), lookup: try sharedLookup(sessions), now: now)
    }

    /// Shared case `concern_agent_running`: Working / agent_running.
    @Test func sharedConcernAgentRunning() throws {
        let t = try sharedAt(#"{"id":"concern-run","title":"Answer the backup question","kind":"concern","created_at":"2026-10-01T00:00:00.000Z","updated_at":"2026-10-20T00:00:00.000Z","done_at":null,"agents":[{"machine_id":"m","session_id":"run","linked_at":"2026-11-10T10:00:00.000Z"}],"origin":"main"}"#,
                             sessions: [#"{"machine_id":"m","session_id":"run","state":"working","status":"working","updated_at":"2026-11-10T11:00:00.000Z"}"#])
        #expect(t.section == .working && t.rule == .agentRunning)
    }

    /// Shared case `concern_failed_start_old`: Other open / rest, not Done.
    @Test func sharedConcernFailedStartOld() throws {
        let t = try sharedAt(#"{"id":"concern-failed","title":"Answer the launch question","kind":"concern","created_at":"2026-11-01T00:00:00.000Z","updated_at":"2026-11-08T00:00:00.000Z","done_at":null,"agents":[],"origin":"main","start_error":"no launcher"}"#)
        #expect(t.section == .other && t.rule == .rest)
    }

    /// Shared case `concern_accepted`: Other open / rest, with open status words, tone and checkbox.
    @Test func sharedConcernAccepted() throws {
        let json = #"{"id":"concern-accepted","title":"Answer the review question","kind":"concern","created_at":"2026-11-09T00:00:00.000Z","updated_at":"2026-11-10T10:00:00.000Z","done_at":null,"agents":[],"origin":"main","supervision":{"supervisor":"main","done_when":[],"state":"accepted","reviews":[{"at":"2026-11-10T09:00:00.000Z","verdict":"accepted","checks":[]}]}}"#
        let td = try sharedTodo(json)
        let t = try sharedAt(json)
        #expect(t.section == .other && t.rule == .rest)
        let status = TodosLogic.todoStatus(td, lookup: try sharedLookup([]))
        #expect(status == .notStarted && !td.acceptedAtRest(status))
        #expect(td.shownState(status) == .notStarted && td.statusLabel(status) == "未开始")
        #expect(TodoTriageLogic.checksAsOpen(td, t))
    }

    @Test(arguments: ["decide", "cannot"])
    func acceptedOwnerDecisionDoesNotReadAsAccepted(outcome: String) throws {
        let json = """
        {"id":"accepted-decision","title":"Choose the rollout","created_at":"2026-11-09T00:00:00.000Z",
         "updated_at":"2026-11-10T10:00:00.000Z","done_at":null,"agents":[],"origin":"main",
         "supervision":{"supervisor":"main","done_when":[],"state":"accepted",
           "reviews":[{"at":"2026-11-10T09:00:00.000Z","verdict":"accepted","checks":[]}]},
         "ask":{"outcome":"\(outcome)","verdict":"Choose the rollout","asked_at":"2026-11-10T10:00:00.000Z"}}
        """
        let td = try sharedTodo(json)
        let t = try sharedAt(json)
        let status: TaskState = .followup
        #expect(t.section == .waiting && t.rule == .ownerDecides)
        #expect(td.acceptanceStands)
        #expect(td.waitsForOwner)
        #expect(!td.acceptedAtRest(status))
        #expect(td.shownState(status) == .needsYou)
        #expect(td.statusLabel(status) == StateName.needsYou.text)
    }

    @Test(arguments: ["decide", "cannot"])
    func acceptedDismissedOwnerDecisionReadsAccepted(outcome: String) throws {
        let json = """
        {"id":"accepted-dismissed-decision","title":"Choose the rollout","created_at":"2026-11-09T00:00:00.000Z",
         "updated_at":"2026-11-10T10:00:00.000Z","done_at":null,"agents":[],"origin":"main",
         "supervision":{"supervisor":"main","done_when":[],"state":"accepted",
           "reviews":[{"at":"2026-11-10T09:00:00.000Z","verdict":"accepted","checks":[]}]},
         "decision_review":{"review_at":"2026-11-10T09:00:00.000Z","dismissal_id":"dismiss-current-round"},
         "ask":{"outcome":"\(outcome)","verdict":"Choose the rollout","asked_at":"2026-11-10T10:00:00.000Z"}}
        """
        let td = try sharedTodo(json)
        let t = try sharedAt(json)
        let status: TaskState = .followup
        #expect(t.section == .done && t.rule == .accepted)
        #expect(td.decisionDismissed)
        #expect(!td.waitsForOwner)
        #expect(td.acceptedAtRest(status))
        #expect(td.shownState(status) == .done)
        #expect(td.statusLabel(status) == String(localized: "Accepted"))
    }

    @Test func oldDismissalDoesNotHideANewerNeedsUserRound() throws {
        let json = """
        {"id":"newer-needs-user","title":"Choose the rollout again","created_at":"2026-11-09T00:00:00.000Z",
         "updated_at":"2026-11-10T10:00:00.000Z","done_at":null,"agents":[],"origin":"main",
         "supervision":{"supervisor":"main","done_when":[],"state":"needs_user",
           "reviews":[{"at":"2026-11-10T10:00:00.000Z","verdict":"needs_user","checks":[]}]},
         "decision_review":{"review_at":"2026-11-10T09:00:00.000Z","dismissal_id":"dismiss-old-round"}}
        """
        let td = try sharedTodo(json)
        let t = try sharedAt(json)
        #expect(t.section == .waiting && t.rule == .ownerDecides)
        #expect(td.waitsForOwner)
        #expect(td.shownState(.followup) == .needsYou)
    }

    @Test(arguments: ["decide", "cannot"])
    func oldDismissalDoesNotHideANewerAcceptedDecision(outcome: String) throws {
        let json = """
        {"id":"newer-accepted-decision","title":"Choose the rollout again","created_at":"2026-11-09T00:00:00.000Z",
         "updated_at":"2026-11-10T10:00:00.000Z","done_at":null,"agents":[],"origin":"main",
         "supervision":{"supervisor":"main","done_when":[],"state":"accepted",
           "reviews":[{"at":"2026-11-10T09:00:00.000Z","verdict":"accepted","checks":[]}]},
         "decision_review":{"review_at":"2026-11-10T08:00:00.000Z","dismissal_id":"dismiss-old-round"},
         "ask":{"outcome":"\(outcome)","verdict":"Choose the rollout again","asked_at":"2026-11-10T10:00:00.000Z"}}
        """
        let td = try sharedTodo(json)
        let t = try sharedAt(json)
        let status: TaskState = .followup
        #expect(t.section == .waiting && t.rule == .ownerDecides)
        #expect(!td.acceptedAtRest(status))
        #expect(td.shownState(status) == .needsYou)
        #expect(td.statusLabel(status) == StateName.needsYou.text)
    }

    /// Shared case `owner_decides_dismissed_with_open_pr`: a same-round dismissal still yields to the open-PR rule.
    @Test func dismissedOwnerDecisionYieldsToOpenPR() throws {
        let json = """
        {"id":"t","title":"t","created_at":"2026-10-29T00:00:00.000Z",
         "updated_at":"2026-11-10T11:00:00.000Z","done_at":null,
         "agents":[{"machine_id":"m","session_id":"idle","linked_at":"2026-11-02T04:00:00.000Z"}],"origin":"conv",
         "supervision":{"supervisor":"c","state":"needs_user",
           "reviews":[{"at":"2026-11-10T09:00:00.000Z","verdict":"needs_user"}]},
         "decision_review":{"review_at":"2026-11-10T09:00:00.000Z","dismissal_id":"dismiss_example"},
         "pr_url":"https://github.com/example/project/pull/42",
         "pr_status":{"url":"https://github.com/example/project/pull/42","state":"open","draft":false,"checked_at":"2026-11-10T11:59:00Z"}}
        """
        let t = try sharedAt(json)
        let td = try sharedTodo(json)
        #expect(t.section == .pr && t.rule == .prOpen)
        #expect(td.decisionDismissed)
        #expect(!td.waitsForOwner)
        #expect(td.shownState(.followup) == .followup)
        #expect(td.shownState(.working) == .working)
        #expect(td.statusLabel(.followup) == TaskState.followup.todoLabel)
        #expect(AssistantEntries.rank(td, .followUp) == AssistantEntries.Status.followUp.rank)
        var undismissed = td
        undismissed.decisionReview = nil
        #expect(undismissed.waitsForOwner)
        #expect(undismissed.shownState(.followup) == .needsYou)
    }

    @Test func dismissedReviewDecisionKeepsIndependentDeliveryApproval() {
        let dismissed = todo("t", agents: ["idle"]) {
            $0.supervision = .init(supervisor: "c", state: "needs_user",
                                   reviews: [.init(at: ago(1), verdict: "needs_user")])
            $0.decisionReview = DecisionReview(reviewAt: ISO8601DateFormatter().string(from: ago(1)), dismissalID: "dismiss-current")
            $0.delivery = .init(state: "pr", gate: "owner", unblock: "user")
        }
        #expect(dismissed.decisionDismissed)
        let t = at(dismissed)
        #expect(t.section == .waiting && t.rule == .ownerDecides)
        var withoutDelivery = dismissed
        withoutDelivery.delivery = nil
        #expect(at(withoutDelivery).rule == .rest)
    }

    @Test func missingPRStateDoesNotWaitForMerge() throws {
        let url = "https://example.com/pull/9"
        for payload in [
            #"{"url":"https://example.com/pull/9","checked_at":"2026-11-10T12:00:00Z"}"#,
            #"{"url":"https://example.com/pull/9","state":null,"checked_at":"2026-11-10T12:00:00Z"}"#,
        ] {
            let facts = try JSON.decoder.decode(TodoPRStatus.self, from: Data(payload.utf8))
            #expect(facts.state.isEmpty)
            let t = todo("missing-state", agents: ["idle"]) { $0.prURL = url; $0.prStatus = facts }
            #expect(at(t).section != .pr)
        }
        let open = try JSON.decoder.decode(TodoPRStatus.self, from: Data(#"{"url":"https://example.com/pull/9","state":"open","checked_at":"2026-11-10T12:00:00Z"}"#.utf8))
        #expect(open.state == "open")
        let t = todo("open-state", agents: ["idle"]) { $0.prURL = url; $0.prStatus = open }
        #expect(at(t).section == .pr)
    }

    /// Shared case `concern_deployed`: Other open / rest.
    @Test func sharedConcernDeployed() throws {        let t = try sharedAt(#"{"id":"concern-deployed","title":"Answer the deployment question","kind":"concern","created_at":"2026-11-09T00:00:00.000Z","updated_at":"2026-11-10T10:00:00.000Z","done_at":null,"agents":[],"origin":"main","delivery":{"state":"live","at":"2026-11-10T09:00:00.000Z"}}"#)
        #expect(t.section == .other && t.rule == .rest)
    }

    /// Shared case `concern_finished_unseen`: Just finished / finished_unseen.
    @Test func sharedConcernFinishedUnseen() throws {
        let t = try sharedAt(#"{"id":"concern-unseen","title":"Answer the report question","kind":"concern","created_at":"2026-11-09T00:00:00.000Z","updated_at":"2026-11-10T10:00:00.000Z","done_at":null,"agents":[{"machine_id":"m","session_id":"unseen","linked_at":"2026-11-10T09:00:00.000Z"}],"origin":"main"}"#,
                             sessions: [#"{"machine_id":"m","session_id":"unseen","state":"unread","status":"idle","updated_at":"2026-11-10T11:00:00.000Z"}"#])
        #expect(t.section == .justDone && t.rule == .finishedUnseen)
    }

    /// Shared case `concern_pr_open`: PR to merge / pr_open.
    @Test func sharedConcernPROpen() throws {
        let t = try sharedAt(#"{"id":"concern-pr","title":"Answer the merge question","kind":"concern","created_at":"2026-11-01T00:00:00.000Z","updated_at":"2026-11-04T10:00:00.000Z","done_at":null,"agents":[],"origin":"main","pr_url":"https://example.com/pull/7","pr_status":{"url":"https://example.com/pull/7","state":"open","draft":false,"review":"pending","checks":"none","conflicts":false,"checked_at":"2026-11-10T12:00:00Z"},"stage":"review"}"#)
        #expect(t.section == .pr && t.rule == .prOpen)
    }

    /// Shared case `concern_finished_seen`: Other open / rest, not Done.
    @Test func sharedConcernFinishedSeen() throws {
        let t = try sharedAt(#"{"id":"concern-seen","title":"Answer the seen question","kind":"concern","created_at":"2026-11-09T00:00:00.000Z","updated_at":"2026-11-10T10:00:00.000Z","done_at":null,"agents":[{"machine_id":"m","session_id":"unseen","linked_at":"2026-11-10T09:00:00.000Z"}],"origin":"main","seen_at":"2026-11-10T11:30:00.000Z"}"#,
                             sessions: [#"{"machine_id":"m","session_id":"unseen","state":"unread","status":"idle","updated_at":"2026-11-10T11:00:00.000Z"}"#])
        #expect(t.section == .other && t.rule == .rest)
    }

    /// Shared case `concern_stale`: Other open / rest, never Shelved.
    @Test func sharedConcernStale() throws {
        let t = try sharedAt(#"{"id":"concern-stale","title":"Answer the quiet question","kind":"concern","created_at":"2026-10-01T00:00:00.000Z","updated_at":"2026-10-20T12:00:00.000Z","done_at":null,"agents":[{"machine_id":"m","session_id":"old","linked_at":"2026-10-02T00:00:00.000Z"}],"origin":"main"}"#,
                             sessions: [#"{"machine_id":"m","session_id":"old","state":"idle","status":"idle","updated_at":"2026-10-20T12:00:00.000Z"}"#])
        #expect(t.section == .other && t.rule == .rest)
    }

    /// Shared case `concern_stale_ordinary_task_control`: Shelved / stale.
    @Test func sharedConcernStaleOrdinaryTaskControl() throws {
        let t = try sharedAt(#"{"id":"task-stale-control","title":"Answer the quiet question","created_at":"2026-10-01T00:00:00.000Z","updated_at":"2026-10-20T12:00:00.000Z","done_at":null,"agents":[{"machine_id":"m","session_id":"old","linked_at":"2026-10-02T00:00:00.000Z"}],"origin":"main"}"#,
                             sessions: [#"{"machine_id":"m","session_id":"old","state":"idle","status":"idle","updated_at":"2026-10-20T12:00:00.000Z"}"#])
        #expect(t.section == .shelved && t.rule == .stale)
    }

    /// Shared case `concern_rest`: Other open / rest.
    @Test func sharedConcernRest() throws {
        let t = try sharedAt(#"{"id":"concern-rest","title":"Answer the ordinary question","kind":"concern","created_at":"2026-11-09T00:00:00.000Z","updated_at":"2026-11-10T11:00:00.000Z","done_at":null,"agents":[],"origin":"main"}"#)
        #expect(t.section == .other && t.rule == .rest)
    }

    /// Shared case `concern_closed_fresh`: Just finished / closed.
    @Test func sharedConcernClosedFresh() throws {
        let t = try sharedAt(#"{"id":"concern-closed-fresh","title":"Answer the fresh report question","kind":"concern","created_at":"2026-11-09T00:00:00.000Z","updated_at":"2026-11-10T10:00:00.000Z","done_at":"2026-11-10T10:00:00.000Z","done_by":"reported","agents":[],"origin":"main"}"#)
        #expect(t.section == .justDone && t.rule == .closed)
    }

    /// Shared case `concern_closed_old`: Done / closed.
    @Test func sharedConcernClosedOld() throws {
        let t = try sharedAt(#"{"id":"concern-closed-old","title":"Answer the old report question","kind":"concern","created_at":"2026-11-01T00:00:00.000Z","updated_at":"2026-11-08T10:00:00.000Z","done_at":"2026-11-08T10:00:00.000Z","done_by":"reported","agents":[],"origin":"main"}"#)
        #expect(t.section == .done && t.rule == .closed)
    }

    /// Shared case `concern_closed_failed_start_old`: a closed concern keeps the ordinary old failed-start result.
    @Test func sharedConcernClosedFailedStartOld() throws {
        let t = try sharedAt(#"{"id":"concern-closed-failed","title":"Answer the closed launch question","kind":"concern","created_at":"2026-11-01T00:00:00.000Z","updated_at":"2026-11-08T00:00:00.000Z","done_at":"2026-11-08T00:00:00.000Z","done_by":"reported","agents":[],"origin":"main","start_error":"no launcher"}"#)
        #expect(t.section == .done && t.rule == .failedStart)
    }

    /// Shared case `concern_open_related_task`: two open rows and two open to-dos.
    @Test func sharedConcernOpenRelatedTask() throws {
        let r = try sharedGroups([
            #"{"id":"concern-open","title":"Answer the launch concern","kind":"concern","created_at":"2026-11-09T00:00:00.000Z","updated_at":"2026-11-10T11:00:00.000Z","done_at":null,"agents":[],"origin":"main","plan_id":"plan-concern"}"#,
            #"{"id":"task-open","title":"Implement the launch task","created_at":"2026-11-09T00:00:00.000Z","updated_at":"2026-11-10T10:00:00.000Z","done_at":null,"agents":[],"origin":"main","plan_id":"plan-concern"}"#,
        ])
        #expect(r.groups(.other).map { $0.members.map(\.id) } == [["concern-open"], ["task-open"]])
        #expect(r.open == 2 && r.shelved == 0 && r.finished == 0)
    }

    /// Shared case `concern_open_related_concern`: two open rows and two open concerns.
    @Test func sharedConcernOpenRelatedConcern() throws {
        let r = try sharedGroups([
            #"{"id":"concern-first","title":"Answer the first concern","kind":"concern","created_at":"2026-11-09T00:00:00.000Z","updated_at":"2026-11-10T11:00:00.000Z","done_at":null,"agents":[],"origin":"main","plan_id":"plan-two-concerns"}"#,
            #"{"id":"concern-second","title":"Answer the second concern","kind":"concern","created_at":"2026-11-09T00:00:00.000Z","updated_at":"2026-11-10T10:00:00.000Z","done_at":null,"agents":[],"origin":"main","plan_id":"plan-two-concerns"}"#,
        ])
        #expect(r.groups(.other).map { $0.members.map(\.id) } == [["concern-first"], ["concern-second"]])
        #expect(r.open == 2 && r.shelved == 0 && r.finished == 0)
    }

    /// Shared case `concern_closed_related_todo`: one Just finished row and two finished to-dos.
    @Test func sharedConcernClosedRelatedTodo() throws {
        let r = try sharedGroups([
            #"{"id":"concern-closed","title":"Answer the reported concern","kind":"concern","created_at":"2026-11-09T00:00:00.000Z","updated_at":"2026-11-10T11:00:00.000Z","done_at":"2026-11-10T11:00:00.000Z","done_by":"reported","agents":[],"origin":"main","plan_id":"plan-closed-concern"}"#,
            #"{"id":"task-closed","title":"Finish the reported task","created_at":"2026-11-09T00:00:00.000Z","updated_at":"2026-11-10T10:00:00.000Z","done_at":"2026-11-10T10:00:00.000Z","agents":[],"origin":"main","plan_id":"plan-closed-concern"}"#,
        ])
        #expect(r.groups(.justDone).map { $0.members.map(\.id) } == [["concern-closed", "task-closed"]])
        #expect(r.open == 0 && r.shelved == 0 && r.finished == 2)
    }

    /// Shared case `concern_stale_next_to_task`: one Other open row, one Shelved row and per-to-do counts.
    @Test func sharedConcernStaleNextToTask() throws {
        let r = try sharedGroups([
            #"{"id":"stale-concern","title":"Answer the quiet concern","kind":"concern","created_at":"2026-10-20T12:00:00.000Z","updated_at":"2026-10-20T12:00:00.000Z","done_at":null,"agents":[],"origin":"main"}"#,
            #"{"id":"stale-task","title":"Finish the quiet task","created_at":"2026-10-20T12:00:00.000Z","updated_at":"2026-10-20T12:00:00.000Z","done_at":null,"agents":[],"origin":"main"}"#,
        ])
        #expect(r.groups(.other).map { $0.members.map(\.id) } == [["stale-concern"]])
        #expect(r.groups(.shelved).map { $0.members.map(\.id) } == [["stale-task"]])
        #expect(r.open == 1 && r.shelved == 1 && r.finished == 0)
    }
}
