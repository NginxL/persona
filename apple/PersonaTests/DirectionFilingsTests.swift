import Foundation
import Testing
@testable import Persona
import PersonaWire

/// Move To… (#11516, `DirectionFilings`): the write's body and its Undo, every refusal, the hub out of reach and a lost
/// answer, the routes answered as missing, and what the rows draw while a move is out. Against `FakeHub`, with the
/// store's own requests and decoding.
@Suite(.serialized)
@MainActor
struct DirectionFilingsTests {
    private static let key = SessionKey(machineID: "m", sessionID: "s")
    private static let path = "/api/v1/assistant/sessions/m/s/direction"
    private static let product = DirectionFilings.Choice(id: "lead-a", name: "Product")

    /// A store over `hub`, with what it shows and asks for recorded.
    private final class Probe {
        var toasts: [Toast] = []
        var dismissed: [UUID] = []
        var treeReads = 0
        var reachable = true
    }

    private func store(_ hub: FakeHub, _ probe: Probe) -> DirectionFilings {
        DirectionFilings(env: DirectionFilings.Env(api: { hub.client }, reachable: { probe.reachable },
                                                   networkUp: { true }, toast: { probe.toasts.append($0) },
                                                   dismissToast: { probe.dismissed.append($0) },
                                                   rereadTree: { probe.treeReads += 1 }))
    }

    /// Waits (3 s at most) until `done`.
    private func until(_ what: String, _ done: () -> Bool, sourceLocation: SourceLocation = #_sourceLocation) async throws {
        for _ in 0..<300 {
            if done() { return }
            try await Task.sleep(for: .milliseconds(10))
        }
        #expect(done(), "timed out waiting: \(what)", sourceLocation: sourceLocation)
    }

    private func body(_ r: LocalRequest) -> String { String(decoding: r.body ?? Data(), as: UTF8.self) }

    private func refusal(_ status: Int, _ code: String, _ words: String) -> APIError {
        APIError(status: status, message: words, fields: ["code": code, "error": words])
    }

    // MARK: Bodies and Undo

    @Test func theBodyNamesTheDirectionOrOnItsOwnAndNullIsWrittenOut() throws {
        let encode = { (t: DirectionTarget) in String(decoding: try JSON.encoder.encode(DirectionFilingBody(target: t)), as: UTF8.self) }
        #expect(try encode(.direction("lead-a")) == #"{"direction":"lead-a"}"#)
        #expect(try encode(.own) == #"{"direction":""}"#)
        #expect(try encode(.remove) == #"{"direction":null}"#, "a body without the key would mean on its own")
        #expect(DirectionTarget(answer: nil) == .remove && DirectionTarget(answer: "") == .own)
        #expect(DirectionTarget(answer: "lead-b") == .direction("lead-b"))
    }

    /// The Undo of a first filing (`previous: null`) sends `{"direction":null}`, which gives back exactly the
    /// placement before it, a chat its leader opened included; nothing else is sent.
    @Test func undoOfAFirstFilingSendsNull() async throws {
        let hub = FakeHub(), probe = Probe()
        hub.on("PUT", Self.path) { req in
            String(decoding: req.body ?? Data(), as: UTF8.self).contains("null")
                ? Data(#"{"direction":null,"previous":"lead-a","changed":true}"#.utf8)
                : Data(#"{"direction":"lead-a","previous":null,"changed":true}"#.utf8)
        }
        let s = store(hub, probe)
        s.move(Self.key, title: "Trip", state: .idle, to: Self.product, from: nil)
        #expect(s.drawnUnder(Self.key) == "lead-a", "drawn there at once, before the answer")
        try await until("the answer's toast") { !probe.toasts.isEmpty }
        let toast = try #require(probe.toasts.first)
        #expect(toast.message == "已移到「Product」下")
        let undo = try #require(toast.action, "a change offers Undo")
        undo.run()
        #expect(s.drawnUnder(Self.key) == "", "drawn back where it was (no direction)")
        try await until("the Undo's write") { hub.sent("PUT", Self.path).count == 2 }
        #expect(body(hub.sent("PUT", Self.path)[1]) == #"{"direction":null}"#)
        try await until("settled") { s.pending.isEmpty }
        #expect(probe.toasts.count == 1, "the Undo itself offers no second Undo")
    }

    /// From On Its Own and from another direction, Undo sends `previous` as it is.
    @Test func undoSendsAStringPreviousAsItIs() async throws {
        for previous in ["", "lead-b"] {
            let hub = FakeHub(), probe = Probe()
            hub.on("PUT", Self.path, json: #"{"direction":"lead-a","previous":"\#(previous)","changed":true}"#)
            let s = store(hub, probe)
            s.move(Self.key, title: "Trip", state: .working, to: Self.product, from: previous.isEmpty ? nil : previous)
            try await until("the answer") { probe.toasts.first?.action != nil }
            probe.toasts.first?.action?.run()
            #expect(s.drawnUnder(Self.key) == previous)
            try await until("the Undo's write") { hub.sent("PUT", Self.path).count == 2 }
            #expect(body(hub.sent("PUT", Self.path)[1]) == #"{"direction":"\#(previous)"}"#)
        }
    }

    /// The same place again answers `changed: false`: no Undo.
    @Test func anUnchangedAnswerOffersNoUndo() async throws {
        let hub = FakeHub(), probe = Probe()
        hub.on("PUT", Self.path, json: #"{"direction":"lead-a","previous":"lead-a","changed":false}"#)
        let s = store(hub, probe)
        s.move(Self.key, title: "Trip", state: .idle, to: Self.product, from: "lead-a")
        try await until("the answer") { !probe.toasts.isEmpty }
        #expect(probe.toasts.first?.action == nil)
        try await until("the tree read") { probe.treeReads == 1 }
    }

    // MARK: Refusals, out of reach, a lost answer

    /// Every refusal puts the row back, shows the hub's sentence and retries nothing; a 404 with one of the routes'
    /// codes is a refusal that keeps the entries.
    @Test func eachRefusalLeavesTheRowAndSaysTheHubsSentence() async throws {
        let cases: [(Int, String)] = [(403, "full_access_required"), (403, "forbidden"), (404, "session_not_found"),
                                      (409, "direction_supervised_task"), (409, "direction_full"),
                                      (503, "direction_unavailable")]
        for (status, code) in cases {
            let hub = FakeHub(), probe = Probe()
            let words = "The hub says no: \(code)"
            hub.on("PUT", Self.path) { [self] _ in throw refusal(status, code, words) }
            let s = store(hub, probe)
            s.move(Self.key, title: "Trip", state: .idle, to: Self.product, from: nil)
            try await until("\(code) settled") { s.pending.isEmpty }
            #expect(s.drawnUnder(Self.key) == nil, "\(code): back where it was")
            #expect(probe.toasts.first?.message.hasPrefix(words) == true, "\(code): \(probe.toasts.first?.message ?? "none")")
            #expect(probe.toasts.first?.style == .error && probe.toasts.first?.action == nil)
            #expect(hub.sent("PUT", Self.path).count == 1, "\(code): sent once, never again")
            #expect(s.routesMissing.isEmpty, "\(code) is a refusal, not a missing route")
            #expect(probe.treeReads == 0)
        }
    }

    /// With the hub out of reach nothing is sent and nothing moves; the reason is said.
    @Test func outOfReachSendsNothing() async throws {
        let hub = FakeHub(), probe = Probe()
        probe.reachable = false
        let s = store(hub, probe)
        s.move(Self.key, title: "Trip", state: .idle, to: Self.product, from: nil)
        #expect(hub.requests.isEmpty)
        #expect(s.pending.isEmpty)
        #expect(probe.toasts.first?.message == "连不上 hub，刚才的操作没有生效")
    }

    /// Connections that never reached the hub put it back; answers lost after sending read the tree again, send
    /// nothing again and offer no Undo.
    @Test func aLostAnswerReadsAgainAndOffersNoUndo() async throws {
        // A cut link between hub and manager answers 503 assistant_offline (hub/bus_forward.go): the manager may have
        // saved it, as with a relay cut off on the way.
        let lost: [Error] = [URLError(.timedOut), URLError(.networkConnectionLost),
                             APIError(status: 502, message: "cut off", fields: ["code": "assistant_in_doubt", "delivered": "unknown"]),
                             APIError(status: 503, message: "offline", fields: ["code": "assistant_offline"])]
        for error in lost {
            let hub = FakeHub(), probe = Probe()
            hub.on("PUT", Self.path) { _ in throw error }
            let s = store(hub, probe)
            s.move(Self.key, title: "Trip", state: .idle, to: Self.product, from: nil)
            try await until("settled") { s.pending.isEmpty && !probe.toasts.isEmpty }
            #expect(probe.toasts.first?.action == nil, "\(error): no Undo")
            let offline = (error as? APIError)?.code == "assistant_offline"
            // assistant_offline is worded by the app for its code (`APIError.businessMessage`), the others alike.
            #expect(probe.toasts.first?.message == (offline ? "助手不在线或正在重启，请稍后重试。" : "没能确认是否移好了，已重新读取它现在的位置。"),
                    "\(error): the words for the hub's code when it gave one")
            #expect(probe.treeReads == 1, "\(error): the tree is read again")
            #expect(hub.sent("PUT", Self.path).count == 1, "\(error): never sent again")
        }
        let hub = FakeHub(), probe = Probe()
        hub.on("PUT", Self.path) { _ in throw URLError(.cannotConnectToHost) }
        let s = store(hub, probe)
        s.move(Self.key, title: "Trip", state: .idle, to: Self.product, from: nil)
        try await until("settled") { s.pending.isEmpty && !probe.toasts.isEmpty }
        #expect(probe.treeReads == 0, "never sent: nothing to read again")
        #expect(DirectionFilings.failure(URLError(.cannotConnectToHost)) == .notSent)
    }

    /// 404 or 405 without one of the routes' codes: an older manager; every entry hides until the next hello.
    @Test func missingRoutesHideTheEntriesUntilTheNextHello() async throws {
        let hub = FakeHub(), probe = Probe()
        hub.on("PUT", Self.path) { [self] _ in throw refusal(404, "not_found", "not found") }
        let s = store(hub, probe)
        s.move(Self.key, title: "Trip", state: .idle, to: Self.product, from: nil)
        try await until("settled") { s.pending.isEmpty }
        #expect(s.routesMissing == [""], "the hub the write went to, alone")
        s.hello("other")
        #expect(s.routesMissing == [""], "another hub's hello says nothing of this one's routes")
        s.forget()
        #expect(s.routesMissing == [""], "nor does a switch")
        s.hello("")
        #expect(s.routesMissing.isEmpty)
        #expect(DirectionRoutes.missing(APIError(status: 405, message: "")))
        #expect(!DirectionRoutes.missing(refusal(404, "direction_not_found", "gone")))
        #expect(!DirectionRoutes.missing(refusal(404, "machine_not_found", "gone")))
    }

    // MARK: Gate, followers and rows

    /// Nothing is drawn without the feature, without the default manager's tree (another manager's page), or once the
    /// routes were answered as missing.
    @Test func theGate() {
        let tree = DirectionTree(directions: [DirectionTree.Direction(id: "lead-a")])
        #expect(DirectionFilings.shows(features: [DirectionRoutes.feature], tree: tree, routesMissing: false))
        #expect(!DirectionFilings.shows(features: [], tree: tree, routesMissing: false))
        #expect(!DirectionFilings.shows(features: [DirectionRoutes.feature], tree: nil, routesMissing: false))
        #expect(!DirectionFilings.shows(features: [DirectionRoutes.feature], tree: tree, routesMissing: true))
    }

    /// An Agent of an open supervised to-do, and the sessions under it, follow their task: no Move To….
    @Test func followersAndTheSessionsUnderThem() throws {
        let todo = try JSON.decoder.decode(TodoItem.self, from: Data(#"""
        {"id":"t","title":"Ship","agents":[{"machine_id":"m","session_id":"a"},{"machine_id":"m","session_id":"gone","released_at":"2026-11-20T00:00:00Z"}],
         "supervision":{"supervisor":"lead-a"}}
        """#.utf8))
        let own = try JSON.decoder.decode(TodoItem.self, from: Data(#"{"id":"o","title":"Mine","agents":[{"machine_id":"m","session_id":"b"}]}"#.utf8))
        let followers = TodoStore.taskFollowers([todo, own])
        #expect(followers == ["m/a"], "a released Agent and the owner's own unsupervised to-do do not follow")
        let parents = ["child": "a", "grandchild": "child"]
        let parent = { (k: SessionKey) in parents[k.sessionID] }
        #expect(DirectionFilings.follows(SessionKey(machineID: "m", sessionID: "a"), followers: followers, parent: parent))
        #expect(DirectionFilings.follows(SessionKey(machineID: "m", sessionID: "grandchild"), followers: followers, parent: parent))
        #expect(DirectionFilings.follows(SessionKey(machineID: "m", sessionID: "a@sub"), followers: followers, parent: { _ in nil }))
        #expect(!DirectionFilings.follows(SessionKey(machineID: "m", sessionID: "b"), followers: followers, parent: parent))
        #expect(!DirectionFilings.follows(SessionKey(machineID: "other", sessionID: "a"), followers: followers, parent: parent))
    }

    /// While a move is out, the session leaves the rows of the direction it left and joins the one it goes to, marked
    /// as moving; the sidebar's rows (`live`) take only a working or waiting one, the page's every one.
    @Test func rowsFollowTheMovesOut() async throws {
        let hub = FakeHub(), probe = Probe()
        hub.onAsync("PUT", Self.path) { _ in
            try await Task.sleep(for: .seconds(30))
            return Data("{}".utf8)
        }
        let s = store(hub, probe)
        let listed = [DirectionTree.FiledSession(machineID: "m", sessionID: "s", title: "Trip", state: "working"),
                      DirectionTree.FiledSession(machineID: "m", sessionID: "x", title: "Other", state: "needs_you")]
        #expect(s.rows(listed, in: "lead-b", group: "", live: true).map(\.id) == ["m/s", "m/x"])
        s.move(Self.key, title: "Trip", state: .working, to: Self.product, from: "lead-b")
        #expect(s.rows(listed, in: "lead-b", group: "", live: true).map(\.id) == ["m/x"], "gone from where it was")
        let there = s.rows([], in: "lead-a", group: "", live: true)
        #expect(there.map(\.id) == ["m/s"] && there.first?.moving == true, "drawn there at once, marked moving")
        let quiet = SessionKey(machineID: "m", sessionID: "q")
        s.move(quiet, title: "Quiet", state: .idle, to: Self.product, from: nil)
        #expect(s.rows([], in: "lead-a", group: "", live: true).map(\.id) == ["m/s"], "the tree lists only live ones")
        #expect(s.rows([], in: "lead-a", group: "", live: false).map(\.id) == ["m/s", "m/q"],
                "the page lists every one, by its state: working before the rest")
    }

    // MARK: The page's words

    /// Each standing says where the session is; none says "not found" or "archived", and a value this app does not
    /// know says nothing.
    @Test func eachStandingSaysWhereItIs() {
        let at = Date(timeIntervalSinceNow: -3 * 86_400)
        func word(_ state: String, live: AgentState? = nil) -> String? {
            DirectionPageLogic.standing(DirectionTree.FiledSession(machineID: "m", sessionID: "s", at: at, state: state),
                                        live: live).map(DirectionPageLogic.words)
        }
        #expect(word("offline") == "未运行", "stopped on a connected computer: Not Running")
        #expect(word("computer_offline") == "离线")
        #expect(word("list_unknown") == "还不知道")
        #expect(word("computer_removed") == "电脑已移除")
        #expect(word("not_listed")?.hasSuffix("放进来") == true, "only when it was added: \(word("not_listed") ?? "nil")")
        #expect(word("idle", live: .working) == AgentState.working.label, "the live state follows it after the read")
        #expect(word("archived_somehow") == nil)
        #expect(DirectionPageLogic.shown(Array(0..<9), all: false) == [0, 1, 2, 3, 4])
        #expect(DirectionPageLogic.shown(Array(0..<9), all: true).count == 9)
    }

    // MARK: Review of #12603

    /// The gate and every exclusion of Move To…, from the facts the app gathers (`DirectionFilings.offers`).
    @Test func moveToIsOfferedOnlyWhereTheIssueSays() {
        let choices = [Self.product]
        func facts(shows: Bool = true, group: String = "", moving: Bool = false, followers: Set<String> = [],
                   parents: [String: String] = [:]) -> DirectionFilings.Facts {
            DirectionFilings.Facts(shows: shows, hubGroup: group, main: SessionKey(machineID: "m", sessionID: "main"),
                                   backgroundSupervisor: { $0 == "bg" }, leaders: ["lead-a", "lead-b"],
                                   followers: followers, parent: { parents[$0.sessionID] }, moving: moving,
                                   choices: choices)
        }
        let chat = SessionKey(machineID: "m", sessionID: "chat")
        #expect(DirectionFilings.offers(chat, facts()) == choices, "an ordinary chat or Agent")
        #expect(DirectionFilings.offers(chat, facts(shows: false)).isEmpty, "no feature, no tree, or missing routes")
        #expect(DirectionFilings.offers(SessionKey(machineID: "m", sessionID: "main"), facts()).isEmpty, "the main chat")
        #expect(DirectionFilings.offers(SessionKey(machineID: "m", sessionID: "bg"), facts()).isEmpty, "the background supervisor")
        #expect(DirectionFilings.offers(SessionKey(machineID: "m", sessionID: "lead-b"), facts()).isEmpty, "a leader conversation")
        #expect(DirectionFilings.offers(SessionKey(group: "work", machineID: "m", sessionID: "chat"), facts()).isEmpty,
                "another hub's session")
        #expect(DirectionFilings.offers(SessionKey(machineID: "m", sessionID: "agent"), facts(followers: ["m/agent"])).isEmpty,
                "an Agent that follows its supervised to-do")
        #expect(DirectionFilings.offers(SessionKey(machineID: "m", sessionID: "sub"),
                                        facts(followers: ["m/agent"], parents: ["sub": "agent"])).isEmpty,
                "a session under one")
        #expect(DirectionFilings.offers(SessionKey(machineID: "m", sessionID: "own"), facts(followers: ["m/agent"])) == choices,
                "an Agent on the owner's own unsupervised to-do")
        #expect(DirectionFilings.offers(chat, facts(moving: true)).isEmpty, "not while a pick of it is out")
    }

    /// A switch of hub or assistant drops a move's late answer (no toast, no reread) and its toast's Undo; Undo goes to
    /// the client the move went out with, not to whatever is current when it is tapped.
    @Test func undoAndLateAnswersStayWithTheirHub() async throws {
        let first = FakeHub(url: "https://first.invalid"), second = FakeHub(url: "https://second.invalid"), probe = Probe()
        for hub in [first, second] {
            hub.on("PUT", Self.path, json: #"{"direction":"lead-a","previous":"","changed":true}"#)
        }
        final class Current { var hub: FakeHub; init(_ hub: FakeHub) { self.hub = hub } }
        let current = Current(first)
        let s = DirectionFilings(env: DirectionFilings.Env(api: { current.hub.client }, reachable: { true }, networkUp: { true },
                                                           toast: { probe.toasts.append($0) },
                                                           rereadTree: { probe.treeReads += 1 }))
        s.move(Self.key, title: "Trip", state: .idle, to: Self.product, from: nil)
        try await until("the answer") { probe.toasts.first?.action != nil }
        current.hub = second
        probe.toasts.first?.action?.run()
        try await until("the Undo's write") { first.sent("PUT", Self.path).count == 2 }
        #expect(second.sent("PUT", Self.path).isEmpty, "Undo went to the hub the move was sent to")

        let slow = FakeHub(), probe2 = Probe()
        slow.onAsync("PUT", Self.path) { _ in
            try await Task.sleep(for: .milliseconds(200))
            return Data(#"{"direction":"lead-a","previous":null,"changed":true}"#.utf8)
        }
        let t = store(slow, probe2)
        t.move(Self.key, title: "Trip", state: .idle, to: Self.product, from: nil)
        t.forget() // another assistant while the answer is out
        #expect(t.pending.isEmpty)
        try await Task.sleep(for: .milliseconds(500))
        #expect(probe2.toasts.isEmpty && probe2.treeReads == 0, "the answer of the assistant before says nothing")
    }

    /// The facts `offers` decides from, made of the app's values: every leader conversation (the tree's and the
    /// settings'), the main chat, the gate for this hub, and the choices in the tree's order by the rows' names.
    @Test func theFactsAreMadeOfTheAppsValues() {
        let tree = DirectionTree(directions: [DirectionTree.Direction(id: "lead-b", name: "Ops"),
                                              DirectionTree.Direction(id: "lead-a", name: ""),
                                              DirectionTree.Direction(id: "main", secretary: true)])
        let main = SessionKey(machineID: "m", sessionID: "main")
        func facts(missing: Set<String> = []) -> DirectionFilings.Facts {
            DirectionFilings.facts(features: [DirectionRoutes.feature], hubGroup: "", tree: tree, routesMissing: missing,
                                   main: main, backgroundSupervisor: { $0 == "bg" }, configLeaders: ["lead-c"],
                                   followers: ["m/agent"], parent: { $0.sessionID == "child" ? "agent" : nil },
                                   moving: false, name: { $0.name.isEmpty ? "Untitled lead" : $0.name })
        }
        let f = facts()
        #expect(f.shows && f.main == main)
        #expect(f.leaders == ["lead-a", "lead-b", "lead-c"], "the tree's leaders and the settings' list")
        #expect(f.choices == [DirectionFilings.Choice(id: "lead-b", name: "Ops"), DirectionFilings.Choice(id: "lead-a", name: "Untitled lead")],
                "the tree's order, by the names the rows show; never the secretary group")
        #expect(!facts(missing: [""]).shows, "this hub's routes missing")
        #expect(facts(missing: ["work"]).shows, "another hub's do not count")
        for leader in ["lead-a", "lead-c"] {
            #expect(DirectionFilings.offers(SessionKey(machineID: "m", sessionID: leader), f).isEmpty)
        }
        #expect(DirectionFilings.offers(main, f).isEmpty, "the main chat")
        for (id, what) in [("bg", "the background supervisor"), ("agent", "an Agent of a supervised to-do"),
                           ("child", "a session under one")] {
            #expect(DirectionFilings.offers(SessionKey(machineID: "m", sessionID: id), f).isEmpty, "\(what)")
        }
        #expect(DirectionFilings.offers(SessionKey(machineID: "m", sessionID: "trip"), f).count == 2, "any other session")
    }

    /// A toast's Undo that would no longer put back what it says is taken down: on a switch, and when a later pick of
    /// the same session goes out.
    @Test func undoToastsGoWhenTheirUndoWouldBeWrong() async throws {
        let hub = FakeHub(), probe = Probe()
        hub.on("PUT", Self.path, json: #"{"direction":"lead-a","previous":null,"changed":true}"#)
        let s = store(hub, probe)
        s.move(Self.key, title: "Trip", state: .idle, to: Self.product, from: nil)
        try await until("the answer") { probe.toasts.first?.action != nil && s.pending.isEmpty }
        let first = try #require(probe.toasts.first)
        s.move(Self.key, title: "Trip", state: .idle, to: nil, from: "lead-a")
        #expect(probe.dismissed == [first.id], "a later pick of the session takes the Undo down")
        try await until("the second answer") { probe.toasts.count == 2 && s.pending.isEmpty }
        let second = try #require(probe.toasts.last)
        s.forget()
        #expect(probe.dismissed == [first.id, second.id], "a switch takes the Undo down")
    }

    /// One pick of a session at a time: a second one while the first is out is not sent.
    @Test func onePickAtATime() async throws {
        let hub = FakeHub(), probe = Probe()
        hub.onAsync("PUT", Self.path) { _ in
            try await Task.sleep(for: .milliseconds(300))
            return Data(#"{"direction":"lead-a","previous":null,"changed":true}"#.utf8)
        }
        let s = store(hub, probe)
        s.move(Self.key, title: "Trip", state: .idle, to: Self.product, from: nil)
        s.move(Self.key, title: "Trip", state: .idle, to: nil, from: nil)
        try await until("settled") { s.pending.isEmpty && !probe.toasts.isEmpty }
        #expect(hub.sent("PUT", Self.path).count == 1)
    }

    /// Added Here and the row's filed sessions: what needs you, then what works, then the rest, newest added first in
    /// each (the hub gives them by computer and session id).
    @Test func filedSessionsAreInTheAppsOrder() {
        let day: TimeInterval = 86_400
        func f(_ id: String, _ state: String, _ ago: TimeInterval?) -> DirectionTree.FiledSession {
            DirectionTree.FiledSession(machineID: "m", sessionID: id, at: ago.map { Date(timeIntervalSinceNow: -$0) }, state: state)
        }
        let hubOrder = [f("a", "idle", 1 * day), f("b", "needs_you", 3 * day), f("c", "working", 2 * day),
                        f("d", "not_listed", 0.5 * day), f("e", "needs_you", 1 * day), f("f", "offline", nil)]
        #expect(DirectionFilings.order(hubOrder).map(\.sessionID) == ["e", "b", "c", "d", "a", "f"])
    }

    /// A filed session's turn changes only its direction's box, never the tree its readers compare (P5).
    @Test func filedSessionsLeaveTheTreeAlone() throws {
        func tree(_ state: String) throws -> DirectionTree {
            try JSON.decoder.decode(DirectionTree.self, from: Data(#"""
            {"directions":[{"id":"lead-a","name":"Product","counts":{"needs_you":1},"tasks":[{"id":"t","title":"T","state":"needs_you"}],
              "sessions":[{"machine_id":"m","session_id":"f","state":"\#(state)"}]},
             {"id":"lead-b","name":"Ops","tasks":[]}]}
            """#.utf8))
        }
        let s = DirectionFilings(env: DirectionFilings.Env(api: { nil }, reachable: { true }, networkUp: { true },
                                                           toast: { _ in }, rereadTree: {}))
        let working = s.takeFiled(try tree("working"))
        #expect(s.filedLive("lead-a").map(\.state) == ["working"])
        let waiting = s.takeFiled(try tree("needs_you"))
        #expect(working == waiting, "the tree compares equal: its readers do not redraw")
        #expect(s.filedLive("lead-a").map(\.state) == ["needs_you"] && s.filedLive("lead-b").isEmpty)
        #expect(working.leader("lead-a")?.tasks.count == 1, "the rest of the tree is as the hub gave it")
        _ = s.takeFiled(DirectionTree(directions: [DirectionTree.Direction(id: "lead-b")]))
        #expect(s.filedLive("lead-a").isEmpty, "a direction gone from the tree lists none")
    }
}
