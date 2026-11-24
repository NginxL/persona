package persona.platform.notify

import android.app.ActivityManager
import android.app.ApplicationExitInfo
import android.content.Context
import dagger.hilt.android.qualifiers.ApplicationContext
import java.io.File
import javax.inject.Inject
import javax.inject.Singleton

@Singleton
class ExitJournal @Inject constructor(@ApplicationContext private val context: Context) {
    private val preferences = context.getSharedPreferences("notify_recovery", Context.MODE_PRIVATE)

    val userStopped: Boolean get() = preferences.getBoolean("user_stopped", false)

    @Synchronized
    fun capture() {
        val _ = runCatching {
            val last = preferences.getLong("last_exit", 0)
            val exits = context.getSystemService(ActivityManager::class.java)
                .getHistoricalProcessExitReasons(context.packageName, 0, 16)
                .filter { it.processName == context.packageName && it.timestamp > last }
                .sortedBy { it.timestamp }
            if (exits.isEmpty()) return
            val file = File(context.filesDir, "notify-exits.log")
            val lines = if (file.exists()) file.readLines().takeLast(63).toMutableList() else mutableListOf()
            exits.forEach { exit ->
                // Descriptions and traces may contain private payloads; retain only numeric diagnostics.
                lines += "timestamp=${exit.timestamp} reason=${exit.reason} status=${exit.status} importance=${exit.importance}"
            }
            file.writeText(lines.takeLast(64).joinToString("\n", postfix = "\n"))
            preferences.edit().putLong("last_exit", exits.last().timestamp).apply {
                if (exits.last().reason == ApplicationExitInfo.REASON_USER_REQUESTED) putBoolean("user_stopped", true)
            }.commit()
        }
    }

    fun resume() { preferences.edit().putBoolean("user_stopped", false).commit() }
}
