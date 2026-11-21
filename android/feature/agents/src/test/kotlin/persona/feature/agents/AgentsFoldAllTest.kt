package persona.feature.agents

import androidx.datastore.core.DataStore
import androidx.datastore.preferences.core.Preferences
import androidx.datastore.preferences.core.emptyPreferences
import androidx.lifecycle.viewModelScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.ExperimentalCoroutinesApi
import kotlinx.coroutines.cancel
import kotlinx.coroutines.flow.MutableSharedFlow
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.first
import kotlinx.coroutines.launch
import kotlinx.coroutines.test.*
import kotlinx.serialization.json.JsonObject
import org.junit.After
import org.junit.Assert.*
import org.junit.Before
import org.junit.Test
import persona.core.contracts.*
import persona.core.contracts.api.*
import persona.core.designsystem.ListTapMotion
import persona.core.designsystem.ListTapMotionKind
import persona.core.domain.sort.*

@OptIn(ExperimentalCoroutinesApi::class)
class AgentsFoldAllTest {
    private lateinit var model: AgentsViewModel
    private val preferences = AgentsPreferences(object : DataStore<Preferences> {
        override val data = MutableStateFlow<Preferences>(emptyPreferences())
        override suspend fun updateData(transform: suspend (Preferences) -> Preferences): Preferences =
            transform(data.value).also { data.value = it }
    }, object : HubPreferences {
        override val preferences = MutableStateFlow(HubPreferencesSnapshot())
        override suspend fun refresh() = Unit
        override suspend fun togglePin(path: String, space: Boolean) = Unit
        override suspend fun toggleMute(path: String, space: Boolean) = Unit
        override suspend fun markSeen(session: SessionKey, version: String) = Unit
    })
    private val sessions = listOf("shown", "hidden").flatMap { machine ->
        (0..1).map { pane -> Session(machine, "pane-$pane", state = "working", spaceId = "space", spaceName = "project",
            slotId = "tab", slotIndex = 1, paneId = "space:p$pane", paneIndex = pane) }
    }

    @Before fun setMain() { Dispatchers.setMain(StandardTestDispatcher()) }
    @After fun tearDown() {
        if (::model.isInitialized) model.viewModelScope.cancel()
        Dispatchers.resetMain()
    }

    private suspend fun TestScope.startModel(extra: List<Session> = emptyList()) {
        preferences.grouping(AgentGrouping.MachineSpace)
        model = AgentsViewModel(object : SessionsRepository {
            override val sessions = MutableStateFlow(this@AgentsFoldAllTest.sessions + extra)
            override val machines = MutableStateFlow(listOf(Machine("shown", online = true), Machine("hidden", online = true)))
            override suspend fun refresh() = Unit
        }, object : InteractionCenter {
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
        }, preferences)
        backgroundScope.launch(UnconfinedTestDispatcher(testScheduler)) { model.page.collect {} }
        runCurrent()
    }

    // "Expand all" opens a tab group folded by hand, as it opens every space (#2792); "Collapse all" leaves tab groups alone
    @Test fun expandAllOpensATabGroupFoldedByHand() = runTest {
        startModel()
        model.machine("shown")
        runCurrent()
        val tab = agentTabs(model.page.value.list.groups.single().rows).values.single()
        model.foldTab(tab)
        runCurrent()
        assertEquals(setOf(tab.foldKey), preferences.snapshots.first().foldedTabs)
        model.foldAll(true)
        runCurrent()
        assertEquals(setOf(tab.foldKey), preferences.snapshots.first().foldedTabs)
        model.foldAll(false)
        runCurrent()
        assertTrue(preferences.snapshots.first().foldedTabs.isEmpty())
        val page = model.page.value
        val tree = agentTreeEntries(page.list, page.settings.machineFilter, tabs = true, foldedTabs = page.preferences.foldedTabs)
        assertFalse(tree.filterIsInstance<TreeTabEntry>().single().folded)
    }

    // #4338 A6, as iOS: folds stay through a search, a range change and a grouping change, and come back after a relaunch.
    @Test fun foldsAreKeptThroughSearchRangeGroupingAndRelaunch() = runTest {
        startModel()
        val id = model.page.value.list.groups.first().id
        model.fold(model.page.value.list.groups.first())
        runCurrent()
        assertEquals(setOf(id), model.page.value.settings.folded)
        assertEquals(setOf(id), preferences.snapshots.first().folds[AgentGrouping.MachineSpace])
        // A search opens every fold while it runs and gives the kept ones back when it clears.
        model.query("pane")
        runCurrent()
        assertTrue(model.page.value.settings.folded.isEmpty())
        model.query("")
        runCurrent()
        assertEquals(setOf(id), model.page.value.settings.folded)
        model.range(AgentRange.All)
        runCurrent()
        assertEquals(setOf(id), model.page.value.settings.folded)
        // Another grouping has folds of its own; coming back brings these back.
        model.grouping(AgentGrouping.State)
        runCurrent()
        assertFalse(id in model.page.value.settings.folded)
        model.grouping(AgentGrouping.MachineSpace)
        runCurrent()
        assertEquals(setOf(id), model.page.value.settings.folded)
        // A new model, as after a relaunch, reads them back.
        model.viewModelScope.cancel()
        startModel()
        assertEquals(setOf(id), model.page.value.settings.folded)
    }

    // #4338 A5, as iOS: By Status's Seen starts folded and opens from its header; opening it is kept, "Expand all" opens it,
    // and rows under a section that names their state leave the word out (Muted mixes states, so its rows keep it).
    @Test fun byStatusSeenOpensFromItsHeaderAndRowsLeaveTheirStateOut() = runTest {
        startModel(listOf(Session("shown", "seen", state = "idle", title = "Seen one")))
        model.grouping(AgentGrouping.State)
        runCurrent()
        fun seen() = model.page.value.list.groups.single { it.id == StateSeenGroup }
        assertTrue(seen().folded)
        model.fold(seen())
        runCurrent()
        assertFalse(seen().folded)
        assertEquals(setOf(StateSeenOpened), preferences.snapshots.first().folds[AgentGrouping.State])
        model.fold(seen())
        runCurrent()
        assertTrue(seen().folded)
        assertTrue(preferences.snapshots.first().folds[AgentGrouping.State].orEmpty().isEmpty())
        model.foldAll(true)
        runCurrent()
        model.foldAll(false)
        runCurrent()
        assertFalse(seen().folded)
        val rows = agentTreeEntries(model.page.value.list, "").filterIsInstance<TreeRowEntry>()
        assertTrue(rows.isNotEmpty())
        assertTrue(rows.all { it.inStateSection })
    }

    // #4338 A11, as iOS's Collapse All: the sections fold, the computer headings stay as they are.
    @Test fun collapseAllLeavesTheComputerHeadingsAsTheyAre() = runTest {
        startModel()
        model.foldAll(true)
        runCurrent()
        assertTrue(model.page.value.list.machines.isNotEmpty())
        assertTrue(model.page.value.list.machines.none { it.folded })
        assertTrue(model.page.value.list.groups.all { it.folded })
        // The list settings say Collapse all is done, the computers open as they are.
        assertTrue(model.page.value.list.allCollapsed)
        model.foldAll(false)
        runCurrent()
        val hidden = model.page.value.list.machines.single { it.machine.id == "hidden" }
        model.fold(hidden)
        runCurrent()
        model.foldAll(true)
        runCurrent()
        assertTrue(model.page.value.list.machines.single { it.machine.id == "hidden" }.folded)
        assertFalse(model.page.value.list.machines.single { it.machine.id == "shown" }.folded)
    }

    @Test fun collapseAllArmsEachComposedList() = runTest {
        startModel()
        val page = ListTapMotion()
        val overlay = ListTapMotion()
        model.attachListTap(page)
        model.attachListTap(overlay)
        model.foldAll(true)
        assertEquals(ListTapMotionKind.State, page.kind)
        assertEquals(ListTapMotionKind.State, overlay.kind)
        model.detachListTap(overlay)
        val overlayGeneration = overlay.generation
        model.foldAll(false)
        assertEquals(overlayGeneration, overlay.generation)
        assertEquals(ListTapMotionKind.State, page.kind)
    }

    @Test fun aHubPushRacingTheTapDoesNotTakeIt() = runTest {
        Dispatchers.setMain(StandardTestDispatcher(testScheduler))
        val rows = MutableStateFlow(sessions)
        val list = StandardTestDispatcher(testScheduler)
        preferences.grouping(AgentGrouping.MachineSpace)
        model = AgentsViewModel(object : SessionsRepository {
            override val sessions = rows
            override val machines = MutableStateFlow(listOf(Machine("shown", online = true), Machine("hidden", online = true)))
            override suspend fun refresh() = Unit
        }, object : InteractionCenter {
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
        }, preferences, listContext = list)
        val motion = ListTapMotion()
        val accepted = mutableListOf<AgentsPageState>()
        backgroundScope.launch(UnconfinedTestDispatcher(testScheduler)) {
            model.page.collect { page ->
                val before = motion.landings
                motion.land(page.tapEpoch)
                if (motion.kind != null && motion.landings != before) accepted += page
            }
        }
        model.attachListTap(motion)
        runCurrent()
        advanceTimeBy(20)
        runCurrent()
        rows.value = sessions + Session("shown", "hub-racer", state = "working", spaceId = "space", spaceName = "project",
            slotId = "tab", slotIndex = 1, paneId = "space:hub", paneIndex = 2)
        runCurrent()
        model.foldAll(true)
        advanceTimeBy(50)
        runCurrent()
        assertEquals(ListTapMotionKind.State, motion.kind)
        assertTrue("the tap's page was still held: ${accepted.map { it.tapEpoch }}", accepted.isNotEmpty())
        assertTrue(accepted.all { it.tapEpoch > 0 })
        assertTrue(accepted.first().list.groups.isNotEmpty() && accepted.first().list.groups.all { it.folded })
        advanceTimeBy(400)
        runCurrent()
        assertTrue(model.page.value.sessions.any { it.sessionId == "hub-racer" })
    }

    @Test fun choosingAComputerDoesNotArm() = runTest {
        startModel()
        val motion = ListTapMotion()
        model.attachListTap(motion)
        model.machine("shown")
        assertNull(motion.kind)
        model.machine("")
        assertNull(motion.kind)
    }

    @Test fun storedFoldsApplyOnVeryFirstPageEmissionWithoutUnfoldedFlash() = runTest {
        preferences.grouping(AgentGrouping.Repo)
        preferences.folds(AgentGrouping.Repo, setOf("pinned"))
        val pinnedSession = Session("shown", "p1", state = "working")
        val hubPrefs = AgentsPreferences(object : DataStore<Preferences> {
            override val data = MutableStateFlow<Preferences>(
                androidx.datastore.preferences.core.preferencesOf(
                    androidx.datastore.preferences.core.stringSetPreferencesKey("folds-Repo") to setOf("pinned"),
                )
            )
            override suspend fun updateData(transform: suspend (Preferences) -> Preferences) = transform(data.value).also { data.value = it }
        }, object : HubPreferences {
            override val preferences = MutableStateFlow(HubPreferencesSnapshot(pins = HubPreferenceSet(sessions = listOf("shown/p1"))))
            override suspend fun refresh() = Unit
            override suspend fun togglePin(path: String, space: Boolean) = Unit
            override suspend fun toggleMute(path: String, space: Boolean) = Unit
            override suspend fun markSeen(session: SessionKey, version: String) = Unit
        })
        model = AgentsViewModel(object : SessionsRepository {
            override val sessions = MutableStateFlow(listOf(pinnedSession))
            override val machines = MutableStateFlow(listOf(Machine("shown", online = true)))
            override suspend fun refresh() = Unit
        }, object : InteractionCenter {
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
        }, hubPrefs)
        val firstNonEmpty = model.page.first { it.list.groups.isNotEmpty() }
        val pinnedGroup = firstNonEmpty.list.groups.first { it.id == "pinned" }
        assertTrue("pinned group must already be folded on the very first emission", pinnedGroup.folded)
        assertTrue(pinnedGroup.visibleRows.isEmpty())
    }

    @Test fun unfoldingLastSectionUnderResolvedGroupingFallbackStaysExpanded() = runTest {
        val pinnedSession = Session("shown", "p1", state = "working")
        // Stored grouping is Todo (unsupported here, so resolvedGrouping falls back to Repo), with folds-Repo storing "pinned"
        val hubPrefs = AgentsPreferences(object : DataStore<Preferences> {
            override val data = MutableStateFlow<Preferences>(
                androidx.datastore.preferences.core.preferencesOf(
                    androidx.datastore.preferences.core.stringPreferencesKey("grouping") to AgentGrouping.Todo.name,
                    androidx.datastore.preferences.core.stringSetPreferencesKey("folds-Repo") to setOf("pinned"),
                    androidx.datastore.preferences.core.stringSetPreferencesKey("folds-Todo") to setOf("pinned"),
                )
            )
            override suspend fun updateData(transform: suspend (Preferences) -> Preferences) = transform(data.value).also { data.value = it }
        }, object : HubPreferences {
            override val preferences = MutableStateFlow(HubPreferencesSnapshot(pins = HubPreferenceSet(sessions = listOf("shown/p1"))))
            override suspend fun refresh() = Unit
            override suspend fun togglePin(path: String, space: Boolean) = Unit
            override suspend fun toggleMute(path: String, space: Boolean) = Unit
            override suspend fun markSeen(session: SessionKey, version: String) = Unit
        })
        model = AgentsViewModel(object : SessionsRepository {
            override val sessions = MutableStateFlow(listOf(pinnedSession))
            override val machines = MutableStateFlow(listOf(Machine("shown", online = true)))
            override suspend fun refresh() = Unit
        }, object : InteractionCenter {
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
        }, hubPrefs)
        backgroundScope.launch(UnconfinedTestDispatcher(testScheduler)) { model.page.collect {} }
        runCurrent()
        val initialGroup = model.page.value.list.groups.first { it.id == "pinned" }
        assertTrue(initialGroup.folded)

        // Unfolding the last folded section empties settings.folded and must stay unfolded
        model.fold(initialGroup)
        runCurrent()
        assertFalse(model.page.value.list.groups.first { it.id == "pinned" }.folded)
    }

    @Test fun pendingGroupingSwitchImmediatelyAppliesTargetGroupingStoredFolds() = runTest {
        val pinnedSession = Session("shown", "p1", state = "working")
        val saveGate = kotlinx.coroutines.CompletableDeferred<Unit>()
        val backing = MutableStateFlow<Preferences>(
            androidx.datastore.preferences.core.preferencesOf(
                androidx.datastore.preferences.core.stringPreferencesKey("grouping") to AgentGrouping.Repo.name,
                androidx.datastore.preferences.core.stringSetPreferencesKey("folds-MachineSpace") to setOf("pinned"),
            )
        )
        val slowPrefs = AgentsPreferences(object : DataStore<Preferences> {
            override val data = backing
            override suspend fun updateData(transform: suspend (Preferences) -> Preferences): Preferences {
                saveGate.await()
                return transform(backing.value).also { backing.value = it }
            }
        }, object : HubPreferences {
            override val preferences = MutableStateFlow(HubPreferencesSnapshot(pins = HubPreferenceSet(sessions = listOf("shown/p1"))))
            override suspend fun refresh() = Unit
            override suspend fun togglePin(path: String, space: Boolean) = Unit
            override suspend fun toggleMute(path: String, space: Boolean) = Unit
            override suspend fun markSeen(session: SessionKey, version: String) = Unit
        })
        model = AgentsViewModel(object : SessionsRepository {
            override val sessions = MutableStateFlow(listOf(pinnedSession))
            override val machines = MutableStateFlow(listOf(Machine("shown", online = true)))
            override suspend fun refresh() = Unit
        }, object : InteractionCenter {
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
        }, slowPrefs)
        backgroundScope.launch(UnconfinedTestDispatcher(testScheduler)) { model.page.collect {} }
        runCurrent()
        assertFalse(model.page.value.list.groups.first { it.id == "pinned" }.folded)

        // Switch to MachineSpace while DataStore write is still blocked: pending page must already have "pinned" folded
        model.grouping(AgentGrouping.MachineSpace)
        runCurrent()
        assertEquals(AgentGrouping.MachineSpace, model.page.value.settings.grouping)
        assertTrue(model.page.value.list.groups.first { it.id == "pinned" }.folded)
        saveGate.complete(Unit)
        runCurrent()
        assertTrue(model.page.value.list.groups.first { it.id == "pinned" }.folded)
    }
}
