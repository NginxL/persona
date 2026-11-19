import XCTest
import PersonaCore
import PersonaCoreTestSupport

final class PersonaCoreProjectionTests: XCTestCase {
    func testEveryAgentsListFixtureThroughTheRealCBinding() throws {
        let count = try ProjectionBindingReplay.sort()
        XCTAssertEqual(count, 60)
        print("PersonaCore Swift/C sort: \(count) agents_list cases, complete expected equality")
    }

    func testEveryAnswerFixtureThroughTheRealCBinding() throws {
        let counts = try ProjectionBindingReplay.answers()
        XCTAssertEqual(counts.questions, 15)
        XCTAssertEqual(counts.recommendations, 9)
        print("PersonaCore Swift/C answer: \(counts.questions) question + \(counts.recommendations) recommendation cases")
    }

    func testEveryStreamChunkFixtureThroughTheRealCBinding() throws {
        let count = try ProjectionBindingReplay.streamChunks()
        XCTAssertEqual(count, 21)
        print("PersonaCore Swift/C stream_chunks: \(count) complete {done, tail} objects")
    }

    func testEveryStaticTurnFixtureThroughTheRealCBindingAndIOSOverlay() throws {
        let result = try ProjectionBindingReplay.turns()
        XCTAssertEqual(result.cases, 57)
        XCTAssertEqual(result.turnObjects, 73)
        print("PersonaCore Swift/C turns: \(result.cases) static cases, \(result.turnObjects) complete turn objects")
    }

    func testEveryTaskIndexFixtureThroughTheRealCBinding() throws {
        let count = try ProjectionBindingReplay.taskIndex()
        XCTAssertEqual(count, 5)
        print("PersonaCore Swift/C task_index: \(count) complete objects plus the background receipt")
    }

    func testEveryViewedImageProjectionThroughTheRealCBinding() throws {
        let result = try ProjectionBindingReplay.viewedImages()
        XCTAssertEqual(result.completeObjects, 28)
        XCTAssertEqual(result.noLineBoundaries, 8)
        print("PersonaCore Swift/C viewed_images: \(result.completeObjects) complete objects, \(result.noLineBoundaries) no-line boundaries")
    }

    func testJSONComparisonKeepsLargeIntegersAndScalarFailuresExact() throws {
        let larger = try ProjectionFixture.decode("9007199254740993", "large integer")
        let smaller = try ProjectionFixture.decode("9007199254740992", "adjacent integer")
        try ProjectionFixture.assertWrongExpectationIsRejected(
            larger, wrong: smaller, label: "JSON integers above 2^53 remain distinct"
        )
        try ProjectionFixture.assertWrongExpectationIsRejected(
            .number("1"), wrong: .number("0"), label: "scalar mismatches are catchable Swift errors"
        )
        let original = try ProjectionFixture.decode("9007199254740993", "encoded integer")
        XCTAssertEqual(try original.encoded(), "9007199254740993")
        let negative = try ProjectionFixture.decode("-9007199254740992", "adjacent negative integer")
        XCTAssertNotEqual(negative, try ProjectionFixture.decode("-9007199254740991", "another negative integer"))
        try ProjectionFixture.assertWrongExpectationIsRejected(
            .bool(true), wrong: .number("1"), label: "JSON booleans are not numbers"
        )
        try ProjectionFixture.assertWrongExpectationIsRejected(
            .bool(false), wrong: .number("0"), label: "JSON false is not zero"
        )
        // Programmatic invalid numeric text must throw rather than reach Foundation's non-finite exception.
        XCTAssertThrowsError(try ContinuityJSON.number("nan").encoded())
        XCTAssertThrowsError(try ContinuityJSON.array([.number("nan")]).encoded())
        XCTAssertThrowsError(try ProjectionFixture.decode("1e1000", "out-of-range exponent"))
    }

    func testMalformedInputMapsToTheCErrorResult() throws {
        // Every malformed-input path must reach the adapter's explicit C failure mapping, not a JSON decoder's nil.
        for projection in [
            PersonaCoreProjections.sort,
            PersonaCoreProjections.answer,
            PersonaCoreProjections.displayAnswer,
            PersonaCoreProjections.streamChunks,
            PersonaCoreProjections.turns,
            PersonaCoreProjections.taskIndex,
            PersonaCoreProjections.viewedImages,
        ] {
            XCTAssertThrowsError(try projection("")) { error in
                XCTAssertEqual(error as? PersonaCoreProjectionError, .emptyResult)
            }
        }
    }
}
