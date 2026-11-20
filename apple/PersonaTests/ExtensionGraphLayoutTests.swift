import Foundation
import Testing
@testable import Persona
import PersonaWire

/// Every field of every shared graph-layout case (`spec/fixtures/graph-layout/cases.json`): the same slots, drawn
/// edges, reading order, depths, fold counts and group frames as the reference implementation, including its two
/// 200-node trees. A client's node pitch is deliberately not part of this fixture.
struct ExtensionGraphLayoutTests {
    private static let repo = URL(fileURLWithPath: #filePath).deletingLastPathComponent()
        .deletingLastPathComponent().deletingLastPathComponent()

    private struct Fixtures: Decodable {
        struct Case: Decodable {
            struct Expected: Decodable {
                struct Frame: Decodable {
                    var group: String
                    var members: [String]
                }
                var at: [String: [Double]]
                var size: [Double]
                var edges: [[String]]
                var order: [String]
                var depth: [String: Int]
                var folds: [String: Int]
                var frames: [Frame]
            }
            var name: String
            var graph: ExtensionGraphComponent
            var expected: Expected
        }
        var version: Int
        var cases: [Case]
    }

    @Test func everySharedCaseMatchesEveryField() throws {
        let data = try Data(contentsOf: Self.repo.appending(path: "spec/fixtures/graph-layout/cases.json"))
        let fixtures = try JSONDecoder().decode(Fixtures.self, from: data)
        #expect(fixtures.version == 1)
        #expect(fixtures.cases.count == 19)

        for test in fixtures.cases {
            let result = ExtensionGraphLayouter.layout(test.graph)
            let expected = test.expected
            #expect(Set(result.at.keys) == Set(expected.at.keys), "\(test.name): visible nodes")
            for (id, point) in expected.at {
                let actual = try #require(result.at[id], "\(test.name): \(id)")
                #expect(point.count == 2, "\(test.name): malformed expected point")
                #expect(abs(actual.x - point[0]) <= 1e-9, "\(test.name): \(id) x")
                #expect(abs(actual.y - point[1]) <= 1e-9, "\(test.name): \(id) y")
            }
            #expect(expected.size.count == 2, "\(test.name): malformed expected size")
            #expect(abs(result.size.width - expected.size[0]) <= 1e-9, "\(test.name): width")
            #expect(abs(result.size.height - expected.size[1]) <= 1e-9, "\(test.name): height")
            #expect(result.edges.map { [$0.from, $0.to] } == expected.edges, "\(test.name): edges")
            #expect(result.order == expected.order, "\(test.name): reading order")
            #expect(result.depth == expected.depth, "\(test.name): depths")
            #expect(result.folds == expected.folds, "\(test.name): folds")
            #expect(result.frames.map { [$0.group] + $0.members } == expected.frames.map { [$0.group] + $0.members },
                    "\(test.name): frames")
        }
    }

    @Test func anExplicitFoldSetUsesTheSameLayoutWithoutChangingTheGraph() throws {
        let graph = ExtensionGraphComponent(nodes: [
            ExtensionGraphNode(id: "root", title: "Root"),
            ExtensionGraphNode(id: "a", title: "A"),
            ExtensionGraphNode(id: "b", title: "B"),
        ], edges: [ExtensionGraphEdge(from: "root", to: "a"), ExtensionGraphEdge(from: "root", to: "b")],
        folded: ["root"])
        let open = ExtensionGraphLayouter.layout(graph, folded: [])
        #expect(open.order == ["root", "a", "b"])
        #expect(open.at["root"] == .init(x: 0, y: 0.5))
        #expect(open.folds == ["root": 0])
        #expect(graph.folded == ["root"], "laying out another fold set does not mutate the view")
    }
}
