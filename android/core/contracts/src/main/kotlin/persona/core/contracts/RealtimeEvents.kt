@file:OptIn(kotlinx.serialization.ExperimentalSerializationApi::class)

package persona.core.contracts

import java.util.Base64
import kotlinx.serialization.KeepGeneratedSerializer
import kotlinx.serialization.SerialName
import kotlinx.serialization.Serializable
import kotlinx.serialization.json.JsonElement
import kotlinx.serialization.json.JsonObject

@KeepGeneratedSerializer
@Serializable(with = DeltaEventSerializer::class)
data class DeltaEvent(
    @SerialName("machine_id") val machineId: String,
    @SerialName("session_id") val sessionId: String,
    val text: String = "",
) : ServerEvent {
    /** The hub sends the whole draft, not an append-only fragment. */
    val turnFinished: Boolean get() = text.isEmpty()
}

@KeepGeneratedSerializer
@Serializable(with = ScreenEventSerializer::class)
data class ScreenEvent(
    @SerialName("machine_id") val machineId: String,
    @SerialName("session_id") val sessionId: String,
    val text: String = "",
    @WireEnum("idle", "working", "blocked", "done", "offline", "unknown") val status: String = "unknown",
    @WireTime val ts: String? = null,
) : ServerEvent

@KeepGeneratedSerializer
@Serializable(with = ToolCallEventSerializer::class)
data class ToolCallEvent(
    @SerialName("call_id") val callId: String,
    val name: String,
    val args: JsonElement = JsonObject(emptyMap()),
) : ServerEvent

@KeepGeneratedSerializer
@Serializable(with = HookEventSerializer::class)
data class HookEvent(
    @SerialName("machine_id") val machineId: String,
    @SerialName("session_id") val sessionId: String,
    val event: String = "",
    val message: String? = null,
    @SerialName("last_assistant_message") val lastAssistantMessage: String? = null,
    @SerialName("tool_name") val toolName: String? = null,
    val cwd: String? = null,
) : ServerEvent

@KeepGeneratedSerializer
@Serializable(with = LarkEventSerializer::class)
data class LarkEvent(
    @SerialName("feed_ids") val feedIds: List<String> = emptyList(),
) : ServerEvent

@KeepGeneratedSerializer
@Serializable(with = InboxEventSerializer::class)
class InboxEvent : ServerEvent

/** The personas the hub offers changed, e.g. a tiered look moved to another tier (docs/apple/persona.md): list them again. */
@KeepGeneratedSerializer
@Serializable(with = PersonasEventSerializer::class)
class PersonasEvent : ServerEvent

/**
 * The desktop apps (the Mac app) connected to the hub now, like WeChat's "WeChat for Mac signed in" (server/internal/hub/desktops.go):
 * sent on connecting and whenever one comes or goes.
 */
@KeepGeneratedSerializer
@Serializable(with = DesktopsEventSerializer::class)
data class DesktopsEvent(val desktops: List<HubDesktop> = emptyList()) : ServerEvent

/** One connected desktop: [device] is its device token's id (what "Sign out" revokes), empty when it used the pairing token. */
@Serializable
data class HubDesktop(val device: String = "", val name: String = "", val since: String? = null)

/** The chosen look changed (another device chose one, or its extension went and the hub went back to the default, #3200). */
@KeepGeneratedSerializer
@Serializable(with = LookEventSerializer::class)
data class LookEvent(val id: String = "", val effective: String = "persona") : ServerEvent

@KeepGeneratedSerializer
@Serializable(with = TerminalEventSerializer::class)
data class TerminalEvent(
    @SerialName("machine_id") val machineId: String,
    @SerialName("session_id") val sessionId: String,
    val epoch: String? = null,
    val seq: Long? = null,
    val full: Boolean = false,
    val cols: Int = 0,
    val rows: Int = 0,
    val data: String? = null,
    val error: String? = null,
    val unsupported: Boolean = false,
) : ServerEvent {
    val streamEnded: Boolean get() = error != null

    /** A stream failure ends the terminal relay, not the conversation. */
    val frameBytes: ByteArray? get() = data?.let { runCatching { Base64.getDecoder().decode(it) }.getOrNull() }
}

@KeepGeneratedSerializer
@Serializable(with = TerminalControlEventSerializer::class)
data class TerminalControlEvent(
    @SerialName("machine_id") val machineId: String,
    @SerialName("session_id") val sessionId: String,
    @WireEnum("active", "busy", "taken", "lost", "unsupported", "unknown") val state: String,
    val cols: Int? = null,
    val rows: Int? = null,
    val error: String? = null,
) : ServerEvent

@KeepGeneratedSerializer
@Serializable(with = ErrorEventSerializer::class)
data class ErrorEvent(
    @SerialName("machine_id") val machineId: String? = null,
    @SerialName("session_id") val sessionId: String? = null,
    /** The hub's words; empty when it sent none (the app says "Unknown error"). */
    val error: String = "",
    val code: String? = null,
    val retryable: Boolean? = null,
    @WireEnum("none", "unknown", "yes") val delivered: String? = null,
    val draft: String? = null,
    val clearable: String? = null,
    val limit: ImageLimits? = null,
    val got: ImageCount? = null,
) : ServerEvent {
    val sessionKey: SessionKey? get() = machineId?.let { machine -> sessionId?.let { SessionKey(machine, it) } }
    val body: ErrorBody get() = ErrorBody(error, code, retryable, delivered, draft, clearable, limit = limit, got = got)
}

object DeltaEventSerializer : WireSerializer<DeltaEvent>(DeltaEvent.generatedSerializer())
object ScreenEventSerializer : WireSerializer<ScreenEvent>(ScreenEvent.generatedSerializer())
object ToolCallEventSerializer : WireSerializer<ToolCallEvent>(ToolCallEvent.generatedSerializer())
object HookEventSerializer : WireSerializer<HookEvent>(HookEvent.generatedSerializer())
object LarkEventSerializer : WireSerializer<LarkEvent>(LarkEvent.generatedSerializer())
object InboxEventSerializer : WireSerializer<InboxEvent>(InboxEvent.generatedSerializer())
object PersonasEventSerializer : WireSerializer<PersonasEvent>(PersonasEvent.generatedSerializer())
object DesktopsEventSerializer : WireSerializer<DesktopsEvent>(DesktopsEvent.generatedSerializer())
object LookEventSerializer : WireSerializer<LookEvent>(LookEvent.generatedSerializer())
object TerminalEventSerializer : WireSerializer<TerminalEvent>(TerminalEvent.generatedSerializer())
object TerminalControlEventSerializer : WireSerializer<TerminalControlEvent>(TerminalControlEvent.generatedSerializer())
object ErrorEventSerializer : WireSerializer<ErrorEvent>(ErrorEvent.generatedSerializer())
