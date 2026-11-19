package persona.android.dev

import android.annotation.SuppressLint
import android.content.BroadcastReceiver
import android.content.ClipData
import android.content.Context
import android.content.Intent
import android.content.IntentFilter
import android.net.ConnectivityManager
import android.net.Network
import android.net.NetworkCapabilities
import android.os.BatteryManager
import android.os.Build
import android.os.PowerManager
import androidx.core.content.ContextCompat
import androidx.core.content.FileProvider
import androidx.lifecycle.DefaultLifecycleObserver
import androidx.lifecycle.Lifecycle
import androidx.lifecycle.LifecycleOwner
import androidx.lifecycle.ProcessLifecycleOwner
import java.io.File
import java.time.Instant
import java.time.ZoneId
import java.util.concurrent.FutureTask
import java.util.concurrent.TimeUnit
import kotlinx.coroutines.CoroutineDispatcher
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.Job
import kotlinx.coroutines.NonCancellable
import kotlinx.coroutines.delay
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.isActive
import kotlinx.coroutines.launch
import kotlinx.coroutines.sync.Mutex
import kotlinx.coroutines.sync.withLock
import kotlinx.coroutines.withContext
import persona.core.contracts.DevProbe
import persona.core.contracts.generated.ExtensionErrorReport
import persona.core.data.dev.DevCategory
import persona.core.data.dev.DevDevice
import persona.core.data.dev.DevDeviceState
import persona.core.data.dev.DevNetPath
import persona.core.data.dev.DevPackage
import persona.core.data.dev.DevRecorder
import persona.core.data.dev.DevStore
import persona.feature.settings.DevelopingDiagnostics
import persona.feature.settings.DevelopingDiagnosticsSnapshot

/** Owns the local switch, system observers and recorder. No diagnostics leave the device until the owner chooses a destination. */
class DevelopingPersona internal constructor(
    private val context: Context,
    private val scope: CoroutineScope,
    private val io: CoroutineDispatcher = Dispatchers.IO,
    private val clock: () -> Instant = Instant::now,
    private val cpuMillis: () -> Long = { android.os.Process.getElapsedCpuTime() },
    private val logReader: () -> List<String> = ::readOwnLogcat,
    private val observeSystem: Boolean = true,
) : DevelopingDiagnostics, DefaultLifecycleObserver {
    private val prefs = context.getSharedPreferences("developing-persona", Context.MODE_PRIVATE)
    private val exportRoot = File(context.cacheDir, "dev-exports")
    internal val store = DevStore(File(context.noBackupFilesDir, "developing-persona"))
    private val build = runCatching {
        context.packageManager.getPackageInfo(context.packageName, 0).longVersionCode.toString()
    }.getOrDefault("")
    private val recorder = DevRecorder(store, clock, cpuMillis, build)
    private val transition = Mutex()
    private val _state = MutableStateFlow(initialState())
    override val state: StateFlow<DevelopingDiagnosticsSnapshot> = _state.asStateFlow()
    private var started = false
    private var recording = false
    private var ticker: Job? = null
    private var networkCallback: ConnectivityManager.NetworkCallback? = null
    private var thermalListener: PowerManager.OnThermalStatusChangedListener? = null
    private var receiver: BroadcastReceiver? = null
    private var batterySignature: Pair<Double?, String>? = null

    fun start() {
        if (started) return
        started = true
        if (observeSystem) ProcessLifecycleOwner.get().lifecycle.addObserver(this)
        if (state.value.enabled) begin()
    }

    override suspend fun setEnabled(enabled: Boolean) = transition.withLock {
        if (state.value.enabled == enabled) return@withLock
        if (enabled) {
            val now = clock()
            prefs.edit().putBoolean(KeyOn, true).putString(KeySince, now.toString()).apply()
            _state.value = DevelopingDiagnosticsSnapshot(true, now, null)
            begin()
        } else {
            end()
            prefs.edit().clear().apply()
            _state.value = DevelopingDiagnosticsSnapshot(recordedBytes = 0)
            // stop() first rejects new records; settle waits for writes that were already queued before clearing them.
            withContext(NonCancellable + io) {
                recorder.settle()
                store.clear()
                exportRoot.deleteRecursively()
            }
        }
    }

    override suspend fun refresh() = transition.withLock {
        if (!state.value.enabled) return@withLock
        val bytes = withContext(io) {
            recorder.settle()
            store.recordedBytes()
        }
        _state.value = state.value.copy(recordedBytes = bytes)
    }

    fun recordError(report: ExtensionErrorReport) = recorder.error(report)

    fun recordExit(reason: Int, timestamp: Long) {
        // These already arrive through ErrorReportQueue.localCopy, including the ANR's parsed main-thread frames.
        if (reason == android.app.ApplicationExitInfo.REASON_ANR ||
            reason == android.app.ApplicationExitInfo.REASON_CRASH ||
            reason == android.app.ApplicationExitInfo.REASON_CRASH_NATIVE) return
        recorder.exit(exitReasonName(reason), Instant.ofEpochMilli(timestamp))
    }

    override suspend fun export(categories: Set<DevCategory>): File = transition.withLock {
        check(state.value.enabled) { "Developing Persona is off" }
        withContext(io) {
            recorder.settle()
            val now = clock()
            val archive = DevPackage.exportZip(exportRoot, store, categories, state.value.since, deviceInfo(now), logReader(), now)
            exportRoot.listFiles().orEmpty().filter { it != archive.parentFile }.forEach(File::deleteRecursively)
            archive
        }
    }

    override fun share(context: Context, file: File) {
        requireOwnedExport(file)
        val uri = FileProvider.getUriForFile(context, "${context.packageName}.dev.files", file)
        val send = Intent(Intent.ACTION_SEND).setType("application/zip")
            .putExtra(Intent.EXTRA_STREAM, uri).putExtra(Intent.EXTRA_SUBJECT, file.name)
            .addFlags(Intent.FLAG_GRANT_READ_URI_PERMISSION)
            .also { it.clipData = ClipData.newRawUri(file.name, uri) }
        context.startActivity(Intent.createChooser(send, file.name).addFlags(Intent.FLAG_ACTIVITY_NEW_TASK))
    }

    override fun sendToAgent(context: Context, file: File) {
        requireOwnedExport(file)
        context.startActivity(Intent(context, DiagnosticsShareActivity::class.java)
            .putExtra(DiagnosticsShareActivity.EXTRA_FILE, file.absolutePath).addFlags(Intent.FLAG_ACTIVITY_NEW_TASK))
    }

    override fun onStart(owner: LifecycleOwner) {
        if (!state.value.enabled) return
        recorder.lifecycle(true, deviceState())
        startTicker()
    }

    override fun onStop(owner: LifecycleOwner) {
        ticker?.cancel()
        ticker = null
        if (!state.value.enabled) return
        recorder.lifecycle(false, deviceState())
        recorder.flush()
    }

    fun close() {
        end()
        if (started && observeSystem) ProcessLifecycleOwner.get().lifecycle.removeObserver(this)
        started = false
        recorder.close()
    }

    private fun begin() {
        if (recording) return
        recording = true
        recorder.start(isInFront(), "launch", deviceState())
        DevProbe.install(recorder)
        if (observeSystem) {
            observeNetwork()
            observePowerAndBattery()
        }
        if (isInFront()) startTicker()
    }

    private fun end() {
        ticker?.cancel()
        ticker = null
        if (observeSystem) {
            networkCallback?.let { runCatching { context.getSystemService(ConnectivityManager::class.java).unregisterNetworkCallback(it) } }
            networkCallback = null
            thermalListener?.let { runCatching { context.getSystemService(PowerManager::class.java).removeThermalStatusListener(it) } }
            thermalListener = null
            receiver?.let { runCatching { context.unregisterReceiver(it) } }
            receiver = null
        }
        DevProbe.install(null)
        recorder.stop()
        recording = false
    }

    private fun startTicker() {
        if (ticker?.isActive == true) return
        ticker = scope.launch(io) {
            while (isActive) {
                delay(60_000)
                recorder.roll(deviceState())
            }
        }
    }

    private fun observeNetwork() {
        val manager = context.getSystemService(ConnectivityManager::class.java) ?: return
        val callback = object : ConnectivityManager.NetworkCallback() {
            override fun onCapabilitiesChanged(network: Network, capabilities: NetworkCapabilities) {
                recorder.path(DevNetPath(
                    type = when {
                        capabilities.hasTransport(NetworkCapabilities.TRANSPORT_WIFI) -> "wifi"
                        capabilities.hasTransport(NetworkCapabilities.TRANSPORT_CELLULAR) -> "cellular"
                        capabilities.hasTransport(NetworkCapabilities.TRANSPORT_ETHERNET) -> "wired"
                        else -> "other"
                    },
                    expensive = !capabilities.hasCapability(NetworkCapabilities.NET_CAPABILITY_NOT_METERED),
                    constrained = !capabilities.hasCapability(NetworkCapabilities.NET_CAPABILITY_NOT_RESTRICTED),
                ))
            }
            override fun onLost(network: Network) { recorder.path(DevNetPath()) }
        }
        networkCallback = callback
        runCatching { manager.registerDefaultNetworkCallback(callback) }
    }

    private fun observePowerAndBattery() {
        val power = context.getSystemService(PowerManager::class.java)
        val thermal = PowerManager.OnThermalStatusChangedListener { status ->
            recorder.device("thermal", mapOf("state" to thermalName(status)))
        }
        thermalListener = thermal
        runCatching { power.addThermalStatusListener(context.mainExecutor, thermal) }
        val changed = object : BroadcastReceiver() {
            override fun onReceive(context: Context?, intent: Intent?) {
                when (intent?.action) {
                    PowerManager.ACTION_POWER_SAVE_MODE_CHANGED -> recorder.device("power", mapOf("low_power" to power.isPowerSaveMode))
                    Intent.ACTION_BATTERY_CHANGED -> {
                        val current = deviceState()
                        val signature = current.battery to current.batteryState
                        if (signature != batterySignature && isInFront()) {
                            batterySignature = signature
                            recorder.device("battery", mapOf("level" to current.battery, "state" to current.batteryState))
                        }
                    }
                }
            }
        }
        receiver = changed
        ContextCompat.registerReceiver(context, changed, IntentFilter().apply {
            addAction(PowerManager.ACTION_POWER_SAVE_MODE_CHANGED)
            addAction(Intent.ACTION_BATTERY_CHANGED)
        }, ContextCompat.RECEIVER_NOT_EXPORTED)
    }

    private fun deviceState(): DevDeviceState {
        val battery = context.getSystemService(BatteryManager::class.java)
        val power = context.getSystemService(PowerManager::class.java)
        val level = battery.getIntProperty(BatteryManager.BATTERY_PROPERTY_CAPACITY)
        return DevDeviceState(
            thermal = thermalName(power.currentThermalStatus),
            battery = if (level in 0..100) level / 100.0 else null,
            lowPower = power.isPowerSaveMode,
            batteryState = batteryState(battery.getIntProperty(BatteryManager.BATTERY_PROPERTY_STATUS)),
        )
    }

    @SuppressLint("DiscouragedApi")
    private fun deviceInfo(now: Instant): DevDevice {
        val info = runCatching { context.packageManager.getPackageInfo(context.packageName, 0) }.getOrNull()
        val commitId = context.resources.getIdentifier("persona_build_commit", "string", context.packageName)
        val rawCommit = if (commitId == 0) null else runCatching { context.resources.getString(commitId) }.getOrNull()
        val commit = rawCommit?.takeIf { it.matches(Regex("[0-9a-fA-F]{7,64}")) }?.lowercase().orEmpty()
        return DevDevice(
            osVersion = Build.VERSION.RELEASE.orEmpty(), model = Build.MODEL.orEmpty(),
            appVersion = info?.versionName.orEmpty(), build = info?.longVersionCode?.toString().orEmpty(), commit = commit,
            utcOffsetMinutes = ZoneId.systemDefault().rules.getOffset(now).totalSeconds / 60,
        )
    }

    private fun isInFront(): Boolean = !observeSystem || ProcessLifecycleOwner.get().lifecycle.currentState.isAtLeast(Lifecycle.State.STARTED)

    private fun requireOwnedExport(file: File) {
        require(file.isFile && file.canonicalFile.toPath().startsWith(exportRoot.canonicalFile.toPath())) {
            "Not a Developing Persona export"
        }
    }

    private fun initialState(): DevelopingDiagnosticsSnapshot {
        val enabled = prefs.getBoolean(KeyOn, false)
        val since = if (enabled) prefs.getString(KeySince, null)?.let { raw -> runCatching { Instant.parse(raw) }.getOrNull() } else null
        return DevelopingDiagnosticsSnapshot(enabled, since, null)
    }

    private companion object {
        const val KeyOn = "on"
        const val KeySince = "since"
    }
}

internal fun exitReasonName(reason: Int): String = when (reason) {
    android.app.ApplicationExitInfo.REASON_EXIT_SELF -> "exit_self"
    android.app.ApplicationExitInfo.REASON_SIGNALED -> "signaled"
    android.app.ApplicationExitInfo.REASON_LOW_MEMORY -> "low_memory"
    android.app.ApplicationExitInfo.REASON_CRASH -> "crash"
    android.app.ApplicationExitInfo.REASON_CRASH_NATIVE -> "crash_native"
    android.app.ApplicationExitInfo.REASON_ANR -> "anr"
    android.app.ApplicationExitInfo.REASON_INITIALIZATION_FAILURE -> "initialization_failure"
    android.app.ApplicationExitInfo.REASON_PERMISSION_CHANGE -> "permission_change"
    android.app.ApplicationExitInfo.REASON_EXCESSIVE_RESOURCE_USAGE -> "excessive_resource_usage"
    android.app.ApplicationExitInfo.REASON_USER_REQUESTED -> "user_requested"
    android.app.ApplicationExitInfo.REASON_USER_STOPPED -> "user_stopped"
    android.app.ApplicationExitInfo.REASON_DEPENDENCY_DIED -> "dependency_died"
    android.app.ApplicationExitInfo.REASON_FREEZER -> "freezer"
    android.app.ApplicationExitInfo.REASON_PACKAGE_STATE_CHANGE -> "package_state_change"
    android.app.ApplicationExitInfo.REASON_PACKAGE_UPDATED -> "package_updated"
    android.app.ApplicationExitInfo.REASON_OTHER -> "other"
    else -> "unknown"
}

internal fun batteryState(status: Int): String = when (status) {
    BatteryManager.BATTERY_STATUS_CHARGING -> "charging"
    BatteryManager.BATTERY_STATUS_FULL -> "full"
    BatteryManager.BATTERY_STATUS_DISCHARGING, BatteryManager.BATTERY_STATUS_NOT_CHARGING -> "unplugged"
    else -> "unknown"
}

internal fun thermalName(status: Int): String = when (status) {
    PowerManager.THERMAL_STATUS_NONE -> "nominal"
    PowerManager.THERMAL_STATUS_LIGHT, PowerManager.THERMAL_STATUS_MODERATE -> "fair"
    PowerManager.THERMAL_STATUS_SEVERE -> "serious"
    else -> if (status > PowerManager.THERMAL_STATUS_SEVERE) "critical" else "unknown"
}

/** Only this process's log, dumped at export; no log permission and no background logcat process. */
private fun readOwnLogcat(): List<String> = try {
    readOwnLogcat(ProcessBuilder("logcat", "-d", "-v", "threadtime", "--pid=${android.os.Process.myPid()}",
        "-t", DevPackage.maxLogLines.toString()).redirectErrorStream(true).start())
} catch (_: Exception) {
    emptyList()
}

internal fun readOwnLogcat(process: Process, timeoutMillis: Long = 5_000): List<String> {
    val reading = FutureTask<List<String>> { process.inputStream.bufferedReader().use { it.readLines() } }
    val reader = Thread(reading, "dev-logcat-reader").apply { isDaemon = true }
    return try {
        reader.start()
        reading.get(timeoutMillis, TimeUnit.MILLISECONDS)
    } catch (_: Exception) {
        emptyList()
    } finally {
        reading.cancel(true)
        runCatching { process.inputStream.close() }
        runCatching { process.errorStream.close() }
        runCatching { process.outputStream.close() }
        if (process.isAlive) {
            process.destroy()
            process.destroyForcibly()
        }
    }
}
