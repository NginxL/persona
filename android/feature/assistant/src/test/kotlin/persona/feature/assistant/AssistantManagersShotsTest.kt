package persona.feature.assistant

import android.content.Context
import android.graphics.Bitmap
import androidx.activity.ComponentActivity
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.width
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.asAndroidBitmap
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.test.captureToImage
import androidx.compose.ui.test.hasClickAction
import androidx.compose.ui.test.hasText
import androidx.compose.ui.test.junit4.createAndroidComposeRule
import androidx.compose.ui.test.onAllNodesWithText
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.compose.ui.unit.dp
import androidx.navigation3.runtime.NavKey
import androidx.test.core.app.ApplicationProvider
import androidx.test.ext.junit.runners.AndroidJUnit4
import kotlinx.coroutines.flow.MutableStateFlow
import org.junit.After
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.core.contracts.Session
import persona.core.contracts.SessionKey
import persona.core.contracts.api.*
import persona.core.designsystem.*
import persona.core.navigation.AppTab
import persona.core.navigation.Navigator
import java.io.File
import java.time.Instant

/**
 * The assistant page with several assistants: selection belongs to the sidebar, an offline current assistant is
 * read-only and says when it last synced, and an online one can send. With one installed assistant the product
 * header stays unchanged in every style, light and dark. Written to `build/managers-shots/assistant-*.png`.
 */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "zh-w400dp-h900dp-xhdpi")
class AssistantManagersShotsTest {
    @get:Rule val rule = createAndroidComposeRule<ComponentActivity>()

    @After fun reset() = AssistantManagerChoice.reset()

    private val roster = ManagerRoster(listOf(
        HubManager("assistant", "助手", online = true, isDefault = true, builtin = true),
        HubManager("m-3fa21c", "工作", online = true),
        HubManager("m-8b04d2", "家里", online = false, lastSeen = Instant.now().minusSeconds(3 * 3600).toString()),
        HubManager("m-c1e9f0", "测试", installing = "2026-11-05T06:07:00Z"),
    ), "assistant")
    private val one = ManagerRoster(listOf(roster.managers.first()), "assistant")
    private val navigator = object : Navigator {
        override fun navigate(key: NavKey) = Unit
        override fun back() = Unit
        override fun selectTab(tab: AppTab) = Unit
    }

    private class Hub : AssistantRepository, MessageSender {
        override val assistant = MutableStateFlow(AssistantSnapshot())
        fun show(manager: String?) {
            val main = SessionKey("hub", "main-${manager ?: "assistant"}")
            assistant.value = AssistantSnapshot(main,
                listOf(Session(machineId = main.machineId, sessionId = main.sessionId,
                    title = "主对话", assistant = true, manager = manager)), manager)
        }
        override val outbox = MutableStateFlow(emptyList<OutboxMessage>())
        val sent = mutableListOf<SessionKey>()
        override suspend fun refresh() = show(AssistantManagerChoice.current.value.manager)
        override suspend fun send(session: SessionKey, text: String, clientMessageId: String, clearDraft: String?) { sent += session }
    }

    private val res get() = ApplicationProvider.getApplicationContext<Context>().resources

    @Test fun switchingAndAnOfflineManagersComposer() {
        val hub = Hub()
        var style by mutableStateOf(AppStyle.Glass)
        var dark by mutableStateOf(false)
        var shown by mutableStateOf(roster)
        AssistantManagerChoice.choose("hub", "m-8b04d2", "assistant")
        rule.setContent {
            LaunchedEffect(hub) {
                AssistantManagerChoice.current.collect { hub.show(it.manager) }
            }
            PersonaTheme(darkTheme = dark, reduceMotion = true, style = style) {
                // The page draws its own theme from the system's night mode: dark shots set it.
                val base = androidx.compose.ui.platform.LocalConfiguration.current
                val night = android.content.res.Configuration(base).apply {
                    uiMode = (uiMode and android.content.res.Configuration.UI_MODE_NIGHT_MASK.inv()) or
                        (if (dark) android.content.res.Configuration.UI_MODE_NIGHT_YES else android.content.res.Configuration.UI_MODE_NIGHT_NO)
                }
                CompositionLocalProvider(LocalManagerRoster provides shown, androidx.compose.ui.platform.LocalConfiguration provides night) {
                    Box(Modifier.testTag("shot").width(400.dp).height(900.dp).background(PersonaTheme.palette.background)) {
                        AssistantScreen(navigator, hub, hub, startConversation = { error("not used") }, composerService = null)
                    }
                }
            }
        }
        rule.waitForIdle()
        val offline = "家里 · 最后同步于 3 小时前"
        assertTrue(rule.onAllNodesWithText(offline).fetchSemanticsNodes().isNotEmpty())
        // Nothing is queued for it: the empty state says so instead of 「连上后继续发送」.
        assertTrue(rule.onAllNodesWithText("家里离线，能看历史，暂时不能发消息。").fetchSemanticsNodes().isNotEmpty())
        assertEquals(0, rule.onAllNodesWithText("发出的内容会先保存在本机，连上后继续发送。").fetchSemanticsNodes().size)
        // The page draws no chips: no manager's name is a button on it (the sidebar's switcher lists them).
        assertEquals(0, rule.onAllNodes(hasText("工作") and hasClickAction()).fetchSemanticsNodes().size)
        assertEquals(0, rule.onAllNodesWithText("测试").fetchSemanticsNodes().size)
        val dir = File(System.getProperty("persona.shots") ?: "build/managers-shots").also { it.mkdirs() }
        fun capture(name: String) {
            rule.waitForIdle()
            val bitmap = rule.onNodeWithTag("shot").captureToImage().asAndroidBitmap()
            File(dir, "assistant-$name.png").outputStream().use { bitmap.compress(Bitmap.CompressFormat.PNG, 100, it) }
        }
        for (s in AppStyle.entries) for (d in listOf(false, true)) {
            style = s; dark = d
            capture("offline-${s.key}-${if (d) "dark" else "light"}")
        }
        assertTrue(hub.sent.isEmpty())
        // Switching to another one: the choice moves (the requests then name it), the composer is back.
        style = AppStyle.Glass; dark = false
        AssistantManagerChoice.pick("m-3fa21c")
        rule.waitForIdle()
        assertEquals("m-3fa21c", AssistantManagerChoice.current.value.manager)
        assertEquals(0, rule.onAllNodesWithText(offline).fetchSemanticsNodes().size)
        // The manager rename reaches the same shared name source: both the top bar and composer change together.
        shown = shown.copy(managers = shown.managers.map { if (it.id == "m-3fa21c") it.copy(name = "公司") else it })
        rule.waitForIdle()
        rule.onNodeWithText("公司").assertExists()
        rule.onNodeWithText("问 公司…").assertExists()
        assertEquals(0, rule.onAllNodesWithText("问 工作…").fetchSemanticsNodes().size)
        capture("renamed-online-glass-light")
        // The default: no header at all.
        AssistantManagerChoice.pick("assistant")
        rule.waitForIdle()
        assertNull(AssistantManagerChoice.current.value.manager)
        assertNull(AssistantManagerChoice.header("hub"))
        // One installed assistant keeps the product title and no personal identity in every style.
        shown = one.copy(managers = listOf(one.managers.first().copy(name = "Personal assistant name")))
        rule.waitForIdle()
        for (s in AppStyle.entries) for (d in listOf(false, true)) {
            rule.runOnIdle { style = s; dark = d }
            assertTrue(rule.onAllNodesWithText("Persona").fetchSemanticsNodes().isNotEmpty())
            assertEquals(0, rule.onAllNodesWithText("Personal assistant name").fetchSemanticsNodes().size)
            assertEquals(0, rule.onAllNodes(hasText("工作") and hasClickAction()).fetchSemanticsNodes().size)
            capture("single-${s.key}-${if (d) "dark" else "light"}")
        }
    }

    @Test fun whatThePageKnowsOfItsManager() {
        val now = Instant.parse("2026-11-05T06:00:00Z")
        val home = roster.copy(managers = roster.managers.map { if (it.id == "m-8b04d2") it.copy(lastSeen = "2026-11-05T05:58:00Z") else it })
        // One manager, or none read yet: as before.
        assertEquals(AssistantManagers(), assistantManagers(null, AssistantManagerChoice.Choice(), res, now))
        assertEquals(AssistantManagers(), assistantManagers(one, AssistantManagerChoice.Choice("hub", null, "assistant"), res, now))
        // The default of several: nothing changes for sending.
        assertEquals(AssistantManagers(), assistantManagers(home, AssistantManagerChoice.Choice("hub", null, "assistant"), res, now))
        // Another online one: its own main conversation, sendable.
        assertEquals(AssistantManagers(other = true), assistantManagers(home, AssistantManagerChoice.Choice("hub", "m-3fa21c", "assistant"), res, now))
        // An offline one: not sendable, and why.
        val off = assistantManagers(home, AssistantManagerChoice.Choice("hub", "m-8b04d2", "assistant"), res, now)
        assertTrue(off.other)
        assertEquals("家里 · 最后同步于 2 分钟前", off.offline)
        assertEquals("家里离线，能看历史，暂时不能发消息。", off.offlineEmpty)
        assertFalse(assistantManagers(home, AssistantManagerChoice.Choice("hub", "m-3fa21c", "assistant"), res, now).offline != null)
    }
}
