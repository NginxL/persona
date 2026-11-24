package persona.feature.inbox

import androidx.compose.ui.test.assertTextEquals
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onNodeWithContentDescription
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.compose.ui.test.performTextInput
import androidx.navigation3.runtime.NavKey
import androidx.test.ext.junit.runners.AndroidJUnit4
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.flowOf
import kotlinx.serialization.json.JsonArray
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.jsonObject
import kotlinx.serialization.json.jsonPrimitive
import kotlinx.serialization.json.put
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import persona.core.contracts.JsonCodec
import persona.core.contracts.api.ConnectionState
import persona.core.contracts.api.InboxEntry
import persona.core.contracts.api.LarkMember
import persona.core.designsystem.PersonaTheme
import persona.core.domain.inbox.InboxItem
import persona.core.navigation.AppTab
import persona.core.navigation.Navigator
import persona.core.inbox.*

/** #576 G122: exercise the real chat composer, from the typed @ to the durable request it sends. */
@RunWith(AndroidJUnit4::class)
@Config(sdk = [35], qualifiers = "en-w400dp-h900dp-xhdpi")
class LarkMentionComposerTest {
    @get:Rule val rule = createComposeRule()

    private val navigator = object : Navigator {
        override fun navigate(key: NavKey) = Unit
        override fun back() = Unit
        override fun selectTab(tab: AppTab) = Unit
    }

    @Test fun aGroupComposerLooksUpTheTypedNameAndSendsThePickedPerson() =
        assertPickerSelectionSends(isTopic = false, typed = "@To", row = "Tom", text = "@Tom ", id = "u-tom", name = "Tom")

    @Test fun aTopicComposerOffersEveryoneAndSendsUserIdAll() =
        assertPickerSelectionSends(isTopic = true, typed = "@", row = "Everyone", text = "@Everyone ", id = "all", name = "Everyone")

    @Test fun aDirectComposerDoesNotLookUpMembersOrOpenThePicker() {
        val chat = Chat(isGroup = false, isTopic = false)
        show(chat)
        rule.onNodeWithTag("inbox-reply-field").performTextInput("@To")
        rule.mainClock.advanceTimeBy(500)
        rule.waitForIdle()
        rule.onNodeWithTag("lark.mentionPicker").assertDoesNotExist()
        assertTrue(chat.memberQueries.isEmpty())
        assertTrue(chat.requests.isEmpty())
    }

    private fun assertPickerSelectionSends(isTopic: Boolean, typed: String, row: String, text: String, id: String, name: String) {
        val chat = Chat(isGroup = true, isTopic = isTopic)
        show(chat)
        rule.onNodeWithTag("inbox-reply-field").performTextInput(typed)
        rule.mainClock.advanceTimeBy(500)
        rule.waitUntil(3_000) { chat.memberQueries.any { it.query == typed.removePrefix("@") } }
        assertTrue(chat.memberQueries.all { it.feedId == chat.feedId && it.limit == 30 })
        rule.onNodeWithTag("lark.mentionPicker").assertExists()
        rule.onNodeWithText(row).performClick()
        rule.onNodeWithTag("inbox-reply-field").assertTextEquals(text)
        rule.onNodeWithContentDescription("Send").performClick()
        rule.waitUntil(3_000) { chat.requests.isNotEmpty() }

        val sent = chat.requests.single()
        assertEquals(text, sent["text"]!!.jsonPrimitive.content)
        assertEquals(JsonArray(listOf(buildJsonObject {
            put("user_id", id)
            put("name", name)
        })), sent["mentions"])
    }

    private fun show(chat: Chat) {
        rule.setContent {
            PersonaTheme(darkTheme = false, reduceMotion = true) {
                LarkScreen(chat, LarkRoute(chat.feedId, chat.itemId), navigator)
            }
        }
        rule.waitForIdle()
    }

    private data class MemberQuery(val feedId: String, val query: String, val limit: Int)

    private class Chat(private val isGroup: Boolean, private val isTopic: Boolean) : InboxRepository {
        val feedId = if (isTopic) "feed-topic" else if (isGroup) "feed-group" else "feed-direct"
        val itemId = "lark:item-${if (isTopic) "topic" else if (isGroup) "group" else "direct"}"
        val memberQueries = mutableListOf<MemberQuery>()
        val requests = mutableListOf<kotlinx.serialization.json.JsonObject>()
        override val entries = MutableStateFlow(emptyList<InboxEntry>())
        override val snapshot = MutableStateFlow(InboxSnapshot(records = listOf(InboxRecord(
            InboxItem(itemId, kind = if (isGroup) "group" else "chat", title = "Synthetic chat"),
            source = "lark", feedId = feedId, isGroup = isGroup, isTopic = isTopic))))
        override val connectionState = flowOf(ConnectionState.Online)
        override suspend fun refresh() = Unit
        override fun conversation(feedId: String, startPosition: Long?) = flowOf(LarkConversation(
            title = "Synthetic chat", isGroup = isGroup, isTopic = isTopic, verified = true))
        override suspend fun loadOlder(feedId: String) = Unit
        override suspend fun refreshConversation(feedId: String) = Unit
        override suspend fun setDone(itemId: String, done: Boolean) = Unit
        override suspend fun readDraft(target: ReplyTarget): ReplyDraft? = null
        override suspend fun saveDraft(target: ReplyTarget, draft: ReplyDraft) = Unit
        override suspend fun larkMembers(feedId: String, query: String, limit: Int): List<LarkMember> {
            memberQueries += MemberQuery(feedId, query, limit)
            return listOf(LarkMember("u-tom", "Tom"), LarkMember("u-zhou", "Zhou"))
                .filter { query.isEmpty() || query in it.name }
        }
        override suspend fun reply(target: ReplyTarget, request: ReplyRequest) {
            requests += JsonCodec.encodeToJsonElement(ReplyRequest.serializer(), request).jsonObject
        }
        override suspend fun downloadFile(file: LarkPart.File): String = error("not used")
    }
}
