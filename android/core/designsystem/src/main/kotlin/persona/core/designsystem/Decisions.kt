package persona.core.designsystem

import androidx.compose.runtime.Composable
import androidx.compose.ui.res.stringResource
import androidx.compose.foundation.gestures.detectTapGestures
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.verticalScroll
import androidx.compose.ui.draw.drawWithContent
import androidx.compose.ui.graphics.BlendMode
import androidx.compose.ui.graphics.CompositingStrategy
import androidx.compose.ui.graphics.graphicsLayer
import androidx.compose.ui.input.pointer.pointerInput
import androidx.compose.ui.layout.onSizeChanged
import androidx.compose.ui.res.pluralStringResource
import androidx.annotation.StringRes
import androidx.compose.animation.animateContentSize
import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material3.Text
import androidx.compose.runtime.*
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.draw.drawBehind
import androidx.compose.ui.draw.shadow
import androidx.compose.ui.geometry.CornerRadius
import androidx.compose.ui.geometry.Offset
import androidx.compose.ui.geometry.Size
import androidx.compose.ui.graphics.Brush
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.semantics.Role
import androidx.compose.ui.semantics.contentDescription
import androidx.compose.ui.semantics.heading
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.semantics.selected
import androidx.compose.ui.semantics.toggleableState
import androidx.compose.ui.state.ToggleableState
import androidx.compose.ui.text.AnnotatedString
import androidx.compose.ui.text.TextLayoutResult
import androidx.compose.ui.text.buildAnnotatedString
import androidx.compose.ui.text.withStyle
import androidx.compose.ui.text.TextStyle
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.text.rememberTextMeasurer
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.unit.Dp
import androidx.compose.ui.unit.dp
import persona.core.designsystem.icons.PhIcon
import persona.core.designsystem.icons.PhIcons

/** What a request asks of the user. */
enum class DecisionKind(@StringRes val labelRes: Int, val icon: PhIcon) {
    Command(R.string.designsystem_decision_command, PhIcons.TerminalWindow),
    Tool(R.string.designsystem_decision_tool, PhIcons.Wrench),
    Plan(R.string.designsystem_decision_plan, PhIcons.ListChecks),
    Question(R.string.designsystem_decision_question, PhIcons.SealQuestion),
    Form(R.string.designsystem_decision_form, PhIcons.NotePencil),
    Screen(R.string.designsystem_decision_screen, PhIcons.TerminalWindow),
    Other(R.string.designsystem_decision_other, PhIcons.Hand);

    val label: String @Composable get() = stringResource(labelRes)
}

/**
 * Something that needs the user, in a list (the Agents page). Glass shows a frosted card with a thin amber edge;
 * brief an ink-on-neutral card; the board a neutral strip with a thin amber edge that expands in
 * place — collapsed it shows [summary] and [quickActions], tapping it opens the [body] and [actions].
 * [onOpen] opens the conversation (glass and brief: tapping the card; board: the "Open" action).
 * [note] says why the quick actions cannot be used (the hub is out of reach, the computer must approve): the board shows it
 * above them while collapsed, where the [body] that says it otherwise is not drawn (#2483).
 */
@Composable
fun DecisionCard(
    kind: DecisionKind,
    title: String,
    modifier: Modifier = Modifier,
    headline: String? = null,
    source: String? = null,
    expires: String? = null,
    summary: String? = null,
    advice: String? = null,
    note: String? = null,
    expandedByDefault: Boolean = true,
    onOpen: (() -> Unit)? = null,
    quickActions: (@Composable RowScope.() -> Unit)? = null,
    body: @Composable ColumnScope.() -> Unit = {},
    actions: (@Composable ColumnScope.() -> Unit)? = null,
) {
    val palette = PersonaTheme.palette
    val type = PersonaTheme.type
    val label = headline ?: kind.label
    when (PersonaTheme.style) {
        AppStyle.Glass -> JCard(modifier, onClick = onOpen, status = Status.NeedsYou) {
            Row(verticalAlignment = Alignment.CenterVertically) {
                // still: iOS's card leads with its kind's icon in Needs You's color (`InteractionPreviewCard`)
                JStatusDot(Status.NeedsYou, size = 8.dp, breathes = false)
                Spacer(Modifier.width(8.dp.dense))
                Text(label, style = type.caption, color = palette.needsYou.text, maxLines = 1, overflow = TextOverflow.Ellipsis, modifier = Modifier.weight(1f))
                expires?.let { Expiry(it) }
            }
            Spacer(Modifier.height(6.dp.dense))
            Text(title, style = type.title, color = palette.ink, maxLines = 2, overflow = TextOverflow.Ellipsis)
            source?.let { Text(it, style = type.caption.copy(fontWeight = FontWeight.Normal), color = palette.ink3, maxLines = 1, overflow = TextOverflow.Ellipsis) }
            Spacer(Modifier.height(12.dp.dense))
            body()
            advice?.let { Spacer(Modifier.height(12.dp.dense)); DecisionAdvice(it) }
            actions?.let { Spacer(Modifier.height(16.dp.dense)); it() }
        }
        AppStyle.Brief -> JCard(modifier, onClick = onOpen) {
            Row(verticalAlignment = Alignment.CenterVertically) {
                JIcon(kind.icon, null, size = 18.dp, tint = palette.ink2)
                Spacer(Modifier.width(8.dp.dense))
                Text(label, style = type.caption, color = palette.ink2, maxLines = 1, overflow = TextOverflow.Ellipsis, modifier = Modifier.weight(1f))
                expires?.let { Text(it, style = type.micro, color = palette.ink3, maxLines = 1) }
            }
            Spacer(Modifier.height(10.dp.dense))
            Text(title, style = type.title, color = palette.ink, maxLines = 2, overflow = TextOverflow.Ellipsis)
            source?.let { Spacer(Modifier.height(2.dp.dense)); Provenance(it) }
            Spacer(Modifier.height(12.dp.dense))
            body()
            advice?.let { Spacer(Modifier.height(12.dp.dense)); DecisionAdvice(it) }
            actions?.let { Spacer(Modifier.height(16.dp.dense)); it() }
        }
        AppStyle.Board -> {
            var open by rememberSaveable(title, label) { mutableStateOf(expandedByDefault) }
            val shape = RoundedCornerShape(10.dp.corner)
            Row(
                modifier.fillMaxWidth().clip(shape).background(palette.surface).holder(palette.needsYou.dot, Tokens.Space.xxs).border(1.dp, palette.line, shape)
                    .clickable(role = Role.Button) { open = !open }.animateContentSize(PersonaTheme.motion.size()),
            ) {
                Box(Modifier.width(36.dp).padding(top = 14.dp.dense), contentAlignment = Alignment.TopCenter) {
                    JIcon(PhIcons.Hand, null, size = 18.dp, tint = palette.needsYou.text, selected = true)
                }
                Column(Modifier.weight(1f).padding(start = 14.dp.dense, end = 14.dp.dense, top = 12.dp.dense, bottom = 12.dp.dense)) {
                    Row(verticalAlignment = Alignment.Top) {
                        Column(Modifier.weight(1f)) {
                            Text(title, style = type.headline, color = palette.ink, maxLines = 1, overflow = TextOverflow.Ellipsis)
                            Text(label, style = type.callout, color = palette.ink2, maxLines = 1, overflow = TextOverflow.Ellipsis)
                        }
                        expires?.let { Spacer(Modifier.width(10.dp.dense)); Text(it, style = type.micro, color = palette.ink, maxLines = 1) }
                    }
                    source?.let { Spacer(Modifier.height(8.dp.dense)); Fields(it.split(" / ")) }
                    if (open) {
                        Spacer(Modifier.height(12.dp.dense))
                        JDivider()
                        Spacer(Modifier.height(12.dp.dense))
                        body()
                        advice?.let { Spacer(Modifier.height(10.dp.dense)); DecisionAdvice(it) }
                        actions?.let { Spacer(Modifier.height(12.dp.dense)); it() }
                        onOpen?.let { Spacer(Modifier.height(4.dp.dense)); JTextButton(stringResource(R.string.designsystem_decision_open_chat), it, icon = PhIcons.ArrowSquareOut) }
                    } else {
                        summary?.let { Spacer(Modifier.height(8.dp.dense)); Text(it, style = type.code, color = palette.ink2, maxLines = 1, overflow = TextOverflow.Ellipsis) }
                        note?.let { Spacer(Modifier.height(Tokens.Space.s)); Text(it, style = type.caption, color = palette.cautionText) }
                        quickActions?.let { Spacer(Modifier.height(10.dp.dense)); Row(horizontalArrangement = Arrangement.spacedBy(6.dp.dense), content = it) }
                    }
                }
            }
        }
    }
}

@Composable
private fun Expiry(text: String) {
    val palette = PersonaTheme.palette
    JIcon(PhIcons.Timer, null, size = 14.dp, tint = palette.ink3)
    Spacer(Modifier.width(4.dp.dense))
    Text(text, style = PersonaTheme.type.micro, color = palette.ink3, maxLines = 1)
}

/** Machine and folder as two quiet facts (brief). The source string is "machine / folder". */
@Composable
private fun Provenance(source: String) {
    val palette = PersonaTheme.palette
    val parts = source.split(" / ")
    Row(verticalAlignment = Alignment.CenterVertically) {
        parts.forEachIndexed { index, part ->
            if (index > 0) Spacer(Modifier.width(14.dp.dense))
            JIcon(if (index == 0) PhIcons.Laptop else PhIcons.FolderSimple, null, size = 15.dp, tint = palette.ink3)
            Spacer(Modifier.width(5.dp.dense))
            Text(part, style = PersonaTheme.type.caption.copy(fontWeight = FontWeight.Normal), color = palette.ink3, maxLines = 1, overflow = TextOverflow.Ellipsis)
        }
    }
}

/** Fixed fields separated by short rules, read left to right like a printed strip (board). */
@Composable
fun Fields(values: List<String>, modifier: Modifier = Modifier) {
    val palette = PersonaTheme.palette
    Row(modifier, verticalAlignment = Alignment.CenterVertically) {
        values.filter { it.isNotBlank() }.forEachIndexed { index, value ->
            if (index > 0) {
                Spacer(Modifier.width(10.dp.dense))
                Box(Modifier.size(1.dp, 12.dp).background(palette.line))
                Spacer(Modifier.width(10.dp.dense))
            }
            Text(value, style = PersonaTheme.type.micro.copy(fontSize = androidx.compose.ui.unit.TextUnit(14f, androidx.compose.ui.unit.TextUnitType.Sp)), color = palette.ink2, maxLines = 1, overflow = TextOverflow.Ellipsis, modifier = Modifier.weight(1f, fill = false))
        }
    }
}

/**
 * One pending request above a conversation's composer, a card of [DecisionStack] (iOS `InteractionPanel`): a floating glass
 * card inset like the composer, a rounded paper card with a light shadow (brief), or a bordered card under an amber rule
 * (board). With [onCollapsedChange] a tap anywhere on its header folds it to that line, [preview] after the headline, and
 * opens it again; the caret is the control TalkBack reads.
 */
@Composable
fun DecisionDock(
    kind: DecisionKind,
    headline: String,
    modifier: Modifier = Modifier,
    expires: String? = null,
    collapsed: Boolean = false,
    onCollapsedChange: ((Boolean) -> Unit)? = null,
    /** What the card asks in one line, after the headline while it is folded (iOS `previewText`). */
    preview: String? = null,
    onExpand: (() -> Unit)? = null,
    headerActions: (@Composable () -> Unit)? = null,
    /** One line over the headline saying what the request waits for ("Waiting for your approval", #4883), as the web's
     *  and iOS's cards do; null draws the dock as before. Folded, the card is its header's line alone. */
    kicker: String? = null,
    /** Optional top-edge grabber for resizing a lone unfolded pending card (#12306). */
    grabber: (@Composable () -> Unit)? = null,
    content: @Composable ColumnScope.() -> Unit,
) {
    val palette = PersonaTheme.palette
    val type = PersonaTheme.type
    val style = PersonaTheme.style
    val fold by rememberUpdatedState(onCollapsedChange?.let { change -> { change(!collapsed) } })
    val folds = if (onCollapsedChange == null) Modifier else Modifier.pointerInput(Unit) { detectTapGestures { fold?.invoke() } }
    val header: @Composable () -> Unit = {
        Column(folds) {
            if (!collapsed) kicker?.let { Text(it, style = type.caption, color = palette.ink2, maxLines = 1, overflow = TextOverflow.Ellipsis) }
            Row(verticalAlignment = Alignment.CenterVertically) {
                when (style) {
                    AppStyle.Glass -> JIcon(kind.icon, null, size = Tokens.Size.badge, tint = palette.needsYou.dot, selected = true)
                    AppStyle.Brief -> JIcon(kind.icon, null, size = 18.dp, tint = palette.ink2)
                    AppStyle.Board -> JIcon(PhIcons.Hand, null, size = 18.dp, tint = palette.needsYou.dot, selected = true)
                }
                Spacer(Modifier.width(8.dp.dense))
                // Folded, what it asks follows the title on the same line and gives way first (iOS `previewText`).
                val previewShown = collapsed && !preview.isNullOrBlank()
                val ink = when (style) {
                    AppStyle.Glass -> palette.needsYou.text
                    AppStyle.Brief -> palette.ink2
                    AppStyle.Board -> palette.ink
                }
                Text(
                    if (!previewShown) AnnotatedString(headline) else buildAnnotatedString {
                        append(headline)
                        append("  ")
                        withStyle(type.callout.toSpanStyle().copy(color = palette.ink)) { append(preview.orEmpty()) }
                    },
                    style = if (style == AppStyle.Brief) type.caption else type.label, maxLines = 1, overflow = TextOverflow.Ellipsis,
                    modifier = Modifier.weight(1f).semantics { heading() }, color = ink,
                )
                expires?.let { Text(it, style = type.micro, color = palette.ink3, maxLines = 1); Spacer(Modifier.width(4.dp.dense)) }
                if (!collapsed) onExpand?.let { JIconButton(PhIcons.ArrowSquareOut, stringResource(R.string.designsystem_decision_open_full), it, tint = palette.ink2) }
                onCollapsedChange?.let { change -> JIconButton(if (collapsed) PhIcons.CaretUp else PhIcons.CaretDown, stringResource(if (collapsed) R.string.designsystem_expand else R.string.designsystem_collapse), { change(!collapsed) }, tint = palette.ink2) }
                headerActions?.invoke()
            }
        }
    }
    val showGrabber = !collapsed && grabber != null
    val body: @Composable ColumnScope.() -> Unit = {
        if (showGrabber) grabber?.invoke()
        header()
        if (!collapsed) { Spacer(Modifier.height(10.dp.dense)); content() }
    }
    val shape = PersonaTheme.shapes.panelShape
    // Each style inset from the page's sides as its composer is.
    when (style) {
        AppStyle.Glass -> Column(
            modifier.fillMaxWidth().padding(horizontal = Tokens.Space.m)
                .floatingSurface(shape)
                .padding(start = 18.dp.dense, end = 10.dp.dense, top = if (showGrabber) Tokens.Space.xs else 10.dp.dense, bottom = 14.dp.dense)
                .animateContentSize(PersonaTheme.motion.size()),
        ) { Column(Modifier.padding(end = 8.dp.dense)) { body() } }
        AppStyle.Brief -> Column(
            modifier.fillMaxWidth().padding(horizontal = Tokens.Space.m)
                .shadow(HomeTokens.composerShadow, shape, ambientColor = Color.Black.copy(alpha = HomeTokens.composerShadowAlpha), spotColor = Color.Black.copy(alpha = HomeTokens.composerShadowAlpha))
                .clip(shape).background(palette.surface)
                .padding(start = 20.dp.dense, end = 12.dp.dense, top = if (showGrabber) Tokens.Space.xs else 16.dp.dense, bottom = 12.dp.dense)
                .animateContentSize(PersonaTheme.motion.size()),
        ) { Column(Modifier.padding(end = 8.dp.dense)) { body() } }
        AppStyle.Board -> Column(modifier.fillMaxWidth().padding(horizontal = Tokens.Space.m).clip(shape).background(palette.surface)
            .border(Tokens.Size.line, palette.line, shape).animateContentSize(PersonaTheme.motion.size())) {
            Box(Modifier.fillMaxWidth().height(Tokens.Size.bar).background(palette.needsYou.dot))
            Column(Modifier.padding(start = 16.dp.dense, end = 8.dp.dense, top = if (showGrabber) Tokens.Space.xs else 10.dp.dense, bottom = 10.dp.dense)) { Column(Modifier.padding(end = 8.dp.dense)) { body() } }
        }
    }
}

/**
 * A conversation's pending requests above its composer, the composer still usable (iOS `PendingCards`, `DESIGN_SYSTEM.md`
 * section 4.8): stacked [Tokens.Space.m] apart as they come while they fit in [maxHeight] (about half the page); past it,
 * or while the [keyboard] is up, they scroll on their own under "N pending" ([count]; not over a lone card with the
 * keyboard up), their edges fading, and [content] is told so (`true`) to put the most urgent first. A new card among
 * [keys] while they scroll brings the stack back to its top, where the most urgent is. A place that says its count some
 * other way (Persona's task question, "N more" over the card) passes `counted = false`: no "N pending" there.
 */
@Composable
fun DecisionStack(
    count: Int,
    maxHeight: Dp,
    modifier: Modifier = Modifier,
    keyboard: Boolean = false,
    keys: List<Any> = emptyList(),
    counted: Boolean = true,
    content: @Composable ColumnScope.(scrolling: Boolean) -> Unit,
) {
    val palette = PersonaTheme.palette
    var contentHeight by remember { mutableIntStateOf(0) }
    val cap = with(LocalDensity.current) { maxHeight.roundToPx() }
    val scrolling = keyboard || contentHeight > cap + 1
    val scroll = rememberScrollState()
    var seen by remember { mutableStateOf(keys.toSet()) }
    LaunchedEffect(keys) {
        val arrived = keys.any { it !in seen }
        seen = keys.toSet()
        if (arrived && scrolling) scroll.animateScrollTo(0)
    }
    // The count is inside [maxHeight] too: the stack never takes more than it was given.
    Column(modifier.fillMaxWidth().heightIn(max = maxHeight), verticalArrangement = Arrangement.spacedBy(Tokens.Space.s)) {
        if (counted && scrolling && !(keyboard && count == 1)) Text(pluralStringResource(R.plurals.designsystem_decision_pending, count, count),
            Modifier.padding(horizontal = Tokens.Space.m + Tokens.Space.xs).semantics { heading() },
            style = PersonaTheme.type.caption, color = palette.ink2, maxLines = 1, overflow = TextOverflow.Ellipsis)
        // Fitting, the cards are measured whole and nothing scrolls or clips; past the cap, what scrolls out fades at the edges.
        val area = if (scrolling) Modifier.edgeFade(palette.ink).verticalScroll(scroll)
            else Modifier.wrapContentHeight(Alignment.Top, unbounded = true)
        Box(Modifier.fillMaxWidth().weight(1f, fill = false).then(area)) {
            Column(Modifier.fillMaxWidth().padding(vertical = if (scrolling) Tokens.Space.s else Tokens.Space.flush)
                .onSizeChanged { contentHeight = it.height }, verticalArrangement = Arrangement.spacedBy(Tokens.Space.m)) { content(scrolling) }
        }
    }
}

/** Fades what scrolls out at the top and bottom: [ink] gives only the mask's opacity, never a color. */
private fun Modifier.edgeFade(ink: Color): Modifier = graphicsLayer { compositingStrategy = CompositingStrategy.Offscreen }
    .drawWithContent {
        drawContent()
        val edge = (Tokens.Space.s.toPx() / size.height.coerceAtLeast(1f)).coerceAtMost(0.5f)
        drawRect(Brush.verticalGradient(0f to ink.copy(alpha = 0f), edge to ink, 1f - edge to ink, 1f to ink.copy(alpha = 0f)), blendMode = BlendMode.DstIn)
    }

/** Persona's suggestion: ink on paper in Brief, a neutral well in Glass and Board. */
@Composable
fun DecisionAdvice(text: String, modifier: Modifier = Modifier) {
    val palette = PersonaTheme.palette
    val type = PersonaTheme.type
    when (PersonaTheme.style) {
        AppStyle.Brief -> Row(modifier, verticalAlignment = Alignment.Top) {
            JIcon(PhIcons.Sparkle, null, size = 16.dp, tint = palette.ink, selected = true, modifier = Modifier.padding(top = 3.dp.dense))
            Spacer(Modifier.width(4.dp.dense))
            Text(stringResource(R.string.designsystem_decision_advice, text), style = type.callout.copy(fontWeight = FontWeight.SemiBold), color = palette.ink)
        }
        else -> Row(
            modifier.fillMaxWidth().clip(RoundedCornerShape(if (PersonaTheme.style == AppStyle.Board) 6.dp.corner else 16.dp.corner)).background(palette.statusSurface)
                .padding(horizontal = 12.dp.dense, vertical = 10.dp.dense),
            verticalAlignment = Alignment.Top,
        ) {
            JIcon(PhIcons.Sparkle, null, size = 16.dp, tint = palette.accent, selected = true, modifier = Modifier.padding(top = 2.dp.dense))
            Spacer(Modifier.width(8.dp.dense))
            Text(text, style = type.callout.copy(fontWeight = FontWeight.Medium), color = palette.ink)
        }
    }
}

/** Text with a highlighter stroke behind each line, the way you would mark a paper briefing (brief). */
@Composable
fun MarkerText(text: String, style: TextStyle, modifier: Modifier = Modifier) {
    val palette = PersonaTheme.palette
    var layout by remember { mutableStateOf<TextLayoutResult?>(null) }
    Text(
        text, style = style.copy(color = palette.onMarker), onTextLayout = { layout = it },
        modifier = modifier.drawBehind {
            val result = layout ?: return@drawBehind
            val padX = 4.dp.toPx()
            for (line in 0 until result.lineCount) {
                val top = result.getLineTop(line)
                val height = result.getLineBottom(line) - top
                drawRoundRect(
                    palette.marker, topLeft = Offset(result.getLineLeft(line) - padX, top + height * 0.12f),
                    size = Size(result.getLineRight(line) - result.getLineLeft(line) + padX * 2, height * 0.80f),
                    cornerRadius = CornerRadius(3.dp.toPx()),
                )
            }
        }.padding(horizontal = 4.dp.dense),
    )
}

/**
 * One answer to choose. The [recommended] one stands out by its label, icon and outline,
 * not a second solid action or a colored backing; [recommendedText] names who recommends it when the default is not enough.
 * A [multiSelect] option is read as a checkbox (ticked or not), otherwise as a
 * radio button; [position] ("current" of "total") is read after the label so a screen reader hears how many there are.
 * The label keeps to 2 lines and the [detail] to 3, unless [fullText] (a full-page view, where an option must be readable
 * in full before it is picked).
 */
@Composable
fun DecisionOption(
    label: String,
    onClick: () -> Unit,
    modifier: Modifier = Modifier,
    detail: String? = null,
    recommended: Boolean = false,
    recommendedText: String? = null,
    selected: Boolean = false,
    enabled: Boolean = true,
    multiSelect: Boolean = false,
    position: Pair<Int, Int>? = null,
    fullText: Boolean = false,
) {
    val palette = PersonaTheme.palette
    val type = PersonaTheme.type
    val style = PersonaTheme.style
    val shape = RoundedCornerShape(when (style) { AppStyle.Glass -> 18.dp.corner; AppStyle.Brief -> 16.dp.corner; AppStyle.Board -> 8.dp.corner })
    val recommendedLabel = recommendedText ?: stringResource(R.string.designsystem_decision_recommended)
    val separator = stringResource(R.string.designsystem_separator)
    val haptics = rememberHaptics()
    val spoken = position?.let { (current, total) ->
        listOfNotNull(label, detail, recommendedLabel.takeIf { recommended },
            stringResource(R.string.designsystem_decision_option_position, current, total)).joinToString(separator)
    }
    val (fill, ink, border) = when (style) {
        AppStyle.Glass -> Triple(palette.statusSurface, palette.ink, if (selected || recommended) palette.accent else palette.strokeBottom)
        AppStyle.Brief -> Triple(palette.background, palette.ink, if (selected || recommended) palette.ink else Color.Transparent)
        AppStyle.Board -> Triple(palette.surface, palette.ink, if (recommended || selected) palette.accent else palette.line)
    }
    Row(
        modifier.fillMaxWidth().heightIn(min = 52.dp.row).clip(shape).background(fill).border(if (selected) 2.dp else 1.dp, border, shape)
            .clickable(enabled = enabled, role = if (multiSelect) Role.Checkbox else Role.RadioButton, onClick = haptics.tapped(onClick))
            .semantics {
                if (multiSelect) toggleableState = ToggleableState(selected) else this.selected = selected
                spoken?.let { contentDescription = it }
            }
            .stableAlpha(if (enabled) 1f else 0.45f).padding(horizontal = 16.dp.dense, vertical = 10.dp.dense),
        verticalAlignment = Alignment.CenterVertically,
    ) {
        Column(Modifier.weight(1f)) {
            Text(label, style = type.label, color = ink, maxLines = if (fullText) Int.MAX_VALUE else 2, overflow = TextOverflow.Ellipsis)
            detail?.let { Text(it, style = type.caption.copy(fontWeight = FontWeight.Normal), color = palette.ink2, maxLines = if (fullText) Int.MAX_VALUE else 3, overflow = TextOverflow.Ellipsis) }
        }
        if (selected) { Spacer(Modifier.width(8.dp.dense)); JIcon(PhIcons.CheckCircle, null, size = 20.dp, tint = palette.accent, selected = true) }
        else if (recommended) {
            Spacer(Modifier.width(8.dp.dense))
            JIcon(PhIcons.Sparkle, null, size = 14.dp, tint = if (style == AppStyle.Board) palette.accent else ink, selected = true)
            Spacer(Modifier.width(3.dp.dense))
            Text(recommendedLabel, style = type.micro.copy(fontWeight = FontWeight.Bold), color = if (style == AppStyle.Board) palette.accent else ink, maxLines = 1)
        }
    }
}

/** A single action line. Alternatives move into a menu before their labels would be truncated. */
@Composable
fun DecisionActions(primary: JAction?, alternatives: List<JAction>, overflowLabel: String, modifier: Modifier = Modifier, busy: Boolean = false) {
    // Pressing an answer is felt; its result plays its own (DESIGN_SYSTEM.md 4.3).
    val haptics = rememberHaptics()
    @Suppress("NAME_SHADOWING") val primary = primary?.let { haptics.tapped(it) }
    @Suppress("NAME_SHADOWING") val alternatives = alternatives.map { haptics.tapped(it) }
    val measurer = rememberTextMeasurer()
    val density = LocalDensity.current
    val type = PersonaTheme.type.labelSmall
    val size = JButtonSize.Small
    var menu by remember { mutableStateOf(false) }
    BoxWithConstraints(modifier.fillMaxWidth()) {
        val actions = alternatives + listOfNotNull(primary)
        val labels = actions.sumOf { measurer.measure(it.label, style = type, maxLines = 1, softWrap = false).size.width }
        val required = with(density) { (size.padding * (actions.size * 2) + Tokens.Space.s * (actions.size - 1).coerceAtLeast(0)).toPx() } + labels
        val overflow = required > with(density) { maxWidth.toPx() }
        Row(Modifier.fillMaxWidth(), horizontalArrangement = Arrangement.spacedBy(Tokens.Space.s), verticalAlignment = Alignment.CenterVertically) {
            if (overflow && alternatives.isNotEmpty()) Box {
                JIconButton(PhIcons.DotsThree, overflowLabel, { menu = true }, enabled = !busy)
                JMenu(menu && !busy, { menu = false }, alternatives.map { it.copy(enabled = it.enabled && !busy) })
            } else alternatives.forEach { action ->
                JButton(action.label, action.onClick, kind = JButtonKind.Secondary, size = size, enabled = action.enabled && !busy)
            }
            primary?.let { action ->
                JButton(action.label, action.onClick, Modifier.weight(1f, fill = false), size = size, enabled = action.enabled && !busy)
            }
        }
    }
}

/** How many of [options] (their widths, in order) fit in [available] beside what follows them, each [gap] apart: the first always. */
internal fun fittingOptions(options: List<Float>, available: Float, gap: Float): Int {
    var left = available
    var count = 0
    for (width in options) {
        if (count > 0 && width > left) break
        left -= width + gap
        count++
    }
    return count.coerceAtLeast(if (options.isEmpty()) 0 else 1)
}

/**
 * The answers of a decision on its row (#3804): [options] as buttons on one line, the first (the recommended one) the
 * only solid one, as many as fit and the first always (alone and still too wide, its label is cut short, never
 * wrapped); then [other], a quiet button that opens the task to answer in words; then a menu with the options that did
 * not fit and [more] (the answers that are no option), under a rule when both are there. [busy] is an answer on its
 * way: nothing can be pressed.
 */
@Composable
fun DecisionAnswers(options: List<JAction>, other: JAction, more: List<JAction>, overflowLabel: String,
    modifier: Modifier = Modifier, busy: Boolean = false) {
    val haptics = rememberHaptics()
    @Suppress("NAME_SHADOWING") val options = options.map { haptics.tapped(it) }
    @Suppress("NAME_SHADOWING") val other = haptics.tapped(other)
    @Suppress("NAME_SHADOWING") val more = more.map { haptics.tapped(it) }
    val measurer = rememberTextMeasurer()
    val density = LocalDensity.current
    val type = PersonaTheme.type.labelSmall
    val size = JButtonSize.Small
    var menu by remember { mutableStateOf(false) }
    BoxWithConstraints(modifier.fillMaxWidth()) {
        val gap = with(density) { Tokens.Space.s.toPx() }
        val minimum = with(density) { Tokens.MinTouch.android.toPx() }
        fun button(label: String, padding: Dp) =
            (measurer.measure(label, style = type, maxLines = 1, softWrap = false).size.width + with(density) { (padding * 2).toPx() }).coerceAtLeast(minimum)
        val widths = options.map { button(it.label, size.padding) }
        // the quiet button and, when anything is behind it, the menu's button stay whatever the options take
        val tail = button(other.label, QuietButtonPadding) + gap
        val room = with(density) { maxWidth.toPx() } - tail
        // without fixed answers the menu exists only for the options that do not fit: leave room for it only then
        val all = fittingOptions(widths, room, gap)
        val fit = if (more.isEmpty() && all == options.size) all else fittingOptions(widths, room - minimum - gap, gap)
        val hidden = options.drop(fit)
        val items = hidden + more.mapIndexed { index, action -> if (index == 0 && hidden.isNotEmpty()) action.copy(separatorBefore = true) else action }
        Row(Modifier.fillMaxWidth(), horizontalArrangement = Arrangement.spacedBy(Tokens.Space.s), verticalAlignment = Alignment.CenterVertically) {
            options.take(fit).forEachIndexed { index, action ->
                JButton(action.label, action.onClick, Modifier.weight(1f, fill = false), kind = if (index == 0) JButtonKind.Primary else JButtonKind.Secondary,
                    size = size, enabled = action.enabled && !busy)
            }
            JButton(other.label, other.onClick, kind = JButtonKind.Quiet, size = size, enabled = other.enabled && !busy)
            if (items.isNotEmpty()) Box {
                JIconButton(PhIcons.DotsThree, overflowLabel, { menu = true }, enabled = !busy)
                JMenu(menu && !busy, { menu = false }, items.map { it.copy(enabled = it.enabled && !busy) })
            }
        }
    }
}

/** The horizontal padding of a quiet [JButton], whatever its size. */
private val QuietButtonPadding = 8.dp

/**
 * The answer buttons of a decision: [secondary] on the left, [primary] on the right, [more] as quiet buttons
 * underneath. On the board the primary is wider.
 */
@OptIn(ExperimentalLayoutApi::class)
@Composable
fun DecisionButtons(primary: JAction, modifier: Modifier = Modifier, secondary: JAction? = null, more: List<JAction> = emptyList(), busy: Boolean = false) {
    val haptics = rememberHaptics()
    @Suppress("NAME_SHADOWING") val primary = haptics.tapped(primary)
    @Suppress("NAME_SHADOWING") val secondary = secondary?.let { haptics.tapped(it) }
    @Suppress("NAME_SHADOWING") val more = more.map { haptics.tapped(it) }
    val wide = if (PersonaTheme.style == AppStyle.Board) 1.5f else 1f
    Column(modifier.fillMaxWidth()) {
        Row(horizontalArrangement = Arrangement.spacedBy(10.dp.dense)) {
            secondary?.let { JButton(it.label, it.onClick, Modifier.weight(1f), kind = if (it.destructive) JButtonKind.Destructive else JButtonKind.Secondary, enabled = it.enabled && !busy) }
            JButton(primary.label, primary.onClick, Modifier.weight(wide), kind = if (primary.destructive) JButtonKind.Destructive else JButtonKind.Primary, enabled = primary.enabled, loading = busy)
        }
        // Several secondary choices (a plan's "Approve and run automatically" and "Confirm each step") wrap to a second line instead of squeezing.
        if (more.isNotEmpty()) FlowRow(Modifier.fillMaxWidth().padding(top = 4.dp.dense), horizontalArrangement = Arrangement.Center) {
            more.forEach { JTextButton(it.label, it.onClick, enabled = it.enabled && !busy) }
        }
    }
}

/**
 * A reply draft has one primary Send and two reversible secondary choices. Stack at large text or narrow widths. [edit]
 * (a message card on Glass, iOS `MessageCard`): Edit / Done beside Send, which opens the draft for typing.
 */
@Composable
fun DraftDecisionButtons(send: JAction, later: JAction, dismiss: JAction, modifier: Modifier = Modifier,
    busy: Boolean = false, sendModifier: Modifier = Modifier, laterModifier: Modifier = Modifier, dismissModifier: Modifier = Modifier,
    edit: JAction? = null, editModifier: Modifier = Modifier) {
    val haptics = rememberHaptics()
    @Suppress("NAME_SHADOWING") val send = haptics.tapped(send)
    @Suppress("NAME_SHADOWING") val later = haptics.tapped(later)
    @Suppress("NAME_SHADOWING") val dismiss = haptics.tapped(dismiss)
    @Suppress("NAME_SHADOWING") val edit = edit?.let { haptics.tapped(it) }
    BoxWithConstraints(modifier.fillMaxWidth()) {
        val stacked = LocalDensity.current.fontScale >= 1.3f || maxWidth < 320.dp
        Column(verticalArrangement = Arrangement.spacedBy(Tokens.Space.s)) {
            if (edit != null && !stacked) Row(horizontalArrangement = Arrangement.spacedBy(Tokens.Space.s)) {
                JButton(send.label, send.onClick, sendModifier.weight(1f), kind = JButtonKind.Primary,
                    size = JButtonSize.Large, enabled = send.enabled && !busy, loading = busy)
                JButton(edit.label, edit.onClick, editModifier, kind = JButtonKind.Secondary, size = JButtonSize.Large,
                    enabled = edit.enabled && !busy)
            } else {
                JButton(send.label, send.onClick, sendModifier.fillMaxWidth(), kind = JButtonKind.Primary,
                    size = JButtonSize.Large, enabled = send.enabled && !busy, loading = busy)
                if (edit != null) JButton(edit.label, edit.onClick, editModifier.fillMaxWidth(), kind = JButtonKind.Secondary,
                    size = JButtonSize.Large, enabled = edit.enabled && !busy)
            }
            if (stacked) {
                JButton(later.label, later.onClick, laterModifier.fillMaxWidth(), kind = JButtonKind.Secondary,
                    size = JButtonSize.Large, enabled = later.enabled && !busy)
                JButton(dismiss.label, dismiss.onClick, dismissModifier.fillMaxWidth(), kind = JButtonKind.Secondary,
                    size = JButtonSize.Large, enabled = dismiss.enabled && !busy)
            } else Row(horizontalArrangement = Arrangement.spacedBy(Tokens.Space.s)) {
                JButton(later.label, later.onClick, laterModifier.weight(1f), kind = JButtonKind.Secondary,
                    size = JButtonSize.Large, enabled = later.enabled && !busy)
                JButton(dismiss.label, dismiss.onClick, dismissModifier.weight(1f), kind = JButtonKind.Secondary,
                    size = JButtonSize.Large, enabled = dismiss.enabled && !busy)
            }
        }
    }
}
