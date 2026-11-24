package persona.feature.inbox

import android.graphics.Bitmap
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.width
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.getValue
import androidx.compose.runtime.key
import androidx.compose.runtime.mutableIntStateOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.asAndroidBitmap
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.test.captureToImage
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.compose.ui.unit.dp
import androidx.navigation3.runtime.NavKey
import androidx.test.ext.junit.runners.AndroidJUnit4
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.flowOf
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.core.contracts.api.ConnectionState
import persona.core.contracts.api.InboxEntry
import persona.core.designsystem.AppStyle
import persona.core.designsystem.PersonaTheme
import persona.core.domain.inbox.InboxItem
import persona.core.navigation.AppTab
import persona.core.navigation.Navigator
import java.io.File
import persona.core.inbox.*

/**
 * A Feishu document's notice opened from the inbox (#2323), on the JVM with Robolectric's native graphics, in every
 * style, light and dark: its type and what happened, "Open sheet", where a reply goes, and the rows about the user with
 * their fields and status. Written as PNGs to `build/inbox-doc-shots/` (or `-Dpersona.shots=<dir>`), docs/android/android.md "Inbox".
 */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "zh-w400dp-h900dp-xhdpi")
class InboxDocShotsTest {
    @get:Rule val rule = createComposeRule()

    private val url = "https://example.larkoffice.com/wiki/W1"
    private val record = InboxRecord(
        InboxItem("doc:T1:1730700000000", kind = "doc", title = "Q4 项目排期", preview = "示例同事提及了你", tier = "action",
            updatedAt = "2026-11-04T09:30:00Z"),
        sourceLabel = "飞书", source = "lark",
        doc = InboxDocRef(url, type = "sheet", notice = "mention", reply = "评论 @你的那个单元格", rows = listOf(
            InboxDocRow("压测报告", "任务", listOf(InboxDocField("跟进人", "@示例同事"), InboxDocField("截止", "11 月 8 日")),
                listOf(InboxDocField("状态", "进行中"))),
            InboxDocRow("灰度方案", "风险", listOf(InboxDocField("跟进人", "@示例同事")), listOf(InboxDocField("状态", ""))),
        )),
    )

    private class OneInbox(snapshot: InboxSnapshot) : InboxRepository {
        override val entries = MutableStateFlow(emptyList<InboxEntry>())
        override val snapshot = MutableStateFlow(snapshot)
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
    }

    private val navigator = object : Navigator {
        override fun navigate(key: NavKey) = Unit
        override fun back() = Unit
        override fun selectTab(tab: AppTab) = Unit
    }

    private fun out(): File = File(System.getProperty("persona.shots") ?: "build/inbox-doc-shots").also { it.mkdirs() }

    private fun capture(name: String) {
        rule.waitForIdle()
        val bitmap = rule.onNodeWithTag("shot").captureToImage().asAndroidBitmap()
        val background = bitmap.getPixel(1, 1)
        var other = 0
        for (y in 0 until bitmap.height step 7) for (x in 0 until bitmap.width step 7) if (bitmap.getPixel(x, y) != background) other++
        assertTrue("$name drew nothing", other > 100)
        File(out(), "$name.png").outputStream().use { bitmap.compress(Bitmap.CompressFormat.PNG, 100, it) }
    }

    @Test fun drawsTheDocumentsPageInEveryStyleAndOpensTheDocument() {
        var style by mutableStateOf(AppStyle.Glass)
        var dark by mutableStateOf(false)
        var run by mutableIntStateOf(0)
        val opened = mutableListOf<String>()
        val inbox = OneInbox(InboxSnapshot(records = listOf(record)))
        rule.setContent {
            PersonaTheme(darkTheme = dark, reduceMotion = true, style = style) {
                key(run) {
                    CompositionLocalProvider(LocalLarkLinkOpener provides { link, _ -> opened += link }) {
                        Box(Modifier.testTag("shot").width(400.dp).height(900.dp).background(PersonaTheme.palette.background)) {
                            InboxItemDetailScreen(inbox, InboxItemDetailRoute(record.item.id), navigator)
                        }
                    }
                }
            }
        }
        for (s in AppStyle.entries) for (d in listOf(false, true)) {
            style = s; dark = d; run++
            rule.waitForIdle()
            rule.onNodeWithText("提到你的行").assertExists()
            rule.onNodeWithText("状态：未设置").assertExists()
            capture("inbox-doc-${s.name.lowercase()}-${if (d) "dark" else "light"}")
        }
        rule.onNodeWithText("打开表格").performClick()
        assertEquals(listOf(url), opened)
    }
}
