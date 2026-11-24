package persona.feature.assistant

import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateMapOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalResources
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.res.stringResource
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.launch
import persona.core.contracts.api.AssistantManagement
import persona.core.contracts.api.SpaceMd
import persona.core.contracts.api.SpaceMdError
import persona.core.contracts.api.SpaceMdFields
import persona.core.contracts.api.SpaceMdPart
import persona.core.contracts.api.SpaceMdText
import persona.core.contracts.api.Tracks
import persona.core.contracts.api.hubCodedFailure
import persona.core.designsystem.JButton
import persona.core.designsystem.JButtonKind
import persona.core.designsystem.JButtonSize
import persona.core.designsystem.JCard
import persona.core.designsystem.JIcon
import persona.core.designsystem.JNotice
import persona.core.designsystem.JSheet
import persona.core.designsystem.JTextField
import persona.core.designsystem.LocalAppFeedback
import persona.core.designsystem.PersonaTheme
import persona.core.designsystem.Status
import persona.core.designsystem.Tokens
import persona.core.designsystem.icons.PersonaIcons
import persona.core.designsystem.operationFeedback

/**
 * The SPACE.md editor (#13500, the web's `SpaceMdEditor.tsx`, [SpaceMdText]): the parts the user wrote are fields in the
 * body font; each block Persona keeps up to date is shown read-only and goes back byte for byte. A save sends the digest
 * the edit started from: changed elsewhere meanwhile (409) keeps what was typed and offers Load latest; a kept block
 * changed (400, also checked before sending) or an offline computer (503) say so in plain words.
 */
@Composable
internal fun SpaceMdEditor(slug: String, api: AssistantManagement, onDismiss: () -> Unit, onSaved: (SpaceMd) -> Unit) {
    val resources = LocalResources.current
    val feedback = LocalAppFeedback.current
    val scope = rememberCoroutineScope()
    var base by remember(slug) { mutableStateOf<SpaceMd?>(null) }
    var fields by remember(slug) { mutableStateOf<SpaceMdFields?>(null) }
    val edits = remember(slug) { mutableStateMapOf<Int, String>() }
    var error by remember(slug) { mutableStateOf<SpaceMdError?>(null) }
    var failure by remember(slug) { mutableStateOf<String?>(null) }
    var loadFailed by remember(slug) { mutableStateOf(false) }
    var saving by remember(slug) { mutableStateOf(false) }

    suspend fun load() {
        try {
            val read = api.spaceMd(slug)
            val shown = SpaceMdText.editable(SpaceMdText.split(read.content))
            // Only fields the user changes go into [edits]: an untouched field goes back exactly as it was read.
            edits.clear()
            base = read
            fields = shown
            error = null
            failure = null
            loadFailed = false
        } catch (cancelled: CancellationException) {
            throw cancelled
        } catch (failed: Exception) {
            val kind = Tracks.spaceMdError(hubCodedFailure(failed)?.status)
            if (base == null) loadFailed = true
            error = kind.takeIf { it != SpaceMdError.Other }
            failure = if (kind == SpaceMdError.Other)
                resources.getString(R.string.assistant_spaces_md_read_failed, operationFeedback(failed).text.resolve(resources)) else null
        }
    }
    LaunchedEffect(slug, api) { load() }
    val reload: () -> Unit = { scope.launch { load() } }
    val changed = fields?.let { shown -> edits.isNotEmpty() && SpaceMdText.assemble(shown.parts, edits.toMap()) != base?.content } ?: false

    fun save() {
        val shown = fields ?: return
        val from = base ?: return
        val content = SpaceMdText.assemble(shown.parts, edits.toMap())
        // What the hub would refuse (a marker typed into a field) is said before anything is sent.
        if (!SpaceMdText.sameManagedBlocks(from.content, content)) { error = SpaceMdError.Managed; failure = null; return }
        saving = true
        scope.launch {
            try {
                val saved = api.writeSpaceMd(slug, content, from.digest)
                feedback?.notice(resources.getString(R.string.assistant_spaces_md_saved))
                onSaved(saved)
                onDismiss()
            } catch (cancelled: CancellationException) {
                throw cancelled
            } catch (failed: Exception) {
                val kind = Tracks.spaceMdError(hubCodedFailure(failed)?.status)
                error = kind.takeIf { it != SpaceMdError.Other }
                failure = if (kind == SpaceMdError.Other)
                    resources.getString(R.string.assistant_spaces_md_failed, operationFeedback(failed).text.resolve(resources)) else null
            } finally {
                saving = false
            }
        }
    }

    JSheet(onDismiss, title = stringResource(R.string.assistant_spaces_md_title), scrollable = true, dismissible = !saving) {
        val palette = PersonaTheme.palette
        Column(Modifier.fillMaxWidth().testTag("space-md-editor"), verticalArrangement = Arrangement.spacedBy(Tokens.Space.m)) {
            Text(stringResource(R.string.assistant_spaces_md_intro), style = PersonaTheme.type.callout, color = palette.ink2)
            val message = when (error) {
                SpaceMdError.Stale -> stringResource(R.string.assistant_spaces_md_stale)
                SpaceMdError.Managed -> stringResource(R.string.assistant_spaces_md_managed)
                SpaceMdError.Missing -> stringResource(R.string.assistant_spaces_md_missing)
                SpaceMdError.Offline -> stringResource(R.string.assistant_spaces_md_offline)
                SpaceMdError.Other, null -> failure
            }
            if (message != null) JNotice(message, Modifier.testTag("space-md-error"), status = Status.NeedsYou,
                actionLabel = stringResource(R.string.assistant_spaces_md_reload).takeIf { error == SpaceMdError.Stale },
                onAction = reload.takeIf { error == SpaceMdError.Stale })
            val shown = fields
            if (shown == null) {
                if (!loadFailed) Text(stringResource(R.string.assistant_spaces_md_loading), style = PersonaTheme.type.callout, color = palette.ink3)
                else {
                    if (message == null) Text(stringResource(R.string.assistant_spaces_md_unavailable), style = PersonaTheme.type.callout, color = palette.ink3)
                    JButton(stringResource(R.string.assistant_spaces_retry), reload, Modifier.testTag("space-md-retry"),
                        kind = JButtonKind.Quiet, size = JButtonSize.Medium, icon = PersonaIcons.retry)
                }
            } else shown.parts.forEachIndexed { index, part ->
                when {
                    index in shown.editable -> JTextField(edits[index] ?: part.text, { edits[index] = it },
                        Modifier.fillMaxWidth().testTag("space-md-field-$index"),
                        placeholder = stringResource(R.string.assistant_spaces_md_placeholder),
                        minLines = if (index == shown.editable.first()) 10 else 5, maxLines = Int.MAX_VALUE,
                        enabled = !saving, readingContent = true)
                    part is SpaceMdPart.Managed -> ManagedBlock(part)
                }
            }
            Row(Modifier.fillMaxWidth().padding(top = Tokens.Space.s), horizontalArrangement = Arrangement.spacedBy(Tokens.Space.s, Alignment.End)) {
                JButton(stringResource(R.string.assistant_spaces_cancel), onDismiss, kind = JButtonKind.Quiet, size = JButtonSize.Medium, enabled = !saving)
                JButton(stringResource(R.string.assistant_spaces_save), ::save, Modifier.testTag("space-md-save"),
                    kind = JButtonKind.Primary, size = JButtonSize.Medium, loading = saving, enabled = changed && !saving)
            }
        }
    }
}

/** One block Persona keeps up to date: read-only, its text as it is. */
@Composable
private fun ManagedBlock(part: SpaceMdPart.Managed) {
    val palette = PersonaTheme.palette
    JCard(Modifier.testTag("space-md-managed-${part.name}")) {
        Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(Tokens.Space.xs)) {
            JIcon(PersonaIcons.locked, null, size = Tokens.Size.markS, tint = palette.ink3, decorative = true)
            Text(stringResource(R.string.assistant_spaces_md_managed_block), style = PersonaTheme.type.caption, color = palette.ink3)
        }
        Text(part.body.trim().ifEmpty { stringResource(R.string.assistant_spaces_md_managed_empty) },
            Modifier.padding(top = Tokens.Space.xs), style = PersonaTheme.type.callout, color = palette.ink2)
    }
}
