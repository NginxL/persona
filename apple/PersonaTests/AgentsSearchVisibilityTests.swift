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
                    let expected = floating || retained ? 0 : min(max(band - scrolled, 0), band)
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
            #expect(retained == !query.isEmpty, "query \(query)")
        }
    }

    @Test func clearingRestoresTheCurrentEmptyScrollState() {
        #expect(AgentsSearchVisibility.rowShown(scrolled: 120, band: band, floating: false, query: "a") == 0)
        #expect(AgentsSearchVisibility.rowShown(scrolled: 120, band: band, floating: false, query: "") == 0)
        #expect(AgentsSearchVisibility.rowShown(scrolled: 0, band: band, floating: false, query: "") == band)
        #expect(!AgentsSearchVisibility.retained(query: ""))
    }
}

@MainActor
struct AgentsSearchStartTests {
    @Test func pushCancelsThePendingSearchBeforeTheKeyboardAnswers() {
        let header = AgentsHeader()
        let start = header.startSearch()
        #expect(header.floating)

        header.closeSearchForPush(wasSearching: false)
        #expect(!header.floating)
        #expect(!header.up)
        #expect(!header.compact)
        #expect(!header.completeSearchStart(start, listInFront: false))
        // Even after Back, the same callback cannot bring the cancelled field up.
        #expect(!header.completeSearchStart(start, listInFront: true))
    }

    @Test func coveredListRejectsTheCurrentCallback() {
        let header = AgentsHeader()
        let start = header.startSearch()
        #expect(!header.completeSearchStart(start, listInFront: false))
        #expect(!header.floating)
        #expect(!header.completeSearchStart(start, listInFront: true))
    }

    @Test func onlyTheCurrentStartCanRiseAndOnlyOnce() {
        let header = AgentsHeader()
        let interrupted = header.startSearch()
        header.closeSearchForPush(wasSearching: false)
        let current = header.startSearch()

        #expect(!header.completeSearchStart(interrupted, listInFront: true))
        #expect(header.completeSearchStart(current, listInFront: true))
        #expect(!header.completeSearchStart(current, listInFront: true))
        #expect(header.floating)

        header.up = true
        header.setCompact(true)
        header.closeSearchForPush(wasSearching: true)
        #expect(!header.floating)
        #expect(!header.up)
        #expect(!header.compact)
    }
}
