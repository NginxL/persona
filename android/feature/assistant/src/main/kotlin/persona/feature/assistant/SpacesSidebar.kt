package persona.feature.assistant

import androidx.compose.foundation.lazy.LazyListScope
import androidx.compose.foundation.lazy.itemsIndexed
import androidx.compose.ui.res.stringResource
import persona.core.contracts.api.Track
import persona.core.designsystem.GroupPosition
import persona.core.designsystem.JGroupItem
import persona.core.designsystem.JRow
import persona.core.designsystem.SpaceIcons
import persona.core.designsystem.icons.PersonaIcons

/**
 * The sidebar's Spaces rows (#13500, the web's `SidebarSpaces.tsx`): each domain space's icon and name, then New space,
 * one group. The health space keeps its own row among the pages and is never listed here ([persona.core.contracts.api.Tracks.sidebarSpaces]).
 */
internal fun LazyListScope.spaceRows(spaces: List<Track>, shaded: String?, onSpace: (String) -> Unit, onNewSpace: (() -> Unit)?) {
    val total = spaces.size + if (onNewSpace != null) 1 else 0
    itemsIndexed(spaces, key = { _, track -> "space-" + track.slug }) { index, track ->
        JGroupItem(GroupPosition.of(index, total)) {
            JRow(track.name, onClick = { onSpace(track.slug) },
                leadingIcon = SpaceIcons.of(track.iconToken),
                selected = shaded == AssistantSidebarShade.space(track.slug))
        }
    }
    if (onNewSpace != null) item(key = "space-new") {
        JGroupItem(GroupPosition.of(spaces.size, total)) {
            JRow(stringResource(R.string.assistant_new_space), onClick = onNewSpace, leadingIcon = PersonaIcons.add,
                selected = shaded == AssistantSidebarShade.NewSpace)
        }
    }
}
