package persona.feature.chat

import androidx.compose.foundation.layout.*
import androidx.compose.material3.Text
import androidx.compose.runtime.*
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalUriHandler
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.text.*
import androidx.compose.ui.text.style.TextOverflow
import persona.core.designsystem.*
import androidx.compose.ui.text.style.TextDecoration

@Composable
internal fun chatLinkAnnotations(text: String): AnnotatedString {
    val color = PersonaTheme.palette.accent
    val links = remember(text) { ChatLinks.matches(text) }
    return remember(text, color) {
        buildAnnotatedString {
            append(text)
            links.forEach { addLink(LinkAnnotation.Url(it.url, TextLinkStyles(SpanStyle(color = color, textDecoration = TextDecoration.Underline))), it.start, it.end) }
        }
    }
}

@Composable
fun ChatLinkText(text: String, modifier: Modifier = Modifier, style: TextStyle = PersonaTheme.type.body.copy(color = PersonaTheme.palette.ink)) {
    Text(chatLinkAnnotations(text), modifier, style = style)
}

/**
 * A context the next message carries (a page's picked text). [compact] is one line, for when the link card leaves
 * the conversation four tenths of the screen: the whole card pushed the composer under the link card (2026-11-02).
 */
@Composable
internal fun ChatContextCard(context: ChatLinkContext, compact: Boolean = false, onRemove: (() -> Unit)? = null) {
    val uriHandler = LocalUriHandler.current
    val quote = context.kind == ChatLinkContext.QUOTE
    val session = context.url.startsWith(SESSION_CONTEXT_URL)
    // A quoted message ("Quote") and a session picked with @ say whom they quote (iOS `quoteHeading`); no title line under it.
    val label = when {
        // Words picked in a message ("Select", then Quote) say so, as iOS's `quoteHeading` does.
        quote && context.part == ChatLinkContext.PART_SELECTION -> stringResource(R.string.chat_quote_selection,
            if (context.title == ChatLinkContext.QUOTE_ME) stringResource(R.string.chat_exchange_me) else context.title)
        quote && context.title == ChatLinkContext.QUOTE_ME -> stringResource(R.string.chat_quote_yours)
        quote -> stringResource(R.string.chat_quote_from, context.title)
        session -> stringResource(R.string.chat_quote_session, context.title)
        context.kind == "edit" -> stringResource(R.string.chat_context_ai_edit)
        else -> stringResource(R.string.chat_context_linked)
    }
    val title = context.title.ifBlank { context.url.ifBlank { stringResource(R.string.chat_context_web_selection) } }
    if (compact) JCard(Modifier.fillMaxWidth(), padding = Tokens.Space.xs) {
        Row(Modifier.padding(start = Tokens.Space.s), verticalAlignment = Alignment.CenterVertically) {
            Text(stringResource(R.string.chat_label_value, label, context.text.lineSequence().firstOrNull { it.isNotBlank() }?.trim() ?: title), Modifier.weight(1f),
                style = PersonaTheme.type.caption, color = PersonaTheme.palette.ink2, maxLines = 1, overflow = TextOverflow.Ellipsis)
            if (onRemove != null) JTextButton(stringResource(R.string.chat_context_remove), onRemove)
        }
    } else JCard(Modifier.fillMaxWidth()) {
        Row(Modifier.padding(Tokens.Space.s)) {
            Column(Modifier.weight(1f)) {
                if (quote || session) Text(label, style = PersonaTheme.type.callout, color = PersonaTheme.palette.accent, maxLines = 1, overflow = TextOverflow.Ellipsis)
                else {
                    Text(label, style = PersonaTheme.type.micro, color = PersonaTheme.palette.ink2)
                    Text(title, style = PersonaTheme.type.callout, color = PersonaTheme.palette.ink, maxLines = 1, overflow = TextOverflow.Ellipsis)
                }
                Text(if (session) context.text.lines().drop(1).joinToString(" ") else context.text, maxLines = 2, overflow = TextOverflow.Ellipsis, style = PersonaTheme.type.caption, color = PersonaTheme.palette.ink2)
                if (ChatLinks.webUrl(context.url) != null) JTextButton(stringResource(R.string.chat_context_view_source), { uriHandler.openUri(context.url) })
            }
            if (onRemove != null) JTextButton(stringResource(R.string.chat_context_remove), onRemove)
        }
    }
}

/** Where a session picked with @ points ([sessionQuote]): `persona://session/<machine>/<session>`. */
internal const val SESSION_CONTEXT_URL = "persona://session/"
