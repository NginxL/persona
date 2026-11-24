import Foundation
import Testing
@testable import Persona
import PersonaWire

/// What the minutes line says while the minutes wait (#13461, docs/assistant/meeting-minutes.md「Gaps and the final
/// pass」): every shared case in spec/fixtures/minutes-wait, which the hub and the web run too. `transcription` is
/// decoded as the recording's detail gives it (`MeetingTranscriptionView(_:)`), then `MinutesWaitLine` reads it.
@MainActor
struct MinutesWaitFixtureTests {
    private static let repo = URL(fileURLWithPath: #filePath).deletingLastPathComponent().deletingLastPathComponent().deletingLastPathComponent()

    private struct Fixtures: Decodable {
        struct Case: Decodable {
            struct Expected: Decodable {
                var line: String
                var doneMinutes: Int?
                var totalMinutes: Int?
                enum CodingKeys: String, CodingKey { case line, doneMinutes = "done_minutes", totalMinutes = "total_minutes" }
            }
            var name: String
            var transcription: JSONValue?
            var minutesState: String?
            var hasMinutes: Bool
            var expected: Expected
            enum CodingKeys: String, CodingKey {
                case name, transcription, minutesState = "minutes_state", hasMinutes = "has_minutes", expected
            }
        }
        var version: Int
        var cases: [Case]
    }

    @Test func readsTheSharedFixturesLikeTheHubAndTheWeb() throws {
        let data = try Data(contentsOf: Self.repo.appending(path: "spec/fixtures/minutes-wait/cases.json"))
        let fixtures = try JSONDecoder().decode(Fixtures.self, from: data)
        #expect(fixtures.version == 1)
        #expect(fixtures.cases.count >= 15)
        for c in fixtures.cases {
            let line = MinutesWaitLine.of(MeetingTranscriptionView(c.transcription), minutesState: c.minutesState, hasMinutes: c.hasMinutes)
            let got: (String, Int?, Int?) = switch line {
            case nil: ("none", nil, nil)
            case .writing?: ("writing", nil, nil)
            case .checking(let p)?: ("checking", p?.done, p?.total)
            }
            #expect(got.0 == c.expected.line, "\(c.name)")
            #expect(got.1 == c.expected.doneMinutes, "\(c.name)")
            #expect(got.2 == c.expected.totalMinutes, "\(c.name)")
        }
    }

    /// The words: the minutes done of the total while the pass runs, the plain line before it starts, and the minutes'
    /// own line otherwise (the test bundle runs in zh-Hans, so only what is said is checked: the numbers, not the words).
    @Test func checkingNamesTheMinutesDoneOfTheTotal() {
        let view = MeetingTranscriptionView(mode: .hub, state: "finishing", finalDone: 2400, end: 6312.5)
        let line = MinutesWaitLine.of(view)
        #expect(line == .checking(.init(done: 40, total: 105)))
        #expect(line.title.contains("40") && line.title.contains("105"))
        #expect(MinutesWaitLine.of(MeetingTranscriptionView(mode: .hub, state: "finishing")).title != line.title)
        #expect(MinutesWaitLine.of(MeetingTranscriptionView(mode: .hub, state: "final")).title == String(localized: "Writing the minutes…"))
    }
}
