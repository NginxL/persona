package persona.feature.settings

import android.graphics.Bitmap
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.width
import androidx.compose.runtime.getValue
import androidx.compose.runtime.key
import androidx.compose.runtime.mutableIntStateOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.asAndroidBitmap
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.test.captureToImage
import androidx.compose.ui.test.hasClickAction
import androidx.compose.ui.test.hasText
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.performClick
import androidx.compose.ui.unit.dp
import androidx.navigation3.runtime.NavKey
import androidx.test.ext.junit.runners.AndroidJUnit4
import kotlinx.coroutines.flow.MutableSharedFlow
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.flowOf
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.core.contracts.api.*
import persona.core.designsystem.*
import persona.core.domain.inbox.InboxItem
import persona.core.navigation.InboxItemUiRoute
import persona.core.navigation.Navigator
import persona.feature.inbox.*
import persona.feature.inbox.InboxRepository
import java.io.File
import persona.core.inbox.*

/**
 * An inbox item whose body its connector's extension draws (inbox_item, #1683), on the JVM with Robolectric's native
 * graphics (no emulator), in every style, light and dark: the inbox's header of the item (title, tier and source
 * line, "Done", "AI summary", "AI draft" / "Hand to AI") over the Feishu extension's body of an event (people, time span, the event it
 * continues, the messages, a reply field, "View in conversation"). Each is checked to draw something and written as a PNG to
 * `build/inbox-item-shots/` (or `-Dpersona.shots=<dir>`) to look at (docs/android/android.md "Extension UI slots"). Pressing
 * "View in conversation" brings the item's source link back to the shell.
 */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "zh-w400dp-h1500dp-xhdpi")
class InboxItemShotsTest {
    @get:Rule val rule = createComposeRule()

    private val route = InboxItemUiRoute("lark:F1:1051", "larkext", "event", "周四的评审")

    private class EventExtension : ExtensionApiStub() {
        override suspend fun renderUi(extension: String, ui: String, context: ExtensionUiContext?, state: String?) = ExtensionInboxItemTest.eventView
        override suspend fun uiAction(extension: String, ui: String, action: String, context: ExtensionUiContext?, state: String?,
            values: Map<String, String>?) = ExtensionUiResult(openUrl = "persona://inbox/${context?.item}/source")
    }

    /** One Feishu event in the inbox, nothing else. */
    private class OneItem : InboxRepository {
        override val entries = MutableStateFlow(emptyList<InboxEntry>())
        override val snapshot = MutableStateFlow(InboxSnapshot(records = listOf(InboxRecord(
            InboxItem("lark:F1:1051", kind = "group", title = "周四的评审", tier = "action",
                summary = "丙定了周四下午评审 A，在等你确认会议室；丁说 B 的接口改了。"),
            sourceLabel = "飞书", feedId = "F1", startPosition = 1051, count = 3, source = "lark"))))
        override suspend fun refresh() = Unit
        override fun conversation(feedId: String, startPosition: Long?) = flowOf(LarkConversation())
        override suspend fun loadOlder(feedId: String) = Unit
        override suspend fun refreshConversation(feedId: String) = Unit
        override suspend fun setDone(itemId: String, done: Boolean) = Unit
        override suspend fun readDraft(target: ReplyTarget): ReplyDraft? = null
        override suspend fun saveDraft(target: ReplyTarget, draft: ReplyDraft) = Unit
        override suspend fun reply(target: ReplyTarget, request: ReplyRequest) = Unit
        override suspend fun downloadFile(file: LarkPart.File): String = error("not used")
    }

    private val navigator = object : Navigator {
        override fun navigate(key: NavKey) = Unit
        override fun back() = Unit
        override fun selectTab(tab: persona.core.navigation.AppTab) = Unit
    }

    private fun out(): File = File(System.getProperty("persona.shots") ?: "build/inbox-item-shots").also { it.mkdirs() }

    private fun capture(name: String) {
        rule.waitForIdle()
        val bitmap = rule.onNodeWithTag("shot").captureToImage().asAndroidBitmap()
        val background = bitmap.getPixel(1, 1)
        var other = 0
        for (y in 0 until bitmap.height step 7) for (x in 0 until bitmap.width step 7) if (bitmap.getPixel(x, y) != background) other++
        assertTrue("$name drew nothing", other > 100)
        File(out(), "$name.png").outputStream().use { bitmap.compress(Bitmap.CompressFormat.PNG, 100, it) }
    }

    @Test fun drawsAnEventPageInEveryStyle() {
        var style by mutableStateOf(AppStyle.Glass)
        var dark by mutableStateOf(false)
        var run by mutableIntStateOf(0)
        var fallback: InboxItemFallback? = null
        val sources = mutableListOf<String>()
        val slots = ExtensionSlots(EventExtension(), flowOf(true), MutableSharedFlow())
        val inbox = OneItem()
        rule.setContent {
            PersonaTheme(darkTheme = dark, reduceMotion = true, style = style) {
                key(run) {
                    Box(Modifier.testTag("shot").width(400.dp).height(1500.dp).background(PersonaTheme.palette.background)) {
                        val body = rememberInboxItemBody(slots, route, onSource = { sources += it })
                        fallback = body.fallback
                        InboxItemExtensionScreen(inbox, route, navigator, body.title, body.loading, body.refreshing, body.refresh, body.items)
                    }
                }
            }
        }
        for (s in AppStyle.entries) for (d in listOf(false, true)) {
            style = s; dark = d; run++
            capture("inbox-item-${s.name.lowercase()}-${if (d) "dark" else "light"}")
            assertNull("a page the plugin draws stays", fallback)
        }
        rule.onNode(hasText("在会话里看") and hasClickAction()).performClick()
        rule.waitForIdle()
        assertEquals(listOf("lark:F1:1051"), sources)
    }

    // The item changed (new messages joined the event, it was done): the extension draws it again; otherwise once.
    @Test fun drawsAgainWhenTheItemChanges() {
        var renders = 0
        val extension = object : ExtensionApiStub() {
            override suspend fun renderUi(extension: String, ui: String, context: ExtensionUiContext?, state: String?): ExtensionView {
                renders++
                return ExtensionInboxItemTest.eventView
            }
        }
        val slots = ExtensionSlots(extension, flowOf(true), MutableSharedFlow())
        var revision by mutableStateOf<Any?>(listOf("v1", 3))
        var other by mutableIntStateOf(0)
        rule.setContent {
            PersonaTheme(reduceMotion = true) {
                other.let { rememberInboxItemBody(slots, route, revision, onSource = {}) }
            }
        }
        rule.waitForIdle()
        assertEquals(1, renders)
        other++
        rule.waitForIdle()
        assertEquals("nothing about the item changed", 1, renders)
        revision = listOf("v2", 4)
        rule.waitForIdle()
        assertEquals(2, renders)
    }
}
