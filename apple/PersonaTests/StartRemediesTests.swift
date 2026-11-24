import Foundation
import Testing
@testable import Persona
import PersonaWire

/// The failed-start remedies (#10452): read-only plan fields, coded wording, launcher retries and terminal questions.
@MainActor
struct StartRemediesTests {
    private func task(todo: String = "t", code: String? = nil, args: [String: String]? = nil,
                      retry: [String]? = nil, error: String? = "raw start failure") -> PlanTask {
        PlanTask(title: "Task", prompt: "Work", machine: "box", cwd: "/work", todoID: todo,
                 error: error, errorCode: code, errorArgs: args, retryWith: retry)
    }

    private func plan(_ id: String, task: PlanTask, state: TaskPlan.State = .started) -> TaskPlan {
        TaskPlan(id: id, conversation: "main", tasks: [task], state: state)
    }

    @Test func remedyFieldsDecodeButPlanEditsNeverSendThemBack() throws {
        let task = try JSON.decoder.decode(PlanTask.self, from: Data(#"{"title":"Task","prompt":"Work","machine":"box","cwd":"/work","todo_id":"t","error":"raw","error_code":"shell_waiting","error_args":{"line":"Continue?","choices":"y,n"},"retry_with":["codex","claude"]}"#.utf8))
        #expect(task.errorCode == "shell_waiting")
        #expect(task.errorArgs == ["line": "Continue?", "choices": "y,n"])
        #expect(task.retryWith == ["codex", "claude"])
        let sent = try #require(try JSONSerialization.jsonObject(with: JSON.encoder.encode(task)) as? [String: Any])
        #expect(sent["error_code"] == nil && sent["error_args"] == nil && sent["retry_with"] == nil)
        #expect(sent["error"] as? String == "raw")

        let old = try JSON.decoder.decode(PlanTask.self, from: Data(#"{"title":"Old","prompt":"Work","machine":"box","cwd":"/work"}"#.utf8))
        #expect(old.errorCode == nil && old.errorArgs == nil && old.retryWith == nil)
    }

    @Test func planLookupUsesTheOwnPlanThenANonDismissedTodoLink() {
        let linked = TodoItem(id: "t", title: "Task", planID: "own")
        let own = task(retry: ["own-launcher"])
        let fallback = task(retry: ["fallback-launcher"])
        #expect(TaskStartRemedy.planTask(of: linked, in: ["own": plan("own", task: own), "other": plan("other", task: fallback)])?.retryWith == ["own-launcher"])
        #expect(TaskStartRemedy.planTask(of: linked, in: ["other": plan("other", task: fallback)])?.retryWith == ["fallback-launcher"])
        #expect(TaskStartRemedy.planTask(of: linked, in: ["other": plan("other", task: fallback, state: .dismissed)]) == nil)

        let withoutPlan = TodoItem(id: "t", title: "Task")
        #expect(TaskStartRemedy.planTask(of: withoutPlan, in: ["other": plan("other", task: fallback)])?.retryWith == ["fallback-launcher"])
    }

    @Test func codedFailureLinesPreferTheShortTerminalAndWorkspaceWording() {
        let launcher = "Codex"
        #expect(TaskStartRemedy.failureLine(code: "launcher_missing", args: ["where": "terminal", "launcher": launcher])
                == String(localized: "\(launcher) can't start in this computer's terminal"))
        #expect(TaskStartRemedy.failureLine(code: "launcher_missing", args: ["where": "service", "launcher": "Codex"]) == nil)
        #expect(TaskStartRemedy.failureLine(code: "worktree_path_taken", args: ["path": "/work"])
                == String(localized: "A workspace from an earlier try is still in the way"))
        #expect(TaskStartRemedy.failureLine(code: "another_code", args: nil) == nil)
    }

    @Test func onlyTheFirstSuggestedLauncherAndOnlyWithTheFeatureIsOffered() {
        let suggested = task(retry: ["codex", "claude"])
        #expect(TaskStartRemedy.retryLauncher(suggested, features: [TaskStartRemedy.retryLauncherFeature]) == "codex")
        #expect(TaskStartRemedy.retryLauncher(suggested, features: []) == nil)
        #expect(TaskStartRemedy.retryLauncher(task(retry: []), features: [TaskStartRemedy.retryLauncherFeature]) == nil)
        #expect(TaskStartRemedy.retryLauncher(nil, features: [TaskStartRemedy.retryLauncherFeature]) == nil)
    }

    @Test func shellQuestionsParseAllWireFieldsAndKeepTheirAnswerOrder() throws {
        let q = try #require(ShellQuestion(args: [
            "line": "Trust this host?", "choices": " y, n, enter ,cancel ", "kind": "ssh_host_key",
            "screen": "terminal output", "line_mode": "true", "secret": "true", "default": "n",
            "kept_until": "2026-11-20T12:00:00.123Z",
        ]))
        #expect(q.choices == ["y", "n", "enter", "cancel"])
        #expect(q.kind == "ssh_host_key" && q.screen == "terminal output" && q.lineMode && q.secret)
        #expect(q.defaultChoice == "n" && q.keptAt != nil && q.keptClock != nil)
        #expect(q.label("enter") == String(localized: "Enter (no)"))
        #expect(q.label("cancel") == String(localized: "Cancel (Ctrl-C)"))
        #expect(ShellQuestion(args: ["line": "No answers"]) == nil)
        #expect(ShellQuestion(args: ["choices": "y,n"]) == nil)
    }

    @Test func onlyAValidFutureExpiryClosesAQuestion() throws {
        let now = Date(timeIntervalSince1970: 1_800_000_000)
        let future = ISO8601DateFormatter().string(from: now.addingTimeInterval(60))
        let past = ISO8601DateFormatter().string(from: now.addingTimeInterval(-1))
        func open(_ expiry: String?) -> ShellQuestion? {
            ShellQuestion(args: ["line": "Continue?", "choices": "y,n", "kept_until": expiry ?? ""])?.open(now: now)
        }
        #expect(open(future) != nil)
        #expect(open(past) == nil)
        #expect(open(nil) != nil)
        #expect(open("not a date") != nil)
    }

    @Test func questionRemediesRequireTheirOwnFeatureAndShellWaitingCode() {
        let args = ["line": "Continue?", "choices": "y,n"]
        let waiting = task(code: "shell_waiting", args: args)
        #expect(TaskStartRemedy.taskQuestion(waiting, features: [TaskStartRemedy.answerFeature]) != nil)
        #expect(TaskStartRemedy.taskQuestion(waiting, features: []) == nil)
        #expect(TaskStartRemedy.taskQuestion(task(code: "launcher_missing", args: args),
                                             features: [TaskStartRemedy.answerFeature]) == nil)
        #expect(TaskStartRemedy.sessionQuestion(code: "shell_waiting", args: args,
                                                features: [TaskStartRemedy.continueFeature]) != nil)
        #expect(TaskStartRemedy.sessionQuestion(code: "launcher_missing", args: args,
                                                features: [TaskStartRemedy.continueFeature]) == nil)
        #expect(TaskStartRemedy.sessionQuestion(code: "shell_waiting", args: args, features: []) == nil)
    }

    @Test func retryRequestsAreBodylessOrContainOnlyTheLauncher() async throws {
        let hub = FakeHub()
        let path = "/api/v1/todos/t/retry"
        hub.on("POST", path, json: #"{"todo":{"id":"t","title":"After"}}"#)
        let ordinary = try await hub.client.retryTodo("t")
        #expect(ordinary.title == "After")
        #expect(hub.sent("POST", path).last?.body == nil)
        let alternate = try await hub.client.retryTodo("t", launcher: "codex")
        #expect(alternate.id == "t")
        let request = try #require(hub.sent("POST", path).last)
        let body = try #require(request.body)
        #expect(try JSONSerialization.jsonObject(with: body) as? [String: String] == ["launcher": "codex"])
    }

    @Test func startupAnswersUseTheirExactRoutesAndChoiceBodies() async throws {
        let hub = FakeHub()
        hub.on("POST", "/api/v1/todos/t/start/answer", json: #"{"todo":{"id":"t","title":"After"}}"#)
        let todo = try await hub.client.answerTaskStart("t", choice: "y")
        #expect(todo?.title == "After")
        var body = try #require(hub.sent("POST", "/api/v1/todos/t/start/answer").first?.body)
        #expect(try JSONSerialization.jsonObject(with: body) as? [String: String] == ["choice": "y"])

        hub.on("POST", "/api/v1/sessions/box/s/start/continue",
               json: #"{"session":{"machine_id":"box","session_id":"s","cwd":"/work"}}"#)
        let session = try await hub.client.continueStart(SessionKey(machineID: "box", sessionID: "s"), choice: "enter")
        #expect(session?.sessionID == "s")
        body = try #require(hub.sent("POST", "/api/v1/sessions/box/s/start/continue").first?.body)
        #expect(try JSONSerialization.jsonObject(with: body) as? [String: String] == ["choice": "enter"])
    }

    @Test func storeRetryWorksWithoutAPlanIDAndAppliesTheReturnedTodo() async {
        let hub = FakeHub()
        let before = TodoItem(id: "t", title: "Before", startError: "failed")
        let after = TodoItem(id: "t", title: "After")
        hub.on("POST", "/api/v1/todos/t/retry", json: #"{"todo":{"id":"t","title":"After"}}"#)
        let todos = TodoStore(cachedTodos: [before], cacheWriter: nil, hubListChanged: {},
                              read: { _, _ in TodosPage(list: [after]) })
        let store = TasksStore(todoStore: todos, toast: { _ in })
        #expect(before.planID == nil)
        #expect(await store.retry(before, features: [], api: hub.client))
        #expect(todos.byID["t"]?.title == "After")
        #expect(todos.byID["t"]?.startError == nil)
        #expect(hub.sent("POST", "/api/v1/todos/t/retry").first?.body == nil)
    }

    @Test func storeRefusesAnOverrideWithoutTheFeatureBeforeAnyRequest() async {
        let hub = FakeHub()
        let todo = TodoItem(id: "t", title: "Task", startError: "failed")
        let store = TasksStore(todoStore: TodoStore(cachedTodos: [todo], cacheWriter: nil, hubListChanged: {}), toast: { _ in })
        #expect(await !store.retry(todo, launcher: "codex", features: [], api: hub.client))
        #expect(hub.requests.isEmpty)
        #expect(store.busyRetries.isEmpty)
    }
}
