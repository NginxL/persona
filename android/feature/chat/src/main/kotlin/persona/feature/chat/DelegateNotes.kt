package persona.feature.chat

import androidx.annotation.StringRes
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.text.font.FontWeight
import persona.core.contracts.Interaction
import persona.core.contracts.parseTime
import persona.core.designsystem.*
import persona.core.designsystem.icons.PersonaIcons
import persona.core.domain.answer.Delegated

private const val QUESTIONS_SEPARATOR = "；" // i18n-ignore: the hub's wire format for several answers

/** The option labels the assistant recommends on each question, when its words are exactly those options. */
internal fun Interaction.recommendedLabels(): List<List<String>> {
    val questions = questions.orEmpty()
    val recommendation = delegate?.recommend?.trim().orEmpty()
    val parts = if (questions.size > 1) recommendation.split(QUESTIONS_SEPARATOR) else listOf(recommendation)
    val fits = parts.size == questions.size
    return questions.mapIndexed { index, question ->
        if (fits) Delegated.labels(parts[index], question).orEmpty() else emptyList()
    }
}

private enum class DelegateField(@StringRes val label: Int) {
    What(R.string.chat_delegate_field_what),
    WhyNow(R.string.chat_delegate_field_why_now),
    Recommend(R.string.chat_delegate_field_recommend),
    Cost(R.string.chat_delegate_field_cost),
    Reason(R.string.chat_delegate_field_reason),
    Deadline(R.string.chat_delegate_field_deadline),
}

private data class DelegateLine(val field: DelegateField, val text: String)

/** What the assistant is doing or found, inside the existing question card (iOS `DelegateNotes`, web `QuestionCard`). */
@Composable
internal fun DelegateNotes(interaction: Interaction, recommendationOnOptions: Boolean) {
    val context = interaction.delegate
    fun line(field: DelegateField, text: String?) = DelegateLine(field, text?.trim().orEmpty())
    val deadline = parseTime(context?.deadline)?.let {
        LocalizedFormats.format(it, LocalizedFormats.DATE_TIME, currentLocale)
    }
    val lines = listOf(
        line(DelegateField.What, context?.what),
        line(DelegateField.WhyNow, context?.whyNow),
        line(DelegateField.Recommend, context?.recommend),
        line(DelegateField.Cost, context?.cost),
        line(DelegateField.Reason, context?.reason),
        line(DelegateField.Deadline, deadline),
    ).filter { it.text.isNotEmpty() && !(it.field == DelegateField.Recommend && recommendationOnOptions) }
    if (!interaction.isDelegating && lines.isEmpty()) return

    Column(Modifier.fillMaxWidth().padding(bottom = Tokens.Space.s), verticalArrangement = Arrangement.spacedBy(Tokens.Space.s)) {
        if (interaction.isDelegating) DelegateWell {
            Row(Modifier.fillMaxWidth(), horizontalArrangement = Arrangement.spacedBy(Tokens.Space.s)) {
                JIcon(PersonaIcons.view, null, size = Tokens.Size.markS, tint = PersonaTheme.palette.ink2, decorative = true)
                Column(Modifier.weight(1f), verticalArrangement = Arrangement.spacedBy(Tokens.Space.xxs)) {
                    Text(stringResource(R.string.chat_delegate_looking), style = PersonaTheme.type.caption.copy(fontWeight = FontWeight.SemiBold),
                        color = PersonaTheme.palette.ink)
                    Text(stringResource(R.string.chat_delegate_looking_hint), style = PersonaTheme.type.caption, color = PersonaTheme.palette.ink2)
                }
            }
        }
        if (lines.isNotEmpty()) DelegateWell {
            Row(horizontalArrangement = Arrangement.spacedBy(Tokens.Space.s), verticalAlignment = Alignment.CenterVertically) {
                JIcon(PersonaIcons.ai, null, size = Tokens.Size.markS, tint = PersonaTheme.palette.ai, decorative = true)
                Text(stringResource(R.string.chat_delegate_context_title), style = PersonaTheme.type.caption.copy(fontWeight = FontWeight.SemiBold),
                    color = PersonaTheme.palette.ai)
            }
            lines.forEach { line ->
                if (LocalDensity.current.fontScale >= 1.5f) Column(verticalArrangement = Arrangement.spacedBy(Tokens.Space.xxs)) {
                    Text(stringResource(line.field.label), style = PersonaTheme.type.caption, color = PersonaTheme.palette.ink2)
                    Text(line.text, style = PersonaTheme.type.caption, color = PersonaTheme.palette.ink)
                } else Row(Modifier.fillMaxWidth(), horizontalArrangement = Arrangement.spacedBy(Tokens.Space.m)) {
                    Text(stringResource(line.field.label), Modifier.weight(1f), style = PersonaTheme.type.caption, color = PersonaTheme.palette.ink2)
                    Text(line.text, Modifier.weight(2f), style = PersonaTheme.type.caption, color = PersonaTheme.palette.ink)
                }
            }
        }
    }
}

@Composable
private fun DelegateWell(content: @Composable ColumnScope.() -> Unit) {
    Column(
        Modifier.fillMaxWidth().clip(RoundedCornerShape(Tokens.Radius.control))
            .background(PersonaTheme.palette.statusSurface).padding(Tokens.Space.m),
        verticalArrangement = Arrangement.spacedBy(Tokens.Space.xs),
        content = content,
    )
}
