package persona.feature.chat

import android.graphics.Bitmap
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.produceState
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.asAndroidBitmap
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.semantics.SemanticsProperties
import androidx.compose.ui.semantics.getOrNull
import androidx.compose.ui.test.SemanticsMatcher
import androidx.compose.ui.test.captureToImage
import androidx.compose.ui.test.hasContentDescription
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.performTouchInput
import androidx.compose.ui.test.swipeDown
import androidx.compose.ui.unit.dp
import androidx.test.ext.junit.runners.AndroidJUnit4
import java.io.File
import java.util.concurrent.atomic.AtomicInteger
import java.util.concurrent.atomic.AtomicReference
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.cancel
import kotlinx.coroutines.flow.MutableSharedFlow
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.launch
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.core.chatlist.ChatList
import persona.core.chatlist.ChatListEffects
import persona.core.chatlist.ChatListState
import persona.core.chatlist.rememberChatListState
import persona.core.contracts.*
import persona.core.contracts.CoreText.Key
import persona.core.contracts.api.*
import persona.core.data.CachedChat
import persona.core.data.ChatItemsCache
import persona.core.data.HubChatRepository
import persona.core.data.KotlinContinuityAdapter
import persona.core.designsystem.AppStyle
import persona.core.designsystem.PersonaTheme
import persona.core.hub.HubError
import persona.core.hub.HubFailure
import kotlinx.serialization.json.JsonObject

/** #11451 through the real repository, ChatScreen and the same older-page effect the screen binds. */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "en-w400dp-h900dp")
class ChatHistoryNotFoundScreenTest {
    @get:Rule val rule = createComposeRule()

    private val key = SessionKey("computer-a", "missing-history")
    private val hubSentence = "This conversation’s history is no longer on this computer."
    private val recent = (1..30).map { Item("$it#0", it, role = "assistant", kind = "text", text = "Message $it") }
    private val session = Session(key.machineId, key.sessionId, title = "Missing history", status = "done", agent = "claude")

    private class Connection : HubConnection {
        override val state = MutableStateFlow(ConnectionState.Online)
        override val events = MutableSharedFlow<ServerEvent>()
        override fun reconnect() = Unit
        override fun send(message: JsonObject) = true
    }

    private class Cache : ChatItemsCache {
        override suspend fun load(session: SessionKey) = CachedChat()
        override suspend fun save(session: SessionKey, snapshot: CachedChat) = Unit
    }

    private inner class Transport(
        private val latestFailure: HubFailure? = null,
        private val firstOlderFailure: HubFailure? = null,
    ) : ChatTransport {
        val latestReads = AtomicInteger()
        val olderReads = AtomicInteger()

        override suspend fun items(session: SessionKey, query: ItemsQuery): ItemsResponse {
            if (query.beforeSeq != null) {
                val read = olderReads.incrementAndGet()
                if (read == 1) firstOlderFailure?.let { throw it }
                // A legacy retry can settle; a correct 404/not_found never makes this second request.
                return ItemsResponse(emptyList(), hasMore = false, history = "h")
            }
            latestReads.incrementAndGet()
            latestFailure?.let { throw it }
            return ItemsResponse(recent, hasMore = true, history = "h", toSeq = recent.size)
        }

        override suspend fun machines(): List<Machine> = emptyList()
        override suspend fun sessions(): List<Session> = listOf(session)
        override suspend fun interactions(): List<Interaction> = emptyList()
        override suspend fun resolve(id: String, resolution: Resolution) = Unit
        override suspend fun image(session: SessionKey, itemId: String) = byteArrayOf()
        override suspend fun fullItem(session: SessionKey, itemId: String) = Item(itemId)
    }

    private fun repository(transport: Transport, scope: CoroutineScope): HubChatRepository {
        val sessions = object : SessionsRepository {
            override val machines = MutableStateFlow(listOf(Machine(key.machineId, "Computer A", online = true)))
            override val sessions = MutableStateFlow(listOf(session))
            override suspend fun refresh() = Unit
        }
        return HubChatRepository(transport, Connection(), Cache(), sessions, scope,
            continuityFactory = { chat, _ -> KotlinContinuityAdapter(chat) })
    }

    private fun missingHistory() = HubFailure(HubError.Server, hubSentence,
        status = 404, code = "not_found", detail = "history_missing")

    private fun texts(): List<String> = rule.onAllNodes(SemanticsMatcher("any node") { true }, useUnmergedTree = true)
        .fetchSemanticsNodes().flatMap { node ->
            node.config.getOrNull(SemanticsProperties.Text).orEmpty().map { it.text } +
                node.config.getOrNull(SemanticsProperties.ContentDescription).orEmpty()
        }

    private fun loadingNodes() = rule.onAllNodes(hasContentDescription("Loading"), useUnmergedTree = true).fetchSemanticsNodes()

    private fun latestPage(style: AppStyle, nameSuffix: String = "") {
        val scope = CoroutineScope(SupervisorJob() + Dispatchers.Default)
        try {
            val transport = Transport(latestFailure = missingHistory())
            val chats = repository(transport, scope)
            val sessions = object : SessionsRepository {
                override val machines = MutableStateFlow(listOf(Machine(key.machineId, "Computer A", online = true)))
                override val sessions = MutableStateFlow(listOf(session))
                override suspend fun refresh() = Unit
            }
            val sender = object : MessageSender {
                override val outbox = MutableStateFlow(emptyList<OutboxMessage>())
                override suspend fun send(session: SessionKey, text: String, clientMessageId: String, clearDraft: String?) = Unit
            }
            val interactions = object : InteractionCenter {
                override val pending = MutableStateFlow(emptyList<Interaction>())
                override suspend fun refresh() = Unit
                override suspend fun resolve(id: String, resolution: Resolution) = Unit
            }
            var dark by mutableStateOf(false)
            rule.setContent {
                PersonaTheme(darkTheme = dark, reduceMotion = true, style = style) {
                    Box(Modifier.fillMaxSize().background(PersonaTheme.palette.background).testTag("history-not-found-shot")) {
                        ChatScreen(key, chats, sender, interactions, sessions, onBack = {})
                    }
                }
            }
            rule.waitUntil(15_000) {
                transport.latestReads.get() == 1 && texts().any {
                    it == hubSentence || it == CoreText.of(Key.ChatSyncFailed).shown
                }
            }
            assertTrue("$style must show the hub's exact sentence; shown=${texts()}", hubSentence in texts())
            assertTrue("$style must show neither ChatSkeleton nor a loading spinner; shown=${texts()}", loadingNodes().isEmpty())
            assertEquals("the failed latest page is not retried", 1, transport.latestReads.get())
            val out = File(System.getProperty("persona.shots") ?: "build/history-not-found-shots").also { it.mkdirs() }
            for (night in listOf(false, true)) {
                rule.runOnIdle { dark = night }
                rule.waitForIdle()
                val name = "history-not-found-${style.name.lowercase()}-${if (night) "dark" else "light"}$nameSuffix.png"
                File(out, name).outputStream().use {
                    assertTrue(rule.onNodeWithTag("history-not-found-shot").captureToImage().asAndroidBitmap()
                        .compress(Bitmap.CompressFormat.PNG, 100, it))
                }
            }
        } finally {
            scope.cancel()
        }
    }

    @Test fun glassLatestNotFoundShowsHubErrorWithoutSkeleton() = latestPage(AppStyle.Glass)

    @Test fun briefLatestNotFoundShowsHubErrorWithoutEndlessSpinner() = latestPage(AppStyle.Brief)

    @Test fun boardLatestNotFoundShowsHubErrorWithoutEndlessSpinner() = latestPage(AppStyle.Board)

    @Test @Config(fontScale = 2f)
    fun glassLatestNotFoundLargeTextShowsHubErrorWithoutSkeleton() = latestPage(AppStyle.Glass, "-text200")

    @Test @Config(fontScale = 2f)
    fun briefLatestNotFoundLargeTextShowsHubErrorWithoutEndlessSpinner() = latestPage(AppStyle.Brief, "-text200")

    @Test @Config(fontScale = 2f)
    fun boardLatestNotFoundLargeTextShowsHubErrorWithoutEndlessSpinner() = latestPage(AppStyle.Board, "-text200")

    private fun olderPageEffect(failure: HubFailure, expectedReads: Int, explicitRetry: Boolean = false) {
        val scope = CoroutineScope(SupervisorJob() + Dispatchers.Default)
        try {
            val transport = Transport(firstOlderFailure = failure)
            val chats = repository(transport, scope)
            val snapshot = AtomicReference(ChatSnapshot())
            lateinit var list: ChatListState
            lateinit var scrollScope: CoroutineScope
            rule.setContent {
                PersonaTheme(darkTheme = false, reduceMotion = true, style = AppStyle.Glass) {
                    scrollScope = rememberCoroutineScope()
                    val shown by produceState(ChatSnapshot(), chats) {
                        chats.observe(key).collect {
                            snapshot.set(it)
                            value = it
                        }
                    }
                    list = rememberChatListState()
                    val rows = shown.items.asReversed()
                    ChatListEffects(list, rows.map { it.id }, ready = !shown.loading && rows.isNotEmpty(),
                        hasOlder = shown.hasOlder, loadingOlder = shown.loadingOlder,
                        onFreeze = {}, onFollow = {}, loadOlder = { chats.loadOlder(key) })
                    ChatList(list, Modifier.fillMaxSize().testTag("history")) {
                        items(rows.size, key = { rows[it].id }) { index ->
                            Box(Modifier.fillMaxWidth().height(36.dp).testTag(rows[index].id))
                        }
                    }
                }
            }
            rule.waitUntil(15_000) {
                snapshot.get().verified && snapshot.get().hasOlder && list.list.layoutInfo.totalItemsCount == recent.size
            }
            rule.runOnIdle { scrollScope.launch { list.list.scrollToItem(recent.lastIndex) } }
            rule.waitUntil(5_000) { list.list.layoutInfo.visibleItemsInfo.any { it.index == recent.lastIndex } }
            rule.onNode(SemanticsMatcher("history list") { it.config.getOrNull(SemanticsProperties.TestTag) == "history" })
                .performTouchInput { swipeDown() }

            if (explicitRetry) {
                rule.waitUntil(15_000) { transport.olderReads.get() == 1 && !snapshot.get().loadingOlder }
                rule.runOnIdle { scrollScope.launch { chats.loadOlder(key) } }
            }

            // A missing page ends on its first read; an ordinary failure still allows an explicit second read.
            rule.waitUntil(15_000) {
                transport.olderReads.get() >= 1 && !snapshot.get().hasOlder && !snapshot.get().loadingOlder
            }
            assertEquals("older transport reads for status=${failure.status} code=${failure.code}",
                expectedReads, transport.olderReads.get())
        } finally {
            scope.cancel()
        }
    }

    @Test fun olderNotFoundDoesNotRestartThePagingEffect() {
        olderPageEffect(missingHistory(), expectedReads = 1)
    }

    @Test fun plain404OlderFailureAllowsAnExplicitRetry() {
        olderPageEffect(HubFailure(HubError.Server, "HTTP 404", status = 404), expectedReads = 2, explicitRetry = true)
    }
}
