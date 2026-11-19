import Foundation
import Testing
import PersonaWire

/// The views, counts and places the shared to-do cases expect (spec/fixtures/todo-view and todo-counts, #10226), and a
/// to-do's timelines, results and recaps (spec/fixtures/todo-rounds, #10465), read into the generated types
/// (schema/wire/todo-view.schema.json) and write back the same bytes: every field, no other.
@MainActor struct TodoViewWireTests {
    private static let fixtures = URL(fileURLWithPath: #filePath).deletingLastPathComponent().deletingLastPathComponent()
        .deletingLastPathComponent().appending(path: "spec/fixtures")

    /// Every expected value of the three domains, with the definition it holds to and the case it comes from.
    private static func samples() throws -> [(definition: String, name: String, sample: [String: Any])] {
        var out: [(definition: String, name: String, sample: [String: Any])] = []
        for domain in ["todo-view", "todo-counts", "todo-rounds"] {
            let files = try FileManager.default.contentsOfDirectory(at: fixtures.appending(path: domain),
                                                                    includingPropertiesForKeys: nil)
                .filter { $0.pathExtension == "json" }
                .sorted { $0.lastPathComponent < $1.lastPathComponent }
            for file in files {
                let root = try JSONSerialization.jsonObject(with: Data(contentsOf: file)) as? [String: Any] ?? [:]
                for c in root["cases"] as? [[String: Any]] ?? [] {
                    let expected = c["expected"] as? [String: Any] ?? [:]
                    let name = "\(domain)/\(file.lastPathComponent) \(c["name"] as? String ?? "")"
                    for (key, definition) in [("view", "TodoSummary"), ("counts", "TodoCounts"), ("places", "TodoPlaces"),
                                              ("timeline", "TodoTimeline"), ("recap", "TodoRecap")] {
                        if let sample = expected[key] as? [String: Any] {
                            out.append((definition, name, sample))
                        }
                    }
                    for sample in expected["results"] as? [[String: Any]] ?? [] {
                        out.append(("TodoResult", name, sample))
                    }
                }
            }
        }
        return out
    }

    @Test func generatedTypesWriteTheFixtureBytesBack() throws {
        let samples = try Self.samples()
        #expect(Set(samples.map(\.definition)) == ["TodoSummary", "TodoCounts", "TodoPlaces", "TodoTimeline", "TodoRecap",
                                                    "TodoResult"])
        let encoder = JSONEncoder()
        encoder.outputFormatting = [.sortedKeys, .withoutEscapingSlashes]
        for (definition, name, sample) in samples {
            let data = try JSONSerialization.data(withJSONObject: sample, options: [.sortedKeys, .withoutEscapingSlashes])
            let written: Data = switch definition {
            case "TodoSummary": try encoder.encode(JSONDecoder().decode(TodoSummary.self, from: data))
            case "TodoCounts": try encoder.encode(JSONDecoder().decode(TodoCounts.self, from: data))
            case "TodoPlaces": try encoder.encode(JSONDecoder().decode(TodoPlaces.self, from: data))
            case "TodoTimeline": try encoder.encode(JSONDecoder().decode(TodoTimeline.self, from: data))
            case "TodoRecap": try encoder.encode(JSONDecoder().decode(TodoRecap.self, from: data))
            default: try encoder.encode(JSONDecoder().decode(TodoResult.self, from: data))
            }
            #expect(written == data, "\(name)")
        }
    }
}
