package persona.feature.chat

import android.content.Context
import android.content.Intent
import android.content.res.Resources
import android.net.Uri
import androidx.compose.foundation.gestures.awaitEachGesture
import androidx.compose.foundation.gestures.awaitFirstDown
import androidx.compose.runtime.Composable
import androidx.compose.runtime.Immutable
import androidx.compose.runtime.Stable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberUpdatedState
import androidx.compose.runtime.setValue
import androidx.compose.runtime.staticCompositionLocalOf
import androidx.compose.ui.Modifier
import androidx.compose.ui.geometry.Offset
import androidx.compose.ui.hapticfeedback.HapticFeedbackType
import androidx.compose.ui.input.pointer.PointerEventPass
import androidx.compose.ui.input.pointer.PointerEventType
import androidx.compose.ui.input.pointer.isSecondaryPressed
import androidx.compose.ui.input.pointer.pointerInput
import androidx.compose.ui.layout.LayoutCoordinates
import androidx.compose.ui.layout.onGloballyPositioned
import androidx.compose.ui.platform.LocalHapticFeedback
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.semantics.heading
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.text.AnnotatedString
import androidx.compose.ui.text.LinkAnnotation
import androidx.compose.ui.text.TextLayoutResult
import androidx.compose.ui.text.TextStyle
import androidx.compose.ui.text.buildAnnotatedString
import com.mikepenz.markdown.annotator.annotatorSettings
import com.mikepenz.markdown.annotator.buildMarkdownAnnotatedString
import com.mikepenz.markdown.compose.LocalMarkdownExtendedSpans
import com.mikepenz.markdown.compose.elements.MarkdownText
import org.intellij.markdown.IElementType
import org.intellij.markdown.ast.ASTNode
import org.intellij.markdown.ast.findChildOfType
import persona.core.designsystem.JAction
import persona.core.designsystem.JActionSheet
import persona.core.designsystem.JPointerMenu
import persona.core.designsystem.LocalSelectionAbove
import persona.core.designsystem.icons.PersonaIcons
import persona.core.domain.ports.PortLinks

internal const val CHAT_LINK_MENU_TAG = "chat_link_menu"

/** A link long-pressed or secondary-clicked in a conversation. */
@Immutable
internal data class ChatLinkMenuTarget(val url: String, val anchor: Offset?)

/** Shared link-menu state provided by [ChatLinkHost] so links coordinate with [MessageMenuState]. */
@Stable
internal class ChatLinkMenuState {
    var target by mutableStateOf<ChatLinkMenuTarget?>(null)
        private set

    private var activePress: Any? = null

    val isPressingLink: Boolean get() = activePress != null

    fun beginPress(token: Any) {
        activePress = token
    }

    fun endPress(token: Any) {
        if (activePress === token) activePress = null
    }

    fun open(url: String, anchor: Offset?) {
        target = ChatLinkMenuTarget(url, anchor)
    }

    fun close() {
        target = null
    }
}

internal val LocalChatLinkMenu = staticCompositionLocalOf<ChatLinkMenuState?> { null }

/** Whether [url] is eligible for the link long-press menu (excludes local file paths and refused schemes). */
internal fun isMenuLinkUrl(url: String): Boolean {
    val trimmed = url.trim()
    if (trimmed.isEmpty() || PathLinks.pathOf(trimmed) != null) return false
    return ChatLinks.kind(trimmed) != ChatLinkKind.Refused
}

/** Concise header shown above the link actions so users can verify the destination URL. */
internal fun linkMenuSection(url: String): String {
    val trimmed = url.trim()
    return trimmed
        .removePrefix("https://")
        .removePrefix("http://")
        .ifEmpty { trimmed }
}

internal fun AnnotatedString.hasMenuLinkTargets(): Boolean =
    getLinkAnnotations(0, length).any { range ->
        val item = range.item as? LinkAnnotation.Url ?: return@any false
        isMenuLinkUrl(item.url)
    }

/**
 * Returns the eligible URL under [position] in [layout], or null when [position] lies on non-link text
 * or empty whitespace outside the rendered line bounds.
 */
internal fun findLinkAtPosition(layout: TextLayoutResult, position: Offset): String? {
    val text = layout.layoutInput.text
    if (text.isEmpty()) return null
    if (position.x < 0f || position.y < 0f || position.x > layout.size.width || position.y > layout.size.height) return null
    val line = layout.getLineForVerticalPosition(position.y)
    if (position.x < layout.getLineLeft(line) || position.x > layout.getLineRight(line)) return null
    val offset = layout.getOffsetForPosition(position)
    val candidates = buildList {
        if (offset in 0 until text.length) add(offset)
        if (offset > 0 && offset - 1 < text.length) add(offset - 1)
    }
    for (index in candidates) {
        val box = layout.getBoundingBox(index)
        if (position.x !in box.left..box.right || position.y !in box.top..box.bottom) continue
        val url = text.getLinkAnnotations(index, index + 1)
            .firstNotNullOfOrNull { (it.item as? LinkAnnotation.Url)?.url?.trim()?.takeIf(::isMenuLinkUrl) }
        if (url != null) return url
    }
    return null
}

/** Builds the dropdown actions for [url] matching the link sheet's browser, copy, and share rules. */
internal fun chatLinkMenuActions(
    url: String,
    resources: Resources,
    isForwardedPort: Boolean,
    onOpenInApp: () -> Unit,
    onOpenInBrowser: () -> Unit,
    onCopy: () -> Unit,
    onShare: () -> Unit,
): List<JAction> {
    val kind = ChatLinks.kind(url)
    val web = kind == ChatLinkKind.Web && ChatLinks.webUrl(url) != null
    val canLeaveApp = web && !isForwardedPort
    val raw = buildList {
        add(JAction(
            label = resources.getString(if (web) R.string.chat_link_open_in_app else R.string.chat_link_open),
            icon = PersonaIcons.servedPage,
            onClick = onOpenInApp,
        ))
        if (canLeaveApp) {
            add(JAction(
                label = resources.getString(R.string.chat_link_open_browser),
                icon = PersonaIcons.openExternal,
                onClick = onOpenInBrowser,
            ))
        }
        add(JAction(
            label = resources.getString(R.string.chat_link_copy),
            icon = PersonaIcons.link,
            onClick = onCopy,
        ))
        if (canLeaveApp) {
            add(JAction(
                label = resources.getString(R.string.chat_link_share),
                icon = PersonaIcons.share,
                onClick = onShare,
            ))
        }
    }
    val header = linkMenuSection(url)
    return raw.mapIndexed { idx, action -> if (idx == 0) action.copy(section = header) else action }
}

internal fun openInSystemBrowser(context: Context, url: String): Boolean =
    runCatching {
        context.startActivity(
            Intent(Intent.ACTION_VIEW, Uri.parse(url))
                .addCategory(Intent.CATEGORY_BROWSABLE)
                .addFlags(Intent.FLAG_ACTIVITY_NEW_TASK),
        )
    }.isSuccess

internal fun shareLinkText(context: Context, url: String): Boolean =
    runCatching {
        val send = Intent(Intent.ACTION_SEND).setType("text/plain").putExtra(Intent.EXTRA_TEXT, url)
        context.startActivity(Intent.createChooser(send, null).addFlags(Intent.FLAG_ACTIVITY_NEW_TASK))
    }.isSuccess

@Composable
internal fun ChatLinkMenuOverlay(
    target: ChatLinkMenuTarget,
    resources: Resources,
    ports: ChatPorts?,
    onDismiss: () -> Unit,
    onOpenInApp: (String) -> Unit,
    onOpenInBrowser: (String) -> Unit,
    onCopy: (String) -> Unit,
    onShare: (String) -> Unit,
) {
    val isForwarded = remember(target.url, ports) {
        ports?.let { PortLinks.link(target.url, it.hostnames) } != null
    }
    val actions = remember(target.url, resources, isForwarded, onOpenInApp, onOpenInBrowser, onCopy, onShare) {
        chatLinkMenuActions(
            url = target.url,
            resources = resources,
            isForwardedPort = isForwarded,
            onOpenInApp = { onOpenInApp(target.url) },
            onOpenInBrowser = { onOpenInBrowser(target.url) },
            onCopy = { onCopy(target.url) },
            onShare = { onShare(target.url) },
        )
    }
    val anchor = target.anchor
    if (anchor != null) {
        JPointerMenu(
            anchor = anchor,
            onDismiss = onDismiss,
            actions = actions,
            modifier = Modifier.testTag(CHAT_LINK_MENU_TAG),
        )
    } else {
        JActionSheet(
            actions = actions,
            onDismiss = onDismiss,
            title = linkMenuSection(target.url),
        )
    }
}

/**
 * Intercepts long-press and secondary-click gestures that land on an eligible [LinkAnnotation.Url]
 * inside a text composable, leaving short taps, scrolling, and non-link message long-press untouched.
 */
@Composable
internal fun Modifier.chatLinkLongPress(
    annotated: AnnotatedString,
    layout: () -> TextLayoutResult?,
): Modifier {
    val linkMenu = LocalChatLinkMenu.current
    if (linkMenu == null || LocalSelectionAbove.current || LocalInPicture.current || !annotated.hasMenuLinkTargets()) return this
    val haptics = LocalHapticFeedback.current
    val currentLayout by rememberUpdatedState(layout)
    val coords = remember { mutableStateOf<LayoutCoordinates?>(null) }
    return this
        .onGloballyPositioned { coords.value = it }
        .pointerInput(annotated, linkMenu) {
            awaitEachGesture {
                val firstEvent = awaitPointerEvent(PointerEventPass.Initial)
                val down = firstEvent.changes.firstOrNull { it.pressed && !it.previousPressed } ?: return@awaitEachGesture
                val hit = currentLayout()?.let { findLinkAtPosition(it, down.position) } ?: return@awaitEachGesture
                if (firstEvent.type == PointerEventType.Press && firstEvent.buttons.isSecondaryPressed) {
                    down.consume()
                    haptics.performHapticFeedback(HapticFeedbackType.LongPress)
                    val anchor = coords.value?.takeIf { it.isAttached }?.localToWindow(down.position)
                    linkMenu.open(hit, anchor)
                    return@awaitEachGesture
                }
                val token = Any()
                linkMenu.beginPress(token)
                try {
                    val slop = viewConfiguration.touchSlop
                    val ended = withTimeoutOrNull(viewConfiguration.longPressTimeoutMillis) {
                        while (true) {
                            val event = awaitPointerEvent(PointerEventPass.Initial)
                            val change = event.changes.firstOrNull { it.id == down.id } ?: return@withTimeoutOrNull true
                            if (!change.pressed || (change.position - down.position).getDistance() > slop) {
                                return@withTimeoutOrNull true
                            }
                        }
                    }
                    if (ended != null) return@awaitEachGesture
                    haptics.performHapticFeedback(HapticFeedbackType.LongPress)
                    val anchor = coords.value?.takeIf { it.isAttached }?.localToWindow(down.position)
                    linkMenu.open(hit, anchor)
                    do {
                        val event = awaitPointerEvent(PointerEventPass.Initial)
                        event.changes.forEach { it.consume() }
                    } while (event.changes.any { it.pressed })
                } finally {
                    linkMenu.endPress(token)
                }
            }
        }
}

/** Markdown text/paragraph/heading block with long-press link menu support via mikepenz's [MarkdownText]. */
@Composable
internal fun LinkAwareMarkdownText(
    content: String,
    node: ASTNode,
    style: TextStyle,
    modifier: Modifier = Modifier,
    contentChildType: IElementType? = null,
    isHeading: Boolean = false,
) {
    val targetNode = contentChildType?.let { node.findChildOfType(it) } ?: node
    val settings = annotatorSettings()
    val annotated = buildAnnotatedString {
        pushStyle(style.toSpanStyle())
        if (targetNode.children.isEmpty()) append(content.substring(targetNode.startOffset, targetNode.endOffset))
        else buildMarkdownAnnotatedString(content, targetNode, settings)
        pop()
    }
    val extendedSpans = LocalMarkdownExtendedSpans.current.extendedSpans?.invoke()
    val layout = remember { mutableStateOf<TextLayoutResult?>(null) }
    val headingModifier = if (isHeading) modifier.semantics { heading() } else modifier
    MarkdownText(
        content = annotated,
        node = targetNode,
        modifier = headingModifier.chatLinkLongPress(annotated) { layout.value },
        style = style,
        onTextLayout = { result, color ->
            layout.value = result
            extendedSpans?.onTextLayout(result, color)
        },
        sourceContent = content,
        extendedSpans = extendedSpans,
    )
}
