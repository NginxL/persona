import Foundation
import Testing
@testable import Persona

/// Replays spec/fixtures/inbox/timeline.json, the timeline contract shared with the hub, web app and Android.
@MainActor
struct InboxTimelineFixtureTests {
    private static let repo = URL(fileURLWithPath: #filePath).deletingLastPathComponent().deletingLastPathComponent().deletingLastPathComponent()

    private struct ChainOnly: Decodable { var chain: [InboxItem.ChainEntry] }

    private struct Fixture: Decodable {
        var via_icons: [String: String]
        var reported_icon: String
        var kind_icons_without_via: [String: String]
        var cases: [Case]

        struct Case: Decodable {
            var name: String
            var input: Input
            var expected: Expected

            struct Input: Decodable { var item: ChainOnly }
            struct Expected: Decodable {
                var folded: [String]
                var expanded: [String]
                var steps: [Step]

                struct Step: Decodable {
                    var id: String
                    var icon: String
                    var reported: Bool
                }
            }
        }
    }

    private static func fixture() throws -> Fixture {
        let data = try Data(contentsOf: repo.appending(path: "spec/fixtures/inbox/timeline.json"))
        return try JSON.decoder.decode(Fixture.self, from: data)
    }

    @Test func everySharedTimelineCase() throws {
        for c in try Self.fixture().cases {
            let chain = c.input.item.chain
            #expect(InboxEventText.chainRows(chain, expanded: false).map(\.id) == c.expected.folded, "\(c.name)")
            #expect(InboxEventText.chainRows(chain, expanded: true).map(\.id) == c.expected.expanded, "\(c.name)")
            for step in c.expected.steps {
                let entry = try #require(chain.first { $0.id == step.id }, "\(c.name): \(step.id)")
                #expect(InboxEventText.iconMeaning(entry) == step.icon, "\(c.name): \(step.id)")
                #expect((entry.kind == "reported") == step.reported, "\(c.name): \(step.id)")
                #expect(!InboxEventText.symbol(entry).isEmpty, "\(c.name): \(step.id)")
            }
        }
    }

    @Test func theSharedIconTablesAreExhaustive() throws {
        let fixture = try Self.fixture()
        #expect(fixture.reported_icon == "reported")
        for (via, meaning) in fixture.via_icons {
            let entry = InboxItem.ChainEntry(id: via, kind: "something_new", text: "A new step", via: via)
            #expect(InboxEventText.iconMeaning(entry) == meaning, "\(via)")
        }
        for (kind, meaning) in fixture.kind_icons_without_via {
            let entry = InboxItem.ChainEntry(id: kind, kind: kind, text: "An older step")
            #expect(entry.via == nil)
            #expect(InboxEventText.iconMeaning(entry) == meaning, "\(kind)")
        }
        // A reported step is an account by the assistant or Agent, never a program-confirmed source step.
        let reported = InboxItem.ChainEntry(id: "r", kind: "reported", text: "Sent it", via: "agent")
        #expect(InboxEventText.iconMeaning(reported) == fixture.reported_icon)
        #expect(InboxEventText.symbol(reported) == Theme.Icon.reported)
    }

    @Test func emptyViaFallsBackToTheKindAndBadViaDoesNotLoseTheEntry() throws {
        let chain = try JSON.decoder.decode(ChainOnly.self, from: Data(#"""
        {"chain":[
          {"id":"c1","kind":"received","text":"Received","via":""},
          {"id":"c2","kind":"received","text":"Still decoded","via":42}
        ]}
        """#.utf8)).chain
        #expect(chain.count == 2)
        #expect(chain.map(\.via) == [String?](repeating: nil, count: 2))
        #expect(chain.allSatisfy { InboxEventText.iconMeaning($0) == "received" })
    }
}
