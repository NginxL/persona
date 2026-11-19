import Foundation
import Observation
import Testing
@testable import Persona
import PersonaWire

@MainActor
private final class TodoObservationFlag { var changed = false }

/// Captures the FakeHub answer at request start, then deliberately ignores cancellation until released.
@MainActor
private final class TodoReadGate {
    let hub = FakeHub()
    private(set) var reads = 0
    private var pending: [Int: (Result<Data, Error>, CheckedContinuation<Data, Error>)] = [:]
    private var closed = false
    var client: APIClient {
        var api = hub.client
        api.transport = { [self] request in try await receive(request) }
        return api
    }
    private func receive(_ request: LocalRequest) async throws -> Data {
        guard !closed else { throw CancellationError() }
        guard request.method == "GET", request.path == "/api/v1/todos" else {
            return try await hub.client.transport!(request)
        }
        let result: Result<Data, Error>
        do { result = .success(try await hub.client.transport!(request)) }
        catch { result = .failure(error) }
        reads += 1
        let index = reads
        return try await withCheckedThrowingContinuation { pending[index] = (result, $0) }
    }
    func answer(_ index: Int) throws {
        let removed = pending.removeValue(forKey: index)
        let item = try #require(removed)
        item.1.resume(with: item.0)
    }
    func close() {
        closed = true
        let remaining = pending.values
        pending = [:]
        for item in remaining { item.1.resume(throwing: CancellationError()) }
    }
}

@MainActor
struct TodoCacheTests {
    @Test func requestsDuringARefreshCoalesceIntoOneFollowup() async {
        let hub = FakeHub()
        let store = TodoStore(cachedTodos: [], cacheWriter: nil, hubListChanged: {})
        defer { store.forget() }
        hub.on("GET", "/api/v1/todos", json: #"{"todos":[{"id":"first","title":"First"}]}"#)
        var client = hub.client
        client.transport = { request in
            let result = try await hub.client.transport!(request)
            if request.path == "/api/v1/todos", hub.sent("GET", request.path).count == 1 {
                await MainActor.run {
                    store.reload(api: hub.client)
                    store.reload(api: hub.client)
                }
                hub.on("GET", "/api/v1/todos", json: #"{"todos":[{"id":"second","title":"Second"}]}"#)
            }
            return result
        }
        let reload = store.reload(api: client)
        await reload.value
        #expect(hub.sent("GET", "/api/v1/todos").count == 2)
        #expect(store.todos.map(\.id) == ["second"])
        #expect(store.byID["first"] == nil)
        #expect(store.byID["second"]?.title == "Second")
    }

    /// The list is read with the tag of the last one; when the hub says it did not change (304) that list is applied
    /// again without being read (#9590); a new one replaces it and its tag.
    @Test func aListThatDidNotChangeIsNotReadAgain() async throws {
        var asked: [String?] = []
        var answers: [TodosPage] = [
            TodosPage(list: [TodoItem(id: "a", title: "A")], etag: "t1"), TodosPage(list: nil, etag: "t1"),
            TodosPage(list: [TodoItem(id: "b", title: "B")], etag: "t2"),
        ]
        let store = TodoStore(cachedTodos: [], cacheWriter: nil, hubListChanged: {}, read: { _, etag in
            asked.append(etag)
            return answers.removeFirst()
        })
        let api = FakeHub().client
        await store.refresh(api: api)
        #expect(store.todos.map(\.id) == ["a"])
        await store.refresh(api: api)
        #expect(asked == [nil, "t1"])
        #expect(store.todos.map(\.id) == ["a"], "unchanged: the last list stands")
        await store.refresh(api: api)
        #expect(asked == [nil, "t1", "t1"])
        #expect(store.todos.map(\.id) == ["b"])
        store.forget()
        answers = [TodosPage(list: [TodoItem(id: "c", title: "C")], etag: "t3")]
        await store.refresh(api: api)
        #expect(asked.last == .some(nil), "another hub: no tag of the last one's")
    }

    @Test func taskReadersShareCachedAndMutatedTodos() async throws {
        let cached = TodoItem(id: "cached", title: "Cached")
        let store = TodoStore(cachedTodos: [cached], cacheWriter: nil, hubListChanged: {})
        let tasks = TasksStore(todoStore: store)
        let hub = FakeHub()
        #expect(tasks.todos == ["cached": cached])
        hub.on("POST", "/api/v1/todos", json: #"{"todo":{"id":"new","title":"New"}}"#)
        _ = try await store.create(title: "New", api: hub.client)
        #expect(tasks.todos["new"]?.title == "New")
        #expect(tasks.todos == store.byID)
        hub.on("PATCH", "/api/v1/todos/new", json: #"{"todo":{"id":"new","title":"Edited"}}"#)
        _ = try await store.update(id: "new", title: "Edited", api: hub.client)
        #expect(tasks.todos["new"]?.title == "Edited")
        #expect(tasks.todos == store.byID)
        hub.on("DELETE", "/api/v1/todos/new", json: "{}")
        try await store.delete(id: "new", api: hub.client)
        #expect(tasks.todos["new"] == nil)
        #expect(tasks.todos == store.byID)
        hub.on("GET", "/api/v1/todos", json: #"{"todos":[{"id":"remote","title":"Remote"}]}"#)
        await store.reload(api: hub.client).value
        #expect(tasks.todos.keys.sorted() == ["remote"])
        #expect(tasks.todos == store.byID)
        tasks.forget()
        #expect(tasks.todos["remote"]?.title == "Remote")
        store.forget()
        #expect(tasks.todos.isEmpty && store.byID.isEmpty)
    }

    /// Calls the two stores manually in didConnect order; this does not execute AppModel.didConnect.
    @Test func reconnectionRefreshesTwoShownFeedsButOnlyOneTodoList() async {
        let hub = FakeHub()
        let store = TodoStore(cachedTodos: [], cacheWriter: nil, hubListChanged: {})
        let tasks = TasksStore(todoStore: store)
        for conversation in ["one", "two"] {
            hub.on("GET", "/api/v1/assistant/conversations/\(conversation)/feed", json: #"{"items":[]}"#)
            await tasks.load(conversation, refreshTodos: false, api: hub.client)?.value
        }
        hub.on("GET", "/api/v1/todos", json: #"{"todos":[]}"#)
        let reload = store.reload(api: hub.client)
        let feeds = tasks.reconnected(api: hub.client)
        await reload.value
        for feed in feeds { await feed.value }
        #expect(hub.sent("GET", "/api/v1/todos").count == 1)
        #expect(hub.sent("GET", "/api/v1/assistant/conversations/one/feed").count == 2)
        #expect(hub.sent("GET", "/api/v1/assistant/conversations/two/feed").count == 2)
    }

    /// Exercises TodoStore.reload alone; AppModel's todos event wiring is outside this test's scope.
    @Test func oneReloadRequestsOneList() async {
        let hub = FakeHub()
        let store = TodoStore(cachedTodos: [], cacheWriter: nil, hubListChanged: {})
        let tasks = TasksStore(todoStore: store)
        hub.on("GET", "/api/v1/todos", json: #"{"todos":[{"id":"event","title":"Event"}]}"#)
        await store.reload(api: hub.client).value
        #expect(hub.sent("GET", "/api/v1/todos").count == 1)
        #expect(tasks.todos["event"]?.title == "Event")
    }

    @Test func onlyChangedHubListsPushTheWatchSummary() async throws {
        var pushes = 0
        let store = TodoStore(cachedTodos: [], cacheWriter: nil, hubListChanged: { pushes += 1 })
        let hub = FakeHub()
        hub.on("POST", "/api/v1/todos", json: #"{"todo":{"id":"t","title":"Local"}}"#)
        _ = try await store.create(title: "Local", api: hub.client)
        #expect(pushes == 0)
        hub.on("GET", "/api/v1/todos", json: #"{"todos":[{"id":"t","title":"Hub"}]}"#)
        await store.refresh(api: hub.client)
        #expect(pushes == 1)
        await store.refresh(api: hub.client)
        #expect(pushes == 1)
        hub.on("PATCH", "/api/v1/todos/t", json: #"{"todo":{"id":"t","title":"Edited"}}"#)
        _ = try await store.update(id: "t", title: "Edited", api: hub.client)
        #expect(pushes == 1)
        hub.on("DELETE", "/api/v1/todos/t", json: "{}")
        try await store.delete(id: "t", api: hub.client)
        #expect(pushes == 1)
    }

    @Test func observationFollowsTheSharedIndex() async throws {
        let store = TodoStore(cachedTodos: [], cacheWriter: nil, hubListChanged: {})
        let tasks = TasksStore(todoStore: store)
        let observed = TodoObservationFlag()
        withObservationTracking { _ = tasks.todos["t"] } onChange: {
            MainActor.assumeIsolated { observed.changed = true }
        }
        let hub = FakeHub()
        hub.on("POST", "/api/v1/todos", json: #"{"todo":{"id":"t","title":"New"}}"#)
        _ = try await store.create(title: "New", api: hub.client)
        #expect(observed.changed)
        #expect(tasks.todos["t"]?.title == "New")
    }

    @Test func aHubEchoAfterALocalEditPushesTheWatchSummary() async throws {
        var pushes = 0
        let store = TodoStore(cachedTodos: [], cacheWriter: nil, hubListChanged: { pushes += 1 })
        let hub = FakeHub()
        hub.on("GET", "/api/v1/todos", json: #"{"todos":[{"id":"t","title":"Hub"}]}"#)
        await store.refresh(api: hub.client)
        #expect(pushes == 1)
        hub.on("PATCH", "/api/v1/todos/t", json: #"{"todo":{"id":"t","title":"Edited"}}"#)
        _ = try await store.update(id: "t", title: "Edited", api: hub.client)
        #expect(pushes == 1)
        hub.on("GET", "/api/v1/todos", json: #"{"todos":[{"id":"t","title":"Edited"}]}"#)
        await store.refresh(api: hub.client)
        #expect(pushes == 2)
    }

    @Test func cachedTodosAndForgetKeepTheLastPushedComparison() async {
        var pushes = 0
        let store = TodoStore(cachedTodos: [TodoItem(id: "t", title: "Cached")], cacheWriter: nil,
                              hubListChanged: { pushes += 1 })
        let hub = FakeHub()
        hub.on("GET", "/api/v1/todos", json: #"{"todos":[{"id":"t","title":"Cached"}]}"#)
        await store.refresh(api: hub.client)
        #expect(pushes == 0)
        store.forget()
        hub.on("GET", "/api/v1/todos", json: #"{"todos":[{"id":"new","title":"New"}]}"#)
        await store.refresh(api: hub.client)
        #expect(pushes == 1)
        store.forget()
        hub.on("GET", "/api/v1/todos", json: #"{"todos":[]}"#)
        await store.refresh(api: hub.client)
        #expect(pushes == 2)
    }

    @Test func forgetDiscardsAnOldResponseWithoutPersistingIt() async throws {
        var writes: [[TodoItem]] = []
        var pushes = 0
        let store = TodoStore(cachedTodos: [], cacheWriter: { writes.append($0) }, hubListChanged: { pushes += 1 })
        let hub = FakeHub()
        var old = hub.client
        old.transport = { _ in
            await MainActor.run { store.forget() }
            return Data(#"{"todos":[{"id":"old","title":"Old","spaces":[]}]}"#.utf8)
        }
        await store.refresh(api: old)
        #expect(store.todos.isEmpty && store.byID.isEmpty)
        #expect(store.error == nil && store.isSupported && !store.spacesSupported && !store.loading)
        #expect(pushes == 0)
        // Observe the real debounce through an in-memory writer, never the application cache file.
        for _ in 0..<150 where writes.isEmpty { try await Task.sleep(for: .milliseconds(20)) }
        #expect(writes == [[]]) // Only forget's empty snapshot, never the old response.
        hub.on("GET", "/api/v1/todos", json: #"{"todos":[{"id":"new","title":"New","spaces":[]}]}"#)
        await store.reload(api: hub.client).value
        #expect(store.byID.keys.sorted() == ["new"])
        #expect(store.spacesSupported)
        #expect(pushes == 1)
    }

    @Test(arguments: [404, 500]) func forgetDiscardsOldFetchErrors(_ status: Int) async {
        let store = TodoStore(cachedTodos: [], cacheWriter: nil, hubListChanged: {})
        let hub = FakeHub()
        var old = hub.client
        old.transport = { _ in
            await MainActor.run {
                store.forget()
                store.error = "Current hub error"
            }
            throw APIError(status: status, message: "Old hub error")
        }
        await store.refresh(api: old)
        #expect(store.todos.isEmpty && store.byID.isEmpty)
        #expect(store.isSupported && !store.spacesSupported && !store.loading)
        #expect(store.error == "Current hub error")
    }

    @Test(arguments: ["linkAgent", "unlinkAgent", "setSpace"])
    func membershipFollowupDiscardsAnOldResponseAfterForget(_ operation: String) async throws {
        var pushes = 0
        let store = TodoStore(cachedTodos: [], cacheWriter: nil, hubListChanged: { pushes += 1 })
        let hub = FakeHub()
        var client = hub.client
        client.transport = { request in
            if request.method == "GET" {
                await MainActor.run { store.forget() }
                return Data(#"{"todos":[{"id":"old","title":"Old","spaces":[]}]}"#.utf8)
            }
            return Data("{}".utf8)
        }
        switch operation {
        case "linkAgent": try await store.linkAgent(todoID: "t", machineID: "m", sessionID: "s", api: client)
        case "unlinkAgent": try await store.unlinkAgent(todoID: "t", machineID: "m", sessionID: "s", api: client)
        default: try await store.setSpace(todoID: "t", space: .init(machineID: "m", space: "p"), linked: true, api: client)
        }
        #expect(store.todos.isEmpty && store.byID.isEmpty)
        #expect(store.isSupported && !store.spacesSupported)
        #expect(store.error == nil && pushes == 0)
    }

    @Test func writeOperationsDiscardOldResponsesAndRollbacksAfterForget() async throws {
        let store = TodoStore(cachedTodos: [], cacheWriter: nil, hubListChanged: {})
        let hub = FakeHub()

        // 1. create in-flight when forget happens
        var createClient = hub.client
        createClient.transport = { _ in
            await MainActor.run { store.forget() }
            return Data(#"{"todo":{"id":"created","title":"Created"}}"#.utf8)
        }
        _ = try await store.create(title: "Created", api: createClient)
        #expect(store.todos.isEmpty)
        #expect(store.byID["created"] == nil)

        // 2. update in-flight when forget happens
        var updateClient = hub.client
        updateClient.transport = { _ in
            await MainActor.run { store.forget() }
            return Data(#"{"todo":{"id":"updated","title":"Updated"}}"#.utf8)
        }
        _ = try await store.update(id: "updated", title: "Updated", api: updateClient)
        #expect(store.todos.isEmpty)
        #expect(store.byID["updated"] == nil)

        // 3. toggleDone in-flight when forget happens and PATCH fails (revert must not restore original)
        let originalItem = TodoItem(id: "orig", title: "Original", doneAt: nil)
        let storeWithItem = TodoStore(cachedTodos: [originalItem], cacheWriter: nil, hubListChanged: {})
        var toggleClient = hub.client
        toggleClient.transport = { _ in
            await MainActor.run { storeWithItem.forget() }
            throw APIError(status: 500, message: "Server error")
        }
        storeWithItem.toggleDone(originalItem, api: toggleClient)
        // Give background Task a chance to hit the catch block
        for _ in 0..<50 where !storeWithItem.todos.isEmpty {
            try await Task.sleep(for: .milliseconds(10))
        }
        #expect(storeWithItem.todos.isEmpty)
        #expect(storeWithItem.byID.isEmpty)

        // 4. setSpace in-flight when forget happens and link fails (revert must not restore original)
        let spaceStore = TodoStore(cachedTodos: [originalItem], cacheWriter: nil, hubListChanged: {})
        var spaceClient = hub.client
        spaceClient.transport = { _ in
            await MainActor.run { spaceStore.forget() }
            throw APIError(status: 500, message: "Link failure")
        }
        do {
            _ = try await spaceStore.setSpace(todoID: "orig", space: .init(machineID: "m", space: "sp"), linked: true, api: spaceClient)
        } catch {}
        #expect(spaceStore.todos.isEmpty)
        #expect(spaceStore.byID.isEmpty)
    }

    @Test func reloadDoesNotWaitForSlowPriorGenerationFetch() async throws {
        let store = TodoStore(cachedTodos: [], cacheWriter: nil, hubListChanged: {})
        let hub = FakeHub()

        let slowFetchStarted = AsyncStream<Void>.makeStream()
        let allowSlowFetchToFinish = AsyncStream<Void>.makeStream()

        var slowClient = hub.client
        slowClient.transport = { request in
            if request.method == "GET" && request.path == "/api/v1/todos" {
                slowFetchStarted.continuation.yield()
                // Wait for explicit signal before finishing old fetch
                for await _ in allowSlowFetchToFinish.stream { break }
                return Data(#"{"todos":[{"id":"old","title":"Old"}]}"#.utf8)
            }
            return Data("{}".utf8)
        }

        // Start old reload
        let oldTask = store.reload(api: slowClient)
        for await _ in slowFetchStarted.stream { break }

        // Switch hub
        store.forget()
        #expect(store.todos.isEmpty)

        // New hub reload starts immediately without waiting for oldTask
        hub.on("GET", "/api/v1/todos", json: #"{"todos":[{"id":"new","title":"New"}]}"#)
        let newTask = store.reload(api: hub.client)
        await newTask.value

        #expect(store.todos.map(\.id) == ["new"])
        #expect(store.byID["new"]?.title == "New")

        // Now allow old fetch to finish and verify it does not overwrite new hub's todos
        allowSlowFetchToFinish.continuation.yield()
        await oldTask.value

        #expect(store.todos.map(\.id) == ["new"])
        #expect(store.byID["new"]?.title == "New")
        #expect(store.byID["old"] == nil)
    }

    @Test func loadingRemainsTrueWhenOldGenerationFetchCompletesDuringNewFetch() async throws {
        let store = TodoStore(cachedTodos: [], cacheWriter: nil, hubListChanged: {})
        let hub = FakeHub()

        let oldFetchStarted = AsyncStream<Void>.makeStream()
        let allowOldFetchToFinish = AsyncStream<Void>.makeStream()

        var oldClient = hub.client
        oldClient.transport = { request in
            if request.method == "GET" && request.path == "/api/v1/todos" {
                oldFetchStarted.continuation.yield()
                for await _ in allowOldFetchToFinish.stream { break }
                return Data(#"{"todos":[{"id":"old","title":"Old"}]}"#.utf8)
            }
            return Data("{}".utf8)
        }

        let oldTask = store.reload(api: oldClient)
        for await _ in oldFetchStarted.stream { break }
        #expect(store.loading == true)

        store.forget()
        #expect(store.loading == false)

        let newFetchStarted = AsyncStream<Void>.makeStream()
        let allowNewFetchToFinish = AsyncStream<Void>.makeStream()

        var newClient = hub.client
        newClient.transport = { request in
            if request.method == "GET" && request.path == "/api/v1/todos" {
                newFetchStarted.continuation.yield()
                for await _ in allowNewFetchToFinish.stream { break }
                return Data(#"{"todos":[{"id":"new","title":"New"}]}"#.utf8)
            }
            return Data("{}".utf8)
        }

        let newTask = store.reload(api: newClient)
        for await _ in newFetchStarted.stream { break }
        #expect(store.loading == true)

        // Old fetch returns while new fetch is still in flight
        allowOldFetchToFinish.continuation.yield()
        await oldTask.value

        // store.loading must remain true because old fetch generation mismatch did not clear loading
        #expect(store.loading == true)

        // New fetch finishes and clears loading
        allowNewFetchToFinish.continuation.yield()
        await newTask.value
        #expect(store.loading == false)
        #expect(store.todos.map(\.id) == ["new"])
    }

    @Test func sustainedTodoEventsShareReadsAndLandTheCompleteFinalSnapshot() async throws {
        let gate = TodoReadGate()
        let store = TodoStore(cachedTodos: [], cacheWriter: nil, hubListChanged: {})
        defer { store.forget(); gate.close() }
        gate.hub.on("GET", "/api/v1/todos", json: #"{"todos":[{"id":"initial","title":"Initial"}]}"#)
        var firstFinished = false
        let first = store.reload(api: gate.client)
        let observer = Task { await first.value; firstFinished = true }
        defer { observer.cancel() }
        try await waitForRefreshTest { gate.reads == 1 }
        gate.hub.on("GET", "/api/v1/todos", json: #"{"todos":[{"id":"middle","title":"Middle"}]}"#)
        for _ in 0..<50 { store.reload(api: gate.client) }
        #expect(gate.reads == 1)
        try gate.answer(1)
        try await waitForRefreshTest { gate.reads == 2 }
        gate.hub.on("GET", "/api/v1/todos", json: #"{"todos":[{"id":"final","title":"Final"}]}"#)
        var final = first
        for _ in 0..<50 { final = store.reload(api: gate.client) }
        #expect(gate.reads == 2)
        try gate.answer(2)
        try await waitForRefreshTest { firstFinished && gate.reads == 3 }
        #expect(store.byID.keys.sorted() == ["middle"])
        try gate.answer(3)
        await final.value
        #expect(gate.hub.sent("GET", "/api/v1/todos").count == 3)
        #expect(store.todos.map(\.id) == ["final"])
        #expect(!store.loading && store.error == nil)
    }

    @Test func manualAndAutomaticTodoReadersShareTheInFlightGet() async throws {
        let gate = TodoReadGate()
        let store = TodoStore(cachedTodos: [], cacheWriter: nil, hubListChanged: {})
        defer { store.forget(); gate.close() }
        gate.hub.on("GET", "/api/v1/todos", json: #"{"todos":[{"id":"shared","title":"Shared"}]}"#)
        let automatic = store.reload(api: gate.client)
        try await waitForRefreshTest { gate.reads == 1 }
        var manualEntered = false
        let manual = Task {
            manualEntered = true
            await store.refresh(api: gate.client)
        }
        defer { manual.cancel() }
        try await waitForRefreshTest { manualEntered }
        #expect(gate.reads == 1 && store.loading)
        try gate.answer(1)
        await automatic.value
        await manual.value
        #expect(gate.hub.sent("GET", "/api/v1/todos").count == 1)
        #expect(store.byID["shared"]?.title == "Shared")
    }

    @Test(arguments: ["create", "update", "delete"])
    func localWritesRejectTheLatePreWriteSnapshot(_ operation: String) async throws {
        let gate = TodoReadGate()
        let store = TodoStore(cachedTodos: [TodoItem(id: "t", title: "Before")], cacheWriter: nil, hubListChanged: {})
        defer { store.forget(); gate.close() }
        gate.hub.on("GET", "/api/v1/todos", json: #"{"todos":[{"id":"t","title":"Before"}]}"#)
        let oldRead = Task { await store.refresh(api: gate.client) }
        defer { oldRead.cancel() }
        try await waitForRefreshTest { gate.reads == 1 }
        switch operation {
        case "create":
            gate.hub.on("POST", "/api/v1/todos", json: #"{"todo":{"id":"new","title":"Created"}}"#)
            _ = try await store.create(title: "Created", api: gate.client)
        case "update":
            gate.hub.on("PATCH", "/api/v1/todos/t", json: #"{"todo":{"id":"t","title":"Edited"}}"#)
            _ = try await store.update(id: "t", title: "Edited", api: gate.client)
        default:
            gate.hub.on("DELETE", "/api/v1/todos/t", json: "{}")
            try await store.delete(id: "t", api: gate.client)
        }
        let afterWrite = store.todos
        try gate.answer(1)
        await oldRead.value
        #expect(store.todos == afterWrite)
        #expect(gate.hub.sent("GET", "/api/v1/todos").count == 1)
        switch operation {
        case "create": #expect(store.byID["new"]?.title == "Created")
        case "update": #expect(store.byID["t"]?.title == "Edited")
        default: #expect(store.todos.isEmpty)
        }
    }

    @Test func aManualRefreshAfterALocalWriteDoesNotJoinTheStaleRead() async throws {
        let gate = TodoReadGate()
        let store = TodoStore(cachedTodos: [TodoItem(id: "t", title: "Before")], cacheWriter: nil, hubListChanged: {})
        defer { store.forget(); gate.close() }
        gate.hub.on("GET", "/api/v1/todos", json: #"{"todos":[{"id":"t","title":"Before"}]}"#)
        let old = Task { await store.refresh(api: gate.client) }
        defer { old.cancel() }
        try await waitForRefreshTest { gate.reads == 1 }
        gate.hub.on("PATCH", "/api/v1/todos/t", json: #"{"todo":{"id":"t","title":"Edited"}}"#)
        _ = try await store.update(id: "t", title: "Edited", api: gate.client)
        gate.hub.on("GET", "/api/v1/todos", json: #"{"todos":[{"id":"t","title":"Confirmed"}]}"#)
        var manualEntered = false
        var manualFinished = false
        let manual = Task {
            manualEntered = true
            await store.refresh(api: gate.client)
            manualFinished = true
        }
        defer { manual.cancel() }
        try await waitForRefreshTest { manualEntered }
        #expect(gate.reads == 1 && !manualFinished)
        try gate.answer(1)
        await old.value
        try await waitForRefreshTest { gate.reads == 2 }
        #expect(store.byID["t"]?.title == "Edited" && !manualFinished)
        try gate.answer(2)
        await manual.value
        #expect(store.byID["t"]?.title == "Confirmed")
        #expect(gate.hub.sent("GET", "/api/v1/todos").count == 2)
    }

    @Test(arguments: ["linkAgent", "unlinkAgent", "setSpace"])
    func membershipConfirmationUsesANewGetAndIgnoresThePreWriteGet(_ operation: String) async throws {
        let gate = TodoReadGate()
        var pushes = 0
        let store = TodoStore(cachedTodos: [], cacheWriter: nil, hubListChanged: { pushes += 1 })
        defer { store.forget(); gate.close() }
        gate.hub.on("GET", "/api/v1/todos", json: #"{"todos":[{"id":"old","title":"Before"}]}"#)
        let oldRead = Task { await store.refresh(api: gate.client) }
        defer { oldRead.cancel() }
        try await waitForRefreshTest { gate.reads == 1 }
        let method = operation == "unlinkAgent" ? "DELETE" : "PUT"
        let path = operation == "setSpace" ? TodosLogic.spacePath(todoID: "t", machineID: "m", space: "p") : "/api/v1/todos/t/agents/m/s"
        gate.hub.on(method, path, json: "{}")
        gate.hub.on("GET", "/api/v1/todos", json: #"{"todos":[{"id":"new","title":"After","spaces":[]}]}"#)
        let write = Task {
            switch operation {
            case "linkAgent": try await store.linkAgent(todoID: "t", machineID: "m", sessionID: "s", api: gate.client)
            case "unlinkAgent": try await store.unlinkAgent(todoID: "t", machineID: "m", sessionID: "s", api: gate.client)
            default: _ = try await store.setSpace(todoID: "t", space: .init(machineID: "m", space: "p"), linked: true, api: gate.client)
            }
        }
        defer { write.cancel() }
        try await waitForRefreshTest { gate.hub.sent(method, path).count == 1 }
        // Let the completed in-memory write enqueue its confirmation; no simulated time advances.
        for _ in 0..<20 { await Task.yield() }
        #expect(gate.reads == 1)
        try gate.answer(1)
        try await waitForRefreshTest { gate.reads == 2 }
        #expect(store.byID["old"] == nil && pushes == 0)
        try gate.answer(2)
        try await write.value
        await oldRead.value
        #expect(store.byID.keys.sorted() == ["new"])
        #expect(pushes == 1 && !store.loading)
        #expect(gate.hub.sent("GET", "/api/v1/todos").count == 2)
    }

    @Test func aNewTodoEventRecoversAfterAReadError() async throws {
        let gate = TodoReadGate()
        let store = TodoStore(cachedTodos: [], cacheWriter: nil, hubListChanged: {})
        defer { store.forget(); gate.close() }
        gate.hub.on("GET", "/api/v1/todos") { _ in throw APIError(status: 500, message: "Temporary failure") }
        let failed = store.reload(api: gate.client)
        try await waitForRefreshTest { gate.reads == 1 }
        try gate.answer(1)
        await failed.value
        #expect(store.error != nil && !store.loading)
        gate.hub.on("GET", "/api/v1/todos", json: #"{"todos":[{"id":"recovered","title":"Recovered"}]}"#)
        let next = store.reload(api: gate.client)
        try await waitForRefreshTest { gate.reads == 2 }
        try gate.answer(2)
        await next.value
        #expect(store.error == nil && store.byID["recovered"] != nil)
        #expect(gate.hub.sent("GET", "/api/v1/todos").count == 2)
    }

    @Test func aCancelledViewWaitDoesNotCancelAnotherTodoReader() async throws {
        let gate = TodoReadGate()
        let store = TodoStore(cachedTodos: [], cacheWriter: nil, hubListChanged: {})
        defer { store.forget(); gate.close() }
        gate.hub.on("GET", "/api/v1/todos", json: #"{"todos":[{"id":"shared","title":"Shared"}]}"#)
        let automatic = store.reload(api: gate.client)
        try await waitForRefreshTest { gate.reads == 1 }
        var viewEntered = false
        let view = Task {
            viewEntered = true
            await store.refresh(api: gate.client)
        }
        try await waitForRefreshTest { viewEntered }
        view.cancel()
        await view.value
        #expect(store.loading && gate.reads == 1)
        try gate.answer(1)
        await automatic.value
        #expect(store.byID["shared"] != nil && !store.loading)
        #expect(gate.hub.sent("GET", "/api/v1/todos").count == 1)
    }

    @Test func forgetEndsTodoWaitsBeforeTheOldTransportReturns() async throws {
        let gate = TodoReadGate()
        let store = TodoStore(cachedTodos: [], cacheWriter: nil, hubListChanged: {})
        defer { store.forget(); gate.close() }
        gate.hub.on("GET", "/api/v1/todos", json: #"{"todos":[{"id":"old","title":"Old"}]}"#)
        let old = store.reload(api: gate.client)
        try await waitForRefreshTest { gate.reads == 1 }
        store.forget()
        await old.value // This ends while the cancellation-ignoring transport is still held.
        gate.hub.on("GET", "/api/v1/todos", json: #"{"todos":[{"id":"new","title":"New"}]}"#)
        let new = store.reload(api: gate.client)
        try await waitForRefreshTest { gate.reads == 2 }
        try gate.answer(1)
        for _ in 0..<20 { await Task.yield() }
        #expect(store.loading && store.todos.isEmpty)
        try gate.answer(2)
        await new.value
        #expect(store.byID.keys.sorted() == ["new"] && !store.loading)
    }

    // MARK: The V1 list's views and counts (#10465)

    /// Views and counts come with the list, apart from its rows, and a 304 applies the ones read with the tag again.
    @Test func viewsAndCountsSurviveAListThatDidNotChange() async throws {
        let view = TodoSummary(phase: TodoPhase.working, section: TodoListSection.working, reason: TodoReason.agentRunning, rank: 0)
        let counts = TodoCounts(open: 1, needsYou: 0, inProgress: 1, sections: [TodoListSection.working: 1])
        var answers = [TodosPage(list: [TodoItem(id: "a", title: "A")], views: ["a": view], counts: counts, etag: "t1"),
                       TodosPage(list: nil, etag: "t1")]
        let store = TodoStore(cachedTodos: [], cacheWriter: nil, hubListChanged: {}, read: { _, _ in answers.removeFirst() },
                              v1: .on(features: TodoFeatures(view: true)))
        defer { store.forget() }
        let api = FakeHub().client
        await store.refresh(api: api)
        #expect(store.views == ["a": view] && store.counts == counts)
        await store.refresh(api: api)
        #expect(answers.isEmpty)
        #expect(store.views == ["a": view] && store.counts == counts, "a 304: the views and counts of the tagged list stand")
        #expect(store.todo(id: "a")?.title == "A")
    }

    /// A row whose view alone changed (its run's latest line) is no news for the watch and the widgets, and nothing
    /// that reads the rows is drawn again (P5).
    @Test func aViewOnlyChangeLeavesTheRowsAlone() async throws {
        var pushes = 0
        let hub = FakeHub()
        let store = TodoStore(cachedTodos: [], cacheWriter: nil, hubListChanged: { pushes += 1 }, v1: .on())
        defer { store.forget() }
        let row = { (line: String) in
            TodoViewJSON.list([TodoViewJSON.row("t", view: TodoViewJSON.view(TodoListSection.working, line: line))],
                              counts: TodoViewJSON.counts(needsYou: 0, inProgress: 1, open: 1, sections: [TodoListSection.working: 1]))
        }
        hub.on("GET", "/api/v1/todos", json: row("persona_working"))
        hub.on("GET", "/api/v1/todos/places", json: #"{"places":[{"id":"all","kind":"all","title":"","needs_you":0}],"counts":{}}"#)
        await store.refresh(api: hub.client)
        #expect(pushes == 1 && store.views["t"]?.line?.key == "persona_working")
        let observed = TodoObservationFlag()
        withObservationTracking { _ = store.todos } onChange: { MainActor.assumeIsolated { observed.changed = true } }
        hub.on("GET", "/api/v1/todos", json: row("agent_running"))
        await store.refresh(api: hub.client)
        #expect(store.views["t"]?.line?.key == "agent_running")
        #expect(pushes == 1, "only the facts tell the watch and the widgets")
        #expect(!observed.changed, "the rows did not change")
    }

    /// While V1 is on the views, counts and places are kept on disk beside the rows, with the numbers the pages showed;
    /// a new store draws them before its first read, its numbers included. An entry none of whose views is for the kept
    /// rows (another hub's) is not used at all.
    @Test func viewsAreKeptOnDiskWhileV1IsOn() async throws {
        var saved: [TodoViewCache?] = []
        let hub = FakeHub()
        hub.on("GET", "/api/v1/todos", json: TodoViewJSON.list([TodoViewJSON.row("t", view: TodoViewJSON.view(TodoListSection.todo))],
                                                              counts: TodoViewJSON.counts(needsYou: 0, open: 1, sections: [TodoListSection.todo: 1])))
        hub.on("GET", "/api/v1/todos/places", json: #"{"places":[{"id":"all","kind":"all","title":"","needs_you":0}],"counts":{}}"#)
        let store = TodoStore(cachedTodos: [], cacheWriter: nil, hubListChanged: {}, v1: .on(saved: { saved.append($0) }))
        await store.refresh(api: hub.client)
        try await waitForTodoTest { !saved.isEmpty }
        let kept = try #require(saved.last ?? nil)
        #expect(kept.views.keys.sorted() == ["t"] && kept.counts?.open == 1 && kept.places?.places.map(\.id) == ["all"])
        #expect(kept.shownCounts == store.countsSource.counts && kept.shownPlaces == store.countsSource.places)

        let next = TodoStore(cachedTodos: store.todos, cacheWriter: nil, hubListChanged: {}, v1: .on(cache: kept))
        defer { next.forget() }
        #expect(next.viewsRead, "kept from the last run: the list draws at once")
        #expect(next.views == kept.views && next.counts == kept.counts && next.places == kept.places)
        #expect(next.countsSource.counts == kept.shownCounts, "the first frame's numbers")
        for rows in [[], [TodoItem(id: "elsewhere", title: "Another hub's")]] {
            let other = TodoStore(cachedTodos: rows, cacheWriter: nil, hubListChanged: {}, v1: .on(cache: kept))
            #expect(!other.viewsRead && other.views.isEmpty && other.counts == nil && other.places == nil)
            other.forget()
        }
        let empty = TodoStore(cachedTodos: [], cacheWriter: nil, hubListChanged: {}, v1: .on(cache: TodoViewCache(counts: kept.counts)))
        #expect(empty.viewsRead && empty.counts == kept.counts, "an empty list's entry")
        empty.forget()
        store.forget()
        try await waitForTodoTest { saved.last == .some(nil) } // forget takes the entry away
    }

    /// While V1 is off the views are read all the same (V1 coming on in this run has them), but nothing else: no cache,
    /// no places, no timer; forgetting the hub still takes away an entry V1 may have written for it.
    @Test func v1OffReadsOnlyTheList() async throws {
        var writes: [TodoViewCache?] = []
        let hub = FakeHub()
        // The store's own reads go through `hub` too, so a timer that armed anyway would show in its count below.
        var off = TodoStore.V1.on(saved: { writes.append($0) }, api: hub.client)
        off.isOn = { false }
        let soon = ISO8601DateFormatter().string(from: Date().addingTimeInterval(0.05))
        hub.on("GET", "/api/v1/todos", json: TodoViewJSON.list([TodoViewJSON.row("t", view: TodoViewJSON.view(TodoListSection.todo, next: soon))]))
        hub.on("GET", "/api/v1/todos/places", json: #"{"places":[],"counts":{}}"#)
        let store = TodoStore(cachedTodos: [], cacheWriter: nil, hubListChanged: {}, v1: off)
        await store.refresh(api: hub.client)
        try await Task.sleep(for: .milliseconds(300))
        #expect(store.views.keys.sorted() == ["t"])
        #expect(writes.isEmpty && hub.sent("GET", "/api/v1/todos/places").isEmpty, "no cache, no places")
        #expect(hub.sent("GET", "/api/v1/todos").count == 1, "no timer")
        #expect(store.viewsRead && store.countsSource.needsYou == 0)
        store.forget()
        #expect(writes == [nil])
    }

    /// V1 coming on in this run (a hello listed the view after the list was read) counts, writes the cache and reads the
    /// places from what the last read had, without waiting for a push; going off lets the numbers go.
    @Test func v1ComingOnBringsWhatOnlyItReadsUp() async throws {
        var on = false
        var saved: [TodoViewCache?] = []
        let hub = FakeHub()
        hub.on("GET", "/api/v1/todos", json: TodoViewJSON.list(
            [TodoViewJSON.row("t", view: TodoViewJSON.view(TodoListSection.needsYou, asks: ["todo:decide@1"]))],
            counts: TodoViewJSON.counts(needsYou: 1, open: 1, sections: [TodoListSection.needsYou: 1])))
        hub.on("GET", "/api/v1/todos/places", json: #"{"places":[{"id":"all","kind":"all","title":"","needs_you":1}],"counts":{"needs_you":1}}"#)
        var v1 = TodoStore.V1.on(saved: { saved.append($0) }, api: hub.client)
        v1.isOn = { on }
        let store = TodoStore(cachedTodos: [], cacheWriter: nil, hubListChanged: {}, v1: v1)
        defer { store.forget() }
        await store.refresh(api: hub.client)
        #expect(store.counts?.needsYou == 1 && store.countsSource.needsYou == 0 && store.places == nil)
        on = true
        store.v1Changed()
        #expect(store.countsSource.needsYou == 1, "counted at once from the last read")
        try await waitForTodoTest { store.places != nil && !saved.isEmpty }
        #expect(hub.sent("GET", "/api/v1/todos/places").count == 1)
        on = false
        store.v1Changed()
        #expect(store.countsSource.needsYou == 0)
    }

    /// The first V1 launch has no views kept: the list waits for the first read to end, answered or failed.
    @Test func theFirstReadEndsTheWait() async throws {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/todos") { _ in throw APIError(status: 503, message: "assistant offline") }
        var slow = TodoStore.V1.on()
        slow.timing.hold = 30
        let store = TodoStore(cachedTodos: [TodoItem(id: "t", title: "Kept")], cacheWriter: nil, hubListChanged: {}, v1: slow)
        defer { store.forget() }
        #expect(!store.viewsRead)
        await store.refresh(api: hub.client)
        #expect(store.viewsRead && store.error != nil && store.todo(id: "t") != nil)
        store.forget()
        #expect(!store.viewsRead, "another hub: wait for its first read")
    }

    /// ...but no longer than a moment: with no read under way (no connection yet), or one that does not answer, the
    /// rows are drawn by the app's own rules.
    @Test func theWaitForTheFirstReadIsBounded() async throws {
        let store = TodoStore(cachedTodos: [TodoItem(id: "t", title: "Kept")], cacheWriter: nil, hubListChanged: {}, v1: .on())
        defer { store.forget() }
        #expect(!store.viewsRead)
        try await waitForTodoTest { store.viewsRead }
    }

    /// The places are read with the list while V1 is on and the hub lists them, again only when the list changed. A
    /// failure keeps the last; a hub without the route (405), or whose hello stopped listing them, has none.
    @Test func placesAreReadWithTheList() async throws {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/todos/places", json: #"{"places":[{"id":"all","kind":"all","title":"","needs_you":2}],"counts":{"needs_you":2}}"#)
        var answers = [TodosPage(list: [TodoItem(id: "a", title: "A")], etag: "t1"), TodosPage(list: nil, etag: "t1"),
                       TodosPage(list: [TodoItem(id: "a", title: "A2")], etag: "t2"),
                       TodosPage(list: [TodoItem(id: "a", title: "A3")], etag: "t3")]
        let store = TodoStore(cachedTodos: [], cacheWriter: nil, hubListChanged: {}, read: { _, _ in answers.removeFirst() },
                              v1: .on())
        defer { store.forget() }
        await store.refresh(api: hub.client)
        #expect(store.places?.places.first?.needsYou == 2)
        await store.refresh(api: hub.client)
        #expect(hub.sent("GET", "/api/v1/todos/places").count == 1, "a 304 changes no place")
        hub.on("GET", "/api/v1/todos/places") { _ in throw APIError(status: 503, message: "assistant offline") }
        await store.refresh(api: hub.client)
        #expect(hub.sent("GET", "/api/v1/todos/places").count == 2)
        #expect(store.places?.places.first?.needsYou == 2, "kept until a read answers")
        hub.on("GET", "/api/v1/todos/places") { _ in throw APIError(status: 405, message: "method not allowed") }
        await store.refresh(api: hub.client)
        #expect(hub.sent("GET", "/api/v1/todos/places").count == 3)
        #expect(store.places == nil, "not there")

        var listsPlaces = true
        var v1 = TodoStore.V1.on()
        v1.features = { TodoFeatures(view: true, places: listsPlaces) }
        hub.on("GET", "/api/v1/todos/places", json: #"{"places":[{"id":"all","kind":"all","title":"","needs_you":0}],"counts":{}}"#)
        let unlisted = TodoStore(cachedTodos: [], cacheWriter: nil, hubListChanged: {},
                                 read: { _, _ in TodosPage(list: [TodoItem(id: "b", title: "B")], etag: nil) }, v1: v1)
        defer { unlisted.forget() }
        await unlisted.refresh(api: hub.client)
        #expect(unlisted.places != nil && hub.sent("GET", "/api/v1/todos/places").count == 4)
        listsPlaces = false
        await unlisted.refresh(api: hub.client)
        #expect(unlisted.places == nil && hub.sent("GET", "/api/v1/todos/places").count == 4,
                "a hub that stops listing todo_places has none, and is not asked")
    }

    /// One timer to the earliest moment a row changes by time alone reads the list again then, through the app's hub
    /// (never a client a caller once gave), only while the scene is in front. A read that still says the moment (the
    /// hub's clock behind) is tried again twice, later, then left to the next push.
    @Test func aRowsNextChangeReadsTheListAgain() async throws {
        let hub = FakeHub(), app = FakeHub()
        let soon = ISO8601DateFormatter().string(from: Date().addingTimeInterval(0.05))
        for h in [hub, app] {
            h.on("GET", "/api/v1/todos", json: TodoViewJSON.list([
                TodoViewJSON.row("a", view: TodoViewJSON.view(TodoListSection.justFinished, phase: TodoPhase.ready, next: soon)),
                TodoViewJSON.row("b", view: TodoViewJSON.view(TodoListSection.todo, next: "2099-01-01T00:00:00Z")),
            ]))
            h.on("GET", "/api/v1/todos/places", json: #"{"places":[],"counts":{}}"#)
        }
        let store = TodoStore(cachedTodos: [], cacheWriter: nil, hubListChanged: {}, v1: .on(api: app.client))
        defer { store.forget() }
        store.sceneActive(false)
        await store.refresh(api: hub.client)
        try await Task.sleep(for: .milliseconds(200))
        #expect(app.sent("GET", "/api/v1/todos").isEmpty, "away from the front: no timer")
        store.sceneActive(true)
        try await waitForTodoTest { app.sent("GET", "/api/v1/todos").count == 3 }
        try await Task.sleep(for: .milliseconds(450))
        #expect(app.sent("GET", "/api/v1/todos").count == 3, "the same moment a third time: the next push files it")
        #expect(hub.sent("GET", "/api/v1/todos").count == 1, "the client the caller gave is not the timer's")
    }

    /// A retry that finds the row moved on is the timer's last read for that moment.
    @Test func aRetryThatFindsTheChangeIsTheLast() async throws {
        let hub = FakeHub()
        let soon = ISO8601DateFormatter().string(from: Date().addingTimeInterval(0.05))
        var reads = 0
        hub.on("GET", "/api/v1/todos") { _ in
            reads += 1
            let moved = reads > 2
            let view = moved ? TodoViewJSON.view(TodoListSection.done, phase: TodoPhase.done)
                : TodoViewJSON.view(TodoListSection.justFinished, phase: TodoPhase.ready, next: soon)
            return Data(TodoViewJSON.list([TodoViewJSON.row("a", view: view)]).utf8)
        }
        hub.on("GET", "/api/v1/todos/places", json: #"{"places":[],"counts":{}}"#)
        let store = TodoStore(cachedTodos: [], cacheWriter: nil, hubListChanged: {}, v1: .on(api: hub.client))
        defer { store.forget() }
        await store.refresh(api: hub.client)
        try await waitForTodoTest { store.views["a"]?.section == TodoListSection.done }
        try await Task.sleep(for: .milliseconds(450))
        #expect(reads == 3)
    }

    /// A row filed by the app's own rules (an older manager's, without a view) moves with the clock: it is counted
    /// again while the scene is in front, without a read, and not while it is away.
    @Test func aRowFiledByTheRulesMovesWithTheClock() async throws {
        let hub = FakeHub()
        var now = Date()
        // Closed a minute less than Just finished lasts, by the store's clock.
        let closed = ISO8601DateFormatter().string(from: now.addingTimeInterval(-TodoTriageLogic.justDone + 60))
        hub.on("GET", "/api/v1/todos", json: TodoViewJSON.list([TodoViewJSON.row("old", extra: #""done_at":"\#(closed)""#)]))
        hub.on("GET", "/api/v1/todos/places", json: #"{"places":[],"counts":{}}"#)
        var v1 = TodoStore.V1.on()
        v1.now = { now }
        let store = TodoStore(cachedTodos: [], cacheWriter: nil, hubListChanged: {}, v1: v1)
        defer { store.forget() }
        await store.refresh(api: hub.client)
        #expect(store.countsSource.byRules && store.countsSource.filed.first?.section == TodoListSection.justFinished)
        store.sceneActive(false)
        now += 120
        try await Task.sleep(for: .milliseconds(300)) // three of the test's ticks
        #expect(store.countsSource.filed.first?.section == TodoListSection.justFinished, "away from the front: no clock")
        store.sceneActive(true)
        try await waitForTodoTest { store.countsSource.filed.first?.section == TodoListSection.done }
        #expect(hub.sent("GET", "/api/v1/todos").count == 1, "counted again, not read again")
    }

    /// A hub whose moments keep coming a moment ahead is read no more often than the floor allows.
    @Test func theTimerHasAFloor() async throws {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/todos") { _ in
            let next = ISO8601DateFormatter().string(from: Date().addingTimeInterval(1))
            return Data(TodoViewJSON.list([TodoViewJSON.row("a", view: TodoViewJSON.view(TodoListSection.working, next: next))]).utf8)
        }
        hub.on("GET", "/api/v1/todos/places", json: #"{"places":[],"counts":{}}"#)
        var timing = TodoStore.V1.Timing.fast
        timing.floor = 3
        let store = TodoStore(cachedTodos: [], cacheWriter: nil, hubListChanged: {}, v1: .on(api: hub.client, timing: timing))
        defer { store.forget() }
        await store.refresh(api: hub.client)
        try await waitForTodoTest { hub.sent("GET", "/api/v1/todos").count == 2 }
        try await Task.sleep(for: .milliseconds(1_500))
        #expect(hub.sent("GET", "/api/v1/todos").count == 2, "the next read waits out the floor after the last")
    }
}

/// Waits (up to 5 s) for something a timer does.
@MainActor
private func waitForTodoTest(_ condition: @MainActor () -> Bool) async throws {
    for _ in 0..<250 where !condition() { try await Task.sleep(for: .milliseconds(20)) }
    try #require(condition(), "it did not happen in time")
}
