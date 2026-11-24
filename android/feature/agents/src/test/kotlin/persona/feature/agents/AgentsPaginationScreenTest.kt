package persona.feature.agents

import android.graphics.Bitmap
import androidx.activity.ComponentActivity
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.runtime.*
import androidx.compose.runtime.snapshots.Snapshot
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.asAndroidBitmap
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.semantics.SemanticsActions
import androidx.compose.ui.semantics.SemanticsProperties
import androidx.compose.ui.test.*
import androidx.compose.ui.test.junit4.createAndroidComposeRule
import androidx.compose.ui.text.AnnotatedString
import androidx.compose.ui.text.TextRange
import androidx.datastore.core.DataStore
import androidx.datastore.preferences.core.Preferences
import androidx.datastore.preferences.core.emptyPreferences
import androidx.navigation3.runtime.NavKey
import androidx.test.ext.junit.runners.AndroidJUnit4
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.flow.MutableSharedFlow
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.runBlocking
import kotlinx.coroutines.test.StandardTestDispatcher
import kotlinx.serialization.json.JsonObject
import org.junit.Assert.*
import org.junit.After
import androidx.lifecycle.viewModelScope
import kotlinx.coroutines.cancel
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.core.contracts.*
import persona.core.contracts.api.*
import persona.core.data.HubSessionsRepository
import persona.core.designsystem.*
import persona.core.domain.sort.AgentQuickFilter
import persona.core.navigation.*
import java.io.File
import kotlin.coroutines.CoroutineContext
import kotlin.coroutines.EmptyCoroutineContext

/** Real AgentsScreen, native JVM rendering; not physical-device frame-time evidence. */
@RunWith(AndroidJUnit4::class)
@Config(sdk = [35], qualifiers = "en-w400dp-h900dp-xhdpi")
@GraphicsMode(GraphicsMode.Mode.NATIVE)
class AgentsPaginationScreenTest {
    @get:Rule val rule = createAndroidComposeRule<ComponentActivity>()
    private val models = mutableListOf<AgentsViewModel>()
    @After fun disposeModels() { models.forEach { it.viewModelScope.cancel() } }

    private class PagedRepository(rowCount: Int = 50, status: SessionListStatus? = null) : SessionsRepository {
        private fun row(index: Int) = Session("demo", "s-$index", title = "Task $index", cwd = "/demo/app",
            state = "working", status = "working", waiting = false, updatedAt = "2026-11-06T09:00:00Z")
        override val sessions = MutableStateFlow((0 until rowCount).map(::row))
        override val machines = MutableStateFlow(listOf(Machine("demo", "Example computer", online = true)))
        private val counts = SessionListCounts(total = 8000, machine = mapOf("demo" to 8000),
            project = mapOf("/demo/app" to 8000), state = mapOf("working" to 8000), revision = "r1")
        override val listStatus = MutableStateFlow(status ?: SessionListStatus(paged = true, loaded = true, hasMore = true,
            total = 8000, counts = counts, machineCounts = counts))
        val filters = mutableListOf<SessionListFilter>()
        val countFilters = mutableListOf<SessionListFilter>()
        override suspend fun listCounts(filter: SessionListFilter): SessionListCounts { countFilters += filter; return counts }
        var pages = 1
        var refreshes = 0
        override suspend fun refresh() { refreshes++ }
        override suspend fun setListFilter(filter: SessionListFilter) { filters += filter }
        override suspend fun loadMore() {
            pages++
            val start = sessions.value.size
            sessions.value += (start until start + 50).map(::row)
        }
    }

    @Test fun pagedRowsAndCountsUseTheSameHubVisibilityAfterRefresh() {
        var show = false
        val api = object : AgentDisplayApi {
            override suspend fun read() = AgentsDisplaySettings(show, true)
            override suspend fun migrateHidden(): AgentsDisplaySettings = error("No legacy key")
        }
        val hello = MutableStateFlow<SocketHello?>(SocketHello(1, Hello(protocol = 1, serverVersion = "fixture")))
        val pairs = AgentsDisplayHubs { kotlinx.coroutines.flow.flowOf(listOf(AgentsDisplayHub("fixture", null, true, hello, api))) }
        val repository = object : SessionsRepository {
            val lark = Session("demo", "group", title = "Chat topic", paneId = "botmux:1", cwd = "/demo", state = "working")
            override val sessions = MutableStateFlow(listOf(lark, lark.copy(sessionId = "terminal", paneId = "w1:p1")))
            override val machines = MutableStateFlow(listOf(Machine("demo", "Example computer")))
            override val listStatus = MutableStateFlow(SessionListStatus(paged = true, loaded = true))
            override suspend fun refresh() = Unit
            override suspend fun setListFilter(filter: SessionListFilter) {
                val hidden = null in filter.hiddenBotmuxHubs
                val total = if (hidden) 12 else 20
                val counts = SessionListCounts(total, machine = mapOf("demo" to total), state = mapOf("working" to total),
                    paneSource = if (hidden) mapOf("terminal" to 12) else mapOf("terminal" to 12, "feishu" to 8), revision = "fixture")
                listStatus.value = SessionListStatus(paged = true, loaded = true, hasMore = true, counts = counts,
                    machineCounts = counts, filter = filter, total = total)
            }
        }
        val model = model(repository, display = pairs)
        rule.setContent { PersonaTheme(reduceMotion = true) { AgentsScreen(model, Nowhere, AgentsMetadata()) } }
        rule.waitUntil(5_000) { model.page.value.allCount == 12 && model.page.value.sessions.size == 1 }
        assertEquals(0, model.page.value.list.counts[AgentQuickFilter.Feishu])
        assertEquals(12, model.page.value.machineTotal)
        rule.runOnIdle { show = true; model.refresh() }
        rule.waitUntil(5_000) { model.page.value.allCount == 20 && model.page.value.sessions.size == 2 }
        assertEquals(8, model.page.value.list.counts[AgentQuickFilter.Feishu])
        assertEquals(20, model.page.value.list.counts[AgentQuickFilter.Working])
        assertEquals(20, model.page.value.machineTotal)
    }

    @Test fun firstFiftyVirtualizedThenScrollLoadsNextPageAndServerFilters() {
        val repository = PagedRepository()
        val model = model(repository)
        rule.setContent { PersonaTheme(reduceMotion = true) { AgentsScreen(model, Nowhere, AgentsMetadata()) } }
        rule.waitUntil(5_000) { model.page.value.loaded }
        rule.waitForIdle()
        assertEquals(1, repository.pages)
        assertEquals(50, model.page.value.sessions.size)
        assertEquals(8000, model.page.value.allCount)
        assertEquals(8000, model.page.value.machineTotal)
        assertEquals(8000, model.page.value.list.counts[AgentQuickFilter.Working])
        // One computer has no heading (#4338 A7), so only its repository asks for its count.
        rule.waitUntil(5_000) { model.page.value.list.groups.firstOrNull()?.totalCount == 8000 }
        assertTrue(repository.countFilters.contains(SessionListFilter(machineId = "demo", project = "/demo/app")))
        assertEquals(8000, model.page.value.list.groups.first().totalCount)
        assertTrue("offscreen row is not composed", rule.onAllNodesWithText("Task 49", useUnmergedTree = true).fetchSemanticsNodes().isEmpty())
        rule.onNode(SemanticsMatcher.keyIsDefined(SemanticsActions.ScrollToIndex)).performScrollToIndex(50)
        rule.waitUntil(5_000) { repository.pages > 1 }
        assertEquals(100, repository.sessions.value.size)
        rule.runOnIdle { model.query("deploy"); model.machine("demo"); model.filter(AgentQuickFilter.Working) }
        rule.waitForIdle()
        println("Forwarded filters=${repository.filters}; failure=${model.failure.value}")
        rule.waitUntil(5_000) { repository.filters.lastOrNull() == SessionListFilter(state = "working", machineId = "demo", query = "deploy", requiresCompleteMembership = true) }
        println("8000 synthetic sessions: first page=50, after end scroll=100; LazyColumn offscreen Task 49 not composed")
    }

    @Test fun liveReorderKeepsTheSessionBeingReadAtTheSameHeight() {
        val repository = PagedRepository(rowCount = 80)
        val model = model(repository)
        rule.setContent { PersonaTheme(reduceMotion = true) { AgentsScreen(model, Nowhere, AgentsMetadata()) } }
        rule.waitUntil(5_000) { model.page.value.loaded }

        val ordered = model.page.value.list.groups.single().rows.map { it.session }
        val first = ordered[30]
        val list = rule.onNode(hasScrollToKeyAction())
        list.performScrollToKey("session:${first.key.path}")
        val viewport = list.fetchSemanticsNode().boundsInRoot
        val middle = viewport.top + viewport.height / 2
        val promoted = (ordered.take(12) + ordered.subList(31, 58)).mapTo(mutableSetOf()) { it.key }
        val promotedTitles = ordered.filter { it.key in promoted }.mapTo(mutableSetOf()) { it.title }
        val reader = rule.onAllNodesWithText("Task ", substring = true).fetchSemanticsNodes()
            .filter { it.boundsInRoot.height > 0 && it.config[SemanticsProperties.Text].any { text -> text.text in promotedTitles } }
            .minBy { kotlin.math.abs(it.boundsInRoot.center.y - middle) }
        val title = reader.config[SemanticsProperties.Text].first { it.text in promotedTitles }.text
        val readingRow = rule.onNodeWithText(title).assertIsDisplayed()
        val before = reader.boundsInRoot.top
        assertTrue("the reader is below the beginning of the viewport", before > viewport.top + viewport.height / 4)
        rule.onNodeWithText(first.title.orEmpty()).assertIsDisplayed()

        // The first visible session stays Working. The reader and its neighbors move well above it when they become Unread.
        rule.runOnIdle {
            repository.sessions.value = repository.sessions.value.map { session ->
                if (session.key in promoted) session.copy(state = "unread", status = "done") else session
            }
        }
        rule.waitUntil(5_000) {
            model.page.value.list.groups.singleOrNull()?.rows?.take(promoted.size)?.map { it.session.key }?.toSet() == promoted
        }

        val after = readingRow.assertIsDisplayed()
            .assert(SemanticsMatcher.expectValue(SemanticsProperties.StateDescription, Status.Unread.label(rule.activity)))
            .fetchSemanticsNode().boundsInRoot.top
        assertEquals("the reader stays at the same viewport position after its rank changes", before, after, 2f)
    }

    @Test fun foldingAGroupStartsANewReadingPosition() {
        val repository = PagedRepository(rowCount = 80, status = SessionListStatus(paged = true, loaded = true))
        repository.sessions.value = repository.sessions.value.mapIndexed { index, session ->
            val repo = when (index) {
                0 -> "alpha"
                1, 2 -> "beta"
                else -> "gamma"
            }
            session.copy(cwd = "/demo/$repo")
        }
        val model = model(repository)
        rule.setContent { PersonaTheme(style = AppStyle.Glass, reduceMotion = true) { AgentsScreen(model, Nowhere, AgentsMetadata()) } }
        rule.waitUntil(5_000) { model.page.value.loaded }

        val first = repository.sessions.value.first()
        val beta = model.page.value.list.groups.single { group -> group.rows.any { it.session.title == "Task 1" } }
        val list = rule.onNode(hasScrollToKeyAction())
        list.performScrollToKey("session:${first.key.path}")
        val middle = list.fetchSemanticsNode().boundsInRoot.center.y
        val firstRow = rule.onNodeWithText(first.title).assertIsDisplayed()
        val firstTop = firstRow.fetchSemanticsNode().boundsInRoot.top
        val hidden = rule.onNodeWithText("Task 1").assertIsDisplayed()
        val hiddenHeight = hidden.fetchSemanticsNode().boundsInRoot.height
        val gamma = rule.onNodeWithContentDescription("gamma", substring = true).assertIsDisplayed()
        val before = gamma.fetchSemanticsNode().boundsInRoot
        assertTrue("the reading position falls in gamma below the fold", before.bottom < middle)

        // Folding beta removes the rows between the first visible key and the reading position in gamma.
        rule.onNodeWithContentDescription("beta", substring = true).assertIsDisplayed().performClick()
        rule.waitUntil(5_000) {
            val page = model.page.value
            beta.id in page.settings.folded && page.list.groups.any { it.id == beta.id && it.folded }
        }

        hidden.assertDoesNotExist()
        assertEquals("the first visible key stays put", firstTop, firstRow.fetchSemanticsNode().boundsInRoot.top, 2f)
        val after = gamma.assertIsDisplayed().fetchSemanticsNode().boundsInRoot.top
        assertTrue("gamma moves up with the manual fold", before.top - after >= hiddenHeight)
    }

    @Test fun compactListStartsANewReadingPositionForLaterLiveUpdates() {
        val repository = PagedRepository(rowCount = 80, status = SessionListStatus(paged = true, loaded = true))
        repository.sessions.value = repository.sessions.value.map { it.copy(lastText = "Current progress") }
        val model = model(repository)
        rule.setContent { PersonaTheme(style = AppStyle.Brief, reduceMotion = true) { AgentsScreen(model, Nowhere, AgentsMetadata()) } }
        rule.waitUntil(5_000) { model.page.value.loaded }

        val ordered = model.page.value.list.groups.single().rows.map { it.session }
        val first = ordered[30]
        val list = rule.onNode(hasScrollToKeyAction())
        list.performScrollToKey("session:${first.key.path}")
        val firstRow = rule.onNodeWithText(first.title).assertIsDisplayed()
        val detailed = firstRow.fetchSemanticsNode().boundsInRoot

        rule.onNodeWithContentDescription(rule.activity.getString(R.string.agents_action_list_settings)).performClick()
        rule.onNodeWithText(rule.activity.getString(R.string.agents_menu_compact)).performClick()
        rule.waitUntil(5_000) { model.page.value.preferences.compact }
        val compact = firstRow.assertIsDisplayed().fetchSemanticsNode().boundsInRoot
        assertTrue("the user chose a shorter row layout", compact.height < detailed.height)
        assertEquals("the old reading anchor does not scroll the compact layout", detailed.top, compact.top, 2f)

        val middle = list.fetchSemanticsNode().boundsInRoot.center.y
        val promoted = (ordered.take(12) + ordered.subList(31, 63)).mapTo(mutableSetOf()) { it.key }
        val promotedTitles = ordered.filter { it.key in promoted }.mapTo(mutableSetOf()) { it.title }
        val reader = rule.onAllNodesWithText("Task ", substring = true).fetchSemanticsNodes()
            .filter { it.boundsInRoot.height > 0 && it.config[SemanticsProperties.Text].any { text -> text.text in promotedTitles } }
            .minBy { kotlin.math.abs(it.boundsInRoot.center.y - middle) }
        val title = reader.config[SemanticsProperties.Text].first { it.text in promotedTitles }.text
        val compactReadingTop = reader.boundsInRoot.top

        rule.runOnIdle {
            repository.sessions.value = repository.sessions.value.map { session ->
                if (session.key in promoted) session.copy(state = "unread", status = "done") else session
            }
        }
        rule.waitUntil(5_000) {
            model.page.value.list.groups.singleOrNull()?.rows?.take(promoted.size)?.map { it.session.key }?.toSet() == promoted
        }
        val after = rule.onNodeWithText(title).assertIsDisplayed().fetchSemanticsNode().boundsInRoot.top
        assertEquals("the live update preserves the compact reading position", compactReadingTop, after, 2f)
    }

    @Test fun searchPreservesTextAndCaretWhileListProjectionIsPaused() {
        val repository = PagedRepository()
        // Leave the list dispatcher paused while the editor and repository receive input.
        val model = model(repository, StandardTestDispatcher())
        rule.setContent { PersonaTheme(reduceMotion = true) { AgentsScreen(model, Nowhere, AgentsMetadata()) } }

        val search = rule.onNode(hasSetTextAction())
        search.performTextInput("OA")
        search.assertTextContains("OA")
        search.performSemanticsAction(SemanticsActions.SetSelection) { val _ = it(1, 1, false) }
        search.performTextInput("R")
        search.assertTextContains("ORA")
            .assert(SemanticsMatcher.expectValue(SemanticsProperties.TextSelectionRange, TextRange(2)))
        search.performTextInput("C")
        search.assertTextContains("ORCA")
            .assert(SemanticsMatcher.expectValue(SemanticsProperties.TextSelectionRange, TextRange(3)))
        rule.waitUntil(5_000) { repository.filters.lastOrNull()?.query == "ORCA" }
        assertFalse(model.page.value.loaded)

        rule.onNodeWithContentDescription(rule.activity.getString(persona.core.designsystem.R.string.designsystem_search_clear)).performClick()
        search.assert(SemanticsMatcher.expectValue(SemanticsProperties.EditableText, AnnotatedString("")))
        rule.waitUntil(5_000) { repository.filters.lastOrNull()?.query == "" }
    }

    @Test
    @Config(sdk = [35], qualifiers = "en-w400dp-h420dp-xhdpi")
    fun pagedFirstPageShowsOneVisibleLoadingStateInAShortWindow() {
        val repository = PagedRepository(rowCount = 0, status = SessionListStatus(paged = true, loaded = false, loading = true))
        val model = model(repository)
        rule.setContent { PersonaTheme(reduceMotion = true) { AgentsScreen(model, Nowhere, AgentsMetadata()) } }
        rule.waitForIdle()
        assertFalse(model.page.value.loaded)
        rule.onAllNodesWithContentDescription("Loading").assertCountEquals(1)
        rule.onNodeWithContentDescription("Loading").assertIsDisplayed()
        rule.onNodeWithText("Start your first coding session", substring = true).assertDoesNotExist()
    }

    @Test
    @Config(sdk = [35], qualifiers = "en-w400dp-h420dp-xhdpi")
    fun pagedFirstPageFailureShowsRetryInsteadOfPlaceholders() {
        val repository = PagedRepository(rowCount = 0, status = SessionListStatus(paged = true, loaded = false, failed = true))
        val model = model(repository)
        rule.setContent { PersonaTheme(reduceMotion = true) { AgentsScreen(model, Nowhere, AgentsMetadata()) } }
        rule.waitForIdle()
        assertFalse(model.page.value.loaded)
        rule.onNodeWithContentDescription("Loading").assertDoesNotExist()
        rule.onNodeWithText("Start your first coding session", substring = true).assertDoesNotExist()
        rule.onNodeWithText("Retry").assertIsDisplayed().performClick()
        rule.waitUntil(5_000) { repository.refreshes == 1 }
    }

    @Test fun coldStartFailureFromRealRepositoryShowsRetryInsteadOfEmptyState() {
        val scope = CoroutineScope(SupervisorJob() + Dispatchers.Default)
        try {
            val transport = FailedTransport()
            val repository = HubSessionsRepository(transport, object : HubConnection {
                override val state = MutableStateFlow(ConnectionState.WaitingForNetwork)
                override val events = MutableSharedFlow<ServerEvent>()
                override fun reconnect() = Unit
                override fun send(message: JsonObject) = true
            }, scope)
            runBlocking { assertTrue(runCatching { repository.refresh() }.isFailure) }
            assertEquals(1, transport.pageReads)
            assertFalse(repository.listStatus.value.paged)
            assertFalse(repository.listStatus.value.loaded)
            assertTrue(repository.listStatus.value.failed)

            val model = model(repository)
            rule.setContent { PersonaTheme(reduceMotion = true) { AgentsScreen(model, Nowhere, AgentsMetadata()) } }
            rule.waitUntil(5_000) { model.page.value.pagination.failed }
            rule.waitForIdle()
            assertFalse(model.page.value.loaded)
            rule.onNodeWithText("Retry").assertIsDisplayed()
            rule.onNodeWithText("Start your first coding session", substring = true).assertDoesNotExist()
            rule.onNodeWithContentDescription("Loading").assertDoesNotExist()
        } finally { scope.cancel() }
    }

    @Test fun firstPageScreenshotsAllStylesLightAndDark() {
        val repository = PagedRepository()
        val model = model(repository)
        var style by mutableStateOf(AppStyle.Glass)
        var dark by mutableStateOf(false)
        rule.setContent {
            PersonaTheme(darkTheme = dark, style = style, reduceMotion = true) {
                Box(Modifier.fillMaxSize().background(PersonaTheme.palette.background).testTag("shot")) {
                    AgentsScreen(model, Nowhere, AgentsMetadata())
                }
            }
        }
        rule.waitUntil(5_000) { model.page.value.loaded }
        rule.waitUntil(5_000) { model.page.value.list.groups.firstOrNull()?.totalCount == 8000 }
        val directory = File("build/pagination-shots").also { it.mkdirs() }
        for (appearance in AppStyle.entries) {
            var lightPixel = 0
            for (isDark in listOf(false, true)) {
                rule.runOnIdle { style = appearance; dark = isDark; Snapshot.sendApplyNotifications() }
                rule.waitForIdle()
                val bitmap = rule.onNodeWithTag("shot").captureToImage().asAndroidBitmap()
                if (!isDark) lightPixel = bitmap.getPixel(0, 0)
                else assertNotEquals("dark and light differ", lightPixel, bitmap.getPixel(0, 0))
                File(directory, "agents-${appearance.name.lowercase()}-${if (isDark) "dark" else "light"}.png")
                    .outputStream().use { bitmap.compress(Bitmap.CompressFormat.PNG, 100, it) }
                assertEquals("screenshots do not prefetch the entire collection", 1, repository.pages)
            }
        }
    }

    private fun model(repository: SessionsRepository, listContext: CoroutineContext = EmptyCoroutineContext,
        display: AgentsDisplayHubs = AgentsDisplayHubs { kotlinx.coroutines.flow.flowOf(emptyList()) }) = AgentsViewModel(repository, object : InteractionCenter {
        override val pending = MutableStateFlow<List<Interaction>>(emptyList())
        override suspend fun refresh() = Unit
        override suspend fun resolve(id: String, resolution: Resolution) = Unit
    }, object : HubConnection {
        override val state = MutableStateFlow(ConnectionState.Online)
        override val events = MutableSharedFlow<ServerEvent>()
        override fun reconnect() = Unit
        override fun send(message: JsonObject) = true
    }, object : MessageSender {
        override val outbox = MutableStateFlow<List<OutboxMessage>>(emptyList())
        override suspend fun send(session: SessionKey, text: String, clientMessageId: String, clearDraft: String?) = Unit
    }, AgentsPreferences(object : DataStore<Preferences> {
        override val data = MutableStateFlow<Preferences>(emptyPreferences())
        override suspend fun updateData(transform: suspend (Preferences) -> Preferences) = transform(data.value).also { data.value = it }
    }, object : HubPreferences {
        override val preferences = MutableStateFlow(HubPreferencesSnapshot())
        override suspend fun refresh() = Unit
        override suspend fun togglePin(path: String, space: Boolean) = Unit
        override suspend fun toggleMute(path: String, space: Boolean) = Unit
        override suspend fun markSeen(session: SessionKey, version: String) = Unit
    }, display), listContext = listContext).also { models += it }

    private class FailedTransport : ChatTransport {
        var pageReads = 0
        override suspend fun sessionPage(filter: SessionListFilter, cursor: String?, since: String?): SessionListPage {
            pageReads++
            throw java.io.IOException("HTTP 500")
        }
        override suspend fun sessions(): List<Session> = throw java.io.IOException("HTTP 500")
        override suspend fun machines() = emptyList<Machine>()
        override suspend fun items(session: SessionKey, query: ItemsQuery) = ItemsResponse()
        override suspend fun interactions() = emptyList<Interaction>()
        override suspend fun resolve(id: String, resolution: Resolution) = Unit
        override suspend fun image(session: SessionKey, itemId: String) = byteArrayOf()
        override suspend fun fullItem(session: SessionKey, itemId: String) = Item(itemId)
    }

    private object Nowhere : Navigator {
        override fun navigate(key: NavKey) = Unit
        override fun back() = Unit
        override fun selectTab(tab: AppTab) = Unit
    }
}
