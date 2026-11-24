package persona.core.contracts

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test
import persona.core.contracts.api.SpaceMdPart
import persona.core.contracts.api.SpaceMdText

/** SPACE.md in the editor (#13500), the web's `space-md.test.ts`: the user's parts are fields, Persona's blocks go back byte for byte. */
class SpaceMdTest {
    private val state = "<!-- persona:managed:state:begin -->\nTaxes filed; waiting for the refund.\n<!-- persona:managed:state:end -->"
    private val file = "# Money\n\nWhat this space is for.\n\n$state\n"

    @Test fun splitsAndJoinsBackUnchanged() {
        val parts = SpaceMdText.split(file)
        assertEquals(listOf("human", "managed", "human"), parts.map { if (it is SpaceMdPart.Managed) "managed" else "human" })
        val block = parts[1] as SpaceMdPart.Managed
        assertEquals("state", block.name)
        assertEquals("\nTaxes filed; waiting for the refund.\n", block.body)
        assertEquals(file, SpaceMdText.join(parts))
    }

    @Test fun keepsABlockWithoutItsEndMarkerReadOnlyToTheEnd() {
        val half = "notes\n<!-- persona:managed:state:begin -->\nhalf"
        val parts = SpaceMdText.split(half)
        assertEquals(2, parts.size)
        assertTrue(parts[1] is SpaceMdPart.Managed)
        assertEquals(half, SpaceMdText.join(parts))
    }

    @Test fun showsFieldsForThePartsWithWordsAndOneAtTheStartOtherwise() {
        assertEquals(listOf(0), SpaceMdText.editable(SpaceMdText.split(file)).editable)
        val onlyBlock = SpaceMdText.editable(SpaceMdText.split("$state\n"))
        assertEquals(listOf(0), onlyBlock.editable)
        assertEquals(SpaceMdPart.Human(""), onlyBlock.parts[0])
        assertEquals(listOf(SpaceMdPart.Human("")), SpaceMdText.editable(SpaceMdText.split("")).parts)
    }

    @Test fun putsTheEditsInAndTheBlocksBackByteForByte() {
        val parts = SpaceMdText.split(file)
        val out = SpaceMdText.assemble(parts, mapOf(0 to "# Money\n\nBudget and taxes.\n\n"))
        assertEquals("# Money\n\nBudget and taxes.\n\n$state\n", out)
        assertTrue(SpaceMdText.sameManagedBlocks(file, out))
    }

    @Test fun keepsABlocksMarkerOnALineOfItsOwn() {
        val parts = SpaceMdText.editable(SpaceMdText.split(state)).parts
        assertEquals("Family notes\n\n$state", SpaceMdText.assemble(parts, mapOf(0 to "Family notes")))
    }

    @Test fun noticesABlockChangedOrAMarkerTyped() {
        assertFalse(SpaceMdText.sameManagedBlocks(file, file.replace("refund", "check")))
        assertFalse(SpaceMdText.sameManagedBlocks(file,
            "<!-- persona:managed:state:begin -->x<!-- persona:managed:state:end -->$file"))
        assertFalse(SpaceMdText.sameManagedBlocks(file, file.replace(state, "")))
    }

    @Test fun theHomeShowsOnlyWhatTheUserWrote() {
        assertEquals("# Money\n\nWhat this space is for.", SpaceMdText.human(file))
        assertEquals("", SpaceMdText.human("$state\n"))
        assertEquals("Before\n\nAfter", SpaceMdText.human("Before\n\n$state\n\nAfter\n"))
    }

    @Test fun dropsALeadingTitleThatRepeatsTheName() {
        assertEquals("What this space is for.", SpaceMdText.body("# Money\n\nWhat this space is for.\n", "money"))
        assertEquals("# Bills\n\nWhat this space is for.", SpaceMdText.body("# Bills\n\nWhat this space is for.\n", "Money"))
        assertEquals("## Money\n\nText", SpaceMdText.body("## Money\n\nText\n", "Money"))
        assertEquals("", SpaceMdText.body("# Money\n", "Money"))
    }
}
