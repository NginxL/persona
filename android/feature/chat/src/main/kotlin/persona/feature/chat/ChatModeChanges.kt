package persona.feature.chat

import android.content.res.Resources
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.BoxWithConstraints
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.widthIn
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.platform.LocalResources
import androidx.compose.ui.semantics.clearAndSetSemantics
import androidx.compose.ui.semantics.contentDescription
import androidx.compose.ui.text.rememberTextMeasurer
import androidx.compose.ui.text.style.TextAlign
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.CoroutineStart
import kotlinx.coroutines.Job
import kotlinx.coroutines.delay
import kotlinx.coroutines.launch
import java.time.Instant
import persona.core.contracts.ModeChange
import persona.core.contracts.parseTime
import persona.core.contracts.Session
import persona.core.contracts.SessionKey
import persona.core.designsystem.PersonaTheme
import persona.core.designsystem.Tokens
import persona.core.domain.lines.ChatLine
import persona.core.domain.sort.listedModes

/** Only records up to the last item a frozen projection shows; live following has no cutoff (iOS `shownModes`). */
internal fun modeChangesCutoff(frozen: Boolean, lines: List<ChatLine>): Instant? {
    if (!frozen) return null
    return lines.asReversed().firstNotNullOfOrNull { line -> line.lastItem()?.let { parseTime(it.ts) } } ?: Instant.EPOCH
}

/** Reads an Agent's mode history on open and mode-key edges; no timer, refresh or foreground poll (#10297). */
internal class ModeChangeStore(
    private val scope: CoroutineScope,
    private val read: suspend (SessionKey) -> List<ModeChange>,
) {
    var changes by mutableStateOf<List<ModeChange>>(emptyList())
        private set
    private val modeKeys = mutableMapOf<SessionKey, String>()
    private var current: SessionKey? = null
    private var job: Job? = null
    private var watchVersion = 0L

    fun watch(key: SessionKey, session: Session, agentChat: Boolean) {
        val version = ++watchVersion
        job?.cancel()
        if (current != key) {
            current = key
            changes = emptyList()
        }
        if (!agentChat) {
            changes = emptyList()
            return
        }
        val modeKey = "${session.permissionMode.orEmpty()}|${session.bypassPending}"
        val before = modeKeys.put(key, modeKey)
        job = scope.launch(start = CoroutineStart.UNDISPATCHED) {
            load(key, version)
            // The manager writes its row as the node answers, just after the session row can show the new mode.
            if (before != null && before != modeKey) {
                delay(MODE_CHANGE_FOLLOWUP_MILLIS)
                load(key, version)
            }
        }
    }

    fun close() {
        watchVersion++
        job?.cancel()
    }

    private suspend fun load(key: SessionKey, version: Long) {
        try {
            val read = read(key)
            // Empty and failed answers keep what is shown; an older hub and a refused token have no record to add.
            if (version == watchVersion && current == key && read.isNotEmpty() && changes != read) changes = read
        } catch (cancelled: CancellationException) {
            throw cancelled
        } catch (_: Exception) {
            // A history-decoration failure never replaces or clears the transcript.
        }
    }
}

private const val MODE_CHANGE_FOLLOWUP_MILLIS = 2_000L

/** The web/iOS mode-change sentences, using the session's own mode names where it lists them. */
internal object ModeChangeWords {
    private fun mode(id: String?, session: Session?, res: Resources): String {
        if (id.isNullOrEmpty()) return ""
        return if (session?.listedModes?.contains(id) == true) SessionModes.listedOption(id).title(res)
        else SessionModes.modeLabel(id, res).orEmpty()
    }

    private fun who(change: ModeChange, res: Resources): String =
        if (change.actorKind == "hub") res.getString(R.string.chat_mode_change_by_hub)
        else change.deviceName?.trim()?.takeIf { it.isNotEmpty() } ?: res.getString(R.string.chat_mode_change_by_unknown)

    private fun why(code: String?, res: Resources): String? = when (code) {
        "cancelled" -> R.string.chat_mode_change_why_cancelled
        "replaced" -> R.string.chat_mode_change_why_replaced
        "process_exited" -> R.string.chat_mode_change_why_process_exited
        "process_replaced" -> R.string.chat_mode_change_why_process_replaced
        "process_unknown" -> R.string.chat_mode_change_why_process_unknown
        "expired" -> R.string.chat_mode_change_why_expired
        "not_stopped" -> R.string.chat_mode_change_why_not_stopped
        "start_failed" -> R.string.chat_mode_change_why_start_failed
        "stopped" -> R.string.chat_mode_change_why_stopped
        else -> null
    }?.let(res::getString)

    fun label(change: ModeChange, res: Resources, session: Session? = null): String {
        val to = mode(change.to, session, res)
        when (change.kind) {
            "restart_done" -> return res.getString(if (change.reason == "already_in")
                R.string.chat_mode_change_restart_already else R.string.chat_mode_change_restart_done, to)
            "restart_failed" -> {
                val reason = why(change.reason, res)
                return if (reason == null) res.getString(R.string.chat_mode_change_restart_failed, to)
                else res.getString(R.string.chat_mode_change_restart_failed_why, to, reason)
            }
            "restart_dropped" -> {
                val reason = why(change.reason, res)
                return if (reason == null) res.getString(R.string.chat_mode_change_restart_dropped, to)
                else res.getString(R.string.chat_mode_change_restart_dropped_why, to, reason)
            }
        }
        val who = who(change, res)
        if (change.applied == "restart") return res.getString(R.string.chat_mode_change_restart_queued, who, to)
        val from = mode(change.from, session, res)
        return if (from.isEmpty() || from == to) res.getString(R.string.chat_mode_change_changed_to, who, to)
        else res.getString(R.string.chat_mode_change_changed_from, who, from, to)
    }
}

/** A noninteractive timeline separator; long wording wraps alone instead of leaving dotted or crooked hairlines. */
@Composable
internal fun ModeChangeDivider(change: ModeChange, session: Session? = null) {
    val label = ModeChangeWords.label(change, LocalResources.current, session)
    val palette = PersonaTheme.palette
    val type = PersonaTheme.type.micro
    val measurer = rememberTextMeasurer()
    val density = LocalDensity.current
    BoxWithConstraints(Modifier.fillMaxWidth().clearAndSetSemantics { contentDescription = label }) {
        val words = measurer.measure(label, style = type, maxLines = 1, softWrap = false).size.width
        val beside = with(density) { ((Tokens.Space.l + Tokens.Space.m) * 2).toPx() }
        val oneLine = words + beside + 1 <= with(density) { maxWidth.toPx() }
        if (oneLine) {
            Row(
                Modifier.fillMaxWidth().padding(vertical = Tokens.Space.s),
                horizontalArrangement = Arrangement.spacedBy(Tokens.Space.m),
                verticalAlignment = Alignment.CenterVertically,
            ) {
                Box(Modifier.weight(1f).widthIn(min = Tokens.Space.l).height(Tokens.Size.line).background(palette.line))
                Text(label, style = type, color = palette.ink3, maxLines = 1, softWrap = false)
                Box(Modifier.weight(1f).widthIn(min = Tokens.Space.l).height(Tokens.Size.line).background(palette.line))
            }
        } else {
            Text(
                label,
                Modifier.fillMaxWidth().padding(vertical = Tokens.Space.s),
                style = type,
                color = palette.ink3,
                textAlign = TextAlign.Center,
            )
        }
    }
}
