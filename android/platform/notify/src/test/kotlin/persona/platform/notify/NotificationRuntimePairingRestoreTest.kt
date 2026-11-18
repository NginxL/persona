package persona.platform.notify

import android.app.Application
import android.content.Context
import androidx.work.testing.WorkManagerTestInitHelper
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.ExperimentalCoroutinesApi
import kotlinx.coroutines.async
import kotlinx.coroutines.cancel
import kotlinx.coroutines.flow.MutableSharedFlow
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.first
import kotlinx.coroutines.test.StandardTestDispatcher
import kotlinx.coroutines.test.advanceUntilIdle
import kotlinx.coroutines.test.resetMain
import kotlinx.coroutines.test.runCurrent
import kotlinx.coroutines.test.runTest
import kotlinx.coroutines.test.setMain
import kotlinx.coroutines.withContext
import kotlinx.coroutines.withTimeout
import kotlinx.serialization.json.JsonObject
import org.junit.After
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Before
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.RuntimeEnvironment
import org.robolectric.Shadows.shadowOf
import org.robolectric.annotation.Config
import org.robolectric.util.ReflectionHelpers
import persona.core.contracts.FleetState
import persona.core.contracts.JsonCodec
import persona.core.contracts.Machine
import persona.core.contracts.ServerEvent
import persona.core.contracts.Session
import persona.core.contracts.api.ConnectionState
import persona.core.contracts.api.DeviceNotificationPreferences
import persona.core.contracts.api.HubConnection

/** #4055 AC2: a transient, not-yet-restored false pairing is not an unpair event or permission to resume early. */
@OptIn(ExperimentalCoroutinesApi::class)
@RunWith(RobolectricTestRunner::class)
@Config(sdk = [34])
class NotificationRuntimePairingRestoreTest {
    private lateinit var app: Application
    private val scopes = mutableListOf<CoroutineScope>()
    private var wasRunning = false

    @Before fun setup() {
        app = RuntimeEnvironment.getApplication()
        app.getSharedPreferences("notify_recovery", Context.MODE_PRIVATE).edit().clear().commit()
        app.getSharedPreferences("fleet", Context.MODE_PRIVATE).edit().clear().commit()
        WorkManagerTestInitHelper.initializeTestWorkManager(app)
        wasRunning = HubService.running
        ReflectionHelpers.setStaticField(HubService::class.java, "running", false)
        resetForegroundStartsForTest()
    }

    @After fun cleanup() {
        scopes.forEach { it.cancel() }
        ReflectionHelpers.setStaticField(HubService::class.java, "running", wasRunning)
        resetForegroundStartsForTest()
        Dispatchers.resetMain()
        WorkManagerTestInitHelper.closeWorkDatabase()
    }

    @Test fun initialFalseCollectorDoesNotStopTheRestoredResidentService() = runTest {
        Dispatchers.setMain(StandardTestDispatcher(testScheduler))
        val pairing = RestoringPairingService()
        val fixture = fixture(pairing)
        ReflectionHelpers.setStaticField(HubService::class.java, "running", true)

        fixture.runtime.start()
        runCurrent()

        assertNull(
            "Restore pending must not be collected as an unpair that stops the resident service",
            shadowOf(app).nextStoppedService,
        )
        assertTrue("NotificationRuntime must await pairing restoration", pairing.restoreStarted.isCompleted)

        pairing.completeRestore()
        advanceUntilIdle()
    }

    @Test fun foregroundWaitsForRestoreBeforeResumingTheConnection() = runTest {
        Dispatchers.setMain(StandardTestDispatcher(testScheduler))
        markUserStopped()
        val pairing = RestoringPairingService()
        val fixture = fixture(pairing, started = true)

        fixture.runtime.onForeground()
        runCurrent()

        assertTrue("Foreground must not clear the user's stopped marker before restore", fixture.journal.userStopped)
        assertNull("Foreground must not start a service from transient empty pairings", shadowOf(app).nextStartedService)
        assertTrue("Foreground must be waiting in pairing restore", pairing.restoreStarted.isCompleted)

        pairing.completeRestore()
        advanceUntilIdle()

        assertFalse("The restored foreground return must resume notifications", fixture.journal.userStopped)
        assertNotNull("The restored foreground return must start the resident service", shadowOf(app).nextStartedService)
    }

    @Test fun resumeWaitsForRestoreBeforeClearingTheStoppedMarker() = runTest {
        Dispatchers.setMain(StandardTestDispatcher(testScheduler))
        markUserStopped()
        val pairing = RestoringPairingService()
        val fixture = fixture(pairing, started = true)

        fixture.runtime.resume()
        runCurrent()

        assertTrue("Resume must not release a persisted stop while restoration is pending", fixture.journal.userStopped)
        assertNull(shadowOf(app).nextStartedService)
        assertTrue("Resume must be waiting in pairing restore", pairing.restoreStarted.isCompleted)

        pairing.completeRestore()
        advanceUntilIdle()

        assertFalse(fixture.journal.userStopped)
        assertNotNull("Resume must continue after the pairing is restored", shadowOf(app).nextStartedService)
    }

    @Test fun fleetStartDoesNotEraseASavedFleetOnTheInitialFalsePairing() = runTest {
        Dispatchers.setMain(StandardTestDispatcher(testScheduler))
        val saved = FleetState(phase = "working", working = 1)
        val savedJson = JsonCodec.encodeToString(FleetState.serializer(), saved)
        val preferences = app.getSharedPreferences("fleet", Context.MODE_PRIVATE)
        preferences.edit().putString("pairing", "hub/generation").putString("snapshot", savedJson).commit()
        val pairing = RestoringPairingService()
        var restoredScope: String? = null
        val connection = connection()
        val feed = FleetFeed(app, connection, pairing.service, object : FleetSource {
            override fun pairing() = restoredScope
            override suspend fun fetch() = saved
        })
        scopes += ReflectionHelpers.getField<CoroutineScope>(feed, "scope")

        feed.start()
        pairing.awaitRestore()
        assertEquals(
            "The initial false collector must not erase the restored pairing's saved fleet",
            savedJson,
            preferences.getString("snapshot", null),
        )

        restoredScope = "hub/generation"
        pairing.completeRestore()
        withContext(Dispatchers.IO) {
            withTimeout(5_000) { feed.state.first { it == saved } }
        }
        assertEquals(savedJson, preferences.getString("snapshot", null))
    }

    @Test fun fleetSettledReturnsAfterTheSavedSnapshotIsVisible() = runTest {
        Dispatchers.setMain(StandardTestDispatcher(testScheduler))
        val saved = FleetState(phase = "working", working = 1)
        val preferences = app.getSharedPreferences("fleet", Context.MODE_PRIVATE)
        preferences.edit().putString("pairing", "hub/generation")
            .putString("snapshot", JsonCodec.encodeToString(FleetState.serializer(), saved)).commit()
        val pairing = RestoringPairingService()
        var restoredScope: String? = null
        val feed = FleetFeed(app, connection(), pairing.service, object : FleetSource {
            override fun pairing() = restoredScope
            override suspend fun fetch() = saved
        })
        scopes += ReflectionHelpers.getField<CoroutineScope>(feed, "scope")

        val waiting = async { feed.settled(); feed.state.value }
        pairing.awaitRestore()
        assertTrue("Fleet readers must wait for the snapshot restore", waiting.isActive)

        restoredScope = "hub/generation"
        pairing.completeRestore()
        assertEquals(saved, withContext(Dispatchers.IO) { withTimeout(5_000) { waiting.await() } })
    }

    private fun markUserStopped() {
        app.getSharedPreferences("notify_recovery", Context.MODE_PRIVATE).edit().putBoolean("user_stopped", true).commit()
    }

    private fun connection(state: ConnectionState = ConnectionState.Online) = object : HubConnection {
        override val state = MutableStateFlow(state)
        override val events = MutableSharedFlow<ServerEvent>()
        var wakes = 0
        override fun reconnect() { wakes++ }
        override fun send(message: JsonObject) = true
    }

    private fun fixture(pairing: RestoringPairingService, started: Boolean = false): Fixture {
        val journal = ExitJournal(app)
        val sessions = object : persona.core.contracts.api.SessionsRepository {
            override val machines = MutableStateFlow<List<Machine>>(emptyList())
            override val sessions = MutableStateFlow<List<Session>>(emptyList())
            override suspend fun refresh() = Unit
        }
        val notifications = object : NotificationPreferencesSource {
            override val preferences = MutableStateFlow<DeviceNotificationPreferences?>(null)
            override suspend fun refreshPreferences() = preferences.value
        }
        val notifier = Notifier(app, sessions, notifications)
        val feed = FleetFeed(app, connection(), pairing.service, object : FleetSource {
            override fun pairing() = "hub/generation"
            override suspend fun fetch() = FleetState()
        })
        val runtime = NotificationRuntime(app, pairing.service, journal, notifier, feed, sessions, ResidentConnectionRequests(app))
        if (started) ReflectionHelpers.setField(runtime, "started", true)
        for (owner in listOf(notifier, feed, runtime)) scopes += ReflectionHelpers.getField<CoroutineScope>(owner, "scope")
        return Fixture(runtime, journal)
    }

    private data class Fixture(val runtime: NotificationRuntime, val journal: ExitJournal)
}
