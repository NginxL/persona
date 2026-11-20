package persona.core.designsystem

import androidx.compose.ui.res.stringResource
import androidx.compose.animation.animateColorAsState
import androidx.compose.animation.core.*
import androidx.compose.ui.draw.drawBehind
import androidx.compose.foundation.Canvas
import androidx.compose.foundation.LocalIndication
import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.clickable
import androidx.compose.foundation.combinedClickable
import androidx.compose.foundation.interaction.MutableInteractionSource
import androidx.compose.foundation.interaction.collectIsFocusedAsState
import androidx.compose.foundation.interaction.collectIsPressedAsState
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.text.BasicTextField
import androidx.compose.foundation.text.KeyboardActions
import androidx.compose.foundation.text.KeyboardOptions
import androidx.compose.material3.Icon
import androidx.compose.material3.LocalContentColor
import androidx.compose.material3.Text
import androidx.compose.material3.minimumInteractiveComponentSize
import androidx.compose.runtime.*
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.draw.drawWithCache
import androidx.compose.ui.draw.drawWithContent
import androidx.compose.ui.draw.scale
import androidx.compose.ui.draw.shadow
import androidx.compose.ui.geometry.Offset
import androidx.compose.ui.geometry.Size
import androidx.compose.ui.graphics.Brush
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.Path
import androidx.compose.ui.graphics.Shape
import androidx.compose.ui.graphics.SolidColor
import androidx.compose.ui.graphics.addOutline
import androidx.compose.ui.graphics.drawscope.clipPath
import androidx.compose.ui.graphics.drawscope.scale as scaleDraw
import androidx.compose.ui.graphics.StrokeCap
import androidx.compose.ui.graphics.graphicsLayer
import androidx.compose.ui.graphics.drawscope.Stroke
import androidx.compose.ui.autofill.ContentDataType
import androidx.compose.ui.platform.LocalFocusManager
import androidx.compose.ui.semantics.Role
import androidx.compose.ui.semantics.clearAndSetSemantics
import androidx.compose.ui.semantics.contentDataType
import androidx.compose.ui.semantics.contentDescription
import androidx.compose.ui.semantics.role
import androidx.compose.ui.semantics.selected
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.semantics.toggleableState
import androidx.compose.ui.state.ToggleableState
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.input.ImeAction
import androidx.compose.ui.text.input.KeyboardType
import androidx.compose.ui.text.input.PasswordVisualTransformation
import androidx.compose.ui.text.input.VisualTransformation
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.layout.onSizeChanged
import androidx.compose.ui.layout.positionInParent
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.node.requireDensity
import kotlinx.coroutines.launch
import androidx.compose.ui.unit.Dp
import androidx.compose.ui.unit.dp
import persona.core.designsystem.icons.PhIcon
import persona.core.designsystem.icons.PhIcons

// ------------------------------------------------------------------ icons

/** A [JIcon]'s size when the caller gives none. */
internal val JIconDefaultSize: Dp = Tokens.Size.iconS

/**
 * A Phosphor icon in the weight the style uses: regular in glass and brief, bold on the board, filled when
 * [selected]. [decorative] icons (leading icons of settings rows) use the duotone weight in glass.
 */
@Composable
fun JIcon(
    icon: PhIcon,
    contentDescription: String? = null,
    modifier: Modifier = Modifier,
    size: Dp = JIconDefaultSize,
    tint: Color = LocalContentColor.current,
    selected: Boolean = false,
    decorative: Boolean = false,
) {
    val vector = when {
        selected -> icon.fill
        PersonaTheme.style == AppStyle.Board -> icon.bold
        decorative && PersonaTheme.style == AppStyle.Glass -> icon.regular
        else -> icon.regular
    }
    Icon(vector, contentDescription, modifier.size(size), tint = tint)
}

// ------------------------------------------------------------------ buttons

/**
 * Draws the rest of the chain at [alpha]: the dimmed content of a disabled control. Use it instead of
 * `Modifier.alpha` wherever the value changes. `Modifier.alpha(1f)` drops its graphicsLayer and any other value
 * adds one, so turning a control enabled removed a layer from the middle of its chain; behind `clip` and
 * `background` (a button's fill, a field's frame) Compose then stopped drawing that background until it changed
 * again, and the button showed only its label (Compose UI 1.12.1, every style). This layer always stays and only
 * its alpha changes.
 */
fun Modifier.stableAlpha(alpha: Float): Modifier = graphicsLayer { this.alpha = alpha }

/**
 * Clips what the rest of the chain draws to [shape], but not where it takes touches. `Modifier.clip` also cuts off
 * the 48dp touch target Compose gives a smaller child at its own edge: a 36dp segment inside a clipped frame lost the
 * taps just above it (DS-06, `TouchTargetTest`).
 */
internal fun Modifier.clipDrawing(shape: Shape): Modifier = drawWithCache {
    val outline = Path().apply { addOutline(shape.createOutline(size, layoutDirection, this@drawWithCache)) }
    onDrawWithContent { clipPath(outline) { this@onDrawWithContent.drawContent() } }
}

enum class JButtonKind { Primary, Secondary, Quiet, Destructive }

enum class JButtonSize(val height: Dp, val padding: Dp) { Large(48.dp, 20.dp), Medium(40.dp, 16.dp), Small(32.dp, 12.dp) }

/** Labels are always one line (project rule); long ones ellipsize rather than wrap. */
@Composable
fun JButton(
    text: String,
    onClick: () -> Unit,
    modifier: Modifier = Modifier,
    kind: JButtonKind = JButtonKind.Primary,
    size: JButtonSize = JButtonSize.Large,
    icon: PhIcon? = null,
    enabled: Boolean = true,
    loading: Boolean = false,
    loadingText: String? = null,
) {
    val palette = PersonaTheme.palette
    val style = PersonaTheme.style
    val shapes = PersonaTheme.shapes
    val shape = RoundedCornerShape(if (style == AppStyle.Board) shapes.button else size.height / 2)
    val interaction = remember { MutableInteractionSource() }
    val pressed by interaction.collectIsPressedAsState()
    val reduceMotion = PersonaTheme.reduceMotion
    // Layout width, not snapshot state. Reading it from composition made every Brief and Board button recompose once
    // after the first layout. It is read only while a Glass button is pressed; scale does not change the layout width.
    val widthPx = remember { floatArrayOf(0f) }
    val growthPx = with(LocalDensity.current) { PageTransitionTokens.pressGrowthDp.dp.toPx() }
    val glassPress = pressed && style == AppStyle.Glass && !reduceMotion
    val scale by animateFloatAsState(
        targetValue = if (glassPress) glassPressTarget(true, true, false, widthPx[0], growthPx) else 1f,
        animationSpec = if (pressed) {
            IosSpring(PageTransitionTokens.glassPressDurationMs, PageTransitionTokens.glassPressBounce).spec()
        } else {
            IosSpring(PageTransitionTokens.glassReleaseDurationMs, PageTransitionTokens.glassReleaseBounce).spec()
        },
        label = "press",
    )
    val (fill, content) = when (kind) {
        JButtonKind.Primary -> palette.action to palette.onAction
        JButtonKind.Secondary -> (if (style == AppStyle.Board) palette.surface else palette.surfaceStrong) to palette.ink
        JButtonKind.Quiet -> Color.Transparent to (if (style == AppStyle.Board) palette.accent else palette.ink2)
        JButtonKind.Destructive -> palette.statusSurface to palette.danger.text
    }
    val frame = when {
        kind == JButtonKind.Secondary && style == AppStyle.Glass ->
            Modifier.clip(shape).background(fill).border(1.dp, Brush.verticalGradient(listOf(palette.strokeTop, palette.strokeBottom)), shape)
        kind == JButtonKind.Secondary && style == AppStyle.Board -> Modifier.clip(shape).background(fill).border(1.dp, palette.line, shape)
        else -> Modifier.clip(shape).background(fill)
    }
    val busy = loading
    Row(
        modifier.heightIn(min = size.height).minimumInteractiveComponentSize().scale(scale)
            .onSizeChanged { if (style == AppStyle.Glass && !reduceMotion) widthPx[0] = it.width.toFloat() }
            .then(frame)
            .clickable(interaction, indication = androidx.compose.material3.ripple(), enabled = enabled && !busy, role = Role.Button, onClick = onClick)
            .stableAlpha(if (enabled || busy) 1f else 0.4f)
            .padding(horizontal = if (kind == JButtonKind.Quiet) 8.dp.dense else size.padding),
        horizontalArrangement = Arrangement.Center, verticalAlignment = Alignment.CenterVertically,
    ) {
        if (busy) {
            JSpinner(Modifier.size(16.dp), color = content)
            Spacer(Modifier.width(8.dp.dense))
        } else if (icon != null) {
            JIcon(icon, null, size = if (size == JButtonSize.Small) 15.dp else 18.dp, tint = content, selected = kind == JButtonKind.Primary)
            Spacer(Modifier.width(6.dp.dense))
        }
        Text(
            if (busy) loadingText ?: text else text,
            style = if (size == JButtonSize.Large) PersonaTheme.type.label else PersonaTheme.type.labelSmall,
            color = content, maxLines = 1, softWrap = false, overflow = TextOverflow.Ellipsis,
        )
    }
}

@Composable
fun JTextButton(text: String, onClick: () -> Unit, modifier: Modifier = Modifier, enabled: Boolean = true, icon: PhIcon? = null) =
    JButton(text, onClick, modifier, kind = JButtonKind.Quiet, size = JButtonSize.Medium, icon = icon, enabled = enabled)

/**
 * Plain icons sit in bars; [raised] ones are standalone controls (a glass circle, a wash disc, a bordered square).
 * [badge] is the needs-you dot at the top end; [count], when above 0, a neutral count capsule at the bottom end (how many
 * a control holds, never how many need the user: that is [JBadge]'s, in the needs-you color).
 * Adjacent home actions can opt out of [expandOnHomePress] to keep the pressed circle clear of its neighbor.
 */
@Composable
fun JIconButton(
    icon: PhIcon,
    contentDescription: String?,
    onClick: () -> Unit,
    modifier: Modifier = Modifier,
    enabled: Boolean = true,
    raised: Boolean = LocalIconsRaised.current,
    tint: Color? = null,
    badge: Boolean = false,
    selected: Boolean = false,
    count: Int = 0,
    expandOnHomePress: Boolean = true,
) {
    if (count <= 0) JIconButtonBox(icon, contentDescription, onClick, modifier, enabled, raised, tint, badge, selected, expandOnHomePress)
    else Box(modifier) {
        // The circle clips the button's ripple; the capsule hangs over its edge, so it sits outside that clip.
        val palette = PersonaTheme.palette
        val label = if (count > 99) "99+" else "$count"
        JIconButtonBox(icon, contentDescription, onClick, Modifier, enabled, raised, tint, badge, selected, expandOnHomePress)
        Text(label, style = PersonaTheme.type.micro.copy(fontWeight = FontWeight.SemiBold),
            color = palette.ink2, maxLines = 1,
            modifier = Modifier.align(Alignment.BottomEnd).clearAndSetSemantics {}.stableAlpha(if (enabled) 1f else 0.4f)
                .clip(CircleShape).background(palette.surfaceOpaque).border(Tokens.Size.line, palette.line, CircleShape)
                .padding(horizontal = SectionHeaderTokens.countPadding, vertical = SectionHeaderTokens.countPaddingVertical))
    }
}

@Composable
private fun JIconButtonBox(
    icon: PhIcon, contentDescription: String?, onClick: () -> Unit, modifier: Modifier, enabled: Boolean, raised: Boolean,
    tint: Color?, badge: Boolean, selected: Boolean, expandOnHomePress: Boolean,
) {
    val palette = PersonaTheme.palette
    val style = PersonaTheme.style
    val color = tint ?: palette.ink
    val homeAction = raised && style == AppStyle.Glass && LocalHomeNavigationActions.current
    val interactive = homeAction && enabled && LocalGlassSource.current != null &&
        PersonaTheme.glass == GlassLevel.Liquid && !PersonaTheme.reduceMotion
    val pressSource = if (interactive) remember { MutableInteractionSource() } else null
    val feedback = if (pressSource != null) {
        val pressed by pressSource.collectIsPressedAsState()
        val progress = animateFloatAsState(if (pressed) 1f else 0f, PersonaTheme.motion.enter(), label = "home action press")
        val growth = if (expandOnHomePress) HomeTokens.actionPressScale - 1f else 0f
        // Draw outside the resting circle. The hit target and Haze capture keep their original coordinates.
        Modifier.graphicsLayer().drawWithContent {
            val fraction = progress.value.coerceIn(0f, 1f)
            if (fraction == 0f) drawContent()
            else scaleDraw(1f + growth * fraction) {
                this@drawWithContent.drawContent()
                drawCircle(Color.White, alpha = HomeTokens.actionPressVeil * fraction)
            }
        }
    } else Modifier
    val surface = when {
        !raised -> Modifier
        homeAction -> Modifier.size(Tokens.MinTouch.android).then(feedback).homeNavigationGlassSurface(interactive)
        style == AppStyle.Glass -> Modifier.size(44.dp).floatingSurface(CircleShape)
        style == AppStyle.Brief -> Modifier.size(42.dp).clip(CircleShape).background(palette.surface)
        else -> Modifier.size(40.dp).clip(RoundedCornerShape(8.dp.corner)).background(palette.surface).border(1.dp, palette.line, RoundedCornerShape(8.dp.corner))
    }
    Box(
        modifier.size(48.dp).then(if (interactive && expandOnHomePress) Modifier else Modifier.clip(CircleShape))
            .clickable(interactionSource = pressSource, indication = LocalIndication.current, enabled = enabled, role = Role.Button, onClick = onClick)
            .semantics { if (contentDescription != null) this.contentDescription = contentDescription }
            .stableAlpha(if (enabled) 1f else 0.4f),
        contentAlignment = Alignment.Center,
    ) {
        Box(surface, contentAlignment = Alignment.Center) {
            JIcon(icon, null, size = if (raised) 20.dp else 24.dp, tint = color, selected = selected)
        }
        if (badge) Box(
            Modifier.align(Alignment.TopEnd).padding(top = 10.dp.dense, end = 10.dp.dense).size(9.dp).clip(CircleShape)
                .background(palette.needsYou.dot).border(1.5.dp, palette.background, CircleShape),
        )
    }
}

// ------------------------------------------------------------------ text fields

/**
 * The text field of every style. [label] sits above the field; [supportingText] below it. Secrets pass a
 * [visualTransformation] as with Material fields.
 */
@Composable
fun JTextField(
    value: String,
    onValueChange: (String) -> Unit,
    modifier: Modifier = Modifier,
    placeholder: String? = null,
    label: String? = null,
    supportingText: String? = null,
    singleLine: Boolean = false,
    minLines: Int = 1,
    maxLines: Int = if (singleLine) 1 else 6,
    enabled: Boolean = true,
    isError: Boolean = false,
    leading: PhIcon? = null,
    trailing: (@Composable () -> Unit)? = null,
    keyboardOptions: KeyboardOptions = KeyboardOptions.Default,
    keyboardActions: KeyboardActions = KeyboardActions.Default,
    visualTransformation: VisualTransformation = VisualTransformation.None,
    /** False keeps the field out of Autofill: nothing is filled into it, nothing typed is offered to be saved. */
    autofill: Boolean = true,
    /** Configuration and code use the design system's code type scale, never a page-local font. */
    monospace: Boolean = false,
) {
    val palette = PersonaTheme.palette
    val style = PersonaTheme.style
    val interaction = remember { MutableInteractionSource() }
    val focused by interaction.collectIsFocusedAsState()
    val shape = PersonaTheme.shapes.controlShape
    val (fill, border) = when (style) {
        AppStyle.Glass -> palette.sunken to if (focused) palette.accent else palette.line
        AppStyle.Brief -> palette.surface to if (focused) palette.ink else Color.Transparent
        AppStyle.Board -> palette.surface to if (focused) palette.accent else palette.line
    }
    Column(modifier) {
        label?.let {
            Text(it, style = PersonaTheme.type.caption, color = palette.ink2, maxLines = 1, overflow = TextOverflow.Ellipsis)
            Spacer(Modifier.height(6.dp.dense))
        }
        BasicTextField(
            value = value, onValueChange = onValueChange, enabled = enabled, singleLine = singleLine,
            minLines = minLines, maxLines = maxLines, interactionSource = interaction,
            textStyle = (if (monospace) PersonaTheme.type.code else PersonaTheme.type.body).copy(color = palette.ink), cursorBrush = SolidColor(palette.accent.takeIf { style != AppStyle.Brief } ?: palette.ink),
            keyboardOptions = keyboardOptions, keyboardActions = keyboardActions, visualTransformation = visualTransformation,
            // On the text field's own semantics node: Autofill reads the node it would fill, not its parents.
            modifier = (if (autofill) Modifier.fillMaxWidth() else Modifier.fillMaxWidth().semantics { contentDataType = ContentDataType.None }).hardwareShortcutTextInput(),
            decorationBox = { field ->
                Row(
                    Modifier.fillMaxWidth().heightIn(min = 48.dp.row).clip(shape).background(fill)
                        .border(if (focused || style != AppStyle.Brief) 1.dp else 0.dp, if (isError) palette.danger.dot else border, shape)
                        .stableAlpha(if (enabled) 1f else 0.5f).padding(horizontal = 14.dp.dense, vertical = 12.dp.dense),
                    verticalAlignment = if (singleLine) Alignment.CenterVertically else Alignment.Top,
                ) {
                    leading?.let { JIcon(it, null, size = 20.dp, tint = palette.ink3); Spacer(Modifier.width(10.dp.dense)) }
                    Box(Modifier.weight(1f)) {
                        if (value.isEmpty() && placeholder != null) Text(placeholder, style = if (monospace) PersonaTheme.type.code else PersonaTheme.type.body, color = palette.ink3, maxLines = if (singleLine) 1 else maxLines, overflow = TextOverflow.Ellipsis)
                        field()
                    }
                    trailing?.let { Spacer(Modifier.width(6.dp.dense)); it() }
                }
            },
        )
        supportingText?.let {
            Spacer(Modifier.height(6.dp.dense))
            Text(it, style = PersonaTheme.type.caption, color = if (isError) palette.danger.text else palette.ink3)
        }
    }
}

/**
 * A password the app never keeps (iOS `SecretField`): masked, on the password keyboard without corrections or
 * suggestions, and outside Autofill (`ContentDataType.None`), since it is a password of the user's own computer, not
 * this app's. The keyboard's action key only puts the keyboard away; nothing is sent from the keyboard.
 */
@Composable
fun JSecretField(value: String, onValueChange: (String) -> Unit, label: String, modifier: Modifier = Modifier, enabled: Boolean = true) {
    val focus = LocalFocusManager.current
    JTextField(
        value, onValueChange, modifier, label = label, singleLine = true, enabled = enabled,
        keyboardOptions = KeyboardOptions(keyboardType = KeyboardType.Password, autoCorrectEnabled = false, imeAction = ImeAction.Done),
        keyboardActions = KeyboardActions(onDone = { focus.clearFocus() }),
        visualTransformation = PasswordVisualTransformation(), autofill = false,
    )
}

@Composable
fun JSearchField(query: String, onQueryChange: (String) -> Unit, placeholder: String, modifier: Modifier = Modifier) {
    val clear = stringResource(R.string.designsystem_search_clear)
    JTextField(
        query, onQueryChange, modifier, placeholder = placeholder, singleLine = true, leading = PhIcons.MagnifyingGlass,
        trailing = if (query.isEmpty()) null else ({
            Box(Modifier.size(24.dp).clip(CircleShape).clickable(role = Role.Button) { onQueryChange("") }.semantics { contentDescription = clear }, contentAlignment = Alignment.Center) {
                JIcon(PhIcons.XCircle, null, size = 18.dp, tint = PersonaTheme.palette.ink3, selected = true)
            }
        }),
    )
}

// ------------------------------------------------------------------ toggles

@Composable
fun JSwitch(checked: Boolean, onCheckedChange: ((Boolean) -> Unit)?, modifier: Modifier = Modifier, enabled: Boolean = true) {
    val palette = PersonaTheme.palette
    val style = PersonaTheme.style
    val (width, height, thumb) = when (style) {
        AppStyle.Glass -> Triple(50.dp, 30.dp, 24.dp)
        AppStyle.Brief -> Triple(48.dp, 28.dp, 22.dp)
        AppStyle.Board -> Triple(46.dp, 26.dp, 20.dp)
    }
    val corner = if (style == AppStyle.Board) 6.dp else height / 2
    val track = when {
        checked && style == AppStyle.Brief -> palette.action
        checked -> palette.done.dot
        else -> palette.surfaceStrong.compositeOn(palette.surfaceOpaque)
    }
    val offset by animateDpAsState(if (checked) width - thumb - 6.dp else 0.dp, label = "thumb")
    Box(
        modifier.size(width, height).clip(RoundedCornerShape(corner)).background(track)
            .then(if (!checked) Modifier.border(1.dp, palette.line, RoundedCornerShape(corner)) else Modifier)
            .then(if (onCheckedChange != null) Modifier.clickable(enabled = enabled, role = Role.Switch) { onCheckedChange(!checked) } else Modifier)
            .semantics { toggleableState = ToggleableState(checked) }
            .stableAlpha(if (enabled) 1f else 0.4f).padding(3.dp.dense),
        contentAlignment = Alignment.CenterStart,
    ) {
        val thumbColor = if (checked && style == AppStyle.Brief) palette.onAction else Color.White
        Box(
            Modifier.offset(x = offset).size(thumb)
                .shadow(if (style == AppStyle.Glass) 2.dp else 0.dp, RoundedCornerShape(if (style == AppStyle.Board) 4.dp else thumb / 2))
                .clip(RoundedCornerShape(if (style == AppStyle.Board) 4.dp else thumb / 2)).background(thumbColor),
        )
    }
}

@Composable
fun JCheckbox(checked: Boolean, onCheckedChange: ((Boolean) -> Unit)?, modifier: Modifier = Modifier, enabled: Boolean = true) {
    Box(
        modifier.size(48.dp).then(if (onCheckedChange != null) Modifier.clickable(enabled = enabled, role = Role.Checkbox) { onCheckedChange(!checked) } else Modifier)
            .semantics { toggleableState = ToggleableState(checked) }.stableAlpha(if (enabled) 1f else 0.4f),
        contentAlignment = Alignment.Center,
    ) { JCheckMark(ToggleableState(checked)) }
}

/**
 * A checkbox of three states (#2576): a group's tick in a selection, none, some (–) or all (✓) of its items picked.
 * A tap reports [onClick]; the caller decides what the group becomes.
 */
@Composable
fun JTriCheckbox(state: ToggleableState, onClick: (() -> Unit)?, modifier: Modifier = Modifier, label: String? = null) {
    Box(
        modifier.size(48.dp).then(if (onClick != null) Modifier.clickable(role = Role.Checkbox, onClickLabel = label, onClick = onClick) else Modifier)
            .semantics { toggleableState = state; if (label != null) contentDescription = label },
        contentAlignment = Alignment.Center,
    ) { JCheckMark(state) }
}

/** The box a checkbox draws, without its tap target: a row in a selection shows it before its avatar. */
@Composable
fun JCheckMark(state: ToggleableState, modifier: Modifier = Modifier) {
    val palette = PersonaTheme.palette
    val style = PersonaTheme.style
    val shape = when (style) { AppStyle.Glass -> CircleShape; AppStyle.Brief -> RoundedCornerShape(7.dp.corner); AppStyle.Board -> RoundedCornerShape(4.dp) }
    val on = if (style == AppStyle.Brief) palette.action else palette.accent
    val filled = state != ToggleableState.Off
    Box(
        modifier.size(22.dp).clip(shape).background(if (filled) on else Color.Transparent)
            .border(if (filled) 0.dp else 1.5.dp, palette.ink3, shape),
        contentAlignment = Alignment.Center,
    ) {
        if (filled) JIcon(if (state == ToggleableState.On) PhIcons.Check else PhIcons.Minus, null, size = 15.dp,
            tint = if (palette.customRoles) ExtensionThemeColors.on(on) else if (style == AppStyle.Brief) palette.onAction else Color.White, selected = false)
    }
}

@Composable
fun JRadio(selected: Boolean, onClick: (() -> Unit)?, modifier: Modifier = Modifier, enabled: Boolean = true) {
    val palette = PersonaTheme.palette
    val on = if (PersonaTheme.style == AppStyle.Brief) palette.action else palette.accent
    Box(
        modifier.size(48.dp).then(if (onClick != null) Modifier.clickable(enabled = enabled, role = Role.RadioButton, onClick = onClick) else Modifier)
            .semantics { this.selected = selected }.stableAlpha(if (enabled) 1f else 0.4f),
        contentAlignment = Alignment.Center,
    ) {
        Box(Modifier.size(22.dp).clip(CircleShape).border(if (selected) 6.5.dp else 1.5.dp, if (selected) on else palette.ink3, CircleShape))
    }
}

// ------------------------------------------------------------------ segmented filters and chips

/**
 * One choice of [JSegmented]. [countTone]: the [count] in a capsule of that state's colours ([JToneCount]: none at 0,
 * 99+ past 99) after the label, spoken with it ("Needs you, 12"); none: the count follows the label as plain text.
 */
data class JSegment(val label: String, val count: Int? = null, val status: Status? = null, val countTone: Status? = null)

/**
 * A small set of mutually exclusive filters. On Glass one pill slides to the chosen segment
 * (`GlassSlidingPill`, `Theme.Motion.state`) and the label colour fades with it. An index outside
 * the list draws no pill. Brief keeps underlined text tabs and Board a ruled strip of cells.
 * [compact] reduces spacing only; text keeps the ambient font scale. [revealIn]: the [modifier]'s sideways scroll, in
 * which the selected option is kept whole in sight ([revealWhenSelected]), [revealMargin] clear of an edge with more past
 * it (where a fade covers it).
 */
@Composable
fun JSegmented(options: List<JSegment>, selected: Int, onSelect: (Int) -> Unit, modifier: Modifier = Modifier, compact: Boolean = false,
               revealIn: androidx.compose.foundation.ScrollState? = null, revealMargin: Dp = Tokens.Space.flush) =
    JSegmentedStrip(options, selected, onSelect, modifier, compact, revealIn, revealMargin)

/**
 * Brief's tabs scrolling in a strip [viewport] px wide that fades at an edge with more past it: when the edge falls between
 * two tabs, or shows less than [peek] of the next, their gaps narrow, to no less than [JSegmented]'s compact gap, so that
 * much of the next one shows under the fade ([peekGap]).
 */
internal data class TabPeek(val viewport: Int, val peek: Dp)

/** [JSegmented], and in Brief with [peek] its tabs spaced so the next one past the edge shows ([TabPeek]). */
@Composable
internal fun JSegmentedStrip(options: List<JSegment>, selected: Int, onSelect: (Int) -> Unit, modifier: Modifier = Modifier, compact: Boolean = false,
                             revealIn: androidx.compose.foundation.ScrollState? = null, revealMargin: Dp = Tokens.Space.flush, peek: TabPeek? = null) {
    val palette = PersonaTheme.palette
    val type = PersonaTheme.type
    fun text(segment: JSegment) = segment.label + (segment.count?.let { " $it" } ?: "")
    when (PersonaTheme.style) {
        // One pill, not a background on each segment. iOS: the system's sliding thumb (`Picker` `.segmented`).
        AppStyle.Glass -> GlassSlidingPill(
            selected = selected,
            count = options.size,
            color = palette.action,
            modifier = modifier.clipDrawing(CircleShape).background(palette.surface)
                .border(1.dp, Brush.verticalGradient(listOf(palette.strokeTop, palette.strokeBottom)), CircleShape)
                .padding(if (compact) Tokens.Space.xxs else 4.dp.dense),
            segmentModifier = { index -> Modifier.revealWhenSelected(revealIn, index == selected, revealMargin) },
        ) { index ->
            val segment = options[index]
            val on = index == selected
            val spoken = segment.countTone?.let { spokenCount(segment.label, segment.count) }
            Box(
                Modifier.clip(CircleShape)
                    .clickable(role = Role.Tab) { onSelect(index) }.semantics { this.selected = on; spoken?.let { contentDescription = it } }
                    .padding(horizontal = if (compact) Tokens.Space.s else 16.dp.dense, vertical = 9.dp.dense),
                contentAlignment = Alignment.Center,
            ) {
                // One weight, as this row always had. Only the colour fades (`GlassSegmentLabel`).
                val label: @Composable (String) -> Unit = {
                    GlassSegmentLabel(
                        it, selected = on, selectedColor = palette.onAction, idleColor = palette.ink2,
                        style = type.labelSmall, softWrap = false,
                    )
                }
                if (segment.countTone == null) label(text(segment))
                else TonedSegmentLabel(segment, type.labelSmall, if (on) palette.onAction else palette.ink2, onFill = on, label = label)
            }
        }
        AppStyle.Brief -> {
            val gap = if (compact) Tokens.Space.s else JSegmentedBriefGap
            val tabs: @Composable () -> Unit = {
                options.forEachIndexed { index, segment ->
                    val on = index == selected
                    val spoken = segment.countTone?.let { spokenCount(segment.label, segment.count) }
                    Column(
                        Modifier.revealWhenSelected(revealIn, on, revealMargin).clickable(role = Role.Tab) { onSelect(index) }.semantics { this.selected = on; spoken?.let { contentDescription = it } }.padding(vertical = 6.dp.dense),
                        horizontalAlignment = Alignment.CenterHorizontally,
                    ) {
                        if (segment.countTone == null) Text(text(segment), style = type.label, color = if (on) palette.ink else palette.ink3, maxLines = 1, softWrap = false, overflow = TextOverflow.Ellipsis)
                        else TonedSegmentLabel(segment, type.label, if (on) palette.ink else palette.ink3)
                        Spacer(Modifier.height(6.dp.dense))
                        Box(Modifier.size(20.dp, 2.5.dp).clip(CircleShape).background(if (on) palette.ink else Color.Transparent))
                    }
                }
            }
            if (peek == null) Row(modifier, horizontalArrangement = Arrangement.spacedBy(gap)) { tabs() }
            else androidx.compose.ui.layout.Layout(tabs, modifier) { measurables, constraints ->
                val placeables = measurables.map { it.measure(androidx.compose.ui.unit.Constraints(maxHeight = constraints.maxHeight)) }
                val spacing = peekGap(placeables.map { it.width }, peek.viewport, gap.roundToPx(), peek.peek.roundToPx(), Tokens.Space.s.roundToPx())
                val width = placeables.sumOf { it.width } + spacing * (placeables.size - 1).coerceAtLeast(0)
                layout(width, placeables.maxOfOrNull { it.height } ?: 0) {
                    var x = 0
                    placeables.forEach { it.placeRelative(x, 0); x += it.width + spacing }
                }
            }
        }
        AppStyle.Board -> Row(
            modifier.height(IntrinsicSize.Min).clipDrawing(RoundedCornerShape(8.dp.corner)).background(palette.surface).border(1.dp, palette.line, RoundedCornerShape(8.dp.corner)),
        ) {
            options.forEachIndexed { index, segment ->
                val on = index == selected
                if (index > 0) Box(Modifier.width(1.dp).fillMaxHeight().background(palette.line))
                val accent = palette.accent
                val spoken = segment.countTone?.let { spokenCount(segment.label, segment.count) }
                Box(
                    Modifier.weight(1f, fill = false).revealWhenSelected(revealIn, on, revealMargin)
                        // The selected one's underline is drawn, not laid out: a fillMaxWidth line widened that segment to
                        // its whole share of the row (the image editor's "Pen" took the row's width, "Box" stayed small).
                        .drawWithContent {
                            drawContent()
                            if (on) drawRect(accent, Offset(0f, size.height - 2.dp.toPx()), Size(size.width, 2.dp.toPx()))
                        }
                        .clickable(role = Role.Tab) { onSelect(index) }.semantics { this.selected = on; spoken?.let { contentDescription = it } },
                ) {
                    val padding = Modifier.padding(horizontal = if (compact) Tokens.Space.s else 14.dp.dense, vertical = 10.dp.dense)
                    val style = type.labelSmall.copy(fontWeight = if (on) FontWeight.Bold else FontWeight.Medium)
                    if (segment.countTone == null) Text(
                        text(segment), style = style,
                        color = if (on) palette.ink else palette.ink2, maxLines = 1, softWrap = false, overflow = TextOverflow.Ellipsis,
                        modifier = padding,
                    )
                    else TonedSegmentLabel(segment, style, if (on) palette.ink else palette.ink2, padding)
                }
            }
        }
    }
}

/** The room between two of Brief's tabs ([JSegmented]); a bar that splits its tabs in two keeps it between the parts. */
internal val JSegmentedBriefGap: Dp get() = 22.dp.dense

/**
 * The gap between tabs of [widths] in a strip [viewport] wide ([TabPeek]): [gap] while they fit or while the edge shows at
 * least [peek] of the first tab past it; else the narrowest gap, down to [least], that brings [peek] of that tab into sight,
 * and [gap] again when even [least] can't.
 */
internal fun peekGap(widths: List<Int>, viewport: Int, gap: Int, peek: Int, least: Int): Int {
    if (widths.size < 2 || viewport <= 0 || widths.sum() + gap * (widths.size - 1) <= viewport) return gap
    var before = 0
    for ((index, width) in widths.withIndex()) {
        val start = before + gap * index
        if (start + width > viewport) {
            if (index == 0 || viewport - start >= peek) return gap
            val narrower = (viewport - peek - before) / index
            return if (narrower >= least) minOf(narrower, gap) else gap
        }
        before += width
    }
    return gap
}

/**
 * A [JSegment] with a toned count: its label, then the count's capsule (none at 0); [onFill]: on the selected pill's fill.
 * [label] draws the label (Glass's fades with its sliding pill).
 */
@Composable
private fun TonedSegmentLabel(segment: JSegment, style: androidx.compose.ui.text.TextStyle, color: Color, modifier: Modifier = Modifier,
                              onFill: Boolean = false,
                              label: @Composable (String) -> Unit = { Text(it, style = style, color = color, maxLines = 1, softWrap = false, overflow = TextOverflow.Ellipsis) }) {
    Row(modifier, verticalAlignment = Alignment.CenterVertically) {
        label(segment.label)
        val tone = segment.countTone ?: return@Row
        segment.count?.takeIf { it > 0 }?.let { Spacer(Modifier.width(JChipTokens.countGap)); JToneCount(it, tone, onFill = onFill) }
    }
}

/**
 * While [selected], a choice in a row that scrolls sideways with [scroll] is scrolled whole into sight, by as little as it
 * takes, when it is first placed and whenever it becomes selected (a search or a link choosing it, not only a tap); one
 * already in sight stays where it is. [margin]: it stays that far from an edge the row can still scroll past, clear of
 * a fade there. Only that row moves, never the page around it. Null [scroll]: nothing.
 */
fun Modifier.revealWhenSelected(scroll: androidx.compose.foundation.ScrollState?, selected: Boolean, margin: Dp = Tokens.Space.flush): Modifier =
    if (scroll == null) this else this.then(RevealWhenSelected(scroll, selected, margin))

private data class RevealWhenSelected(val scroll: androidx.compose.foundation.ScrollState, val selected: Boolean, val margin: Dp) :
    androidx.compose.ui.node.ModifierNodeElement<RevealNode>() {
    override fun create() = RevealNode(scroll, selected, margin)
    override fun update(node: RevealNode) = node.update(scroll, selected, margin)
    override fun androidx.compose.ui.platform.InspectorInfo.inspectableProperties() {
        name = "revealWhenSelected"
        properties["selected"] = selected
        properties["margin"] = margin
    }
}

private class RevealNode(private var scroll: androidx.compose.foundation.ScrollState, private var selected: Boolean, private var margin: Dp) :
    Modifier.Node(), androidx.compose.ui.node.LayoutAwareModifierNode {
    /** Where it sits along the row's content, from its last placement. */
    private var start = -1
    private var end = -1
    private var pending = selected

    override fun onPlaced(coordinates: androidx.compose.ui.layout.LayoutCoordinates) {
        start = coordinates.positionInParent().x.toInt()
        end = start + coordinates.size.width
        if (pending) reveal()
    }

    fun update(scroll: androidx.compose.foundation.ScrollState, selected: Boolean, margin: Dp) {
        this.scroll = scroll
        this.margin = margin
        if (selected && !this.selected) pending = true
        this.selected = selected
        if (pending) reveal()
    }

    private fun reveal() {
        if (start < 0 || !isAttached) return
        pending = false
        if (!selected) return
        coroutineScope.launch {
            // Once the frame that placed it is done: a scroll written while the row is still being placed (its first
            // frame, a choice made from outside) was taken but never moved the row.
            androidx.compose.runtime.withFrameNanos { }
            val viewport = scroll.viewportSize
            // Clear of a fade at an edge with more past it; the row stops at its ends, where nothing fades.
            val inset = with(requireDensity()) { margin.roundToPx() }.coerceAtMost((viewport - (end - start)) / 2).coerceAtLeast(0)
            val target = when {
                !selected || viewport <= 0 -> return@launch
                start - inset < scroll.value -> start - inset
                end + inset > scroll.value + viewport -> end + inset - viewport
                else -> return@launch
            }
            scroll.scrollTo(target.coerceIn(0, scroll.maxValue))
        }
    }
}

/** What TalkBack hears for a choice with a toned count: its name and the exact number ("Needs you, 12"), the name alone at 0. */
@Composable
internal fun spokenCount(label: String, count: Int?): String =
    count?.takeIf { it > 0 }?.let { stringResource(R.string.designsystem_count_spoken, label, it) } ?: label

/** A count's words in a badge: "99+" past 99 (iOS `CountBadge.text`). */
internal fun countBadgeText(count: Int): String = if (count > 99) "99+" else "$count"

/**
 * A count in a capsule of [tone]'s colours, as [JSectionHeader]'s `countTone` draws it (iOS `CountBadge`: Needs you's look
 * for what waits for the user, [Status.Idle]'s for a neutral total): nothing at 0, "99+" past 99. Pass the exact number
 * in the control's spoken name ([spokenCount]).
 */
@Composable
internal fun JToneCount(count: Int, tone: Status, modifier: Modifier = Modifier, onFill: Boolean = false) {
    if (count <= 0) return
    val colors = PersonaTheme.palette.status(tone)
    // On a selected chip's fill the tint alone could vanish (a grey on black): it sits on the page's opaque surface.
    Text(countBadgeText(count), modifier.clip(CircleShape).then(if (onFill) Modifier.background(PersonaTheme.palette.surfaceOpaque) else Modifier).background(colors.tint)
        .padding(horizontal = SectionHeaderTokens.countPadding, vertical = SectionHeaderTokens.countPaddingVertical),
        style = PersonaTheme.type.micro.copy(fontWeight = FontWeight.SemiBold), color = colors.text, maxLines = 1, softWrap = false)
}

/**
 * The filter chip's measurements, shared by every control that must read as one of these short capsules (the old All /
 * task chips above the composer, and now the Assistant's Pending / Progress / Agents entries).
 */
object JChipTokens {
    /** A tappable chip's minimum height. */
    val height = 36.dp
    /** A quiet (not tappable) chip's minimum height. */
    val quietHeight = 28.dp
    val horizontalPadding = 12.dp
    val border = 1.dp
    val dotSize = 8.dp
    val dotGap = 6.dp
    val iconSize = 15.dp
    val iconGap = 5.dp
    val countGap = 5.dp
}

/**
 * A filter or a fact. With [onClick] it toggles; without, it is a quiet label. [onLongClick] (with [onClick]) is a long
 * press for more (a menu of other ways to use it); the chip looks the same. [dot] is a plain colored mark in the place
 * of a [status] dot, for a category rather than a state. Pass [Role.Button] as [role] for an action instead of a toggle.
 * [trailingIcon] adds a compact affordance such as an expansion chevron. [breathes]: false keeps a Needs You [status] dot
 * still, as iOS's quick filters draw it ([JStatusDot]).
 */
@Composable
fun JChip(
    text: String,
    modifier: Modifier = Modifier,
    selected: Boolean = false,
    onClick: (() -> Unit)? = null,
    icon: PhIcon? = null,
    status: Status? = null,
    count: Int? = null,
    /** Greyed text (not selected): what it stands for is not reachable now, like an offline manager. */
    muted: Boolean = false,
    onLongClick: (() -> Unit)? = null,
    dot: Color? = null,
    role: Role = Role.Checkbox,
    trailingIcon: PhIcon? = null,
    /** The accent's soft fill and the accent's ink (not selected; iOS `Chip`'s tint): a choice that stays on, such as
     *  the task the next message goes to. Board keeps its edge. */
    accent: Boolean = false,
    /** A × of its own at the end that takes the thing away, read as [removeLabel]; the chip is then as tall as a
     *  tappable one, so the × has its full touch target. */
    onRemove: (() -> Unit)? = null,
    removeLabel: String? = null,
    /** Bounds the label inside a weighted chip while keeping its icons and count visible. */
    boundedLabel: Boolean = false,
    breathes: Boolean = true,
    /** The [count] in a capsule of this state's colours ([JToneCount]: none at 0, 99+ past 99), spoken with the text
     *  ("Needs you, 12"): the Inbox's section bar (#9053). None: quiet digits, as before. */
    countTone: Status? = null,
    /** Gives a standalone chip an opaque surface using its usual style colors. */
    opaque: Boolean = false,
) {
    val palette = PersonaTheme.palette
    val style = PersonaTheme.style
    val shape = RoundedCornerShape(PersonaTheme.shapes.chip)
    val spoken = countTone?.let { spokenCount(text, count) }
    val edgeless = Color.Transparent
    val (fill, plainInk, border) = when {
        accent && !selected -> Triple(palette.accentSoft, palette.accent, if (style == AppStyle.Board) palette.line else edgeless)
        style == AppStyle.Glass -> Triple(if (selected) palette.action else palette.surfaceStrong, if (selected) palette.onAction else palette.ink, if (selected) edgeless else palette.strokeBottom)
        style == AppStyle.Brief -> Triple(if (selected) palette.action else palette.surface, if (selected) palette.onAction else palette.ink, edgeless)
        else -> Triple(if (selected) palette.statusSurface else palette.surface, palette.ink, if (selected) palette.accent else palette.line)
    }
    val ink = if (muted && (!selected || style == AppStyle.Board)) palette.ink3 else plainInk
    val tall = onClick != null || onRemove != null
    Row(
        modifier.heightIn(min = if (tall) JChipTokens.height else JChipTokens.quietHeight)
            .then(if (tall) Modifier.minimumInteractiveComponentSize() else Modifier)
            .clip(shape).background(if (opaque) fill.compositeOn(palette.surfaceOpaque) else fill)
            .border(JChipTokens.border, border, shape)
            .then(when {
                onClick != null && onLongClick != null ->
                    Modifier.combinedClickable(role = role, onClick = onClick, onLongClick = onLongClick)
                        .semantics { if (role == Role.Checkbox) this.selected = selected }
                onClick != null -> Modifier.clickable(role = role, onClick = onClick)
                    .semantics { if (role == Role.Checkbox) this.selected = selected }
                else -> Modifier
            })
            .then(if (spoken != null) Modifier.semantics { contentDescription = spoken } else Modifier)
            .padding(start = JChipTokens.horizontalPadding, end = if (onRemove != null) Tokens.Space.xs else JChipTokens.horizontalPadding),
        verticalAlignment = Alignment.CenterVertically,
    ) {
        status?.let { JStatusDot(it, size = JChipTokens.dotSize, breathes = breathes); Spacer(Modifier.width(JChipTokens.dotGap)) }
        if (status == null) dot?.let { Box(Modifier.size(JChipTokens.dotSize).clip(CircleShape).background(it)); Spacer(Modifier.width(JChipTokens.dotGap)) }
        icon?.let { JIcon(it, null, size = JChipTokens.iconSize, tint = ink); Spacer(Modifier.width(JChipTokens.iconGap)) }
        // With a × or a bounded label, long text gives way instead of pushing out the rest of the chip.
        Text(
            text,
            modifier = if (onRemove != null || boundedLabel) Modifier.weight(1f, fill = false) else Modifier,
            style = PersonaTheme.type.labelSmall.copy(fontWeight = FontWeight.Medium),
            color = ink,
            maxLines = 1,
            overflow = TextOverflow.Ellipsis,
        )
        if (countTone != null) count?.takeIf { it > 0 }?.let {
            Spacer(Modifier.width(JChipTokens.countGap))
            JToneCount(it, countTone, onFill = selected && style != AppStyle.Board)
        } else count?.let {
            Spacer(Modifier.width(JChipTokens.countGap))
            Text("$it", style = PersonaTheme.type.micro, color = if (selected && style != AppStyle.Board) ink else palette.ink3, maxLines = 1, softWrap = false)
        }
        trailingIcon?.let {
            Spacer(Modifier.width(JChipTokens.iconGap))
            JIcon(it, null, size = JChipTokens.iconSize, tint = ink)
        }
        onRemove?.let { remove ->
            Spacer(Modifier.width(Tokens.Space.xs))
            Box(Modifier.size(Tokens.Size.icon).clip(CircleShape).clickable(role = Role.Button, onClick = remove)
                .semantics { removeLabel?.let { contentDescription = it } }, contentAlignment = Alignment.Center) {
                JIcon(persona.core.designsystem.icons.PersonaIcons.close, null, size = JChipTokens.iconSize, tint = ink)
            }
        }
    }
}

/** A count waiting for the user (tab badges, request counts). */
@Composable
fun JBadge(count: Int, modifier: Modifier = Modifier, text: String? = null) {
    val palette = PersonaTheme.palette
    val label = text ?: if (count > 99) "99+" else "$count"
    if (LocalHomeDrawer.current && PersonaTheme.style == AppStyle.Glass) {
        val fill = sidebarActionFill(palette)
        Box(modifier.heightIn(min = HomeTokens.sidebarBadgeSize).widthIn(min = HomeTokens.sidebarBadgeSize)
            .clip(CircleShape).background(fill).padding(horizontal = Tokens.Space.xs), contentAlignment = Alignment.Center) {
            Text(label, style = PersonaTheme.type.caption.copy(fontWeight = FontWeight.SemiBold),
                color = palette.onAccentFill(fill), maxLines = 1)
        }
        return
    }
    val shape = if (PersonaTheme.style == AppStyle.Board) RoundedCornerShape(4.dp) else CircleShape
    Box(
        modifier.heightIn(min = 18.dp).widthIn(min = 18.dp).clip(shape).background(palette.needsYou.dot)
            .border(1.5.dp, palette.background, shape).padding(horizontal = 5.dp.dense),
        contentAlignment = Alignment.Center,
    ) { Text(label, style = PersonaTheme.type.micro.copy(fontWeight = FontWeight.Bold, fontSize = androidx.compose.ui.unit.TextUnit(11f, androidx.compose.ui.unit.TextUnitType.Sp), lineHeight = androidx.compose.ui.unit.TextUnit(14f, androidx.compose.ui.unit.TextUnitType.Sp)), color = palette.needsYou.onDot, maxLines = 1) }
}

/** A small static label such as "Draft", "Demo", "Read-only". */
@Composable
fun JTag(text: String, modifier: Modifier = Modifier, status: Status? = null) =
    JTagColored(text, modifier, status?.let { PersonaTheme.palette.status(it) })

/**
 * A small label in a role colour ([Tone]): an extension's badge ("Valid", "Expiring soon"). [animated]: drawn again with another tone
 * its colours go over to the new ones and a new text fades in (an extension's row badge, #2811).
 */
@Composable
fun JTag(text: String, tone: Tone, modifier: Modifier = Modifier, animated: Boolean = false) =
    JTagColored(text, modifier, PersonaTheme.palette.tone(tone).takeIf { tone != Tone.Neutral }, animated)

/** A small label in a named colour ([JHue], UI version 6): a category ("客厅") rather than a meaning. */
@Composable
fun JTag(text: String, hue: JHue, modifier: Modifier = Modifier, animated: Boolean = false) =
    JTagColored(text, modifier, PersonaTheme.palette.hue(hue), animated)

@Composable
internal fun JTagColored(text: String, modifier: Modifier, tone: StatusColors?, animated: Boolean = false) {
    val palette = PersonaTheme.palette
    val shape = RoundedCornerShape(if (PersonaTheme.style == AppStyle.Board) 4.dp else 6.dp.corner)
    val style = PersonaTheme.type.micro.copy(fontWeight = FontWeight.SemiBold)
    var ink = tone?.text ?: palette.ink2
    var fill = palette.statusSurface
    if (animated) {
        val spec: AnimationSpec<Color> = if (PersonaTheme.reduceMotion) snap() else tween(PersonaTheme.motion.standard)
        ink = animateColorAsState(ink, spec, label = "tag ink").value
        fill = animateColorAsState(fill, spec, label = "tag fill").value
        Box(modifier.clip(shape).drawBehind { drawRect(fill) }.padding(TagPadding)) { JChangingText(text, style = style, color = ink) }
    } else Text(text, style = style, color = ink, maxLines = 1, modifier = modifier.clip(shape).background(fill).padding(TagPadding))
}

private val TagPadding = PaddingValues(horizontal = 6.dp.dense, vertical = 2.dp.dense)


// ------------------------------------------------------------------ progress

/** Fixed stroke widths for [JSpinner], independent of the page's spacing density. */
object SpinnerTokens {
    val compactStroke = Tokens.Android.Size.spinnerStroke
}

/**
 * The style's spinner: an ink arc in brief, an accent arc elsewhere. Static when motion is reduced. It reads its angle only while
 * drawing, so a turn draws the arc again and recomposes nothing (iOS's spinners turn off the main thread).
 */
@Composable
fun JSpinner(modifier: Modifier = Modifier, color: Color = if (PersonaTheme.style == AppStyle.Brief) PersonaTheme.palette.ink else PersonaTheme.palette.accent, stroke: Dp = 2.2.dp) {
    val turning = !PersonaTheme.reduceMotion
    val loading = stringResource(R.string.designsystem_loading)
    // Read while drawing only, never while composing: each frame draws the arc again and composes nothing.
    val angle = if (turning) rememberInfiniteTransition(label = "spin").animateFloat(0f, 360f, infiniteRepeatable(tween(1100, easing = LinearEasing)), label = "angle") else null
    Canvas(modifier.size(20.dp).clearAndSetSemantics { contentDescription = loading }) {
        val width = stroke.toPx()
        val inset = width / 2
        val arcSize = Size(size.width - width, size.height - width)
        drawArc(color.copy(alpha = 0.2f), 0f, 360f, false, Offset(inset, inset), arcSize, style = Stroke(width))
        drawArc(color, (angle?.value ?: 0f) - 90f, 100f, false, Offset(inset, inset), arcSize, style = Stroke(width, cap = StrokeCap.Round))
    }
}

@Composable
fun JLinearProgress(progress: Float?, modifier: Modifier = Modifier, tone: Tone? = null, color: Color? = null) {
    val palette = PersonaTheme.palette
    // A toned bar (an extension's progress) takes the role colour; otherwise the style's ink or accent.
    val effectiveColor = color ?: tone?.takeIf { it != Tone.Neutral }?.let { palette.tone(it).dot }
        ?: if (PersonaTheme.style == AppStyle.Brief) palette.ink else palette.accent
    Box(modifier.fillMaxWidth().height(4.dp).clip(CircleShape).background(palette.surfaceStrong)) {
        if (progress != null) Box(Modifier.fillMaxWidth(progress.coerceIn(0f, 1f)).fillMaxHeight().clip(CircleShape).background(effectiveColor))
        else {
            val shift by rememberInfiniteTransition(label = "bar").animateFloat(0f, 1f, infiniteRepeatable(tween(1200, easing = LinearEasing)), label = "shift")
            BoxWithConstraints(Modifier.fillMaxSize()) {
                Box(Modifier.offset(x = maxWidth * (shift * 1.3f - 0.3f)).width(maxWidth * 0.3f).fillMaxHeight().clip(CircleShape).background(effectiveColor))
            }
        }
    }
}
