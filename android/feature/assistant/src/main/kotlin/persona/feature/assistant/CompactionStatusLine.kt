package persona.feature.assistant

import androidx.compose.foundation.layout.padding
import androidx.compose.runtime.Composable
import androidx.compose.ui.Modifier
import androidx.compose.ui.res.stringResource
import persona.core.designsystem.InlineTone
import persona.core.designsystem.JIconButton
import persona.core.designsystem.JInlineStatus
import persona.core.designsystem.Tokens
import persona.core.designsystem.icons.PhIcons
import persona.core.designsystem.pagePadding

/** The selected conversation's compaction as one line over it; a failure closes by hand. */
@Composable
internal fun CompactionStatusLine(line: CompactionLine, onDismiss: () -> Unit, modifier: Modifier = Modifier) {
    JInlineStatus(
        line.text(),
        modifier.padding(horizontal = pagePadding, vertical = Tokens.Space.s),
        tone = when (line) {
            CompactionLine.Running -> InlineTone.Busy
            CompactionLine.Done -> InlineTone.Done
            is CompactionLine.Failed -> InlineTone.Failed
        },
        action = if (line is CompactionLine.Failed) ({ JIconButton(PhIcons.X, stringResource(R.string.assistant_close), onDismiss) }) else null,
    )
}

/** The line's words: "Compacting context…", "Compacted", "Compaction failed" or "Compaction failed: <the hub's reason>". */
@Composable
internal fun CompactionLine.text(): String = when (this) {
    CompactionLine.Running -> stringResource(R.string.assistant_compaction_running)
    CompactionLine.Done -> stringResource(R.string.assistant_compaction_done)
    is CompactionLine.Failed -> if (error.isBlank()) stringResource(R.string.assistant_compaction_failed)
        else stringResource(R.string.assistant_compaction_failed_reason, error)
}
