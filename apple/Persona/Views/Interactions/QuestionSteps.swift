import Foundation

/// A card asking several questions at once through the hook (#2970): Claude Code's AskUserQuestion, the assistant's
/// ask_user, Codex's questions. Its step capsules show progress and the question it is on (they are not buttons), and
/// a review of every answer comes before they go, as on a terminal dialog of several (`ScreenStepsLogic`, `ScreenReview`).
nonisolated enum QuestionSteps {
    /// Whether the card draws steps and a review: several questions, on iPhone and iPad (the Mac app is frozen).
    static func enabled(_ questions: [Question]) -> Bool { ScreenStepsLogic.enabled && questions.count > 1 }

    /// A capsule per question, labeled with its header (Claude's chip, at most a few words), ticked once it has an answer.
    static func steps(_ questions: [Question], answered: (Int) -> Bool) -> [ScreenParser.Dialog.Step] {
        questions.enumerated().map { i, q in .init(label: label(q), done: answered(i)) }
    }

    /// The answer as the review shows it: the typed words, else the picked options in the card's order, without
    /// "(Recommended)". Nil when there is neither.
    static func shown(_ q: Question, picked: Set<String>, typed: String) -> String? {
        let words = typed.trimmingCharacters(in: .whitespacesAndNewlines)
        if !words.isEmpty { return words }
        let labels = q.options.map(\.label).filter(picked.contains).map { QuestionOption.display($0).0 }
        return labels.isEmpty ? nil : labels.joined(separator: ", ")
    }

    /// How long a single-select pick waits before it moves on: long enough to see its tick land. `-questionStepPause
    /// <ms>` (DEBUG) stretches it, so a UI test can tap again within it.
    static var pause: Duration {
        #if DEBUG
        let ms = UserDefaults.standard.integer(forKey: "questionStepPause")
        if ms > 0 { return .milliseconds(ms) }
        #endif
        return .milliseconds(250)
    }

    /// Where a single-select pick made on page `from` steps once its pause is over (#6549): to the next question, only
    /// while the card is still on `from` (`now`), never past the last one. Another tap within the pause (a second
    /// option, or Next / Previous) steps nowhere more: each pick used to step on its own, so a question was skipped, or
    /// the card went past the last one onto a page with none.
    static func next(after from: Int, now: Int, count: Int) -> Int? {
        guard now == from, from + 1 < count else { return nil }
        return from + 1
    }

    /// The review: a row per question, its header (else the question itself) and its answer, nil when it has none.
    static func rows(_ questions: [Question], answers: [Int: String]) -> [ScreenReviewRow] {
        questions.enumerated().map { i, q in
            ScreenReviewRow(index: i, label: label(q), question: q.question, answer: answers[i])
        }
    }

    private static func label(_ q: Question) -> String { q.header?.trimmingCharacters(in: .whitespaces) ?? "" }
}
