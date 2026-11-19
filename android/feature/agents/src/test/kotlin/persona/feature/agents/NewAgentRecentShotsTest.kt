package persona.feature.agents

import android.graphics.Bitmap
import androidx.activity.ComponentActivity
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.width
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.asAndroidBitmap
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.test.captureToImage
import androidx.compose.ui.test.isDialog
import androidx.compose.ui.test.junit4.createAndroidComposeRule
import androidx.compose.ui.test.longClick
import androidx.compose.ui.test.onAllNodesWithText
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.compose.ui.test.performTouchInput
import androidx.compose.ui.unit.dp
import androidx.datastore.core.DataStore
import androidx.datastore.preferences.core.Preferences
import androidx.datastore.preferences.core.emptyPreferences
import androidx.test.ext.junit.runners.AndroidJUnit4
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.runBlocking
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RuntimeEnvironment
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.core.contracts.Machine
import persona.core.contracts.Session
import persona.core.contracts.Todo
import persona.core.contracts.api.*
import persona.core.designsystem.AppStyle
import persona.core.designsystem.PersonaTheme
import java.io.File

/**
 * New Agent's "Recent" cards (#2134) on the page, over the real DataStore-backed store: two starts through the form, a
 * fresh form shows them and a tap fills it without starting; then every style in light and dark, the cards as opened, after
 * a tap, a card's long-press menu, and a to-do's "Start a new one" prefilled from the same memory. PNGs go to
 * `build/recent-setups-shots/` (or `-Dpersona.shots=<dir>`). Machines, folders and models are made up.
 */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "zh-w400dp-h2000dp-xhdpi")
class NewAgentRecentShotsTest {
    @get:Rule val rule = createAndroidComposeRule<ComponentActivity>()

    private fun out(): File = File(System.getProperty("persona.shots") ?: "build/recent-setups-shots").also { it.mkdirs() }

    private val machines = listOf(
        Machine("mac-a", "MacBook", online = true, home = "/Users/me"),
        Machine("work-b", "工作站", online = true),
        Machine("home-c", "家里的 Mac", online = false),
    )
    private val persona = RecentSetup("mac-a", "/code/persona", "", "opus", "claude", "plan", "high", "2026-11-09T03:00:00Z")
    private val notes = RecentSetup("work-b", "/code/notes-app", "claude", "sonnet", "claude", "bypassPermissions", "xhigh", "2026-11-09T02:00:00Z")
    private val pipeline = RecentSetup("home-c", "/code/data-pipeline", "codex", "gpt-5.5", "codex", at = "2026-11-09T01:00:00Z")

    private class MemoryDataStore : DataStore<Preferences> {
        override val data = MutableStateFlow<Preferences>(emptyPreferences())
        override suspend fun updateData(transform: suspend (Preferences) -> Preferences): Preferences =
            transform(data.value).also { data.value = it }
    }

    private class Service : AgentCreationService {
        override val scope = "hub/pairing"
        val requests = mutableListOf<AgentStartRequest>()
        private val claude = LauncherModels("claude", listOf(
            AgentModel("default", "默认（推荐）", isDefault = true, efforts = listOf("low", "medium", "high", "xhigh")),
            AgentModel("opus", "Opus", efforts = listOf("low", "medium", "high", "xhigh")),
            AgentModel("sonnet", "Sonnet", efforts = listOf("low", "medium", "high", "xhigh")),
        ))
        private val codex = LauncherModels("codex", listOf(AgentModel("gpt-5.5", "GPT-5.5", isDefault = true)))
        override suspend fun launchers(machineId: String) =
            listOf(AgentLauncher("claude", model = "opus"), AgentLauncher("codex", agentType = "codex", model = "gpt-5.5"))
        override suspend fun directories(machineId: String, path: String) =
            AgentDirectories(path.ifEmpty { "/code" }, recent = listOf(AgentRecentDirectory("/code/persona"), AgentRecentDirectory("/code/notes-app")))
        override suspend fun start(request: AgentStartRequest, operationId: String): AgentStartResult {
            requests += request
            return AgentStartResult.Created(Session(machineId = request.machineId, sessionId = "s${requests.size}"))
        }
        override suspend fun operation(operationId: String): AgentStartResult = AgentStartResult.Unknown
        override suspend fun newAgentModel() = ModelPurposeDetail(ModelPurpose("agent.new", effective = ModelBinding("launcher.claude", "")),
            listOf(ModelProvider("launcher.claude", "claude")))
        override suspend fun launcherModels(machineId: String, launcher: String, refresh: Boolean) = if (launcher == "codex") codex else claude
    }

    private fun preferences() = AgentCreationPreferences(RuntimeEnvironment.getApplication(), MemoryDataStore())

    @Test fun twoStartsThenAFreshFormShowsThemAndATapFillsItWithoutStarting() {
        val store = preferences()
        val service = Service()
        val flow = MutableStateFlow(machines)
        val first = NewAgentViewModel(service, store, flow)
        var shown by mutableStateOf(first)
        rule.setContent {
            PersonaTheme(darkTheme = false, reduceMotion = true, style = AppStyle.Glass) {
                NewAgentPage(shown, machines, online = true, onBack = {})
            }
        }
        rule.waitForIdle()
        assertEquals("No setups yet: no section", 0, rule.onAllNodesWithText("最近").fetchSemanticsNodes().size)
        rule.onNodeWithText("开始对话").performClick()
        rule.waitForIdle()
        runBlocking { first.acknowledge() }
        rule.runOnIdle { first.selection(first.state.value.selection.copy(cwd = "/code/notes-app", launcher = "codex")) }
        rule.runOnIdle { first.prompt("整理一下日志") }
        rule.onNodeWithText("开始对话").performClick()
        rule.waitForIdle()
        runBlocking { first.acknowledge() }
        assertEquals(2, service.requests.size)

        val reopened = NewAgentViewModel(service, store, flow)
        rule.runOnIdle { shown = reopened }
        rule.waitForIdle()
        rule.onNodeWithText("最近").assertExists()
        val kept = reopened.state.value.recentSetups
        assertEquals(listOf("/code/notes-app", "/code/persona"), kept.map { it.cwd })
        assertEquals(listOf("codex", "claude"), kept.map { it.agentType })
        rule.onNodeWithText("persona · MacBook").assertExists()
        rule.onNodeWithText("notes-app · MacBook").performClick()
        rule.waitForIdle()
        reopened.state.value.let { state ->
            assertEquals("/code/notes-app", state.selection.cwd)
            assertEquals("codex", state.selection.launcher)
            assertEquals(kept[0], state.pickedSetup)
            assertEquals(1, state.focusPrompt)
        }
        assertEquals("A card never starts", 2, service.requests.size)
    }

    @Test fun aTabStartedInAWorkspaceShowsUpInRecent() {
        val store = preferences()
        runBlocking { listOf(pipeline, notes, persona).forEach { setup -> store.updateRecentSetups("hub/pairing") { RecentSetups.record(it, setup) } } }
        val service = Service()
        val flow = MutableStateFlow(machines)
        val inSpace = NewAgentViewModel(service, store, flow)
        var shown by mutableStateOf(inSpace)
        rule.setContent {
            PersonaTheme(darkTheme = false, reduceMotion = true, style = AppStyle.Glass) {
                Box(Modifier.testTag("shot").width(400.dp).height(2000.dp).background(PersonaTheme.palette.background)) {
                    NewAgentPage(shown, machines, online = true, onBack = {})
                }
            }
        }
        rule.runOnIdle { inSpace.startIn(AgentSpacePreset("work-b", "w1", "/code/billing", "billing")) }
        rule.waitForIdle()
        rule.runOnIdle { inSpace.prompt("修一下发票导出") }
        rule.onNodeWithText("开始对话").performClick()
        rule.waitForIdle()
        runBlocking { inSpace.acknowledge() }
        assertEquals("w1", service.requests.single().workspaceId)

        val reopened = NewAgentViewModel(service, store, flow)
        rule.runOnIdle { shown = reopened }
        rule.waitForIdle()
        assertEquals(listOf("/code/billing", "/code/persona", "/code/notes-app"), reopened.state.value.recentSetups.take(3).map { it.cwd })
        rule.onNodeWithText("billing · 工作站").assertExists()
        val bitmap = rule.onNodeWithTag("shot").captureToImage().asAndroidBitmap()
        File(out(), "recent-from-workspace-glass-light.png").outputStream().use { bitmap.compress(Bitmap.CompressFormat.PNG, 100, it) }
    }

    private enum class Scene { Cards, Picked, Menu, HandOff }

    @Test fun everyStyleLightAndDark() {
        val store = preferences()
        runBlocking {
            store.saveDefaults("hub/pairing", "work-b", AgentDefaults("/code/notes-app", "claude", "default", ""))
            store.saveDefaults("hub/pairing", "mac-a", AgentDefaults("/code/persona", "", "plan", "high"))
            listOf(pipeline, notes, persona).forEach { setup -> store.updateRecentSetups("hub/pairing") { RecentSetups.record(it, setup) } }
        }
        val service = Service()
        val todo = Todo("t1", title = "发登录修复", notes = "先跑测试再提 PR。")
        var style by mutableStateOf(AppStyle.Glass)
        var dark by mutableStateOf(false)
        var scene by mutableStateOf(Scene.Cards)
        var model by mutableStateOf(NewAgentViewModel(service, store, MutableStateFlow(machines)))
        rule.setContent {
            PersonaTheme(darkTheme = dark, reduceMotion = true, style = style) {
                Box(Modifier.testTag("shot").width(400.dp).height(2000.dp).background(PersonaTheme.palette.background)) {
                    if (scene == Scene.HandOff) TodoHandOffSheet(todo, machines, emptyList(), onDismiss = {}, onHandOffNew = { _, _, _, _, _ -> },
                        onHandOffExisting = { _, _ -> }, store = store, scope = service.scope)
                    else NewAgentPage(model, machines, online = true, onBack = {})
                }
            }
        }
        for (s in AppStyle.entries) for (d in listOf(false, true)) for (sc in Scene.entries) {
            rule.runOnIdle {
                style = s; dark = d; scene = sc
                if (sc == Scene.Cards) model = NewAgentViewModel(service, store, MutableStateFlow(machines))
            }
            rule.waitForIdle()
            when (sc) {
                Scene.Cards -> {
                    listOf("persona", "notes-app", "data-pipeline", "完全访问", "离线", "跟随模型配置").forEach {
                        assertTrue("$s ${if (d) "dark" else "light"} shows $it",
                            rule.onAllNodesWithText(it, substring = true, useUnmergedTree = true).fetchSemanticsNodes().isNotEmpty())
                    }
                    // The offline card picks nothing.
                    rule.onNodeWithText("data-pipeline · 家里的 Mac").performClick()
                    rule.waitForIdle()
                    assertEquals(null, model.state.value.pickedSetup)
                }
                Scene.Picked -> {
                    rule.onNodeWithText("notes-app · 工作站").performClick()
                    rule.waitForIdle()
                    assertEquals(AgentDefaults("/code/notes-app", "claude", "bypassPermissions", "xhigh"), model.state.value.selection)
                    assertEquals("sonnet", model.state.value.sentModel)
                }
                Scene.Menu -> {
                    // An offline card picks nothing but can still be removed.
                    rule.onNodeWithText("data-pipeline · 家里的 Mac").performTouchInput { longClick() }
                    rule.waitForIdle()
                    rule.onNodeWithText("从最近中移除").assertExists()
                    rule.onNodeWithText("清除最近配置").assertExists()
                }
                Scene.HandOff -> {
                    rule.onNodeWithText("/code/persona", useUnmergedTree = true).assertExists()
                    rule.onNodeWithText("计划模式 (plan)", useUnmergedTree = true).assertExists()
                }
            }
            val node = if (sc == Scene.Menu || sc == Scene.HandOff) rule.onNode(isDialog()) else rule.onNodeWithTag("shot")
            val bitmap = node.captureToImage().asAndroidBitmap()
            File(out(), "recent-${sc.name.lowercase()}-${s.name.lowercase()}-${if (d) "dark" else "light"}.png").outputStream()
                .use { bitmap.compress(Bitmap.CompressFormat.PNG, 100, it) }
        }
    }

    @Config(sdk = [35], qualifiers = "en-w400dp-h2000dp-xhdpi")
    @Test fun english() {
        val store = preferences()
        runBlocking {
            store.saveDefaults("hub/pairing", "mac-a", AgentDefaults("/code/persona", "", "plan", "high"))
            listOf(pipeline, notes, persona).forEach { setup -> store.updateRecentSetups("hub/pairing") { RecentSetups.record(it, setup) } }
        }
        val english = machines.map { m -> m.copy(name = mapOf("work-b" to "Workstation", "home-c" to "Home Mac")[m.id] ?: m.name) }
        val model = NewAgentViewModel(Service(), store, MutableStateFlow(english))
        rule.setContent {
            PersonaTheme(darkTheme = false, reduceMotion = true, style = AppStyle.Glass) {
                Box(Modifier.testTag("shot").width(400.dp).height(2000.dp).background(PersonaTheme.palette.background)) {
                    NewAgentPage(model, english, online = true, onBack = {})
                }
            }
        }
        rule.waitForIdle()
        listOf("Recent", "Full access", "Offline", "Follow model settings").forEach {
            assertTrue("shows $it", rule.onAllNodesWithText(it, substring = true, useUnmergedTree = true).fetchSemanticsNodes().isNotEmpty())
        }
        val bitmap = rule.onNodeWithTag("shot").captureToImage().asAndroidBitmap()
        File(out(), "recent-cards-en.png").outputStream().use { bitmap.compress(Bitmap.CompressFormat.PNG, 100, it) }

        rule.onNodeWithText("notes-app · Workstation").performTouchInput { longClick() }
        rule.waitForIdle()
        rule.onNodeWithText("Remove from Recent").performClick()
        rule.waitForIdle()
        assertEquals(listOf(persona, pipeline), model.state.value.recentSetups)
        assertEquals(0, rule.onAllNodesWithText("notes-app · Workstation").fetchSemanticsNodes().size)
        rule.onNodeWithText("persona · MacBook").performTouchInput { longClick() }
        rule.waitForIdle()
        rule.onNodeWithText("Clear Recent Setups").performClick()
        rule.waitForIdle()
        assertTrue(runBlocking { store.recentSetups("hub/pairing") }.isEmpty())
        assertEquals("Nothing to show: no section", 0, rule.onAllNodesWithText("Recent").fetchSemanticsNodes().size)
    }
}
