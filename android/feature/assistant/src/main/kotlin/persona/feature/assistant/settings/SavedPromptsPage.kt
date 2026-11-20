package persona.feature.assistant.settings

import androidx.activity.compose.BackHandler
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.text.KeyboardOptions
import androidx.compose.foundation.lazy.itemsIndexed
import androidx.compose.material3.Text
import androidx.compose.runtime.*
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.res.stringResource
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import kotlinx.coroutines.launch
import persona.core.contracts.api.SavedPrompt
import persona.core.designsystem.*
import persona.core.designsystem.icons.PersonaIcons
import persona.feature.assistant.R

@Composable
internal fun SavedPromptsPage(store: SavedPromptsStore, onBack: () -> Unit) {
    val state by store.state.collectAsStateWithLifecycle()
    val scope = rememberCoroutineScope()
    var editorId by rememberSaveable { mutableStateOf<String?>(null) }
    var editorName by rememberSaveable { mutableStateOf("") }
    var editorText by rememberSaveable { mutableStateOf("") }
    var saving by remember { mutableStateOf(false) }
    var deleting by remember { mutableStateOf<SavedPrompt?>(null) }
    LaunchedEffect(store) {
        val current = store.state.value
        if (!current.refreshing && (current.available != null || current.loadFailure != null)) store.refresh()
    }
    fun open(prompt: SavedPrompt?) {
        store.clearWriteFailure()
        editorName = prompt?.name.orEmpty()
        editorText = prompt?.text.orEmpty()
        editorId = prompt?.id.orEmpty()
    }
    fun closeEditor() {
        if (store.state.value.writing) return
        store.clearWriteFailure()
        editorId = null
        editorName = ""
        editorText = ""
    }

    val id = editorId
    if (id != null) {
        val cancel = { if (!saving) closeEditor() }
        BackHandler(onBack = cancel)
        SavedPromptEditor(
            creating = id.isEmpty(), name = editorName, text = editorText, state = state.copy(writing = state.writing || saving),
            nameTaken = SavedPromptInput.nameTaken(SavedPromptInput.name(editorName), id, state.prompts),
            onBack = cancel,
            onName = { editorName = it; store.clearWriteFailure() },
            onText = { editorText = it; store.clearWriteFailure() },
            onSave = { name, text ->
                if (!saving) {
                    saving = true
                    scope.launch {
                        try {
                            if (store.save(id.takeIf(String::isNotEmpty), name, text)) closeEditor()
                        } finally {
                            saving = false
                        }
                    }
                }
            },
        )
        return
    }

    val prompts = state.prompts
    val canAdd = state.available == true
    val addLabel = stringResource(R.string.assistant_saved_prompts_new)
    val refresh: () -> Unit = { scope.launch { store.refresh() } }
    JListPage(
        title = stringResource(R.string.assistant_saved_prompts_title),
        onBack = onBack,
        titleBusy = state.writing,
        refreshing = state.refreshing && state.available != null,
        onRefresh = refresh,
        actions = {
            if (canAdd && prompts.isNotEmpty()) JIconButton(PersonaIcons.add, addLabel, { open(null) }, enabled = !state.writing)
        },
    ) {
        state.writeFailure?.let { error -> item("write-error") {
            JNotice(stringResource(R.string.assistant_saved_prompts_write_failed, failureReasonText(error).text()), status = Status.NeedsYou)
        } }
        when {
            state.loadFailure != null && prompts.isEmpty() -> item("read-error") {
                JEmptyState(stringResource(R.string.assistant_saved_prompts_load_failed), failureReasonText(requireNotNull(state.loadFailure)).text(),
                    icon = PersonaIcons.error,
                    action = { JButton(stringResource(R.string.assistant_settings_retry), refresh) })
            }
            state.available == null -> item("loading") { JLoading(text = stringResource(R.string.assistant_settings_loading)) }
            state.available == false -> item("unavailable") {
                JEmptyState(stringResource(R.string.assistant_saved_prompts_unavailable), stringResource(R.string.assistant_saved_prompts_unavailable_detail),
                    icon = PersonaIcons.document)
            }
            prompts.isEmpty() && !state.writing -> item("empty") {
                JEmptyState(stringResource(R.string.assistant_saved_prompts_empty), stringResource(R.string.assistant_saved_prompts_use),
                    icon = PersonaIcons.document, action = { JButton(addLabel, { open(null) }) })
            }
        }
        if (prompts.isNotEmpty()) {
            state.loadFailure?.let { error -> item("read-notice") {
                JNotice(failureReasonText(error).text(), status = Status.NeedsYou,
                    actionLabel = stringResource(R.string.assistant_settings_retry), onAction = refresh)
            } }
            itemsIndexed(prompts, key = { _, prompt -> prompt.id }) { index, prompt ->
                JGroupItem(GroupPosition.of(index, prompts.size)) {
                    SavedPromptRow(prompt, enabled = !state.writing, onEdit = { open(prompt) },
                        moveUp = if (index > 0) ({ scope.launch { store.move(prompt.id, -1) } }) else null,
                        moveDown = if (index < prompts.lastIndex) ({ scope.launch { store.move(prompt.id, 1) } }) else null,
                        onDelete = { deleting = prompt })
                }
            }
            item("footer") {
                Text(stringResource(R.string.assistant_saved_prompts_use), style = PersonaTheme.type.caption, color = PersonaTheme.palette.ink2)
            }
        }
    }
    deleting?.let { prompt ->
        JDialog(
            onDismiss = { deleting = null },
            title = stringResource(R.string.assistant_saved_prompts_delete_title, prompt.name),
            text = stringResource(R.string.assistant_saved_prompts_delete_detail),
            destructive = true,
            confirmEnabled = !state.writing,
            confirm = stringResource(R.string.assistant_settings_delete) to {
                deleting = null
                scope.launch { store.delete(prompt.id) }
            },
            dismiss = stringResource(R.string.assistant_settings_cancel) to { deleting = null },
        )
    }
}

@Composable
private fun SavedPromptRow(
    prompt: SavedPrompt, enabled: Boolean, onEdit: () -> Unit, moveUp: (() -> Unit)?, moveDown: (() -> Unit)?, onDelete: () -> Unit,
) {
    var menu by remember { mutableStateOf(false) }
    JRow(
        "/${prompt.name}", subtitle = prompt.text.lineSequence().take(2).joinToString("\n"), enabled = enabled,
        modifier = Modifier.testTag("saved-prompt-${prompt.id}"), onClick = onEdit,
        trailing = {
            Box {
                JIconButton(PersonaIcons.more, stringResource(R.string.assistant_saved_prompts_actions, prompt.name), { menu = true },
                    enabled = enabled)
                JMenu(menu && enabled, { menu = false }, buildList {
                    add(JAction(stringResource(R.string.assistant_saved_prompts_edit), PersonaIcons.edit, onClick = onEdit))
                    moveUp?.let { add(JAction(stringResource(R.string.assistant_saved_prompts_up), PersonaIcons.queueUp, onClick = it)) }
                    moveDown?.let { add(JAction(stringResource(R.string.assistant_saved_prompts_down), PersonaIcons.down, onClick = it)) }
                    add(JAction(stringResource(R.string.assistant_settings_delete), PersonaIcons.delete, destructive = true, onClick = onDelete))
                })
            }
        },
    )
}

@Composable
private fun SavedPromptEditor(
    creating: Boolean, name: String, text: String, state: SavedPromptsState, nameTaken: Boolean,
    onBack: () -> Unit, onName: (String) -> Unit, onText: (String) -> Unit, onSave: (String, String) -> Unit,
) {
    var tried by rememberSaveable { mutableStateOf(false) }
    val cleanName = SavedPromptInput.name(name)
    val cleanText = SavedPromptInput.text(text)
    val nameInvalid = !SavedPromptInput.validName(cleanName)
    val textInvalid = !SavedPromptInput.validText(cleanText)
    val showNameError = nameTaken || (tried || name.isNotEmpty()) && nameInvalid
    val showTextError = (tried || text.isNotEmpty()) && textInvalid
    val textCount = SavedPromptInput.length(cleanText)
    val editable = !state.writing
    JListPage(
        title = stringResource(if (creating) R.string.assistant_saved_prompts_new else R.string.assistant_saved_prompts_edit),
        onBack = onBack, titleBusy = state.writing,
        actions = {
            JIconButton(PersonaIcons.done, stringResource(R.string.assistant_settings_save), {
                tried = true
                if (!nameInvalid && !nameTaken && !textInvalid) onSave(cleanName, cleanText)
            }, enabled = editable && state.available == true)
        },
    ) {
        item("name") {
            JTextField(name, onName, Modifier.fillMaxWidth().testTag("saved-prompt-name"), singleLine = true, enabled = editable,
                label = stringResource(R.string.assistant_saved_prompts_name), monospace = true, autofill = false,
                keyboardOptions = KeyboardOptions(autoCorrectEnabled = false),
                supportingText = stringResource(when {
                    nameTaken -> R.string.assistant_saved_prompts_name_taken
                    showNameError -> R.string.assistant_saved_prompts_name_rule
                    else -> R.string.assistant_saved_prompts_name_hint
                }), isError = showNameError)
        }
        item("text") {
            JTextField(text, onText, Modifier.fillMaxWidth().testTag("saved-prompt-text"), enabled = editable, minLines = 6,
                label = stringResource(R.string.assistant_saved_prompts_text), autofill = false, isError = showTextError,
                supportingText = when {
                    showTextError -> stringResource(R.string.assistant_saved_prompts_text_rule)
                    textCount * 5 > SavedPromptInput.TEXT_MAX * 4 -> stringResource(R.string.assistant_saved_prompts_text_count, textCount)
                    else -> null
                })
        }
        state.writeFailure?.let { error -> item("save-error") {
            JNotice(stringResource(R.string.assistant_saved_prompts_save_failed, failureReasonText(error).text()), status = Status.NeedsYou)
        } }
        if (state.available == false) item("unavailable") {
            JNotice(stringResource(R.string.assistant_saved_prompts_unavailable_detail), status = Status.Offline)
        }
    }
}
