package persona.feature.chat

import android.view.View
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.height
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.SideEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalUriHandler
import androidx.compose.ui.platform.LocalView
import androidx.compose.ui.semantics.SemanticsActions
import androidx.compose.ui.test.getUnclippedBoundsInRoot
import androidx.compose.ui.test.hasClickAction
import androidx.compose.ui.test.hasParent
import androidx.compose.ui.test.hasText
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onNodeWithContentDescription
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.onRoot
import androidx.compose.ui.test.performClick
import androidx.compose.ui.test.performSemanticsAction
import androidx.compose.ui.unit.Dp
import androidx.compose.ui.unit.dp
import androidx.core.graphics.Insets
import androidx.core.view.ViewCompat
import androidx.core.view.WindowInsetsCompat
import androidx.test.ext.junit.runners.AndroidJUnit4
import kotlinx.coroutines.currentCoroutineContext
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.flowOf
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.addJsonObject
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.put
import kotlinx.serialization.json.putJsonArray
import kotlinx.serialization.json.putJsonObject
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
import persona.core.contracts.api.RequestManager
import persona.core.designsystem.AppStyle
import persona.core.designsystem.ChromeInsets
import persona.core.designsystem.JPage
import persona.core.designsystem.JSheet
import persona.core.designsystem.LocalChromeInsets
import persona.core.designsystem.PersonaTheme
import persona.core.designsystem.Tokens
import persona.feature.inbox.InboxRepository
import persona.core.inbox.InboxSnapshot
import persona.core.inbox.LarkContentSource
import persona.core.inbox.LarkConversation
import persona.core.inbox.LarkPart
import persona.feature.inbox.LocalInboxRepository
import persona.feature.inbox.ManagerLinks
import persona.core.inbox.ReplyDraft
import persona.core.inbox.ReplyRequest
import persona.core.inbox.ReplyTarget

/** A document another manager's item page opened natively is read as that manager, and so is a document linked inside it. */
@RunWith(AndroidJUnit4::class)
@Config(sdk = [35], qualifiers = "en-w400dp-h900dp-mdpi")
class ChatLinkHostManagerTest {
    @get:Rule val rule = createComposeRule()

    @Before fun choose() = AssistantManagerChoice.choose(null, null, "assistant")
    @After fun reset() = AssistantManagerChoice.reset()

    private val first = "https://example.feishu.cn/docx/FirstLinkHostDoc"
    private val second = "https://example.feishu.cn/docx/SecondLinkHostDoc"

    /** Each document read with the manager it named; the first one links to the second. */
    private inner class Documents : InboxAssistantTransport {
        val reads = mutableListOf<Pair<String, String?>>()
        override val scope = "link-host-manager-test"
        override suspend fun readDocument(url: String): JsonObject {
            val manager = currentCoroutineContext()[RequestManager]?.id
            synchronized(reads) { reads += url to manager }
            return buildJsonObject {
                put("title", if (url == first) "First" else "Second")
                putJsonObject("content") {
                    put("type", "doc")
                    if (url != second) putJsonArray("content") { addJsonObject {
                        put("type", "paragraph")
                        putJsonArray("content") { addJsonObject { put("type", "text"); put("text", second) } }
                    } }
                }
            }
        }
        override suspend fun documentExtra(path: String, url: String) = buildJsonObject { }
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

    private class Paired(transport: InboxAssistantTransport) : InboxRepository {
        override val entries = MutableStateFlow(emptyList<InboxEntry>())
        override val snapshot = MutableStateFlow(InboxSnapshot(contentSource = LarkContentSource(transport)))
        override val connectionState = flowOf(ConnectionState.Online)
        override suspend fun refresh() = Unit
        override fun conversation(feedId: String, startPosition: Long?) = flowOf(LarkConversation())
        override suspend fun loadOlder(feedId: String) = Unit
        override suspend fun refreshConversation(feedId: String) = Unit
        override suspend fun setDone(itemId: String, done: Boolean) = Unit
        override suspend fun readDraft(target: ReplyTarget): ReplyDraft? = null
        override suspend fun saveDraft(target: ReplyTarget, draft: ReplyDraft) = Unit
        override suspend fun reply(target: ReplyTarget, request: ReplyRequest) = Unit
        override suspend fun downloadFile(file: LarkPart.File): String = error("unused")
    }

    @Test fun aDocumentLinkedInsideAnotherManagersDocumentIsReadAsThatManager() {
        val documents = Documents()
        rule.setContent {
            PersonaTheme(reduceMotion = true) {
                CompositionLocalProvider(LocalInboxRepository provides Paired(documents)) {
                    ChatLinkHost { _ ->
                        // An item page of m-3 (ManagerLinks), as Root.kt draws an extension's item page.
                        ManagerLinks("m-3") {
                            val uri = LocalUriHandler.current
                            Text("Open the first document", Modifier.clickable { uri.openUri(first) })
                        }
                    }
                }
            }
        }
        rule.onNodeWithText("Open the first document").performClick()
        rule.waitForIdle()
        rule.waitUntil(5_000) { rule.onAllNodes(hasText(second)).fetchSemanticsNodes().isNotEmpty() }
        // The link inside the first document's text (its own node, under the text's).
        rule.onNode(hasClickAction() and hasParent(hasText(second)), useUnmergedTree = true).performSemanticsAction(SemanticsActions.OnClick)
        rule.waitForIdle()
        rule.waitUntil(5_000) { synchronized(documents.reads) { documents.reads.any { it.first == second } } }
        assertEquals(listOf(first to "m-3", second to "m-3"), synchronized(documents.reads) { documents.reads.toList() })
    }

    private lateinit var composeView: View
    private var contentInset = 0.dp

    private fun openCard(url: String, hostBar: Dp = 0.dp) {
        contentInset = 0.dp
        rule.setContent {
            composeView = LocalView.current
            PersonaTheme(reduceMotion = true) {
                CompositionLocalProvider(
                    LocalInboxRepository provides Paired(Documents()),
                    LocalChromeInsets provides ChromeInsets(top = hostBar),
                ) {
                    ChatLinkHost { bottomInset ->
                        SideEffect { contentInset = bottomInset }
                        ManagerLinks("m-3") {
                            val uri = LocalUriHandler.current
                            Text("Open the first document", Modifier.clickable { uri.openUri(url) })
                        }
                    }
                }
            }
        }
        rule.onNodeWithText("Open the first document").performClick()
        rule.waitForIdle()
    }

    private fun applyInsets(statusTop: Int, cutout: Insets = Insets.NONE, navigation: Insets = Insets.NONE) {
        val insets = WindowInsetsCompat.Builder()
            .setInsets(WindowInsetsCompat.Type.statusBars(), Insets.of(0, statusTop, 0, 0))
            .setInsets(WindowInsetsCompat.Type.displayCutout(), cutout)
            .setInsets(WindowInsetsCompat.Type.navigationBars(), navigation)
            .build()
        rule.runOnIdle { ViewCompat.dispatchApplyWindowInsets(composeView, insets) }
        rule.waitForIdle()
    }

    private fun assertCardTop(expected: Dp) {
        val actual = rule.onNodeWithTag("chat_link_card").getUnclippedBoundsInRoot().top
        assertEquals(expected.value, actual.value, 0.01f)
    }

    @Test fun fullCardStartsBelowTheStatusBarAndCutout() {
        openCard("https://example.feishu.cn/docx/SafeTop")
        applyInsets(statusTop = 24, cutout = Insets.of(0, 40, 0, 0), navigation = Insets.of(0, 0, 0, 48))
        assertCardTop(40.dp)
    }

    @Test fun fullCardDoesNotAddSafeTopToALargerHostBar() {
        openCard("https://example.feishu.cn/docx/HostBar", hostBar = 96.dp)
        applyInsets(statusTop = 24, cutout = Insets.of(0, 40, 0, 0), navigation = Insets.of(0, 0, 0, 48))
        assertCardTop(96.dp)
    }

    @Test fun peekUsesTheSameSafeTopForTheCardAndConversationInset() {
        openCard("https://example.feishu.cn/docx/PeekInsets", hostBar = 16.dp)
        applyInsets(statusTop = 24, cutout = Insets.of(0, 40, 0, 0), navigation = Insets.of(0, 0, 0, 48))
        val root = rule.onRoot().getUnclippedBoundsInRoot()
        val height = root.bottom - root.top
        rule.onNodeWithContentDescription("Shrink and keep chatting").performClick()
        rule.onNodeWithContentDescription("Fill the screen").assertExists()
        assertCardTop(40.dp + (height - 40.dp) * 0.4f)
        assertEquals(((height - 40.dp) * 0.6f).value, contentInset.value, 0.01f)
    }

    @Test
    @Config(qualifiers = "en-w900dp-h400dp-mdpi")
    fun landscapeCardKeepsControlsClearOfSideCutoutsAndNavigation() {
        openCard("https://example.feishu.cn/docx/LandscapeInsets")
        applyInsets(statusTop = 24, cutout = Insets.of(56, 0, 0, 0), navigation = Insets.of(0, 0, 40, 0))
        assertCardTop(24.dp)
        val root = rule.onRoot().getUnclippedBoundsInRoot()
        val width = root.right - root.left
        assertEquals(56f, rule.onNodeWithContentDescription("Back").getUnclippedBoundsInRoot().left.value, 0.01f)
        assertEquals((width - 40.dp - Tokens.Space.xs).value,
            rule.onNodeWithContentDescription("Close page").getUnclippedBoundsInRoot().right.value, 0.01f)
    }

    private fun openEmbeddedCard(style: AppStyle, url: String, sheet: Boolean = false) {
        rule.setContent {
            PersonaTheme(style = style, reduceMotion = true) {
                CompositionLocalProvider(LocalInboxRepository provides Paired(Documents())) {
                    val host: @Composable () -> Unit = {
                        composeView = LocalView.current
                        ChatLinkHost(Modifier.height(400.dp)) { _ ->
                            ManagerLinks("m-3") {
                                val uri = LocalUriHandler.current
                                Text("Open the first document", Modifier.clickable { uri.openUri(url) })
                            }
                        }
                    }
                    if (sheet) JSheet(onDismiss = {}) { host() }
                    else JPage(title = "Host") { _ -> host() }
                }
            }
        }
        rule.onNodeWithText("Open the first document").performClick()
        rule.waitForIdle()
    }

    private fun assertCardStartsAtHostContent() {
        val hostTop = rule.onNodeWithText("Open the first document").getUnclippedBoundsInRoot().top
        val cardTop = rule.onNodeWithTag("chat_link_card").getUnclippedBoundsInRoot().top
        assertEquals(hostTop.value, cardTop.value, 0.01f)
    }

    @Test fun briefPageCardDoesNotRepeatTheSafeTopBelowItsHostBar() {
        openEmbeddedCard(AppStyle.Brief, "https://example.feishu.cn/docx/BriefInsetDoc")
        applyInsets(statusTop = 24)
        assertCardStartsAtHostContent()
    }

    @Test fun boardPageCardDoesNotRepeatTheSafeTopBelowItsHostBar() {
        openEmbeddedCard(AppStyle.Board, "https://example.feishu.cn/docx/BoardInsetDoc")
        applyInsets(statusTop = 24)
        assertCardStartsAtHostContent()
    }

    @Test fun sheetCardDoesNotRepeatTheSafeTopAlreadyAppliedByTheSheet() {
        openEmbeddedCard(AppStyle.Glass, "https://example.feishu.cn/docx/SheetInsetDoc", sheet = true)
        applyInsets(statusTop = 24)
        assertCardStartsAtHostContent()
    }

    @Test fun nativeDocumentDoesNotRepeatTheStatusBarInsetInsideTheCard() {
        openCard("https://example.feishu.cn/docx/NativeInsetDoc")
        rule.waitUntil(5_000) { rule.onAllNodes(hasText(second)).fetchSemanticsNodes().isNotEmpty() }
        applyInsets(statusTop = 0)
        val initialOffset = rule.onNode(hasText(second)).getUnclippedBoundsInRoot().top -
            rule.onNodeWithTag("chat_link_card").getUnclippedBoundsInRoot().top
        applyInsets(statusTop = 24)
        val insetOffset = rule.onNode(hasText(second)).getUnclippedBoundsInRoot().top -
            rule.onNodeWithTag("chat_link_card").getUnclippedBoundsInRoot().top
        assertEquals(initialOffset.value, insetOffset.value, 0.01f)
    }

    /**
     * The card's top follows where the host is in the window (its position, recorded in layout), both when the card opens
     * and when the host moves while it is open: the safe top already above the host is not left over the card (#8334).
     */
    @Test fun cardTopFollowsTheHostsPositionInTheWindow() {
        var above by mutableStateOf(0.dp)
        rule.setContent {
            PersonaTheme(reduceMotion = true) {
                CompositionLocalProvider(LocalInboxRepository provides Paired(Documents())) {
                    composeView = LocalView.current
                    Column {
                        Spacer(Modifier.height(above))
                        ChatLinkHost(Modifier.height(400.dp)) { _ ->
                            ManagerLinks("m-3") {
                                val uri = LocalUriHandler.current
                                Text("Open the first document", Modifier.clickable { uri.openUri("https://example.feishu.cn/docx/MovingHost") })
                            }
                        }
                    }
                }
            }
        }
        applyInsets(statusTop = 24)
        rule.onNodeWithText("Open the first document").performClick()
        rule.waitForIdle()
        // At the window's top: the card starts below the status bar.
        assertCardTop(24.dp)
        // Moved below the status bar while open: the card starts at the host's top, with no safe top left to keep.
        rule.runOnIdle { above = 100.dp }
        rule.waitForIdle()
        assertCardTop(100.dp)
        // Back up to 10 dp: 14 dp of the status bar is still over the host.
        rule.runOnIdle { above = 10.dp }
        rule.waitForIdle()
        assertCardTop(24.dp)
    }

    /** The host's bar growing while a card is open moves the card's anchors too, not only what composition reads. */
    @Test fun aHostBarChangedWhileTheCardIsOpenMovesItsAnchors() {
        var bar by mutableStateOf(16.dp)
        rule.setContent {
            composeView = LocalView.current
            PersonaTheme(reduceMotion = true) {
                CompositionLocalProvider(LocalInboxRepository provides Paired(Documents()), LocalChromeInsets provides ChromeInsets(top = bar)) {
                    ChatLinkHost { _ ->
                        ManagerLinks("m-3") {
                            val uri = LocalUriHandler.current
                            Text("Open the first document", Modifier.clickable { uri.openUri("https://example.feishu.cn/docx/GrowingBar") })
                        }
                    }
                }
            }
        }
        rule.onNodeWithText("Open the first document").performClick()
        rule.waitForIdle()
        assertCardTop(16.dp)
        rule.runOnIdle { bar = 96.dp }
        rule.waitForIdle()
        assertCardTop(96.dp)
        // And a drag target taken afterwards uses the new bar: Peek is 40% of the way down from it.
        val root = rule.onRoot().getUnclippedBoundsInRoot()
        rule.onNodeWithContentDescription("Shrink and keep chatting").performClick()
        rule.waitForIdle()
        // Within a pixel: the card's height is laid out in whole pixels (mdpi here).
        val peek = 96.dp + (root.bottom - root.top - 96.dp) * 0.4f
        assertEquals(peek.value, rule.onNodeWithTag("chat_link_card").getUnclippedBoundsInRoot().top.value, 1f)
    }
}
