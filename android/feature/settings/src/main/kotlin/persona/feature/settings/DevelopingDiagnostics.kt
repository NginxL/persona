package persona.feature.settings

import android.content.Context
import java.io.File
import java.time.Instant
import kotlinx.coroutines.flow.StateFlow
import persona.core.data.dev.DevCategory

data class DevelopingDiagnosticsSnapshot(
    val enabled: Boolean = false,
    val since: Instant? = null,
    val recordedBytes: Long? = null,
)

/** Device-local diagnostics behind Settings › About; no hub state or automatic upload. */
interface DevelopingDiagnostics {
    val state: StateFlow<DevelopingDiagnosticsSnapshot>
    suspend fun setEnabled(enabled: Boolean)
    suspend fun refresh()
    suspend fun export(categories: Set<DevCategory>): File
    fun share(context: Context, file: File)
    fun sendToAgent(context: Context, file: File)
}
