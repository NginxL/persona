package persona.feature.chat

import persona.core.designsystem.text
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.text.selection.SelectionContainer
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import android.content.res.Resources
import androidx.compose.ui.platform.LocalResources
import androidx.compose.ui.res.stringResource
import persona.core.contracts.Item
import persona.core.contracts.ModelRefusal
import persona.core.contracts.parseTime
import persona.core.designsystem.ChatNoteChip
import persona.core.designsystem.DetailField
import persona.core.designsystem.ErrorCard
import persona.core.designsystem.ErrorTag
import persona.core.designsystem.ErrorTagTone
import persona.core.designsystem.JButton
import persona.core.designsystem.JButtonKind
import persona.core.designsystem.JButtonSize
import persona.core.designsystem.JDivider
import persona.core.designsystem.JIconButton
import persona.core.designsystem.JSheet
import persona.core.designsystem.JTextButton
import persona.core.designsystem.PersonaTheme
import persona.core.designsystem.ReadingContent
import persona.core.designsystem.rememberCopy
import persona.core.designsystem.LocalizedFormats
import persona.core.designsystem.Tokens
import persona.core.designsystem.icons.PhIcons
import persona.core.domain.lines.ApiErrorInfo
import persona.core.domain.lines.ChatLine
import java.time.Instant
import java.time.ZoneId

/**
 * A model API error as a card (D6, iOS `APIErrorCard`): the reason in words with its status, "Temporary" or "Needs action" and
 * "N times in a row"; tapped, the request id and session to copy, the time and the raw text. "Retry" (sends "Continue") only on the
 * newest card while the agent is idle and the error may pass by itself ([retryLine]). When every model of a recovery chain
 * failed ([exhaustedRecovery], #7824), that card also says why and when the configured model is back, above "Retry", and
 * folds the models tried with its details.
 */
@Composable
internal fun ApiErrorLine(line: ChatLine.ApiError) {
    val actions = LocalChatRowActions.current
    val info = line.info
    var open by rememberSaveable(line.id) { mutableStateOf(false) }
    val copy = rememberCopy()
    val palette = PersonaTheme.palette
    val resources = LocalResources.current
    val retry = actions?.takeIf { it.retryLineId == line.id }?.onRetry
    val retryButton: (@Composable () -> Unit)? = if (retry != null) ({ JButton(stringResource(R.string.chat_api_error_retry), retry, kind = JButtonKind.Secondary, size = JButtonSize.Medium, icon = PhIcons.ArrowClockwise) }) else null
    val exhausted = actions?.exhausted?.takeIf { it.lineId == line.id }?.refusal
    val exhaustedLines = exhausted?.let { recoveryExhaustedLines(it, resources) }.orEmpty()
    ErrorCard(
        info.reason.text(), apiErrorTags(info, line.count, resources), open, { open = !open },
        action = if (exhaustedLines.isEmpty()) retryButton else ({
            Column(verticalArrangement = Arrangement.spacedBy(Tokens.Space.s)) {
                exhaustedLines.forEach { Text(it, style = PersonaTheme.type.callout, color = palette.ink2) }
                retryButton?.invoke()
            }
        }),
    ) {
        exhausted?.recoveryTried?.takeIf(String::isNotBlank)?.let {
            Text(stringResource(R.string.chat_model_fallback_recovery_tried, it), style = PersonaTheme.type.caption, color = palette.ink2)
        }
        info.requestId?.let { id -> DetailField(stringResource(R.string.chat_api_error_request_id), id) { JIconButton(PhIcons.Copy, stringResource(R.string.chat_api_error_copy_request_id), { copy(id, null) }) } }
        info.session?.let { session -> DetailField(stringResource(R.string.chat_api_error_session), session) { JIconButton(PhIcons.Copy, stringResource(R.string.chat_api_error_copy_session), { copy(session, null) }) } }
        apiErrorTime(line.item.item.ts, resources)?.let { DetailField(stringResource(R.string.chat_api_error_time), it, mono = false) }
        SelectionContainer {
            Text(info.raw, style = PersonaTheme.type.code, color = palette.ink,
                modifier = Modifier.fillMaxWidth().clip(PersonaTheme.shapes.controlShape).background(palette.sunken).padding(Tokens.Space.m))
        }
        JTextButton(stringResource(R.string.chat_api_error_copy_raw), { copy(info.raw, null) }, icon = PhIcons.Copy)
    }
}

/** The facts under an error's reason: its status, whether it may pass by itself, how many times in a row. */
internal fun apiErrorTags(info: ApiErrorInfo, count: Int, resources: Resources): List<ErrorTag> = buildList {
    info.status?.let { add(ErrorTag("$it", ErrorTagTone.Danger)) }
    when (info.transient) {
        true -> add(ErrorTag(resources.getString(R.string.chat_api_error_transient)))
        false -> add(ErrorTag(resources.getString(R.string.chat_api_error_needs_action), ErrorTagTone.Caution))
        null -> Unit
    }
    if (count > 1) add(ErrorTag(resources.getQuantityString(R.plurals.chat_api_error_repeated, count, count)))
}

/** "Nov 2 08:01:05" for an item's time, in the phone's zone; null without one. */
internal fun apiErrorTime(ts: String?, resources: Resources, zone: ZoneId = ZoneId.systemDefault()): String? = parseTime(ts)?.let {
    LocalizedFormats.format(it, LocalizedFormats.MONTH_DAY + LocalizedFormats.TIME_SECONDS, resources.configuration.locales[0], zone)
}

/**
 * The card that offers "Retry": the conversation's newest line when it is an API error that may pass by itself, the agent
 * is not working and the page can send. [exhausted]: every model of the conversation's recovery chain failed (#7824), so
 * the card keeps "Retry" whatever the error says: the configured model may be back by the time it is tapped. [lines] are
 * in reading order (oldest first).
 */
internal fun retryLine(lines: List<ChatLine>, working: Boolean, canSend: Boolean, exhausted: Boolean = false,
    turnFix: TurnErrorFix? = null): String? {
    val last = lines.lastOrNull() as? ChatLine.ApiError ?: return null
    val retryable = exhausted || !last.info.needsAction || turnFix == TurnErrorFix.ToolSearchUnsupported
    return last.id.takeIf { canSend && !working && retryable }
}

/** An exhausted recovery chain (#7824) on the card of [lineId]: [refusal] is the session's marker, with the cause, the recovery time and the models tried. */
internal data class ExhaustedRecovery(val lineId: String, val refusal: ModelRefusal)

/**
 * The exhausted recovery the conversation's newest line explains: the session's marker when every model of its recovery
 * chain failed (#7824) and that line is an API error, the turn's last failure; null otherwise, and from hubs that send
 * no such marker. [lines] are in reading order (oldest first).
 */
internal fun exhaustedRecovery(lines: List<ChatLine>, fallback: ModelRefusal?): ExhaustedRecovery? {
    val marker = fallback?.takeIf { it.recoveryExhausted } ?: return null
    val last = lines.lastOrNull() as? ChatLine.ApiError ?: return null
    return ExhaustedRecovery(last.id, marker)
}

/**
 * Claude Code's "while you were away" recap (several in a row are one line): "While you were away · 21 min ago ›"; tapped,
 * the recaps in full in a sheet, newest first (iOS `RecapDrawer`). The chat's open recap ([LocalOpenRecap], Glass) is
 * drawn in full instead ([RecapStrip]), the earlier ones of its run as the one-line marker above it (iOS `recapInFlow`).
 */
@Composable
internal fun RecapLine(items: List<Item>) {
    if (items.isEmpty()) return
    val shown = LocalOpenRecap.current?.takeIf { it.id == items.last().id }
    if (shown != null) {
        Column(verticalArrangement = Arrangement.spacedBy(Tokens.Space.s)) {
            RecapLine(items.dropLast(1))
            RecapStrip(items.last(), shown.close)
        }
        return
    }
    var open by rememberSaveable(items.first().id) { mutableStateOf(false) }
    val now by rememberNow(60_000)
    val newest = items.last()
    val resources = LocalResources.current
    val label = recapLabel(items.size, parseTime(newest.ts), now, resources)
    Box(Modifier.fillMaxWidth()) { ChatNoteChip(label, RecapIcon, onClick = { open = true }) }
    if (open) JSheet({ open = false }, title = stringResource(R.string.chat_recap_title), scrollable = true) {
        items.asReversed().forEachIndexed { index, item ->
            if (index > 0) JDivider(Modifier.padding(vertical = Tokens.Space.m))
            parseTime(item.ts)?.let {
                Text(stringResource(R.string.chat_recap_written, relativeTime(it, now, resources)), style = PersonaTheme.type.caption, color = PersonaTheme.palette.ink2)
                Spacer(Modifier.height(Tokens.Space.s))
            }
            ReadingContent { ChatMarkdown(item.text.orEmpty(), "recap/${item.id}") }
        }
    }
}

/** The recap's mark, folded ([RecapLine]) and open ([RecapStrip]). */
internal val RecapIcon get() = PhIcons.ClockCountdown

/** "While you were away · 21 min ago", with how many when there are several. */
internal fun recapLabel(count: Int, newest: Instant?, now: Instant, resources: Resources): String {
    val title = if (count > 1) resources.getQuantityString(R.plurals.chat_recap_count, count, count)
        else resources.getString(R.string.chat_recap_title)
    return newest?.let { resources.getString(R.string.chat_recap_with_time, title, relativeTime(it, now, resources)) } ?: title
}
