package persona.feature.chat

import androidx.compose.foundation.ScrollState
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.PaddingValues
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.text.selection.SelectionContainer
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableIntStateOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.produceState
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.res.pluralStringResource
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.text.style.TextOverflow
import androidx.lifecycle.Lifecycle
import androidx.lifecycle.compose.LocalLifecycleOwner
import androidx.lifecycle.repeatOnLifecycle
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.delay
import persona.core.contracts.Session
import persona.core.contracts.SessionKey
import persona.core.contracts.api.SessionFiles
import persona.core.contracts.api.SessionLogTail
import persona.core.contracts.parseTime
import persona.core.designsystem.JCard
import persona.core.designsystem.JIconButton
import persona.core.designsystem.JLoading
import persona.core.designsystem.JNotice
import persona.core.designsystem.JTextButton
import persona.core.designsystem.LocalizedFormats
import persona.core.designsystem.PersonaTheme
import persona.core.designsystem.Tokens
import persona.core.designsystem.icons.PersonaIcons
import persona.core.designsystem.rememberCopy
import persona.core.designsystem.text
import persona.core.domain.sort.WorkflowTimeline
import java.time.Instant
import java.time.ZoneId

private const val BackgroundOutputPoll = 2_000L

/** Perform at most one read at a time. Cancellation also cancels an in-flight tail read. */
internal suspend fun readBackgroundOutput(
    running: Boolean,
    read: suspend () -> SessionLogTail,
    onResult: (Result<SessionLogTail>) -> Unit,
) {
    do {
        onResult(try { Result.success(read()) }
        catch (cancelled: CancellationException) { throw cancelled }
        catch (failure: Exception) { Result.failure(failure) })
        if (running) delay(BackgroundOutputPoll)
    } while (running)
}

private data class LogTarget(val session: SessionKey, val manager: String?, val folder: String, val task: String, val path: String)
private data class LogOutput(val tail: SessionLogTail? = null, val failed: Boolean = false)

@Composable
internal fun backgroundStatus(outcome: BackgroundOutcome): String = stringResource(when (outcome) {
    BackgroundOutcome.Running -> R.string.chat_background_running
    BackgroundOutcome.Completed -> R.string.chat_background_completed
    BackgroundOutcome.Failed -> R.string.chat_background_failed
    BackgroundOutcome.Stopped -> R.string.chat_background_stopped
    BackgroundOutcome.Unknown -> R.string.chat_background_unknown
})

@Composable
internal fun backgroundDuration(seconds: Long): String = WorkflowTimeline.took(seconds).text()

@Composable
internal fun BackgroundCommandDetail(
    session: Session,
    command: BackgroundCommand,
    files: SessionFiles?,
    onOpenFile: ((String) -> Unit)?,
    padding: PaddingValues,
    pageScroll: ScrollState,
) {
    val task = command.task
    val palette = PersonaTheme.palette
    val copy = rememberCopy()
    val copied = stringResource(R.string.chat_background_command_copied)
    val copyLabel = stringResource(R.string.chat_background_copy_command)
    val lifecycle = LocalLifecycleOwner.current.lifecycle
    val folder = session.cwd?.takeIf(String::isNotBlank)
    val path = task.outputFile?.takeIf(String::isNotBlank)
    val scope = if (path != null && folder != null) LogTarget(session.key, session.manager, folder, task.id, path) else null
    var output by remember(scope, files) { mutableStateOf(LogOutput()) }
    var retry by remember(scope, files) { mutableIntStateOf(0) }
    val logScroll = rememberScrollState()
    var follow by remember(scope) { mutableStateOf(true) }
    LaunchedEffect(scope, files, command.running, retry, lifecycle) {
        if (scope == null || files == null) return@LaunchedEffect
        lifecycle.repeatOnLifecycle(Lifecycle.State.STARTED) {
            // Toggling Running off cancels its pending poll and takes one final snapshot.
            readBackgroundOutput(command.running, read = {
                files.readLogTail(scope.session.machineId, scope.folder, scope.path, scope.manager)
            }, onResult = { result ->
                result.onSuccess {
                    follow = output.tail == null || !logScroll.canScrollForward
                    output = LogOutput(tail = it)
                }.onFailure { output = output.copy(failed = true) }
            })
        }
    }
    LaunchedEffect(logScroll.maxValue, output.tail, follow) {
        if (follow && output.tail != null && logScroll.maxValue != Int.MAX_VALUE)
            logScroll.scrollTo(logScroll.maxValue)
    }
    val now by produceState(Instant.now(), command.running) {
        while (command.running) { delay(1_000); value = Instant.now() }
    }
    val res = androidx.compose.ui.platform.LocalResources.current
    Column(Modifier.fillMaxSize().verticalScroll(pageScroll).padding(padding),
        verticalArrangement = Arrangement.spacedBy(Tokens.Space.m)) {
        MetadataLine(stringResource(R.string.chat_background_status_label), backgroundStatus(command.outcome),
            failed = command.outcome == BackgroundOutcome.Failed)
        command.elapsed(now)?.let { MetadataLine(stringResource(R.string.chat_background_runtime), backgroundDuration(it)) }
        parseTime(task.startedAt)?.let {
            MetadataLine(stringResource(R.string.chat_background_started),
                LocalizedFormats.format(it, LocalizedFormats.DATE_TIME, res.configuration.locales[0], ZoneId.systemDefault()))
        }
        task.exitCode?.let { MetadataLine(stringResource(R.string.chat_background_exit_code), it.toString()) }
        task.command?.takeIf(String::isNotBlank)?.let { text ->
            var expanded by remember(task.id) { mutableStateOf(false) }
            var clipped by remember(text) { mutableStateOf(false) }
            Column(verticalArrangement = Arrangement.spacedBy(Tokens.Space.xs)) {
                Row(Modifier.fillMaxWidth(), verticalAlignment = Alignment.CenterVertically) {
                    Text(stringResource(R.string.chat_background_command), Modifier.weight(1f), style = PersonaTheme.type.headline, color = palette.ink)
                    JIconButton(PersonaIcons.copy, copyLabel, { copy(text, copied) })
                }
                JCard {
                    SelectionContainer {
                        Text(text, style = PersonaTheme.type.code, color = palette.ink,
                            maxLines = if (expanded) Int.MAX_VALUE else 3, overflow = TextOverflow.Ellipsis,
                            onTextLayout = { if (!expanded) clipped = it.hasVisualOverflow })
                    }
                }
                if (expanded || clipped) JTextButton(stringResource(if (expanded) R.string.chat_background_less_command
                    else R.string.chat_background_full_command), onClick = { expanded = !expanded })
            }
        }
        Row(Modifier.fillMaxWidth(), verticalAlignment = Alignment.CenterVertically) {
            Text(stringResource(R.string.chat_background_output), Modifier.weight(1f), style = PersonaTheme.type.headline, color = palette.ink)
            output.tail?.let { tail ->
                val count = backgroundOutputLines(tail.text)
                Text(pluralStringResource(if (tail.suffix) R.plurals.chat_background_last_lines else R.plurals.chat_background_lines, count, count),
                    color = palette.ink2, style = PersonaTheme.type.caption)
            }
        }
        if (path != null && onOpenFile != null) JTextButton(stringResource(R.string.chat_background_open_file), { onOpenFile(path) })
        val tail = output.tail
        when {
            path == null -> OutputNote(stringResource(if (command.running) R.string.chat_background_output_pending else R.string.chat_background_output_empty))
            scope == null || files == null -> OutputNote(stringResource(R.string.chat_background_output_unavailable))
            tail == null && !output.failed -> JLoading()
            tail?.text?.isEmpty() == true -> OutputNote(stringResource(if (command.running) R.string.chat_background_output_pending else R.string.chat_background_output_empty))
            tail != null -> JCard {
                SelectionContainer {
                    Text(tail.text, Modifier.fillMaxWidth().heightIn(max = Tokens.Size.box150 * 2).verticalScroll(logScroll),
                        style = PersonaTheme.type.code, color = palette.ink)
                }
            }
        }
        if (output.failed) JNotice(stringResource(R.string.chat_background_output_failed),
            actionLabel = stringResource(R.string.chat_background_retry), onAction = { retry++ })
    }
}

internal fun backgroundOutputLines(text: String): Int = if (text.isEmpty()) 0 else text.removeSuffix("\n").count { it == '\n' } + 1

@Composable
private fun MetadataLine(label: String, value: String, failed: Boolean = false) {
    val palette = PersonaTheme.palette
    Row(Modifier.fillMaxWidth(), horizontalArrangement = Arrangement.spacedBy(Tokens.Space.m)) {
        Text(label, Modifier.weight(1f), style = PersonaTheme.type.callout, color = palette.ink2)
        Text(value, Modifier.weight(1f), style = PersonaTheme.type.callout, color = if (failed) palette.danger.text else palette.ink)
    }
}

@Composable
private fun OutputNote(words: String) {
    Text(words, color = PersonaTheme.palette.ink2, style = PersonaTheme.type.callout)
}
