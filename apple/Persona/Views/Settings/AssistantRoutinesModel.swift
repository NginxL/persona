import SwiftUI

/// What the assistant's pages (Scheduled Tasks, Memory) need from the app: the hub, error reporting, toasts and opening a
/// conversation. `live` is AppModel; tests pass a FakeHub's client and record the rest.
struct AssistantDeps {
    var api: () -> APIClient?
    var report: (Error) -> Void
    var toast: (Toast) -> Void
    /// Show a conversation in the Persona tab (a task that was run now).
    var open: (SessionKey) -> Void
    /// A successful local intake change; tests keep this side effect isolated.
    var intakeChanged: () -> Void = {}

    static var live: AssistantDeps {
        let model = AppModel.shared
        var deps = AssistantDeps(api: { model.api }, report: { model.show($0) }, toast: { model.toast = $0 },
                             open: { model.openAssistantConversation($0) })
        #if os(iOS)
        deps.intakeChanged = { Task { await HealthIntakeSync.shared.refresh(model: model) } }
        #endif
        return deps
    }
}

/// Scheduled Tasks' state and everything the page does with the hub (MVVM: ARCH-PLAN.md). The view draws what is here and
/// calls its methods, so tests run it against a `FakeHub`. One list: what repeats (every day, every N minutes) and
/// the one-offs Persona scheduled from a conversation.
@Observable final class AssistantRoutinesModel {
    typealias Deps = AssistantDeps
    typealias Item = APIClient.ScheduledTask

    private(set) var tasks: [Item] = []
    private(set) var lastRuns: [APIClient.ScheduleRun] = []
    private(set) var loaded = false
    /// Why the list could not be read; the page shows it only when it has none to show.
    private(set) var error: String?
    @ObservationIgnored private let deps: Deps
    @ObservationIgnored private var centralModels: Bool?
    /// The hub's client as the page first read with it (it names the current assistant then, `APIClient.showing`): what
    /// the page reads again and writes goes to the assistant whose tasks it shows, also once another is current.
    /// `restart` moves the page to that one.
    @ObservationIgnored private var client: APIClient?
    /// The latest read: an older one that answers after it doesn't land.
    @ObservationIgnored private var reads = 0
    private var api: APIClient? { client ?? deps.api() }

    init(deps: Deps = .live) { self.deps = deps }

    /// Every day / every N minutes, in the hub's order.
    var repeating: [Item] { tasks.filter { $0.repeats != .once } }
    /// One-offs, soonest first.
    var oneOff: [Item] { tasks.filter { $0.repeats == .once }.sorted { ($0.at ?? .distantFuture) < ($1.at ?? .distantFuture) } }

    var upcoming: [AssistantUpcoming] { Array(AssistantUpcoming.merge(tasks).prefix(4)) }

    func load() async {
        guard let api else { return }
        if client == nil { client = api }
        reads += 1
        let read = reads
        async let h = try? api.scheduleRuns()
        do {
            let list = try await api.scheduledTasks()
            guard read == reads else { return }
            tasks = list
            error = nil
        } catch {
            guard read == reads else { return }
            self.error = error.shown ?? self.error
        }
        let runs = await h ?? []
        guard read == reads else { return }
        lastRuns = runs
        loaded = true
    }

    /// Another assistant is current while the page is up (where it stays: Settings): the page starts over on that one.
    /// Nothing of the one before is left on it, and a change to its tasks still on its way went to it.
    func restart() async {
        client = nil
        reads += 1
        tasks = []
        lastRuns = []
        loaded = false
        error = nil
        centralModels = nil
        await load()
    }

    /// Adds `t` (no id yet) or saves it; returns it as saved. Optimistic: a toggle does not bounce back while saving;
    /// a failure reports and reloads what the hub has.
    func save(_ t: Item) async -> Item? {
        guard let api else { return nil }
        if let i = tasks.firstIndex(where: { $0.id == t.id && !t.id.isEmpty }) {
            withAnimation(Theme.Motion.standard) { tasks[i] = t }
        }
        do {
            if centralModels == nil {
                do {
                    _ = try await api.modelConfiguration()
                    centralModels = true
                } catch let error as APIError where error.status == 404 {
                    centralModels = false
                }
            }
            let saved = try await api.saveTask(t, includesModelSettings: centralModels != true)
            if let i = tasks.firstIndex(where: { $0.id == saved.id }) { tasks[i] = saved } else { tasks.append(saved) }
            return saved
        } catch {
            deps.report(error)
            if let fresh = try? await api.scheduledTasks() { tasks = fresh }
            return nil
        }
    }

    func delete(_ t: Item) async {
        guard let api, !t.id.isEmpty else { return }
        withAnimation { tasks.removeAll { $0.id == t.id } }
        do {
            try await api.deleteTask(t.id)
            deps.toast(Toast(String(localized: "Deleted “\(AssistantScheduleText.title(t))”"), style: .success))
        } catch {
            deps.report(error)
            if let fresh = try? await api.scheduledTasks() { tasks = fresh }
        }
    }

    /// Run Now: opens the conversation it went to. A one-off is done after it (the hub drops it from the list).
    func run(_ t: Item) async {
        guard let api else { return }
        if t.repeats == .once { withAnimation { tasks.removeAll { $0.id == t.id } } }
        do {
            let key = try await api.runTask(t.id)
            Haptics.success()
            deps.open(key)
        } catch {
            deps.report(error)
            if let fresh = try? await api.scheduledTasks() { tasks = fresh }
        }
    }
}

/// Memory: the assistant's memory files, newest first, and deleting one. `space` "health" is the health space's own
/// memory (Health › Health Memory, docs/assistant/health.md); nil the assistant's.
@Observable final class AssistantMemoryModel {
    private(set) var files: [APIClient.MemoryFile] = []
    private(set) var loaded = false
    private(set) var error: String?
    let space: String?
    @ObservationIgnored private let deps: AssistantDeps

    init(space: String? = nil, deps: AssistantDeps = .live) {
        self.space = space
        self.deps = deps
    }

    func load() async {
        guard let api = deps.api() else { return }
        do {
            files = try await api.assistantMemory(space: space)
            error = nil
        } catch {
            self.error = error.shown ?? self.error
        }
        loaded = true
    }

    func delete(_ name: String) async {
        guard let api = deps.api() else { return }
        do {
            try await api.deleteMemory(name: name, space: space)
            Haptics.success()
            await load()
        } catch {
            deps.report(error)
        }
    }
}
