package persona.feature.settings

import org.junit.Assert.assertEquals
import org.junit.Test

/**
 * An extension download's size picks its unit and decimals after rounding, as the machine pages' `MetricFormat.bytes`
 * does (#9903, #9890).
 */
class OperationSizeTest {
    @Test fun aSizeThatRoundsTo1024KbIsAMegabyte() {
        // 1023.5 KB and up round to 1024 KB: that is 1.0 MB
        assertEquals("1.0 MB", OperationText.size(1_048_064))
        assertEquals("1.0 MB", OperationText.size(1_048_575))
        assertEquals("1023 KB", OperationText.size(1_048_063))
    }

    @Test fun oneDecimalOnlyWhileTheRoundedFigureIsUnderTen() {
        // 9.95 KB rounds to 10.0: no decimal
        assertEquals("10 KB", OperationText.size(10_189))
        assertEquals("9.9 KB", OperationText.size(10_188))
        // 9.99 MB rounds to 10.0: no decimal
        assertEquals("10 MB", OperationText.size(10_480_000))
    }

    @Test fun otherSizesReadAsBefore() {
        assertEquals("1023 B", OperationText.size(1_023))
        assertEquals("1.0 KB", OperationText.size(1_024))
        assertEquals("1.5 KB", OperationText.size(1_536))
        assertEquals("512 KB", OperationText.size(524_288))
        assertEquals("3.0 MB", OperationText.size(3_145_728))
        assertEquals("16 MB", OperationText.size(16_777_216))
    }
}
