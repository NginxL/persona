package persona.feature.chat

import androidx.compose.foundation.background
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
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.res.pluralStringResource
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.text.SpanStyle
import androidx.compose.ui.text.buildAnnotatedString
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.text.withStyle
import persona.core.contracts.TaskBrief
import persona.core.contracts.parseTime
import persona.core.designsystem.JIcon
import persona.core.designsystem.JTextButton
import persona.core.designsystem.LocalizedFormats
import persona.core.designsystem.PersonaTheme
import persona.core.designsystem.Tokens
import persona.core.designsystem.currentLocale
import persona.core.designsystem.icons.PersonaIcons
import persona.core.designsystem.icons.PhIcon

private const val PLAN_BRIEF_FOLDED_LINES = 2

/**
 * The frozen brief Start confirms (docs/assistant/unified-entry.md "Briefs"). One inset well on every style: who asked
 * and when, a folded scope summary, and one disclosure for the request, goal, criteria, time and out-of-bounds.
 */
@Composable
internal fun PlanTaskBrief(brief: TaskBrief, title: String) {
    var open by rememberSaveable(brief.id, title) { mutableStateOf(false) }
    var cut by rememberSaveable(brief.id, title) { mutableStateOf(false) }
    val summary = briefSummary(brief)
    val hasGoal = brief.goalText.isNotEmpty() && brief.goalText != title.trim()
    val more = cut || open || summary.isNotEmpty() || hasGoal || !brief.handover.isNullOrBlank()
    Column(
        Modifier.fillMaxWidth().clip(PersonaTheme.shapes.controlShape).background(PersonaTheme.palette.sunken)
            .padding(horizontal = Tokens.Space.m, vertical = Tokens.Space.s).testTag("plan-brief"),
        verticalArrangement = Arrangement.spacedBy(Tokens.Space.xs),
    ) {
        BriefAsked(brief, open) { cut = it }
        if (open) BriefFields(brief, title)
        if (more) {
            Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(Tokens.Space.s)) {
                if (!open && summary.isNotEmpty()) {
                    Text(summary, Modifier.weight(1f), style = PersonaTheme.type.caption, color = PersonaTheme.palette.ink3,
                        maxLines = 2, overflow = TextOverflow.Ellipsis)
                }
                JTextButton(
                    stringResource(if (open) R.string.chat_content_quote_less else R.string.chat_task_brief_show_all),
                    { open = !open },
                    Modifier.testTag("plan-brief-toggle"),
                )
            }
        }
    }
}

/** Who asked, in their own words when the hub recorded them; never mistake a hand-over for those words. */
@Composable
private fun BriefAsked(brief: TaskBrief, open: Boolean, onCut: (Boolean) -> Unit) {
    val request = brief.request
    val requestText = request?.text?.trim().orEmpty()
    if (request != null && requestText.isNotEmpty()) {
        Column(verticalArrangement = Arrangement.spacedBy(Tokens.Space.xxs)) {
            BriefLine(
                PersonaIcons.quote,
                stringResource(R.string.chat_task_brief_asked_owner, briefDate(request.at)) + " ",
                requestText,
                PersonaTheme.palette.ink,
                open,
                onCut,
            )
            if (request.truncated) {
                Text(stringResource(R.string.chat_task_brief_asked_cut), style = PersonaTheme.type.caption,
                    color = PersonaTheme.palette.ink3)
            }
        }
        return
    }
    if (brief.reconstructed == true) {
        Column(verticalArrangement = Arrangement.spacedBy(Tokens.Space.xs)) {
            BriefLine(PersonaIcons.info, "", stringResource(R.string.chat_task_brief_reconstructed),
                PersonaTheme.palette.ink2, open, onCut)
            val handover = brief.handover?.trim().orEmpty()
            if (handover.isNotEmpty()) {
                Text(stringResource(R.string.chat_task_brief_handover), style = PersonaTheme.type.caption.copy(fontWeight = FontWeight.SemiBold),
                    color = PersonaTheme.palette.ink3, maxLines = 1)
                BriefClampedText(handover, PersonaTheme.palette.ink2, open, onCut)
            }
        }
        return
    }
    val date = briefDate(brief.createdAt)
    val line = when (brief.startedBy) {
        "assistant" -> stringResource(R.string.chat_task_brief_started_assistant, date)
        "leader" -> stringResource(R.string.chat_task_brief_started_leader, date)
        "routine" -> stringResource(R.string.chat_task_brief_started_routine, date)
        "hub" -> stringResource(R.string.chat_task_brief_started_hub, date)
        else -> stringResource(R.string.chat_task_brief_started_unknown, date)
    }
    val why = brief.reason?.trim().takeUnless { it.isNullOrEmpty() } ?: brief.triggerTitle?.trim().orEmpty()
    val words = if (why.isEmpty()) line else stringResource(R.string.chat_task_brief_started_why, line, why)
    val icon = if (brief.startedBy in setOf("assistant", "leader", "routine", "hub")) PersonaIcons.ai else PersonaIcons.info
    BriefLine(icon, "", words, PersonaTheme.palette.ink2, open, onCut)
}

@Composable
private fun BriefLine(icon: PhIcon, prefix: String, text: String, color: androidx.compose.ui.graphics.Color,
    open: Boolean, onCut: (Boolean) -> Unit) {
    Row(horizontalArrangement = Arrangement.spacedBy(Tokens.Space.s)) {
        JIcon(icon, decorative = true, size = Tokens.Size.markXS, tint = PersonaTheme.palette.ink3,
            modifier = Modifier.padding(top = Tokens.Space.xxs))
        val words = buildAnnotatedString {
            if (prefix.isNotEmpty()) withStyle(SpanStyle(color = PersonaTheme.palette.ink2)) { append(prefix) }
            append(text)
        }
        BriefClampedText(words, color, open, onCut, Modifier.weight(1f))
    }
}

@Composable
private fun BriefClampedText(text: String, color: androidx.compose.ui.graphics.Color, open: Boolean, onCut: (Boolean) -> Unit) =
    BriefClampedText(buildAnnotatedString { append(text) }, color, open, onCut, Modifier)

@Composable
private fun BriefClampedText(text: androidx.compose.ui.text.AnnotatedString, color: androidx.compose.ui.graphics.Color,
    open: Boolean, onCut: (Boolean) -> Unit, modifier: Modifier) {
    SelectionContainer {
        Text(text, modifier.fillMaxWidth(), style = PersonaTheme.type.caption, color = color,
            maxLines = if (open) Int.MAX_VALUE else PLAN_BRIEF_FOLDED_LINES, overflow = TextOverflow.Ellipsis,
            onTextLayout = { if (!open) onCut(it.hasVisualOverflow) })
    }
}

@Composable
private fun BriefFields(brief: TaskBrief, title: String) {
    Column(verticalArrangement = Arrangement.spacedBy(Tokens.Space.xs)) {
        if (brief.goalText.isNotEmpty() && brief.goalText != title.trim()) {
            BriefField(stringResource(R.string.chat_goal_title), listOf(brief.goalText))
        }
        if (brief.criteriaNow.isNotEmpty()) BriefField(stringResource(R.string.chat_task_brief_done_when), brief.criteriaNow)
        if (brief.expect > 0) BriefField(stringResource(R.string.chat_task_brief_expected_time), listOf(expectText(brief.expect)))
        if (brief.bounds.isNotEmpty()) BriefField(stringResource(R.string.chat_task_brief_out_of_bounds), brief.bounds)
    }
}

@Composable
private fun BriefField(label: String, items: List<String>) {
    Column(verticalArrangement = Arrangement.spacedBy(Tokens.Space.xxs)) {
        Text(label, style = PersonaTheme.type.caption.copy(fontWeight = FontWeight.SemiBold), color = PersonaTheme.palette.ink3, maxLines = 1)
        SelectionContainer {
            Column(verticalArrangement = Arrangement.spacedBy(Tokens.Space.xxs)) {
                items.forEach { item ->
                    Text(if (items.size > 1) "• $item" else item, Modifier.fillMaxWidth(),
                        style = PersonaTheme.type.caption, color = PersonaTheme.palette.ink2)
                }
            }
        }
    }
}

/** "1 criterion · about 45 min · 1 out of bounds": the folded scope, using its effective values. */
@Composable
private fun briefSummary(brief: TaskBrief): String {
    val parts = mutableListOf<String>()
    val criteria = brief.criteriaNow.size
    if (criteria > 0) parts += pluralStringResource(R.plurals.chat_task_brief_summary_criteria, criteria, criteria)
    if (brief.expect > 0) {
        parts += if (brief.expect < 120) stringResource(R.string.chat_task_brief_summary_minutes, brief.expect)
        else stringResource(R.string.chat_task_brief_summary_hours, hoursText(brief.expect))
    }
    val bounds = brief.bounds.size
    if (bounds > 0) parts += pluralStringResource(R.plurals.chat_task_brief_summary_bounds, bounds, bounds)
    return parts.joinToString(" · ")
}

@Composable
private fun expectText(minutes: Int): String = if (minutes < 120) {
    stringResource(R.string.chat_review_expect, minutes)
} else {
    stringResource(R.string.chat_task_brief_expect_hours, hoursText(minutes))
}

@Composable
private fun hoursText(minutes: Int): String = LocalizedFormats.number(((minutes * 10 + 30) / 60) / 10.0, currentLocale)

@Composable
private fun briefDate(value: String?): String = parseTime(value)?.let { LocalizedFormats.format(it, LocalizedFormats.DATE, currentLocale) }.orEmpty()
