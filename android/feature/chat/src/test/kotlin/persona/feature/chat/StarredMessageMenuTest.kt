package persona.feature.chat

import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.ui.test.assertIsDisplayed
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.test.ext.junit.runners.AndroidJUnit4
import kotlinx.coroutines.runBlocking
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import persona.core.contracts.Item
import persona.core.contracts.SessionKey
import persona.core.contracts.api.DeliveryState
import persona.core.contracts.api.StarredConversation
import persona.core.contracts.api.StarredIndex
import persona.core.contracts.api.StarredList
import persona.core.contracts.api.StarredMessagesApi
import persona.core.contracts.api.StarredReference
import persona.core.contracts.api.StarredSnapshot
import persona.core.designsystem.AppStyle
import persona.core.designsystem.PersonaTheme

@RunWith(AndroidJUnit4::class)
@Config(sdk = [35], qualifiers = "en-w400dp-h800dp-xhdpi")
class StarredMessageMenuTest {
    @get:Rule val rule = createComposeRule()

    private class FakeStarredApi : StarredMessagesApi {
        val stars = mutableListOf<StarredSnapshot>()
        override suspend fun index() = StarredIndex(
            refs = stars.map {
                StarredReference(it.id!!, it.machineId, it.sessionId, it.itemId, it.conversation, it.sourceFingerprint)
            },
            canWrite = true,
        )
        override suspend fun list() = StarredList(stars.toList(), canWrite = true)
        override suspend fun add(star: StarredSnapshot): StarredSnapshot {
            val saved = star.bounded().copy(id = "sm-${stars.size + 1}", starredAt = "2026-11-22T02:00:00Z")
            stars.add(0, saved)
            return saved
        }
        override suspend fun remove(id: String): StarredSnapshot? {
            val found = stars.firstOrNull { it.id == id } ?: return null
            stars.remove(found)
            return found
        }
        override suspend fun fullItem(session: SessionKey, itemId: String) = Item(id = itemId, role = "assistant", kind = "text", text = "Full")
    }

    @Test fun messageActionsIncludeStarAndUnstarOnlyWhenAvailableAndNotPending() {
        val target = MessageTarget("m1", "Hello", user = false)
        assertEquals(
            listOf(MessageAction.Copy, MessageAction.Select, MessageAction.Quote, MessageAction.Share, MessageAction.Star, MessageAction.Context),
            messageActions(target, writes = true, forward = false, extras = MessageExtras(star = false)),
        )
        assertEquals(
            listOf(MessageAction.Copy, MessageAction.Select, MessageAction.Quote, MessageAction.Share, MessageAction.Unstar, MessageAction.Context),
            messageActions(target, writes = true, forward = false, extras = MessageExtras(star = true)),
        )
        assertEquals(
            listOf(MessageAction.Copy, MessageAction.Select, MessageAction.Share),
            messageActions(target.copy(pending = DeliveryState.Queued), writes = true, forward = false, extras = MessageExtras(star = false)),
        )
    }

    @Test fun storeAndChatMarkTrackStarredMessageState() {
        val api = FakeStarredApi()
        val store = StarredMessagesStore(api, offered = true)
        runBlocking { store.loadIndex() }
        val item = Item(id = "msg-42", role = "assistant", kind = "text", text = "Important release checklist")
        val chat = StarredChatContext(
            key = SessionKey("server", "s1"),
            conversation = StarredConversation.Assistant,
            title = "Release Notes",
            speaker = "Persona",
        )
        val menu = MessageMenuState()
        val target = MessageTarget(item.id, item.text!!, user = false, starItem = item)
        var toggled: Pair<String, Boolean>? = null

        rule.setContent {
            PersonaTheme(style = AppStyle.Glass, darkTheme = false, reduceMotion = true) {
                CompositionLocalProvider(
                    LocalStarredMessagesStore provides store,
                    LocalStarredChatContext provides chat,
                ) {
                    val starredState by store.state.collectAsState()
                    StarredChatMark(item)
                    MessageMenuSheet(
                        menu = menu,
                        writes = true,
                        onQuote = {},
                        onEdit = {},
                        onContext = {},
                        onForward = null,
                        onNotice = {},
                        star = starredState.referenceId(chat.source(item)!!) != null,
                        onStar = { picked, starred -> toggled = picked.id to starred },
                    )
                }
            }
        }

        rule.runOnIdle { menu.open(target) }
        rule.onNodeWithText("Star").assertIsDisplayed().performClick()
        assertEquals("msg-42" to false, toggled)

        runBlocking {
            val draft = chat.draft(item, target)!!
            assertTrue(store.add(draft).isSuccess)
        }
        rule.waitForIdle()
        rule.onNodeWithTag("chat-starred-mark-msg-42").assertIsDisplayed()

        rule.runOnIdle { menu.open(target) }
        rule.onNodeWithText("Unstar").assertIsDisplayed().performClick()
        assertEquals("msg-42" to true, toggled)
    }
}
