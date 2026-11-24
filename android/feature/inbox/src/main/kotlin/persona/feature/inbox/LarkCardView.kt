package persona.feature.inbox

import androidx.compose.ui.platform.LocalResources
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.platform.LocalContext

import androidx.compose.foundation.layout.*
import androidx.compose.material3.Text
import androidx.compose.runtime.*
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalUriHandler
import androidx.compose.ui.text.LinkAnnotation
import androidx.compose.ui.text.SpanStyle
import androidx.compose.ui.text.TextLinkStyles
import androidx.compose.ui.text.buildAnnotatedString
import androidx.compose.ui.unit.dp
import kotlinx.coroutines.launch
import persona.core.designsystem.*
import persona.core.designsystem.Tokens
import persona.core.inbox.*

@Composable
internal fun LarkLinkedText(text: String, source: LarkContentSource? = null, modifier: Modifier = Modifier) {
    val open = LocalLarkLinkOpener.current
    val uri = LocalUriHandler.current
    val style = TextLinkStyles(style = SpanStyle(color = PersonaTheme.palette.accent))
    val annotated = buildAnnotatedString {
        var cursor = 0
        Regex("https?://[\\x21-\\x7e]+", RegexOption.IGNORE_CASE).findAll(text).forEach { match ->
            val url = match.value.substringBefore('<').substringBefore('>').substringBefore('"').trimEnd('.', ',', ';', ':', '!', '?')
                .let { raw -> var clean = raw; while (clean.endsWith(')') && clean.count { it == ')' } > clean.count { it == '(' }) clean = clean.dropLast(1); clean }
            if (larkWebUrl(url) != null) {
                append(text.substring(cursor, match.range.first))
                pushLink(LinkAnnotation.Url(url, style) { if (open != null) open(url, source) else uri.openUri(url) })
                append(url)
                pop()
                cursor = match.range.first + url.length
            }
        }
        append(text.substring(cursor))
    }
    Text(annotated, modifier, style = PersonaTheme.type.body, color = PersonaTheme.palette.ink)
}

@Composable
internal fun LarkInteractiveCard(part: LarkPart.Card, repository: InboxRepository, onError: (String) -> Unit) {
    val card = part.content
    JCard(Modifier.fillMaxWidth(), padding = Tokens.Space.m) {
        if (card.title.isNotBlank()) {
            Text(card.title, style = PersonaTheme.type.headline, color = PersonaTheme.palette.ink)
            Spacer(Modifier.height(Tokens.Space.s))
        }
        Column(verticalArrangement = Arrangement.spacedBy(Tokens.Space.s)) {
            if (card.blocks.isEmpty()) LarkLinkedText(card.summary.ifBlank { stringResource(R.string.inbox_lark_card_unsupported) }, part.source)
            else CardBlocks(card.blocks, part.source, part.messageId, repository, onError)
        }
    }
}

@Composable
private fun CardBlocks(
    blocks: List<LarkCardBlock>,
    source: LarkContentSource,
    messageId: String?,
    repository: InboxRepository,
    onError: (String) -> Unit,
) {
    val scope = rememberCoroutineScope()
    val uriHandler = LocalUriHandler.current
    val linkOpener = LocalLarkLinkOpener.current

    val res = LocalResources.current
    blocks.forEach { block ->
        when (block) {
            is LarkCardBlock.Text -> LarkLinkedText(block.text, source)
            is LarkCardBlock.Placeholder -> LarkLinkedText(stringResource(block.text), source)
            is LarkCardBlock.Button -> {
                var pending by remember(block) { mutableStateOf(false) }
                val hasLink = !block.url.isNullOrBlank()
                val isCallback = block.actionMethod == 2 || !block.actionId.isNullOrBlank()
                val enabled = !pending && (hasLink || (isCallback && messageId != null))

                JButton(
                    text = if (pending) stringResource(R.string.inbox_lark_processing) else block.label.ifBlank { stringResource(R.string.inbox_lark_card_action) },
                    onClick = {
                        if (hasLink) {
                            val targetUrl = block.url ?: return@JButton
                            if (linkOpener != null) linkOpener(targetUrl, source)
                            else uriHandler.openUri(targetUrl)
                        } else if (isCallback && messageId != null && block.actionId != null) {
                            pending = true
                            scope.launch {
                                try {
                                    repository.cardAction(messageId, block.actionId!!)
                                } catch (e: Exception) {
                                    onError(e.message ?: res.getString(R.string.inbox_lark_action_failed))
                                } finally {
                                    pending = false
                                }
                            }
                        }
                    },
                    enabled = enabled,
                    modifier = Modifier.fillMaxWidth(),
                    kind = if (isCallback) JButtonKind.Primary else JButtonKind.Secondary
                )
            }
            is LarkCardBlock.Image -> LarkPartContent(source.image(block.url).copy(description = block.alt.ifBlank { stringResource(R.string.inbox_lark_card_image) },
                width = block.width, height = block.height), repository, onError)
            is LarkCardBlock.Group -> Column(verticalArrangement = Arrangement.spacedBy(Tokens.Space.s)) {
                CardBlocks(block.children, source, messageId, repository, onError)
            }
            is LarkCardBlock.Columns -> BoxWithConstraints(Modifier.fillMaxWidth()) {
                // Bounded weights prevent long field values from expanding the card beyond its parent.
                if (!constraints.hasBoundedWidth || maxWidth < (block.columns.size * 112).dp) {
                    Column(verticalArrangement = Arrangement.spacedBy(Tokens.Space.s)) {
                        block.columns.forEach { Column { CardBlocks(it, source, messageId, repository, onError) } }
                    }
                } else Row(horizontalArrangement = Arrangement.spacedBy(Tokens.Space.s)) {
                    block.columns.forEachIndexed { index, column ->
                        Column(Modifier.weight(block.weights.getOrElse(index) { 1f }), verticalArrangement = Arrangement.spacedBy(Tokens.Space.s)) {
                            CardBlocks(column, source, messageId, repository, onError)
                        }
                    }
                }
            }
            LarkCardBlock.Divider -> JDivider()
        }
    }
}
