package persona.feature.agents

import android.graphics.Bitmap
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.width
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.asAndroidBitmap
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.semantics.SemanticsActions
import androidx.compose.ui.test.assertIsDisplayed
import androidx.compose.ui.test.captureToImage
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onAllNodesWithText
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.text.TextLayoutResult
import androidx.compose.ui.unit.dp
import androidx.test.ext.junit.runners.AndroidJUnit4
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.core.contracts.BackgroundTask
import persona.core.contracts.Session
import persona.core.contracts.SessionCapabilities
import persona.core.contracts.Workflow
import persona.core.designsystem.*
import persona.core.domain.sort.AgentSession
import java.io.File
import java.time.Instant

/**
 * A long Workflow shares its metadata line with read-only, confirmation, outgoing, draft and background facts. An older
 * hub's row that only has a workflow in its background, and a plain row, preserve their compact forms. Every style,
 * light and dark; PNGs in `build/workflow-row-shots/`.
 */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "zh-w400dp-h700dp-xhdpi")
class AgentWorkflowRowShotsTest {
    @get:Rule val rule = createComposeRule()

    private fun out(): File = File(System.getProperty("persona.shots") ?: "build/workflow-row-shots").also { it.mkdirs() }

    private val now = Instant.parse("2026-11-05T10:14:30Z")
    private val longName = "parity-audit-with-a-name-that-cannot-fit-beside-critical-facts"
    private val audit = Workflow(taskId = "t1", runId = "wf_1", name = longName, status = "running", running = true,
        phases = listOf("Audit", "Verify"), phase = "Verify", phaseIndex = 2, agentsStarted = 185, agentsDone = 169, agentsRunning = 16,
        startedAt = "2026-11-05T10:00:00Z")
    private fun row(id: String, title: String, words: String, background: List<BackgroundTask>? = null, workflows: List<Workflow>? = null,
        sourceKind: String? = null, agent: String? = null, agentLabel: String? = null) =
        AgentSession(Session(machineId = "server", sessionId = id, title = title, cwd = "/home/me/code/persona", status = "idle", state = "working",
            lastText = words, updatedAt = "2026-11-05T10:12:00Z", background = background, workflows = workflows,
            sourceKind = sourceKind, agent = agent, agentLabelHint = agentLabel, capabilities = sourceKind?.let { SessionCapabilities(read = true) }),
        stateSince = "2026-11-05T10:00:00Z")

    private data class Fixture(val row: AgentSession, val metadata: AgentRowMetadata = AgentRowMetadata())

    private val rows = listOf(
        Fixture(row("w", "对齐三端的 workflow 显示", "开了 parity-audit，跑完再汇报。",
            background = listOf(BackgroundTask("t1", "workflow", runId = "wf_1"), BackgroundTask("t2", "workflow", runId = "wf_2"),
                BackgroundTask("b1", "shell", "npm test")),
            workflows = listOf(audit, audit.copy(taskId = "t2", runId = "wf_2", name = "docs-sweep"), audit.copy(taskId = "t0", status = "killed", running = false)),
            sourceKind = "runtime", agent = "dsh", agentLabel = "DSH"),
            AgentRowMetadata(draft = "改", needsConfirmation = true, pendingCount = 2, sending = true)),
        Fixture(row("o", "旧 hub 上的 Agent", "workflow 起来了。", background = listOf(BackgroundTask("t1", "workflow")))),
        Fixture(row("p", "没有后台的 Agent", "改完了。")),
    )

    @Test fun drawsTheWorkflowAndCriticalFactsInEveryStyleAndAppearance() {
        assertEquals(3, rows[0].row.session.backgroundCount) // npm test + two running workflows, not their background entries again
        val workflowText = "后台 workflow「$longName」   Verify 2/2   169/185+   14 分   等 1 个"
        val criticalFacts = listOf("仅查看", "结果待确认", "待发 2", "发送中", "草稿：改", "后台 3")
        val factsText = listOf("DSH", "仅查看", "后台 3", "结果待确认", "待发 2", "发送中", "草稿：改").joinToString("   ")
        var style by mutableStateOf(AppStyle.Glass)
        var dark by mutableStateOf(false)
        rule.setContent {
            PersonaTheme(style = style, darkTheme = dark, reduceMotion = true) {
                Column(Modifier.testTag("shot").width(400.dp).background(PersonaTheme.palette.background).padding(Tokens.Space.s)) {
                    rows.forEachIndexed { index, fixture ->
                        SlotRow(fixture.row, "工作站", fixture.metadata, now, false, false, false, depth = 0,
                            position = GroupPosition(index == 0, index == rows.lastIndex), located = false, onOpen = {}, menuItems = { emptyList() })
                    }
                }
            }
        }

        fun textLayout(text: String): TextLayoutResult {
            val node = rule.onNodeWithText(text, useUnmergedTree = true).fetchSemanticsNode()
            val layouts = mutableListOf<TextLayoutResult>()
            val _ = node.config[SemanticsActions.GetTextLayoutResult].action!!(layouts)
            return layouts.single()
        }

        for (nextStyle in AppStyle.entries) for (nextDark in listOf(false, true)) {
            rule.runOnIdle { style = nextStyle; dark = nextDark }
            rule.onNodeWithText(workflowText, useUnmergedTree = true).assertIsDisplayed()
            assertTrue("$nextStyle dark=$nextDark: long Workflow details must ellipsize", textLayout(workflowText).hasVisualOverflow)
            rule.onNodeWithText("后台运行中", useUnmergedTree = true).assertIsDisplayed()
            rule.onAllNodesWithText("后台 3", substring = true, useUnmergedTree = true).fetchSemanticsNodes().let { assertEquals(1, it.size) }
            criticalFacts.forEach { fact ->
                rule.onNodeWithText(fact, substring = true, useUnmergedTree = true).assertIsDisplayed()
            }
            // Glass names the source on the row's third line (#4338 A9); view only stays among the critical facts.
            if (nextStyle == AppStyle.Glass) rule.onNodeWithText("DSH", useUnmergedTree = true).assertIsDisplayed()
            val factsLayout = textLayout(if (nextStyle == AppStyle.Glass) factsText.removePrefix("DSH   ") else factsText)
            assertFalse("$nextStyle dark=$nextDark: critical facts must keep their full text " +
                "(desired=${factsLayout.multiParagraph.maxIntrinsicWidth}, available=${factsLayout.layoutInput.constraints.maxWidth})",
                factsLayout.hasVisualOverflow)
            val bitmap = rule.onNodeWithTag("shot").captureToImage().asAndroidBitmap()
            val name = "workflow-row-${nextStyle.name.lowercase()}-${if (nextDark) "dark" else "light"}"
            File(out(), "$name.png").outputStream().use { assertTrue(bitmap.compress(Bitmap.CompressFormat.PNG, 100, it)) }
        }
    }
}
