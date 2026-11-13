package persona.core.designsystem

import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.sizeIn
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.rotate
import androidx.compose.ui.semantics.contentDescription
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.text.style.TextOverflow
import persona.core.contracts.CoreText
import persona.core.contracts.Direction
import persona.core.contracts.DirectionTaskState
import persona.core.designsystem.icons.PersonaIcons

// The direction tree's words and colours (#4352), one place for the drawer, the To-dos page and the Agents page: the
// hub decides a task's state, these only say it. Needs you takes the needs-you colour, Working the working one, Stuck and
// To verify the caution colour, Not started the quiet idle grey (#5860); the words always go with the colour
// (docs/design/DESIGN_SYSTEM.md §6.3).

/** The colours of a task's [state]. */
fun PersonaPalette.directionTone(state: DirectionTaskState): StatusColors = when (state) {
    DirectionTaskState.NeedsYou -> status(Status.NeedsYou)
    DirectionTaskState.Working -> status(Status.Working)
    DirectionTaskState.Stuck, DirectionTaskState.Review -> tone(Tone.Caution)
    // Not started: the quiet idle grey, the web's text-ink-3 (#5860), so it never shouts beside the active states.
    DirectionTaskState.Planned -> tone(Tone.Neutral)
}

/** A task's state word ("Needs you", "To verify", "Stuck", "Working") in the page's language. */
val DirectionTaskState.localizedLabel: String @Composable get() = CoreText.of(label).text()

/** What a direction's heading says: its name, 书记官 for the secretary group, an untitled chat's word without a name. */
val Direction.localizedTitle: String @Composable get() = title.text()

/** A task's state in words, in its colour, for the end of a task row. */
@Composable
fun JDirectionStateLabel(state: DirectionTaskState, modifier: Modifier = Modifier) {
    Text(state.localizedLabel, modifier, style = PersonaTheme.type.caption, color = PersonaTheme.palette.directionTone(state).text,
        maxLines = 1, softWrap = false, overflow = TextOverflow.Ellipsis)
}

/**
 * What a direction has going, as the drawer's 常驻 row shows it (#6626): the working spinner while anything under it
 * runs ([Direction.running], the hub's one rule), then its tasks waiting on you in the needs-you colour ("1 need you",
 * the words of leaderCountsLabel); nothing at all when neither. Never "N working" beside the spinner: one meaning, one
 * expression.
 */
@Composable
fun JDirectionCounts(direction: Direction, modifier: Modifier = Modifier) {
    val needsYou = direction.counts.needsYou.takeIf { it > 0 }?.let { CoreText.of(CoreText.Key.DirectionCountNeedsYou, it).text() }
    if (!direction.running && needsYou == null) return
    Row(modifier, verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(Tokens.Space.xs)) {
        if (direction.running) JStatusDot(Status.Working, spinning = true, breathes = false)
        needsYou?.let {
            Text(it, style = PersonaTheme.type.caption, color = PersonaTheme.palette.status(Status.NeedsYou).text, maxLines = 1,
                softWrap = false, overflow = TextOverflow.Ellipsis)
        }
    }
}

/**
 * The caret that folds a tree row open or shut, a button of its own (48dp) beside the row's own tap: TalkBack hears
 * [label] (what it folds), whether it is open and what a tap does ([foldToggle]).
 */
@Composable
fun JDisclosureButton(open: Boolean, onToggle: () -> Unit, label: String, modifier: Modifier = Modifier) {
    Box(modifier.sizeIn(minWidth = Tokens.MinTouch.android, minHeight = Tokens.MinTouch.android).foldToggle(open, onToggle)
        .semantics { contentDescription = label }, contentAlignment = Alignment.Center) {
        JIcon(PersonaIcons.disclosure, null, size = Tokens.Space.l, tint = PersonaTheme.palette.ink3,
            modifier = Modifier.rotate(if (open) 90f else 0f), decorative = true)
    }
}
