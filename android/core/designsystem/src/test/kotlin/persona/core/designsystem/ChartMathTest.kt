package persona.core.designsystem

import org.junit.Assert.*
import org.junit.Test

// The arithmetic behind JChart (extensions' charts, docs/extensions/extensions.md "UI slots"): short numbers, the range with zero and
// stacked sums, round ticks, and labels that never overlap however many there are.
class ChartMathTest {
    @Test fun compactNumbers() {
        assertEquals("1.2M", ChartMath.compact(1_200_000.0))
        assertEquals("850K", ChartMath.compact(850_000.0))
        assertEquals("12K", ChartMath.compact(12_000.0))
        assertEquals("1,234", ChartMath.compact(1234.0))
        assertEquals("120", ChartMath.compact(120.0))
        assertEquals("3.5", ChartMath.compact(3.5))
        assertEquals("0.12", ChartMath.compact(0.123))
        assertEquals("-2.5M", ChartMath.compact(-2_500_000.0))
        assertEquals("0", ChartMath.compact(0.0))
        assertEquals("—", ChartMath.compact(Double.NaN))
    }

    private fun data(stacked: Boolean, vararg series: List<Double>) =
        JChartData(JChartKind.Bar, series.first().indices.map { "d$it" }, series.map { JChartSeries(null, it, it.map(Double::toString)) }, stacked)

    @Test fun theRangeHasZeroAndStackedSums() {
        assertEquals(0.0 to 5.0, ChartMath.range(data(false, listOf(1.0, 5.0), listOf(2.0, 3.0))))
        assertEquals(0.0 to 8.0, ChartMath.range(data(true, listOf(1.0, 5.0), listOf(2.0, 3.0))))
        assertEquals(-4.0 to 2.0, ChartMath.range(data(true, listOf(-1.0, 2.0), listOf(-3.0, 0.0))))
        assertEquals(0.0 to 1.0, ChartMath.range(data(false, listOf(0.0, 0.0))))
    }

    @Test fun ticksAreRoundAndCoverTheRange() {
        val ticks = ChartMath.ticks(0.0, 8.3)
        assertEquals(0.0, ticks.first(), 0.0)
        assertTrue(ticks.last() >= 8.3)
        assertTrue(ticks.size in 3..5)
        val negative = ChartMath.ticks(-4.0, 2.0)
        assertTrue(0.0 in negative)
        assertTrue(negative.first() <= -4.0 && negative.last() >= 2.0)
    }

    @Test fun labelsNeverOverlap() {
        for (n in listOf(1, 2, 3, 7, 30, 90, 120)) for (width in listOf(120f, 300f, 700f)) {
            val widths = List(n) { if (it % 3 == 0) 40f else 28f }
            val shown = ChartMath.labelIndices(widths, width, 8f)
            assertEquals("first n=$n width=$width", 0, shown.first())
            assertTrue("in order", shown.zipWithNext().all { (a, b) -> a < b })
            shown.zipWithNext().forEach { (a, b) ->
                val rightOfA = ChartMath.labelLeft(a, widths[a], width, n) + widths[a]
                assertTrue("n=$n width=$width $a/$b overlap", ChartMath.labelLeft(b, widths[b], width, n) >= rightOfA + 8f)
            }
        }
        // Room for every one: all of them, the last one included.
        assertEquals((0 until 7).toList(), ChartMath.labelIndices(List(7) { 30f }, 700f, 8f))
        // Crowded: the last one shows rather than the one before it.
        assertEquals(29, ChartMath.labelIndices(List(30) { 30f }, 320f, 8f).last())
    }

    @Test fun aCardWritesTheFirstAndLastWhenTheyFit() {
        assertEquals(listOf(0, 6), ChartMath.endIndices(7, 200f, 40f, 40f, 8f))
        assertEquals(listOf(0), ChartMath.endIndices(7, 80f, 40f, 40f, 8f))
        assertEquals(listOf(0), ChartMath.endIndices(1, 200f, 40f, 40f, 8f))
    }

    @Test fun tapsPickTheSlotUnderThem() {
        assertEquals(0, ChartMath.indexAt(1f, 700f, 7))
        assertEquals(6, ChartMath.indexAt(699f, 700f, 7))
        assertEquals(6, ChartMath.indexAt(900f, 700f, 7))
        assertEquals(0, ChartMath.indexAt(-5f, 700f, 7))
    }

    @Test fun statGridColumns() {
        assertEquals(1, statColumns(1, 320f))
        assertEquals(2, statColumns(2, 320f))
        assertEquals(3, statColumns(3, 320f))
        assertEquals(2, statColumns(3, 260f))
        assertEquals(2, statColumns(4, 600f))
        assertEquals(3, statColumns(6, 360f))
    }

    private fun shareData(kind: JChartKind = JChartKind.Bar, stacked: Boolean = true, values: List<List<Double>>) =
        JChartData(kind, values.first().indices.map { "d$it" },
            values.mapIndexed { i, v -> JChartSeries("s$i", v, v.map(Double::toString)) }, stacked)

    @Test fun oneCategoryStackedBarSplitsIntoShares() {
        val shares = ChartMath.singleCategoryShares(shareData(values = listOf(listOf(300.0), listOf(600.0), listOf(100.0))))
        assertEquals(listOf(0.3, 0.6, 0.1), shares)
    }

    @Test fun severalCategoriesStayColumns() {
        assertNull(ChartMath.singleCategoryShares(shareData(values = listOf(listOf(1.0, 2.0), listOf(3.0, 4.0)))))
    }

    @Test fun nonStackedBarsLinesNegativesAndZeroTotalsStayColumns() {
        assertNull(ChartMath.singleCategoryShares(shareData(stacked = false, values = listOf(listOf(100.0), listOf(50.0)))))
        assertNull(ChartMath.singleCategoryShares(shareData(kind = JChartKind.Line, values = listOf(listOf(100.0)))))
        assertNull(ChartMath.singleCategoryShares(shareData(values = listOf(listOf(100.0), listOf(-50.0)))))
        assertNull(ChartMath.singleCategoryShares(shareData(values = listOf(listOf(0.0), listOf(0.0)))))
        assertNull(ChartMath.singleCategoryShares(JChartData(JChartKind.Bar, emptyList(), emptyList(), stacked = true)))
    }

    @Test fun aZeroValuePartStillLinesUpWithTheOtherShares() {
        assertEquals(listOf(0.0, 1.0), ChartMath.singleCategoryShares(shareData(values = listOf(listOf(0.0), listOf(100.0)))))
    }

    @Test fun finiteValuesWhoseSumOverflowsStillShareTheBar() {
        val shares = ChartMath.singleCategoryShares(shareData(values = listOf(listOf(Double.MAX_VALUE), listOf(Double.MAX_VALUE))))
        assertEquals(listOf(0.5, 0.5), shares)
    }

    @Test fun nonFiniteValuesStayColumns() {
        assertNull(ChartMath.singleCategoryShares(shareData(values = listOf(listOf(Double.NaN), listOf(1.0)))))
        assertNull(ChartMath.singleCategoryShares(shareData(values = listOf(listOf(Double.POSITIVE_INFINITY), listOf(1.0)))))
    }
}
