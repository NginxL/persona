package persona.feature.assistant

import android.graphics.Bitmap
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.asAndroidBitmap
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.test.assertIsDisplayed
import androidx.compose.ui.test.captureToImage
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onAllNodesWithText
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.compose.ui.test.performTextInput
import androidx.test.ext.junit.runners.AndroidJUnit4
import org.junit.Assert.assertEquals
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.core.contracts.Item
import persona.core.contracts.SessionKey
import persona.core.contracts.api.StarredConversation
import persona.core.contracts.api.StarredIndex
import persona.core.contracts.api.StarredList
import persona.core.contracts.api.StarredMessagesApi
import persona.core.contracts.api.StarredReference
import persona.core.contracts.api.StarredSnapshot
import persona.core.designsystem.AppFeedback
import persona.core.designsystem.AppStyle
import persona.core.designsystem.FeedbackAction
import persona.core.designsystem.LocalAppFeedback
import persona.core.designsystem.PersonaTheme
import persona.feature.chat.StarredMessagesStore
import java.io.File

@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "en-w412dp-h860dp-xhdpi")
class StarredMessagesScreenTest {
    @get:Rule val rule = createComposeRule()

    private class FakeApi(
        initial: List<StarredSnapshot>,
        private val canWrite: Boolean = true,
        private val missingOriginals: Set<String> = emptySet(),
    ) : StarredMessagesApi {
        val items = initial.toMutableList()
        var failNextAdd = false
        override suspend fun index() = StarredIndex(
            refs = items.map { StarredReference(it.id!!, it.machineId, it.sessionId, it.itemId, it.conversation, it.sourceFingerprint) },
            canWrite = canWrite,
        )
        override suspend fun list() = StarredList(items.toList(), canWrite = canWrite)
        override suspend fun add(star: StarredSnapshot): StarredSnapshot {
            if (failNextAdd) {
                failNextAdd = false
                error("restore failed")
            }
            val saved = star.copy(id = star.id ?: "sm-restored")
            items.add(0, saved)
            return saved
        }
        override suspend fun remove(id: String): StarredSnapshot? {
            val found = items.firstOrNull { it.id == id } ?: return null
            items.remove(found)
            return found
        }
        override suspend fun fullItem(session: SessionKey, itemId: String): Item {
            if (itemId in missingOriginals) return Item(id = "different-id")
            return Item(id = itemId, role = "assistant", kind = "text", text = "Verified")
        }
    }

    private fun sampleStars(): List<StarredSnapshot> = listOf(
        StarredSnapshot(
            id = "sm1",
            machineId = "server",
            sessionId = "s-main",
            itemId = "m-checklist",
            conversation = StarredConversation.Assistant,
            role = "assistant",
            title = "Release Checklist",
            speaker = "Persona",
            writtenAt = "2026-11-22T09:15:00Z",
            text = "Ship checklist:\n- Verify token baseline\n- Capture light and dark Glass screenshots\n- Confirm zero personal data leakage",
        ),
        StarredSnapshot(
            id = "sm2",
            machineId = "macbook",
            sessionId = "s-review",
            itemId = "m-long",
            conversation = StarredConversation.Agent,
            role = "user",
            title = "PR Review Notes",
            speaker = "",
            writtenAt = "2026-11-21T16:40:00Z",
            text = (1..8).joinToString("\n") { "Review point $it: keep every card concise and scannable in Glass." },
            omittedChars = 120,
        ),
    )

    @Test fun searchExpandOpenOriginalAndUnstarWork() {
        val api = FakeApi(sampleStars())
        val store = StarredMessagesStore(api, offered = true)
        val opened = mutableListOf<String>()

        rule.setContent {
            PersonaTheme(style = AppStyle.Glass, darkTheme = false, reduceMotion = true) {
                StarredMessagesScreen(
                    store = store,
                    onBack = {},
                    onOpenOriginal = { opened += "${it.machineId}/${it.sessionId}" },
                )
            }
        }

        rule.waitUntil(5000) { rule.onAllNodesWithText("Release Checklist").fetchSemanticsNodes().isNotEmpty() }
        rule.onNodeWithText("PR Review Notes").assertIsDisplayed()
        rule.onNodeWithText("Show more").assertIsDisplayed().performClick()
        rule.onNodeWithText("Show less").assertIsDisplayed()
        rule.onNodeWithText("120 additional characters").assertIsDisplayed()

        rule.onNodeWithText("Search starred messages").performTextInput("checklist")
        rule.waitForIdle()
        rule.onNodeWithText("Release Checklist").assertIsDisplayed()
        assertEquals(0, rule.onAllNodesWithText("PR Review Notes").fetchSemanticsNodes().size)

        rule.onNodeWithTag("starred-open-original").performClick()
        rule.waitUntil(5000) { opened.isNotEmpty() }
        assertEquals(listOf("server/s-main"), opened)

        rule.onNodeWithTag("starred-unstar").performClick()
        rule.waitUntil(5000) { rule.onAllNodesWithText("Release Checklist").fetchSemanticsNodes().isEmpty() }
        rule.onNodeWithText("No matching messages").assertIsDisplayed()
    }

    @Test fun undoRestoresRemovedStarAfterCardDisposalAndSurfacesFailure() {
        val api = FakeApi(listOf(sampleStars().first()))
        val store = StarredMessagesStore(api, offered = true)
        val notices = mutableListOf<String>()
        var lastAction: FeedbackAction? = null
        val feedback = object : AppFeedback {
            override fun failure(error: Throwable, operation: Boolean): Boolean = true
            override fun notice(message: String): Boolean {
                notices += message
                return true
            }
            override fun notice(message: String, action: FeedbackAction): Boolean {
                notices += message
                lastAction = action
                return true
            }
        }

        rule.setContent {
            CompositionLocalProvider(LocalAppFeedback provides feedback) {
                PersonaTheme(style = AppStyle.Glass, darkTheme = false, reduceMotion = true) {
                    StarredMessagesScreen(store = store, onBack = {}, onOpenOriginal = {})
                }
            }
        }

        rule.waitUntil(5000) { rule.onAllNodesWithText("Release Checklist").fetchSemanticsNodes().isNotEmpty() }
        rule.onNodeWithTag("starred-unstar").performClick()
        // Wait for the card to be completely removed & disposed from the LazyColumn.
        rule.waitUntil(5000) { rule.onAllNodesWithText("Release Checklist").fetchSemanticsNodes().isEmpty() }
        rule.waitUntil(5000) { lastAction != null }

        // Tapping Undo after card disposal still runs in the screen-level scope and restores the snapshot.
        rule.runOnIdle { lastAction!!.run() }
        rule.waitUntil(5000) { rule.onAllNodesWithText("Release Checklist").fetchSemanticsNodes().isNotEmpty() }
        assertEquals(1, api.items.size)

        // If Undo fails to restore, the failure notice is surfaced.
        lastAction = null
        rule.onNodeWithTag("starred-unstar").performClick()
        rule.waitUntil(5000) { rule.onAllNodesWithText("Release Checklist").fetchSemanticsNodes().isEmpty() }
        rule.waitUntil(5000) { lastAction != null }
        api.failNextAdd = true
        val noticesBefore = notices.size
        rule.runOnIdle { lastAction!!.run() }
        rule.waitUntil(5000) { notices.size > noticesBefore }
        assertEquals("Couldn't update starred message", notices.last())
    }

    @Test fun unsupportedHubShowsUnavailableMessage() {
        val store = StarredMessagesStore(api = null, offered = false)
        rule.setContent {
            PersonaTheme(style = AppStyle.Glass, darkTheme = false, reduceMotion = true) {
                StarredMessagesScreen(store = store, onBack = {}, onOpenOriginal = {})
            }
        }
        rule.onNodeWithText("Starred messages are unavailable").assertIsDisplayed()
    }

    @Test fun capturesStarredScreenLightAndDarkGlassShots() {
        val api = FakeApi(sampleStars())
        val store = StarredMessagesStore(api, offered = true)
        var dark by mutableStateOf(false)
        rule.setContent {
            PersonaTheme(style = AppStyle.Glass, darkTheme = dark, reduceMotion = true) {
                Box(Modifier.fillMaxSize().testTag("starred-shot")) {
                    StarredMessagesScreen(store = store, onBack = {}, onOpenOriginal = {})
                }
            }
        }
        rule.waitUntil(5000) { rule.onAllNodesWithText("Release Checklist").fetchSemanticsNodes().isNotEmpty() }
        val out = File("build/starred-shots").also { it.mkdirs() }
        for (d in listOf(false, true)) {
            dark = d
            rule.waitForIdle()
            val bmp = rule.onNodeWithTag("starred-shot").captureToImage().asAndroidBitmap()
            File(out, "starred-screen-${if (d) "dark" else "light"}.png").outputStream().use {
                bmp.compress(Bitmap.CompressFormat.PNG, 100, it)
            }
        }
    }
}
