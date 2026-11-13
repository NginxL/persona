import Foundation
import Observation

/// What the Decisions section needs from the app: the hub, error reporting and toasts. `live` is AppModel; tests pass a
/// FakeHub's client and record the rest.
struct TodoDecisionsDeps {
    var api: () -> APIClient?
    var report: (Error) -> Void
    var toast: (Toast) -> Void
    /// The manager the requests about a to-do name (`TodoStore.named`): its own, whichever assistant is current.
    var named: (String) -> String? = { _ in nil }

    static var live: TodoDecisionsDeps {
        let model = AppModel.shared
        return TodoDecisionsDeps(api: { model.api }, report: { model.show($0) }, toast: { model.toast = $0 },
                                 named: { TodoStore.shared.named($0) })
    }
}

/// The Record / Revise form: a revision replaces `supersedes`.
struct TodoDecisionForm: Identifiable, Equatable {
    var supersedes: TodoDecision?
    var id: String { supersedes?.id ?? "" }
}

/// A todo's decisions and everything its page does with them (#1618, MVVM: ARCH-PLAN.md), as the web's
/// `DecisionsSection` does: read again whenever the todo changes (the hub moves its `updated_at` when a decision is
/// written, an answered card's too), hidden on a hub from before decisions.
@Observable final class TodoDecisionsModel {
    typealias Deps = TodoDecisionsDeps

    /// Every decision, oldest first, the revised ones included; nil until the first read lands.
    private(set) var all: [TodoDecision]?
    /// Why the list could not be read; shown only while there is no list.
    private(set) var failed: String?
    /// The hub has no decisions route: the section is not drawn.
    private(set) var unsupported = false
    /// The revised ones too, struck through.
    var showAll = false
    /// The first five, every one, or none (#6447); each to-do opens at the first five, and nothing is stored.
    var display = TodoDecisionsDisplay.first
    var form: TodoDecisionForm?
    @ObservationIgnored private(set) var todoID = ""
    /// Only the newest read lands: a slow earlier one must not put back an older list.
    @ObservationIgnored private var loads = 0
    @ObservationIgnored private let deps: Deps

    init(deps: Deps = .live) { self.deps = deps }

    /// The hub's client for this to-do's decisions: its manager named.
    private var client: APIClient? { deps.api()?.manager(deps.named(todoID)) }

    var current: [TodoDecision] { TodoDecisionsLogic.current(all ?? []) }
    var revisedCount: Int { (all?.count ?? 0) - current.count }
    /// The list Full History picks: the current decisions, or every one.
    var listed: [TodoDecision] { showAll ? all ?? [] : current }
    /// What the section draws of it (#6447).
    var shown: [TodoDecision] { TodoDecisionsLogic.shown(listed, display) }
    /// Folded under the header, which keeps its count: only a list with something in it.
    var folded: Bool { display == .folded && !listed.isEmpty }
    /// Show All under the first five, or Show First 5 under them all: only for a list longer than that.
    var hasMore: Bool { !folded && listed.count > TodoDecisionsLogic.firstCount }

    /// The header's fold-all button: Collapse All while any decision shows, Expand All (every one) once folded.
    func toggleFold() {
        display = folded ? .all : .folded
    }

    /// Show All under the first five, Show First 5 under them all.
    func toggleMore() {
        display = display == .first ? .all : .first
    }

    func decision(_ id: String) -> TodoDecision? {
        all?.first { $0.id == id }
    }

    func candidates(for d: TodoDecision) -> [TodoDecision] {
        TodoDecisionsLogic.supersedeCandidates(for: d, in: all ?? [])
    }

    /// The todo whose decisions these are; another one starts over.
    func select(_ todoID: String) {
        guard todoID != self.todoID else { return }
        self.todoID = todoID
        loads += 1
        all = nil
        failed = nil
        showAll = false
        display = .first
        form = nil
    }

    func load() async {
        guard let api = client, !todoID.isEmpty else { return }
        loads += 1
        let n = loads
        do {
            let list = try await api.todoDecisions(todoID: todoID)
            guard n == loads else { return }
            all = list
            failed = nil
            unsupported = false
        } catch {
            guard n == loads else { return }
            if TodosLogic.isLegacySpacesRoute(error) {
                unsupported = true
            } else if let shown = error.shown {
                failed = shown
            }
        }
    }

    /// Records a decision, or with `supersedes` one that revises it. True once saved, so the form closes; a failure
    /// keeps it open with what was typed.
    func add(question: String, choice: String, reason: String, supersedes: TodoDecision?) async -> Bool {
        let question = question.trimmingCharacters(in: .whitespacesAndNewlines)
        let choice = choice.trimmingCharacters(in: .whitespacesAndNewlines)
        guard let api = client, !question.isEmpty, !choice.isEmpty else { return false }
        do {
            _ = try await api.addTodoDecision(todoID: todoID, question: question, choice: choice, reason: reason,
                                              supersedesID: supersedes?.id ?? "")
        } catch {
            deps.report(error)
            return false
        }
        display = .all // the one just recorded or revised is last: show them all so it is in view
        await load()
        deps.toast(Toast(supersedes == nil ? String(localized: "Recorded [Todo Decisions]") : String(localized: "Decision revised"),
                         style: .success))
        return true
    }

    /// Marks `d` as revising the earlier decision `old`; "" undoes it.
    func markSupersedes(_ d: TodoDecision, old: String) async {
        guard let api = client else { return }
        do {
            _ = try await api.setTodoDecisionSupersedes(todoID: todoID, id: d.id, supersedesID: old)
        } catch {
            deps.report(error)
            return
        }
        await load()
        deps.toast(Toast(old.isEmpty ? String(localized: "Revision undone") : String(localized: "Marked as revised"), style: .success))
    }
}
