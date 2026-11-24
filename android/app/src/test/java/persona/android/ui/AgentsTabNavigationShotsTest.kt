package persona.android.ui

import android.app.Application
import android.content.ComponentName
import android.graphics.Bitmap
import androidx.activity.ComponentActivity
import androidx.activity.compose.setContent
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.padding
import androidx.compose.runtime.*
import androidx.compose.runtime.snapshots.Snapshot
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.asAndroidBitmap
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.test.*
import androidx.compose.ui.test.junit4.createEmptyComposeRule
import androidx.compose.ui.unit.Density
import androidx.datastore.core.DataStore
import androidx.datastore.preferences.core.Preferences
import androidx.datastore.preferences.core.emptyPreferences
import androidx.lifecycle.viewmodel.navigation3.rememberViewModelStoreNavEntryDecorator
import androidx.navigation3.runtime.*
import androidx.navigation3.ui.NavDisplay
import androidx.test.core.app.ActivityScenario
import androidx.test.core.app.ApplicationProvider
import androidx.test.ext.junit.runners.AndroidJUnit4
import kotlinx.coroutines.flow.MutableSharedFlow
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.serialization.json.JsonObject
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.Shadows
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.android.navigation.AppNavigator
import persona.core.contracts.*
import persona.core.contracts.api.*
import persona.core.designsystem.*
import persona.core.navigation.*
import persona.feature.agents.*
import persona.feature.chat.ChatScreen
import java.io.File
import java.time.Duration
import java.time.Instant

/** Native JVM captures at the default, interface cap and 2.0 scales: home chrome, Agents and chat, with local data. */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(application = Application::class, sdk = [35], qualifiers = "en-w400dp-h800dp-xhdpi")
class AgentsTabNavigationShotsTest {
    @get:Rule val rule = createEmptyComposeRule()
    private val agent = Session("computer", "agent", title = "Plan a weekend trip", state = "working",
        cwd = "/example/travel", updatedAt = Instant.now().minus(Duration.ofDays(7)).toString())
    private val route = ChatRoute(agent.machineId, agent.sessionId, ConversationKind.Agent)
    private val repositories = object : SessionsRepository {
        override val machines = MutableStateFlow(listOf(Machine("computer", "Computer A", online = true)))
        override val sessions = MutableStateFlow(listOf(agent))
        override suspend fun refresh() = Unit
    }
    private val interactions = object : InteractionCenter {
        override val pending = MutableStateFlow(emptyList<Interaction>())
        override suspend fun refresh() = Unit
        override suspend fun resolve(id: String, resolution: Resolution) = Unit
    }
    private val sender = object : MessageSender {
        override val outbox = MutableStateFlow(emptyList<OutboxMessage>())
        override suspend fun send(session: SessionKey, text: String, clientMessageId: String, clearDraft: String?) = Unit
    }
    private val chats = object : ChatRepository {
        override fun observe(session: SessionKey) = MutableStateFlow(ChatSnapshot(loading = false, items = listOf(
            Item("answer", 1, role = "assistant", kind = "text", text = "The trip outline is ready.", ts = "2026-11-12T09:00:00Z"),
        )))
        override suspend fun loadOlder(session: SessionKey) = Unit
        override suspend fun expandFold(session: SessionKey, fromSeq: Int, toSeq: Int) = emptyList<Item>()
    }
    private val connection = object : HubConnection {
        override val state = MutableStateFlow(ConnectionState.Online)
        override val events = MutableSharedFlow<ServerEvent>()
        override val lastHello = Hello(serverVersion = "1.0", protocol = 1)
        override fun reconnect() = Unit
        override fun send(message: JsonObject) = true
    }
    private fun preferences(): AgentsPreferences {
        val store = object : DataStore<Preferences> {
            override val data = MutableStateFlow<Preferences>(emptyPreferences())
            override suspend fun updateData(transform: suspend (Preferences) -> Preferences) = transform(data.value).also { data.value = it }
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
    private fun scene(style: AppStyle, dark: Boolean, fontScale: Float = 1f) {
        val app = ApplicationProvider.getApplicationContext<Application>()
        Shadows.shadowOf(app.packageManager).addActivityIfNotPresent(ComponentName(app, ComponentActivity::class.java))
        val prefs = preferences()
        val dependencies = object : AgentsDependencies {
            override fun sessions() = repositories
            override fun interactions() = this@AgentsTabNavigationShotsTest.interactions
            override fun connection() = this@AgentsTabNavigationShotsTest.connection
            override fun sender() = this@AgentsTabNavigationShotsTest.sender
            override fun preferences() = prefs
            override fun machines(): MachineManagement? = null
        }
        var selected by mutableStateOf(AppTab.Persona)
        val stacks = AppTab.entries.associateWith { mutableStateListOf<NavKey>(tabRoot(it)) }
        val navigator = AppNavigator(stacks, { selected }, { selected = it }, chatTab = { AppTab.Agents })
        rule.mainClock.autoAdvance = false
        ActivityScenario.launch(ComponentActivity::class.java).use { scenario ->
            scenario.onActivity { activity -> activity.setContent {
                CompositionLocalProvider(LocalDensity provides Density(LocalDensity.current.density, fontScale)) {
                    PersonaTheme(style = style, darkTheme = dark, reduceMotion = true) {
                        Box(Modifier.fillMaxSize().testTag("shot")) {
                            val rootKeys = remember { listOf<NavKey>(AssistantRoute, AgentsRoute).map { NavEntry(it) {}.contentKey }.toSet() }
                            val chrome = NavEntryDecorator<NavKey> { entry ->
                                val primary = entry.contentKey in rootKeys
                                CompositionLocalProvider(LocalHomeChrome provides if (primary) HomeChrome(navigationItems(emptyMap()), selected.name,
                                    onSelect = { navigator.selectTab(AppTab.valueOf(it)) }, barHeight = remember { mutableIntStateOf(0) },
                                    leading = { JPersonaButton(PersonaState.Idle, stringResource(persona.android.R.string.app_sidebar), {}) }) else null) {
                                    entry.Content()
                                }
                            }
                            val pageTransitions = rememberPageTransitions()
                            NavDisplay(backStack = stacks.getValue(selected), onBack = navigator::back,
                                entryDecorators = listOf(rememberSaveableStateHolderNavEntryDecorator(), rememberViewModelStoreNavEntryDecorator(), chrome),
                                transitionSpec = { pageTransitions.push },
                                popTransitionSpec = { pageTransitions.pop },
                                predictivePopTransitionSpec = { pageTransitions.predictivePop },
                                entryProvider = entryProvider {
                                    entry<AssistantRoute> {
                                        JPage("Assistant") { padding ->
                                            Box(Modifier.fillMaxSize().padding(padding)) {
                                                ChatScreen(SessionKey("computer", "assistant"), chats, sender, interactions, repositories, onBack = {},
                                                    embedded = true, showTopBar = false,
                                                    renderStyle = persona.feature.chat.ChatRenderStyle.Assistant,
                                                    historySession = Session("computer", "assistant", title = "Assistant", assistant = true))
                                            }
                                        }
                                    }
                                    agentsEntries(navigator, dependencies)
                                    entry<ChatRoute> { ChatScreen(agent.key, chats, sender, interactions, repositories, onBack = navigator::back) }
                                })
                        }
                    }
                }
            } }
            awaitText("Agents")
            capture("assistant", style, dark, fontScale)
            // A task/card's targeted entry opens a chat; returning through the top-level assistant destination leaves
            // that chat on the Agents stack, exactly as a prior session link or saved navigation state does.
            rule.runOnIdle { navigator.navigate(route) }
            awaitText("The trip outline is ready.")
            rule.runOnIdle { assertEquals(route, stacks.getValue(selected).last()) }
            capture("direct-detail", style, dark, fontScale)
            rule.runOnIdle { navigator.selectTab(AppTab.Persona) }
            awaitText("Agents")
            rule.onAllNodesWithText("Agents", useUnmergedTree = true)
                .filter(SemanticsMatcher("placed in the top bar") { it.boundsInRoot.top > 0f })
                .onFirst().performClick()
            frames()
            capture("after-tab", style, dark, fontScale)
            rule.runOnIdle {
                assertEquals(AppTab.Agents, selected)
                assertEquals(listOf(AgentsRoute), stacks.getValue(selected).toList())
            }
            awaitText(agent.displayTitle)
            capture("list", style, dark, fontScale)
            rule.onNodeWithText(agent.displayTitle, useUnmergedTree = true).performClick()
            awaitText("The trip outline is ready.")
            rule.runOnIdle { assertTrue(stacks.getValue(selected).last() is ChatRoute) }
            rule.runOnIdle { navigator.back() }
            awaitText("Agents")
            rule.runOnIdle { assertEquals(listOf(AgentsRoute), stacks.getValue(selected).toList()) }
        }
    }
    private fun frames() {
        Snapshot.sendApplyNotifications()
        repeat(12) { rule.mainClock.advanceTimeByFrame() }
    }
    private fun awaitText(text: String) = await { rule.onAllNodesWithText(text, useUnmergedTree = true).fetchSemanticsNodes().isNotEmpty() }
    private fun await(ready: () -> Boolean) {
        val deadline = System.nanoTime() + 30_000_000_000L
        do {
            frames()
            if (ready()) return
            Thread.sleep(20)
        } while (System.nanoTime() < deadline)
        error("Navigation fixture did not show its destination within 30 seconds: ${rule.onRoot().printToString()}")
    }
    private fun capture(stage: String, style: AppStyle, dark: Boolean, fontScale: Float) {
        frames()
        val bitmap = rule.onNodeWithTag("shot").captureToImage().asAndroidBitmap()
        val output = File(System.getProperty("persona.shots") ?: "build/agents-tab-navigation-shots").also { it.mkdirs() }
        val size = when (fontScale) {
            1f -> ""
            InterfaceFontScaleCap -> "-cap"
            else -> "-large"
        }
        File(output, "$stage-${style.name.lowercase()}-${if (dark) "dark" else "light"}$size.png")
            .outputStream().use { assertTrue(bitmap.compress(Bitmap.CompressFormat.PNG, 100, it)) }
    }
    @Test fun glassLight() = scene(AppStyle.Glass, false)
    @Test fun glassDark() = scene(AppStyle.Glass, true)
    @Test fun briefLight() = scene(AppStyle.Brief, false)
    @Test fun briefDark() = scene(AppStyle.Brief, true)
    @Test fun boardLight() = scene(AppStyle.Board, false)
    @Test fun boardDark() = scene(AppStyle.Board, true)
    @Test fun glassLightCap() = scene(AppStyle.Glass, false, InterfaceFontScaleCap)
    @Test fun glassDarkCap() = scene(AppStyle.Glass, true, InterfaceFontScaleCap)
    @Test fun briefLightCap() = scene(AppStyle.Brief, false, InterfaceFontScaleCap)
    @Test fun briefDarkCap() = scene(AppStyle.Brief, true, InterfaceFontScaleCap)
    @Test fun boardLightCap() = scene(AppStyle.Board, false, InterfaceFontScaleCap)
    @Test fun boardDarkCap() = scene(AppStyle.Board, true, InterfaceFontScaleCap)
    @Test fun glassLightLarge() = scene(AppStyle.Glass, false, 2f)
    @Test fun glassDarkLarge() = scene(AppStyle.Glass, true, 2f)
    @Test fun briefLightLarge() = scene(AppStyle.Brief, false, 2f)
    @Test fun briefDarkLarge() = scene(AppStyle.Brief, true, 2f)
    @Test fun boardLightLarge() = scene(AppStyle.Board, false, 2f)
    @Test fun boardDarkLarge() = scene(AppStyle.Board, true, 2f)
}
