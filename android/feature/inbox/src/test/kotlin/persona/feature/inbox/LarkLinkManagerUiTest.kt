package persona.feature.inbox

import androidx.compose.foundation.clickable
import androidx.compose.material3.Text
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalUriHandler
import androidx.compose.ui.test.ExperimentalTestApi
import androidx.compose.ui.test.hasText
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.navigation3.runtime.NavKey
import androidx.test.core.app.ApplicationProvider
import androidx.test.ext.junit.runners.AndroidJUnit4
import coil3.ImageLoader
import coil3.SingletonImageLoader
import coil3.annotation.DelicateCoilApi
import coil3.intercept.Interceptor
import coil3.request.ErrorResult
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.flowOf
import org.junit.After
import org.junit.Assert.assertEquals
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import persona.core.contracts.LarkMomentsComment
import persona.core.contracts.LarkMomentsContent
import persona.core.contracts.LarkMomentsImage
import persona.core.contracts.LarkMomentsPost
import persona.core.contracts.api.AssistantManagerChoice
import persona.core.contracts.api.ConnectionState
import persona.core.contracts.api.InboxEntry
import persona.core.designsystem.PersonaTheme
import persona.core.domain.inbox.InboxItem
import persona.core.navigation.AppTab
import persona.core.navigation.InboxItemUiRoute
import persona.core.navigation.Navigator
import persona.core.inbox.*

/**
 * The views a chat's or an inbox item's links open read through its manager (docs/assistant/managers.md "Clients"): a
 * Moments post in a chat's link card, and a document or post opened on an item's page.
 */
@OptIn(ExperimentalTestApi::class)
@RunWith(AndroidJUnit4::class)
@Config(sdk = [35])
class LarkLinkManagerUiTest {
    @get:Rule val rule = createComposeRule()

    private val postUrl = "https://applink.feishu.cn/client/moments/detail?postId=p1"

    @After fun reset() = AssistantManagerChoice.reset()

    private class Feed : InboxRepository {
        private val notes = mutableListOf<Pair<String, String?>>()
        val moments: List<Pair<String, String?>> get() = synchronized(notes) { notes.toList() }
        var images: List<LarkMomentsImage>? = null
        override val entries = MutableStateFlow(emptyList<InboxEntry>())
        override val snapshot = MutableStateFlow(InboxSnapshot())
        override val connectionState = flowOf(ConnectionState.Online)
        override suspend fun refresh() = Unit
        override fun conversation(feedId: String, startPosition: Long?) = flowOf(LarkConversation())
        override suspend fun loadOlder(feedId: String) = Unit
        override suspend fun refreshConversation(feedId: String) = Unit
        override suspend fun setDone(itemId: String, done: Boolean) = Unit
        override suspend fun readDraft(target: ReplyTarget): ReplyDraft? = null
        override suspend fun saveDraft(target: ReplyTarget, draft: ReplyDraft) = Unit
        override suspend fun reply(target: ReplyTarget, request: ReplyRequest) = Unit
        override suspend fun downloadFile(file: LarkPart.File): String = error("not used")
        override suspend fun momentsPost(postId: String, manager: String?): LarkMomentsPost {
            synchronized(notes) { notes += "post" to manager }
            return LarkMomentsPost(postId, userName = "Kim Example", content = LarkMomentsContent("Friday's talk moved to the third floor."), images = images)
        }
        override suspend fun momentsComments(postId: String, manager: String?): List<LarkMomentsComment> {
            synchronized(notes) { notes += "comments" to manager }
            return emptyList()
        }
    }

    @Test fun aMomentsLinkInAChatIsReadThroughTheChatsManager() {
        val feed = Feed()
        var manager by mutableStateOf<String?>("m-2")
        rule.setContent {
            PersonaTheme(darkTheme = false, reduceMotion = true) {
                LarkMomentsLinkContent(feed, "p1", postUrl, manager = manager) { Text("web page") }
            }
        }
        rule.waitUntilAtLeastOneExists(hasText("Kim Example"), 5_000)
        assertEquals(listOf("post" to "m-2", "comments" to "m-2"), feed.moments)
        // The default's chat (or one whose manager is not known) names none.
        manager = null
        rule.waitForIdle()
        assertEquals(listOf("post" to null, "comments" to null), feed.moments.drop(2))
    }

    @Test fun aDocumentOpenedOnAnItemsPageIsReadThroughTheItemsManager() {
        AssistantManagerChoice.choose("hub", null, "assistant")
        val reads = ManagerReads()
        val feed = Feed()
        rule.setContent {
            PersonaTheme(darkTheme = false, reduceMotion = true) {
                LarkLinkHost(feed, manager = "m-2") {
                    val open = LocalLarkLinkOpener.current
                    Text("Open the document", Modifier.clickable {
                        open?.invoke("https://example.feishu.cn/docx/ItemPageDoc", LarkContentSource(reads))
                    })
                }
            }
        }
        rule.onNodeWithText("Open the document").performClick()
        // The fake titles a document with the manager its read named.
        rule.waitUntilAtLeastOneExists(hasText("m-2"), 5_000)
        rule.waitForIdle()
        assertEquals(setOf("read", "reader/info", "header", "ai-summary"), reads.named.map { it.first }.toSet() - "comments")
        assertEquals(listOf("m-2"), reads.named.map { it.second }.distinct())
    }

    @Test fun aMomentsLinkOnAnItemsPageIsReadThroughTheItemsManager() {
        val feed = Feed()
        rule.setContent {
            PersonaTheme(darkTheme = false, reduceMotion = true) {
                LarkLinkHost(feed, manager = "m-2") {
                    val open = LocalLarkLinkOpener.current
                    Text("Open the post", Modifier.clickable { open?.invoke(postUrl, null) })
                }
            }
        }
        rule.onNodeWithText("Open the post").performClick()
        rule.waitUntilAtLeastOneExists(hasText("Kim Example"), 5_000)
        assertEquals(listOf("post" to "m-2", "comments" to "m-2"), feed.moments)
    }

    @OptIn(DelicateCoilApi::class)
    @Test fun aMomentsPostsPicturesAreReadAsItsManager() {
        AssistantManagerChoice.choose("hub", null, "assistant")
        val asked = java.util.Collections.synchronizedList(mutableListOf<Any>())
        SingletonImageLoader.setUnsafe(ImageLoader.Builder(ApplicationProvider.getApplicationContext()).components {
            add(Interceptor { chain -> asked += chain.request.data; ErrorResult(null, chain.request, IllegalStateException("not drawn in tests")) })
        }.build())
        try {
            val feed = Feed().apply { images = listOf(LarkMomentsImage("/api/v1/lark/media/img", 120.0, 80.0)) }
            var manager by mutableStateOf<String?>("m-2")
            rule.setContent {
                PersonaTheme(darkTheme = false, reduceMotion = true) {
                    LarkMomentsLinkContent(feed, "p1", postUrl, manager = manager) { Text("web page") }
                }
            }
            rule.waitUntil(5_000) { "/api/v1/lark/media/img?manager=m-2" in asked }
            // The default's post names none.
            rule.runOnIdle { manager = "assistant" }
            rule.waitForIdle()
            rule.waitUntil(5_000) { "/api/v1/lark/media/img" in asked }
            assertEquals(setOf("/api/v1/lark/media/img?manager=m-2", "/api/v1/lark/media/img"), asked.toSet())
        } finally {
            SingletonImageLoader.reset()
        }
    }

    @Test fun aLinkOnAnExtensionsItemPageOpensAsTheItemsManager() {
        val docUrl = "https://example.feishu.cn/docx/ItemPageDoc"
        val feed = Feed()
        feed.snapshot.value = InboxSnapshot(records = listOf(
            InboxRecord(InboxItem("lark:other", title = "From another manager"), manager = "m-3"),
            InboxRecord(InboxItem("lark:default", title = "From the default")),
        ))
        val opened = mutableListOf<Pair<String, String?>>()
        val links = object : ManagerUriHandler {
            override fun openUri(uri: String) { opened += uri to null }
            override fun openUri(uri: String, manager: String?) { opened += uri to manager }
        }
        val navigator = object : Navigator {
            override fun navigate(key: NavKey) = Unit
            override fun back() = Unit
            override fun selectTab(tab: AppTab) = Unit
        }
        var itemId by mutableStateOf("lark:other")
        rule.setContent {
            PersonaTheme(darkTheme = false, reduceMotion = true) {
                CompositionLocalProvider(LocalUriHandler provides links) {
                    InboxItemExtensionScreen(feed, InboxItemUiRoute(itemId, "example-connector", "item"), navigator, null,
                        loading = false, refreshing = false, onRefresh = {}) {
                        item("link") {
                            val uri = LocalUriHandler.current
                            Text("Open the document", Modifier.clickable { uri.openUri(docUrl) })
                        }
                    }
                }
            }
        }
        rule.onNodeWithText("Open the document").performClick()
        rule.runOnIdle { assertEquals(listOf(docUrl to "m-3"), opened) }
        // The default's item opens it as before.
        itemId = "lark:default"
        rule.onNodeWithText("Open the document").performClick()
        rule.runOnIdle { assertEquals(listOf(docUrl to "m-3", docUrl to null), opened) }
    }
}
