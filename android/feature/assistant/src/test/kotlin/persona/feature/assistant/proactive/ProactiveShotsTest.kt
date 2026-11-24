package persona.feature.assistant.proactive

import android.graphics.Bitmap
import androidx.activity.ComponentActivity
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.width
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.getValue
import androidx.compose.runtime.key
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.asAndroidBitmap
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.semantics.SemanticsActions
import androidx.compose.ui.test.assertIsDisplayed
import androidx.compose.ui.test.captureToImage
import androidx.compose.ui.test.hasAnyDescendant
import androidx.compose.ui.test.hasScrollAction
import androidx.compose.ui.test.hasText
import androidx.compose.ui.test.getUnclippedBoundsInRoot
import androidx.compose.ui.test.junit4.createAndroidComposeRule
import androidx.compose.ui.test.onAllNodesWithText
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.compose.ui.test.performScrollToIndex
import androidx.compose.ui.test.performScrollToNode
import androidx.compose.ui.unit.dp
import androidx.compose.ui.text.TextLayoutResult
import androidx.test.ext.junit.runners.AndroidJUnit4
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.core.contracts.Machine
import persona.core.contracts.SessionKey
import persona.core.contracts.api.Run
import persona.core.contracts.api.ScheduledTask
import persona.core.designsystem.*
import java.io.File
import java.time.Instant

/**
 * Settings › Assistant › Proactive in each of the app's languages (docs/protocol/i18n.md): the overview, a routine, a new one from a
 * template, all follow-ups and all runs, Glass light and dark (the overview in every style). The data is fake and
 * written in the language of the shot, as a person would. Written as PNGs to `build/i18n-shots/` (or
 * `-Dpersona.shots=<dir>`) to look at.
 */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
class ProactiveShotsTest {
    @get:Rule val rule = createAndroidComposeRule<ComponentActivity>()

    private fun out(): File = File(System.getProperty("persona.shots") ?: "build/i18n-shots").also { it.mkdirs() }

    @Config(sdk = [35], qualifiers = "zh-w400dp-h1500dp-xhdpi")
    @Test fun simplifiedChinese() = shots("zh", Words("主动做事", "定时任务", "早报", "周报", "晚间回顾", "看看 CI 跑完没有", "会前准备", "服务器", "服务巡检"))

    @Config(sdk = [35], qualifiers = "en-w400dp-h1500dp-xhdpi")
    @Test fun english() = shots("en", englishWords())

    @Config(sdk = [35], qualifiers = "en-w400dp-h1500dp-xhdpi", fontScale = 2f)
    @Test fun englishLargestText() = shots("en", englishWords(), large = true)

    private fun englishWords() = Words("Proactive", "Scheduled task", "Morning briefing", "Weekly report", "Evening review",
        "Check whether CI finished", "Meeting prep", "Server", "Service check")

    @Config(sdk = [35], qualifiers = "ja-w400dp-h1500dp-xhdpi")
    @Test fun japanese() = shots("ja", Words("自動実行", "定期タスク", "朝のまとめ", "週報", "夜の振り返り", "CIが終わったか確認", "会議の準備", "サーバー", "サービス巡回"))

    /** The page title and the routine editor's title in the shot's language, then the user's own data. */
    private class Words(
        val title: String, val routine: String, val morning: String, val weekly: String, val evening: String,
        val ci: String, val prep: String, val server: String, val patrol: String,
    )

    private class FakeApi(var table: List<ScheduledTask>, val runs: List<Run>) : ProactiveApi {
        override suspend fun tasks() = table
        override suspend fun saveTask(task: ScheduledTask): ScheduledTask {
            val saved = task.copy(inbox = task.inbox ?: table.firstOrNull { it.id == task.id }?.inbox)
            table = table.map { if (it.id == saved.id) saved else it }
            return saved
        }
        override suspend fun deleteTask(id: String) { table = table.filterNot { it.id == id } }
        override suspend fun runTask(id: String) = SessionKey("server", "run-$id")
        override suspend fun freshTask(id: String) = table.first { it.id == id }.copy(runSessionId = null)
        override suspend fun runs() = runs
        override suspend fun assistantMachine() = "server"
    }

    private fun shots(language: String, words: Words, large: Boolean = false) {
        val now = Instant.now()
        fun at(minutes: Long) = now.plusSeconds(minutes * 60).toString()
        val tasks = listOf(
            ScheduledTask(id = "morning", title = words.morning, prompt = words.morning, repeats = ScheduledTask.DAILY, time = "09:00",
                weekdays = listOf(1, 2, 3, 4, 5), enabled = true, lastRun = at(-60 * 20), lastSessionId = "s1", lastStatus = "ok", nextRun = at(150),
                runMachineId = "server", runSessionId = "own-1", inbox = "worthy"),
            ScheduledTask(id = "weekly", title = words.weekly, prompt = words.weekly, repeats = ScheduledTask.DAILY, time = "17:00", weekdays = listOf(5),
                enabled = true, createdBy = "assistant", lastRun = at(-60 * 24 * 3), lastStatus = "failed", lastError = "assistant machine is offline",
                nextRun = at(60 * 24 * 3), permissionMode = BypassMode),
            ScheduledTask(id = "patrol", title = words.patrol, prompt = words.patrol, repeats = ScheduledTask.INTERVAL, everyMinutes = 30,
                at = at(12), nextRun = at(12), enabled = true, permissionMode = "auto", push = ScheduledTask.PUSH_ASKED, inbox = "every"),
            ScheduledTask(id = "evening", title = words.evening, prompt = words.evening, repeats = ScheduledTask.DAILY, time = "21:30",
                weekdays = listOf(1, 3, 5), enabled = false),
            ScheduledTask(id = "f1", repeats = ScheduledTask.ONCE, at = at(40), nextRun = at(40), title = words.prep, prompt = words.prep, enabled = true,
                machineId = "server", sessionId = "s2"),
            ScheduledTask(id = "f2", repeats = ScheduledTask.ONCE, at = at(60 * 5), nextRun = at(60 * 5), prompt = words.ci, enabled = true),
        )
        val runs = listOf(
            Run(id = "r1", kind = "routine", refId = "morning", title = words.morning, at = at(-60 * 20), due = at(-60 * 20), status = "ok",
                machineId = "server", sessionId = "s1"),
            Run(id = "r2", kind = "followup", title = words.prep, at = at(-60 * 26), status = "skipped"),
            Run(id = "r3", kind = "routine", refId = "weekly", title = words.weekly, at = at(-60 * 24 * 3), status = "failed",
                error = "assistant machine is offline"),
            Run(id = "r4", kind = "routine", refId = "morning", title = words.morning, at = at(-60 * 24 * 2), status = "ok", manual = true),
        )
        val machines = listOf(Machine("server", name = words.server))
        val api = FakeApi(tasks, runs)
        var style by mutableStateOf(AppStyle.Glass)
        var dark by mutableStateOf(false)
        var page by mutableStateOf<String?>(null)
        var hubFeatures by mutableStateOf(setOf(ScheduledTask.PUSH_FEATURE, ScheduledTask.INBOX_FEATURE))
        val inboxWords = when (language) {
            "zh" -> listOf("跑完放进收件箱", "不放", "有值得看的才放", "每次都放")
            "ja" -> listOf("完了後に受信箱へ入れる", "入れない", "注目時のみ", "毎回")
            else -> listOf("When done, put it in the Inbox", "Don't", "Only if worth a look", "Every time")
        }.let { full ->
            if (!large) full else listOf(when (language) {
                "zh" -> "跑完放收件箱"
                "ja" -> "完了後に受信箱へ"
                else -> "Inbox when done"
            }) + full.drop(1)
        }
        val inboxMarks = when (language) {
            "zh" -> listOf("收件箱：值得看才放", "收件箱：每次")
            "ja" -> listOf("受信箱：見る価値があれば", "受信箱：毎回")
            else -> listOf("Inbox: if worth a look", "Inbox: every time")
        }
        rule.setContent {
            PersonaTheme(darkTheme = dark, reduceMotion = true, style = style) { CompositionLocalProvider(LocalHubFeatures provides hubFeatures) {
                Box(Modifier.testTag("shot").width(400.dp).height(1400.dp).background(PersonaTheme.palette.background)) {
                    key(page) {
                        val scope = rememberCoroutineScope()
                        val say = rememberProactiveText()
                        val store = remember { ProactiveStore(api, scope, say) }
                        ProactiveScreen(store, onBack = {}, onOpenConversation = {}, machines = machines, initialPage = page)
                    }
                }
            } }
        }
        fun capture(name: String) {
            rule.waitForIdle()
            val bitmap = rule.onNodeWithTag("shot").captureToImage().asAndroidBitmap()
            File(out(), "proactive-$language-$name${if (large) "-large" else ""}.png").outputStream().use { bitmap.compress(Bitmap.CompressFormat.PNG, 100, it) }
        }
        if (!large) {
            rule.waitForIdle()
            assertTrue(rule.onAllNodesWithText(words.title).fetchSemanticsNodes().isNotEmpty())
            for (s in AppStyle.entries) for (d in listOf(false, true)) {
                if (s != AppStyle.Glass && d) continue
                style = s; dark = d
                capture("overview-${s.name.lowercase()}-${if (d) "dark" else "light"}")
            }
        }
        // "Push when done" (the service check is only its alerts): the list's mark and the editor's choice in every style, light and dark.
        for (target in listOf<String?>(null, "routine:patrol")) {
            page = target
            rule.waitForIdle()
            if (target == null) inboxMarks.forEach { rule.onNodeWithText(it, substring = true).assertExists() }
            if (large && target != null) rule.onAllNodes(hasScrollAction() and hasAnyDescendant(hasText(words.patrol)))[0]
                .performScrollToIndex(17)
            if (target != null) inboxWords.forEach {
                assertTrue("missing $it", rule.onAllNodesWithText(it).fetchSemanticsNodes().isNotEmpty())
                if (large) rule.onNodeWithText(it).assertIsDisplayed()
            }
            for (s in AppStyle.entries) for (d in listOf(false, true)) {
                style = s; dark = d
                rule.waitForIdle()
                if (large && target != null) {
                    val pageBounds = rule.onNodeWithTag("shot").getUnclippedBoundsInRoot()
                    inboxWords.forEach {
                        val option = rule.onNodeWithText(it, useUnmergedTree = true)
                        assertTrue("$it is clipped outside the page in $s", option.getUnclippedBoundsInRoot().right <= pageBounds.right)
                        val layouts = mutableListOf<TextLayoutResult>()
                        val _ = option.fetchSemanticsNode().config[SemanticsActions.GetTextLayoutResult].action!!(layouts)
                        val layout = layouts.single()
                        val fits = layout.multiParagraph.maxIntrinsicWidth <= layout.layoutInput.constraints.maxWidth || layout.lineCount > 1
                        assertTrue("$it is horizontally clipped in $s: lines=${layout.lineCount}, constraints=${layout.layoutInput.constraints}, intrinsic=${layout.multiParagraph.maxIntrinsicWidth}, size=${layout.size}", fits)
                    }
                }
                capture("push-${target?.substringAfter(':') ?: "overview"}-${s.name.lowercase()}-${if (d) "dark" else "light"}")
            }
        }
        if (large) return
        page = null
        style = AppStyle.Glass
        for (target in listOf("routine:morning", "routine:weekly", "routine:patrol", "routine:f1", "new:0", "followups", "runs")) {
            page = target
            for (d in listOf(false, true)) {
                dark = d
                capture("${target.replace(':', '-')}-glass-${if (d) "dark" else "light"}")
            }
        }
        hubFeatures = setOf(ScheduledTask.PUSH_FEATURE)
        page = "routine:evening"
        rule.waitForIdle()
        rule.onNodeWithText(inboxWords.first()).assertDoesNotExist()
        page = "routine:morning"
        rule.waitForIdle()
        rule.onAllNodes(hasScrollAction() and hasAnyDescendant(hasText(words.morning)))[0]
            .performScrollToNode(hasText(inboxWords.first()))
        rule.onNodeWithText(inboxWords.first()).assertExists()
        if (language == "en") {
            hubFeatures = setOf(ScheduledTask.PUSH_FEATURE, ScheduledTask.INBOX_FEATURE)
            page = null
            rule.waitForIdle()
            page = "routine:patrol"
            rule.waitForIdle()
            rule.onNodeWithText("Only if worth a look").performClick()
            rule.onNodeWithText("Save").performClick()
            rule.waitForIdle()
            assertEquals("worthy", api.table.single { it.id == "patrol" }.inbox)
            assertEquals(ScheduledTask.PUSH_ASKED, api.table.single { it.id == "patrol" }.push)
            page = null
            rule.waitForIdle()
            page = "routine:patrol"
            rule.waitForIdle()
            rule.onNodeWithText("Don't").performClick()
            rule.onNodeWithText("Save").performClick()
            rule.waitForIdle()
            assertEquals("", api.table.single { it.id == "patrol" }.inbox)
            assertEquals(ScheduledTask.PUSH_ASKED, api.table.single { it.id == "patrol" }.push)
        }
    }
}
