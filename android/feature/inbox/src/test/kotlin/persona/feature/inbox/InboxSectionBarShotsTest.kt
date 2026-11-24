package persona.feature.inbox

import android.graphics.Bitmap
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.width
import androidx.compose.material3.LocalRippleConfiguration
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableFloatStateOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.runtime.snapshots.Snapshot
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.asAndroidBitmap
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.semantics.SemanticsProperties
import androidx.compose.ui.test.SemanticsMatcher
import androidx.compose.ui.test.assertIsSelected
import androidx.compose.ui.test.captureToImage
import androidx.compose.ui.test.hasSetTextAction
import androidx.compose.ui.test.hasText
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onAllNodesWithText
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.performClick
import androidx.compose.ui.test.performScrollTo
import androidx.compose.ui.test.performTextInput
import androidx.compose.ui.unit.Density
import androidx.compose.ui.unit.dp
import androidx.navigation3.runtime.NavKey
import androidx.test.ext.junit.runners.AndroidJUnit4
import java.io.File
import java.time.Instant
import java.time.ZoneId
import java.time.temporal.ChronoUnit
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.flowOf
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.core.contracts.api.ConnectionState
import persona.core.contracts.api.InboxBulkAction
import persona.core.contracts.api.InboxDoneTarget
import persona.core.contracts.api.InboxEntry
import persona.core.designsystem.AppStyle
import persona.core.designsystem.PersonaTheme
import persona.core.domain.inbox.InboxInteraction
import persona.core.domain.inbox.InboxItem
import persona.core.navigation.AppTab
import persona.core.navigation.Navigator
import persona.core.inbox.*

/**
 * The whole Inbox page under its section bar (#9053) with dozens of rows per section, as a person sees it: each style on
 * a phone, light and dark, the choices, an empty choice, a search, 200% text, a wide window and English. The bar's cut
 * edge fades where choices run past it. Mock data; PNGs in build/inbox-section-bar-page-shots/ (or `-Dpersona.shots=<dir>`).
 */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "zh-w400dp-h860dp-xhdpi")
class InboxSectionBarShotsTest {
    @get:Rule val rule = createComposeRule()

    private val now = Instant.now().truncatedTo(ChronoUnit.SECONDS)
    private fun ago(minutes: Long) = now.minus(minutes, ChronoUnit.MINUTES).toString()

    private val senders = listOf("示例同事甲", "示例同事乙", "示例经理", "示例财务", "示例设计师", "示例律师", "示例客户", "示例同事丙", "示例同事丁")
    private val topics = listOf(
        "周五发版前要确认的三件事", "合同第二版的修改意见", "下周评审的时间", "预算表需要你签字", "新人入职安排",
        "季度目标的初稿", "接口变更的影响范围", "客户反馈的两个问题", "演示环境的账号", "采购申请待批",
        "设计稿第三页的改动", "测试报告的结论", "会议室改到周四", "发布说明的英文版", "回滚方案还差一步",
        "供应商报价对比", "数据看板的口径", "年度体检预约", "周会纪要", "技术分享的题目",
        "服务器到期续费", "登录页的崩溃", "新版图标", "差旅报销", "合作方的需求清单",
        "用户调研的问卷", "迁移计划的时间表", "值班表", "代码评审的意见", "上线检查清单",
    )
    private val previews = listOf("测试报告、发布说明和回滚方案还差一个", "示例邮件的正文摘要。", "麻烦今天下班前回复一下", "附件里是最新的版本",
        "CI 通过，等你审阅", "本周进展和下周计划", "有两处需要你确认", "已经按评审意见改好了")

    /** Row [i] of a section: Lark, two mail accounts and a GitHub connector in turn. */
    private fun record(prefix: String, i: Int, status: String, tier: String, at: String, doneAt: String? = null): InboxRecord {
        val title = topics[(i * 7 + prefix.length) % topics.size] + if (i >= topics.size) "（跟进）" else ""
        val sender = senders[(i + prefix.length) % senders.size]
        val preview = previews[(i * 3 + prefix.length) % previews.size]
        val summary = if (i % 3 == 0) "需要你决定是否同意，对方在等回复" else null
        return when (i % 4) {
            0 -> InboxRecord(InboxItem("lark:$prefix$i", account = "feishu", kind = "dm", title = title, sender = sender, preview = preview,
                summary = summary, tier = tier, status = status, updatedAt = at, doneAt = doneAt, version = "v-$prefix$i"), sourceLabel = "飞书", source = "lark")
            1 -> InboxRecord(InboxItem("mail:$prefix$i", account = "acc1", kind = "mail", title = title, sender = sender, preview = preview,
                summary = summary, tier = tier, status = status, updatedAt = at, doneAt = doneAt, version = "v-$prefix$i"), sourceLabel = "me@example.com", source = "mail")
            2 -> InboxRecord(InboxItem("mail:$prefix$i", account = "acc2", kind = "mail", title = title, sender = sender, preview = preview,
                summary = summary, tier = tier, status = status, updatedAt = at, doneAt = doneAt, version = "v-$prefix$i"), sourceLabel = "work@example.com", source = "mail")
            else -> InboxRecord(InboxItem("gh:$prefix$i", account = "gh", kind = "pull", title = "#${100 + i} $title", sender = "example-bot", preview = preview,
                summary = summary, tier = tier, status = status, updatedAt = at, doneAt = doneAt, version = "v-$prefix$i"), sourceLabel = "GitHub", source = "github",
                connector = InboxConnectorRef("t$prefix$i", label = "Pull request"))
        }
    }

    // The mock hub's many-items Inbox (MOCK_INBOX_MANY=action:30,working:6,fyi:40,noise:20,done:25).
    private val needsYou = List(30) { record("a", it, "action", "action", ago(10L + it * 170L)) }
    private val working = List(6) { record("w", it, "working", "action", ago(15L + it * 60L)) }
    private val fyi = List(40) { record("f", it, "fyi", "fyi", ago(30L + it * 200L)) }
    private val noise = List(20) { record("n", it, "noise", "noise", ago(45L + it * 240L)) }
    private val done = List(25) { record("d", it, "fyi", if (it % 2 == 0) "fyi" else "action", ago(20L + it * 165L), doneAt = ago(5L + it * 165L)) }
    private val cards = listOf(
        InboxInteraction("q1", "mac", "s1", createdAt = ago(5), sessionTitle = "照片同步脚本", summary = "转换后的照片保存为哪种格式？"),
        InboxInteraction("q2", "mac", "s2", createdAt = ago(25), sessionTitle = "周报整理", summary = "可以把草稿发给团队吗？"),
        InboxInteraction("q3", "mac", "s3", createdAt = ago(90), sessionTitle = "依赖升级", summary = "要运行全部测试吗？"),
    )
    /** The hub's day counts (#3016): the list's own done rows on their days, then 8 a day for the 3 weeks before. */
    private val doneDays: List<InboxDoneDay> = run {
        val zone = ZoneId.systemDefault()
        (done.map { Instant.parse(it.item.doneAt!!).atZone(zone).toLocalDate() } +
            (3L..20L).flatMap { d -> List(8) { now.minus(d, ChronoUnit.DAYS).atZone(zone).toLocalDate() } })
            .groupingBy { it }.eachCount().map { (day, n) -> InboxDoneDay(day, n) }.sortedByDescending { it.day }
    }

    private class Inbox(records: List<InboxRecord>, interactions: List<InboxInteraction>, val days: List<InboxDoneDay>) : InboxRepository {
        val prefs = MutableStateFlow(InboxPreferences())
        override val preferences = prefs
        override suspend fun savePreferences(value: InboxPreferences) { prefs.value = value }
        override val entries = MutableStateFlow(emptyList<InboxEntry>())
        override val snapshot = MutableStateFlow(InboxSnapshot(records = records, interactions = interactions))
        override val connectionState = flowOf(ConnectionState.Online)
        override suspend fun refresh() = Unit
        override fun conversation(feedId: String, startPosition: Long?) = flowOf(LarkConversation())
        override suspend fun loadOlder(feedId: String) = Unit
        override suspend fun refreshConversation(feedId: String) = Unit
        override suspend fun setDone(itemId: String, done: Boolean) = Unit
        override suspend fun doneHistory(manager: String?, cursor: InboxDoneCursor?) = InboxDonePage()
        override suspend fun doneDays(manager: String?, zone: ZoneId) = days
        override suspend fun bulk(items: List<InboxDoneTarget>, action: InboxBulkAction) = InboxBulkResult(changed = items.map { it.id })
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

    private var style by mutableStateOf(AppStyle.Glass)
    private var dark by mutableStateOf(false)
    private var fontScale by mutableFloatStateOf(1f)

    private fun many() = Inbox(needsYou + working + fyi + noise + done, cards, doneDays)

    private fun show(inbox: Inbox, width: Float, height: Float) {
        rule.setContent {
            PersonaTheme(style = style, darkTheme = dark, reduceMotion = true) {
                // No ripples: Robolectric has no render thread, so a tapped choice would keep a frozen one in the shots.
                CompositionLocalProvider(LocalRippleConfiguration provides null,
                    LocalDensity provides Density(LocalDensity.current.density, fontScale)) {
                    Box(Modifier.width(width.dp).height(height.dp).testTag("shot").background(PersonaTheme.palette.background)) {
                        InboxScreen(inbox, navigator)
                    }
                }
            }
        }
        rule.waitForIdle()
    }

    private fun out(): File = File(System.getProperty("persona.shots") ?: "build/inbox-section-bar-page-shots").also { it.mkdirs() }

    /** [view] in [s], light then dark: `<view>-light.png`, `<view>-dark.png`. */
    private fun capture(view: String, s: AppStyle = AppStyle.Glass, themes: List<Boolean> = listOf(false, true)) {
        for (d in themes) {
            style = s; dark = d
            Snapshot.sendApplyNotifications(); rule.waitForIdle()
            val bitmap = rule.onNodeWithTag("shot").captureToImage().asAndroidBitmap()
            File(out(), "$view-${if (d) "dark" else "light"}.png").outputStream().use { bitmap.compress(Bitmap.CompressFormat.PNG, 100, it) }
        }
    }

    /** A choice of the bar (a heading with the same words has no selected state). */
    private fun choice(label: String) = rule.onNode(hasText(label) and SemanticsMatcher.keyIsDefined(SemanticsProperties.Selected))

    /** Taps a choice, scrolling the bar to it first as a person would: on a phone the last choices sit past its edge. */
    private fun pick(label: String) {
        choice(label).performScrollTo().performClick()
        rule.waitForIdle()
        choice(label).assertIsSelected()
    }

    @Test fun phone() {
        show(many(), 400f, 860f)
        choice("需要你").assertIsSelected()
        for (s in AppStyle.entries) capture("phone-${s.key}", s)
        // Back to Glass before choosing, so each choice is made in the style it is shot in.
        style = AppStyle.Glass; dark = false
        Snapshot.sendApplyNotifications(); rule.waitForIdle()
        pick("知悉即可")
        capture("phone-glass-fyi")
        pick("已处理")
        capture("phone-glass-done")
        pick("全部")
        capture("phone-glass-all")
        // Brief scrolled to All: the leading edge fades where In progress is cut.
        capture("phone-brief-all", AppStyle.Brief)
    }

    @Test fun search() {
        show(many(), 400f, 860f)
        rule.onNode(hasSetTextAction()).performTextInput("合同")
        rule.waitForIdle()
        choice("全部").assertIsSelected()
        capture("phone-glass-search")
    }

    @Test fun emptyChoice() {
        // Nothing needs the user, while FYI and Done hold rows: Needs you says so in its own words.
        show(Inbox(fyi + noise + done, emptyList(), doneDays), 400f, 860f)
        choice("需要你").assertIsSelected()
        assertTrue(rule.onAllNodesWithText("没有需要你处理的").fetchSemanticsNodes().isNotEmpty())
        capture("phone-glass-empty")
    }

    @Test fun largeText() {
        fontScale = 2f
        show(many(), 400f, 860f)
        capture("phone-glass-large")
        capture("phone-brief-large", AppStyle.Brief, listOf(false))
        capture("phone-board-large", AppStyle.Board, listOf(false))
    }

    @Config(qualifiers = "zh-w1280dp-h800dp-xhdpi")
    @Test fun wide() {
        // The list alone at the window's width, as on a tablet with no item open (the page keeps itself to its reading width).
        show(many(), 1280f, 800f)
        for (s in AppStyle.entries) capture("wide-${s.key}", s)
    }

    @Config(qualifiers = "en-w400dp-h860dp-xhdpi")
    @Test fun english() {
        show(many(), 400f, 860f)
        choice("Needs you").assertIsSelected()
        capture("phone-glass-en")
    }
}
