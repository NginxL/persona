import Foundation
import Testing
@testable import Persona
import PersonaWire

/// #9278: the row's long press and the detail More menu are one ordered inventory,
/// not two lists that happen to look similar in one fixture.
@MainActor struct AgentActionMenuTests {
    private func session(_ state: String = "working", status: String = "working") throws -> Session {
        let json = """
        {"machine_id":"m","session_id":"s","title":"Example Agent","cwd":"/work/example",
         "status":"\(status)","state":"\(state)","agent":"codex","source_kind":"codex_history",
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

    /// Move To… (#11516): with the hub's `direction_sessions` and a session the owner may file, the session group
    /// gains it after the to-do's own entries, so it reads apart from Move to Another To-do…; without its choices
    /// (no feature, another manager's page, an Agent that follows its supervised task) the inventory is as before.
    @Test func moveToIsOfferedOnlyWithItsChoices() throws {
        let s = try session()
        let without = AgentActionFacts(session: s, todoHub: true)
        #expect(!without.items.contains { $0.id == .moveTo })
        let choices = [DirectionFilings.Choice(id: "lead-a", name: "Product"), DirectionFilings.Choice(id: "lead-b", name: "Ops")]
        let with = AgentActionFacts(session: s, todoHub: true, moveChoices: choices)
        let ids = with.items.map(\.id)
        #expect(ids.filter { $0 != .moveTo } == without.items.map(\.id), "nothing else moves")
        let move = try #require(ids.firstIndex(of: .moveTo))
        #expect(ids[move - 1] == .unlinkTodo && ids[move + 1] == .stopLoop)
        #expect(with.items[move].group == .session)
        #expect(with != without, "a change of the choices rebuilds the menu")
        #expect(with == AgentActionFacts(session: s, todoHub: true, moveChoices: choices))
    }

    /// The gate itself: with no feature or no tree there are no choices (`DirectionFilings.offers` reads it).
    @Test func moveToChoicesNeedTheFeatureAndTheTree() {
        let tree = DirectionTree(directions: [DirectionTree.Direction(id: "lead-a", name: "Product")])
        #expect(!DirectionFilings.shows(features: [], tree: tree, routesMissing: false))
        #expect(!DirectionFilings.shows(features: [DirectionRoutes.feature], tree: nil, routesMissing: false))
        #expect(DirectionFilings.shows(features: [DirectionRoutes.feature], tree: tree, routesMissing: false))
    }

    /// #13396: at most seven items on the menu's top level, More and the last item counted, whatever an Agent offers;
    /// every item of the inventory is drawn once, and More keeps the inventory's order.
    @Test func theTopLevelHoldsAtMostSevenAndMoreHoldsTheRestInOrder() throws {
        var cases = 0
        for state in AgentState.allCases {
            for status in ["working", "idle", "done", "offline"] {
                for pane in [SessionPane.chat, .files] {
                    for rich in [false, true] {
                        var s = try session(state.rawValue, status: status)
                        if rich { s.repoRoot = "/work/example" } else {
                            s.capabilities?.terminal = false
                            s.loop = nil
                            s.todo = nil
                            s.subagents = nil
                        }
                        let git = try rich ? gitStatus(files: 3) : nil
                        let facts = AgentActionFacts(session: s, muted: rich, currentPane: pane, review: rich, preview: rich,
                                                     screen: rich, todoHub: rich, sideCount: rich ? 2 : 0,
                                                     terminalResume: rich ? .available : .hidden, phone: rich, git: git)
                        let layout = facts.layout
                        let what = "\(state)/\(status)/\(pane)/\(rich): \(layout.top.map(\.id)) + More + \(String(describing: layout.last?.id))"
                        #expect(layout.topLevelCount <= AgentActionLayout.topLimit, "\(what)")
                        let drawn = layout.top + layout.more + (layout.last.map { [$0] } ?? [])
                        #expect(drawn.count == facts.items.count, "\(what)")
                        #expect(Set(drawn.map(\.id)) == Set(facts.items.map(\.id)), "\(what)")
                        #expect(layout.more == facts.items.filter { layout.more.contains($0) }, "\(what)")
                        #expect(layout.top == facts.items.filter { layout.top.contains($0) }, "\(what)")
                        cases += 1
                    }
                }
            }
        }
        #expect(cases == AgentState.allCases.count * 16)
    }

    /// The issue's list (#13396): Interrupt, Rename and Pin stay on the top level, with Mark as Seen and, in the chat
    /// on another pane, Back to Chat; Close Agent… is last, Delete Chat… then under More; with no Close, Delete Chat… is last.
    @Test func theFrequentActionsStayOnTopAndOneDestructiveOneIsLast() throws {
        var s = try session("unread", status: "idle")
        s.repoRoot = "/work/example"
        let open = AgentActionFacts(session: s, currentPane: .files, git: try gitStatus(files: 1)).layout
        #expect(open.top.map(\.id) == [.backToChat, .interrupt, .markSeen, .rename, .pin])
        #expect(open.topLevelCount == AgentActionLayout.topLimit)
        #expect(open.last?.id == .close)
        #expect(open.more.first?.id == .git && open.more.last?.id == .delete, "\(open.more.map(\.id))")

        let row = AgentActionFacts(session: try session()).layout
        #expect(row.top.map(\.id) == [.interrupt, .rename, .pin])
        #expect(row.last?.id == .close)
        #expect(row.more.map(\.id).starts(with: [.files, .subagents, .space, .attach]))

        let ended = AgentActionFacts(session: try session("offline", status: "offline")).layout
        #expect(!ended.top.contains { $0.id == .interrupt }, "an ended Agent has nothing to interrupt")
        #expect(ended.last?.id == .delete)
        #expect(!ended.more.contains { $0.id == .delete || $0.id == .close })
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
