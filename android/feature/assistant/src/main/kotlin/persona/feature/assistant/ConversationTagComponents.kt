package persona.feature.assistant

import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
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
import androidx.compose.runtime.rememberCoroutineScope
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
import kotlinx.coroutines.launch
import persona.core.contracts.Session
import persona.core.designsystem.JButton
import persona.core.designsystem.JButtonKind
import persona.core.designsystem.JButtonSize
import persona.core.designsystem.JChip
import persona.core.designsystem.JDialog
import persona.core.designsystem.JIconButton
import persona.core.designsystem.JTextField
import persona.core.designsystem.PersonaTheme
import persona.core.designsystem.Tokens
import persona.core.designsystem.icons.PersonaIcons

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

/**
 * Controlled tag-filter bar (#12018): the parent owns [entries], their order, selection, edit mode and persistence.
 * Only the in-progress draft and validation are local, so this composable can be moved to another pane independently.
 */
@Composable
internal fun ConversationTagFilterBar(
    entries: List<String>,
    activeTag: String?,
    customized: Boolean,
    editing: Boolean,
    busy: Boolean,
    onSelect: (String) -> Unit,
    onChange: suspend (List<String>?) -> Boolean,
    onEdit: () -> Unit,
    onDone: () -> Unit,
) {
    // A fresh install keeps the old list exactly: no empty strip or settings-only row before any tag exists.
    if (entries.isEmpty() && !customized && !editing) return
    var draft by rememberSaveable { mutableStateOf("") }
    var problem by remember { mutableStateOf<ConversationTagProblem?>(null) }
    var pending by remember { mutableStateOf(false) }
    val scope = rememberCoroutineScope()
    val changing = busy || pending
    val label = stringResource(R.string.assistant_tags_filter)
    fun change(next: List<String>?, onSuccess: () -> Unit = {}) {
        if (changing) return
        pending = true
        scope.launch {
            try {
                if (onChange(next)) {
                    problem = null
                    onSuccess()
                }
            } finally {
                pending = false
            }
        }
    }
    fun add() {
        val tag = draft.trim()
        if (tag.isEmpty() || changing) return
        if (tag in entries) {
            draft = ""
            problem = null
            return
        }
        val candidate = entries + tag
        problem = conversationTagFiltersProblem(candidate)
        if (problem == null) change(normalizeConversationTags(candidate)) { draft = "" }
    }
    Column(Modifier.fillMaxWidth().testTag("conversation-tag-filter").semantics { contentDescription = label }) {
        Row(verticalAlignment = Alignment.CenterVertically) {
            if (entries.isNotEmpty()) {
                if (editing) FlowRow(
                    Modifier.weight(1f),
                    horizontalArrangement = Arrangement.spacedBy(Tokens.Space.s),
                    verticalArrangement = Arrangement.spacedBy(Tokens.Space.xs),
                ) {
                    entries.forEach { tag ->
                        JChip(tag, selected = activeTag == tag,
                            onClick = if (changing) null else ({ onSelect(tag) }),
                            onRemove = if (!changing) ({ change(entries.filterNot { it == tag }) }) else null,
                            removeLabel = stringResource(R.string.assistant_tags_remove, tag))
                    }
                } else LazyRow(
                    Modifier.weight(1f),
                    contentPadding = PaddingValues(vertical = Tokens.Space.xs),
                    horizontalArrangement = Arrangement.spacedBy(Tokens.Space.s),
                ) {
                    items(entries, key = { it }) { tag ->
                        JChip(tag, selected = activeTag == tag,
                            onClick = if (changing) null else ({ onSelect(tag) }))
                    }
                }
            } else {
                if (customized) Text(stringResource(R.string.assistant_tag_filters_empty),
                    Modifier.weight(1f).padding(horizontal = Tokens.Space.xs),
                    style = PersonaTheme.type.caption, color = PersonaTheme.palette.ink3)
                else Spacer(Modifier.weight(1f))
            }
            if (!editing) JIconButton(PersonaIcons.options, stringResource(R.string.assistant_tag_filters_customize),
                onEdit, enabled = !changing)
        }
        if (editing) {
            Row(verticalAlignment = Alignment.CenterVertically) {
                JTextField(
                    draft, { draft = it; problem = null },
                    Modifier.weight(1f).testTag("conversation-tag-filter-draft"),
                    placeholder = stringResource(R.string.assistant_tag_filters_placeholder),
                    singleLine = true, enabled = !changing, isError = problem != null, autofill = false,
                    keyboardOptions = KeyboardOptions(imeAction = ImeAction.Done),
                    keyboardActions = KeyboardActions(onDone = { add() }),
                )
                Spacer(Modifier.padding(Tokens.Space.xs))
                JButton(stringResource(R.string.assistant_tags_add), { add() },
                    enabled = !changing && draft.isNotBlank(), kind = JButtonKind.Quiet, size = JButtonSize.Small)
            }
            FlowRow(
                horizontalArrangement = Arrangement.spacedBy(Tokens.Space.xs),
                verticalArrangement = Arrangement.spacedBy(Tokens.Space.xs),
            ) {
                if (customized) JButton(stringResource(R.string.assistant_tag_filters_reset), { change(null) },
                    enabled = !changing, kind = JButtonKind.Quiet, size = JButtonSize.Small)
                JButton(stringResource(R.string.assistant_tag_filters_done), {
                    draft = ""
                    problem = null
                    onDone()
                }, enabled = !changing, size = JButtonSize.Small)
            }
            when (problem) {
                ConversationTagProblem.TooLong -> Text(stringResource(R.string.assistant_tag_too_long,
                    CONVERSATION_TAG_MAX_CODE_POINTS), style = PersonaTheme.type.caption, color = PersonaTheme.palette.danger.text)
                ConversationTagProblem.TooMany -> Text(stringResource(R.string.assistant_tag_filters_count_limit,
                    CONVERSATION_TAG_FILTERS_MAX), style = PersonaTheme.type.caption, color = PersonaTheme.palette.danger.text)
                null -> Unit
            }
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
