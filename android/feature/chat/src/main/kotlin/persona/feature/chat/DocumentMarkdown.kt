package persona.feature.chat

import androidx.compose.foundation.layout.Box
import androidx.compose.runtime.Composable
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.text.TextStyle
import persona.core.designsystem.PersonaTheme

/**
 * A document's Markdown drawn as formatted text outside `feature:chat`, the way a reply draws it: a domain space's
 * "About this space" (#13500). Not selectable, so it can sit inside a folded or tappable card.
 */
@Composable
fun DocumentMarkdown(
    text: String, id: String, modifier: Modifier = Modifier,
    bodyStyle: TextStyle = PersonaTheme.type.body, color: Color = PersonaTheme.palette.ink,
) {
    Box(modifier) {
        ChatMarkdown(text = text, id = id, bodyStyle = bodyStyle, color = color, selectable = false)
    }
}
