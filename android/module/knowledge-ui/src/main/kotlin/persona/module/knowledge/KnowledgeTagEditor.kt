package persona.module.knowledge

import androidx.compose.foundation.horizontalScroll
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.rememberScrollState
import androidx.compose.material3.Text
import androidx.compose.runtime.*
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.ui.Modifier
import androidx.compose.ui.res.stringResource
import persona.core.contracts.api.KnowledgeEntry
import persona.core.contracts.api.KnowledgeUpdate
import persona.core.designsystem.*
import kotlin.math.roundToInt

/**
 * "Tag" (iOS `KBTagEditor`): importance, relevance, tags and, for a project, its kind and status, set by hand. The hub
 * keeps them over its automatic tagging. Saving closes the sheet once the hub has them; a failure stays here.
 */
@Composable
internal fun KnowledgeTagEditor(entry: KnowledgeEntry, tagging: KnowledgeTagging, onSave: (KnowledgeUpdate) -> Unit, onDismiss: () -> Unit) {
    val start = remember(entry.key) { KnowledgeTagForm.of(entry) }
    var importance by rememberSaveable(entry.key) { mutableStateOf(start.importance) }
    var relevance by rememberSaveable(entry.key) { mutableIntStateOf(start.relevance) }
    var tags by rememberSaveable(entry.key) { mutableStateOf(start.tags) }
    var projectKind by rememberSaveable(entry.key) { mutableStateOf(start.projectKind) }
    var status by rememberSaveable(entry.key) { mutableStateOf(start.status) }
    val palette = PersonaTheme.palette
    JSheet(onDismiss = onDismiss, title = stringResource(R.string.knowledge_ui_tag_title, entry.name), dismissible = !tagging.busy, scrollable = true) {
        Column(Modifier.fillMaxWidth().imePadding()) {
            Label(stringResource(R.string.knowledge_ui_importance))
            Choices(KnowledgeTagForm.IMPORTANCE.map { it to it }, importance, enabled = !tagging.busy, status = ::importanceStatus) { importance = it }
            Label(stringResource(R.string.knowledge_ui_relevance_value, relevance))
            JSlider(relevance.toFloat(), { relevance = it.roundToInt() }, valueRange = 0f..100f, step = 5f,
                enabled = !tagging.busy, contentDescription = stringResource(R.string.knowledge_ui_relevance_label))
            Label(stringResource(R.string.knowledge_ui_tags))
            JTextField(tags, { tags = it }, placeholder = stringResource(R.string.knowledge_ui_tags_placeholder), singleLine = true, enabled = !tagging.busy)
            if (entry.isProject) {
                Label(stringResource(R.string.knowledge_ui_type))
                Choices(KnowledgeTagForm.PROJECT_KINDS.map { (key, label) -> key to stringResource(label) }, projectKind, enabled = !tagging.busy) { projectKind = it }
                Label(stringResource(R.string.knowledge_ui_status))
                Choices(KnowledgeTagForm.PROJECT_STATUSES.map { (key, label) -> key to stringResource(label) }, status, enabled = !tagging.busy) { status = it }
            }
            Spacer(Modifier.height(Tokens.Space.m))
            Text(stringResource(R.string.knowledge_ui_tag_locked), style = PersonaTheme.type.caption, color = palette.ink3)
            tagging.error?.let {
                Spacer(Modifier.height(Tokens.Space.m))
                JNotice(stringResource(R.string.knowledge_ui_not_saved, it.text()), status = Status.NeedsYou)
            }
            Spacer(Modifier.height(Tokens.Space.section))
            JButton(stringResource(R.string.knowledge_ui_save), {
                onSave(KnowledgeTagForm(importance, relevance, tags, projectKind, status).update(entry.isProject))
            }, Modifier.fillMaxWidth(), loading = tagging.busy, loadingText = stringResource(R.string.knowledge_ui_saving))
        }
    }
}

@Composable
private fun Label(text: String) {
    JSectionHeader(text)
}

/** A few choices all in sight (importance, a project's kind and status); labels stay on one line and scroll if need be. */
@Composable
private fun Choices(
    options: List<Pair<String, String>>,
    value: String,
    enabled: Boolean,
    status: (String) -> Status? = { null },
    onPick: (String) -> Unit,
) {
    Row(Modifier.fillMaxWidth().horizontalScroll(rememberScrollState()), horizontalArrangement = Arrangement.spacedBy(Tokens.Space.s)) {
        options.forEach { (key, label) ->
            // Busy chips keep their click (ignored) so they keep their height while saving.
            JChip(label, selected = key == value, onClick = { if (enabled) onPick(key) }, status = status(key))
        }
    }
}
