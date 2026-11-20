package persona.feature.knowledge

import androidx.annotation.StringRes
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.imePadding
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.res.stringResource
import java.time.Duration
import java.time.Instant
import kotlinx.serialization.json.JsonArray
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.JsonPrimitive
import kotlinx.serialization.json.doubleOrNull
import persona.core.contracts.GroupKeys
import persona.core.contracts.JsonCodec
import persona.core.contracts.Machine
import persona.core.contracts.Session
import persona.core.contracts.api.KnowledgeDetail
import persona.core.contracts.api.MeetingHandoff
import persona.core.contracts.parseTime
import persona.core.designsystem.GroupPosition
import persona.core.designsystem.JButton
import persona.core.designsystem.JGroupItem
import persona.core.designsystem.JNotice
import persona.core.designsystem.JRadio
import persona.core.designsystem.JRow
import persona.core.designsystem.JSectionHeader
import persona.core.designsystem.JSheet
import persona.core.designsystem.JSwitchRow
import persona.core.designsystem.JTextButton
import persona.core.designsystem.JTextField
import persona.core.designsystem.PersonaTheme
import persona.core.designsystem.Status
import persona.core.designsystem.Tokens
import persona.core.designsystem.UiText
import persona.core.designsystem.icons.PersonaIcons
import persona.core.designsystem.operationFeedback
import persona.core.designsystem.text
import persona.core.navigation.ChatRoute
import persona.core.navigation.ConversationKind
import persona.core.navigation.Navigator
import persona.feature.inbox.handOffTargets

private val MeetingRunningStale: Duration = Duration.ofHours(12)
private val RunningRooms = setOf("starting", "ready", "unavailable")
private val EndedRooms = setOf("none", "ended", "deleted")

/** Inbox Hand to AI's latest first, on this hub's online computers, less the targets the hub refuses for a meeting. */
internal fun meetingHandoffTargets(
    sessions: List<Session>,
    machines: List<Machine>,
    namespace: String? = null,
): List<Session> {
    val online = machines.filter { it.online && GroupKeys.namespaceOf(it.id) == namespace }.associate { it.id to it.displayName }
    val eligible = sessions.filter {
        GroupKeys.namespaceOf(it.machineId) == namespace && online.containsKey(it.machineId) &&
            !it.isSubagent && it.status != "interrupted" && it.permissionMode != "bypassPermissions"
    }
    val byKey = eligible.associateBy { it.key.path }
    return handOffTargets(eligible, online).mapNotNull { byKey[it.key] }
}

/** The transcript is still on the hub: non-echo lines, or the hub's nonzero character count when lines are not loaded. */
internal fun meetingHandoffTranscriptOffered(detail: KnowledgeDetail): Boolean {
    if (detail.kind != "recordings" || detail.meta.string("trimmed_at").isNotBlank()) return false
    val chars = (detail.meta?.get("chars") as? JsonPrimitive)?.doubleOrNull ?: 0.0
    return chars > 0 || transcript(detail.segments).isNotEmpty()
}

private fun meetingHandoffRunning(detail: KnowledgeDetail, now: Instant): Boolean {
    val meta = detail.meta
    if (meta.string("trimmed_at").isNotBlank()) return false
    if (parseTime(meta.string("ended_at")) == null) {
        val updated = parseTime(meta.string("updated")) ?: return true
        if (Duration.between(updated, now) < MeetingRunningStale) return true
    }
    val room = meta?.get("room") as? JsonObject ?: return false
    val state = room.string("state")
    return state in RunningRooms || state !in EndedRooms
}

/** A finished, non-all-day recording with minutes or transcript, and at least one Agent that can take it. */
internal fun meetingHandoffEligible(detail: KnowledgeDetail, targets: List<Session>, now: Instant = Instant.now()): Boolean {
    if (detail.kind != "recordings" || detail.meta.string("kind") == "day" || targets.isEmpty()) return false
    val material = detail.minutes != null || meetingHandoffTranscriptOffered(detail)
    return material && !meetingHandoffRunning(detail, now)
}

/** The latest readable kept try. Unknown states and malformed entries do not hide the one before them. */
internal fun latestMeetingHandoff(meta: JsonObject?, namespace: String? = null): MeetingHandoff? {
    val history = meta?.get("handoffs") as? JsonArray ?: return null
    return history.mapNotNull { item ->
        val obj = item as? JsonObject ?: return@mapNotNull null
        runCatching { JsonCodec.decodeFromString(MeetingHandoff.serializer(), obj.toString()) }.getOrNull()
            ?.takeIf { it.state == "sent" || it.state == "failed" }
            ?.inMeetingGroup(namespace)
    }.lastOrNull()
}

internal fun MeetingHandoff.inMeetingGroup(namespace: String?): MeetingHandoff =
    if (namespace == null) this else copy(machineId = GroupKeys.machine(machineId, namespace))

/** A just-made result stays until the detail carries a newer one (including when a reload is slower than the write). */
internal fun newerMeetingHandoff(made: MeetingHandoff?, kept: MeetingHandoff?): MeetingHandoff? {
    if (made == null) return kept
    if (kept == null) return made
    val madeAt = parseTime(made.at)
    val keptAt = parseTime(kept.at)
    return if (madeAt == null || keptAt == null || madeAt >= keptAt) made else kept
}

@StringRes
internal fun meetingHandoffProblem(problem: String?): Int = when (problem) {
    "not_agent" -> R.string.knowledge_handoff_problem_not_agent
    "not_allowed" -> R.string.knowledge_handoff_problem_not_allowed
    "unavailable" -> R.string.knowledge_handoff_problem_unavailable
    else -> R.string.knowledge_handoff_problem_send_failed
}

internal fun meetingHandoffCallError(error: Exception): UiText {
    val failure = persona.core.contracts.api.hubCodedFailure(error)
    return when (failure?.code) {
        "meeting_running" -> UiText.Res(R.string.knowledge_handoff_running)
        "no_material" -> UiText.Res(R.string.knowledge_handoff_no_material)
        else -> operationFeedback(error).text
    }
}

/** The latest try above the meeting's material: Open its Agent, or Retry with the failed choices. */
@Composable
internal fun MeetingHandoffRow(
    handoff: MeetingHandoff,
    navigator: Navigator,
    modifier: Modifier = Modifier,
    onRetry: (() -> Unit)? = null,
) {
    val failed = handoff.state == "failed"
    val name = handoff.title?.takeIf { it.isNotBlank() } ?: stringResource(R.string.knowledge_handoff_agent)
    JGroupItem(GroupPosition.Single, modifier = modifier) {
        JRow(
            stringResource(if (failed) R.string.knowledge_handoff_row_failed else R.string.knowledge_handoff_row_sent, name),
            subtitle = if (failed) stringResource(meetingHandoffProblem(handoff.problem)) else null,
            leadingIcon = if (failed) PersonaIcons.error else PersonaIcons.handToAI,
            chevron = false,
            titleLines = 2,
            subtitleLines = Int.MAX_VALUE,
            trailing = {
                if (failed) {
                    onRetry?.let { JTextButton(stringResource(R.string.knowledge_handoff_retry), it) }
                } else {
                    JTextButton(stringResource(R.string.knowledge_handoff_open), onClick = {
                        navigator.navigate(ChatRoute(handoff.machineId, handoff.sessionId, ConversationKind.Agent))
                    })
                }
            },
        )
    }
}

/** Pick an Agent, an optional line, and whether the retained transcript goes too. Sent closes; kept failure stays here. */
@Composable
internal fun MeetingHandoffSheet(
    targets: List<Session>,
    machines: List<Machine>,
    transcriptOffered: Boolean,
    again: MeetingHandoff?,
    work: KnowledgeMeetingHandoffWork,
    onSend: (Session, String, Boolean) -> Unit,
    onDismiss: () -> Unit,
) {
    // Read once: a session coming or going must not move the list while the owner writes.
    val choices = remember { targets }
    val names = remember { machines.associate { it.id to it.displayName } }
    val retryKey = again?.let { "${it.machineId}/${it.sessionId}" }
    var pickedKey by rememberSaveable(again?.id) { mutableStateOf(retryKey) }
    val picked = choices.firstOrNull { it.key.path == pickedKey } ?: choices.firstOrNull()
    var instruction by rememberSaveable(again?.id) { mutableStateOf("") }
    var includeTranscript by rememberSaveable(again?.id, transcriptOffered) {
        mutableStateOf(transcriptOffered && again?.transcript == true)
    }
    val failed = work.made ?: again
    val error = when {
        work.busy -> null
        work.error != null -> work.error.text()
        failed?.state == "failed" -> stringResource(meetingHandoffProblem(failed.problem))
        else -> null
    }
    JSheet(onDismiss = onDismiss, title = stringResource(R.string.knowledge_handoff_title), dismissible = !work.busy, scrollable = true) {
        Column(Modifier.fillMaxWidth().imePadding()) {
            JSectionHeader(stringResource(R.string.knowledge_handoff_pick))
            choices.forEachIndexed { index, agent ->
                JGroupItem(GroupPosition.of(index, choices.size)) {
                    JRow(agent.displayTitle, subtitle = names[agent.machineId] ?: agent.machineId, leadingIcon = PersonaIcons.handToAI,
                        onClick = { if (!work.busy) pickedKey = agent.key.path }, chevron = false, enabled = !work.busy,
                        trailing = { JRadio(picked == agent, null) })
                }
            }
            Spacer(Modifier.height(Tokens.Space.m))
            Text(stringResource(R.string.knowledge_handoff_ask_label), style = PersonaTheme.type.callout, color = PersonaTheme.palette.ink2)
            JTextField(instruction, { instruction = it }, placeholder = stringResource(R.string.knowledge_handoff_ask_placeholder),
                minLines = 2, maxLines = 5, enabled = !work.busy)
            if (transcriptOffered) {
                Spacer(Modifier.height(Tokens.Space.s))
                JGroupItem(GroupPosition.Single) {
                    JSwitchRow(stringResource(R.string.knowledge_handoff_transcript), includeTranscript,
                        { includeTranscript = it }, enabled = !work.busy)
                }
            }
            Spacer(Modifier.height(Tokens.Space.s))
            Text(stringResource(R.string.knowledge_handoff_gets), style = PersonaTheme.type.caption, color = PersonaTheme.palette.ink3)
            error?.let {
                Spacer(Modifier.height(Tokens.Space.m))
                JNotice(it, status = Status.NeedsYou)
            }
            Spacer(Modifier.height(Tokens.Space.section))
            JButton(stringResource(R.string.knowledge_handoff_send), {
                picked?.let { onSend(it, instruction, includeTranscript && transcriptOffered) }
            }, Modifier.fillMaxWidth(), icon = PersonaIcons.handToAI, enabled = picked != null && !work.busy,
                loading = work.busy, loadingText = stringResource(R.string.knowledge_handoff_send))
        }
    }
}
