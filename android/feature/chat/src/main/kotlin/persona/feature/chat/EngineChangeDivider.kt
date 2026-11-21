package persona.feature.chat

import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.res.stringResource
import persona.core.designsystem.JModelChangeDivider
import persona.core.designsystem.JTextButton
import persona.core.domain.lines.EngineChange
import persona.core.domain.lines.ModelChanges

/** Keep the handoff behind a reader, so a long handoff never displaces the user's message. */
@Composable
internal fun EngineChangeDivider(change: EngineChange) {
    var reading by remember(change) { mutableStateOf(false) }
    val from = engineMark(change.from).name
    val to = engineMark(change.to).name
    val badge = stringResource(if (change.resumed) R.string.chat_engine_switch_resumed else R.string.chat_engine_switch_summary)
    val label = listOfNotNull("$from → $to", change.model?.let(ModelChanges::replyModelName), badge).joinToString(" · ")
    val spoken = stringResource(R.string.chat_engine_switch_spoken, from, to, badge)
    Column(Modifier.fillMaxWidth(), horizontalAlignment = Alignment.CenterHorizontally) {
        JModelChangeDivider(label, spoken)
        if (change.handoff.isNotBlank()) {
            JTextButton(stringResource(R.string.chat_engine_switch_view_handoff), onClick = { reading = true })
        }
    }
    if (reading) FullTextReader(change.handoff, onDismiss = { reading = false },
        title = stringResource(R.string.chat_engine_switch_handoff_title), plain = true)
}
