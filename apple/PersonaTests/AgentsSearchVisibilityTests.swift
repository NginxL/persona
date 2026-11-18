import SwiftUI
import Testing
@testable import Persona

/// The empty-query oracle is the old `band - scrolled` clamp at every representative position (#7332).
struct AgentsSearchVisibilityTests {
    private let band: CGFloat = 54

    @Test func rowVisibilityMatrixMatchesTheEmptyFieldOracle() {
        let positions: [CGFloat] = [-20, 0, 27, 54, 120]
        for scrolled in positions {
            for floating in [false, true] {
                for query in ["", "a"] {
                    let retained = AgentsSearchVisibility.retained(query: query)
                    let shown = AgentsSearchVisibility.rowShown(scrolled: scrolled, band: band,
                                                                floating: floating, query: query)
                    #if os(iOS)
                    let expected = floating || retained ? 0 : min(max(band - scrolled, 0), band)
                    #else
                    let expected = floating ? 0 : min(max(band - scrolled, 0), band)
                    #endif
                    #expect(shown == expected, "scrolled \(scrolled), floating \(floating), query \(query)")
                    // These are the exact inputs used by the row's hit testing and accessibility hiding.
                    #expect((shown >= band / 2) == (expected >= band / 2))
                    #expect((shown < band / 2) == (expected < band / 2))
                }
            }
        }
    }

    @Test func retainedQueryMatrix() {
        for query in ["", "a", " "] {
            let retained = AgentsSearchVisibility.retained(query: query)
            #if os(iOS)
            #expect(retained == !query.isEmpty, "query \(query)")
            #else
            #expect(!retained, "retained search is scoped to iOS")
            #endif
        }
    }

    @Test func clearingRestoresTheCurrentEmptyScrollState() {
        #expect(AgentsSearchVisibility.rowShown(scrolled: 120, band: band, floating: false, query: "a") == 0)
        #expect(AgentsSearchVisibility.rowShown(scrolled: 120, band: band, floating: false, query: "") == 0)
        #expect(AgentsSearchVisibility.rowShown(scrolled: 0, band: band, floating: false, query: "") == band)
        #expect(!AgentsSearchVisibility.retained(query: ""))
    }
}
