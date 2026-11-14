import Foundation
import Testing
@testable import Persona

/// A hook card of several questions (#2970): its capsules, and the review's rows and answers in both styles.
struct QuestionStepsTests {
    private let questions = [
        Question(question: "Which environment first?", header: "Env", multiSelect: false,
                 options: [QuestionOption(label: "Preview (Recommended)"), QuestionOption(label: "Production")]),
        Question(question: "Which platforms too?", header: "Platforms", multiSelect: true,
                 options: [QuestionOption(label: "iPhone"), QuestionOption(label: "Watch"), QuestionOption(label: "iPad")]),
        Question(question: "Who hears when it's out?", header: nil, multiSelect: false,
                 options: [QuestionOption(label: "Everyone"), QuestionOption(label: "Design")]),
    ]

    @Test func onlyACardOfSeveralQuestionsSteps() {
        #expect(QuestionSteps.enabled(questions))
        #expect(!QuestionSteps.enabled(Array(questions.prefix(1))))
    }

    @Test func aCapsulePerQuestionWithItsHeader() {
        let steps = QuestionSteps.steps(questions) { $0 == 1 }
        #expect(steps.map(\.label) == ["Env", "Platforms", ""])
        #expect(steps.map(\.done) == [false, true, false])
    }

    @Test func theReviewShowsWordsAsTheCardDoes() {
        // Typed words win; picks keep the options' order and lose "(Recommended)"; nothing is no answer.
        #expect(QuestionSteps.shown(questions[0], picked: ["Preview (Recommended)"], typed: "") == "Preview")
        #expect(QuestionSteps.shown(questions[1], picked: ["iPad", "iPhone"], typed: "") == "iPhone, iPad")
        #expect(QuestionSteps.shown(questions[1], picked: ["iPad"], typed: "  only the iPad  ") == "only the iPad")
        #expect(QuestionSteps.shown(questions[2], picked: [], typed: " ") == nil)
    }

    /// A single-select pick steps once, from the page it was made on (#6549): two taps within its pause made two steps.
    @Test func aPickStepsOnceFromItsOwnPage() {
        #expect(QuestionSteps.next(after: 0, now: 0, count: 3) == 1)
        // a step from a page the card has left (it got past its cancel): nowhere
        #expect(QuestionSteps.next(after: 0, now: 1, count: 3) == nil)
        // never past the last question
        #expect(QuestionSteps.next(after: 1, now: 1, count: 3) == 2)
        #expect(QuestionSteps.next(after: 2, now: 2, count: 3) == nil)
        // Previous within the pause: the card stays where it went
        #expect(QuestionSteps.next(after: 1, now: 0, count: 3) == nil)
    }

    @MainActor private final class Count { var steps = 0 }

    /// A second pick within the pause starts it again (#6549): the first pick's step never comes, and the card steps
    /// once, a whole pause after the second pick. The first pick's step used to come a pause after it, and the second
    /// pick's tick was on screen for a moment only. Previous / Next within the pause cancel the step: none at all.
    @MainActor @Test func aSecondPickWithinThePauseStartsItAgainAndStepsOnce() async {
        let count = Count()
        let first = QuestionSteps.step(replacing: nil, after: .seconds(5)) { count.steps += 1 }
        let picked = ContinuousClock.now
        let second = QuestionSteps.step(replacing: first, after: .milliseconds(200)) { count.steps += 1 }
        await first.value
        await second.value
        #expect(count.steps == 1)
        #expect(ContinuousClock.now - picked >= .milliseconds(200))
        let next = QuestionSteps.step(replacing: nil, after: .milliseconds(50)) { count.steps += 1 }
        next.cancel()
        await next.value
        #expect(count.steps == 1)
    }

    @Test func aRowPerQuestionAndTheOneLeftHasNoAnswer() {
        let rows = QuestionSteps.rows(questions, answers: [0: "Preview", 1: "iPhone"])
        #expect(rows.map(\.index) == [0, 1, 2])
        #expect(rows.map(\.label) == ["Env", "Platforms", ""])
        #expect(rows[2].question == "Who hears when it's out?")
        #expect(rows.map(\.answer) == ["Preview", "iPhone", nil])
    }

    @MainActor @Test func beautifulUIsAnswersAndTheOneSkipped() {
        // Its card keys answers by question and option index; a question skipped with ↓ has none.
        #expect(BUIQuestionForm.shown(.init(picks: [0: [0], 1: [2, 0]], custom: [:]), questions) == [0: "Preview", 1: "iPhone, iPad"])
        // What goes to the hub is unchanged: the labels as the agent wrote them, the skipped question left out.
        #expect(BUIQuestionForm.answers(.init(picks: [0: [0], 1: [2, 0]], custom: [:]), questions)
                == ["Which environment first?": "Preview (Recommended)", "Which platforms too?": "iPhone, iPad"])
    }
}
