import Foundation
import Testing
@testable import Persona
import PersonaWire

/// #9278: the row's long press and the detail More menu are one ordered inventory,
/// not two lists that happen to look similar in one fixture.
@MainActor struct AgentActionMenuTests {
    private func session(_ state: String = "working") throws -> Session {
        let json = """
        {"machine_id":"m","session_id":"s","title":"Example Agent","cwd":"/work/example",
         "status":"working","state":"\(state)","agent":"codex","source_kind":"codex_history",
         "space_kind":"herdr","space_id":"space","space_name":"Example","pane_id":"w:p",
         "capabilities":{"read":true,"send":false,"interrupt":true,"terminal":true,"attach":true,
                         "takeover":{"running":true,"can_resume":true}},
         "subagents":[{"id":"child","session_id":"s@child","running":false}],
         "loop":{"kind":"wakeup","interval_seconds":60},
         "todo":{"id":"todo","title":"An example to-do"}}
        """ // i18n-ignore: synthetic protocol fixture
        return try JSON.decoder.decode(Session.self, from: Data(json.utf8))
    }

    @Test func bothEntryPointsUseTheSameFullOrderedInventoryIncludingSubmenus() throws {
        var s = try session()
        s.repoRoot = "/work/example"
        let row = AgentActionFacts(session: s, review: true, preview: true, screen: true, todoHub: true,
                                   sideCount: 2, terminalResume: .available, phone: true, git: try gitStatus(files: 3))
        let detail = AgentActionFacts(session: s, review: true, preview: true, screen: true, todoHub: true,
                                      sideCount: 2, terminalResume: .available, phone: true, git: try gitStatus(files: 3))
        #expect(row.items == detail.items)
        let ids: [AgentActionItem.ID] = [
            .git, .files, .review, .preview, .subagents, .sideSessions, .space,
            .attach, .interrupt, .takeover, .continueInNewSession, .rename,
            .pin, .mute, .todo, .moveTodo, .unlinkTodo, .stopLoop, .handToPersona, .screen,
            .copyDirectory, .terminal, .copyResume, .resume,
            .splitPane, .splitRight, .splitDown, .nodeGit, .gitPull, .gitPush, .gitFetch, .closeTab,
            .close,
        ]
        #expect(row.items.map(\.id) == ids)
        #expect(row.items.contains { $0.id == .pin }, "detail has the row's Pin action")
        #expect(row.items.contains { $0.id == .terminal }, "the row has the detail's Terminal action")
    }

    @Test func stateAndCapabilitiesChooseTheSameActionsInBothPlaces() throws {
        var s = try session("unread")
        s.capabilities?.terminal = false
        s.capabilities?.attach = false
        s.loop = nil
        s.todo = nil
        s.subagents = nil
        let facts = AgentActionFacts(session: s, panes: AgentMenuFacts.panes(s, git: nil), phone: true)
        #expect(facts.items.map(\.id) == [
            .files, .space, .interrupt, .markSeen, .takeover, .continueInNewSession,
            .pin, .mute, .handToPersona, .copyDirectory,
        ])
        #expect(!facts.items.contains { $0.id == .close }, "cannot close without a terminal")
        #expect(!facts.items.contains { $0.id == .delete }, "a live session is not deleted")
        #expect(!facts.items.contains { $0.id == .terminal }, "no terminal capability, no terminal")
    }

    @Test func bothEntryPointsConstructTheIdenticalView() throws {
        let s = try session()
        let facts = AgentActionFacts(session: s, pinned: true)
        let handlers = AgentMenuHandlers()
        let row = AgentActionMenu(facts: facts, handlers: handlers)
        let detail = AgentActionMenu(facts: facts, handlers: handlers)
        #expect(row == detail)
        #expect(row.key == detail.key)
        #expect(facts.pinned)
        #expect(facts.items.contains { $0.id == .pin })
        #expect(String(localized: "Unpin [Personal Agent List View c92179]") == "取消置顶")
        #expect(Theme.Icon.unpin == "pin.slash")
    }

    @Test func aReplyOrWorkingTickDoesNotRebuildAnOpenMenu() throws {
        let original = try session()
        var ticked = original
        ticked.lastText = "another reply"
        ticked.rev = 100
        ticked.updatedAt = Date(timeIntervalSince1970: 100)
        #expect(AgentActionFacts(session: original) == AgentActionFacts(session: ticked))
        var changed = ticked
        changed.capabilities?.rename = false
        #expect(AgentActionFacts(session: original) != AgentActionFacts(session: changed))
    }

    @Test func aListDestinationIsConsumedOnceByItsChat() {
        let routing = AgentMenuRouting()
        let key = SessionKey(machineID: "m", sessionID: "s")
        routing.open(.pane(.files), at: key)
        #expect(routing.take(key) == .pane(.files))
        #expect(routing.take(key) == nil)
    }

    @Test func gitUsesTheSessionsRepositoryWithoutAnotherFetch() throws {
        var s = try session()
        #expect(!AgentMenuFacts.panes(s, git: nil).contains(.git))
        s.repoRoot = "/work/example"
        #expect(!AgentMenuFacts.panes(s, git: nil).contains(.git), "a repoRoot is not a loaded repository status")
        let git = try gitStatus(files: 0)
        #expect(AgentMenuFacts.panes(s, git: git).contains(.git))
    }

    @Test func productionEntryPointsAgreeWhileGitIsLoadingAndAfterChangesArrive() throws {
        var s = try session()
        s.repoRoot = "/work/example"
        let model = AppModel.shared
        let routing = AgentMenuRouting.shared
        routing.store.clear(.init(session: s.key, cwd: s.cwd))
        defer { routing.store.clear(.init(session: s.key, cwd: s.cwd)) }

        // The row and the detail both show `AgentActionMenuReader`, whose facts these are: no repository request and no
        // manually cloned facts.
        func rowFacts() -> AgentActionFacts { AgentActionMenuReader.factsForTesting(session: s, model: model) }
        func detailFacts() -> AgentActionFacts { AgentActionMenuReader.factsForTesting(session: s, model: model) }
        let unloaded = try #require(detailFacts())
        #expect(rowFacts().items.map(\.id) == unloaded.items.map(\.id))
        #expect(!unloaded.items.contains { $0.id == .git })

        let changed = try gitStatus(files: 3)
        routing.store.set(changed, for: .init(session: s.key, cwd: s.cwd))
        let loaded = try #require(detailFacts())
        #expect(rowFacts().items.map(\.id) == loaded.items.map(\.id))
        #expect(rowFacts().panes == loaded.panes)
        #expect(rowFacts().paneBadges[.git] == loaded.paneBadges[.git])
        #expect(rowFacts().paneBadges[.git] == "3")
        #expect(loaded.paneBadges[.git] == "3", "both menu labels are Git (3), not one plain Git")
    }

    private func gitStatus(files: Int) throws -> GitStatus {
        let rows = (0..<files).map { #"{"path":"file\#($0).swift","index":"M","worktree":"M","added":1,"deleted":0}"# }
        let json = #"{"is_repo":true,"ahead":0,"behind":0,"files":[\#(rows.joined(separator: ","))],"stashes":0,"commits":[]}"#
        return try JSON.decoder.decode(GitStatus.self, from: Data(json.utf8))
    }
}
