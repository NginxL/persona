package persona.feature.assistant

import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.FlowRow
import androidx.compose.foundation.layout.PaddingValues
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.lazy.LazyRow
import androidx.compose.foundation.lazy.items
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.semantics.contentDescription
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.text.input.ImeAction
import androidx.compose.foundation.text.KeyboardActions
import androidx.compose.foundation.text.KeyboardOptions
import persona.core.contracts.Session
import persona.core.designsystem.JButton
import persona.core.designsystem.JButtonKind
import persona.core.designsystem.JChip
import persona.core.designsystem.JDialog
import persona.core.designsystem.JTextField
import persona.core.designsystem.PersonaTheme
import persona.core.designsystem.Tokens

/** Noninteractive labels on a conversation row; tapping the row still opens the chat. */
@Composable
internal fun ConversationTagLabels(session: Session, modifier: Modifier = Modifier) {
    val tags = session.tags.orEmpty()
    if (tags.isEmpty()) return
    FlowRow(
        modifier.testTag("conversation-row-tags-${session.key.path}"),
        horizontalArrangement = Arrangement.spacedBy(Tokens.Space.xs),
        verticalArrangement = Arrangement.spacedBy(Tokens.Space.xs),
    ) {
        tags.forEach { JChip(it) }
    }
}

/** One chip per tag in use; selecting the selected chip again clears the filter. */
@Composable
internal fun ConversationTagFilter(tags: List<String>, activeTag: String?, onSelect: (String) -> Unit) {
    val label = stringResource(R.string.assistant_tags_filter)
    LazyRow(
        Modifier.fillMaxWidth().testTag("conversation-tag-filter").semantics { contentDescription = label },
        contentPadding = PaddingValues(vertical = Tokens.Space.xs),
        horizontalArrangement = Arrangement.spacedBy(Tokens.Space.s),
    ) {
        items(tags, key = { it }) { tag ->
            JChip(tag, selected = activeTag == tag, onClick = { onSelect(tag) })
        }
    }
}

/** Add with Enter or Add, remove with a chip's ×, and save the normalized whole set. */
@Composable
internal fun ConversationTagsDialog(session: Session, busy: Boolean, onDismiss: () -> Unit, onSave: (List<String>) -> Unit) {
    val initial = remember(session.key.path) { normalizeConversationTags(session.tags.orEmpty()) }
    var tags by remember(session.key.path) { mutableStateOf(initial) }
    var draft by rememberSaveable(session.key.path) { mutableStateOf("") }
    var problem by remember(session.key.path) { mutableStateOf<ConversationTagProblem?>(null) }
    val unchanged = tags == initial
    fun add() {
        if (busy || draft.isBlank()) return
        val candidate = tags + draft
        problem = conversationTagsProblem(candidate)
        if (problem == null) {
            tags = normalizeConversationTags(candidate)
            draft = ""
        }
    }
    val hint = stringResource(R.string.assistant_tags_hint, CONVERSATION_TAGS_MAX, CONVERSATION_TAG_MAX_CODE_POINTS)
    val error = when (problem) {
        ConversationTagProblem.TooLong -> stringResource(R.string.assistant_tag_too_long, CONVERSATION_TAG_MAX_CODE_POINTS)
        ConversationTagProblem.TooMany -> stringResource(R.string.assistant_tag_count_limit, CONVERSATION_TAGS_MAX)
        null -> null
    }
    JDialog(
        onDismiss = { if (!busy) onDismiss() },
        title = stringResource(R.string.assistant_tags_title),
        confirmEnabled = !busy && !unchanged,
        confirm = stringResource(R.string.assistant_save) to { if (!busy && !unchanged) onSave(tags) },
        dismiss = stringResource(R.string.assistant_cancel) to { if (!busy) onDismiss() },
    ) {
        Row(verticalAlignment = Alignment.CenterVertically) {
            JTextField(
                draft, { draft = it; problem = null },
                Modifier.weight(1f).testTag("conversation-tag-draft"),
                placeholder = stringResource(R.string.assistant_tags_placeholder),
                singleLine = true, enabled = !busy, isError = problem != null, autofill = false,
                keyboardOptions = KeyboardOptions(imeAction = ImeAction.Done),
                keyboardActions = KeyboardActions(onDone = { add() }),
            )
            Spacer(Modifier.padding(Tokens.Space.xs))
            JButton(stringResource(R.string.assistant_tags_add), { add() }, enabled = !busy && draft.isNotBlank(), kind = JButtonKind.Quiet)
        }
        if (tags.isNotEmpty()) {
            Spacer(Modifier.height(Tokens.Space.s))
            FlowRow(
                horizontalArrangement = Arrangement.spacedBy(Tokens.Space.xs),
                verticalArrangement = Arrangement.spacedBy(Tokens.Space.xs),
            ) {
                tags.forEach { tag ->
                    JChip(tag, onRemove = if (busy) null else ({ tags = tags.filterNot { it == tag }; problem = null }),
                        removeLabel = stringResource(R.string.assistant_tags_remove, tag))
                }
            }
        }
        Spacer(Modifier.height(Tokens.Space.xs))
        Text(error ?: hint, style = PersonaTheme.type.caption,
            color = if (error == null) PersonaTheme.palette.ink3 else PersonaTheme.palette.danger.text)
    }
}
