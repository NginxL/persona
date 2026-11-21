package persona.feature.chat

import android.graphics.Bitmap
import androidx.compose.runtime.getValue
import androidx.compose.runtime.key
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.ui.graphics.asAndroidBitmap
import androidx.compose.ui.test.captureToImage
import androidx.compose.ui.test.hasText
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onNodeWithTag
import androidx.test.ext.junit.runners.AndroidJUnit4
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.core.contracts.BackgroundTask
import persona.core.contracts.Session
import persona.core.contracts.Workflow
import persona.core.contracts.WorkflowAgent
import persona.core.contracts.api.SessionLogTail
import persona.core.designsystem.AppStyle
import persona.core.designsystem.PersonaTheme
import java.io.File
import java.time.Instant

@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "en-w400dp-h840dp-xhdpi")
class BackgroundTasksShotsTest {
    @get:Rule val rule = createComposeRule()

    private val now = Instant.now()
    private val monitor = BackgroundTask("watch", "monitor", description = "Watch integration results", command = "tail -f integration.log",
        outputFile = "/project/integration.log", startedAt = now.minusSeconds(118).toString())
    private val ended = BackgroundTask("build", "shell", description = "Run the build", command = "make verify",
        outputFile = "/project/build.log", startedAt = now.minusSeconds(330).toString(),
        status = "failed", endedAt = now.minusSeconds(200).toString(), exitCode = 2)
    private val workflow = Workflow(taskId = "workflow", runId = "verify", name = "Verify the release",
        summary = "Run a check for each client and collect its results.", status = "running", running = true,
        phases = listOf("Verify"), phase = "Verify", phaseIndex = 1, agentsStarted = 2, agentsDone = 1,
        agentsRunning = 1, startedAt = now.minusSeconds(215).toString(),
        agents = listOf(WorkflowAgent("android", "worker@android", label = "Android", phase = "Verify", status = "running", running = true)))
    private val row = Session("box", "worker", manager = "owner", cwd = "/project",
        agent = "claude", background = listOf(monitor), backgroundEnded = listOf(ended), workflows = listOf(workflow))
    private val files = object : FixtureBackgroundFiles() {
        override suspend fun readLogTail(machineId: String, cwd: String, path: String, manager: String?) =
            SessionLogTail("Checking local build cache\nAndroid resources compiled\nOne integration check failed\n", suffix = true)
    }
    private data class Look(val style: AppStyle, val dark: Boolean, val target: BackgroundDestination)

    @Test fun lightAndDarkAcrossStyles() = capture("regular")
    @Test @Config(fontScale = 2f) fun largestTextAcrossStyles() = capture("large")

    private fun capture(scale: String) {
        var look by mutableStateOf(Look(AppStyle.Glass, false, BackgroundDestination.List))
        var summarized by mutableStateOf(false)
        val summary = row.copy(waiting = false, background = null, backgroundEnded = null, workflows = null)
        val output = File(System.getProperty("persona.shots") ?: "build/background-task-shots").apply { mkdirs() }
        rule.setContent {
            key(look) {
                PersonaTheme(style = look.style, darkTheme = look.dark, reduceMotion = true) {
                    BackgroundTasksPanel(BackgroundPanelRequest(row.key, row.manager, look.target), if (summarized) summary else row,
                        actions = null, connection = null, exact = false, files = files,
                        onStopLoop = null, onOpenFile = {}, onOpenAgent = {}, onDismiss = {})
                }
            }
        }
        val samples = listOf(
            Triple("list", BackgroundDestination.List, "Watch integration results"),
            Triple("workflow", BackgroundDestination.Run(workflow.backgroundId()), "Verify the release"),
            Triple("command", BackgroundDestination.Command(ended.id, ended), "One integration check failed"),
        )
        for (style in AppStyle.entries) {
            for (dark in listOf(false, true)) {
                for ((scene, target, expected) in samples) {
                    rule.runOnIdle { look = Look(style, dark, target) }
                    rule.waitUntil(12_000) {
                        rule.onAllNodes(hasText(expected, substring = true), useUnmergedTree = true).fetchSemanticsNodes().isNotEmpty()
                    }
                    val name = listOf(scene, style.name.lowercase(), if (dark) "dark" else "light", scale).joinToString("-")
                    val bitmap = rule.onNodeWithTag("background-panel").captureToImage().asAndroidBitmap()
                    File(output, "$name.png").outputStream().use { assertTrue(bitmap.compress(Bitmap.CompressFormat.PNG, 100, it)) }
                    if (scene == "command" && style == AppStyle.Glass) {
                        rule.runOnIdle { summarized = true }
                        rule.onNodeWithTag("background-list").assertDoesNotExist()
                        val retained = rule.onNodeWithTag("background-panel").captureToImage().asAndroidBitmap()
                        assertTrue("The retained command detail should keep its appearance ($name)", bitmap.sameAs(retained))
                        rule.runOnIdle { summarized = false }
                    }
                }
            }
        }
    }
}
