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
import androidx.compose.ui.test.junit4.createAndroidComposeRule
import androidx.compose.ui.test.onAllNodesWithContentDescription
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.unit.dp
import androidx.datastore.core.DataStore
import androidx.datastore.preferences.core.Preferences
import androidx.datastore.preferences.core.emptyPreferences
import androidx.navigation3.runtime.NavKey
import androidx.test.ext.junit.runners.AndroidJUnit4
import kotlinx.coroutines.flow.MutableSharedFlow
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.serialization.json.JsonObject
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.core.contracts.*
import persona.core.contracts.api.*
import persona.core.designsystem.*
import persona.core.navigation.AppTab
import persona.core.navigation.Navigator
import java.io.File
import java.time.Instant

/**
 * Each agent's mark before its state on its row (#6390): Claude, Codex and TRAE by their own marks, an agent with none by
 * its letter, the command line's with a </> badge; in every style and appearance, the detailed and the compact list.
 * Written as PNGs to `build/agent-mark-shots/` (or `-Dpersona.shots=<dir>`).
 */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "zh-w400dp-h1400dp-xhdpi")
class AgentMarkShotsTest {
    @get:Rule val rule = createAndroidComposeRule<ComponentActivity>()

    private fun out(): File = File(System.getProperty("persona.shots") ?: "build/agent-mark-shots").also { it.mkdirs() }

    private val now = Instant.now()
    private fun ago(minutes: Long) = now.minusSeconds(minutes * 60).toString()
    private val machines = listOf(Machine("mac", "MacBook Pro", online = true, engine = "herdr"))
    private val sessions = listOf(
        Session("mac", "s1", title = "整理发布说明", cwd = "/code/app", state = "working", spaceId = "w1", spaceName = "app",
            slotName = "1", lastText = "在写中文版", updatedAt = ago(1), paneId = "w1:p1", agent = "claude", agentSurface = "cli"),
        Session("mac", "s2", title = "修复订单列表的 N+1 查询", cwd = "/code/app", state = "unread", spaceId = "w1", spaceName = "app",
            slotName = "2", lastText = "改成一次 JOIN 了", updatedAt = ago(4), paneId = "w1:p2", agent = "codex", agentSurface = "desktop"),
        Session("mac", "s3", title = "TRAE CLI 的一个会话", cwd = "/code/app", state = "needs_you", spaceId = "w1", spaceName = "app",
            slotName = "3", lastText = "要跑测试吗？", updatedAt = ago(6), paneId = "w1:p3", agent = "traex", agentSurface = "cli"),
        Session("mac", "s4", title = "amp 的一个会话", cwd = "/code/app", state = "unread", spaceId = "w1", spaceName = "app",
            slotName = "4", lastText = "好的", updatedAt = ago(30), paneId = "w1:p4", agent = "amp"),
    ) + listOf("Grok" to "grok", "Antigravity CLI" to "agy", "OpenCode" to "opencode").mapIndexed { index, (name, agent) ->
        Session("mac", "mark-$agent", title = name, cwd = "/code/app", state = "unread", spaceId = "w1", spaceName = "app",
            agent = agent, agentLabelHint = name, updatedAt = ago(8L + index))
    }

    private class Connection : HubConnection {
        override val state = MutableStateFlow(ConnectionState.Online)
        override val events = MutableSharedFlow<ServerEvent>()
        override val lastHello: Hello get() = Hello(serverVersion = "1.0.0", protocol = 1)
        override fun reconnect() = Unit
        override fun send(message: JsonObject) = true
    }
    private class Interactions : InteractionCenter {
        override val pending = MutableStateFlow<List<Interaction>>(emptyList())
        override suspend fun refresh() = Unit
        override suspend fun resolve(id: String, resolution: Resolution) = Unit
    }
    private class Sender : MessageSender {
        override val outbox = MutableStateFlow<List<OutboxMessage>>(emptyList())
        override suspend fun send(session: SessionKey, text: String, clientMessageId: String, clearDraft: String?) = Unit
    }
    private class Repository(sessions: List<Session>, machines: List<Machine>) : SessionsRepository {
        override val sessions = MutableStateFlow(sessions)
        override val machines = MutableStateFlow(machines)
        override suspend fun refresh() = Unit
    }
    private object Nowhere : Navigator {
        override fun navigate(key: NavKey) = Unit
        override fun back() = Unit
        override fun selectTab(tab: AppTab) = Unit
    }

    private fun preferences(): AgentsPreferences {
        val store = object : DataStore<Preferences> {
            override val data = MutableStateFlow<Preferences>(emptyPreferences())
            override suspend fun updateData(transform: suspend (Preferences) -> Preferences): Preferences =
                transform(data.value).also { data.value = it }
        }
        val hub = object : HubPreferences {
            override val preferences = MutableStateFlow(HubPreferencesSnapshot())
            override suspend fun refresh() = Unit
            override suspend fun togglePin(path: String, space: Boolean) = Unit
            override suspend fun toggleMute(path: String, space: Boolean) = Unit
            override suspend fun markSeen(session: SessionKey, version: String) = Unit
        }
        return AgentsPreferences(store, hub)
    }

    @Test fun marksBeforeTheState() {
        val model = AgentsViewModel(Repository(sessions, machines), Interactions(), Connection(), Sender(), preferences())
        var style by mutableStateOf(AppStyle.Glass)
        var dark by mutableStateOf(false)
        rule.setContent {
            PersonaTheme(darkTheme = dark, reduceMotion = true, style = style) {
                Box(Modifier.testTag("shot").width(400.dp).height(1100.dp).background(PersonaTheme.palette.background)) {
                    AgentsScreen(model, Nowhere, AgentsMetadata())
                }
            }
        }
        rule.waitForIdle()
        for (compact in listOf(false, true)) {
            if (compact) { rule.runOnIdle { model.compact() }; rule.waitForIdle() }
            for (s in AppStyle.entries) for (d in listOf(false, true)) {
                rule.runOnIdle { style = s; dark = d }
                rule.waitForIdle()
                val list = rule.onNodeWithTag("shot").captureToImage().asAndroidBitmap()
                val name = "marks-${if (compact) "compact" else "list"}-${s.name.lowercase()}-${if (d) "dark" else "light"}.png"
                File(out(), name).outputStream().use { assertTrue(list.compress(Bitmap.CompressFormat.PNG, 100, it)) }
                for (said in listOf("Claude Code（命令行版）", "Codex（桌面版）", "TRAE CLI（命令行版）", "amp", "Grok", "Antigravity CLI", "OpenCode")) {
                    assertEquals("$said in $s ${if (d) "dark" else "light"} ${if (compact) "compact" else "list"}", 1,
                        rule.onAllNodesWithContentDescription(said, useUnmergedTree = true).fetchSemanticsNodes().size)
                }
            }
        }
    }
}
