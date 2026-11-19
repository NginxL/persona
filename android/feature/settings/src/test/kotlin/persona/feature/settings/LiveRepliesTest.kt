package persona.feature.settings

import android.content.Context
import android.content.res.Resources
import androidx.compose.ui.test.assertIsOff
import androidx.compose.ui.test.assertIsOn
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onAllNodesWithTag
import androidx.compose.ui.test.onAllNodesWithText
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.test.core.app.ApplicationProvider
import androidx.test.ext.junit.runners.AndroidJUnit4
import kotlinx.coroutines.CompletableDeferred
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.launch
import kotlinx.coroutines.runBlocking
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import persona.core.contracts.Machine
import persona.core.contracts.Session
import persona.core.contracts.api.AgentLauncher
import persona.core.contracts.api.ClaudeSetting
import persona.core.contracts.api.HerdrState
import persona.core.contracts.api.LauncherQuotaAnswer
import persona.core.contracts.api.MachineManagement
import persona.core.contracts.api.SessionsRepository
import persona.core.designsystem.PersonaTheme
import persona.core.hub.HubError
import persona.core.hub.HubFailure

@RunWith(AndroidJUnit4::class)
@Config(sdk = [35])
class LiveRepliesTest {
    @get:Rule val rule = createComposeRule()

    private val res: Resources get() = ApplicationProvider.getApplicationContext<Context>().resources
    private val box = Machine("box", name = "Box", kind = "local", online = true)
    private val studio = Machine("studio", name = "Studio", kind = "ssh", online = true)

    private class FakeApi : MachineManagement {
        val reads = mutableListOf<String>()
        val writes = mutableListOf<Pair<String, Boolean>>()
        var readResult: suspend (String) -> Boolean? = { true }
        var writeResult: suspend (String, Boolean) -> Boolean = { _, on -> on }
        var settingsList = listOf(ClaudeSetting("verbose", "详细输出", type = "bool"))

        override suspend fun claudeLiveReplies(machineId: String): Boolean? {
            reads += machineId
            return readResult(machineId)
        }

        override suspend fun setClaudeLiveReplies(machineId: String, on: Boolean): Boolean {
            writes += machineId to on
            return writeResult(machineId, on)
        }

        override suspend fun claudeSettings(machineId: String) = settingsList
        override suspend fun setClaudeSetting(machineId: String, key: String, value: kotlinx.serialization.json.JsonElement) = settingsList
        override suspend fun codexSettings(machineId: String) = emptyList<ClaudeSetting>()
        override suspend fun setCodexSetting(machineId: String, key: String, value: kotlinx.serialization.json.JsonElement) = emptyList<ClaudeSetting>()
        override suspend fun launchers(machineId: String) = emptyList<AgentLauncher>()
        override suspend fun launcherQuota(machineId: String, name: String, refresh: Boolean) = LauncherQuotaAnswer()
        override suspend fun reconnect(machineId: String) = Unit
        override suspend fun deploy(machineId: String) = Unit
        override suspend fun herdr(machineId: String): HerdrState = error("unused")
        override suspend fun startHerdr(machineId: String): HerdrState = error("unused")
        override suspend fun setEngine(machineId: String, engine: String) = Machine(machineId)
    }

    @Test fun readsSwitchWhenLoaded() = runBlocking {
        val api = FakeApi().apply { readResult = { false } }
        val model = LiveRepliesModel(api)
        assertNull(model.state.value.on)
        model.load("box")
        assertEquals(false, model.state.value.on)
        assertNull(model.state.value.loadError)
        assertFalse(model.state.value.unsupported)
        assertFalse(model.state.value.loading)
    }

    @Test fun changeMovesImmediatelyTracksSavingAndKeepsHubAnswer() = runBlocking {
        val gate = CompletableDeferred<Boolean>()
        val api = FakeApi().apply {
            readResult = { true }
            writeResult = { _, _ -> gate.await() }
        }
        val model = LiveRepliesModel(api)
        model.load("box")
        assertEquals(true, model.state.value.on)

        var notice: ResText? = null
        val job = launch { notice = model.set(false) }
        while (!model.state.value.saving) kotlinx.coroutines.yield()

        // Switch flipped at once and rejects a concurrent write while saving.
        assertEquals(false, model.state.value.on)
        assertTrue(model.state.value.saving)
        assertNull(model.set(true))
        assertEquals(listOf("box" to false), api.writes)

        gate.complete(false)
        job.join()
        assertEquals(false, model.state.value.on)
        assertFalse(model.state.value.saving)
        assertNull(model.state.value.error)
        assertEquals("已保存", notice?.resolve(res))
    }

    @Test fun failedChangeRestoresPreviousValueAndReportsReason() = runBlocking {
        val api = FakeApi().apply {
            readResult = { true }
            writeResult = { _, _ -> throw HubFailure(HubError.Server, "写不进设置", 500) }
        }
        val model = LiveRepliesModel(api)
        model.load("box")
        val notice = model.set(false, shownName = "Box")
        assertNull(notice)
        assertEquals(true, model.state.value.on)
        assertFalse(model.state.value.saving)
        assertEquals("没有全部保存：\nBox：写不进设置", model.state.value.error?.resolve(res))
    }

    @Test fun applyToAllWritesOtherMachinesAndReportsIndividualFailures() = runBlocking {
        val api = FakeApi().apply {
            readResult = { true }
            writeResult = { id, on ->
                if (id == "studio") throw HubFailure(HubError.Server, "离线", 503)
                on
            }
        }
        val model = LiveRepliesModel(api)
        model.load("box")
        val partialFailure = model.set(false, others = listOf(studio, box))
        assertNull(partialFailure)
        assertEquals(listOf("box" to false, "studio" to false), api.writes)
        assertEquals(false, model.state.value.on)
        assertEquals("没有全部保存：\nStudio：离线", model.state.value.error?.resolve(res))

        // When every machine succeeds, the notice names how many were updated.
        api.writes.clear()
        api.writeResult = { _, on -> on }
        val allSaved = model.set(true, others = listOf(studio))
        assertEquals(listOf("box" to true, "studio" to true), api.writes)
        assertEquals(true, model.state.value.on)
        assertNull(model.state.value.error)
        assertEquals("已应用到 2 台机器", allSaved?.resolve(res))
    }

    @Test fun aReadingOfThePreviousComputerDoesNotArriveOverTheOnePickedNext() = runBlocking {
        val held = CompletableDeferred<Boolean?>()
        val api = FakeApi().apply { readResult = { if (it == "box") held.await() else false } }
        val model = LiveRepliesModel(api)
        val slow = launch { model.load("box") }
        while (api.reads.isEmpty()) kotlinx.coroutines.yield()

        // The owner picked another computer before the first one answered, then the first answer lands.
        model.load("studio")
        assertEquals("studio", model.state.value.machineId)
        assertEquals(false, model.state.value.on)
        held.complete(true)
        slow.join()
        assertEquals("studio", model.state.value.machineId)
        assertEquals(false, model.state.value.on)
        assertFalse(model.state.value.loading)
        assertNull(model.state.value.loadError)
    }

    @Test fun aSaveOnThePreviousComputerFinishingLateDoesNotArriveOverTheOnePickedNext() = runBlocking {
        val held = CompletableDeferred<Boolean>()
        val api = FakeApi().apply {
            readResult = { it == "box" }
            writeResult = { id, on -> if (id == "box") held.await() else on }
        }
        val model = LiveRepliesModel(api)
        model.load("box")
        val saving = launch { model.set(false) }
        while (!model.state.value.saving) kotlinx.coroutines.yield()

        // Another computer is picked while the first one's write is still out; its answer must not move this switch.
        model.load("studio")
        assertEquals("studio", model.state.value.machineId)
        held.complete(true)
        saving.join()
        assertEquals("studio", model.state.value.machineId)
        assertEquals(false, model.state.value.on)
        assertFalse(model.state.value.saving)
        assertNull(model.state.value.error)
    }

    @Test fun anotherComputersReadingIsDrawnAsThisOneStillReading() {
        val boxState = LiveRepliesUiState(machineId = "box", on = true, saving = true, error = ResText.of(R.string.settings_src_saved))
        assertEquals(boxState, boxState.forMachine("box"))
        val picked = boxState.forMachine("studio")
        assertEquals("studio", picked.machineId)
        assertNull(picked.on)
        assertTrue(picked.loading)
        assertFalse(picked.saving)
        assertNull(picked.error)
        assertFalse(picked.unsupported)
    }

    @Test fun olderHubHidesSwitchAndIgnoresSet() = runBlocking {
        val api = FakeApi().apply { readResult = { null } }
        val model = LiveRepliesModel(api)
        model.load("box")
        assertTrue(model.state.value.unsupported)
        assertNull(model.state.value.on)
        assertNull(model.state.value.loadError)
        assertNull(model.set(false))
        assertTrue(api.writes.isEmpty())
    }

    @Test fun failedLoadShowsRetryAndKeepsExistingSwitchOnRefreshFailure() = runBlocking {
        var fail = true
        val api = FakeApi().apply {
            readResult = {
                if (fail) throw HubFailure(HubError.Timeout, "timed out")
                true
            }
        }
        val model = LiveRepliesModel(api)
        model.load("box")
        assertNull(model.state.value.on)
        assertEquals("读不到这台电脑的设置：连不上 hub", model.state.value.loadError?.resolve(res))

        fail = false
        model.load("box")
        assertEquals(true, model.state.value.on)
        assertNull(model.state.value.loadError)

        // Refresh failure while already shown keeps the current switch value.
        fail = true
        model.load("box")
        assertEquals(true, model.state.value.on)
        assertNull(model.state.value.loadError)
    }

    @Test fun claudeSettingsPageFollowsApplyAllAndHidesForCodexOr404() {
        val api = FakeApi()
        val sessions = object : SessionsRepository {
            override val machines = MutableStateFlow(listOf(box, studio))
            override val sessions = MutableStateFlow(emptyList<Session>())
            override suspend fun refresh() {}
        }
        rule.setContent {
            PersonaTheme {
                ClaudeSettingsPage(sessions, api, onBack = {}, kind = AgentSettingsKind.Claude)
            }
        }
        rule.waitUntil(5_000) { rule.onAllNodesWithTag("live-replies").fetchSemanticsNodes().isNotEmpty() }
        rule.onNodeWithTag("live-replies").assertIsOn()
        // Default applyAll is true: toggling writes to both box and studio.
        rule.onNodeWithTag("live-replies").performClick()
        rule.waitForIdle()
        rule.onNodeWithTag("live-replies").assertIsOff()
        assertEquals(listOf("box" to false, "studio" to false), api.writes)

        // Turn applyAll off: toggling writes only to box.
        api.writes.clear()
        rule.onNodeWithText("同时改所有在线机器").performClick()
        rule.onNodeWithTag("live-replies").performClick()
        rule.waitForIdle()
        rule.onNodeWithTag("live-replies").assertIsOn()
        assertEquals(listOf("box" to true), api.writes)
    }

    @Test fun codexPageHidesLiveRepliesSection() {
        val api = FakeApi()
        val sessions = object : SessionsRepository {
            override val machines = MutableStateFlow(listOf(box))
            override val sessions = MutableStateFlow(emptyList<Session>())
            override suspend fun refresh() {}
        }
        rule.setContent {
            PersonaTheme {
                ClaudeSettingsPage(sessions, api, onBack = {}, kind = AgentSettingsKind.Codex)
            }
        }
        rule.waitForIdle()
        assertTrue(rule.onAllNodesWithText("实时回复").fetchSemanticsNodes().isEmpty())
        assertTrue(api.reads.isEmpty())
    }

    @Test fun agyPageHidesLiveRepliesSection() {
        val api = FakeApi()
        val sessions = object : SessionsRepository {
            override val machines = MutableStateFlow(listOf(box))
            override val sessions = MutableStateFlow(emptyList<Session>())
            override suspend fun refresh() {}
        }
        rule.setContent {
            PersonaTheme {
                ClaudeSettingsPage(sessions, api, onBack = {}, kind = AgentSettingsKind.Agy)
            }
        }
        rule.waitForIdle()
        assertTrue(rule.onAllNodesWithText("实时回复").fetchSemanticsNodes().isEmpty())
        assertTrue(api.reads.isEmpty())
    }
}
