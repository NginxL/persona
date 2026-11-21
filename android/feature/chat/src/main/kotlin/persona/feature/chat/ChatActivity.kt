package persona.feature.chat

import android.content.res.Resources
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.text.selection.SelectionContainer
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.produceState
import androidx.compose.runtime.remember
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalResources
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.semantics.Role
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextOverflow
import kotlinx.coroutines.delay
import persona.core.contracts.BackgroundTask
import persona.core.contracts.LoopInfo
import persona.core.contracts.Session
import persona.core.designsystem.AppStyle
import persona.core.designsystem.JActivityPill
import persona.core.designsystem.JButton
import persona.core.designsystem.JButtonKind
import persona.core.designsystem.JButtonSize
import persona.core.designsystem.JCard
import persona.core.designsystem.JDialog
import persona.core.designsystem.JIcon
import persona.core.designsystem.LocalizedFormats
import persona.core.designsystem.PersonaTheme
import persona.core.designsystem.Tokens
import persona.core.designsystem.icons.PersonaIcons
import java.time.Duration
import java.time.Instant
import java.time.ZoneId

/**
 * The Agent's activity entry (#4339 C8). On Glass the pill opens Background Tasks and carries the loop's next wake.
 * Brief and Board have the same destination in the chat menu.
 */
internal object ChatActivity {
    /** Glass shows them in the header of an Agent's chat; the other styles and Persona's conversations do not. */
    fun inHeader(style: AppStyle, topBar: Boolean, render: ChatRenderStyle): Boolean =
        style == AppStyle.Glass && topBar && render != ChatRenderStyle.Assistant

    /** A loop or running work earns an activity pill. The menu also serves empty and completed-only lists. */
    fun shown(session: Session?, now: Instant = Instant.now()): Boolean =
        session != null && (session.loop != null || backgroundRunningCount(session, now) > 0)

    /**
     * What Stop Loop sends (iOS `AppModel.stopLoop`, the same words): nothing outside the agent can cancel its
     * scheduled wake-up or its cron job, so the agent is asked to, and it stops scheduling.
     */
    const val STOP_LOOP = "停止当前的 /loop：调用 ScheduleWakeup(stop: true) 结束循环（如果用 CronCreate 建过定时任务，用 CronDelete 删掉），" + // i18n-ignore: a prompt sent to the agent, iOS's words
        "不要再安排下一次唤醒，回复一句「循环已停止」即可。" // i18n-ignore: a prompt sent to the agent, iOS's words

    fun next(loop: LoopInfo): Instant? = loop.nextAt?.let { runCatching { Instant.parse(it) }.getOrNull() }

    /** The task, without the "/loop" command word (iOS `LoopInfo.task`). */
    fun task(loop: LoopInfo): String {
        val prompt = loop.prompt.orEmpty().trim()
        return if (prompt.startsWith("/loop")) prompt.removePrefix("/loop").trim() else prompt
    }

    /** "Every 30 minutes" / "Cron 33 17 * * *" (iOS `LoopInfo.cadence`); null when the hub says neither. */
    fun cadence(res: Resources, loop: LoopInfo): String? {
        if (loop.kind == "cron") loop.cron?.takeIf(String::isNotBlank)?.let { return res.getString(R.string.chat_activity_cron, it) }
        val s = loop.intervalSeconds?.takeIf { it > 0 } ?: return null
        return when {
            s < 120 -> res.getQuantityString(R.plurals.chat_activity_every_seconds, s, s)
            s < 7200 -> res.getQuantityString(R.plurals.chat_activity_every_minutes, s / 60, s / 60)
            else -> res.getQuantityString(R.plurals.chat_activity_every_hours, s / 3600, s / 3600)
        }
    }

    /** The pill's loop words (iOS `shortLoopLabel`): the next time, Running once it is due, else Loop. */
    fun shortLabel(res: Resources, loop: LoopInfo, now: Instant, zone: ZoneId = ZoneId.systemDefault()): String {
        val next = next(loop)
        return when {
            next != null && next.isAfter(now) -> time(res, next, zone)
            loop.nextAt != null -> res.getString(R.string.chat_activity_running)
            else -> res.getString(R.string.chat_activity_loop)
        }
    }

    /** The loop in full words, for the screen reader (iOS `loopLabel`): "Next 14:05", "Loop running" or its cadence. */
    fun label(res: Resources, loop: LoopInfo, now: Instant, zone: ZoneId = ZoneId.systemDefault()): String {
        val next = next(loop)
        return when {
            next != null && next.isAfter(now) -> res.getString(R.string.chat_activity_next, time(res, next, zone))
            loop.nextAt != null -> res.getString(R.string.chat_activity_loop_running)
            else -> cadence(res, loop) ?: res.getString(R.string.chat_activity_looping)
        }
    }

    /** What the pill says to the screen reader (iOS `ActivityChips.a11yLabel`): "Looping, Next 14:05, 2 background tasks, tap to view". */
    fun spoken(res: Resources, loop: LoopInfo?, background: Int, now: Instant, zone: ZoneId = ZoneId.systemDefault()): String {
        val parts = listOfNotNull(
            loop?.let { res.getString(R.string.chat_activity_looping_spoken, label(res, it, now, zone)) },
            background.takeIf { it > 0 }?.let { res.getQuantityString(R.plurals.chat_activity_background_count, it, it) },
        )
        return res.getString(R.string.chat_activity_tap_to_view, parts.joinToString(res.getString(R.string.chat_activity_list_separator)))
    }

    /** "4 minutes left" / "1 h 5 min left" / "2 hours left" (iOS `LoopStrip.until`). */
    fun until(res: Resources, date: Instant, now: Instant): String {
        val m = maxOf(1L, Math.ceil(Duration.between(now, date).toMillis() / 60_000.0).toLong()).toInt()
        return when {
            m < 60 -> res.getQuantityString(R.plurals.chat_activity_minutes_left, m, m)
            m % 60 > 0 -> res.getString(R.string.chat_activity_hours_minutes_left, m / 60, m % 60)
            else -> res.getQuantityString(R.plurals.chat_activity_hours_left, m / 60, m / 60)
        }
    }

    /** A task by what it is for (iOS `BackgroundTask.title`): its description, its command's first line, else its kind. */
    fun taskTitle(res: Resources, task: BackgroundTask): String =
        task.description?.takeIf(String::isNotBlank)
            ?: task.command?.lineSequence()?.firstOrNull { it.isNotBlank() }?.trim()
            ?: res.getString(if (task.kind == "agent") R.string.chat_activity_subagent else R.string.chat_activity_background_command)

    private fun time(res: Resources, at: Instant, zone: ZoneId): String =
        LocalizedFormats.format(at, LocalizedFormats.TIME, res.configuration.locales[0], zone)
}

/** The clock the pill and the sheet read, every [millis] (iOS `TimelineView(.periodic)`). */
@Composable
private fun ticking(millis: Long): Instant {
    val now by produceState(Instant.now(), millis) { while (true) { delay(millis); value = Instant.now() } }
    return now
}

/** The pill at the end of the header's second row (iOS `ActivityChips`). */
@Composable
internal fun ChatActivityPill(session: Session, onOpen: () -> Unit) {
    val res = LocalResources.current
    val now = ticking(30_000)
    val loop = session.loop
    val background = backgroundRunningCount(session, now)
    JActivityPill(loop?.let { ChatActivity.shortLabel(res, it, now) }, background,
        ChatActivity.spoken(res, loop, background, now), onOpen)
}

/** The header's second row with nothing but the pill, held at its trailing end as beside the tabs. */
@Composable
internal fun ChatActivityRow(pill: @Composable () -> Unit) {
    Row(Modifier.fillMaxWidth().padding(horizontal = Tokens.Space.l, vertical = Tokens.Space.xs),
        horizontalArrangement = Arrangement.End, verticalAlignment = Alignment.CenterVertically) { pill() }
}

/** The loop driving the agent (iOS `LoopStrip`, open): what it repeats, how often, when it wakes next, and Stop Loop. */
@Composable
internal fun BackgroundLoopStrip(loop: LoopInfo, onStop: (() -> Unit)?) {
    val res = LocalResources.current
    val palette = PersonaTheme.palette
    var open by rememberSaveable { mutableStateOf(true) }
    var confirming by remember { mutableStateOf(false) }
    val now = ticking(30_000)
    JCard(padding = Tokens.Space.flush) {
        Column(Modifier.padding(horizontal = Tokens.Space.l, vertical = Tokens.Space.m), verticalArrangement = Arrangement.spacedBy(Tokens.Space.s)) {
            Row(Modifier.fillMaxWidth().clickable(role = Role.Button, onClickLabel = stringResource(if (open) R.string.chat_activity_collapse else R.string.chat_activity_expand)) { open = !open },
                verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(Tokens.Space.s)) {
                JIcon(PersonaIcons.inProgress, null, size = Tokens.Size.markS, tint = if (palette.dark) Tokens.Common.Dark.loop else Tokens.Common.Light.loop)
                Column(Modifier.weight(1f), verticalArrangement = Arrangement.spacedBy(Tokens.Space.xxs)) {
                    Text(ChatActivity.cadence(res, loop)?.let { stringResource(R.string.chat_activity_looping_cadence, it) } ?: stringResource(R.string.chat_activity_looping),
                        style = PersonaTheme.type.caption.copy(fontWeight = FontWeight.SemiBold), color = palette.ink, maxLines = 1, overflow = TextOverflow.Ellipsis)
                    ChatActivity.next(loop)?.let { next ->
                        Text(if (next.isAfter(now)) stringResource(R.string.chat_activity_next_wakeup,
                                LocalizedFormats.format(next, LocalizedFormats.TIME, res.configuration.locales[0], ZoneId.systemDefault()),
                                ChatActivity.until(res, next, now))
                            else stringResource(R.string.chat_activity_running_round),
                            style = PersonaTheme.type.micro, color = palette.ink2, maxLines = 1, overflow = TextOverflow.Ellipsis)
                    }
                }
                JButton(stringResource(R.string.chat_activity_stop), { confirming = true }, kind = JButtonKind.Destructive,
                    size = JButtonSize.Small, enabled = onStop != null)
            }
            if (open) {
                ChatActivity.task(loop).takeIf(String::isNotBlank)?.let {
                    SelectionContainer { Text(it, style = PersonaTheme.type.callout, color = palette.ink2) }
                }
                loop.reason?.takeIf(String::isNotBlank)?.let { reason ->
                    Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(Tokens.Space.xs)) {
                        JIcon(PersonaIcons.agentChat, null, size = Tokens.Size.markS, tint = palette.ink3)
                        Text(reason, style = PersonaTheme.type.micro, color = palette.ink3)
                    }
                }
            }
        }
    }
    if (confirming) JDialog(onDismiss = { confirming = false }, title = stringResource(R.string.chat_activity_stop_title),
        text = stringResource(R.string.chat_activity_stop_text),
        confirm = stringResource(R.string.chat_activity_stop) to { confirming = false; onStop?.invoke() }, destructive = true)
}
