package persona.feature.inbox

import androidx.compose.ui.test.hasContentDescription
import androidx.compose.ui.test.isEnabled
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.performClick
import androidx.navigation3.runtime.NavKey
import androidx.navigation3.runtime.entryProvider
import androidx.test.core.app.ApplicationProvider
import androidx.test.ext.junit.runners.AndroidJUnit4
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.cancel
import kotlinx.coroutines.currentCoroutineContext
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.flowOf
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.put
import kotlinx.serialization.json.putJsonArray
import kotlinx.serialization.json.addJsonObject
import org.junit.After
import org.junit.Assert.assertEquals
import org.junit.Before
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import persona.core.contracts.api.AssistantManagerChoice
import persona.core.contracts.api.ConnectionState
import persona.core.contracts.api.InboxAssistantTransport
import persona.core.contracts.api.InboxEntry
import persona.core.contracts.api.ManagerPins
import persona.core.contracts.api.RequestManager
import persona.core.designsystem.PersonaTheme
import persona.core.domain.inbox.InboxItem
import persona.core.navigation.AppTab
import persona.core.navigation.Navigator
import persona.core.inbox.*

/**
 * Another manager's chat opened from the inbox reads where to open (its unread position) as that manager, and its
 * sends go as that manager although the chat's composer is shared and sends in the pairing's scope.
 */
@RunWith(AndroidJUnit4::class)
@Config(sdk = [35], qualifiers = "en-w400dp-h900dp")
class LarkScreenManagerTest {
    @get:Rule val rule = createComposeRule()
    private val shared = CoroutineScope(SupervisorJob() + Dispatchers.Main)

    @Before fun choose() = AssistantManagerChoice.choose(null, null, "assistant")
    @After fun reset() { AssistantManagerChoice.reset(); shared.cancel() }

    private val navigator = object : Navigator {
        override fun navigate(key: NavKey) = Unit
        override fun back() = Unit
        override fun selectTab(tab: AppTab) = Unit
    }

    /** The chat list ([larkConversations]) records the manager each read named. */
    private class Transport : InboxAssistantTransport {
        val listed = mutableListOf<String?>()
        override val scope = "pairing"
        override suspend fun larkConversations(cursor: String?): JsonObject {
            listed += currentCoroutineContext()[RequestManager]?.id
            return buildJsonObject { putJsonArray("conversations") { addJsonObject {
                put("id", "f1"); put("read_position", "4"); put("unread", 2)
            } } }
        }
        override suspend fun inbox() = error("unused")
        override suspend fun setDone(itemId: String, done: Boolean) = error("unused")
        override suspend fun reply(itemId: String?, feedId: String?, text: String, clientId: String) = error("unused")
        override suspend fun messages(feedId: String, around: Long?, before: String?, after: String?) = error("unused")
        override suspend fun assistant() = error("unused")
        override suspend fun createConversation(main: Boolean) = error("unused")
        override suspend fun pins() = error("unused")
        override suspend fun mutes() = error("unused")
        override suspend fun media(ref: String) = error("unused")
    }

    /** One chat item; its composers are shared and unpinned, as the app's are; each send records the pins it went under. */
    private class Chat(transport: Transport, itemId: String, manager: String?, scope: CoroutineScope) : InboxRepository {
        val sends = mutableListOf<ManagerPins?>()
        override val entries = MutableStateFlow(emptyList<InboxEntry>())
        override val snapshot = MutableStateFlow(InboxSnapshot(contentSource = LarkContentSource(transport), records = listOf(
            InboxRecord(InboxItem(itemId, kind = "group", title = "Planning"), source = "lark", feedId = "f1", manager = manager))))
        override val connectionState = flowOf(ConnectionState.Online)
        override val replyComposers = MutableStateFlow<ReplyComposers?>(ReplyComposers(this, scope))
        override suspend fun refresh() = Unit
        override fun conversation(feedId: String, startPosition: Long?) = flowOf(LarkConversation(loading = false, verified = true,
            messages = listOf(LarkMessage("m5", "s", "Sender", 5, parts = listOf(LarkPart.Text("Hello"))))))
        override suspend fun loadOlder(feedId: String) = Unit
        override suspend fun refreshConversation(feedId: String) = Unit
        override suspend fun setDone(itemId: String, done: Boolean) = Unit
        override suspend fun readDraft(target: ReplyTarget): ReplyDraft? = ReplyDraft("On my way", "c1")
        override suspend fun saveDraft(target: ReplyTarget, draft: ReplyDraft) = Unit
        override suspend fun reply(target: ReplyTarget, request: ReplyRequest) { sends += currentCoroutineContext()[ManagerPins] }
        override suspend fun downloadFile(file: LarkPart.File): String = error("unused")
    }

    private val sendPath = listOf("lark", "conversations", "f1", "messages")

    /** Opens the chat route of item [itemId] stamped [manager], then sends its draft. */
    private fun open(itemId: String, manager: String?): Pair<Transport, Chat> {
        val transport = Transport()
        val chat = Chat(transport, itemId, manager, shared)
        rule.setContent {
            PersonaTheme(reduceMotion = true) {
                entryProvider<NavKey> { inboxEntries(navigator, chat) }(LarkRoute("f1", itemId)).Content()
            }
        }
        val send = ApplicationProvider.getApplicationContext<android.content.Context>().getString(persona.core.designsystem.R.string.designsystem_send)
        rule.waitUntil(5_000) { rule.onAllNodes(hasContentDescription(send) and isEnabled()).fetchSemanticsNodes().isNotEmpty() }
        rule.onNode(hasContentDescription(send) and isEnabled()).performClick()
        rule.waitForIdle()
        rule.waitUntil(5_000) { chat.sends.isNotEmpty() }
        return transport to chat
    }

    @Test fun anotherManagersChatOpensAndSendsAsThatManager() {
        val (transport, chat) = open(InboxItemKey.of("lark:f1", "m-3"), "m-3")
        assertEquals(listOf("m-3"), transport.listed)
        assertEquals("m-3", chat.sends.single()?.manager(sendPath))
    }

    @Test fun theDefaultsChatNamesNoManager() {
        val (transport, chat) = open("lark:f1", "assistant")
        // The default's stamp: an empty name, which the hub client drops (no header, no `?manager=`).
        assertEquals(listOf(""), transport.listed)
        assertEquals("", chat.sends.single()?.manager(sendPath))
    }
}
