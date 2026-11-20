package persona.feature.agents

import android.graphics.Bitmap
import androidx.activity.ComponentActivity
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.width
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.asAndroidBitmap
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.test.captureToImage
import androidx.compose.ui.test.junit4.createAndroidComposeRule
import androidx.compose.ui.test.onAllNodesWithText
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
import kotlinx.serialization.json.JsonPrimitive
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
 * The Agents list's parity with iOS / the web (#3038): "Lark groups" / "Terminal" quick filters, the three
 * empty states (search, quick filter, none at all) and a herdr start that failed, with its reason on the row. Every
 * style in light and dark, written to `build/parity-shots/` (or `-Dpersona.shots=<dir>`).
 */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
class AgentsParityShotsTest {
    @get:Rule val rule = createAndroidComposeRule<ComponentActivity>()

    private fun out(): File = File(System.getProperty("persona.shots") ?: "build/parity-shots").also { it.mkdirs() }

    private val now = Instant.parse("2026-11-05T09:30:00Z")
    private fun ago(minutes: Long) = now.minusSeconds(minutes * 60).toString()
    private val machines = listOf(Machine("mac", "MacBook Pro", online = true, engine = "herdr"))
    private val sessions = listOf(
        Session("mac", "s1", title = "Fix login page", cwd = "/code/app", state = "working", spaceId = "w1", spaceName = "app",
            slotName = "1", paneId = "w1:p1", lastText = "Updating the retry logic", updatedAt = ago(2)),
        Session("mac", "s2", title = "Release checklist", cwd = "/code/app", state = "unread", spaceId = "w1", spaceName = "app",
            slotName = "2", paneId = "botmux:topic-1", lastText = "Summary is ready", updatedAt = ago(8)),
        Session("mac", "s3", title = "Weekly notes", cwd = "/code/notes", state = "working", spaceId = "w2", spaceName = "notes",
            slotName = "1", paneId = "w2:p1", lastText = "Writing", updatedAt = ago(30)),
    )

    private class Connection : HubConnection {
        override val state = MutableStateFlow(ConnectionState.Online)
        override val events = MutableSharedFlow<ServerEvent>()
        override val lastHello: Hello get() = Hello(serverVersion = "1.0.0", protocol = 1)
        override fun reconnect() = Unit
        override fun send(message: JsonObject) = true
    }

    private class Interactions(list: List<Interaction>) : InteractionCenter {
        override val pending = MutableStateFlow(list)
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

    private enum class Page { List, FilterEmpty, SearchEmpty, Herdr }

    @Config(sdk = [35], qualifiers = "zh-w400dp-h1100dp-xhdpi")
    @Test fun parity() {
        var style by mutableStateOf(AppStyle.Glass)
        var dark by mutableStateOf(false)
        var page by mutableStateOf(Page.List)
        val model = AgentsViewModel(Repository(sessions, machines), Interactions(emptyList()), Connection(), Sender(), preferences())
        val failure = HerdrFailure(AgentText(R.string.agents_herdr_why_not_installed), "herdr is not installed\nHTTP 502")
        rule.setContent {
            PersonaTheme(darkTheme = dark, reduceMotion = true, style = style) {
                Box(Modifier.testTag("shot").width(400.dp).height(1100.dp).background(PersonaTheme.palette.background)) {
                    if (page == Page.Herdr) Column(Modifier.padding(Tokens.Space.l)) {
                        HerdrNotice("MacBook Pro", false, {}, failure = failure)
                    } else AgentsScreen(model, Nowhere, AgentsMetadata())
                }
            }
        }
        for (p in Page.entries) {
            rule.runOnIdle {
                page = p
                when (p) {
                    Page.List -> { model.query(""); model.filter(persona.core.domain.sort.AgentQuickFilter.All) }
                    Page.FilterEmpty -> { model.query(""); model.filter(persona.core.domain.sort.AgentQuickFilter.NeedsYou) }
                    Page.SearchEmpty -> { model.filter(persona.core.domain.sort.AgentQuickFilter.All); model.query("deploy") }
                    Page.Herdr -> Unit
                }
            }
            rule.waitForIdle()
            val expected = when (p) {
                Page.List -> listOf("飞书群", "终端")
                Page.FilterEmpty -> listOf("没有需要你的会话", "看全部")
                Page.SearchEmpty -> listOf("没有找到「deploy」")
                Page.Herdr -> listOf("没启动：这台电脑上没装 herdr")
            }
            for (s in AppStyle.entries) for (d in listOf(false, true)) {
                rule.runOnIdle { style = s; dark = d }
                rule.waitForIdle()
                expected.forEach { assertTrue("$p shows $it", rule.onAllNodesWithText(it, substring = true, useUnmergedTree = true).fetchSemanticsNodes().isNotEmpty()) }
                if (p == Page.SearchEmpty) assertTrue("search has no 新建会话 button",
                    rule.onAllNodesWithText("新建会话", useUnmergedTree = true).fetchSemanticsNodes().isEmpty())
                val bitmap = rule.onNodeWithTag("shot").captureToImage().asAndroidBitmap()
                File(out(), "agents-${p.name.lowercase()}-${s.name.lowercase()}-${if (d) "dark" else "light"}.png").outputStream()
                    .use { bitmap.compress(Bitmap.CompressFormat.PNG, 100, it) }
            }
        }
    }

    /** Every quick filter with agents: all six in one bar, at the narrowest phone width the list is drawn at. */
    @Config(sdk = [35], qualifiers = "en-w360dp-h640dp-xhdpi")
    @Test fun sixFilters() {
        var style by mutableStateOf(AppStyle.Glass)
        var dark by mutableStateOf(false)
        val six = sessions + Session("mac", "s4", title = "Approve deploy", cwd = "/code/app", state = "needs_you", spaceId = "w1",
            spaceName = "app", slotName = "3", paneId = "w1:p3", status = "blocked", lastText = "Allow the push?", updatedAt = ago(1))
        val model = AgentsViewModel(Repository(six, machines), Interactions(emptyList()), Connection(), Sender(), preferences())
        rule.setContent {
            PersonaTheme(darkTheme = dark, reduceMotion = true, style = style) {
                Box(Modifier.testTag("shot").width(360.dp).height(640.dp).background(PersonaTheme.palette.background)) {
                    AgentsScreen(model, Nowhere, AgentsMetadata())
                }
            }
        }
        for (s in AppStyle.entries) for (d in listOf(false, true)) {
            rule.runOnIdle { style = s; dark = d }
            rule.waitForIdle()
            listOf("All", "Needs you", "Unread", "Working", "Lark groups", "Terminal").forEach {
                assertTrue("$s shows $it", rule.onAllNodesWithText(it, substring = true, useUnmergedTree = true).fetchSemanticsNodes().isNotEmpty())
            }
            val bitmap = rule.onNodeWithTag("shot").captureToImage().asAndroidBitmap()
            File(out(), "agents-six-${s.name.lowercase()}-${if (d) "dark" else "light"}.png").outputStream()
                .use { bitmap.compress(Bitmap.CompressFormat.PNG, 100, it) }
        }
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
}
