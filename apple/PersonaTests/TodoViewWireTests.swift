import Foundation
import Testing
@testable import Persona
import PersonaWire

/// The views, counts and places the shared to-do cases expect (spec/fixtures/todo-view and todo-counts, #10226), a
/// to-do's timelines, results and recaps (spec/fixtures/todo-rounds, #10465) with what the item route carries of them
/// (itemDetail), and the decision pushes' todo (spec/fixtures/todo-push, #10465) read into the generated types
/// (schema/wire/todo-view.schema.json) and write back the same bytes: every field, no other.
@MainActor struct TodoViewWireTests {
    private static let fixtures = URL(fileURLWithPath: #filePath).deletingLastPathComponent().deletingLastPathComponent()
        .deletingLastPathComponent().appending(path: "spec/fixtures")

    /// Every expected value of the four domains, with the definition it holds to and the case it comes from.
    private static func samples() throws -> [(definition: String, name: String, sample: [String: Any])] {
        var out: [(definition: String, name: String, sample: [String: Any])] = []
        for domain in ["todo-view", "todo-counts", "todo-rounds", "todo-push"] {
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
                                              ("timeline", "TodoTimeline"), ("recap", "TodoRecap"),
                                              ("todo", "TodoDecidePush")] {
                        if let sample = expected[key] as? [String: Any] {
                            out.append((definition, name, sample))
                        }
                    }
                    for sample in expected["results"] as? [[String: Any]] ?? [] {
                        out.append(("TodoResult", name, sample))
                    }
                    if let sample = itemDetail(expected) {
                        out.append(("TodoItemDetail", name, sample))
                    }
                }
            }
        }
        return out
    }

    /// What GET /api/v1/todos/{id} carries beside the row for a todo-rounds case (#10465): the rounds the paper shows
    /// (the case's latest), how many there are, its recap and results; nil for a case of another domain.
    private static func itemDetail(_ expected: [String: Any]) -> [String: Any]? {
        guard let rounds = (expected["timeline"] as? [String: Any])?["rounds"] as? [[String: Any]] else { return nil }
        let latest = Set(expected["latest"] as? [String] ?? [])
        var out: [String: Any] = ["rounds": rounds.filter { latest.contains($0["id"] as? String ?? "") },
                                  "rounds_total": rounds.count]
        out["recap"] = expected["recap"]
        out["results"] = expected["results"]
        return out
    }

    @Test func generatedTypesWriteTheFixtureBytesBack() throws {
        let samples = try Self.samples()
        #expect(Set(samples.map(\.definition)) == ["TodoSummary", "TodoCounts", "TodoPlaces", "TodoTimeline", "TodoRecap",
                                                    "TodoResult", "TodoDecidePush", "TodoItemDetail"])
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
            case "TodoDecidePush": try encoder.encode(JSONDecoder().decode(TodoDecidePush.self, from: data))
            case "TodoItemDetail": try encoder.encode(JSONDecoder().decode(TodoItemDetail.self, from: data))
            default: try encoder.encode(JSONDecoder().decode(TodoResult.self, from: data))
            }
            #expect(written == data, "\(name)")
        }
    }

    /// The list's answer as the app reads it (#10465): each row's view beside its row, by id, and the list's counts; a
    /// view or counts that do not decode are none, never a row or the list lost; the row's new facts decode with it.
    @Test func theListAnswerCarriesViewsAndCountsBesideTheRows() async throws {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/todos", json: """
        {"todos":[
          {"id":"a","title":"Plan the trip","part_of":"p","after":["s1","s2"],"restored_at":"2026-11-17T08:00:00Z",
           "someday_until":"9999-12-31T00:00:00Z","view":{"phase":"needs_you","section":"needs_you","reason":"owner_decides",
           "line":{"key":"decision_waiting","args":{"since":"2026-11-18T09:58:00Z"}},"rank":0,
           "asks":[{"id":"todo:decision@2026-11-18T09:58:00Z","kind":"decision","home":"todo","recommend":"Hanami"}]}},
          {"id":"b","title":"No view"},
          {"id":"c","title":"Broken view","view":[1,2]}
        ],"counts":{"open":2,"needs_you":1,"in_progress":0,"sections":{"needs_you":1,"todo":1}}}
        """)
        let page = try await hub.client.todos(etag: nil)
        let list = try #require(page.list)
        #expect(list.map(\.id) == ["a", "b", "c"])
        #expect(page.views.keys.sorted() == ["a"], "no view, and a view that does not decode, are none")
        let view = try #require(page.views["a"])
        #expect(view.section == TodoListSection.needsYou && view.asks?.first?.recommend == "Hanami"
                && view.line?.args?["since"] == "2026-11-18T09:58:00Z")
        #expect(page.counts?.needsYou == 1 && page.counts?.sections[TodoListSection.todo] == 1)
        let a = list[0]
        #expect(a.partOf == "p" && a.after == ["s1", "s2"] && a.restoredAt != nil && a.somedayUntil != nil)
        // The facts round-trip through the rows' cache; the views are not in them.
        let cached = try JSON.decoder.decode(TodoItem.self, from: JSON.encoder.encode(a))
        #expect(cached == a)

        hub.on("GET", "/api/v1/todos", json: #"{"todos":[{"id":"a","title":"Old hub"}],"counts":"nope"}"#)
        let old = try await hub.client.todos(etag: nil)
        #expect(old.list?.count == 1 && old.views.isEmpty && old.counts == nil)
    }
}
