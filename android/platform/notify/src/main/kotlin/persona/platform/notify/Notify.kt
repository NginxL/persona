package persona.platform.notify

import android.app.Activity
import android.app.Application
import android.content.Context
import android.os.Bundle
import android.os.Handler
import android.os.Looper
import android.os.PowerManager
import dagger.hilt.EntryPoint
import dagger.hilt.InstallIn
import dagger.hilt.android.EntryPointAccessors
import dagger.hilt.components.SingletonComponent
import persona.core.contracts.ServerEvent
import persona.core.contracts.SessionKey
import persona.core.contracts.api.HubConnection
import persona.core.contracts.api.InteractionCenter
import persona.core.contracts.api.PairingService
import persona.core.contracts.api.SessionsRepository
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.CoroutineStart
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.flow.Flow
import kotlinx.coroutines.flow.combine
import kotlinx.coroutines.flow.distinctUntilChanged
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.emptyFlow
import kotlinx.coroutines.launch
import javax.inject.Inject
import javax.inject.Singleton

/** The composition root supplies the shared WS stream, never a second hub connection. */
object Notify {
    @Volatile internal var events: Flow<ServerEvent> = emptyFlow()
        private set
    internal val visibleSession = MutableStateFlow<SessionKey?>(null)
    @Volatile private var lastViewedSession: SessionKey? = null
    internal val foreground = MutableStateFlow(false)

    /** Call once from Application.onCreate; Hilt access is deferred until application setup finishes. */
    fun install(application: Application, events: Flow<ServerEvent>) {
        this.events = events
        application.registerActivityLifecycleCallbacks(object : Application.ActivityLifecycleCallbacks {
            private val resumed = mutableSetOf<Activity>()
            override fun onActivityResumed(activity: Activity) {
                resumed += activity
                foreground.value = true
                dependencies(application).runtime().onForeground()
            }
            override fun onActivityPaused(activity: Activity) {
                resumed -= activity
                foreground.value = resumed.isNotEmpty()
            }
            override fun onActivityCreated(activity: Activity, state: Bundle?) = Unit
            override fun onActivityStarted(activity: Activity) = Unit
            override fun onActivityStopped(activity: Activity) = Unit
            override fun onActivitySaveInstanceState(activity: Activity, state: Bundle) = Unit
            override fun onActivityDestroyed(activity: Activity) { resumed -= activity }
        })
        Handler(Looper.getMainLooper()).post { dependencies(application).runtime().start() }
    }

    /** Set while a conversation is visible, and clear when leaving it. */
    fun viewing(session: SessionKey?) {
        visibleSession.value = session
        if (session != null) lastViewedSession = session
    }

    fun disconnect(context: Context) {
        lastViewedSession = null
        dependencies(context).runtime().disconnect()
    }

    fun resume(context: Context) = dependencies(context).runtime().resume()

    fun userInteraction(context: Context) = dependencies(context).runtime().onUserInteraction()

    fun requireCapsuleConnection(context: Context, required: Boolean, userAction: Boolean = false) =
        dependencies(context).runtime().requireCapsuleConnection(required, userAction)

    fun capsuleVisible(context: Context, visible: Boolean) = dependencies(context).runtime().capsuleVisible(visible)

    val viewedSession get() = visibleSession.value ?: lastViewedSession

    internal fun dependencies(context: Context): NotifyDependencies =
        EntryPointAccessors.fromApplication(context.applicationContext, NotifyDependencies::class.java)
}

@EntryPoint
@InstallIn(SingletonComponent::class)
internal interface NotifyDependencies {
    fun connection(): HubConnection
    fun interactions(): InteractionCenter
    fun sessions(): SessionsRepository
    fun pairing(): PairingService
    fun notifier(): Notifier
    fun notificationPreferences(): NotificationPreferencesSource
    fun actionJournal(): NotificationActionJournal
    fun journal(): ExitJournal
    fun runtime(): NotificationRuntime
    fun extensionNotifications(): ExtensionNotificationEnvironment
}

@Singleton
class NotificationRuntime @Inject constructor(
    private val application: Application,
    private val pairing: PairingService,
    private val journal: ExitJournal,
    private val notifier: Notifier,
    private val fleet: FleetFeed,
    private val sessions: SessionsRepository,
    private val requests: ResidentConnectionRequests,
) {
    private val scope = CoroutineScope(SupervisorJob() + Dispatchers.Main.immediate)
    private var started = false

    fun start() {
        if (started) return
        started = true
        journal.capture()
        fleet.start()
        // A result seen in the app or anywhere else no longer needs its notification (#3567).
        // Coming back to a chat left on screen counts too, so the app being in front is part of the key.
        scope.launch {
            combine(Notify.visibleSession, Notify.foreground) { session, front -> session?.takeIf { front } }
                .distinctUntilChanged().collect { it?.let(notifier::retractSeen) }
        }
        scope.launch {
            val seen = SeenSessions()
            sessions.sessions.collect { list -> seen.update(list).forEach(notifier::retractSeen) }
        }
        // An assistant answer looked at on the web, iOS or a Mac: its seen_at moved past the notification (#4805).
        scope.launch {
            val seen = SeenAssistantNotifications()
            sessions.sessions.collect { list ->
                val moved = seen.update(list, System.currentTimeMillis())
                if (moved.isNotEmpty()) SeenAssistantNotifications.stale(notifier.showingAssistant(), moved)
                    .forEach { (tag, before) -> notifier.retractAssistant(tag, before) }
            }
        }
        scope.launch(start = CoroutineStart.UNDISPATCHED) {
            pairing.restore()
            combine(pairing.isPaired, Notify.foreground, requests.capsule, requests.overlayVisible, HubService.runningState) {
                    paired, foreground, capsule, overlay, running ->
                paired to (!running && mayStartResidentConnection(paired, journal.userStopped, foreground,
                    application.getSystemService(PowerManager::class.java).isIgnoringBatteryOptimizations(application.packageName),
                    capsule, overlay))
            }.distinctUntilChanged().collect { (paired, start) ->
                if (paired) {
                    ConnectionWatchdog.schedule(application)
                    if (start) HubService.start(application)
                } else {
                    ConnectionWatchdog.cancel(application)
                    HubService.stop(application)
                    notifier.clear()
                }
            }
        }
    }

    fun disconnect() {
        ConnectionWatchdog.cancel(application)
        HubService.stop(application)
        notifier.clear()
        Notify.viewing(null)
    }

    fun onForeground() {
        start()
        scope.launch(start = CoroutineStart.UNDISPATCHED) {
            pairing.restore()
            if (pairing.isPaired.value) resumeAfterRestore()
        }
    }

    fun onUserInteraction() {
        start()
        scope.launch(start = CoroutineStart.UNDISPATCHED) {
            pairing.restore()
            if (pairing.isPaired.value && (journal.userStopped || !HubService.running)) resumeAfterRestore()
        }
    }

    fun resume() = scope.launch(start = CoroutineStart.UNDISPATCHED) {
        pairing.restore()
        resumeAfterRestore()
    }

    private fun resumeAfterRestore() {
        journal.resume()
        notifier.dismissStopped()
        if (pairing.isPaired.value) {
            ConnectionWatchdog.schedule(application)
            HubService.start(application)
        }
    }

    fun requireCapsuleConnection(required: Boolean, userAction: Boolean) {
        requests.capsule(required)
        if (userAction) resume()
    }

    fun capsuleVisible(visible: Boolean) { requests.overlayVisible(visible) }
}
