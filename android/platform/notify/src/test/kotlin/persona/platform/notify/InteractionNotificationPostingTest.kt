package persona.platform.notify

import android.Manifest
import android.app.Application
import android.app.Notification
import android.app.NotificationManager
import androidx.test.core.app.ApplicationProvider
import androidx.test.ext.junit.runners.AndroidJUnit4
import kotlinx.coroutines.flow.MutableStateFlow
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.Shadows.shadowOf
import org.robolectric.annotation.Config
import persona.core.contracts.Interaction
import persona.core.contracts.JsonCodec
import persona.core.contracts.Machine
import persona.core.contracts.Session
import persona.core.contracts.api.DeviceNotificationPreferences
import persona.core.contracts.api.SessionsRepository
import persona.core.designsystem.PendingStatus
import persona.core.navigation.NotificationRoute

@RunWith(AndroidJUnit4::class)
@Config(sdk = [35])
class InteractionNotificationPostingTest {
    // Only the request itself counts: a verifying / result notice reusing the tag is the answer's own and stays.
    @Test fun resultNoticesAreNotRequests() {
        val app = ApplicationProvider.getApplicationContext<Application>()
        shadowOf(app).grantPermissions(Manifest.permission.POST_NOTIFICATIONS)
        val sessions = object : SessionsRepository {
            override val machines = MutableStateFlow<List<Machine>>(emptyList())
            override val sessions = MutableStateFlow<List<Session>>(emptyList())
            override suspend fun refresh() = Unit
        }
        val preferences = object : NotificationPreferencesSource {
            override val preferences = MutableStateFlow<DeviceNotificationPreferences?>(DeviceNotificationPreferences())
            override suspend fun refreshPreferences() = preferences.value
        }
        val notifier = Notifier(app, sessions, preferences)
        val a = Interaction("a", machineId = "machine", sessionId = "session", kind = "permission")
        val b = Interaction("b", machineId = "machine", sessionId = "session", kind = "permission")
        notifier.postInteraction(a)
        notifier.postInteraction(b)
        notifier.pending("b", b.sessionKey, PendingStatus.Sending, null)
        assertEquals(setOf("a"), notifier.showingRequests())
        notifier.retract("a")
        assertEquals(emptySet<String>(), notifier.showingRequests())
    }

    @Test fun irreversibleQuestionPostsOnlyTheOpenAction() {
        val app = ApplicationProvider.getApplicationContext<Application>()
        shadowOf(app).grantPermissions(Manifest.permission.POST_NOTIFICATIONS)
        val sessions = object : SessionsRepository {
            override val machines = MutableStateFlow<List<Machine>>(emptyList())
            override val sessions = MutableStateFlow<List<Session>>(emptyList())
            override suspend fun refresh() = Unit
        }
        val preferences = object : NotificationPreferencesSource {
            override val preferences = MutableStateFlow<DeviceNotificationPreferences?>(DeviceNotificationPreferences())
            override suspend fun refreshPreferences() = preferences.value
        }
        val notifier = Notifier(app, sessions, preferences)
        val card = JsonCodec.decodeFromString(Interaction.serializer(), """
            {"id":"open-only","kind":"question","machine_id":"machine","session_id":"session",
             "reversible":false,"questions":[{"question":"Continue?","options":[{"label":"Continue"}]}]}
        """.trimIndent())
        fun postAndRead(request: Interaction): Notification {
            notifier.postInteraction(request)
            return app.getSystemService(NotificationManager::class.java).activeNotifications
                .single { it.tag == "interaction:${request.id}" }.notification
        }
        val openOnly = postAndRead(card)
        assertTrue(openOnly.actions.isNullOrEmpty())
        assertEquals(NotificationRoute.conversation("machine", "session", null, card.id).uri(),
            shadowOf(openOnly.contentIntent).savedIntent.data)
        for (reversible in listOf(null, true)) {
            assertEquals(listOf("Continue"), postAndRead(card.copy(reversible = reversible))
                .actions.map { it.title.toString() })
        }
    }

    @Test fun catchUpUsesTheOwningGroupPreferencesAndRecordsActualDelivery() {
        val app = ApplicationProvider.getApplicationContext<Application>()
        shadowOf(app).grantPermissions(Manifest.permission.POST_NOTIFICATIONS)
        val sessions = object : SessionsRepository {
            override val machines = MutableStateFlow<List<Machine>>(emptyList())
            override val sessions = MutableStateFlow<List<Session>>(emptyList())
            override suspend fun refresh() = Unit
        }
        val preferences = object : NotificationPreferencesSource {
            override val preferences = MutableStateFlow(DeviceNotificationPreferences(input = false, lockDetails = false))
            override suspend fun refreshPreferences() = preferences.value
        }
        val notifier = Notifier(app, sessions, preferences)
        val request = Interaction("catch-up", machineId = "machine", sessionId = "session", kind = "permission")
        notifier.postInteraction(request)
        notifier.postInteraction(request, false, null)
        assertTrue(notifier.showingRequests().isEmpty())

        notifier.postInteraction(request, false, DeviceNotificationPreferences(input = true, lockDetails = true))
        assertEquals(setOf(request.id), notifier.showingRequests())
        assertEquals(setOf(request.id), notifier.deliveredRequests())
        val shown = app.getSystemService(NotificationManager::class.java).activeNotifications
            .single { it.tag == "interaction:${request.id}" }.notification
        assertEquals(Notification.VISIBILITY_PUBLIC, shown.visibility)

        notifier.retract(request.id)
        assertTrue(notifier.deliveredRequests().isEmpty())
    }
}
