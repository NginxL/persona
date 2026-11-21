package persona.core.designsystem

import androidx.compose.foundation.layout.*
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.semantics.heading
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextOverflow
import persona.core.designsystem.icons.PhIcons

/** A sidebar row's footprint shared by the Pages, Persona Chats and Agents headings. */
@Composable
fun JSidebarSectionHeading(
    title: String,
    open: Boolean? = null,
    onToggle: (() -> Unit)? = null,
    actions: @Composable RowScope.() -> Unit = {},
) {
    Row(
        Modifier.fillMaxWidth().padding(top = Tokens.Space.s)
            .then(if (open != null && onToggle != null) Modifier.foldToggle(open, onToggle) else Modifier)
            .semantics { heading() }
            .heightIn(min = Tokens.MinTouch.android)
            .padding(horizontal = Tokens.Space.s),
        verticalAlignment = Alignment.CenterVertically,
    ) {
        Text(title, Modifier.weight(1f), style = PersonaTheme.type.label.copy(fontWeight = FontWeight.SemiBold),
            color = PersonaTheme.palette.ink2, maxLines = 1, overflow = TextOverflow.Ellipsis)
        if (open != null) JIcon(if (open) PhIcons.CaretUp else PhIcons.CaretDown, null, tint = PersonaTheme.palette.ink3)
        actions()
    }
}
