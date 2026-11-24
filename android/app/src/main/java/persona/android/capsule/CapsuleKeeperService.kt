package persona.android.capsule

import android.accessibilityservice.AccessibilityService
import android.content.BroadcastReceiver
import android.content.ComponentName
import android.content.Context
import android.content.Intent
import android.content.IntentFilter
import android.provider.Settings
import android.view.accessibility.AccessibilityEvent
import androidx.core.content.ContextCompat
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.asStateFlow
import persona.platform.notify.Notify

/**
 * Keeps the capsule resident the way floating assistants do: the system binds an enabled accessibility service and
 * rebinds it after the process is killed, which an overlay alone does not survive (phones that clear background apps on
 * lock took the capsule away until Persona was opened again, 2026-11-06). It reads no window content and handles no
 * events; it only holds the process and brings the resident connection back on connect and on each unlock.
 */
class CapsuleKeeperService : AccessibilityService() {
    private var registered = false
    private val unlocked = object : BroadcastReceiver() {
        override fun onReceive(context: Context, intent: Intent) { Notify.userInteraction(context) }
    }

    override fun onServiceConnected() {
        super.onServiceConnected()
        CapsuleKeeper.connected(true)
        CapsuleKeeper.wanted(this, true)
        CapsuleKeeperReminder.clear(this)
        // Turning the keeper on is the user's request to stay connected, so it also clears an earlier stop.
        Notify.userInteraction(this)
        if (!registered) {
            ContextCompat.registerReceiver(this, unlocked, IntentFilter(Intent.ACTION_USER_PRESENT).apply {
                addAction(Intent.ACTION_SCREEN_ON)
            }, ContextCompat.RECEIVER_NOT_EXPORTED)
            registered = true
        }
    }

    override fun onAccessibilityEvent(event: AccessibilityEvent?) = Unit
    override fun onInterrupt() = Unit

    override fun onUnbind(intent: Intent?): Boolean {
        // Turned off in the accessibility settings while running: the user's choice, not an update's.
        CapsuleKeeper.wanted(this, false)
        release()
        return super.onUnbind(intent)
    }

    override fun onDestroy() {
        release()
        super.onDestroy()
    }

    private fun release() {
        if (registered) { val _ = runCatching { unregisterReceiver(unlocked) }; registered = false }
        CapsuleKeeper.connected(false)
    }
}

object CapsuleKeeper {
    private val live = MutableStateFlow(false)
    /** Bound by the system in this process right now. */
    val running = live.asStateFlow()
    internal fun connected(value: Boolean) { live.value = value }

    /** Turned on in the system's accessibility settings (it may not be bound yet). */
    fun enabled(context: Context): Boolean {
        val component = ComponentName(context, CapsuleKeeperService::class.java)
        val list = Settings.Secure.getString(context.contentResolver, Settings.Secure.ENABLED_ACCESSIBILITY_SERVICES).orEmpty()
        return list.split(':').any { ComponentName.unflattenFromString(it) == component }
    }

    fun settingsIntent(): Intent = Intent(Settings.ACTION_ACCESSIBILITY_SETTINGS).addFlags(Intent.FLAG_ACTIVITY_NEW_TASK)

    private fun preferences(context: Context) = context.getSharedPreferences("capsule-keeper", Context.MODE_PRIVATE)
    /** The keeper was on and not turned off by the user: some systems switch it off when the app updates itself. */
    fun wanted(context: Context) = preferences(context).getBoolean("wanted", false)
    internal fun wanted(context: Context, value: Boolean) { preferences(context).edit().putBoolean("wanted", value).apply() }

    /**
     * Turns the keeper back on without the user, which needs WRITE_SECURE_SETTINGS (granted once from a computer:
     * `adb shell pm grant <package> android.permission.WRITE_SECURE_SETTINGS`; it survives updates).
     */
    fun restore(context: Context): Boolean {
        if (enabled(context)) return true
        if (context.checkSelfPermission(android.Manifest.permission.WRITE_SECURE_SETTINGS) != android.content.pm.PackageManager.PERMISSION_GRANTED) return false
        return runCatching {
            val component = ComponentName(context, CapsuleKeeperService::class.java).flattenToString()
            val current = Settings.Secure.getString(context.contentResolver, Settings.Secure.ENABLED_ACCESSIBILITY_SERVICES).orEmpty()
            val list = current.split(':').filter { it.isNotBlank() && it != component } + component
            Settings.Secure.putString(context.contentResolver, Settings.Secure.ENABLED_ACCESSIBILITY_SERVICES, list.joinToString(":"))
            Settings.Secure.putInt(context.contentResolver, Settings.Secure.ACCESSIBILITY_ENABLED, 1)
            true
        }.getOrDefault(false)
    }

    /** After an update or at start: the keeper the user wanted is back on, by itself or through a reminder. */
    fun recover(context: Context) {
        if (!wanted(context) || enabled(context)) return
        if (!restore(context)) CapsuleKeeperReminder.show(context)
    }
}

/** A notification that opens the accessibility settings when the system switched the keeper off after an update. */
object CapsuleKeeperReminder {
    private const val CHANNEL = "capsule-keeper"
    private const val ID = 7301

    fun show(context: Context) {
        val manager = context.getSystemService(android.app.NotificationManager::class.java)
        manager.createNotificationChannel(android.app.NotificationChannel(CHANNEL,
            context.getString(persona.android.R.string.app_capsule_keeper_channel), android.app.NotificationManager.IMPORTANCE_DEFAULT))
        val open = android.app.PendingIntent.getActivity(context, ID, CapsuleKeeper.settingsIntent(),
            android.app.PendingIntent.FLAG_IMMUTABLE or android.app.PendingIntent.FLAG_UPDATE_CURRENT)
        val notification = androidx.core.app.NotificationCompat.Builder(context, CHANNEL)
            .setSmallIcon(persona.core.designsystem.R.drawable.ic_stat)
            .setContentTitle(context.getString(persona.android.R.string.app_capsule_keeper_off_title))
            .setContentText(context.getString(persona.android.R.string.app_capsule_keeper_off_text))
            .setStyle(androidx.core.app.NotificationCompat.BigTextStyle().bigText(context.getString(persona.android.R.string.app_capsule_keeper_off_text)))
            .setContentIntent(open).setAutoCancel(true).build()
        val _ = runCatching { manager.notify(ID, notification) }
    }

    fun clear(context: Context) { context.getSystemService(android.app.NotificationManager::class.java).cancel(ID) }
}

/** The app was updated: bring the keeper back (MY_PACKAGE_REPLACED reaches a stopped-by-update app). */
class CapsuleKeeperUpdateReceiver : BroadcastReceiver() {
    override fun onReceive(context: Context, intent: Intent) {
        if (intent.action == Intent.ACTION_MY_PACKAGE_REPLACED) CapsuleKeeper.recover(context)
    }
}
