package persona.android.ui

import androidx.compose.foundation.layout.fillMaxHeight
import androidx.compose.foundation.layout.width
import androidx.compose.ui.platform.LocalWindowInfo
import androidx.compose.ui.unit.dp
import persona.android.navigation.*

import android.app.Application
import android.content.ComponentName
import androidx.activity.ComponentActivity
import androidx.activity.compose.setContent
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.runtime.*
import androidx.compose.runtime.snapshots.Snapshot
import androidx.compose.ui.Modifier
import androidx.compose.ui.test.*
import androidx.compose.ui.test.junit4.createEmptyComposeRule
import androidx.datastore.core.DataStore
import androidx.datastore.preferences.core.Preferences
import androidx.datastore.preferences.core.emptyPreferences
import androidx.navigation3.runtime.entryProvider
import androidx.navigation3.runtime.NavKey
import androidx.test.core.app.ActivityScenario
import androidx.test.core.app.ApplicationProvider
import androidx.test.ext.junit.runners.AndroidJUnit4
import kotlinx.coroutines.flow.MutableSharedFlow
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.serialization.json.JsonObject
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.After
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.Shadows
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.core.contracts.*
import persona.core.contracts.api.*
import persona.core.designsystem.*
import persona.core.navigation.*
import persona.feature.agents.*
import persona.feature.chat.ChatServices
import persona.feature.chat.ChatWiringSource
import persona.feature.chat.ChatAppBindings
import persona.feature.assistant.AssistantHostBindings
import persona.feature.assistant.LocalAssistantHost
import java.lang.reflect.Proxy
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.cancel
import java.io.File

import androidx.core.graphics.Insets
import androidx.core.view.ViewCompat
import androidx.core.view.WindowInsetsCompat
import persona.feature.inbox.*
import persona.feature.settings.demoSettingsServices

/** Acceptance tests use the production shell, decorators, navigator, pages and Back dispatcher. */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(application = OverlayTestApplication::class, sdk = [35], qualifiers = "en-w673dp-h951dp-mdpi")
class PersonaShellOverlayTest {
    @get:Rule val rule = createEmptyComposeRule()
    @After fun resetAssistant() {
        AssistantManagerChoice.reset()
        persona.platform.notify.Notify.viewing(null)
    }
    private val agent = Session("computer", "agent", title = "Plan a weekend trip", state = "unread",
        cwd = "/example/travel", updatedAt = "2026-11-12T09:00:00Z")
    private val route = ChatRoute(agent.machineId, agent.sessionId, ConversationKind.Agent)
    private val otherAgent = agent.copy(sessionId = "packing", title = "Review the packing list")
    private val repositories = object : SessionsRepository {
        override val machines = MutableStateFlow(listOf(Machine("computer", "Computer A", online = true)))
        override val sessions = MutableStateFlow(listOf(agent, otherAgent))
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
            Item("answer", 1, role = "assistant", kind = "text",
                text = if (session == otherAgent.key) "The packing list is ready." else "The trip outline is ready.", ts = "2026-11-12T09:00:00Z"),
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


    @Test fun assistantAvatarBackScrimAndPickUseTheRealShell() = shell { scenario ->
        awaitText("The trip outline is ready.")
        sidebar()
        close().assertExists()
        back(scenario)
        close().assertDoesNotExist()
        awaitText("The trip outline is ready.")
        sidebar()
        close().performTouchInput { click(centerRight) }
        frames()
        close().assertDoesNotExist()
        sidebar()
        rule.onNodeWithText("Weekend plans").performScrollTo().performClick()
        awaitText("The weekend plan is ready.")
        close().assertDoesNotExist()
        sidebar()
        close().assertExists()
        back(scenario)
        close().assertDoesNotExist()
        awaitText("The weekend plan is ready.")
    }

    @Test fun agentsBackClosesOnlyTheOverlayAndPicksReplaceTheDetail() = shell(agents = true) { scenario ->
        awaitText("The trip outline is ready.")
        sidebar()
        close().assertExists()
        back(scenario)
        close().assertDoesNotExist()
        awaitText("The trip outline is ready.")
        // If the first Back popped the detail, this button and the detail text would be gone.
        sidebar()
        close().performTouchInput { click(centerRight) }
        frames()
        close().assertDoesNotExist()
        sidebar()
        rule.onNodeWithText(agent.displayTitle).performClick()
        frames()
        close().assertDoesNotExist()
        awaitText("The trip outline is ready.")
        sidebar()
        rule.onNodeWithText(otherAgent.displayTitle).performClick()
        awaitText("The packing list is ready.")
        close().assertDoesNotExist()
        sidebar()
        back(scenario)
        awaitText("The packing list is ready.")
        back(scenario)
        awaitText("Pick a session")
        rule.onNodeWithContentDescription("Show list").performClick()
        awaitText(agent.displayTitle)
        rule.onAllNodesWithText("The packing list is ready.").assertCountEquals(0)
    }

    @Config(qualifiers = "en-w740dp-h951dp-mdpi")
    @Test fun safeInsetsUseTheSamePlacementForTheAvatarAndBothColumns() = shell(sideInset = 48) { scenario ->
        awaitText("The trip outline is ready.")
        // 740dp window, 692dp pane area: the avatar must operate the overlay, not the beside-column state.
        sidebar()
        close().assertExists()
        back(scenario)
        close().assertDoesNotExist()
        rule.onNodeWithText("Agents").performClick()
        frames()
        rule.onNodeWithContentDescription("Show list").performClick()
        awaitText(agent.displayTitle)
        rule.onNodeWithText(agent.displayTitle).performClick()
        awaitText("The trip outline is ready.")
        sidebar()
        close().assertExists()
        back(scenario)
        close().assertDoesNotExist()
        awaitText("The trip outline is ready.")
    }

    @Test fun unfoldingAnOpenDrawerBecomesAnOverlayAndBackKeepsThePage() {
        val width = mutableStateOf(411)
        shell(width = width) { scenario ->
            awaitText("The trip outline is ready.")
            sidebar()
            assertTrue(close().getBoundsInRoot().left.value > 0f)
            rule.runOnIdle { width.value = 673 }
            frames()
            assertEquals(320f, close().getBoundsInRoot().left.value, 0.01f)
            assertEquals(673f, close().getBoundsInRoot().right.value, 0.01f)
            back(scenario)
            close().assertDoesNotExist()
            awaitText("The trip outline is ready.")
            sidebar()
            close().assertExists()
        }
    }

    @Test fun anUnselectedAgentsPageKeepsTheListOverItsPlaceholder() = shell { scenario ->
        awaitText("The trip outline is ready.")
        clickText("Agents")
        awaitText("Pick a session")
        close().assertDoesNotExist()
        rule.onNodeWithContentDescription("Show list").performClick()
        frames()
        close().assertExists()
        awaitText(agent.displayTitle)
        back(scenario)
        close().assertDoesNotExist()
        awaitText("Pick a session")
        rule.onNodeWithContentDescription("Show list").performClick()
        frames()
        clickText(agent.displayTitle)
        awaitText("The trip outline is ready.")
    }

    @Test fun leavingTheAgentsOverlayForAPersonaTodoKeepsThePreviousAgentInItsStack() = shell(agents = true, withTodos = true) { scenario ->
        awaitText("The trip outline is ready.")
        sidebar()
        close().assertExists()
        clickText("Assistant")
        close().assertDoesNotExist()
        sidebar()
        clickText("To-dos")
        awaitText("Trip plan")
        clickText("Trip plan")
        awaitText(otherAgent.displayTitle)
        clickText(otherAgent.displayTitle)
        awaitText("The packing list is ready.")
        awaitSavedAgent(otherAgent.sessionId)
        close().assertDoesNotExist()
        back(scenario)
        // Back returns to its Persona source; the retained Agents stack must still hold the earlier chat.
        awaitText("Trip plan")
        awaitSavedAgent(agent.sessionId)
    }

    @Test fun switchingTabsClosesTheOverlayEvenWhenTheSameAgentsDetailReturns() {
        val navigation = mutableStateOf<((Router) -> ResolvedRoute?)?>({ ResolvedRoute(AppTab.Agents, route) })
        shell(openNavigation = navigation) {
            awaitText("The trip outline is ready.")
            sidebar()
            close().assertExists()
            clickText("Assistant")
            awaitText("The trip outline is ready.")
            awaitSavedTab(AppTab.Persona)
            // Return without changing the retained detail key: its scene's detail-change effect cannot close the list.
            rule.runOnIdle { navigation.value = { ResolvedRoute(AppTab.Agents, route) } }
            frames()
            awaitText("The trip outline is ready.")
            awaitSavedTab(AppTab.Agents)
            close().assertDoesNotExist()
            sidebar()
            close().assertExists()
        }
    }

    @Test fun aSharedTodoEntryUsesTheOrdinaryNavigatorEvenWithAnOpenAgentsOverlay() {
        val agents = mutableListOf<NavKey>(AgentsRoute, route)
        val persona = mutableListOf<NavKey>(AssistantRoute, TodoDetailRoute("trip"))
        var selected = AppTab.Persona
        val navigator = AppNavigator(mapOf(AppTab.Agents to agents, AppTab.Persona to persona), { selected }, { selected = it },
            chatTab = { AppTab.Agents })
        val shown = mutableStateOf(true)
        val listNavigator = object : Navigator by navigator {
            override fun navigate(key: NavKey) { pickAgentsList(navigator, agents, shown, key) }
        }
        // Keep the modal flag true deliberately: tab-change clearing must not mask a shared-entry wiring regression.
        shell(withTodos = true, content = { session ->
            val provider = entryProvider<NavKey> { agentsEntries(navigator, session.agents, listNavigator = listNavigator) }
            provider(TodoDetailRoute("trip")).Content()
        }) {
            awaitText(otherAgent.displayTitle)
            clickText(otherAgent.displayTitle)
            assertEquals(AppTab.Agents, selected)
            assertTrue(shown.value)
            assertEquals(listOf(AgentsRoute, route, ChatRoute(otherAgent.machineId, otherAgent.sessionId)), agents)
            navigator.back()
            assertEquals(listOf(AgentsRoute, route), agents)
        }
    }

    @Test fun aNonDetailActionFromTheOverlaidListReturnsToThePreviousChat() = shell(agents = true, withTodos = true,
        groupByTodo = true) { scenario ->
        awaitText("The trip outline is ready.")
        sidebar()
        awaitText("Trip plan")
        rule.onNodeWithContentDescription("View to-do").performClick()
        frames()
        close().assertDoesNotExist()
        awaitText(otherAgent.displayTitle)
        back(scenario)
        awaitText("The trip outline is ready.")
        close().assertDoesNotExist()
    }

    @Config(qualifiers = "en-w1000dp-h800dp-mdpi")
    @Test fun hidingTheSidebarBesideThePageStartsHiddenOnShellRestart() {
        shell(automation = false) {
            awaitText("The trip outline is ready.")
            awaitText("Weekend plans")
            assertFalse(hasDisplayedContentDescription("Sidebar"))
            assertTrue(hasDisplayedContentDescription("Hide sidebar"))
            val switcherOut = rule.onNodeWithTag("home-switcher").getBoundsInRoot()
            assertEquals(690f, (switcherOut.left.value + switcherOut.right.value) / 2f, 0.5f)

            hideSidebar()
            assertFalse(isTextDisplayed("Weekend plans"))
            assertFalse(hasDisplayedContentDescription("Hide sidebar"))
            assertTrue(hasDisplayedContentDescription("Sidebar"))
            val switcherFolded = rule.onNodeWithTag("home-switcher").getBoundsInRoot()
            assertEquals("Top-bar switcher must stay centered when hiding sidebar",
                500f, (switcherFolded.left.value + switcherFolded.right.value) / 2f, 0.5f)
            awaitSavedColumns(sidebar = false)
        }
        shell(automation = false) {
            awaitText("The trip outline is ready.")
            assertFalse(isTextDisplayed("Weekend plans"))
            assertFalse(hasDisplayedContentDescription("Hide sidebar"))
            assertTrue(hasDisplayedContentDescription("Sidebar"))

            sidebar()
            awaitText("Weekend plans")
            assertFalse(hasDisplayedContentDescription("Sidebar"))
            assertTrue(hasDisplayedContentDescription("Hide sidebar"))
            awaitSavedColumns(sidebar = true)
        }
    }

    @Config(qualifiers = "en-w1000dp-h800dp-mdpi")
    @Test fun overThePageOverlayDismissalDoesNotOverwriteKeptSidebarChoice() {
        val width = mutableStateOf(1000)
        shell(width = width, automation = false) {
            awaitText("The trip outline is ready.")
            awaitText("Weekend plans")
            hideSidebar()
            assertFalse(isTextDisplayed("Weekend plans"))
            awaitSavedColumns(sidebar = false)

            rule.runOnIdle { width.value = 673 }
            frames()
            assertFalse(hasDisplayedContentDescription("Hide sidebar"))
            sidebar()
            close().assertExists()
            assertFalse(hasDisplayedContentDescription("Hide sidebar"))
            close().performTouchInput { click(centerRight) }
            frames()
            close().assertDoesNotExist()

            sidebar()
            rule.onNodeWithText("Weekend plans").performScrollTo().performClick()
            awaitText("The weekend plan is ready.")
            close().assertDoesNotExist()
            awaitSavedColumns(sidebar = false)

            rule.runOnIdle { width.value = 1000 }
            frames()
            assertFalse(isTextDisplayed("Weekend plans"))
            awaitSavedColumns(sidebar = false)
        }
    }

    @Config(qualifiers = "en-w1000dp-h800dp-mdpi")
    @Test fun wideAgentsDrawerNeverShowsColumnFoldButton() {
        shell(automation = false) {
            awaitText("The trip outline is ready.")
            awaitText("Weekend plans")
            clickText("Agents")
            awaitText(agent.displayTitle)
            assertFalse(hasDisplayedContentDescription("Hide sidebar"))
            sidebar()
            close().assertExists()
            assertFalse(hasDisplayedContentDescription("Hide sidebar"))
        }
    }

    @Config(qualifiers = "en-w1000dp-h800dp-mdpi")
    @Test fun hidingTheAgentsListBesideThePageStartsHiddenOnShellRestart() {
        val app = ApplicationProvider.getApplicationContext<OverlayTestApplication>()
        val resize = app.getString(persona.core.designsystem.R.string.designsystem_pane_resize)
        val collapsedState = app.getString(persona.core.designsystem.R.string.designsystem_pane_collapsed)
        shell(automation = false) {
            awaitText("The trip outline is ready.")
            clickText("Agents")
            awaitText(otherAgent.displayTitle)
            clickText(otherAgent.displayTitle)
            awaitText("The packing list is ready.")
            assertTrue(isTextDisplayed(agent.displayTitle))

            rule.onNodeWithContentDescription(resize).performTouchInput {
                swipe(center, center + androidx.compose.ui.geometry.Offset(-350f, 0f), 200)
            }
            frames()
            rule.onNodeWithContentDescription(resize).assert(SemanticsMatcher.expectValue(
                androidx.compose.ui.semantics.SemanticsProperties.StateDescription, collapsedState))
            assertFalse(isTextDisplayed(agent.displayTitle))
            awaitSavedAgent(otherAgent.sessionId)
            awaitSavedColumns(agents = false)
        }
        shell(automation = false) {
            awaitText("The packing list is ready.")
            rule.onNodeWithContentDescription(resize).assert(SemanticsMatcher.expectValue(
                androidx.compose.ui.semantics.SemanticsProperties.StateDescription, collapsedState))
            assertFalse(isTextDisplayed(agent.displayTitle))

            rule.onNodeWithContentDescription(resize).performClick()
            awaitText(agent.displayTitle)
            awaitSavedColumns(agents = true)
        }
    }

    @Config(qualifiers = "en-w1000dp-h800dp-mdpi")
    @Test fun wideAgentsHidesBackBesideListAndHeaderButtonRestoresCollapsedList() {
        val app = ApplicationProvider.getApplicationContext<OverlayTestApplication>()
        val resize = app.getString(persona.core.designsystem.R.string.designsystem_pane_resize)
        val collapsedState = app.getString(persona.core.designsystem.R.string.designsystem_pane_collapsed)
        shell(automation = false) {
            awaitText("The trip outline is ready.")
            clickText("Agents")
            awaitText(otherAgent.displayTitle)
            clickText(otherAgent.displayTitle)
            awaitText("The packing list is ready.")
            assertTrue(isTextDisplayed(agent.displayTitle))
            // First page beside the Agents list: no Back button in the header.
            assertFalse(hasDisplayedContentDescription("Back"))

            rule.onNodeWithContentDescription(resize).performTouchInput {
                swipe(center, center + androidx.compose.ui.geometry.Offset(-350f, 0f), 200)
            }
            frames()
            rule.onNodeWithContentDescription(resize).assert(SemanticsMatcher.expectValue(
                androidx.compose.ui.semantics.SemanticsProperties.StateDescription, collapsedState))
            assertFalse(isTextDisplayed(agent.displayTitle))
            assertFalse(hasDisplayedContentDescription("Back"))
            awaitSavedColumns(agents = false)

            // With the list collapsed, the header's sidebar button brings it back and records the kept choice.
            sidebar()
            awaitText(agent.displayTitle)
            assertFalse(hasDisplayedContentDescription("Back"))
            awaitSavedColumns(agents = true)

            // Collapse again before restart to verify the restored collapsed state exposes the header button too.
            rule.onNodeWithContentDescription(resize).performTouchInput {
                swipe(center, center + androidx.compose.ui.geometry.Offset(-350f, 0f), 200)
            }
            frames()
            awaitSavedColumns(agents = false)
        }
        shell(automation = false) {
            awaitText("The packing list is ready.")
            assertFalse(isTextDisplayed(agent.displayTitle))
            assertFalse(hasDisplayedContentDescription("Back"))

            sidebar()
            awaitText(agent.displayTitle)
            awaitSavedColumns(agents = true)
        }
    }

    private fun shell(agents: Boolean = false, sideInset: Int = 0, width: MutableState<Int>? = null,
        withTodos: Boolean = false, groupByTodo: Boolean = false,
        automation: Boolean = true,
        openNavigation: MutableState<((Router) -> ResolvedRoute?)?>? = null,
        content: (@Composable (ShellSession) -> Unit)? = null,
        check: (ActivityScenario<ComponentActivity>) -> Unit) {
        AssistantManagerChoice.choose("hub", null, "assistant")
        val app = ApplicationProvider.getApplicationContext<OverlayTestApplication>()
        Shadows.shadowOf(app.packageManager).addActivityIfNotPresent(ComponentName(app, ComponentActivity::class.java))
        val main = Session("computer", "assistant", title = "Main conversation", assistant = true)
        val recent = main.copy(sessionId = "recent", title = "Weekend plans")
        val snapshot = AssistantSnapshot(mainSession = main.key, conversations = listOf(main, recent))
        val assistant = object : AssistantConversations {
            override val assistant = MutableStateFlow(snapshot)
            override val pinned = MutableStateFlow(emptySet<SessionKey>())
            override suspend fun refresh() = Unit
            override suspend fun startConversation() = main.key
        }
        val chat = object : ChatRepository by chats {
            override fun observe(session: SessionKey) = if (session == recent.key) MutableStateFlow(ChatSnapshot(loading = false,
                items = listOf(Item("weekend", 1, role = "assistant", kind = "text", text = "The weekend plan is ready.")))) else chats.observe(session)
        }
        val inbox = object : persona.feature.inbox.InboxRepository {
            override val entries = MutableStateFlow(emptyList<InboxEntry>())
            override val snapshot = MutableStateFlow(InboxSnapshot())
            override suspend fun refresh() = Unit
            override fun conversation(feedId: String, startPosition: Long?) = MutableStateFlow(LarkConversation())
            override suspend fun loadOlder(feedId: String) = Unit
            override suspend fun refreshConversation(feedId: String) = Unit
            override suspend fun setDone(itemId: String, done: Boolean) = Unit
            override suspend fun readDraft(target: ReplyTarget): ReplyDraft? = null
            override suspend fun saveDraft(target: ReplyTarget, draft: ReplyDraft) = Unit
            override suspend fun reply(target: ReplyTarget, request: ReplyRequest) = Unit
            override suspend fun downloadFile(file: LarkPart.File): String = error("No files in this fixture")
        }
        val hubScope = HubScope("overlay-test", "fixture")
        File(app.noBackupFilesDir, "hub-data/${hubScope.path}").mkdirs()
        val scope = CoroutineScope(SupervisorJob() + Dispatchers.Main.immediate)
        val todo = Todo("trip", "Trip plan", agents = listOf(TodoAgent(otherAgent.machineId, otherAgent.sessionId)))
        val tasks = if (withTodos) persona.core.domain.tasks.TasksStore(object : TasksApi {
            override suspend fun todos() = listOf(todo)
            override suspend fun todoDecisions(todoId: String) = emptyList<TodoDecision>()
            override suspend fun feed(conversation: String, since: String?): FeedResponse = error("No feed in this fixture")
            override suspend fun startPlan(id: String, remember: Boolean): TaskPlan = error("No plans in this fixture")
            override suspend fun dismissPlan(id: String): TaskPlan = error("No plans in this fixture")
            override suspend fun retryTask(todoId: String): Todo = error("No retries in this fixture")
            override suspend fun removeTask(todoId: String) = error("No removal in this fixture")
            override suspend fun route(conversation: String, todoId: String, text: String): FeedItem = error("No sends in this fixture")
            override suspend fun today(conversation: String): FeedItem = error("No today card in this fixture")
            override suspend fun mainConversation(): String? = null
            override suspend fun inboxReply(itemId: String, text: String, card: String?) = error("No replies in this fixture")
        }, connection, scope) else null
        val drafts = object : DraftRepository {
            override fun observe(key: DraftKey) = MutableStateFlow(DraftSnapshot(hubScope, key))
            override suspend fun read(key: DraftKey) = DraftSnapshot(hubScope, key)
            override suspend fun save(key: DraftKey, text: String, expectedVersion: Long) = DraftSnapshot(hubScope, key, text, expectedVersion + 1)
        }
        val management = object : AssistantManagement by unused() {
            override fun forManager(manager: String?) = this
            override suspend fun warm() = Unit
        }
        val composer = object : ChatComposerService {
            override val deliveries = MutableStateFlow(emptyMap<String, ComposerDelivery>())
            override suspend fun upload(session: SessionKey, name: String, file: File): String = error("No uploads in this fixture")
            override suspend fun interrupt(session: SessionKey) = error("No interruption in this fixture")
            override suspend fun retryRefused(clientMessageId: String, clearDraft: Boolean): ComposerDelivery = error("No retries in this fixture")
        }
        val actions = object : ChatActions {
            override suspend fun isMuted(session: Session) = false
            override suspend fun execute(session: Session, action: ChatAction): ChatActionResult = error("No chat actions in this fixture")
            override suspend fun verify(session: Session, action: ChatAction): ChatActionResult = error("No chat actions in this fixture")
        }
        val mainSender = object : MainConversationSender {
            override val outbox = sender.outbox
            override suspend fun sendToMain(text: String, clientMessageId: String, draft: DraftSnapshot?) = error("No sends in this fixture")
        }
        val bindings = ChatAppBindings(hubScope, scope, drafts, connection, actions, composer, unused(), management, mainSender)
        val wiring = object : ChatWiringSource { override val current = MutableStateFlow<ChatAppBindings?>(bindings) }
        app.component = object : ChatServices, persona.feature.chat.ChatRenameServices, AgentsRenameDependencies, dagger.hilt.internal.GeneratedComponent {
            override fun sessionRenamer() = object : persona.core.domain.sort.SessionRenamer {
                override fun project(source: kotlinx.coroutines.flow.Flow<List<Session>>) = source
                override suspend fun rename(session: Session, title: String?, tabName: String?) = false
            }
            override fun renamer() = sessionRenamer()
            override fun chats() = chat
            override fun messages() = sender
            override fun interactions() = this@PersonaShellOverlayTest.interactions
            override fun sessions() = repositories
            override fun chatWiring() = wiring
            override fun inboxTrays() = object : AssistantInboxTrays {
                override fun drafts(scope: String) = MutableStateFlow(emptyList<AssistantInboxDraft>())
                override suspend fun send(scope: String, draft: AssistantInboxDraft, clientId: String) = false
                override suspend fun refresh(scope: String) = Unit
            }
            override fun assistantRepository() = assistant
        }
        val preferences = preferences()
        if (groupByTodo) kotlinx.coroutines.runBlocking { preferences.grouping(persona.core.domain.sort.AgentGrouping.Todo) }
        val session = ShellSession(hubScope.path, connection, repositories, chat, interactions, sender, assistant, inbox, preferences, tasks = tasks)
        val host = object : ShellHost {
            override val automation = automation
            override val posture = mutableStateOf<String?>(null)
            override val feedback = object : AppFeedback {
                override fun failure(error: Throwable, operation: Boolean) = false
                override fun notice(message: String) = false
            }
            override val terminalPanes: TerminalPanes? = null
            override fun groupTerminal(key: SessionKey): TerminalTransport? = null
            override fun exitDemo() = Unit
        }
        val navigation = openNavigation ?: mutableStateOf<((Router) -> ResolvedRoute?)?>(if (agents) ({ ResolvedRoute(AppTab.Agents, route) }) else null)
        rule.mainClock.autoAdvance = false
        ActivityScenario.launch(ComponentActivity::class.java).use { scenario ->
            scenario.onActivity { activity ->
                activity.setContent {
                    PersonaTheme(style = AppStyle.Glass, reduceMotion = true) {
                        val actual = LocalWindowInfo.current
                        val window = if (width == null) actual else object : androidx.compose.ui.platform.WindowInfo by actual {
                            override val containerSize get() = androidx.compose.ui.unit.IntSize(width.value, actual.containerSize.height)
                        }
                        val assistantHost = remember {
                            AssistantHostBindings(hubScope.path, management, drafts, scope, false, repositories.machines.value,
                                assistant, mainSender, chatContent = { key, _ ->
                                    persona.feature.chat.ChatDestination(ChatRoute(key.machineId, key.sessionId, ConversationKind.Assistant),
                                        unused<Navigator>(), embedded = true,
                                        showTopBar = false, renderStyle = persona.feature.chat.ChatRenderStyle.Assistant,
                                        historySession = snapshot.conversations.firstOrNull { it.key == key })
                                })
                        }
                        CompositionLocalProvider(LocalWindowInfo provides window, LocalAssistantHost provides assistantHost) {
                            Box(Modifier.then(if (width == null) Modifier.fillMaxSize() else
                                Modifier.width(width.value.dp).fillMaxHeight())) {
                                if (content != null) content(session)
                                else PersonaShell(session, navigation, demoSettingsServices(app.resources), host)
                            }
                        }
                    }
                }
            }
            frames()
            if (sideInset > 0) {
                scenario.onActivity { activity -> ViewCompat.dispatchApplyWindowInsets(activity.window.decorView,
                    WindowInsetsCompat.Builder().setInsets(WindowInsetsCompat.Type.navigationBars(), Insets.of(sideInset, 0, 0, 0)).build()) }
                frames()
            }
            check(scenario)
        }
        scope.cancel()
    }
    private fun close() = rule.onNodeWithContentDescription("Close")
    private fun sidebar() {
        val buttons = rule.onAllNodesWithContentDescription("Sidebar")
        val visible = (0 until buttons.fetchSemanticsNodes().size).single { buttons[it].isDisplayed() }
        buttons[visible].performClick()
        frames()
    }
    private fun hideSidebar() {
        val buttons = rule.onAllNodesWithContentDescription("Hide sidebar")
        val visible = (0 until buttons.fetchSemanticsNodes().size).single { buttons[it].isDisplayed() }
        buttons[visible].performClick()
        frames()
    }
    private fun clickText(text: String) {
        val nodes = rule.onAllNodesWithText(text)
        val visible = (0 until nodes.fetchSemanticsNodes().size).single { nodes[it].isDisplayed() }
        nodes[visible].performClick()
        frames()
    }
    private fun awaitSavedAgent(sessionId: String) {
        val app = ApplicationProvider.getApplicationContext<OverlayTestApplication>()
        val store = ShellPositionStore(app, HubScope("overlay-test", "fixture").path)
        rule.waitUntil(10_000) { store.load().chats[AppTab.Agents]?.sessionId == sessionId }
    }
    private fun awaitSavedTab(tab: AppTab) {
        val app = ApplicationProvider.getApplicationContext<OverlayTestApplication>()
        val store = ShellPositionStore(app, HubScope("overlay-test", "fixture").path)
        rule.waitUntil(10_000) { store.load().tab == tab }
    }
    private fun awaitSavedColumns(sidebar: Boolean? = null, agents: Boolean? = null) {
        val app = ApplicationProvider.getApplicationContext<OverlayTestApplication>()
        val store = ShellPositionStore(app, HubScope("overlay-test", "fixture").path)
        rule.waitUntil(10_000) {
            val saved = store.load()
            (sidebar == null || saved.sidebarColumnShown == sidebar) &&
                (agents == null || saved.agentsListShown == agents)
        }
    }
    private fun isTextDisplayed(text: String): Boolean {
        val nodes = rule.onAllNodesWithText(text, useUnmergedTree = true)
        return (0 until nodes.fetchSemanticsNodes().size).any { nodes[it].isDisplayed() }
    }
    private fun hasDisplayedContentDescription(description: String): Boolean {
        val nodes = rule.onAllNodesWithContentDescription(description, useUnmergedTree = true)
        return (0 until nodes.fetchSemanticsNodes().size).any { nodes[it].isDisplayed() }
    }
    private fun back(scenario: ActivityScenario<ComponentActivity>) {
        scenario.onActivity { it.onBackPressedDispatcher.onBackPressed() }
        frames()
    }
    private fun frames() {
        Snapshot.sendApplyNotifications()
        repeat(24) { rule.mainClock.advanceTimeByFrame() }
        rule.waitForIdle()
    }
    private fun awaitText(text: String) {
        val deadline = System.nanoTime() + 30_000_000_000L
        do {
            frames()
            val nodes = rule.onAllNodesWithText(text, useUnmergedTree = true)
            if ((0 until nodes.fetchSemanticsNodes().size).any { nodes[it].isDisplayed() }) return
            Thread.sleep(20)
        } while (System.nanoTime() < deadline)
        error("Missing $text: ${rule.onRoot().printToString()}")
    }

    /** Services outside these navigation gestures fail if the shell unexpectedly calls them. */
    private inline fun <reified T> unused(): T = Proxy.newProxyInstance(T::class.java.classLoader, arrayOf(T::class.java)) { proxy, method, args ->
        when (method.name) {
            "toString" -> "Unused ${T::class.java.simpleName}"
            "hashCode" -> System.identityHashCode(proxy)
            "equals" -> proxy === args?.firstOrNull()
            else -> error("Unexpected ${T::class.java.simpleName}.${method.name}")
        }
    } as T
}

class OverlayTestApplication : Application(), dagger.hilt.internal.GeneratedComponentManager<Any> {
    lateinit var component: Any
    override fun generatedComponent() = component
}
