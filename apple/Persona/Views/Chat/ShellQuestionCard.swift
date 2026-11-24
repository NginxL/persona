import SwiftUI

/// The terminal's startup question, using the design system's notice card and measured, non-wrapping choice buttons.
/// The answer closure owns the wire request; an error stays here instead of becoming a toast detached from the question.
struct ShellQuestionCard: View {
    var question: ShellQuestion
    var answer: (String) async throws -> Void

    @State private var busy: String?
    @State private var failure: String?
    @State private var answered = false
    @State private var expired = false

    private var detail: String {
        var text = question.hint
        if let keptClock = question.keptClock {
            text += " " + String(localized: "The tab stays open until \(keptClock).")
        }
        return text
    }

    var body: some View {
        if !answered, !expired, question.open() != nil {
            ReadinessCard(title: String(localized: "Your computer's terminal is asking: “\(question.line)”"), detail: detail,
                          tone: .neutral, details: [question.screen].compactMap { $0 },
                          choices: question.choices.map { choice in
                              ReadinessChoice(id: choice, title: question.label(choice), busy: busy == choice, disabled: busy != nil) {
                                  pick(choice)
                              }
                          }, symbol: Theme.Icon.terminal, failure: failure)
                .accessibilityIdentifier("shell-question")
                .tabActiveTask(id: question.keptAt) {
                    guard let until = question.keptAt else { return }
                    let wait = until.timeIntervalSinceNow
                    guard wait > 0 else { expired = true; return }
                    try? await Task.sleep(for: .seconds(wait))
                    if !Task.isCancelled { expired = true }
                }
        }
    }

    private func pick(_ choice: String) {
        guard busy == nil, question.choices.contains(choice) else { return }
        busy = choice
        failure = nil
        Task {
            do {
                try await answer(choice)
                answered = true
            } catch {
                failure = error.shown ?? error.localizedDescription
            }
            busy = nil
        }
    }
}

/// A session's own start question. The shared parser is memoized, so deriving it here does no repeated ISO work.
struct SessionShellQuestion: View {
    @Environment(AppModel.self) private var model
    var session: Session

    var body: some View {
        let question = TaskStartRemedy.sessionQuestion(code: session.startErrorCode, args: session.startErrorArgs,
                                                       features: model.features(session.key.group))
        if let question {
            ShellQuestionCard(question: question) { choice in
                try await model.continueStart(session.key, choice: choice)
            }
            .id(question)
        }
    }
}

/// A planned task's start question. Its answer restarts the task on the hub; no chat outbox entry is involved.
struct TaskShellQuestion: View {
    @Environment(AppModel.self) private var model
    var todo: TodoItem
    var task: PlanTask

    var body: some View {
        let question = TaskStartRemedy.taskQuestion(task, features: model.hubFeatures)
        if let question {
            ShellQuestionCard(question: question) { choice in
                try await TasksStore.shared.answerStart(todo, choice: choice, features: model.hubFeatures)
            }
            .id(question)
        }
    }
}
