package persona.core.designsystem

import androidx.compose.animation.core.FiniteAnimationSpec
import androidx.compose.animation.core.animateFloatAsState
import androidx.compose.animation.core.snap
import androidx.compose.animation.core.tween
import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.BoxWithConstraints
import androidx.compose.foundation.layout.fillMaxHeight
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.navigationBarsPadding
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.statusBarsPadding
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.systemGestureExclusion
import androidx.compose.ui.draw.clipToBounds
import androidx.compose.ui.draw.drawWithContent
import androidx.compose.ui.geometry.CornerRadius
import androidx.compose.ui.geometry.Offset
import androidx.compose.ui.geometry.Rect
import androidx.compose.ui.geometry.Size
import androidx.compose.ui.graphics.drawscope.Stroke
import androidx.compose.runtime.Composable
import androidx.compose.runtime.Immutable
import androidx.compose.runtime.compositionLocalOf
import androidx.compose.runtime.getValue
import androidx.compose.runtime.staticCompositionLocalOf
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.focus.focusProperties
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.graphicsLayer
import androidx.compose.ui.input.pointer.pointerInput
import androidx.compose.ui.layout.positionInWindow
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.semantics.Role
import androidx.compose.ui.semantics.clearAndSetSemantics
import androidx.compose.ui.semantics.contentDescription
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.unit.Dp
import androidx.compose.ui.unit.dp
import androidx.compose.ui.zIndex

class HomeChrome(
    val items: List<NavItem>,
    val selected: String,
    val onSelect: (String) -> Unit,
    val leading: @Composable () -> Unit,
    /** Where the home bar ends, in pixels from the page's top: what floats under it starts there (the link pill). */
    val barHeight: androidx.compose.runtime.MutableIntState? = null,
    val showSwitch: Boolean = true,
)

val LocalHomeChrome = staticCompositionLocalOf<HomeChrome?> { null }
val LocalHomeDrawer = staticCompositionLocalOf { false }

/**
 * Split-pane context for a detail page in a two-pane or overlaid list-detail layout (matching iOS `SplitDetail`).
 * Null in single-pane layouts, sheets, full-screen overlays, or sub-views (e.g. Files/Git) that keep their own Back.
 */
@Immutable
data class SplitDetailContext(
    val isFirstPage: Boolean,
    val listHidden: Boolean = false,
    val onShowList: (() -> Unit)? = null,
)

val LocalSplitDetail = compositionLocalOf<SplitDetailContext?> { null }

/** Leading header control resolved from [SplitDetailContext] and a page's own [onBack] callback. */
sealed interface DetailLeading {
    data object Hidden : DetailLeading
    data class ShowList(val onClick: () -> Unit) : DetailLeading
    data class Back(val onClick: () -> Unit) : DetailLeading
}

/**
 * Resolves the leading header control for a detail page (matching iOS `hidesBack` / `backShowsList`):
 * - First page with the list shown beside it: [DetailLeading.Hidden].
 * - First page with the list hidden or overlaid: [DetailLeading.ShowList] to bring the list back.
 * - Pushed page, compact/single-pane layout, or no split context: [DetailLeading.Back].
 */
fun resolveDetailLeading(split: SplitDetailContext?, onBack: (() -> Unit)?): DetailLeading? {
    if (onBack == null) return null
    if (split != null && split.isFirstPage) {
        if (split.listHidden) {
            split.onShowList?.let { return DetailLeading.ShowList(it) }
        } else {
            return DetailLeading.Hidden
        }
    }
    return DetailLeading.Back(onBack)
}

internal fun <T> drawerSpec(style: AppStyle, reduceMotion: Boolean): FiniteAnimationSpec<T> = when {
    reduceMotion -> tween(Tokens.Motion.reduced)
    style == AppStyle.Glass -> IosSpring(Tokens.Motion.Drawer.duration, Tokens.Motion.Drawer.bounce).spec()
    else -> tween(Motions.of(style).standard)
}

/**
 * The sidebar stays behind a full-size page that moves aside; opening it never changes navigation.
 * On a phone, [dragEnabled] lets the page follow a finger the way `DrawerPan` does
 * (`apple/Persona/Views/Assistant/AssistantSidebar.swift`). [onOpen] runs when that drag settles open.
 */
@Composable
fun JShellDrawer(
    opened: Boolean,
    onDismiss: () -> Unit,
    modifier: Modifier = Modifier,
    dragEnabled: Boolean = false,
    onOpen: () -> Unit = {},
    drawer: @Composable () -> Unit,
    content: @Composable () -> Unit,
) {
    val drag = rememberPhoneDrawerDrag(opened, onOpen, onDismiss, dragEnabled)
    // While a finger tracks, progress is read in the draw lambdas below. Reading it here would recompose this
    // drawer on every move. At rest [PhoneDrawerDrag.live] is false and progress is exactly 0 or 1, which is the
    // drawing from before finger tracking.
    val live = drag.live
    val progress = if (live) 0f else drag.progress
    val palette = PersonaTheme.palette
    val density = LocalDensity.current
    val shape = RoundedCornerShape(HomeTokens.drawerPageRadius)
    val close = stringResource(R.string.designsystem_close)
    // Hairline fades with progress (`DrawerPageMotion`, AssistantSidebar.swift:421). At rest it is the same line as before.
    val hairline = if (live) null else DrawerDrag.hairlineAlpha(progress, palette.line.alpha)
    val edge = if (hairline == null) Modifier else Modifier.border(
        Tokens.Space.Standard.xxs / 2,
        if (progress >= 1f) palette.line else palette.line.copy(alpha = hairline),
        shape,
    )
    // Gesture modifier stays inside the background so it does not sit in the layer that casts the page shadow.
    BoxWithConstraints(modifier.fillMaxSize().background(sidebarBackground()).then(drag.modifier)) {
        val drawerWidth = (maxWidth * HomeTokens.drawerFraction).coerceAtMost(HomeTokens.drawerMaximum)
        val widthPx = with(density) { drawerWidth.toPx() }
        val shadowPx = with(density) { HomeTokens.drawerShadow.toPx() }
        val liftFull = with(density) { Tokens.Space.s.toPx() }
        val shadow = Color.Black.copy(alpha = HomeTokens.drawerShadowAlpha)
        drag.widthPx = widthPx
        val showSidebar = opened || live || progress > 0f
        // The sidebar rises as the page slides off it (`AssistantSidebar.swift:69`, `Theme.Motion.drawerLift`).
        // At rest open the offset is zero, so no layer is added and the pixels match the drawer from before tracking.
        val sidebarLift = when {
            live -> Modifier.graphicsLayer { translationY = DrawerDrag.liftPx(drag.progress, liftFull) }
            DrawerDrag.liftPx(progress, liftFull) == 0f -> Modifier
            else -> Modifier.graphicsLayer { translationY = DrawerDrag.liftPx(progress, liftFull) }
        }
        val pageMotion = if (live) {
            Modifier.graphicsLayer {
                val p = drag.progress
                translationX = widthPx * p
                this.shape = shape
                clip = p > 0f
                shadowElevation = shadowPx * p
                ambientShadowColor = shadow
                spotShadowColor = shadow
            }.drawWithContent {
                drawContent()
                val p = drag.progress
                val hair = DrawerDrag.hairlineAlpha(p, palette.line.alpha) ?: return@drawWithContent
                val color = if (p >= 1f) palette.line else palette.line.copy(alpha = hair)
                val stroke = (Tokens.Space.Standard.xxs / 2).toPx()
                val inset = stroke / 2f
                val radius = (HomeTokens.drawerPageRadius.toPx() - inset).coerceAtLeast(0f)
                drawRoundRect(
                    color = color,
                    topLeft = Offset(inset, inset),
                    size = Size(size.width - stroke, size.height - stroke),
                    cornerRadius = CornerRadius(radius, radius),
                    style = Stroke(stroke),
                )
            }
        } else {
            Modifier.graphicsLayer {
                translationX = widthPx * progress
                this.shape = shape
                clip = progress > 0f
                shadowElevation = shadowPx * progress
                ambientShadowColor = shadow
                spotShadowColor = shadow
            }.then(edge)
        }
        HardwareShortcutHost(Modifier.fillMaxSize()) {
            if (showSidebar) {
                Box(Modifier.width(drawerWidth).fillMaxHeight().then(sidebarLift).statusBarsPadding().navigationBarsPadding()) {
                    androidx.compose.runtime.CompositionLocalProvider(LocalHomeDrawer provides (PersonaTheme.style == AppStyle.Glass)) { drawer() }
                }
            }
            Box(Modifier.fillMaxSize().then(pageMotion)) {
                // A closed drawer must not override a descendant overlay's focus restriction.
                Box(Modifier.fillMaxSize().then(if (opened)
                    Modifier.focusProperties { canFocus = false }.clearAndSetSemantics {} else Modifier)) { content() }
                if (showSidebar) {
                    val scrim = if (live) {
                        Modifier.graphicsLayer { alpha = drag.progress }.background(palette.scrim)
                    } else {
                        Modifier.background(palette.scrim.copy(alpha = palette.scrim.alpha * progress))
                    }
                    Box(Modifier.fillMaxSize().then(scrim)
                        .clickable(enabled = opened, role = Role.Button, onClick = onDismiss).semantics { contentDescription = close })
                }
            }
        }
    }
}

/**
 * The same sidebar as a column beside the page, the way an open iPhone Duo and an iPad show it (a regular window):
 * [width] wide, the page from [pageStart] on (past a separating fold). [overlay] keeps the page full width and dims it
 * while [shown]; a tap beside the column calls [onDismiss]. Without [sidebar] the page fills the box, and stays the
 * same composition either way, so folding or unfolding keeps everything the page holds.
 */
@Composable
fun JSidebarColumn(
    sidebar: (@Composable () -> Unit)?,
    width: Dp,
    pageStart: Dp,
    shown: Boolean,
    modifier: Modifier = Modifier,
    overlay: Boolean = false,
    onDismiss: () -> Unit = {},
    insetSidebar: Boolean = true,
    drags: Boolean = false,
    detailAtRoot: Boolean = true,
    /**
     * False while this pane is composed but not the selected tab (`Root.kt` keeps Persona with
     * `Modifier.unplaced()`). An unplaced node can still publish exclusion rects, so the edge must
     * stay with system Back on Agents and other tabs.
     */
    paneShown: Boolean = true,
    onDragSettle: (Boolean) -> Unit = {},
    content: @Composable () -> Unit,
) {
    // The finger drag is SplitColumnControl (`SidebarColumnDrag.kt`). Idle, and a column that does not drag, keeps
    // this layout and `sidebarColumnSpec` (the button, the scrim and Back).
    val drag = if (drags && sidebar != null) rememberColumnDragController(width, onDragSettle) else null
    val laidOut = drag?.layoutShown ?: shown
    val progress by animateFloatAsState(if (laidOut) 1f else 0f,
        if (drag?.snaps == true) snap() else sidebarColumnSpec(PersonaTheme.style, PersonaTheme.reduceMotion),
        label = "sidebar-column")
    val palette = PersonaTheme.palette
    val visible = sidebar != null && progress > 0f
    val presented = drag?.layoutShown ?: shown
    val modal = sidebar != null && overlay && presented
    val close = stringResource(R.string.designsystem_close)
    @Composable
    fun panes() {
        if (visible) Box(Modifier.width(width).fillMaxHeight()
            .zIndex(if (overlay) 1f else 0f)
            .then(if (overlay) Modifier.pointerInput(Unit) {
                // Blank column space belongs to the column too, never to the page underneath it.
                awaitPointerEventScope { while (true) awaitPointerEvent() }
            } else Modifier)
            .graphicsLayer { translationX = -(1f - progress) * width.toPx() }
            .background(sidebarBackground())) {
            Box(Modifier.fillMaxSize().then(if (insetSidebar) Modifier.statusBarsPadding().navigationBarsPadding() else Modifier)) {
                androidx.compose.runtime.CompositionLocalProvider(LocalHomeDrawer provides (PersonaTheme.style == AppStyle.Glass)) { sidebar?.invoke() }
            }
            Box(Modifier.align(Alignment.TopEnd).width(Tokens.Space.xxs / 2).fillMaxHeight().background(palette.line))
        }
        // The detail stack is that column's own navigation controller, so a push stays inside it
        // (SplitNavigation.swift:140-143). Clip after the inset, or the slide still paints the column.
        // Over the page the column stays above the page (SplitNavigation.swift:202-208).
        val beside = sidebar != null && !overlay
        Box(
            Modifier.fillMaxSize()
                .padding(start = if (beside) pageStart * progress else 0.dp)
                .then(if (beside) Modifier.clipToBounds() else Modifier),
        ) {
            Box(Modifier.fillMaxSize().then(if (modal)
                Modifier.focusProperties { canFocus = false }.clearAndSetSemantics {} else Modifier)) { content() }
            if (overlay && visible) Box(Modifier.fillMaxSize().padding(start = width * progress)
                .background(palette.scrim.copy(alpha = palette.scrim.alpha * progress))
                .clickable(enabled = shown, role = Role.Button, onClick = onDismiss).semantics { contentDescription = close })
        }
    }
    // One slot for the page. A second branch when the drag controller appears (a phone unfolding into a regular
    // window) composed the Assistant route twice in one frame. The pictures record `panes` only. The overlay is a
    // sibling, drawn above them (`SplitColumnControl.swift:204`).
    val gesture = if (drag != null) drag.gestureModifier(width, shown, detailAtRoot, overlay) else Modifier
    // While this tab is shown, the column is hidden and the detail is at its root, reclaim the open-zone
    // strip so system Back does not take it (`SplitColumnControl.swift:132-137`, `:160-169`). A pushed
    // detail, an open column, or an unselected tab keeps Back (no exclusion).
    val density = LocalDensity.current
    val excludeEdge = if (paneShown && drag != null && !shown && detailAtRoot) {
        val stripPx = with(density) { (Tokens.MinTouch.ios / 2).toPx() }
        val bandPx = with(density) { RootEdgeExclusion.heightDp.dp.toPx() }
        Modifier.systemGestureExclusion { coordinates ->
            RootEdgeExclusion.rect(
                active = true,
                stripWidthPx = stripPx,
                layoutWidthPx = coordinates.size.width.toFloat(),
                layoutHeightPx = coordinates.size.height.toFloat(),
                bandHeightPx = bandPx,
                // Horizontal safe-drawing / cutout pads the shell (`Root.kt`); system Back is from window x=0.
                layoutLeftInWindowPx = coordinates.positionInWindow().x,
            ) ?: Rect.Zero
        }
    } else {
        Modifier
    }
    // [progress] is this frame's layout. The picture waits until that value has caught up (`SplitColumnControl.swift:216-231`).
    val record = if (drag != null) drag.recordModifier(progress) else Modifier
    Box(modifier.fillMaxSize().then(excludeEdge).then(gesture)) {
        Box(Modifier.fillMaxSize().then(record)) { panes() }
        if (drag != null) ColumnDragOverlay(drag, drag.pagePicture, drag.columnPicture, drag.step)
    }
}

/** The sidebar's own surface, whether it is a drawer or a column. */
@Composable
private fun sidebarBackground(): Color = if (PersonaTheme.style == AppStyle.Glass) {
    if (PersonaTheme.darkTheme) HomeTokens.drawerDark else HomeTokens.drawerLight
} else PersonaTheme.palette.surfaceOpaque
