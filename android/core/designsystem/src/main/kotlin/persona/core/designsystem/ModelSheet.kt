package persona.core.designsystem

import androidx.compose.ui.res.stringResource
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.itemsIndexed
import androidx.compose.foundation.lazy.rememberLazyListState
import androidx.compose.material3.Text
import androidx.compose.runtime.*
import androidx.compose.ui.Modifier
import persona.core.contracts.api.AgentModel
import persona.core.contracts.api.modelName
import persona.core.designsystem.icons.PhIcons

/**
 * A launcher's /model list to pick from (#794, #943): "Default" first, then every model the agent offers, searchable by name,
 * id, the model an alias stands for and description. Picking one closes it; "Refresh" asks the CLI again. [custom] also takes
 * an id the list does not have (model configuration): the one in use stays shown, and what is typed can be used as is.
 */
@Composable
fun JModelSheet(
    note: String,
    models: List<AgentModel>,
    selected: String,
    defaultDetail: String?,
    loading: Boolean,
    error: String?,
    onPick: (String) -> Unit,
    onRefresh: () -> Unit,
    onDismiss: () -> Unit,
    initialQuery: String = "",
    custom: Boolean = false,
    /** The launcher's display name, repeated on every model row when this sheet is a start's picker. */
    owner: String? = null,
) {
    var query by remember { mutableStateOf(initialQuery) }
    val shown = remember(models, query) { models.filter { !it.isDefault && it.matches(query) } }
    val unlisted = selected.takeIf { custom && it.isNotEmpty() && models.none { m -> m.id == it } }
    val typed = if (custom) customModelId(models, query) else null
    // Results start from the top: the list would otherwise keep the row it was at, and "Default" coming back in front of it
    // once the query is cleared would stay above the viewport.
    val scroll = rememberLazyListState()
    LaunchedEffect(query) { scroll.scrollToItem(0) }
    JSheet(onDismiss = onDismiss, title = stringResource(R.string.designsystem_model_title)) {
        Text(note, style = PersonaTheme.type.callout, color = PersonaTheme.palette.ink2)
        Spacer(Modifier.height(Tokens.Space.m))
        JSearchField(query, { query = it }, stringResource(if (custom) R.string.designsystem_model_search_custom else R.string.designsystem_model_search))
        Spacer(Modifier.height(Tokens.Space.m))
        LazyColumn(Modifier.fillMaxWidth().weight(1f), scroll, contentPadding = PaddingValues(bottom = Tokens.Space.l)) {
            error?.let { item("error") { JInlineStatus(stringResource(R.string.designsystem_model_refresh_failed, it), tone = InlineTone.Failed) } }
            if (query.isBlank()) item("default") {
                val rows = if (unlisted != null) 2 else 1
                Column {
                    JGroupItem(GroupPosition.of(0, rows)) {
                        ModelChoice(stringResource(R.string.designsystem_model_default), defaultDetail, null, selected.isEmpty(), owner) { onPick(""); onDismiss() }
                    }
                    if (unlisted != null) JGroupItem(GroupPosition.of(1, rows)) {
                        ModelChoice(modelName(unlisted), stringResource(R.string.designsystem_model_unlisted), null, true, owner) { onDismiss() }
                    }
                }
            }
            item("all") {
                JSectionHeader(stringResource(if (query.isBlank()) R.string.designsystem_model_all else R.string.designsystem_model_results), count = shown.size, action = {
                    if (loading) JSpinner() else JTextButton(stringResource(R.string.designsystem_model_refresh), onRefresh, icon = PhIcons.ArrowsClockwise)
                })
            }
            if (shown.isEmpty() && typed == null) item("none") {
                JEmptyState(stringResource(R.string.designsystem_model_none), stringResource(R.string.designsystem_model_none_detail), icon = PhIcons.MagnifyingGlass)
            }
            itemsIndexed(shown, key = { _, model -> model.id }) { index, model ->
                JGroupItem(GroupPosition.of(index, shown.size)) {
                    ModelChoice(model.label, model.detailLine(), model.description, model.id == selected, owner) { onPick(model.id); onDismiss() }
                }
            }
            typed?.let { id ->
                item("custom") {
                    JGroupItem(GroupPosition.Single, Modifier.padding(top = Tokens.Space.m)) {
                        ModelChoice(stringResource(R.string.designsystem_model_use, id), stringResource(R.string.designsystem_model_use_detail), null, id == selected, owner) { onPick(id); onDismiss() }
                    }
                }
            }
        }
    }
}

@Composable
private fun ModelChoice(title: String, detail: String?, description: String?, selected: Boolean, owner: String? = null, onClick: () -> Unit) {
    JRow(title, subtitle = listOfNotNull(launcherModelRowDetail(owner, detail), description?.takeIf { it.isNotBlank() }).joinToString("\n").ifEmpty { null },
        subtitleLines = 3, chevron = false, onClick = onClick,
        trailing = if (selected) ({ JIcon(PhIcons.Check, stringResource(R.string.designsystem_selected), tint = PersonaTheme.palette.ink) }) else null)
}

/** A model row keeps its launcher's name beside the model detail, never instead of it. */
internal fun launcherModelRowDetail(owner: String?, detail: String?): String? =
    listOfNotNull(owner?.trim()?.takeIf(String::isNotEmpty), detail?.trim()?.takeIf(String::isNotEmpty)).joinToString(" · ").ifEmpty { null }

/** A model's second line: its id when the name is another word for it, and the model an alias stands for. */
fun AgentModel.detailLine(): String? {
    val rawLabel = name.ifBlank { id }
    val id = id.takeUnless { it.equals(rawLabel, ignoreCase = true) }
    val resolved = resolved?.takeIf { it.isNotBlank() && !it.equals(rawLabel, true) && it != this.id }
    return when {
        id != null && resolved != null -> "$id → $resolved"
        else -> id ?: resolved
    }
}

/** Whether [query] is in the model's name, friendly label, id, resolved model or description. */
fun AgentModel.matches(query: String): Boolean {
    val q = query.trim()
    return q.isEmpty() || listOf(name, label, id, resolved.orEmpty(), description.orEmpty()).any { it.contains(q, ignoreCase = true) }
}

/** What is typed in the search, as an id to use when no model has exactly that id; null when it is no id (blank, words). */
fun customModelId(models: List<AgentModel>, query: String): String? {
    val id = query.trim()
    return id.takeIf { it.isNotEmpty() && it.none(Char::isWhitespace) && models.none { m -> m.id == it } }
}
