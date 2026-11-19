import AppIntents
import Foundation

/// "New To-Do": writes a todo (docs/assistant/todos.md) straight to the hub, so Siri, the Action button or a shortcut can note
/// something down without opening the app. It goes to the hub group the app has selected, where the todos page reads.
struct AddTodoIntent: AppIntent {
    nonisolated static let title: LocalizedStringResource = "New To-Do"
    nonisolated static let description = IntentDescription(
        "Note a to-do in Persona. Later you can hand it to an agent from the To-dos page.",
        categoryName: "To-dos",
        searchKeywords: ["To-dos", "todo", "to-do", "Tasks", "Reminder [Add Todo Intent 357f8a]", "Reminder [Add Todo Intent 8f29b3]"]
    )
    @available(iOS 26.0, *)
    nonisolated static var supportedModes: IntentModes { .background }

    @Parameter(title: "Title", requestValueDialog: "What's the to-do?")
    var todoTitle: String

    @Parameter(title: "Notes", description: "Sent along as context when you hand it to an agent", inputOptions: String.IntentInputOptions(multiline: true))
    var notes: String?

    nonisolated init() {}
    nonisolated init(todoTitle: String, notes: String? = nil) {
        self.todoTitle = todoTitle
        self.notes = notes
    }

    nonisolated static var parameterSummary: some ParameterSummary {
        Summary("New to-do \(\.$todoTitle)") { \.$notes }
    }

    nonisolated func perform() async throws -> some IntentResult & ProvidesDialog & ReturnsValue<String> {
        let api: APIClient
        do {
            api = try IntentSupport.selectedClient()
        } catch {
            throw IntentFailureSentence(TodoIntentText.failure(error))
        }
        let todo = try await AddTodoFlow.add(api, title: todoTitle, notes: notes)
        return .result(value: todo.title, dialog: IntentSupport.dialog(TodoIntentText.added(todo.title)))
    }
}

/// The network part of AddTodoIntent, separate from the pairing so the tests can hand it a FakeHub client.
nonisolated enum AddTodoFlow {
    /// Creates the todo; every failure comes back as one sentence Siri can read.
    static func add(_ api: APIClient, title: String, notes: String?) async throws -> TodoItem {
        guard let title = TodoIntentText.cleaned(title) else { throw IntentFailureSentence(TodoIntentText.askTitle) }
        do {
            return try await api.createTodo(title: title, notes: TodoIntentText.cleaned(notes))
        } catch {
            throw IntentFailureSentence(TodoIntentText.failure(error))
        }
    }
}

/// Wording for AddTodoIntent. Pure, so SiriIntentsTests can pin it.
nonisolated enum TodoIntentText {
    static let askTitle = String(localized: "What's the to-do?")
    static let oldHub = String(localized: "This hub doesn't support to-dos yet. Update the hub first.")

    /// Trimmed text, or nil when nothing is left.
    static func cleaned(_ text: String?) -> String? {
        guard let t = text?.trimmingCharacters(in: .whitespacesAndNewlines), !t.isEmpty else { return nil }
        return t
    }

    static func added(_ title: String) -> String {
        String(localized: "Added to-do: “\(IntentText.excerpt(title, limit: 60))”")
    }

    static func failure(_ error: Error) -> String {
        if TodosLogic.isOldHub(error) { return oldHub }
        return IntentFailure.sentence(for: error, host: IntentSupport.host())
    }
}
