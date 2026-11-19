package persona.feature.chat

import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.draw.drawBehind
import androidx.compose.ui.geometry.Size
import androidx.compose.ui.platform.LocalResources
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.semantics.Role
import androidx.compose.ui.semantics.contentDescription
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextOverflow
import persona.core.contracts.SessionKey
import persona.core.designsystem.ChatContextBodyMaxHeight
import persona.core.designsystem.JIcon
import persona.core.designsystem.MessageContextGroup
import persona.core.designsystem.PersonaTheme
import persona.core.designsystem.Tokens
import persona.core.designsystem.currentLocale
import persona.core.designsystem.icons.PersonaIcons
import persona.core.domain.MessageContext
import persona.core.navigation.MessageJumps

/** A forward shows this many lines of the original until it is opened (iOS `QuoteBlock.forwardLines`). */
internal const val FORWARD_LINES = 4

internal const val FORWARD_BUBBLE_TAG = "bubble-forward"

/**
 * A sent forward at the top of its bubble (#3901, iOS `QuoteBlock` for a forward, web `ForwardBlock`): the arrow and
 * "Forwarded from Claude · chat", the computer and time, and the first [FORWARD_LINES] lines, on the bubble's quiet well with
 * the accent rule. A tap opens the original chat at that message ([openForwarded]); when this app does not hold that chat,
 * it opens to the whole text in place. [id] keeps that open across scrolling.
 */
@Composable
internal fun ForwardBubbleCard(context: MessageContext, id: String, modifier: Modifier = Modifier) {
    val palette = PersonaTheme.palette
    val type = PersonaTheme.type
    val resources = LocalResources.current
    val locale = currentLocale
    val forwarding = LocalChatForwarding.current
    val chat = LocalChatRowActions.current?.session
    var open by rememberSaveable(id) { mutableStateOf(false) }
    val heading = remember(context, resources) { ForwardMessage.heading(context.attributes, resources) }
    val meta = remember(context, resources, locale) { ForwardMessage.meta(context.attributes, resources, locale) }
    val summary = remember(context, resources) { ForwardMessage.summary(context.body, resources) }
    val spoken = listOfNotNull(heading, meta, summary).joinToString(": ")
    val openLabel = stringResource(R.string.chat_forward_open_original)
    val rule = Tokens.Space.xs * 3 / 4
    Column(
        modifier.fillMaxWidth().testTag(FORWARD_BUBBLE_TAG).clip(PersonaTheme.shapes.controlShape).background(palette.sunken)
            .drawBehind { drawRect(palette.accent, size = Size(rule.toPx(), size.height)) }
            .clickable(role = Role.Button, onClickLabel = openLabel) {
                if (!openForwarded(context, chat, forwarding)) open = !open
            }
            .semantics { contentDescription = spoken }
            .padding(start = Tokens.Space.m, end = Tokens.Space.m, top = Tokens.Space.s, bottom = Tokens.Space.s),
        verticalArrangement = Arrangement.spacedBy(Tokens.Space.xxs),
    ) {
        Row(verticalAlignment = Alignment.CenterVertically) {
            JIcon(PersonaIcons.forwardMessage, null, size = Tokens.Space.l, tint = palette.accent, decorative = true)
            Spacer(Modifier.width(Tokens.Space.xs))
            Text(heading, style = type.caption.copy(fontWeight = FontWeight.Normal), color = palette.ink2, maxLines = 1,
                overflow = TextOverflow.Ellipsis)
        }
        meta?.let { Text(it, style = type.micro, color = palette.ink2, maxLines = 1, overflow = TextOverflow.Ellipsis) }
        if (open) Text(context.body.ifBlank { summary }, Modifier.heightIn(max = ChatContextBodyMaxHeight).verticalScroll(rememberScrollState()),
            style = type.callout, color = palette.ink)
        else Text(summary, style = type.callout, color = palette.ink, maxLines = FORWARD_LINES, overflow = TextOverflow.Ellipsis)
    }
}

/**
 * Opens the chat a forward came from, at the message, when this app holds that chat ([chat] is the one the card is in, for
 * its hub group); false otherwise.
 */
internal fun openForwarded(context: MessageContext, chat: SessionKey?, forwarding: ChatForwarding?): Boolean {
    val origin = ForwardMessage.origin(context.url, context.attributes["msg"]) ?: return false
    forwarding ?: return false
    val key = chat?.let(origin::key) ?: SessionKey(origin.machine, origin.session)
    val snapshot = forwarding.assistant.value
    val persona = key == snapshot.mainSession || snapshot.conversations.any { it.key == key }
    if (!persona && forwarding.agents.value.none { it.key == key }) return false
    origin.item?.let { MessageJumps.request(key.machineId, key.sessionId, it) }
    forwarding.openChat(key, persona)
    return true
}

/** A message of yours not in the transcript yet: its words as before, or [PendingForwardBubble] when it carries a forward. */
@Composable
internal fun OutboxBubble(text: String, id: String) {
    if (carriesForward(text)) PendingForwardBubble(text, id) else persona.core.designsystem.UserBubble(composerMessagePreview(text))
}

/** Whether a message carries a forward block (#3901). */
fun carriesForward(text: String): Boolean =
    persona.core.domain.hasContextTag(text) && persona.core.domain.parseContexts(text).first.any { it.kind == ForwardMessage.KIND }

/**
 * A message of yours that carries a forward (#3901) while it waits to reach the transcript: the forward's card at the top
 * of the bubble and the note under it, as the sent message will show them.
 */
@Composable
fun PendingForwardBubble(text: String, id: String) {
    // Everything the message carries besides the forward, as the sent bubble shows it (MergedUserBubble): the other
    // contexts above it, then the files, photos and note inside it.
    val model = remember(text) { messageModel(text, user = true) }
    val forwards = model.contexts.filter { it.kind == ForwardMessage.KIND }
    val contexts = model.contexts.filter { it.kind != ForwardMessage.KIND }
    val rest = model.plain
    val uri = androidx.compose.ui.platform.LocalUriHandler.current
    Column(Modifier.fillMaxWidth()) {
        MessageContextGroup(contexts, "${id}/contexts", uri::openUri)
        AttachedFileCards(model.attached)
        persona.core.designsystem.UserBubbleFrame {
            forwards.forEachIndexed { index, forward ->
                ForwardBubbleCard(forward, "outbox/$id/$index")
                if (index < forwards.lastIndex || rest.isNotEmpty() || model.images.isNotEmpty())
                    Spacer(Modifier.padding(top = Tokens.Space.s))
            }
            if (model.images.isNotEmpty()) {
                AttachedPhotoRow(model.images)
                if (rest.isNotEmpty()) Spacer(Modifier.padding(top = Tokens.Space.s))
            }
            if (rest.isNotEmpty()) Text(rest, style = PersonaTheme.type.body, color = PersonaTheme.palette.ink)
        }
    }
}
