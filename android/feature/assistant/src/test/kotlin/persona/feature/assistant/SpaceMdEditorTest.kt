package persona.feature.assistant

import androidx.activity.ComponentActivity
import androidx.compose.ui.test.*
import androidx.compose.ui.test.junit4.createAndroidComposeRule
import androidx.test.ext.junit.runners.AndroidJUnit4
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import persona.core.contracts.SessionKey
import persona.core.contracts.api.*
import persona.core.designsystem.PersonaTheme

/** The SPACE.md editor (#13500): Persona's blocks go back byte for byte, and each refusal says what happened in plain words. */
@RunWith(AndroidJUnit4::class)
@Config(sdk = [35], qualifiers = "en-w400dp-h900dp-xhdpi")
class SpaceMdEditorTest {
    @get:Rule val rule = createAndroidComposeRule<ComponentActivity>()
    private val state = "<!-- persona:managed:state:begin -->\nWaiting for the refund.\n<!-- persona:managed:state:end -->"
    private val file = "# Money\n\nBudget.\n\n$state\n"

    private class Coded(override val status: Int?) : Exception("refused"), HubCodedFailure {
        override val code: String? = null
    }

    private inner class Api(var refusal: Int?) : AssistantManagement {
        val writes = mutableListOf<Pair<String, String>>()
        var reads = 0
        override suspend fun spaceMd(slug: String): SpaceMd { reads++; return SpaceMd(file, "d$reads", "Waiting for the refund.") }
        override suspend fun writeSpaceMd(slug: String, content: String, baseDigest: String): SpaceMd {
            writes += content to baseDigest
            refusal?.let { throw Coded(it) }
            return SpaceMd(content, "saved", "")
        }
        override suspend fun warm() = Unit
        override suspend fun endConversation(session: SessionKey) = Unit
        override suspend fun tasks(): List<ScheduledTask> = emptyList()
        override suspend fun saveTask(task: ScheduledTask): ScheduledTask = task
        override suspend fun deleteTask(id: String) = Unit
        override suspend fun runTask(id: String): SessionKey = error("Unexpected")
        override suspend fun freshTask(id: String): ScheduledTask = error("Unexpected")
        override suspend fun runs(): List<Run> = emptyList()
        override suspend fun notes() = AssistantNotes()
        override suspend fun setNotes(text: String) = Unit
        override suspend fun memory(): List<MemoryFile> = emptyList()
        override suspend fun saveMemory(name: String, content: String) = Unit
        override suspend fun deleteMemory(name: String) = Unit
    }

    private fun mount(api: Api, saved: MutableList<SpaceMd> = mutableListOf()) {
        rule.setContent {
            PersonaTheme(reduceMotion = true) { SpaceMdEditor("qa-finance", api, onDismiss = {}) { saved += it } }
        }
        rule.waitUntil(5_000) { rule.onAllNodesWithTag("space-md-field-0").fetchSemanticsNodes().isNotEmpty() }
    }

    /** The text field itself: the tag sits on [persona.core.designsystem.JTextField]'s wrapper, which has no SetText action. */
    private fun field(index: Int) = rule.onNode(hasSetTextAction() and hasAnyAncestor(hasTestTag("space-md-field-$index")))

    @Test fun savesTheUsersPartAndPutsTheBlockBackByteForByte() {
        val api = Api(null)
        val saved = mutableListOf<SpaceMd>()
        mount(api, saved)
        rule.onNodeWithTag("space-md-managed-state").assertExists()
        field(0).performTextReplacement("# Money\n\nBudget and taxes.\n\n")
        rule.onNodeWithTag("space-md-save").performClick()
        rule.waitUntil(5_000) { saved.isNotEmpty() }
        val (content, digest) = api.writes.single()
        assertEquals("d1", digest)
        assertEquals("# Money\n\nBudget and taxes.\n\n$state\n", content)
        assertTrue(SpaceMdText.sameManagedBlocks(file, content))
    }

    @Test fun changedElsewhereKeepsWhatWasTypedAndOffersTheLatest() {
        val api = Api(409)
        mount(api)
        field(0).performTextReplacement("Mine")
        rule.onNodeWithTag("space-md-save").performClick()
        rule.waitUntil(5_000) { rule.onAllNodesWithTag("space-md-error").fetchSemanticsNodes().isNotEmpty() }
        rule.onNodeWithText("This was changed somewhere else", substring = true).assertExists()
        field(0).assert(hasText("Mine"))
        rule.onNodeWithText("Load latest").performClick()
        rule.waitUntil(5_000) { api.reads == 2 }
    }

    @Test fun aMarkerTypedIntoTheFieldIsRefusedBeforeSending() {
        val api = Api(null)
        mount(api)
        field(0).performTextReplacement("<!-- persona:managed:state:begin -->x<!-- persona:managed:state:end -->")
        rule.onNodeWithTag("space-md-save").performClick()
        rule.waitForIdle()
        rule.onNodeWithText("The part Persona keeps up to date can't be changed here", substring = true).assertExists()
        assertEquals(emptyList<Pair<String, String>>(), api.writes)
    }

    @Test fun saveStaysOffUntilSomethingChanges() {
        val api = Api(null)
        mount(api)
        rule.onNodeWithTag("space-md-save").assertIsNotEnabled()
        field(0).performTextReplacement("# Money\n\nBudget.\n\n")
        rule.onNodeWithTag("space-md-save").assertIsNotEnabled()
        field(0).performTextReplacement("# Money\n\nBudget and rent.\n\n")
        rule.onNodeWithTag("space-md-save").assertIsEnabled()
        assertEquals(emptyList<Pair<String, String>>(), api.writes)
    }

    @Test fun anOfflineComputerSaysSo() {
        mount(Api(503))
        field(0).performTextReplacement("# Money\n\nBudget and rent.\n\n")
        rule.onNodeWithTag("space-md-save").performClick()
        rule.waitUntil(5_000) { rule.onAllNodesWithTag("space-md-error").fetchSemanticsNodes().isNotEmpty() }
        rule.onNodeWithText("The assistant's computer is offline", substring = true).assertExists()
    }
}
