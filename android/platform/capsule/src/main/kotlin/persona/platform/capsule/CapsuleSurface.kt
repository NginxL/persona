package persona.platform.capsule

import androidx.compose.animation.core.Animatable
import androidx.compose.animation.core.CubicBezierEasing
import androidx.compose.animation.core.tween
import androidx.compose.foundation.ScrollState
import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.text.BasicTextField
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.Text
import androidx.compose.runtime.*
import androidx.compose.runtime.snapshots.SnapshotStateMap
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.draw.shadow
import androidx.compose.ui.graphics.TransformOrigin
import androidx.compose.ui.graphics.graphicsLayer
import androidx.compose.ui.focus.FocusRequester
import androidx.compose.ui.focus.focusRequester
import androidx.compose.ui.graphics.SolidColor
import androidx.compose.ui.input.key.*
import androidx.compose.ui.layout.LayoutCoordinates
import androidx.compose.ui.layout.onGloballyPositioned
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.platform.LocalFocusManager
import androidx.compose.ui.platform.LocalSoftwareKeyboardController
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.semantics.contentDescription
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.text.style.TextOverflow
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.Job
import kotlinx.coroutines.delay
import kotlinx.coroutines.flow.collect
import kotlinx.coroutines.launch
import persona.core.designsystem.*
import persona.core.designsystem.icons.PhIcons

/** The prototype's ease-out, cubic-bezier(.2, .8, .2, 1): quick to move, soft to land. */
private val CapsuleEasing = CubicBezierEasing(.2f, .8f, .2f, 1f)

private enum class CapsuleMenu { Switch, More, Target, Context }
private data class CapsuleSendConfirmation(val id: String?, val key: Pair<String?, String?>,
    val text: String, val target: String?, val recipient: String)
private class CapsuleScrollPosition {
    val scroll = ScrollState(0)
    var positioned = false
    var following = true
    var programmatic = false
}

/** Content providers own all receipts, navigation and business actions, including explicit Next. */
@Composable
fun CapsuleSurface(
    content: CapsuleContent,
    expanded: Boolean,
    onExpand: () -> Unit,
    onCollapse: () -> Unit,
    onSelect: (String?) -> Unit,
    onSelectSection: (String) -> Unit,
    onOpen: (String?) -> Unit,
    onSend: suspend (String?, String) -> Unit,
    onAction: suspend (String?, String) -> Unit,
    onViewed: suspend (String?, String, String) -> Boolean,
    windowFocused: Boolean,
    drafts: SnapshotStateMap<Pair<String?, String?>, String>,
    onDraftChange: (String?, String) -> Unit = { _, _ -> },
    fullScreen: Boolean = false,
    extraContent: @Composable (CapsuleEntry?) -> Unit = {},
    onClose: () -> Unit = {},
) {
    val palette = PersonaTheme.palette
    val type = PersonaTheme.type
    val selected = content.selected
    val shape = if (expanded) PersonaTheme.shapes.panelShape else RoundedCornerShape(CapsuleTokens.compactRadius)
    val scope = rememberCoroutineScope()
    var operation by remember { mutableStateOf(false) }
    var failure by remember(content.sectionId, content.selectedId) { mutableStateOf<String?>(null) }
    var menu by remember(content.sectionId, content.selectedId, expanded) { mutableStateOf<CapsuleMenu?>(null) }
    var confirmation by remember(content.sectionId, content.selectedId, content.replyTargetId, expanded) {
        mutableStateOf<CapsuleSendConfirmation?>(null)
    }
    var nextJob by remember { mutableStateOf<Job?>(null) }
    val pending = content.busy || operation
    val conversationKey = content.sectionId to content.selectedId
    // Preserve legacy keys for providers without explicit reply targets.
    val draftKey = (content.replyTargetId?.let { "${content.sectionId.orEmpty()}:target:${it.length}:$it" }
        ?: content.sectionId) to content.selectedId
    val text = content.draftText ?: drafts[draftKey].orEmpty()
    val currentContent by rememberUpdatedState(content)
    val currentText by rememberUpdatedState(text)
    val inputFocus = remember { FocusRequester() }
    val focusManager = LocalFocusManager.current
    val keyboard = LocalSoftwareKeyboardController.current
    var focusedDraft by remember(expanded) { mutableStateOf<Pair<String?, String?>?>(null) }
    val editable = expanded && !content.listing && content.canSend
    LaunchedEffect(editable, draftKey, windowFocused, pending) {
        if (!editable) {
            focusedDraft = null
            focusManager.clearFocus(force = true)
            keyboard?.hide()
        } else if (windowFocused && !pending && focusedDraft != draftKey) {
            inputFocus.requestFocus()
            keyboard?.show()
            focusedDraft = draftKey
        }
    }
    fun cancelNext() { nextJob?.cancel(); nextJob = null }
    DisposableEffect(expanded, conversationKey) { onDispose { cancelNext() } }
    fun closeOverlay() { menu = null; confirmation = null }
    fun showMenu(value: CapsuleMenu) { cancelNext(); menu = value }
    val failed = stringResource(R.string.capsule_failure)
    fun run(block: suspend () -> Unit) {
        scope.launch {
            operation = true
            failure = null
            try { block() }
            catch (cancelled: CancellationException) { throw cancelled }
            catch (error: Exception) { failure = error.message ?: failed }
            finally { operation = false }
        }
    }
    fun send(captured: CapsuleSendConfirmation) {
        run {
            onSend(captured.id, captured.text)
            if (currentContent.selectedId == captured.id && currentContent.replyTargetId == captured.target &&
                currentText == captured.text) {
                drafts[captured.key] = ""
                onDraftChange(captured.id, "")
            } else if (drafts[captured.key] == captured.text) drafts[captured.key] = ""
        }
    }
    val positions = remember { mutableMapOf<Pair<String?, String?>, CapsuleScrollPosition>() }
    val scrollKey = if (content.selectedId != null) null to content.selectedId else conversationKey
    val position = positions.getOrPut(scrollKey) { CapsuleScrollPosition() }
    val messageScroll = position.scroll
    val bottomTolerance = with(LocalDensity.current) { CapsuleTokens.followBottomTolerance.roundToPx() }
    LaunchedEffect(position, bottomTolerance) {
        var previousValue = messageScroll.value
        var previousMaximum = messageScroll.maxValue
        snapshotFlow { messageScroll.value to messageScroll.maxValue }.collect { (value, maximum) ->
            if (!position.programmatic && value != previousValue && maximum == previousMaximum) {
                position.following = value >= maximum - bottomTolerance
            }
            previousValue = value; previousMaximum = maximum
        }
    }
    LaunchedEffect(expanded, content.listing, conversationKey, content.messages, messageScroll.maxValue) {
        if (expanded && !content.listing && content.messages.isNotEmpty() && (!position.positioned || position.following)) {
            withFrameNanos { }; withFrameNanos { }
            if (!messageScroll.isScrollInProgress) {
                position.programmatic = true
                try { messageScroll.scrollTo(messageScroll.maxValue) } finally { position.programmatic = false }
                position.positioned = true
            }
        }
    }
    val latestReply = content.messages.lastOrNull { !it.fromUser }?.id
    var replyCoordinates by remember(conversationKey, latestReply) { mutableStateOf<LayoutCoordinates?>(null) }
    var viewportCoordinates by remember(conversationKey) { mutableStateOf<LayoutCoordinates?>(null) }
    var replyVisible by remember(conversationKey, latestReply) { mutableStateOf(false) }
    fun refreshVisibility() { replyVisible = capsuleReplyVisible(replyCoordinates, viewportCoordinates) }
    var viewedReply by remember(conversationKey) { mutableStateOf<Pair<String, String>?>(null) }
    var seenReply by remember(conversationKey) { mutableStateOf<String?>(null) }
    val readVersion = content.readVersion
    LaunchedEffect(expanded, content.listing, latestReply, replyVisible, conversationKey, readVersion,
        menu, confirmation, messageScroll.isScrollInProgress, windowFocused) {
        if (expanded && windowFocused && !content.listing && latestReply != null && readVersion != null && replyVisible &&
            menu == null && confirmation == null && !messageScroll.isScrollInProgress && viewedReply != (latestReply to readVersion)) {
            delay(CapsuleTokens.readDelayMillis)
            // The dots go as soon as the reply has been seen; a receipt that fails brings them back.
            seenReply = latestReply
            try {
                if (onViewed(content.selectedId, latestReply, readVersion)) viewedReply = latestReply to readVersion
                else if (seenReply == latestReply) seenReply = null
            } catch (cancelled: CancellationException) { if (seenReply == latestReply) seenReply = null; throw cancelled }
            catch (error: Exception) { if (seenReply == latestReply) seenReply = null; failure = error.message ?: failed }
        }
    }
    LaunchedEffect(replyVisible, messageScroll.isScrollInProgress, windowFocused) {
        if (!windowFocused || !replyVisible || messageScroll.isScrollInProgress) cancelNext()
    }
    val openDescription = stringResource(if (content.badgeCount > 0) R.string.capsule_open_new else R.string.capsule_open)
    // Expanding grows the card out of the capsule's corner and fades it in; collapsing pops the capsule back (V7).
    val reduceMotion = LocalReduceMotion.current
    val motion = PersonaTheme.motion
    val reveal = remember(expanded) { Animatable(if (reduceMotion) 1f else 0f) }
    LaunchedEffect(expanded, reduceMotion) {
        if (reduceMotion) reveal.snapTo(1f)
        else reveal.animateTo(1f, tween(if (expanded) motion.standard else motion.quick, easing = CapsuleEasing))
    }
    val from = if (expanded) CapsuleTokens.revealScale else CapsuleTokens.collapseScale
    Box(Modifier.graphicsLayer {
            val t = reveal.value
            scaleX = from + (1f - from) * t; scaleY = scaleX
            // Starts partly visible: the window swaps capsule and card at once, so a fade from nothing reads as a blink.
            alpha = CapsuleTokens.revealAlpha + (1f - CapsuleTokens.revealAlpha) * t
            transformOrigin = TransformOrigin(.5f, 0f)
        }.shadow(CapsuleTokens.elevation, shape).clip(shape).background(palette.surfaceOpaque)
        .border(CapsuleTokens.border, palette.line, shape)
        .then(if (expanded && fullScreen) Modifier.fillMaxSize()
            else if (expanded) Modifier.width(CapsuleTokens.cardWidth).height(CapsuleTokens.cardMaxHeight)
            else Modifier.size(CapsuleTokens.compactWidth, CapsuleTokens.orb))
        .onPreviewKeyEvent { if (it.type == KeyEventType.KeyDown && it.key == Key.Escape && (menu != null || confirmation != null)) {
            closeOverlay(); true
        } else false }.testTag(if (expanded) "capsule.card" else "capsule.orb")) {
        if (!expanded) Box(Modifier.fillMaxSize().clickable(onClick = onExpand).semantics { contentDescription = openDescription }) {
            Box(Modifier.align(Alignment.Center)) {
                CompositionLocalProvider(LocalReduceMotion provides true) { PersonaFace(PersonaState.Idle, size = CapsuleTokens.face) }
            }
            if (content.badgeCount > 0) Box(Modifier.align(Alignment.TopEnd).padding(CapsuleTokens.badgeInset)) {
                CapsuleUnreadDot("capsule.badge")
            }
        } else Column(Modifier.fillMaxSize()) {
            Row(Modifier.fillMaxWidth().height(CapsuleTokens.headerHeight).padding(horizontal = CapsuleTokens.smallGap),
                verticalAlignment = Alignment.CenterVertically) {
                if (!content.listing && content.section?.list == true) CapsuleControl(stringResource(R.string.capsule_back),
                    "capsule.back", { cancelNext(); onSelect(null) }, icon = PhIcons.ArrowLeft, iconOnly = true)
                CapsuleControl(if (content.listing) content.section?.title.orEmpty() else selected?.title ?: content.section?.title ?: "Persona",
                    "capsule.switch", {
                        // The grouped unread list is the one place to switch from; the old flat menu stays only for
                        // providers without one.
                        val list = content.sections.firstOrNull { it.list }?.id
                        if (list != null) { cancelNext(); onSelectSection(list) } else showMenu(CapsuleMenu.Switch)
                    }, Modifier.weight(1f), icon = PhIcons.CaretDown,
                    style = CapsuleControlStyle.Title)
                if (!content.listing) CapsuleControl(stringResource(when {
                    content.nextBusy -> R.string.capsule_syncing
                    content.nextError != null -> R.string.capsule_retry
                    !content.nextAvailable -> R.string.capsule_all_read
                    else -> R.string.capsule_next
                }), "capsule.next", {
                    cancelNext()
                    nextJob = scope.launch {
                        try { onAction(content.selectedId, "next") }
                        catch (cancelled: CancellationException) { throw cancelled }
                        catch (error: Exception) { failure = error.message ?: failed }
                    }
                }, enabled = !content.nextBusy && (content.nextAvailable || content.nextError != null),
                    description = stringResource(if (!content.nextAvailable && content.nextError == null && !content.nextBusy) R.string.capsule_no_more else R.string.capsule_next),
                    style = if (content.nextAvailable || content.nextError != null) CapsuleControlStyle.Tinted else CapsuleControlStyle.Plain)
                CapsuleControl(stringResource(if (fullScreen) R.string.capsule_return_capsule else R.string.capsule_expand), "capsule.open", { cancelNext(); run { onOpen(content.selectedId) } },
                    icon = PhIcons.ArrowSquareOut)
                CapsuleControl(stringResource(R.string.capsule_more), "capsule.more", { showMenu(CapsuleMenu.More) },
                    icon = PhIcons.DotsThree, iconOnly = true)
                if (!fullScreen) CapsuleControl(stringResource(R.string.capsule_collapse), "capsule.collapse.bar",
                    { closeOverlay(); cancelNext(); onCollapse() }, icon = PhIcons.Minus, iconOnly = true)
            }
            Box(Modifier.fillMaxWidth().height(CapsuleTokens.divider).background(palette.line))
            if (content.listing) {
                Row(Modifier.fillMaxWidth().padding(horizontal = CapsuleTokens.gap), horizontalArrangement = Arrangement.spacedBy(CapsuleTokens.smallGap)) {
                    content.sections.forEach { section ->
                        JChip(section.title + if (section.count > 0) " ${section.count}" else "", Modifier.weight(1f).testTag("capsule.section.${section.id}"),
                            selected = section.id == content.sectionId, onClick = { cancelNext(); onSelectSection(section.id) })
                    }
                }
                Box(Modifier.weight(1f)) { CapsuleInboxList(content, pending, { entry ->
                    cancelNext(); run { if (entry.detailOnly) onOpen(entry.id) else onSelect(entry.id) }
                }, { run { onAction(null, "read-fyi") } }, { group -> run { onAction(null, "read-group:$group") } }) }
                failure?.let { Text(it, Modifier.padding(CapsuleTokens.gap), style = type.caption, color = palette.danger.text) }
            } else {
                (content.nextError ?: content.error ?: failure)?.let { error ->
                    Row(Modifier.fillMaxWidth().background(palette.danger.tint).padding(horizontal = CapsuleTokens.gap),
                        verticalAlignment = Alignment.CenterVertically) {
                        Text(error, Modifier.weight(1f), style = type.micro, color = palette.danger.text, maxLines = 2,
                            overflow = TextOverflow.Ellipsis)
                        if (content.nextError != null) CapsuleControl(stringResource(R.string.capsule_retry), "capsule.read.retry",
                            { cancelNext(); nextJob = scope.launch { onAction(content.selectedId, "next") } }, enabled = !content.nextBusy)
                    }
                }
                Column(Modifier.weight(1f).fillMaxWidth().onGloballyPositioned { viewportCoordinates = it; refreshVisibility() }
                    .verticalScroll(messageScroll).padding(CapsuleTokens.gap).testTag("capsule.messages"),
                    verticalArrangement = Arrangement.spacedBy(CapsuleTokens.gap)) {
                    extraContent(selected)
                    val seen = seenReply != null && seenReply == latestReply
                    content.messages.forEach { shown ->
                        val message = if (seen && shown.unread) shown.copy(unread = false) else shown
                        CapsuleMessageBubble(message, Modifier.testTag("capsule.message.${message.id}"),
                            onTextPositioned = if (message.id == latestReply) { coordinates -> replyCoordinates = coordinates; refreshVisibility() } else null)
                    }
                    if (content.messages.isEmpty()) Text(stringResource(if (content.busy) R.string.capsule_loading else R.string.capsule_empty),
                        style = type.callout, color = palette.ink2)
                }
                if (content.canSend) Row(Modifier.fillMaxWidth().height(CapsuleTokens.composerHeight)
                    .background(palette.surfaceOpaque).padding(horizontal = CapsuleTokens.gap),
                    verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(CapsuleTokens.smallGap)) {
                    if (content.replyTargets.isNotEmpty()) CapsuleControl(content.replyTargets.firstOrNull { it.id == content.replyTargetId }?.title.orEmpty(),
                        "capsule.target", { showMenu(CapsuleMenu.Target) }, Modifier.widthIn(max = CapsuleTokens.targetMaxWidth), icon = PhIcons.CaretDown)
                    val placeholder = when {
                        content.replyTargetId == "persona" -> stringResource(R.string.capsule_ask_persona)
                        content.recipient != null -> stringResource(R.string.capsule_reply_to, content.recipient)
                        else -> stringResource(if (selected?.state == CapsuleState.Working) R.string.capsule_placeholder_working else R.string.capsule_placeholder)
                    }
                    ReadingContent {
                        BasicTextField(text, { value -> drafts[draftKey] = value; onDraftChange(content.selectedId, value) },
                            Modifier.weight(1f).heightIn(max = CapsuleTokens.inputMaxHeight).focusRequester(inputFocus).testTag("capsule.input"),
                            textStyle = type.callout.copy(color = palette.ink), cursorBrush = SolidColor(palette.accent),
                            maxLines = 2, enabled = !pending, decorationBox = { field ->
                                Box(Modifier.fillMaxWidth().clip(PersonaTheme.shapes.controlShape).background(palette.sunken)
                                    .border(CapsuleTokens.border, if (text.isNotEmpty()) palette.accent else palette.line, PersonaTheme.shapes.controlShape)
                                    .padding(CapsuleTokens.gap)) {
                                    if (text.isEmpty()) InterfaceContent {
                                        Text(placeholder, style = type.callout, color = palette.ink3, maxLines = 1, overflow = TextOverflow.Ellipsis)
                                    }
                                    field()
                                }
                            })
                    }
                    CapsuleSendButton(stringResource(R.string.capsule_send), enabled = text.isNotBlank() && !pending) {
                        cancelNext()
                        val captured = CapsuleSendConfirmation(content.selectedId, draftKey, text, content.replyTargetId, content.recipient.orEmpty())
                        if (content.confirmSend) confirmation = captured else send(captured)
                    }
                }
            }
        }
        if (expanded && menu != null) CapsuleOverlay(stringResource(when (menu) {
            CapsuleMenu.Switch -> R.string.capsule_switch
            CapsuleMenu.Target -> R.string.capsule_target
            CapsuleMenu.Context -> R.string.capsule_context
            else -> R.string.capsule_more
        }), { closeOverlay() }) {
            when (menu) {
                CapsuleMenu.Switch -> {
                    content.sections.forEach { section -> CapsuleControl(section.title + if (section.count > 0) " ${section.count}" else "",
                        "capsule.section.${section.id}", { closeOverlay(); onSelectSection(section.id) }, Modifier.fillMaxWidth()) }
                    (content.unreadEntries + content.entries).distinctBy { it.id }.forEach { entry ->
                        CapsuleControl(entry.title, "capsule.choose.${entry.id}", { closeOverlay();
                            run { if (entry.detailOnly) onOpen(entry.id) else onSelect(entry.id) } }, Modifier.fillMaxWidth())
                    }
                }
                CapsuleMenu.Target -> content.replyTargets.forEach { target -> CapsuleControl(target.title, "capsule.target.${target.id}",
                    { closeOverlay(); run { onAction(content.selectedId, "target:${target.id}") } }, Modifier.fillMaxWidth()) }
                CapsuleMenu.Context -> content.context?.let {
                    ReadingContent { Text(it, Modifier.testTag("capsule.context.body").padding(CapsuleTokens.gap), style = type.callout, color = palette.ink) }
                } ?: Text(stringResource(R.string.capsule_loading), style = type.caption, color = palette.ink2)
                CapsuleMenu.More -> {
                    if (!content.listing) {
                        CapsuleControl(stringResource(if (content.canLoadOlder || content.context != null) R.string.capsule_context else R.string.capsule_open_chat),
                            "capsule.context", { if (content.canLoadOlder || content.context != null) { menu = CapsuleMenu.Context; run { onAction(content.selectedId, "context") } }
                            else { closeOverlay(); run { onOpen(content.selectedId) } } }, Modifier.fillMaxWidth())
                        selected?.actions?.forEach { action -> CapsuleControl(action.label, "capsule.action.${action.id}",
                            { closeOverlay(); run { onAction(content.selectedId, action.id) } }, Modifier.fillMaxWidth(), enabled = !pending) }
                        CapsuleControl(stringResource(R.string.capsule_hide_keyboard), "capsule.keyboard.hide", {
                            closeOverlay(); keyboard?.hide(); focusManager.clearFocus()
                        }, Modifier.fillMaxWidth(), icon = PhIcons.Keyboard)
                    }
                    CapsuleControl(stringResource(R.string.capsule_collapse), "capsule.collapse", {
                        closeOverlay(); cancelNext(); onCollapse()
                    }, Modifier.fillMaxWidth())
                    CapsuleControl(stringResource(R.string.capsule_turn_off), "capsule.close", {
                        closeOverlay(); cancelNext(); onClose()
                    }, Modifier.fillMaxWidth(), description = stringResource(R.string.capsule_turn_off_hint))
                }
                null -> Unit
            }
        }
        confirmation?.let { captured -> CapsuleOverlay(stringResource(R.string.capsule_confirm_send), { confirmation = null }) {
            Text(stringResource(R.string.capsule_recipient, captured.recipient), style = type.label, color = palette.ink)
            ReadingContent {
                Text(captured.text, Modifier.padding(vertical = CapsuleTokens.gap).testTag("capsule.confirm.text"), style = type.callout, color = palette.ink)
            }
            CapsuleControl(stringResource(R.string.capsule_confirm_send), "capsule.confirm.send", { confirmation = null; send(captured) },
                Modifier.fillMaxWidth(), enabled = !pending)
        } }
    }
}
