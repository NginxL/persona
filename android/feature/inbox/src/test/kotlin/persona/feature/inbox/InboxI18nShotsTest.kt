package persona.feature.inbox

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
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onAllNodesWithText
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.unit.dp
import androidx.navigation3.runtime.NavKey
import androidx.test.ext.junit.runners.AndroidJUnit4
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.flowOf
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.core.contracts.LarkMomentsComment
import persona.core.contracts.LarkMomentsContent
import persona.core.contracts.LarkMomentsPost
import persona.core.contracts.LarkReaction
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
 * The inbox's pages in each of the app's languages (docs/protocol/i18n.md): the list with demo items, a document's item page,
 * a Lark chat and a Moments post, in Glass light and dark and the other styles light. Written as PNGs to
 * `build/i18n-shots/` (or `-Dpersona.shots=<dir>`) to look at. The demo data is neutral English, as the hub's would
 * be for an English user; only the app's own words change with the language.
 */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
class InboxI18nShotsTest {
    @get:Rule val rule = createComposeRule()

    private fun out(): File = File(System.getProperty("persona.shots") ?: "build/i18n-shots").also { it.mkdirs() }

    @Config(sdk = [35], qualifiers = "zh-w400dp-h900dp-xhdpi")
    @Test fun simplifiedChinese() = shots("zh", listOf("收件箱", "提到你的行", "这件事从这里开始", "评论"))

    @Config(sdk = [35], qualifiers = "en-w400dp-h900dp-xhdpi")
    @Test fun english() = shots("en", listOf("Inbox", "Rows that mention you", "This item starts here", "Comment"))

    @Config(sdk = [35], qualifiers = "ja-w400dp-h900dp-xhdpi")
    @Test fun japanese() = shots("ja", listOf("受信トレイ", "あなたがメンションされた行", "ここから始まります", "コメント"))

    private val records = listOf(
        InboxRecord(InboxItem("lark:f1:120", kind = "mention", title = "Launch team", sender = "Alex Example",
            preview = "@Me can you confirm the rollout date?", tier = "action", updatedAt = "2026-11-04T09:30:00Z"),
            feedId = "f1", startPosition = 120, count = 3, source = "lark"),
        InboxRecord(InboxItem("mail:a1:t:7", kind = "mail", title = "Weekly report", sender = "Sam Example",
            preview = "This week's progress and next week's plan are attached.", tier = "action", updatedAt = "2026-11-04T08:10:00Z"),
            source = "mail", mail = InboxMailRef("a1", uids = listOf(7)), localStatus = LOCAL_DRAFT + "Thanks, will read today"),
        InboxRecord(InboxItem("doc:T1:1", kind = "doc", title = "Q4 schedule", preview = "Alex mentioned you", tier = "action",
            updatedAt = "2026-11-04T07:00:00Z"), source = "lark", doc = doc()),
        InboxRecord(InboxItem("moment:p1", kind = "moment", title = "Moments", sender = "Kim Example",
            preview = "Our team offsite photos are up.", tier = "fyi", updatedAt = "2026-11-03T18:00:00Z"),
            source = "lark", moment = InboxMomentRef("p1")),
        InboxRecord(InboxItem("lark:f2:9", kind = "group", title = "Design review", sender = "Lee Example",
            preview = "Moved to Thursday afternoon.", tier = "fyi", updatedAt = "2026-11-03T15:00:00Z"),
            feedId = "f2", count = 2, source = "lark"),
    )

    private fun doc() = InboxDocRef("https://example.com/wiki/W1", type = "sheet", notice = "mention", reply = "a comment on the cell", rows = listOf(
        InboxDocRow("Load test report", "Tasks", listOf(InboxDocField("Owner", "@Alex Example"), InboxDocField("Due", "Nov 8")),
            listOf(InboxDocField("Status", "In progress"))),
        InboxDocRow("Rollout plan", "Risks", listOf(InboxDocField("Owner", "@Sam Example")), listOf(InboxDocField("Status", ""))),
    ))

    private val messages = listOf(
        LarkMessage("m1", "u1", "Alex Example", position = 118, time = "2026-11-04T09:20:00Z", parts = listOf(LarkPart.Text("Morning all"))),
        LarkMessage("m2", "u2", "Sam Example", position = 119, time = "2026-11-04T09:22:00Z", parts = emptyList(), system = true,
            recalled = true),
        LarkMessage("m3", "u1", "Alex Example", position = 120, time = "2026-11-04T09:30:00Z",
            parts = listOf(LarkPart.Text("@Me can you confirm the rollout date?")), replyCount = 2, edited = true,
            reactions = listOf(LarkReaction("THUMBSUP", 2, mine = false))),
        LarkMessage("m4", "u3", "Lee Example", position = 121, time = "2026-11-04T09:31:00Z",
            parts = listOf(LarkPart.File("file-1", "rollout-plan.pdf", "application/pdf"))),
        LarkMessage("m5", "me", "Me", position = 122, time = "2026-11-04T09:40:00Z", mine = true,
            parts = listOf(LarkPart.Text("Thursday works")), quote = LarkQuote("m2", "Sam Example", "", recalled = true)),
    )

    private class Inbox(snapshot: InboxSnapshot, private val messages: List<LarkMessage>) : InboxRepository {
        override val entries = MutableStateFlow(emptyList<InboxEntry>())
        override val snapshot = MutableStateFlow(snapshot)
        override val connectionState = flowOf(ConnectionState.Online)
        override suspend fun refresh() = Unit
        override fun conversation(feedId: String, startPosition: Long?) =
            flowOf(LarkConversation(title = "Launch team", messages = messages, hasOlder = true, verified = true))
        override suspend fun loadOlder(feedId: String) = Unit
        override suspend fun refreshConversation(feedId: String) = Unit
        override suspend fun setDone(itemId: String, done: Boolean) = Unit
        override suspend fun readDraft(target: ReplyTarget): ReplyDraft? = null
        override suspend fun saveDraft(target: ReplyTarget, draft: ReplyDraft) = Unit
        override suspend fun reply(target: ReplyTarget, request: ReplyRequest) = Unit
        override suspend fun downloadFile(file: LarkPart.File): String = error("not used")
        override suspend fun momentsPost(postId: String, manager: String?) = LarkMomentsPost(postId, userName = "Kim Example",
            content = LarkMomentsContent("Our team offsite photos are up. Thanks everyone for a great two days."), commentCount = 2,
            createTime = 1_762_190_000_000.0)
        override suspend fun momentsComments(postId: String, manager: String?) = listOf(
            LarkMomentsComment("c1", userName = "Alex Example", content = LarkMomentsContent("Great photos"), createTime = 1_762_191_000_000.0),
            LarkMomentsComment("c2", userName = "Sam Example", content = LarkMomentsContent("Agreed"), createTime = 1_762_192_000_000.0,
                replyUserName = "Alex Example"),
        )
    }

    private val navigator = object : Navigator {
        override fun navigate(key: NavKey) = Unit
        override fun back() = Unit
        override fun selectTab(tab: AppTab) = Unit
    }

    private fun shots(language: String, words: List<String>) {
        val inbox = Inbox(InboxSnapshot(records = records), messages)
        var style by mutableStateOf(AppStyle.Glass)
        var dark by mutableStateOf(false)
        var page by mutableStateOf("list")
        var run by mutableIntStateOf(0)
        rule.setContent {
            PersonaTheme(darkTheme = dark, reduceMotion = true, style = style) {
                key(run) {
                    Box(Modifier.testTag("shot").width(400.dp).height(900.dp).background(PersonaTheme.palette.background)) {
                        when (page) {
                            "list" -> InboxScreen(inbox, navigator)
                            "doc" -> InboxItemDetailScreen(inbox, InboxItemDetailRoute("doc:T1:1"), navigator)
                            "lark" -> LarkScreen(inbox, LarkRoute("f1", "lark:f1:120", 120), navigator)
                            else -> LarkMomentsScreen(inbox, LarkMomentRoute("p1", "moment:p1"), navigator)
                        }
                    }
                }
            }
        }
        val pages = listOf("list", "doc", "lark", "moments")
        for ((index, p) in pages.withIndex()) for (s in AppStyle.entries) for (d in listOf(false, true)) {
            if (s != AppStyle.Glass && d) continue
            page = p; style = s; dark = d; run++
            rule.waitForIdle()
            rule.mainClock.advanceTimeBy(1_000)
            rule.waitForIdle()
            if (s == AppStyle.Glass) assertTrue("$p: ${words[index]}", rule.onAllNodesWithText(words[index], substring = true, useUnmergedTree = true).fetchSemanticsNodes().isNotEmpty())
            val bitmap = rule.onNodeWithTag("shot").captureToImage().asAndroidBitmap()
            val name = "inbox-$p-$language-${s.name.lowercase()}-${if (d) "dark" else "light"}.png"
            File(out(), name).outputStream().use { bitmap.compress(Bitmap.CompressFormat.PNG, 100, it) }
        }
    }
}
