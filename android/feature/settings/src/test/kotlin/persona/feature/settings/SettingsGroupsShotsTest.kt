package persona.feature.settings

import android.graphics.Bitmap
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.semantics.SemanticsActions
import androidx.compose.ui.test.hasClickAction
import androidx.compose.ui.test.hasText
import androidx.compose.ui.test.hasSetTextAction
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onAllNodesWithText
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.graphics.asAndroidBitmap
import androidx.compose.ui.test.captureToImage
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.performSemanticsAction
import androidx.compose.ui.test.performTextInput
import androidx.compose.ui.test.performTextClearance
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.unit.Density
import androidx.test.ext.junit.runners.AndroidJUnit4
import kotlinx.coroutines.flow.MutableSharedFlow
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.serialization.json.JsonObject
import org.junit.After
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.core.contracts.Machine
import persona.core.contracts.ServerEvent
import persona.core.contracts.Session
import persona.core.contracts.api.*
import persona.core.designsystem.AppStyle
import persona.core.designsystem.LocalAssistantWaiting
import persona.core.designsystem.LocalManagerRoster
import persona.core.designsystem.PersonaTheme
import java.io.File

/**
 * Settings in three groups (docs/assistant/managers.md "Clients"): the current assistant's settings (its monogram, name and
 * Switch, then its rows and footnote), this device's, the hub's (with their footnote), in every style, light and dark,
 * in English and Japanese and at the largest text size; Switch opens the sidebar's roster. With the mock hub's roster
 * (`scripts/mock/mock_hub.py`). PNGs go to `build/settings-groups-shots/`.
 */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "zh-w400dp-h2800dp-xhdpi")
class SettingsGroupsShotsTest {
    @get:Rule val rule = createComposeRule()
    @After fun reset() = AssistantManagerChoice.reset()

    private val roster = ManagerRoster(ManagersSample, "assistant")

    private val extensions = object : ExtensionApiStub() {
        override suspend fun listing() = ExtensionListing(listOf(
            HubExtension("example-notes", "示例笔记", "connectors", "running", source = "installed", enabled = true),
            HubExtension("example-mail", "工作邮箱", "connectors", "running", source = "installed", enabled = true, manager = "m-3fa21c"),
        ), canInstall = true)
        override suspend fun extensions() = listing().extensions
    }

    private val hubUpdates = object : HubUpdates {
        override suspend fun status() = HubUpdateStatus(version = "1.0.0")
        override suspend fun check() = status()
        override suspend fun start(now: Boolean) = status()
        override suspend fun settings(auto: Boolean?, noticeSeen: Boolean?) = status()
        override suspend fun history() = emptyList<HubUpdateRecord>()
    }

    private fun services() = SettingsServices(
        pairing = object : PairingService {
            override val isPaired = MutableStateFlow(true)
            override suspend fun pair(request: PairingRequest) = error("not used")
            override suspend fun unpair() = error("not used")
        },
        connection = object : HubConnection {
            override val state = MutableStateFlow(ConnectionState.Online)
            override val events = MutableSharedFlow<ServerEvent>()
            override fun reconnect() = error("not used")
            override fun send(message: JsonObject) = false
        },
        sessions = object : SessionsRepository {
            override val machines = MutableStateFlow(listOf(Machine("server", "server", online = true),
                Machine("mac", "MacBook", online = true, manager = "m-3fa21c"), Machine("nas", "NAS", online = false, manager = "m-8b04d2")))
            override val sessions = MutableStateFlow(emptyList<Session>())
            override suspend fun refresh() {}
        },
        updates = object : AppUpdates { override suspend fun check(): AppUpdate? = null },
        hubAddress = "<hub address>",
        managers = FakeManagers(),
        extensions = extensions,
        hubs = HubGroups.None,
        hubUpdates = hubUpdates,
    )

    private fun out(): File = File(System.getProperty("persona.shots") ?: "build/settings-groups-shots").also { it.mkdirs() }

    /** Every window drawn where it is (Switch's roster is a menu, a window of its own), cut to the page. */
    @Suppress("UNCHECKED_CAST")
    private fun capture(name: String) {
        rule.waitForIdle()
        val global = Class.forName("android.view.WindowManagerGlobal")
        val instance = global.getMethod("getInstance").invoke(null)
        val views = global.getDeclaredField("mViews").apply { isAccessible = true }.get(instance) as List<android.view.View>
        val screen = Bitmap.createBitmap(views.first().width, views.first().height, Bitmap.Config.ARGB_8888)
        val canvas = android.graphics.Canvas(screen)
        views.forEach { view ->
            val at = IntArray(2).also { view.getLocationOnScreen(it) }
            canvas.save(); canvas.translate(at[0].toFloat(), at[1].toFloat()); view.draw(canvas); canvas.restore()
        }
        File(out(), "$name.png").outputStream().use { screen.compress(Bitmap.CompressFormat.PNG, 100, it) }
    }

    private fun top(text: String) = rule.onAllNodesWithText(text)[0].fetchSemanticsNode().boundsInRoot.top

    private fun show(style: () -> AppStyle = { AppStyle.Glass }, dark: () -> Boolean = { false }, roster: ManagerRoster? = this.roster,
                     page: String = "settings") {
        val services = services()
        rule.setContent {
            PersonaTheme(darkTheme = dark(), reduceMotion = true, style = style()) {
                val density = LocalDensity.current
                val fontScale = androidx.compose.ui.platform.LocalConfiguration.current.fontScale
                CompositionLocalProvider(LocalManagerRoster provides roster, LocalAssistantWaiting provides mapOf("m-3fa21c" to 3),
                    LocalDensity provides Density(density.density, fontScale)) {
                    Box(Modifier.testTag("shot").fillMaxSize().background(PersonaTheme.palette.background)) {
                        SettingsScreen(services, initialPage = page, onNavigate = {})
                    }
                }
            }
        }
        rule.waitForIdle()
    }

    @Test fun threeGroupsInTheirOrderInEveryStyle() {
        var style by mutableStateOf(AppStyle.Glass)
        var dark by mutableStateOf(false)
        AssistantManagerChoice.choose("hub", null, "assistant")
        show({ style }, { dark })
        // 1. The current assistant's: its head, its rows in their order, its footnote.
        rule.onNodeWithText("助手的设置").assertExists()
        rule.onNodeWithText("只显示「助手」自己的内容。要改别的助手，先切换过去。").assertExists()
        val first = listOf("模型", "助手资料", "连接器", "扩展", "Agent")
        first.zipWithNext().forEach { (a, b) -> assertTrue("$a before $b", top(a) < top(b)) }
        // 2. This device's, then 3. the hub's, with the footnote that says neither is an assistant's.
        assertTrue(top("Agent") < top("这台设备"))
        val device = listOf("这台设备", "外观", "通知与后台", "翻译", "存储空间", "关于与更新")
        device.zipWithNext().forEach { (a, b) -> assertTrue("$a before $b", top(a) < top(b)) }
        assertTrue(top("存储空间") < top("Hub"))
        val hub = listOf("Hub", "连接", "Hub 连接", "Hub 版本", "助手", "形象", "设备")
        hub.zipWithNext().forEach { (a, b) -> assertTrue("$a before $b", top(a) < top(b)) }
        rule.onNodeWithText("这两组和当前是哪个助手无关。").assertExists()
        for (s in AppStyle.entries) for (d in listOf(false, true)) {
            style = s; dark = d
            capture("settings-${s.key}-${if (d) "dark" else "light"}")
        }
    }

    @Config(qualifiers = "zh-w400dp-h950dp-xhdpi")
    @Test fun phoneHomeAndSearchInEveryStyleAndScheme() {
        var style by mutableStateOf(AppStyle.Glass)
        var dark by mutableStateOf(false)
        AssistantManagerChoice.choose("hub", null, "assistant")
        show({ style }, { dark })
        fun image(name: String) {
            rule.waitForIdle()
            val bitmap = rule.onNodeWithTag("shot").captureToImage().asAndroidBitmap()
            File(out(), "$name.png").outputStream().use { bitmap.compress(Bitmap.CompressFormat.PNG, 100, it) }
        }
        for (s in AppStyle.entries) for (d in listOf(false, true)) {
            style = s; dark = d
            rule.onNode(hasSetTextAction()).performTextClearance()
            image("settings-phone-${s.key}-${if (d) "dark" else "light"}")
            rule.onNode(hasSetTextAction()).performTextInput("Codex")
            rule.onNodeWithText("Agent").assertExists()
            assertEquals(0, rule.onAllNodesWithText("这台设备").fetchSemanticsNodes().size)
            image("settings-search-${s.key}-${if (d) "dark" else "light"}")
            rule.onNode(hasSetTextAction()).performTextClearance()
            rule.onNode(hasSetTextAction()).performTextInput("不存在的设置")
            rule.onNodeWithText("没有找到相关设置").assertExists()
            image("settings-search-empty-${s.key}-${if (d) "dark" else "light"}")
        }
    }

    @Test fun formerFullNamesSearchTheirGroupedHomeEntries() {
        show()
        for ((query, entry) in listOf("模型供应商" to "模型", "Claude Code 设置" to "Agent", "后台保活" to "通知与后台",
            "悬浮胶囊" to "通知与后台", "检查更新" to "关于与更新", "连接诊断" to "连接")) {
            rule.onNode(hasSetTextAction()).performTextClearance()
            rule.onNode(hasSetTextAction()).performTextInput(query)
            rule.onNodeWithText(entry).assertExists()
        }
    }

    /** Switch opens the sidebar's roster: picking another assistant makes the group that one's. */
    @Test fun switchOpensTheRosterAndTheGroupFollows() {
        AssistantManagerChoice.choose("hub", null, "assistant")
        show()
        rule.onNode(hasText("切换") and hasClickAction()).performSemanticsAction(SemanticsActions.OnClick)
        rule.onNodeWithText("新建助手").assertExists()
        rule.onNodeWithText("管理助手…").assertExists()
        rule.onNodeWithText("3").assertExists() // 工作's badge, from the shell's count
        capture("settings-switch-open-glass-light")
        rule.onNode(hasText("工作") and hasClickAction()).performSemanticsAction(SemanticsActions.OnClick)
        rule.waitForIdle()
        assertEquals("m-3fa21c", AssistantManagerChoice.current.value.shown)
        rule.onNodeWithText("工作的设置").assertExists()
        rule.onNodeWithText("只显示「工作」自己的内容。要改别的助手，先切换过去。").assertExists()
        assertEquals(0, rule.onAllNodesWithText("新建助手").fetchSemanticsNodes().size)
        capture("settings-switched-glass-light")
    }

    /** A switch while one of its pages is open in place goes back to the list: no page shows one assistant's under another's. */
    @Test fun aSwitchWhileItsPageIsOpenGoesBackToTheList() {
        AssistantManagerChoice.choose("hub", null, "assistant")
        val services = services()
        rule.setContent {
            PersonaTheme(reduceMotion = true) {
                CompositionLocalProvider(LocalManagerRoster provides roster) {
                    Box(Modifier.fillMaxSize()) { SettingsScreen(services) }
                }
            }
        }
        rule.onNode(hasText("Agent") and hasClickAction()).performSemanticsAction(SemanticsActions.OnClick)
        rule.onNode(hasText("Codex 设置") and hasClickAction()).performSemanticsAction(SemanticsActions.OnClick)
        rule.waitForIdle()
        assertEquals(0, rule.onAllNodesWithText("助手的设置").fetchSemanticsNodes().size)
        rule.runOnIdle { AssistantManagerChoice.pick("m-3fa21c") }
        rule.waitForIdle()
        rule.onNodeWithText("工作的设置").assertExists()
        // A page of this device stays open across a switch.
        rule.onNode(hasText("存储空间") and hasClickAction()).performSemanticsAction(SemanticsActions.OnClick)
        rule.waitForIdle()
        rule.runOnIdle { AssistantManagerChoice.pick(null) }
        rule.waitForIdle()
        assertEquals(0, rule.onAllNodesWithText("助手的设置").fetchSemanticsNodes().size)
    }

    /** One assistant: the group still names it, with its monogram, Switch (its roster: it, New assistant, Manage assistants…) and footer. */
    @Test fun oneAssistantKeepsItsGroupAndSwitch() {
        AssistantManagerChoice.choose("hub", null, "assistant")
        show(roster = ManagerRoster(ManagersSample.filter { it.id == "assistant" }, "assistant"))
        rule.onNodeWithText("助手的设置").assertExists()
        rule.onNodeWithText("只显示「助手」自己的内容。要改别的助手，先切换过去。").assertExists()
        capture("settings-one-glass-light")
        rule.onNode(hasText("切换") and hasClickAction()).performSemanticsAction(SemanticsActions.OnClick)
        rule.onNodeWithText("新建助手").assertExists()
        rule.onNodeWithText("管理助手…").assertExists()
        capture("settings-one-switch-open-glass-light")
    }

    /** No roster (an older hub): no assistant to name, so a plain heading, no Switch and no footer. */
    @Test fun withoutARosterThereIsNoAssistantToName() {
        show(roster = null)
        assertEquals(0, rule.onAllNodesWithText("切换").fetchSemanticsNodes().size)
        assertEquals(0, rule.onAllNodesWithText("的设置", substring = true).fetchSemanticsNodes().size)
        assertEquals(0, rule.onAllNodesWithText("只显示", substring = true).fetchSemanticsNodes().size)
        assertTrue(top("Persona") < top("模型"))
        rule.onNodeWithText("这两组和当前是哪个助手无关。").assertExists()
    }

    /** The current assistant offline: its pages say so, as its inbox does, instead of looking empty. */
    @Test fun anOfflineAssistantsPagesSayItIsOffline() {
        AssistantManagerChoice.choose("hub", "m-8b04d2", "assistant")
        var dark by mutableStateOf(false)
        show(dark = { dark }, page = "connectors")
        rule.onNodeWithText("家里 离线").assertExists()
        assertEquals(0, rule.onAllNodesWithText("这台手机").fetchSemanticsNodes().size)
        // A page's own bar is blurred glass, which only the compose capture draws (no popup on this page).
        for (d in listOf(false, true)) {
            dark = d
            rule.waitForIdle()
            val image = rule.onNodeWithTag("shot").captureToImage().asAndroidBitmap()
            File(out(), "settings-offline-connectors-glass-${if (d) "dark" else "light"}.png").outputStream().use { image.compress(Bitmap.CompressFormat.PNG, 100, it) }
        }
    }

    @Config(qualifiers = "en-w400dp-h2800dp-xhdpi")
    @Test fun inEnglish() {
        AssistantManagerChoice.choose("hub", null, "assistant")
        show()
        rule.onNodeWithText("助手's settings").assertExists()
        rule.onNodeWithText("Switch").assertExists()
        rule.onNodeWithText("This device").assertExists()
        rule.onNodeWithText("These two groups are the same whichever assistant is current.").assertExists()
        capture("settings-glass-light-en")
    }

    @Config(qualifiers = "ja-w400dp-h2800dp-xhdpi")
    @Test fun inJapanese() {
        AssistantManagerChoice.choose("hub", null, "assistant")
        show()
        rule.onNodeWithText("助手の設定").assertExists()
        rule.onNodeWithText("切り替え").assertExists()
        rule.onNodeWithText("このデバイス").assertExists()
        assertEquals(2, rule.onAllNodesWithText("ハブ").fetchSemanticsNodes().size) // the group's heading and its hubs row
        capture("settings-glass-light-ja")
    }

    @Config(qualifiers = "zh-w400dp-h4000dp-xhdpi", fontScale = 2f) // tall enough for the whole page at 200 %
    @Test fun atTheLargestTextSize() {
        var style by mutableStateOf(AppStyle.Glass)
        var dark by mutableStateOf(false)
        AssistantManagerChoice.choose("hub", null, "assistant")
        show({ style }, { dark })
        rule.onNodeWithText("助手的设置").assertExists()
        for (s in AppStyle.entries) for (d in listOf(false, true)) {
            style = s; dark = d
            rule.onNode(hasSetTextAction()).performTextClearance()
            capture("settings-${s.key}-${if (d) "dark" else "light"}-large")
            rule.onNode(hasSetTextAction()).performTextInput("Codex")
            capture("settings-search-${s.key}-${if (d) "dark" else "light"}-large")
        }
    }
}
