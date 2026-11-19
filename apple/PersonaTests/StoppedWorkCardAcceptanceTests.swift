import Foundation
import Testing
import BeautifulUI
@testable import Persona
import PersonaWire

/// A stopped Agent cannot receive an answer, but its manager's work card can still hand the turn back locally (#6839).
@MainActor
struct StoppedWorkCardAcceptanceTests {
    private let question = "The model service failed. What next?"
    private let followUp = "What should happen to the remaining work?"
    private let continueLabel = "Handled, continue"
    private let deferLabel = "Leave it"

    private func decode<T: Decodable>(_ type: T.Type, _ json: String) throws -> T {
        try JSON.decoder.decode(type, from: Data(json.utf8))
    }

    private func session(stopped: Bool = true) throws -> Session {
        let status = stopped ? "offline" : "working"
        let state = stopped ? "needs_you" : "working"
        let capabilities = stopped
            ? #"{"read":true,"terminal":true,"send":false,"approve":false}"#
            : #"{"read":true,"terminal":true,"send":true,"approve":true}"#
        return try decode(Session.self, """
        {"machine_id":"m","session_id":"s","title":"Stopped Agent","cwd":"/tmp/p","status":"\(status)","state":"\(state)","capabilities":\(capabilities)}
        """)
    }

    private func workCard(id: String = "work_1", tool: String = "persona_triage", multiSelect: Bool = false,
                          twoQuestions: Bool = false, withAction: Bool = true) throws -> Interaction {
        let action = withAction ? #","action":"defer""# : ""
        let options = #"[{"label":"\#(continueLabel)"},{"label":"\#(deferLabel)","description":"Hand the turn back"\#(action)}]"#
        let second = twoQuestions ? #",{"question":"\#(followUp)","multi_select":false,"options":\#(options)}"# : ""
        return try decode(Interaction.self, """
        {"id":"\(id)","machine_id":"m","session_id":"s","kind":"question","tool_name":"\(tool)","can_cancel":true,
         "questions":[{"question":"\(question)","multi_select":\(multiSelect),"options":\(options)}\(second)]}
        """)
    }

    private func permissionCard() throws -> Interaction {
        try decode(Interaction.self, #"{"id":"permission_1","machine_id":"m","session_id":"s","kind":"permission","tool_name":"persona_triage","summary":"Run the tests"}"#)
    }

    private func model(_ hub: FakeHub, _ ix: Interaction, _ session: Session) -> AppModel {
        let folder = FileManager.default.temporaryDirectory.appending(path: "stopped-work-card-\(UUID().uuidString)")
        let model = AppModel(stateFolder: folder)
        model.forgetRestoredForTests()
        model.apiForTests = hub.client
        model.applySessions([MachineRef(group: "", id: "m"): [session]])
        model.replaceInteractions("", [ix])
        return model
    }

    private func singleResolution(_ hub: FakeHub, _ path: String) throws -> Resolution {
        let requests = hub.sent("POST", path)
        #expect(requests.count == 1)
        let request = try #require(requests.first)
        let body = try #require(request.body)
        return try JSON.decoder.decode(Resolution.self, from: body)
    }

    @Test func questionOptionActionSurvivesCodingAndStaysOptional() throws {
        let option = try decode(QuestionOption.self,
                                #"{"label":"Leave it","description":"Hand the turn back","action":"defer"}"#)
        #expect(option.label == deferLabel && option.description == "Hand the turn back")
        let object = try #require(try JSONSerialization.jsonObject(with: JSON.encoder.encode(option)) as? [String: Any])
        #expect(object["action"] as? String == "defer")

        let old = try decode(QuestionOption.self, #"{"label":"Leave it","description":"Hand the turn back"}"#)
        let oldObject = try #require(try JSONSerialization.jsonObject(with: JSON.encoder.encode(old)) as? [String: Any])
        #expect(oldObject["action"] == nil)
        #expect(try decode(QuestionOption.self, String(decoding: JSON.encoder.encode(old), as: UTF8.self)) == old)
    }

    @Test func stoppedManagerWorkCardStaysEnabled() throws {
        let stopped = try session()
        let ix = try workCard()
        #expect(stopped.isStopped && !stopped.canApprove)
        #expect(ix.answerBlock(session: stopped) == nil)

        let model = model(FakeHub(), ix, stopped)
        let blocked = model.answerBlock(ix)
        let pending = PendingCards.Card(interaction: ix, enabled: blocked == nil, blocked: blocked)
        #expect(blocked == nil)
        #expect(pending.enabled && pending.blocked == nil)
    }

    @Test func stoppedManagerDeferUsesResolveAndClearsTheCard() async throws {
        let ix = try workCard()
        let path = "/api/v1/interactions/work_1/resolve"
        let hub = FakeHub()
        hub.on("POST", path, json: #"{"ok":true}"#)
        let model = model(hub, ix, try session())
        let answer = Resolution(answers: [question: deferLabel])

        await model.resolve(ix, answer)

        #expect(try singleResolution(hub, path) == answer)
        #expect(model.pendingInteractions.isEmpty)
    }

    @Test func stoppedManagerBareDismissUsesResolveAndClearsTheCard() async throws {
        let ix = try workCard()
        let path = "/api/v1/interactions/work_1/resolve"
        let hub = FakeHub()
        hub.on("POST", path, json: #"{"ok":true}"#)
        let model = model(hub, ix, try session())

        await model.resolve(ix, .dismissed)

        #expect(try singleResolution(hub, path) == .dismissed)
        #expect(model.pendingInteractions.isEmpty)
    }

    @Test func stoppedManagerMessageAnswersAndCancellationAreRefused() async throws {
        let ix = try workCard()
        let resolutions = [
            Resolution(answers: [question: continueLabel]),
            Resolution(answers: [question: "Try again"]),
            Resolution(answers: [question: "A custom reply"]),
            .cancelled,
            Resolution(answers: [question: deferLabel], cancel: true),
            Resolution(answers: [question: deferLabel], message: "Also wake the Agent"),
            Resolution(answers: [question: deferLabel], dismiss: true),
        ]
        for answer in resolutions {
            let hub = FakeHub()
            let model = model(hub, ix, try session())
            await model.resolve(ix, answer)
            #expect(hub.requests.isEmpty)
            #expect(model.pendingInteractions.map(\.id) == [ix.id])
            #expect(model.answerProgress(ix.uid) == nil)
        }
    }

    @Test func stoppedManagerAcceptsOnlyAnOfferedSingleChoiceDeferAction() async throws {
        let multi = try workCard(multiSelect: true)
        let unmarked = try workCard(withAction: false)
        let otherQuestion = Resolution(answers: ["A question no longer on the card": deferLabel])
        for (ix, answer) in [
            (multi, Resolution(answers: [question: deferLabel])),
            (unmarked, Resolution(answers: [question: deferLabel])),
            (try workCard(), otherQuestion),
        ] {
            let hub = FakeHub()
            let model = model(hub, ix, try session())
            await model.resolve(ix, answer)
            #expect(hub.requests.isEmpty)
            #expect(model.pendingInteractions.map(\.id) == [ix.id])
        }
    }

    @Test func stoppedManagerRefusesStaleAndMixedDrafts() async throws {
        let ix = try workCard(twoQuestions: true)
        let questions = try #require(ix.questions)
        let mixed = BUIQuestionForm.answers(.init(picks: [0: [1], 1: [0]], custom: [:]), questions)
        #expect(mixed == [question: deferLabel, followUp: continueLabel])
        let custom = BUIQuestionForm.answers(.init(picks: [0: [1]], custom: [0: "Retry the Agent"]), questions)
        #expect(custom[question] == "Retry the Agent")

        for answers in [mixed, custom] {
            let hub = FakeHub()
            let model = model(hub, ix, try session())
            await model.resolve(ix, Resolution(answers: answers))
            #expect(hub.requests.isEmpty)
            #expect(model.pendingInteractions.map(\.id) == [ix.id])
        }
    }

    @Test func aRetryFromBeforeTheStopCannotMessageTheStoppedAgent() async throws {
        let ix = try workCard()
        let path = "/api/v1/interactions/work_1/resolve"
        let hub = FakeHub()
        var attempts = 0
        hub.on("POST", path) { _ in
            attempts += 1
            if attempts == 1 { throw APIError(status: 400, message: "not delivered") }
            return Data(#"{"ok":true}"#.utf8)
        }
        let model = model(hub, ix, try session(stopped: false))
        await model.resolve(ix, Resolution(answers: [question: continueLabel]))
        #expect(hub.sent("POST", path).count == 1)
        #expect(model.answerFailure(ix.uid) != nil)

        model.applySessions([MachineRef(group: "", id: "m"): [try session()]])
        #expect(model.session(ix.sessionKey)?.isStopped == true)
        await model.retryAnswer(ix)

        #expect(hub.sent("POST", path).count == 1)
        #expect(model.pendingInteractions.map(\.id) == [ix.id])
    }

    @Test func stoppedPermissionAndNonManagerCardsStayDisabledAndRefused() async throws {
        for ix in [try permissionCard(), try workCard(id: "ordinary_1", tool: "AskUserQuestion")] {
            let hub = FakeHub()
            let stopped = try session()
            let model = model(hub, ix, stopped)
            let blocked = model.answerBlock(ix)
            let pending = PendingCards.Card(interaction: ix, enabled: blocked == nil, blocked: blocked)
            #expect(blocked == .computer)
            #expect(!pending.enabled && pending.blocked == .computer)

            await model.resolve(ix, Resolution(allow: true))
            await model.resolve(ix, Resolution(answers: [question: deferLabel]))
            await model.resolve(ix, .dismissed)
            #expect(hub.requests.isEmpty)
            #expect(model.pendingInteractions.map(\.id) == [ix.id])
        }
    }

    @Test func anAttachableManagerCardKeepsItsLiveLink() throws {
        let attachable = try decode(Session.self, #"{"machine_id":"m","session_id":"s","title":"Stopped Agent","cwd":"/tmp/p","status":"offline","state":"needs_you","source_kind":"codex_app_server","capabilities":{"approve":false,"attach":true}}"#)
        let ix = try workCard()
        #expect(ix.answerBlock(session: attachable) == .attach)
    }

    @Test func runningManagerCardsKeepTheirPreviousBehavior() async throws {
        let ix = try workCard()
        let running = try session(stopped: false)
        #expect(!running.isStopped && running.canApprove)
        #expect(ix.answerBlock(session: running) == nil)

        for answer in [
            Resolution(answers: [question: continueLabel]),
            Resolution(answers: [question: "A custom reply"]),
            .cancelled,
            .dismissed,
        ] {
            let path = "/api/v1/interactions/work_1/resolve"
            let hub = FakeHub()
            hub.on("POST", path, json: #"{"ok":true}"#)
            let model = model(hub, ix, running)
            await model.resolve(ix, answer)
            #expect(try singleResolution(hub, path) == answer)
            #expect(model.pendingInteractions.isEmpty)
        }
    }
}
