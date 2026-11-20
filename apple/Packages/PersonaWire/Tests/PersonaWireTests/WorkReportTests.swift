import Foundation
import Testing

@testable import PersonaWire

@Suite struct WorkReportTests {
    private struct Samples: Decodable {
        struct Sample: Decodable { let name, text, display: String }
        let cases: [Sample]
    }

    @Test func reportsAboveTheSameCard() throws {
        let directory = URL(fileURLWithPath: #filePath).deletingLastPathComponent()
        let fixture = directory.appendingPathComponent("../../../../../spec/fixtures/work-reports/terminal.json")
        let samples = try JSONDecoder().decode(Samples.self, from: Data(contentsOf: fixture))
        #expect(!samples.cases.isEmpty)
        for sample in samples.cases {
            #expect(Closeout.holdsBlock(sample.text) == (sample.text != sample.display), "\(sample.name)")
            if sample.name == "done" { #expect(Closeout.strip(sample.text, complete: false) == sample.text) }
            #expect(Closeout.strip(sample.text).utf8.elementsEqual(sample.display.utf8), "\(sample.name)")
        }
    }
}
