package persona.feature.agents

import android.graphics.Bitmap
import android.graphics.Canvas
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.width
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.asAndroidBitmap
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.test.captureToImage
import androidx.compose.ui.test.hasAnyAncestor
import androidx.compose.ui.test.hasText
import androidx.compose.ui.test.isPopup
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onAllNodesWithText
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.compose.ui.unit.dp
import androidx.test.core.app.ApplicationProvider
import androidx.test.ext.junit.runners.AndroidJUnit4
import org.junit.After
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Before
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.core.contracts.Session
import persona.core.contracts.Todo
import persona.core.contracts.TodoAgent
import persona.core.contracts.TodoPRStatus
import persona.core.designsystem.AppStyle
import persona.core.designsystem.PersonaTheme
import persona.core.domain.tasks.TodoListFilter
import persona.core.domain.tasks.TodoListSort
import java.io.File
import java.time.Instant
import java.time.LocalDateTime
import java.time.ZoneId
import java.util.TimeZone

/**
 * The top of the To-dos list (#3712) and its filter and sort (#3740) on a phone, Wednesday November 11 2026 at noon:
 * no "N open · N shelved · N done" line, each section's count beside its title; the filter button beside List | Calendar
 * and its menu (Show with counts, Sort by); This week, Today, This month and No due date, each with its "×" chip and,
 * since one to-do waiting on the user has no due date, "1 more waiting on you" over the list; the rows of each section by
 * due date and by priority; an empty filter (todos-filter-empty-*, not the parity test's todos-empty-*). Every style, light and dark.
 */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "zh-w400dp-h860dp-xhdpi")
class TodoListFilterShotsTest {
    @get:Rule val rule = createComposeRule()

    private fun out(): File = File(System.getProperty("persona.shots") ?: "build/i18n-shots").also { it.mkdirs() }

    private val zone = TimeZone.getDefault()
    @Before fun inShanghai() = TimeZone.setDefault(TimeZone.getTimeZone("Asia/Shanghai"))
    @After fun back() = TimeZone.setDefault(zone)

    private val here = ZoneId.of("Asia/Shanghai")
    private val now = LocalDateTime.of(2026, 11, 11, 12, 0).atZone(here).toInstant()
    private fun ago(minutes: Long) = now.minusSeconds(minutes * 60).toString()
    private fun at(day: Int, hour: Int, month: Int = 11) = LocalDateTime.of(2026, month, day, hour, 0).atZone(here).toInstant().toString()

    private val sessions = listOf(
        Session("mac", "s1", title = "升级数据库", state = "needs_you", updatedAt = ago(12)),
        Session("mac", "s2", title = "评估缓存", state = "needs_you", updatedAt = ago(30)),
        Session("mac", "s3", title = "修崩溃", state = "working", updatedAt = ago(1)),
        Session("mac", "s4", title = "批量操作", state = "working", updatedAt = ago(4)),
        Session("mac", "s5", title = "推送", state = "unread", updatedAt = ago(50)),
    )
    private fun agent(sid: String) = listOf(TodoAgent("mac", sid, linkedAt = ago(600)))
    private val todos = listOf(
        Todo("t1", title = "数据库升级到 18", agents = agent("s1"), dueAt = at(11, 18), updatedAt = ago(12), createdAt = ago(900)),
        Todo("t2", title = "选缓存方案", agents = agent("s2"), priority = "high", updatedAt = ago(30), createdAt = ago(900)),
        Todo("t3", title = "修小组件横屏崩溃", agents = agent("s3"), dueDate = "2026-11-20", updatedAt = ago(1), createdAt = ago(900)),
        Todo("t4", title = "收件箱分拣加批量操作", agents = agent("s4"), priority = "high", dueDate = "2026-11-13", updatedAt = ago(4), createdAt = ago(900)),
        Todo("t5", title = "修复 #412 推送丢失", agents = agent("s5"), updatedAt = ago(50), createdAt = ago(900)),
        Todo("t6", title = "网页加载提速", dueDate = "2026-11-11", doneAt = ago(90), doneBy = "user", updatedAt = ago(90), createdAt = ago(2000)),
        Todo("t7", title = "设置页加搜索", prUrl = "https://example.com/pr/31", prStatus = TodoPRStatus(url = "https://example.com/pr/31", state = "open", checkedAt = ago(0)), origin = "conv", dueDate = "2026-11-07", updatedAt = ago(3000), createdAt = ago(9000)),
        Todo("t8", title = "写发布说明", priority = "low", dueDate = "2026-11-14", updatedAt = ago(200), createdAt = ago(3000)),
        Todo("t9", title = "续域名", dueDate = "2026-12-03", updatedAt = ago(400), createdAt = ago(3000)),
        Todo("t10", title = "轮换 API Key", priority = "low", updatedAt = ago(300), createdAt = ago(3000)),
        Todo("t11", title = "交房租", priority = "high", dueDate = "2026-11-30", updatedAt = ago(500), createdAt = ago(3000)),
        Todo("t12", title = "调研离线同步", origin = "conv", updatedAt = ago(7000), createdAt = ago(9000)),
        Todo("t13", title = "交周报", dueDate = "2026-11-06", doneAt = ago(7200), doneBy = "user", updatedAt = ago(7200), createdAt = ago(9000)),
    )

    private fun has(text: String) = rule.onAllNodesWithText(text, substring = true, useUnmergedTree = true).fetchSemanticsNodes().isNotEmpty()
    private fun top(text: String) = rule.onNodeWithText(text, useUnmergedTree = true).fetchSemanticsNode().boundsInRoot.top

    private fun open(list: List<Todo> = todos, style: AppStyle, dark: Boolean, tall: Boolean = false) {
        rule.setContent {
            PersonaTheme(darkTheme = dark, reduceMotion = true, style = style) {
                Box(Modifier.testTag("shot").width(400.dp).height(if (tall) 2400.dp else 860.dp).background(PersonaTheme.palette.background)) {
                    TodosScreen(list, sessions, null, onBack = {}, onOpenTodo = {}, now = now)
                }
            }
        }
        rule.waitForIdle()
    }

    /** Picks [choice] from the filter button's menu. */
    private fun pick(choice: String) {
        rule.onNodeWithTag("todos-filter").performClick()
        rule.waitForIdle()
        rule.onNode(hasText(choice) and hasAnyAncestor(isPopup())).performClick()
        rule.waitForIdle()
    }

    private fun save(name: String, style: AppStyle, dark: Boolean, bitmap: Bitmap = rule.onNodeWithTag("shot").captureToImage().asAndroidBitmap()) {
        File(out(), "todos-$name-${style.name.lowercase()}-${if (dark) "dark" else "light"}.png").outputStream()
            .use { bitmap.compress(Bitmap.CompressFormat.PNG, 100, it) }
    }

    private fun shot(scene: String, style: AppStyle, dark: Boolean) {
        when (scene) {
            "after" -> {
                open(style = style, dark = dark)
                // no summary line; the counts are the sections'
                assertFalse("summary line", has("个未完成"))
                assertTrue(has("等你处理") && has("工作中"))
                rule.onNodeWithTag("todos-filter").assertExists()
                save("after", style, dark)
            }
            "menu" -> {
                open(style = style, dark = dark)
                pick("本周")
                rule.onNodeWithTag("todos-filter").performClick()
                rule.waitForIdle()
                // Show, each with how many it would show; Sort by
                for (t in listOf("显示", "全部", "13 条", "无截止日期", "今天", "本月", "排序方式", "最近动过", "截止时间", "优先级")) assertTrue("menu: $t", has(t))
                val page = rule.onNodeWithTag("shot").captureToImage().asAndroidBitmap()
                val menu = rule.onNode(isPopup()).captureToImage().asAndroidBitmap()
                val p = rule.onNodeWithTag("shot").fetchSemanticsNode().positionOnScreen
                val m = rule.onNode(isPopup()).fetchSemanticsNode().positionOnScreen
                val both = page.copy(Bitmap.Config.ARGB_8888, true)
                Canvas(both).drawBitmap(menu, m.x - p.x, m.y - p.y, null)
                save("menu", style, dark, both)
            }
            "week", "today", "month", "no_due" -> {
                open(style = style, dark = dark)
                val title = mapOf("week" to "本周", "today" to "今天", "month" to "本月", "no_due" to "无截止日期").getValue(scene)
                pick(title)
                rule.onNodeWithTag("todos-filter-clear").assertExists()
                // Needs you is filtered too, and says what it leaves out (选缓存方案 has no due date, 数据库升级 is due today)
                assertTrue("$scene: no hidden line", has("另有 1 项需要你，不在「$title」里"))
                save(scene, style, dark)
            }
            "due", "priority" -> {
                open(style = style, dark = dark)
                pick(if (scene == "due") "截止时间" else "优先级")
                // Needs you, the first section: most recent first was 数据库 (12 min) then 选缓存 (30 min)
                if (scene == "due") assertTrue("due: Needs you", top("数据库升级到 18") < top("选缓存方案"))
                else assertTrue("priority: Needs you", top("选缓存方案") < top("数据库升级到 18"))
                rule.onNodeWithTag("todos-filter-clear").assertDoesNotExist()
                save(scene, style, dark)
            }
            "filter-empty" -> {
                // nothing due today, nothing overdue: an empty filter, and Show all goes back
                open(todos.filter { it.id in setOf("t3", "t8", "t9", "t10") }, style, dark)
                pick("今天")
                assertTrue(has("今天没有到期的待办") && has("换个筛选看看。"))
                save("filter-empty", style, dark)
                rule.onNodeWithText("看全部").performClick()
                rule.waitForIdle()
                assertTrue(has("修小组件横屏崩溃"))
                rule.onNodeWithTag("todos-filter-clear").assertDoesNotExist()
            }
        }
    }

    @Test fun afterGlass() = shot("after", AppStyle.Glass, false)
    @Test fun afterGlassDark() = shot("after", AppStyle.Glass, true)
    @Test fun afterBrief() = shot("after", AppStyle.Brief, false)
    @Test fun afterBriefDark() = shot("after", AppStyle.Brief, true)
    @Test fun afterBoard() = shot("after", AppStyle.Board, false)
    @Test fun afterBoardDark() = shot("after", AppStyle.Board, true)
    @Test fun menuGlass() = shot("menu", AppStyle.Glass, false)
    @Test fun menuGlassDark() = shot("menu", AppStyle.Glass, true)
    @Test fun menuBrief() = shot("menu", AppStyle.Brief, false)
    @Test fun menuBriefDark() = shot("menu", AppStyle.Brief, true)
    @Test fun menuBoard() = shot("menu", AppStyle.Board, false)
    @Test fun menuBoardDark() = shot("menu", AppStyle.Board, true)
    @Test fun weekGlass() = shot("week", AppStyle.Glass, false)
    @Test fun weekGlassDark() = shot("week", AppStyle.Glass, true)
    @Test fun weekBrief() = shot("week", AppStyle.Brief, false)
    @Test fun weekBriefDark() = shot("week", AppStyle.Brief, true)
    @Test fun weekBoard() = shot("week", AppStyle.Board, false)
    @Test fun weekBoardDark() = shot("week", AppStyle.Board, true)
    @Test fun todayGlass() = shot("today", AppStyle.Glass, false)
    @Test fun todayGlassDark() = shot("today", AppStyle.Glass, true)
    @Test fun todayBrief() = shot("today", AppStyle.Brief, false)
    @Test fun todayBriefDark() = shot("today", AppStyle.Brief, true)
    @Test fun todayBoard() = shot("today", AppStyle.Board, false)
    @Test fun todayBoardDark() = shot("today", AppStyle.Board, true)
    @Test fun monthGlass() = shot("month", AppStyle.Glass, false)
    @Test fun monthGlassDark() = shot("month", AppStyle.Glass, true)
    @Test fun monthBrief() = shot("month", AppStyle.Brief, false)
    @Test fun monthBriefDark() = shot("month", AppStyle.Brief, true)
    @Test fun monthBoard() = shot("month", AppStyle.Board, false)
    @Test fun monthBoardDark() = shot("month", AppStyle.Board, true)
    @Test fun noDueGlass() = shot("no_due", AppStyle.Glass, false)
    @Test fun noDueGlassDark() = shot("no_due", AppStyle.Glass, true)
    @Test fun noDueBrief() = shot("no_due", AppStyle.Brief, false)
    @Test fun noDueBriefDark() = shot("no_due", AppStyle.Brief, true)
    @Test fun noDueBoard() = shot("no_due", AppStyle.Board, false)
    @Test fun noDueBoardDark() = shot("no_due", AppStyle.Board, true)
    @Test fun dueGlass() = shot("due", AppStyle.Glass, false)
    @Test fun dueGlassDark() = shot("due", AppStyle.Glass, true)
    @Test fun dueBrief() = shot("due", AppStyle.Brief, false)
    @Test fun dueBriefDark() = shot("due", AppStyle.Brief, true)
    @Test fun dueBoard() = shot("due", AppStyle.Board, false)
    @Test fun dueBoardDark() = shot("due", AppStyle.Board, true)
    @Test fun priorityGlass() = shot("priority", AppStyle.Glass, false)
    @Test fun priorityGlassDark() = shot("priority", AppStyle.Glass, true)
    @Test fun priorityBrief() = shot("priority", AppStyle.Brief, false)
    @Test fun priorityBriefDark() = shot("priority", AppStyle.Brief, true)
    @Test fun priorityBoard() = shot("priority", AppStyle.Board, false)
    @Test fun priorityBoardDark() = shot("priority", AppStyle.Board, true)
    @Test fun emptyGlass() = shot("filter-empty", AppStyle.Glass, false)
    @Test fun emptyGlassDark() = shot("filter-empty", AppStyle.Glass, true)
    @Test fun emptyBrief() = shot("filter-empty", AppStyle.Brief, false)
    @Test fun emptyBriefDark() = shot("filter-empty", AppStyle.Brief, true)
    @Test fun emptyBoard() = shot("filter-empty", AppStyle.Board, false)
    @Test fun emptyBoardDark() = shot("filter-empty", AppStyle.Board, true)

    // Which to-dos each filter shows, the whole list in sight (a phone's screen draws only its first rows).
    @Config(qualifiers = "zh-w400dp-h2400dp-xhdpi")
    @Test fun eachFilterShowsItsToDos() {
        open(style = AppStyle.Glass, dark = false, tall = true)
        val cases = listOf(
            // This week (Monday 9th to Sunday 15th): Wednesday's, Friday's, Saturday's, the PR overdue since the 7th, the one done today
            "本周" to (listOf("数据库升级到 18", "收件箱分拣加批量操作", "设置页加搜索", "写发布说明", "网页加载提速") to
                listOf("修小组件横屏崩溃", "选缓存方案", "交房租", "续域名", "交周报")),
            "今天" to (listOf("数据库升级到 18", "设置页加搜索", "网页加载提速") to listOf("收件箱分拣加批量操作", "写发布说明")),
            "本月" to (listOf("修小组件横屏崩溃", "交房租", "写发布说明", "交周报") to listOf("续域名", "选缓存方案")),
            "无截止日期" to (listOf("选缓存方案", "轮换 API Key", "修复 #412 推送丢失") to listOf("数据库升级到 18", "交房租")),
        )
        for ((filter, rows) in cases) {
            pick(filter)
            // Shelved and Done stay folded: open them to see what they hold
            for (folded in listOf("搁置", "已完成")) rule.onAllNodesWithText(folded).fetchSemanticsNodes().firstOrNull()?.let {
                rule.onAllNodesWithText(folded)[0].performClick(); rule.waitForIdle()
            }
            rows.first.forEach { assertTrue("$filter: no $it", has(it)) }
            rows.second.forEach { assertFalse("$filter: $it shows", has(it)) }
            for (folded in listOf("搁置", "已完成")) rule.onAllNodesWithText(folded).fetchSemanticsNodes().firstOrNull()?.let {
                rule.onAllNodesWithText(folded)[0].performClick(); rule.waitForIdle()
            }
        }
    }

    // A sort orders the rows inside each section; the sections keep their order.
    @Config(qualifiers = "zh-w400dp-h2400dp-xhdpi")
    @Test fun aSortOrdersInsideEachSection() {
        open(style = AppStyle.Glass, dark = false, tall = true)
        // most recent first: 修小组件 (1 min) before 收件箱 (4 min)
        assertTrue(top("修小组件横屏崩溃") < top("收件箱分拣加批量操作"))
        pick("截止时间")
        assertTrue(top("收件箱分拣加批量操作") < top("修小组件横屏崩溃"))
        assertTrue(top("数据库升级到 18") < top("选缓存方案"))
        pick("优先级")
        assertTrue(top("收件箱分拣加批量操作") < top("修小组件横屏崩溃"))
        assertTrue(top("选缓存方案") < top("数据库升级到 18"))
        // Needs you stays above Working
        assertTrue(top("数据库升级到 18") < top("修小组件横屏崩溃"))
    }

    // The filter and the sort are kept on the device.
    @Test fun choicesAreKept() {
        val context = ApplicationProvider.getApplicationContext<android.content.Context>()
        TodoListPrefs(context).apply { filter = TodoListFilter.Week; sort = TodoListSort.Priority }
        assertEquals(TodoListFilter.Week, TodoListPrefs(context).filter)
        assertEquals(TodoListSort.Priority, TodoListPrefs(context).sort)
        open(style = AppStyle.Glass, dark = false)
        rule.onNodeWithTag("todos-filter-clear").assertExists()
        assertTrue(has("另有 1 项需要你，不在「本周」里"))
    }
}
