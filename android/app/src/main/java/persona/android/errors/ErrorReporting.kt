package persona.android.errors

import android.app.ActivityManager
import android.app.ApplicationExitInfo
import android.content.Context
import android.os.Build
import androidx.core.content.edit
import java.io.File
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.ExperimentalCoroutinesApi
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.combine
import kotlinx.coroutines.flow.emptyFlow
import kotlinx.coroutines.flow.filterNotNull
import kotlinx.coroutines.flow.flatMapLatest
import kotlinx.coroutines.flow.map
import kotlinx.coroutines.launch
import persona.android.data.Hub
import persona.core.contracts.api.ConnectionState
import persona.core.contracts.generated.ExtensionErrorReport
import persona.core.hub.errors.ErrorBreadcrumbs
import persona.core.hub.errors.ErrorReportBuilder
import persona.core.hub.errors.ErrorReportContext
import persona.core.hub.errors.ErrorReportQueue
import persona.core.hub.errors.ErrorReportSender
import persona.core.hub.errors.ExitReasonPlanner
import persona.core.hub.errors.ExitRecord
import persona.core.hub.errors.HttpErrorReportTransport

/**
 * The app's errors and crashes for the hub's `error_reports` (docs/android/android.md "Error reports"): an uncaught exception
 * is written to the queue before the process dies, ANRs and native crashes are read from the system's exit reasons at
 * the next start, and the queue goes to the selected hub while its hello says `error_reports`.
 */
class ErrorReporting(
    private val context: Context,
    localCopy: (ExtensionErrorReport) -> Unit = {},
    private val localExit: (reason: Int, timestamp: Long) -> Unit = { _, _ -> },
) {
    private val queue = ErrorReportQueue(File(context.filesDir, "error-reports"), localCopy = localCopy)
    private val sender = ErrorReportSender(queue)
    private val prefs by lazy { context.getSharedPreferences("error-reports", Context.MODE_PRIVATE) }
    private val builder by lazy {
        val info = runCatching { context.packageManager.getPackageInfo(context.packageName, 0) }.getOrNull()
        ErrorReportBuilder(ErrorReportContext(
            release = info?.versionName.orEmpty(),
            build = info?.longVersionCode?.toString().orEmpty(),
            osVersion = Build.VERSION.RELEASE.orEmpty(),
            deviceModel = Build.MODEL.orEmpty(),
        ))
    }

    /** Installs the crash handler; call first thing in `Application.onCreate`. */
    fun installCrashHandler() {
        val previous = Thread.getDefaultUncaughtExceptionHandler()
        // Built now, not in the dying process: the package lookup is a binder call.
        val builder = builder
        Thread.setDefaultUncaughtExceptionHandler { thread, error ->
            try {
                val now = System.currentTimeMillis()
                val report = builder.fromThrowable(
                    error, kind = "crash", level = "fatal", timeMillis = now,
                    breadcrumbs = ErrorBreadcrumbs.shared.snapshot(), previousLaunch = true,
                )
                queue.write(report)
                // The exit reason the system records for this crash is skipped at the next start (ExitReasonPlanner).
                // Written synchronously: the process is about to die, apply() would be lost.
                prefs.edit(commit = true) { putLong(KeyJavaCrash, now) }
            } catch (_: Throwable) {
                // Reporting must never stand in the way of the crash itself.
            }
            if (previous != null) previous.uncaughtException(thread, error)
            else {
                android.os.Process.killProcess(android.os.Process.myPid())
                kotlin.system.exitProcess(10)
            }
        }
    }

    /**
     * Reads the earlier exits once, then sends whenever the selected group's hub comes online. Only that one hub
     * gets reports (docs/android/android.md "Multiple hubs"): the one the hub-level pages show, so the reports of one phone
     * go to one place and a member hub without the extension does not clear what the selected one would take.
     */
    @OptIn(ExperimentalCoroutinesApi::class)
    fun start(scope: CoroutineScope, hub: StateFlow<Hub?>) {
        scope.launch(Dispatchers.IO) {
            val _ = runCatching { recordExits() }
            hub.flatMapLatest { selected ->
                val connection = selected?.connection ?: return@flatMapLatest emptyFlow()
                combine(connection.state, connection.serverFeatures) { state, features ->
                    // The features are from this connection's hello only once it is online.
                    if (state == ConnectionState.Online) features else null
                }.filterNotNull().map { connection to it }
            }.collect { (connection, features) ->
                val _ = runCatching { sender.flush(features, HttpErrorReportTransport(connection)) }
            }
        }
    }

    private fun recordExits() {
        val manager = context.getSystemService(ActivityManager::class.java) ?: return
        val historical = manager.getHistoricalProcessExitReasons(context.packageName, 0, 10)
        val exits = historical.map { info ->
            ExitRecord(
                timestampMillis = info.timestamp,
                reason = when (info.reason) {
                    ApplicationExitInfo.REASON_ANR -> ExitRecord.Kind.Anr
                    ApplicationExitInfo.REASON_CRASH_NATIVE -> ExitRecord.Kind.NativeCrash
                    ApplicationExitInfo.REASON_CRASH -> ExitRecord.Kind.JavaCrash
                    else -> ExitRecord.Kind.Other
                },
                trace = if (info.reason == ApplicationExitInfo.REASON_ANR) ({ info.traceInputStream }) else null,
            )
        }
        val lastSeen = prefs.getLong(KeyLastExit, -1L).takeIf { it >= 0 }
        val javaCrash = prefs.getLong(KeyJavaCrash, -1L).takeIf { it >= 0 }
        val plan = ExitReasonPlanner.plan(exits, lastSeen, listOfNotNull(javaCrash), builder)
        plan.reports.forEach(queue::write)
        // The local recorder keeps every other exit reason too; hub reporting keeps its existing three kinds.
        if (lastSeen != null) historical.filter { it.timestamp > lastSeen }.forEach { info ->
            val _ = runCatching { localExit(info.reason, info.timestamp) }
        }
        // First run with no exits at all: still remember that the first run happened.
        prefs.edit { putLong(KeyLastExit, plan.lastSeen ?: 0L) }
    }

    private companion object {
        const val KeyLastExit = "last_exit_ms"
        const val KeyJavaCrash = "java_crash_ms"
    }
}
