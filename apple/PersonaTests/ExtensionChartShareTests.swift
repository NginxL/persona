import Testing
@testable import Persona

/// A stacked bar chart of one category (every series holds one point, like a single day's parts) is a horizontal
/// segmented bar, not a lone vertical column; several categories keep the columns. The same rule on every app
/// (docs/extensions/extensions.md「New items」chart; issue #5386).
// DataChart is a SwiftUI View, so its main-actor-isolated isSingleCategoryShare predicate can only be called from the
// main actor; a nonisolated Swift Testing suite calls it off-actor and traps at runtime (the build only warns).
@MainActor
struct ExtensionChartShareTests {
    private func series(_ values: [Double]) -> [DataChart.Series] {
        values.enumerated().map { DataChart.Series(name: "Series \($0.offset + 1)", color: nil, values: [$0.element]) }
    }

    @Test func oneCategoryStackedBarIsAShareBar() {
        #expect(DataChart.isSingleCategoryShare(kind: .bar, stacked: true, series: series([300, 600, 100])))
    }

    @Test func severalCategoriesStayColumns() {
        let two = [
            DataChart.Series(name: "a", color: nil, values: [1, 2]),
            DataChart.Series(name: "b", color: nil, values: [3, 4]),
        ]
        #expect(!DataChart.isSingleCategoryShare(kind: .bar, stacked: true, series: two))
    }

    @Test func nonStackedBarsLinesNegativesAndZeroTotalsStayAsTheyAre() {
        #expect(!DataChart.isSingleCategoryShare(kind: .bar, stacked: false, series: series([100, 50])))
        #expect(!DataChart.isSingleCategoryShare(kind: .line, stacked: true, series: series([100])))
        #expect(!DataChart.isSingleCategoryShare(kind: .bar, stacked: true, series: series([100, -50])))
        #expect(!DataChart.isSingleCategoryShare(kind: .bar, stacked: true, series: series([0, 0])))
        #expect(!DataChart.isSingleCategoryShare(kind: .bar, stacked: true, series: []))
    }

    @Test func aZeroValuePartStillLinesUpWithTheOtherShares() {
        #expect(DataChart.isSingleCategoryShare(kind: .bar, stacked: true, series: series([0, 100])))
    }

    @Test func finiteValuesWhoseSumOverflowsStillShareTheBar() {
        let shares = DataChart.singleCategoryShares(kind: .bar, stacked: true, series: series([Double.greatestFiniteMagnitude, Double.greatestFiniteMagnitude]))
        #expect(shares == [0.5, 0.5])
    }

    @Test func nonFiniteValuesStayAsTheyAre() {
        #expect(!DataChart.isSingleCategoryShare(kind: .bar, stacked: true, series: series([Double.nan, 1])))
        #expect(!DataChart.isSingleCategoryShare(kind: .bar, stacked: true, series: series([Double.infinity, 1])))
    }
}
