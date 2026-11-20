package persona.feature.agents

import androidx.datastore.core.DataStore
import androidx.datastore.preferences.core.*
import persona.core.contracts.SessionKey
import persona.core.contracts.Hello
import persona.core.contracts.api.*
import persona.core.domain.sort.AgentGrouping
import persona.core.domain.sort.AgentListDisplay
import persona.core.domain.sort.AgentRowStyle
import persona.core.domain.sort.AgentSort
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.first
import kotlinx.coroutines.launch
import kotlinx.coroutines.test.runTest
import kotlinx.coroutines.test.runCurrent
import org.junit.Assert.assertEquals
import org.junit.Test

class AgentsPreferencesTest {
    @Test fun storedLegacyValuesReadAsTheNewDefaultAndStateRemainsSelectable() = runTest {
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
        val preferences = AgentsPreferences(store, hub)
        assertEquals("a phone that never chose a grouping gets 按仓库", AgentGrouping.Repo, preferences.snapshots.first().grouping)
        for (value in listOf("Space", "removed-value")) {
            store.data.value = preferencesOf(stringPreferencesKey("grouping") to value)
            assertEquals(AgentGrouping.MachineSpace, preferences.snapshots.first().grouping)
        }
        store.data.value = preferencesOf(stringPreferencesKey("grouping") to "Machine")
        assertEquals(AgentGrouping.Machine, preferences.snapshots.first().grouping)
        preferences.grouping(AgentGrouping.State)
        assertEquals(AgentGrouping.State, preferences.snapshots.first().grouping)
        preferences.grouping(AgentGrouping.MachineSpace)
        assertEquals("MachineSpace", store.data.value[stringPreferencesKey("grouping")])
        assertEquals(AgentGrouping.MachineSpace, preferences.snapshots.first().grouping)
        // "Status first" until "Recently updated" is chosen.
        assertEquals(AgentSort.State, preferences.snapshots.first().sort)
        assertEquals(emptySet<String?>(), preferences.snapshots.first().hiddenBotmuxHubs)
        preferences.sort(AgentSort.Updated)
        assertEquals(AgentSort.Updated, preferences.snapshots.first().sort)
        store.data.value = preferencesOf(stringPreferencesKey("sort") to "removed-value")
        assertEquals(AgentSort.State, preferences.snapshots.first().sort)
    }

    @OptIn(kotlinx.coroutines.ExperimentalCoroutinesApi::class)
    @Test fun pairedValuesWinAndFailedLegacyMigrationsStayWithTheirOriginalPairing() = runTest {
        val legacy = booleanPreferencesKey("hide-botmux")
        val values = MutableStateFlow<Preferences>(preferencesOf(legacy to true))
        val store = object : DataStore<Preferences> {
            override val data = values
            override suspend fun updateData(transform: suspend (Preferences) -> Preferences) =
                transform(values.value).also { values.value = it }
        }
        val pins = object : HubPreferences {
            override val preferences = MutableStateFlow(HubPreferencesSnapshot())
            override suspend fun refresh() = Unit
            override suspend fun togglePin(path: String, space: Boolean) = Unit
            override suspend fun toggleMute(path: String, space: Boolean) = Unit
            override suspend fun markSeen(session: SessionKey, version: String) = Unit
        }
        class Api(var setting: AgentsDisplaySettings) : AgentDisplayApi {
            var reads = 0
            var writes = 0
            var readFails = false
            var writeFails = false
            var concurrentValue: Boolean? = null
            override suspend fun read(): AgentsDisplaySettings {
                reads++
                if (readFails) error("Endpoint unavailable")
                return setting
            }
            override suspend fun migrateHidden(): AgentsDisplaySettings {
                writes++
                if (writeFails) error("Write unavailable")
                setting = AgentsDisplaySettings(concurrentValue ?: false, true)
                return setting
            }
        }
        val socket = MutableStateFlow<SocketHello?>(SocketHello(1, Hello(protocol = 1, serverVersion = "fixture")))
        val first = Api(AgentsDisplaySettings(true, false)).apply { readFails = true }
        val second = Api(AgentsDisplaySettings(false, true))
        val firstPair = AgentsDisplayHub("scope-a", null, true, socket, first)
        val secondPair = AgentsDisplayHub("scope-b", "g11111111", false, socket, second)
        val pairs = MutableStateFlow(listOf(firstPair, secondPair))
        val reload = MutableStateFlow(0)
        val prefs = AgentsPreferences(store, pins, AgentsDisplayHubs { pairs })
        backgroundScope.launch { prefs.watchDisplaySettings(reload) }
        runCurrent()
        assertEquals(true, values.value[legacy])
        assertEquals(setOf(null, secondPair.namespace), prefs.snapshots.first().hiddenBotmuxHubs)

        // The old Hub does not implement GET. Switching the selected pairing must not redirect the pending migration.
        pairs.value = listOf(firstPair.copy(selected = false), secondPair.copy(selected = true))
        runCurrent()
        assertEquals(true, values.value[legacy])
        assertEquals(0, second.writes)
        first.readFails = false
        first.writeFails = true
        reload.value++
        runCurrent()
        assertEquals(1, first.writes)
        assertEquals(true, values.value[legacy])

        // The server's atomic PUT returns the other client's explicit true.
        first.writeFails = false
        first.concurrentValue = true
        socket.value = SocketHello(2, socket.value!!.hello)
        runCurrent()
        assertEquals(2, first.writes)
        assertEquals(null, values.value[legacy])
        assertEquals(setOf(secondPair.namespace), prefs.snapshots.first().hiddenBotmuxHubs)

        // An explicit response to GET needs no migration, including after the app restores a pending legacy key.
        values.value = values.value.toMutablePreferences().apply { this[legacy] = true }
        reload.value++
        runCurrent()
        assertEquals(0, second.writes)
        assertEquals(null, values.value[legacy])
        second.setting = AgentsDisplaySettings(true, true)
        reload.value++
        runCurrent()
        assertEquals(emptySet<String?>(), prefs.snapshots.first().hiddenBotmuxHubs)
        assertEquals(2, first.writes)

        // Re-pairing under the same plain row namespace does not reuse the previous pairing's cached decision.
        val unsupported = Api(AgentsDisplaySettings(false, true)).apply { readFails = true }
        pairs.value = listOf(firstPair.copy(scope = "fresh", api = unsupported, selected = true))
        runCurrent()
        assertEquals(emptySet<String?>(), prefs.snapshots.first().hiddenBotmuxHubs)
        assertEquals(0, unsupported.writes)
    }

    /**
     * The row styles (#4338 A9) are kept in the two values iOS keeps them in: the `compact` the phone always kept, and the
     * style. No one's choice is lost: Compact stays Compact List, Detailed reads as Avatar (the row Glass's Detailed list
     * drew), and Compact List keeps the style chosen before it.
     */
    @Test fun theDisplayKeepsTheCompactChoiceAndTheStyleUnderIt() = runTest {
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
        val preferences = AgentsPreferences(store, hub)
        suspend fun shown() = preferences.snapshots.first().display.also { assertEquals(it, preferences.display.first()) }
        // A phone that never chose: Avatar.
        assertEquals(AgentListDisplay.Avatar, shown())
        // Chosen before the styles came: Compact is Compact List, Detailed is Avatar.
        store.data.value = preferencesOf(booleanPreferencesKey("compact") to true)
        assertEquals(AgentListDisplay.CompactList, shown())
        store.data.value = preferencesOf(booleanPreferencesKey("compact") to false)
        assertEquals(AgentListDisplay.Avatar, shown())
        // A style, then Compact List over it: the style stays and comes back when the list is not compact.
        preferences.setDisplay(AgentListDisplay.Cards)
        assertEquals(AgentListDisplay.Cards, shown())
        assertEquals(false, store.data.value[booleanPreferencesKey("compact")])
        assertEquals("Cards", store.data.value[stringPreferencesKey("row-style")])
        preferences.setDisplay(AgentListDisplay.CompactList)
        assertEquals(AgentListDisplay.CompactList, shown())
        assertEquals(AgentRowStyle.Cards, preferences.snapshots.first().rowStyle)
        // Brief and Board's Compact / Detailed switch the same value.
        preferences.compact(false)
        assertEquals(AgentListDisplay.Cards, shown())
        preferences.setDisplay(AgentListDisplay.TwoLines)
        assertEquals(AgentListDisplay.TwoLines, shown())
        preferences.compact(true)
        assertEquals(AgentListDisplay.CompactList, shown())
        // A style this version doesn't know reads as Avatar.
        store.data.value = preferencesOf(stringPreferencesKey("row-style") to "removed-value")
        assertEquals(AgentListDisplay.Avatar, shown())
    }
}
