package persona.core.designsystem

import androidx.compose.ui.res.stringResource
import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.text.selection.SelectionContainer
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.semantics.Role
import androidx.compose.ui.semantics.contentDescription
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.Dp
import androidx.compose.ui.unit.dp
import persona.core.designsystem.icons.PhIcon
import persona.core.designsystem.icons.PhIcons
import persona.core.domain.MessageContext
import persona.core.domain.contextSummary

/** How tall an opened context's text gets before it scrolls: a long chat history would otherwise fill the screen. */
val ChatContextBodyMaxHeight: Dp = 320.dp

/** The icon of a context by its kind: instructions, a document, a selection to edit, anything else. */
fun contextIcon(kind: String): PhIcon = when (kind) {
    "instructions" -> PhIcons.Info
    "doc" -> PhIcons.FileText
    "edit" -> PhIcons.PencilSimple
    else -> PhIcons.Quotes
}

/**
 * What a message carries besides what the user wrote (`<persona-context>` blocks: page excerpts picked on the phone,
 * everything a Feishu `/agent` request hands its agent), as one quiet folded row beside the bubble:
 * "Context · N items" or "Context · title". Open, it lists the titles; a title opens to its text and, when it has a link,
 * "Open" ([onOpen]). With one context the row opens straight to its text. [id] keeps what is open across scrolling.
 */
@Composable
fun MessageContextGroup(
    contexts: List<MessageContext>,
    id: String,
    onOpen: (String) -> Unit,
    modifier: Modifier = Modifier,
    alignEnd: Boolean = true,
) {
    if (contexts.isEmpty()) return
    val palette = PersonaTheme.palette
    val type = PersonaTheme.type
    val style = PersonaTheme.style
    var open by rememberSaveable(id) { mutableStateOf(false) }
    // The opened list fades in place (ContextGroup, LinkSheet.swift:554, ChatPin.swift:707-726). One context is this fade only.
    val edge = rememberOpenEdge(id)
    val revealing = edge.revealing(open)
    val one = contexts.singleOrNull()
    val summary = contextSummary(contexts).text()
    Column(
        modifier.fillMaxWidth().padding(vertical = Tokens.Space.xxs),
        horizontalAlignment = if (alignEnd) Alignment.End else Alignment.Start,
    ) {
        val spoken = stringResource(R.string.designsystem_with_state, summary, stringResource(if (open) R.string.designsystem_expanded else R.string.designsystem_collapsed))
        Row(
            Modifier.fillMaxWidth(0.86f).wrapContentWidth(if (alignEnd) Alignment.End else Alignment.Start)
                .heightIn(min = 36.dp.row).clip(PersonaTheme.shapes.controlShape)
                .clickable(role = Role.Button) { open = !open }
                .semantics { contentDescription = spoken }
                .padding(horizontal = Tokens.Space.s),
            verticalAlignment = Alignment.CenterVertically,
        ) {
            JIcon(one?.let { contextIcon(it.kind) } ?: PhIcons.Stack, null, size = 14.dp, tint = palette.ink3)
            Spacer(Modifier.width(Tokens.Space.xs + Tokens.Space.xxs))
            Text(summary, style = type.caption.copy(fontWeight = FontWeight.Normal), color = palette.ink2, maxLines = 1,
                overflow = TextOverflow.Ellipsis, modifier = Modifier.weight(1f, fill = false))
            Spacer(Modifier.width(Tokens.Space.xs))
            JIcon(if (open) PhIcons.CaretUp else PhIcons.CaretDown, null, size = 12.dp, tint = palette.ink3)
        }
        if (open) {
            val shape = PersonaTheme.shapes.controlShape
            Column(
                Modifier.unfolding(revealing).fillMaxWidth(0.86f).clip(shape).background(palette.sunken)
                    .then(if (style == AppStyle.Board) Modifier.border(1.dp, palette.line, shape) else Modifier),
            ) {
                if (one != null) ContextBody(one, onOpen, Modifier.padding(top = Tokens.Space.m))
                else contexts.forEachIndexed { index, context ->
                    if (index > 0) Box(Modifier.fillMaxWidth().height(1.dp).background(palette.line))
                    ContextEntry(context, "$id/$index", onOpen)
                }
            }
        }
    }
}

/** One context in the opened list: its icon and title on one line ("Empty" when it has no text); a tap shows the text. */
@Composable
private fun ContextEntry(context: MessageContext, id: String, onOpen: (String) -> Unit) {
    val palette = PersonaTheme.palette
    val type = PersonaTheme.type
    var open by rememberSaveable(id) { mutableStateOf(false) }
    // One title's text fades on its own (ContextRow, LinkSheet.swift:591, ChatPin.swift:707-726).
    val edge = rememberOpenEdge(id)
    val revealing = edge.revealing(open)
    val label = context.label.text()
    val spoken = stringResource(R.string.designsystem_with_state, label, stringResource(if (open) R.string.designsystem_expanded else R.string.designsystem_collapsed))
    Column {
        Row(
            Modifier.fillMaxWidth().heightIn(min = 40.dp.row).clickable(role = Role.Button) { open = !open }
                .semantics { contentDescription = spoken }
                .padding(horizontal = Tokens.Space.m),
            verticalAlignment = Alignment.CenterVertically,
        ) {
            JIcon(contextIcon(context.kind), null, size = 14.dp, tint = palette.ink3)
            Spacer(Modifier.width(Tokens.Space.s))
            Text(label, style = type.caption.copy(fontWeight = FontWeight.Normal), color = palette.ink2, maxLines = 1,
                overflow = TextOverflow.Ellipsis, modifier = Modifier.weight(1f))
            if (context.body.isEmpty()) {
                Spacer(Modifier.width(Tokens.Space.s))
                Text(stringResource(R.string.designsystem_context_empty), style = type.micro, color = palette.ink3, maxLines = 1)
            }
            Spacer(Modifier.width(Tokens.Space.xs))
            JIcon(if (open) PhIcons.CaretDown else PhIcons.CaretRight, null, size = 12.dp, tint = palette.ink3)
        }
        if (open) ContextBody(context, onOpen, Modifier.unfolding(revealing))
    }
}

/** A context's text (selectable, scrolling past [ChatContextBodyMaxHeight]) and "Open" for its link. */
@Composable
private fun ContextBody(context: MessageContext, onOpen: (String) -> Unit, modifier: Modifier = Modifier) {
    val palette = PersonaTheme.palette
    val type = PersonaTheme.type
    Column(
        modifier.fillMaxWidth().padding(start = Tokens.Space.m, end = Tokens.Space.m, bottom = Tokens.Space.s),
        verticalArrangement = Arrangement.spacedBy(Tokens.Space.xs),
    ) {
        if (context.body.isEmpty()) Text(stringResource(R.string.designsystem_context_no_content), style = type.caption.copy(fontWeight = FontWeight.Normal), color = palette.ink3, maxLines = 1)
        else Box(Modifier.fillMaxWidth().heightIn(max = ChatContextBodyMaxHeight).verticalScroll(rememberScrollState())) {
            SelectionContainer {
                ReadingContent { Text(context.body, style = type.caption.copy(fontWeight = FontWeight.Normal), color = palette.ink2) }
            }
        }
        context.url?.let { url ->
            JButton(stringResource(R.string.designsystem_open), { onOpen(url) }, kind = JButtonKind.Quiet, size = JButtonSize.Small, icon = PhIcons.ArrowSquareOut)
        }
    }
}
