package persona.android.debug

import android.content.Context
import android.content.pm.ApplicationInfo
import android.os.Process
import android.os.SystemClock
import android.util.AtomicFile
import java.io.File
import java.security.SecureRandom
import javax.crypto.Mac
import javax.crypto.spec.SecretKeySpec
import kotlinx.serialization.Serializable
import kotlinx.serialization.encodeToString
import persona.core.contracts.JsonCodec
import persona.core.designsystem.AssistantStackProbe
import persona.core.designsystem.AssistantStackProbeBackend

private const val STACK_PROBE_FILE = "hub-data/assistant-stack-probe.json"
private const val STACK_PROBE_VERSION = 1
private const val STACK_HMAC_ALGORITHM = "HmacSHA256"

@Serializable
private data class DebugStackComposerEvidence(val draftHmac: String, val focused: Boolean)

@Serializable
private data class DebugStackPageEvidence(val updatedAtNanos: Long, val composer: DebugStackComposerEvidence)

@Serializable
private data class DebugStackProbeSnapshot(
    val fieldVersion: Int,
    val session: String,
    val pid: Int,
    val updatedAtNanos: Long,
    val route: String,
    val selectedKey: String?,
    val pages: Map<String, DebugStackPageEvidence>,
)

/** Writes route and reply-draft evidence only for a debug.automation launch. Draft plaintext never leaves the field. */
object DebugAssistantStackProbe : AssistantStackProbeBackend {
    private val lock = Any()
    private var file: File? = null
    private var session = ""
    private var hmacKey: SecretKeySpec? = null
    private var route = "assistant"
    private var selectedKey: String? = null
    private var pages = linkedMapOf<String, DebugStackPageEvidence>()

    fun enable(context: Context) {
        val app = context.applicationContext
        if (app.applicationInfo.flags and ApplicationInfo.FLAG_DEBUGGABLE == 0) return
        synchronized(lock) {
            if (AssistantStackProbe.enabled) return
            hmacKey = SecretKeySpec(ByteArray(32).also(SecureRandom()::nextBytes), STACK_HMAC_ALGORITHM)
            session = java.util.UUID.randomUUID().toString()
            route = "assistant"
            selectedKey = null
            pages = linkedMapOf()
            file = File(app.noBackupFilesDir, STACK_PROBE_FILE)
            file?.let { AtomicFile(it).delete() }
            AssistantStackProbe.install(this)
        }
    }

    fun disable() {
        synchronized(lock) {
            AssistantStackProbe.uninstall(this)
            file?.let { AtomicFile(it).delete() }
            file = null
            hmacKey = null
            session = ""
            pages.clear()
        }
    }

    override fun publishNavigation(route: String, selectedKey: String?) {
        synchronized(lock) {
            if (!AssistantStackProbe.enabled || this.route == route && this.selectedKey == selectedKey) return
            this.route = route
            this.selectedKey = selectedKey
            writeLocked()
        }
    }

    override fun publishDraft(page: String, draft: String, focused: Boolean) {
        synchronized(lock) {
            if (!AssistantStackProbe.enabled) return
            val evidence = DebugStackPageEvidence(now(), DebugStackComposerEvidence(draft.hmac(), focused))
            if (pages[page]?.composer == evidence.composer) return
            pages[page] = evidence
            writeLocked()
        }
    }

    override fun removeDraft(page: String) {
        synchronized(lock) {
            if (pages.remove(page) != null) writeLocked()
        }
    }

    override fun touch() {
        synchronized(lock) {
            if (!AssistantStackProbe.enabled) return
            val now = now()
            pages = LinkedHashMap(pages.mapValues { (_, page) -> page.copy(updatedAtNanos = now) })
            writeLocked()
        }
    }

    private fun writeLocked() {
        val target = file ?: return
        target.parentFile?.mkdirs()
        val snapshot = DebugStackProbeSnapshot(STACK_PROBE_VERSION, session, Process.myPid(), now(), route, selectedKey, pages.toMap())
        val atomic = AtomicFile(target)
        val _ = runCatching {
            val output = atomic.startWrite()
            try {
                output.write(JsonCodec.encodeToString(snapshot).toByteArray(Charsets.UTF_8))
                atomic.finishWrite(output)
            } catch (error: Exception) {
                atomic.failWrite(output)
                throw error
            }
        }
    }

    private fun String.hmac(): String {
        val key = hmacKey ?: return ""
        val mac = Mac.getInstance(STACK_HMAC_ALGORITHM)
        mac.init(key)
        return mac.doFinal(toByteArray(Charsets.UTF_8)).joinToString("") { "%02x".format(it.toInt() and 0xff) }
    }

    private fun now() = SystemClock.elapsedRealtimeNanos()
}
