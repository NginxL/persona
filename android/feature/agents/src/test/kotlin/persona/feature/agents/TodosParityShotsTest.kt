package persona.feature.agents

import android.graphics.Bitmap
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.width
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.asAndroidBitmap
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.test.captureToImage
import androidx.compose.ui.test.hasScrollToNodeAction
import androidx.compose.ui.test.hasText
import androidx.compose.ui.test.isDialog
import androidx.compose.ui.test.performScrollToNode
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onAllNodesWithText
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.compose.ui.unit.dp
import androidx.test.ext.junit.runners.AndroidJUnit4
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.core.contracts.Machine
import persona.core.contracts.Session
import persona.core.contracts.Todo
import persona.core.contracts.TodoAgent
import persona.core.contracts.TodoDelivery
import persona.core.contracts.TodoSpace
import persona.core.designsystem.AppStyle
import persona.core.designsystem.PersonaTheme
import java.io.File
import java.time.Instant

/**
 * To-dos as iOS shows them (#3034): the list's line under each open to-do (red while an agent waits, the notes, "Not handed to an agent yet"),
 * finished ones with the auto-done reason, the empty list's "New to-do", the detail's "N agents · created X", projects and
 * where each agent came from, the status sections (#3206: Needs you, PR to merge with its PR, Just finished, Other open unfolded), and "New to-do…" / "Create and link" when picking a to-do. Every style, light and dark.
 */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "zh-w400dp-h1400dp-xhdpi")
class TodosParityShotsTest {
    @get:Rule val rule = createComposeRule()

    private fun out(): File = File(System.getProperty("persona.shots") ?: "build/i18n-shots").also { it.mkdirs() }

    private val now = Instant.parse("2026-11-05T09:30:00Z")
    private fun ago(minutes: Long) = now.minusSeconds(minutes * 60).toString()
    private val machines = listOf(Machine("mac", "工作笔记本", online = true), Machine("box", "构建服务器", online = true))
    private val sessions = listOf(
        Session("mac", "s1", title = "修登录页", cwd = "/code/app", state = "needs_you", spaceId = "w1", spaceName = "app", slotName = "1",
            lastText = "要跑一下测试吗？", updatedAt = ago(2)),
        Session("mac", "s2", title = "补测试", cwd = "/code/app", state = "working", spaceId = "w1", spaceName = "app", slotName = "2",
            lastText = "在写 AuthService 的测试", updatedAt = ago(1)),
    )
    private val todos = listOf(
        Todo("t1", title = "发登录修复", notes = "先跑测试再提 PR。", createdAt = ago(90), updatedAt = ago(3),
            spaces = listOf(TodoSpace("mac", "app"), TodoSpace("box", "ci")),
            agents = listOf(TodoAgent("mac", "s1", title = "修登录页", cwd = "/code/app"),
                TodoAgent("mac", "s2", title = "补测试", cwd = "/code/app", source = "spawned", via = "mac/s1"))),
        // Done as asked, held at its PR (#3306): the row and the page say where and who can unhold it.
        Todo("t2", title = "写发布说明", notes = "这周合并的 PR 都要提到", createdAt = ago(300), spaces = emptyList(), origin = "conv",
            delivery = TodoDelivery("pr", gate = "等你批准合并", unblock = "user", at = ago(20))),
        Todo("t3", title = "轮换 API Key", createdAt = ago(400), spaces = emptyList()),
        Todo("t4", title = "整理崩溃报告", createdAt = ago(600), doneAt = ago(120), doneBy = "auto", doneReason = "Agent 已经把报告整理好并发到群里",
            spaces = emptyList()),
        // An open PR that is not held for the owner's approval belongs to PR to merge, not Needs you or Shelved.
        Todo("t5", title = "发布补丁", createdAt = ago(80), updatedAt = ago(5), origin = "conv", spaces = emptyList(),
            delivery = TodoDelivery("pr", unblock = "agent", at = ago(5))),
    )

    /** Agents that never started (#3713): one waits on the owner; the other's work finished in a to-do of its plan. */
    private val failed = todos + listOf(
        Todo("f1", title = "部署自托管面板", createdAt = ago(200), updatedAt = ago(60), doneAt = ago(60), origin = "conv",
            startError = "rpc: connection closed", spaces = emptyList()),
        Todo("f2", title = "修复控制台远程执行漏洞", createdAt = ago(300), updatedAt = ago(150), doneAt = ago(150), origin = "conv",
            planId = "p", startError = "rpc: connection closed", spaces = emptyList()),
        Todo("f3", title = "控制台远程执行漏洞：上线验证", createdAt = ago(300), updatedAt = ago(100), doneAt = ago(100), origin = "conv",
            planId = "p", doneBy = "review", doneReason = "验收通过", spaces = emptyList()),
    )

    private val offline = persona.core.contracts.api.CurrentAssistant(persona.core.contracts.api.ManagerRoster(listOf(
        persona.core.contracts.api.HubManager("assistant", "助手", online = true, isDefault = true, builtin = true),
        persona.core.contracts.api.HubManager("m-8b04d2", "家里", online = false, lastSeen = Instant.now().minusSeconds(3 * 3600).toString()),
    ), "assistant"), persona.core.contracts.api.AssistantManagerChoice.Choice("hub", "m-8b04d2", "assistant"))

    private fun shot(scene: String, style: AppStyle, dark: Boolean, expected: String) {
        rule.setContent {
            PersonaTheme(darkTheme = dark, reduceMotion = true, style = style) {
                Box(Modifier.testTag("shot").width(400.dp).height(1400.dp).background(PersonaTheme.palette.background)) {
                    when (scene) {
                        "list", "list-sections" -> TodosScreen(todos, sessions, null, onBack = {}, onOpenTodo = {}, now = now)
                        "failed" -> TodosScreen(failed, sessions, null, onBack = {}, onOpenTodo = {}, now = now)
                        "empty" -> TodosScreen(emptyList(), emptyList(), null, onBack = {}, onOpenTodo = {}, now = now)
                        // The current assistant offline (the hub's list leaves it out): offline, not "no to-dos" (#3374 follow-up).
                        "offline" -> TodosScreen(emptyList(), emptyList(), null, onBack = {}, onOpenTodo = {}, now = now, assistant = offline)
                        "held" -> TodoDetailScreen("t2", todos, machines, sessions, null, onBack = {}, onOpenSession = {}, onHandOff = {}, now = now)
                        "detail" -> TodoDetailScreen("t1", todos, machines, sessions, null, onBack = {}, onOpenSession = {}, onHandOff = {}, now = now)
                        "picker" -> TodoPickerSheet(todos, { k -> sessions.firstOrNull { it.key == k } }, onDismiss = {}, onPick = {},
                            onCreate = {}, unlink = "移出待办" to {})
                    }
                }
            }
        }
        rule.waitForIdle()
        if (scene == "picker") { rule.onNodeWithText("新建待办…").performClick(); rule.waitForIdle() }
        if (scene == "list-sections") {
            // The actual PR section can be below the initial viewport. Bring it into view without changing triage,
            // fixtures' status sections or the viewport size.
            rule.onNode(hasScrollToNodeAction()).performScrollToNode(hasText(expected))
        }
        assertTrue("$scene: no 「$expected」", rule.onAllNodesWithText(expected, substring = true, useUnmergedTree = true).fetchSemanticsNodes().isNotEmpty())
        val node = if (scene == "picker") rule.onNode(isDialog()) else rule.onNodeWithTag("shot")
        val bitmap = node.captureToImage().asAndroidBitmap()
        File(out(), "todos-$scene-${style.name.lowercase()}-${if (dark) "dark" else "light"}.png").outputStream()
            .use { bitmap.compress(Bitmap.CompressFormat.PNG, 100, it) }
    }

    @Test fun listGlass() = shot("list", AppStyle.Glass, false, "还没交给 Agent")
    @Test fun listGlassDark() = shot("list", AppStyle.Glass, true, "还没交给 Agent")
    @Test fun listBrief() = shot("list", AppStyle.Brief, false, "还没交给 Agent")
    @Test fun listBriefDark() = shot("list", AppStyle.Brief, true, "还没交给 Agent")
    @Test fun listBoard() = shot("list", AppStyle.Board, false, "还没交给 Agent")
    @Test fun listBoardDark() = shot("list", AppStyle.Board, true, "还没交给 Agent")

    @Test fun listHeldGlass() = shot("list", AppStyle.Glass, false, "停在 PR · 卡在「等你批准合并」 · 要你点头")
    @Test fun listHeldBoardDark() = shot("list", AppStyle.Board, true, "停在 PR · 卡在「等你批准合并」 · 要你点头")

    @Test fun heldGlass() = shot("held", AppStyle.Glass, false, "卡在「等你批准合并」")
    @Test fun heldGlassDark() = shot("held", AppStyle.Glass, true, "卡在「等你批准合并」")
    @Test fun heldBrief() = shot("held", AppStyle.Brief, false, "卡在「等你批准合并」")
    @Test fun heldBriefDark() = shot("held", AppStyle.Brief, true, "卡在「等你批准合并」")
    @Test fun heldBoard() = shot("held", AppStyle.Board, false, "卡在「等你批准合并」")
    @Test fun heldBoardDark() = shot("held", AppStyle.Board, true, "卡在「等你批准合并」")

    @Test fun listSectionsGlass() = shot("list-sections", AppStyle.Glass, false, "PR 待合并")
    @Test fun listSectionsGlassDark() = shot("list-sections", AppStyle.Glass, true, "刚做完")

    @Test fun failedGlass() = shot("failed", AppStyle.Glass, false, "部署自托管面板")
    @Test fun failedGlassDark() = shot("failed", AppStyle.Glass, true, "部署自托管面板")
    @Test fun failedBrief() = shot("failed", AppStyle.Brief, false, "部署自托管面板")
    @Test fun failedBriefDark() = shot("failed", AppStyle.Brief, true, "部署自托管面板")
    @Test fun failedBoard() = shot("failed", AppStyle.Board, false, "部署自托管面板")
    @Test fun failedBoardDark() = shot("failed", AppStyle.Board, true, "部署自托管面板")

    @Test fun emptyGlass() = shot("empty", AppStyle.Glass, false, "新建待办")
    @Test fun emptyGlassDark() = shot("empty", AppStyle.Glass, true, "新建待办")
    @Test fun emptyBrief() = shot("empty", AppStyle.Brief, false, "新建待办")
    @Test fun emptyBoard() = shot("empty", AppStyle.Board, false, "新建待办")

    @Test fun offlineGlass() = shot("offline", AppStyle.Glass, false, "最后同步于 3 小时前。它上线后这里会更新。")
    @Test fun offlineGlassDark() = shot("offline", AppStyle.Glass, true, "家里 离线")

    @Test fun detailGlass() = shot("detail", AppStyle.Glass, false, "由 修登录页 派出")
    @Test fun detailGlassDark() = shot("detail", AppStyle.Glass, true, "由 修登录页 派出")
    @Test fun detailBrief() = shot("detail", AppStyle.Brief, false, "由 修登录页 派出")
    @Test fun detailBriefDark() = shot("detail", AppStyle.Brief, true, "由 修登录页 派出")
    @Test fun detailBoard() = shot("detail", AppStyle.Board, false, "由 修登录页 派出")
    @Test fun detailBoardDark() = shot("detail", AppStyle.Board, true, "由 修登录页 派出")

    @Test fun pickerGlass() = shot("picker", AppStyle.Glass, false, "创建并挂上")
    @Test fun pickerGlassDark() = shot("picker", AppStyle.Glass, true, "创建并挂上")
    @Test fun pickerBrief() = shot("picker", AppStyle.Brief, false, "创建并挂上")
    @Test fun pickerBoard() = shot("picker", AppStyle.Board, false, "创建并挂上")
}
