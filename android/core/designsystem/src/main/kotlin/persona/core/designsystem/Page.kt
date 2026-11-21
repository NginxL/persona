package persona.core.designsystem

import androidx.compose.ui.res.stringResource
import androidx.compose.animation.AnimatedVisibility
import androidx.compose.animation.core.animateFloatAsState
import androidx.compose.animation.core.tween
import androidx.compose.animation.Crossfade
import androidx.compose.animation.fadeIn
import androidx.compose.animation.fadeOut
import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.LazyListScope
import androidx.compose.foundation.lazy.LazyListState
import androidx.compose.foundation.lazy.rememberLazyListState
import androidx.compose.ui.input.nestedscroll.NestedScrollConnection
import androidx.compose.ui.input.nestedscroll.nestedScroll
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material3.ExperimentalMaterial3Api
import androidx.compose.material3.Text
import androidx.compose.material3.pulltorefresh.PullToRefreshBox
import androidx.compose.material3.pulltorefresh.PullToRefreshDefaults
import androidx.compose.material3.pulltorefresh.rememberPullToRefreshState
import androidx.compose.runtime.*
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clipToBounds
import androidx.compose.ui.geometry.Rect
import androidx.compose.ui.layout.SubcomposeLayout
import androidx.compose.ui.layout.onSizeChanged
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.platform.LocalView
import androidx.compose.ui.window.Dialog
import androidx.compose.ui.window.DialogProperties
import androidx.compose.ui.window.DialogWindowProvider
import androidx.core.view.WindowCompat
import androidx.compose.ui.semantics.Role
import androidx.compose.ui.semantics.heading
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.Dp
import androidx.compose.ui.unit.dp
import dev.chrisbanes.haze.HazeInput
import dev.chrisbanes.haze.HazeProgressive
import dev.chrisbanes.haze.HazeState
import dev.chrisbanes.haze.blur.HazeBlurStyle
import dev.chrisbanes.haze.blur.hazeBlur
import androidx.compose.ui.graphics.Brush
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.graphicsLayer
import persona.core.designsystem.icons.PhIcons
import java.time.LocalTime

/**
 * Room taken by app chrome that floats over pages: the glass dock at the bottom, the bar of a page that hosts this
 * one at the top (the Persona page around its conversation). The page still fills the whole area and its content
 * slides under that chrome; its own bars sit inside it. [handlesNavigationBar] means the chrome already sits above
 * the system navigation bar, so the page must not add that inset again.
 */
@Immutable
data class ChromeInsets(val bottom: Dp = 0.dp, val handlesNavigationBar: Boolean = false, val top: Dp = 0.dp)

val LocalChromeInsets = compositionLocalOf { ChromeInsets() }

/** The leading control of root pages (the hub avatar that opens settings), supplied by the shell. */
val LocalRootLeading = compositionLocalOf<(@Composable () -> Unit)?> { null }

/**
 * True when this entry is a sidebar place shown as the page root at regular width.
 * [LocalRootLeading] is also set by Settings, Engine and Pairing for other reasons, so it is not this test.
 */
val LocalIsSidebarRoot = compositionLocalOf { false }

/** Back for a root page the shell shows as a page of its own (settings outside the board style). */
val LocalRootBack = compositionLocalOf<(() -> Unit)?> { null }

private val BlurredScrollEdgeFade = 28.dp

/** A scroll edge's rendering and geometry. [overlap] is the part of the fade beyond the measured bar. */
@Immutable
data class ScrollEdgeStyle(val blur: Boolean, val fade: Dp, val overlap: Dp = Tokens.Space.flush) {
    init {
        require(fade.value.isFinite() && fade > Tokens.Space.flush && overlap >= Tokens.Space.flush && overlap <= fade) {
            "A scroll edge needs a positive fade and an overlap within that fade"
        }
    }

    companion object {
        val Blurred = ScrollEdgeStyle(blur = true, fade = BlurredScrollEdgeFade, overlap = BlurredScrollEdgeFade)
        val Gradient = ScrollEdgeStyle(blur = false, fade = Tokens.Space.l)
    }
}

/**
 * A page. Pass [content] the [PaddingValues] it receives as the content padding of its scrolling container:
 * in glass the bars float over the content, elsewhere they take their own space and the padding is small.
 * [topBar] replaces the default bar (the chat page brings its own); [bottomBar] holds a composer or actions.
 */
@Composable
fun JPage(
    title: String,
    modifier: Modifier = Modifier,
    subtitle: String? = null,
    onBack: (() -> Unit)? = null,
    actions: @Composable RowScope.() -> Unit = {},
    titleBusy: Boolean = false,
    onTitleClick: (() -> Unit)? = null,
    ambience: Ambience? = null,
    topBar: (@Composable () -> Unit)? = null,
    bottomBar: (@Composable () -> Unit)? = null,
    /**
     * False when the content is a page of its own that softens its edges itself (an embedded conversation); that page
     * is then the glass source this page's bars blur, not this page's content.
     */
    edges: Boolean = true,
    /** The edge style on both bars unless [topScrollEdgeStyle] overrides the top. */
    scrollEdgeStyle: ScrollEdgeStyle = ScrollEdgeStyle.Blurred,
    topScrollEdgeStyle: ScrollEdgeStyle = scrollEdgeStyle,
    /**
     * Whether content sits under the top bar right now; null (the default) keeps the top edge on. A page whose top
     * controls stay put passes it so the edge, which reaches below the bar, does not blur them while nothing scrolls.
     */
    underTop: (() -> Boolean)? = null,
    content: @Composable (PaddingValues) -> Unit,
) {
    val back = onBack ?: LocalRootBack.current
    PageFrame(modifier, ambience, topBar ?: { JTopBar(title, subtitle, back, actions, titleBusy, onTitleClick) }, bottomBar,
        underTop = underTop, edges = edges, topScrollEdgeStyle = topScrollEdgeStyle, bottomScrollEdgeStyle = scrollEdgeStyle,
        probePage = title, content = content)
}

@Composable
private fun PageFrame(
    modifier: Modifier,
    ambience: Ambience?,
    topBar: @Composable () -> Unit,
    bottomBar: (@Composable () -> Unit)?,
    /** Whether content sits under the top / bottom bars right now; null means it may at any time. */
    underTop: (() -> Boolean)? = null,
    underBottom: (() -> Boolean)? = null,
    edges: Boolean = true,
    topScrollEdgeStyle: ScrollEdgeStyle,
    bottomScrollEdgeStyle: ScrollEdgeStyle,
    probePage: String? = null,
    /** No reading inset: the content takes the pane's whole width ([JListPage]'s `fullWidth`). */
    fullWidth: Boolean = false,
    content: @Composable (PaddingValues) -> Unit,
) {
    val palette = PersonaTheme.palette
    val chrome = LocalChromeInsets.current
    // Read unconsumed navigation-bar insets inside layout measurement rather than composition: an ancestor or page
    // imePadding() consumes the IME height from the bottom of the window (which covers the navigation bar once open),
    // and horizontal shell insets consume side bars in landscape, without subscribing PageFrame to keyboard frames.
    val navigationInsets = WindowInsets.navigationBars
    var consumedInsets by remember { mutableStateOf(WindowInsets(0, 0, 0, 0)) }
    val glass = LocalGlassSource.current ?: remember { HazeState() }
    val window = LocalWindowLayout.current
    CompositionLocalProvider(LocalGlassSource provides glass) {
        if (PersonaTheme.style == AppStyle.Glass) {
            val statusBar = WindowInsets.statusBars.asPaddingValues().calculateTopPadding()
            val light = ambience ?: LocalAmbience.current
            // Bars and content are measured in one pass. In particular, a dock's real height decides whether the lower
            // side is usable; an onSizeChanged correction would first draw a frame across the hinge.
            SubcomposeLayout(modifier.fillMaxSize().onConsumedWindowInsetsChanged { consumedInsets = it }) { constraints ->
                val loose = constraints.copy(minWidth = 0, minHeight = 0)
                val inset = if (fullWidth) Tokens.Space.flush else readingInset(constraints.maxWidth.toDp())
                val unconsumedNavigation = navigationInsets.exclude(consumedInsets)
                val navigationBottom = unconsumedNavigation.getBottom(this)
                val navigation = navigationBottom.toDp()
                val bars = subcompose(PageSlot.Top) { topBar() }.map { it.measure(loose) }
                // A host's bar above this page (chrome.top): this page's own bars sit below it, the content under both.
                val above = chrome.top.roundToPx().coerceIn(0, constraints.maxHeight)
                val dockStart = if (chrome.handlesNavigationBar) 0 else unconsumedNavigation.getLeft(this, layoutDirection)
                val dockEnd = if (chrome.handlesNavigationBar) 0 else unconsumedNavigation.getRight(this, layoutDirection)
                // System and host insets are placement arithmetic, not part of the dock measurement: tabletop needs
                // the dock's actual height, while a flat page keeps exactly its former bottom inset behavior.
                val dock = if (bottomBar == null) emptyList() else subcompose(PageSlot.Bottom) {
                    Box(Modifier.fillMaxWidth().consumeWindowInsets(WindowInsets.navigationBars), contentAlignment = Alignment.TopCenter) {
                        // On wide panes the composer and bars stay as wide as the text they belong to.
                        Box(Modifier.widthIn(max = ReadingWidth + 40.dp)) { bottomBar() }
                    }
                }.map { it.measure(loose.copy(maxWidth = (constraints.maxWidth - dockStart - dockEnd).coerceAtLeast(0))) }
                val topHeight = above + (bars.maxOfOrNull { it.height } ?: 0)
                val dockHeight = dock.maxOfOrNull { it.height } ?: 0
                val flatDockInset = chrome.bottom.roundToPx() +
                    (if (chrome.handlesNavigationBar) 0 else navigationBottom)
                val pageLayout = tabletopPageLayout(window, topHeight,
                    bottomBarHeight = if (bottomBar == null) null else dockHeight,
                    totalHeight = constraints.maxHeight, minimumContentHeight = Tokens.MinTouch.android.roundToPx(),
                    bottomInset = flatDockInset)
                val split = pageLayout as? TabletopPageLayout.Split
                val top = topHeight.toDp()
                val floor = when {
                    split != null -> TabletopHingeSpacing.contentBottom
                    dock.isNotEmpty() -> (dockHeight + flatDockInset).toDp()
                    chrome.handlesNavigationBar -> chrome.bottom
                    else -> navigation
                }
                val bodyHeight = split?.bodyHeight ?: constraints.maxHeight
                val body = subcompose(PageSlot.Content) {
                    if (probePage != null) AdaptiveBoundsProbe(
                        probePage,
                        contentBounds = Rect(0f, 0f, constraints.maxWidth.toFloat(), bodyHeight.toFloat()),
                        hingeBounds = split?.let { Rect(0f, it.bodyHeight.toFloat(), constraints.maxWidth.toFloat(), it.hingeBottom.toFloat()) },
                    )
                    // A tap on the page's blank space puts the keyboard away (the bars and the dock are not in here).
                    // A page of its own in here (!edges) is the glass source itself: Haze does not let an effect draw
                    // an ancestor source, so with this one as well the inner page's edges and dock blurred nothing (#3040).
                    Box(Modifier.fillMaxSize().then(if (split != null) Modifier.clipToBounds() else Modifier)
                        .then(if (edges) Modifier.glassSource() else Modifier).dismissKeyboardOnBlankTap()) {
                        // The window's one light, not a block of this page's own: panes, nested pages and the
                        // shell around them paint the same pixels, so nothing shows where one ends.
                        PageBackdrop(ambience = light)
                        CompositionLocalProvider(LocalReadingInset provides inset) { content(PaddingValues(top = top, bottom = floor)) }
                    }
                }.map { it.measure(constraints.copy(minHeight = bodyHeight, maxHeight = bodyHeight)) }
                // Scroll edges: content sliding under the floating bars (and the gaps between their pieces) goes soft
                // instead of showing raw text next to the glass. They wash towards the page's own light, so with
                // nothing under the bars they cannot be seen (no band at the top or bottom of an empty page). List
                // pages drop them while nothing is under the bars, which also saves the blur. A page without a top bar
                // is embedded below other chrome (the link card): no edge at its top.
                val edgeStrips = if (!edges) emptyList() else subcompose(PageSlot.Edges) {
                    Box(Modifier.fillMaxSize()) {
                        val topEdge = if (topScrollEdgeStyle.blur) maxOf(statusBar + 20.dp, top) else top
                        // When there is no app dock, leave the system gesture area transparent.
                        val bottomEdge = when {
                            bottomScrollEdgeStyle.blur -> maxOf(navigation + 28.dp, floor)
                            dock.isNotEmpty() -> floor
                            else -> Tokens.Space.flush
                        }
                        if (top > 0.dp) EdgeWhen(underTop, Modifier.align(Alignment.TopCenter)) {
                            ScrollEdge(it, topEdge, top = true, light = light, style = topScrollEdgeStyle)
                        }
                        if (split == null && bottomEdge > Tokens.Space.flush) EdgeWhen(underBottom, Modifier.align(Alignment.BottomCenter)) {
                            ScrollEdge(it, bottomEdge, top = false, light = light, style = bottomScrollEdgeStyle)
                        }
                    }
                }.map { it.measure(constraints.copy(minHeight = bodyHeight, maxHeight = bodyHeight)) }
                // The hinge and the lower side continue the same background. Only a separately measured dock may add
                // interactive content below the hinge; an ordinary page has no lower content slot at all.
                val lower = if (split == null) emptyList() else subcompose(PageSlot.Lower) {
                    Box(Modifier.fillMaxSize()) { PageBackdrop(ambience = light) }
                }.map {
                    val lowerHeight = constraints.maxHeight - split.bodyHeight
                    it.measure(constraints.copy(minHeight = lowerHeight, maxHeight = lowerHeight))
                }
                layout(constraints.maxWidth, constraints.maxHeight) {
                    lower.forEach { it.place(0, split?.bodyHeight ?: 0) }
                    body.forEach { it.place(0, 0) }
                    edgeStrips.forEach { it.place(0, 0) }
                    bars.forEach { it.place(0, above) }
                    dock.forEach {
                        val dockPlacement = split?.dockPlacement as? TabletopDockPlacement.BottomBar
                        val x = if (split != null) dockStart + (constraints.maxWidth - dockStart - dockEnd - it.width) / 2 else dockStart
                        it.place(x, dockPlacement?.top ?: (constraints.maxHeight - dockHeight - flatDockInset))
                    }
                }
            }
        } else {
            // Brief and Board use the same measured policy. Their bars take layout space instead of floating, but the
            // content slot and its state stay identical when a posture change selects split or single layout.
            SubcomposeLayout(
                modifier.fillMaxSize().background(palette.background).onConsumedWindowInsetsChanged { consumedInsets = it },
            ) { constraints ->
                val loose = constraints.copy(minWidth = 0, minHeight = 0)
                val inset = if (fullWidth) Tokens.Space.flush else readingInset(constraints.maxWidth.toDp())
                val unconsumedNavigation = navigationInsets.exclude(consumedInsets)
                val navigationBottom = unconsumedNavigation.getBottom(this)
                val navigation = navigationBottom.toDp()
                val bars = subcompose(PageSlot.Top) { topBar() }.map { it.measure(loose) }
                val above = chrome.top.roundToPx().coerceIn(0, constraints.maxHeight)
                val dockStart = if (chrome.handlesNavigationBar) 0 else unconsumedNavigation.getLeft(this, layoutDirection)
                val dockEnd = if (chrome.handlesNavigationBar) 0 else unconsumedNavigation.getRight(this, layoutDirection)
                val dock = if (bottomBar == null) emptyList() else subcompose(PageSlot.Bottom) {
                    Box(Modifier.fillMaxWidth().consumeWindowInsets(WindowInsets.navigationBars), contentAlignment = Alignment.TopCenter) {
                        Box(Modifier.widthIn(max = ReadingWidth + 40.dp)) { bottomBar() }
                    }
                }.map { it.measure(loose.copy(maxWidth = (constraints.maxWidth - dockStart - dockEnd).coerceAtLeast(0))) }
                val topHeight = above + (bars.maxOfOrNull { it.height } ?: 0)
                val dockHeight = dock.maxOfOrNull { it.height } ?: 0
                val flatDockInset = chrome.bottom.roundToPx() +
                    (if (chrome.handlesNavigationBar) 0 else navigationBottom)
                val pageLayout = tabletopPageLayout(window, topHeight,
                    bottomBarHeight = if (bottomBar == null) null else dockHeight,
                    totalHeight = constraints.maxHeight, minimumContentHeight = Tokens.MinTouch.android.roundToPx(),
                    bottomInset = flatDockInset)
                val split = pageLayout as? TabletopPageLayout.Split
                val contentBottom = when {
                    split != null -> TabletopHingeSpacing.contentBottom
                    bottomBar != null -> 0.dp
                    chrome.handlesNavigationBar -> chrome.bottom
                    else -> navigation
                }
                val dockSpace = if (dock.isEmpty()) 0 else dockHeight + flatDockInset
                val contentHeight = ((split?.bodyHeight ?: constraints.maxHeight - dockSpace) - topHeight).coerceAtLeast(0)
                val body = subcompose(PageSlot.Content) {
                    if (probePage != null) AdaptiveBoundsProbe(
                        probePage,
                        contentBounds = Rect(0f, topHeight.toFloat(), constraints.maxWidth.toFloat(), (topHeight + contentHeight).toFloat()),
                        hingeBounds = split?.let { Rect(0f, it.bodyHeight.toFloat(), constraints.maxWidth.toFloat(), it.hingeBottom.toFloat()) },
                    )
                    Box(Modifier.fillMaxSize().then(if (split != null) Modifier.clipToBounds() else Modifier)
                        .dismissKeyboardOnBlankTap()) {
                        CompositionLocalProvider(LocalReadingInset provides inset) { content(PaddingValues(bottom = contentBottom)) }
                    }
                }.map { it.measure(constraints.copy(minHeight = contentHeight, maxHeight = contentHeight)) }
                layout(constraints.maxWidth, constraints.maxHeight) {
                    body.forEach { it.place(0, topHeight) }
                    bars.forEach { it.place(0, above) }
                    dock.forEach {
                        val dockPlacement = split?.dockPlacement as? TabletopDockPlacement.BottomBar
                        val x = if (split != null) dockStart + (constraints.maxWidth - dockStart - dockEnd - it.width) / 2 else dockStart
                        // Flat keeps the original Column order: after topBar and the (possibly zero-height) weighted content.
                        it.place(x, dockPlacement?.top ?: (topHeight + contentHeight))
                    }
                }
            }
        }
    }
}

private enum class PageSlot { Top, Bottom, Content, Edges, Lower }

/** Fades [edge] in while [under] holds (always, when null) and drops it entirely when it is gone. */
@Composable
private fun EdgeWhen(under: (() -> Boolean)?, modifier: Modifier, edge: @Composable (Modifier) -> Unit) {
    if (under == null) { edge(modifier); return }
    val show by remember(under) { derivedStateOf(under) }
    val alpha by animateFloatAsState(if (show) 1f else 0f, tween(180), label = "edge")
    if (alpha > 0f) edge(modifier.graphicsLayer { this.alpha = alpha })
}

/**
 * Softens content as it scrolls behind a bar. Both treatments wash toward the page's own light ([PageBackdrop]),
 * so empty space has no visible band. A gradient without blur looks the same at every glass level.
 */
@Composable
internal fun ScrollEdge(
    modifier: Modifier, height: Dp, top: Boolean, light: Ambience, style: ScrollEdgeStyle = ScrollEdgeStyle.Blurred,
) {
    val state = LocalGlassSource.current
    val edgeHeight = height + style.overlap
    val strip = modifier.fillMaxWidth().height(edgeHeight)
    val density = LocalDensity.current
    val full = with(density) { edgeHeight.toPx() }
    val soft = with(density) { style.fade.toPx() }.coerceAtMost(full)
    fun wash(strength: Float): Brush {
        val solid = Color.Black.copy(alpha = strength)
        val stop = (full - soft) / full
        return if (top) Brush.verticalGradient(0f to solid, stop to solid, 1f to Color.Transparent)
        else Brush.verticalGradient(0f to Color.Transparent, (1f - stop) to solid, 1f to solid)
    }
    if (style.blur && state != null && PersonaTheme.glass != GlassLevel.Solid) {
        Box(strip.hazeBlur(HazeInput.Sources(state), style = HazeBlurStyle {
            blurRadius(18.dp)
            noiseFactor(0f)
            progressive(
                if (top) HazeProgressive.verticalGradient(startY = full - soft, startIntensity = 1f, endY = full, endIntensity = 0f)
                else HazeProgressive.verticalGradient(startY = 0f, startIntensity = 0f, endY = soft, endIntensity = 1f),
            )
        })) { AmbientLight(light, Modifier.matchParentSize(), inWindow = true, mask = wash(0.5f)) }
    } else {
        Box(strip) { AmbientLight(light, Modifier.matchParentSize(), inWindow = true, mask = wash(0.92f)) }
    }
}

/**
 * A page above the current one that needs the whole screen (search, a full text, a space, a past conversation):
 * edge to edge in the page background, system bar icons in the theme's tone, back closes it.
 */
@Composable
fun JFullScreen(onDismiss: () -> Unit, content: @Composable () -> Unit) {
    Dialog(onDismiss, DialogProperties(usePlatformDefaultWidth = false, decorFitsSystemWindows = false)) {
        val view = LocalView.current
        val dark = PersonaTheme.darkTheme
        SideEffect {
            (view.parent as? DialogWindowProvider)?.window?.let { window ->
                window.setDimAmount(0f)
                window.isNavigationBarContrastEnforced = false
                WindowCompat.getInsetsController(window, view).run {
                    isAppearanceLightStatusBars = !dark
                    isAppearanceLightNavigationBars = !dark
                }
            }
        }
        CompositionLocalProvider(LocalSplitDetail provides null) {
            Box(Modifier.fillMaxSize().background(PersonaTheme.palette.background)) { content() }
        }
    }
}

/** Page actions along the bottom: a floating glass capsule, a docked bar under a hairline elsewhere. */
@Composable
fun JBottomBar(modifier: Modifier = Modifier, content: @Composable () -> Unit) {
    val palette = PersonaTheme.palette
    when (PersonaTheme.style) {
        AppStyle.Glass -> Box(
            modifier.fillMaxWidth().padding(horizontal = 10.dp.dense, vertical = 8.dp.dense)
                .floatingSurface(RoundedCornerShape(28.dp.corner)).padding(horizontal = 12.dp.dense, vertical = 6.dp.dense),
        ) { content() }
        // The page's own background under a hairline: no second surface along the bottom.
        else -> Column(modifier.fillMaxWidth().background(palette.background)) {
            JDivider()
            Box(Modifier.padding(horizontal = 12.dp.dense, vertical = 6.dp.dense)) { content() }
        }
    }
}

/**
 * The page bar of the active style: a floating glass capsule, a quiet paper bar, or a compact board header.
 * [showTitle] false hides the title while a large title is on screen.
 */
@Composable
fun JTopBar(
    title: String,
    subtitle: String? = null,
    onBack: (() -> Unit)? = null,
    actions: @Composable RowScope.() -> Unit = {},
    titleBusy: Boolean = false,
    onTitleClick: (() -> Unit)? = null,
    showTitle: Boolean = true,
) {
    val home = LocalHomeChrome.current?.takeIf { onBack == null }
    if (home?.showSwitch == true) {
        CompositionLocalProvider(LocalIconsRaised provides true) {
            HomeNavigation(home.items, home.selected, home.onSelect, home.leading,
                modifier = home.barHeight?.let { height -> Modifier.onSizeChanged { height.intValue = it.height } } ?: Modifier,
                trailing = actions, onSelectedMenu = onTitleClick)
        }
        return
    }
    val palette = PersonaTheme.palette
    val style = PersonaTheme.style
    val compactHeight = 56.dp
    val barModifier = home?.barHeight?.let { height -> Modifier.onSizeChanged { height.intValue = it.height } } ?: Modifier
    val rootLeading = LocalRootLeading.current
    val splitLeading = resolveDetailLeading(LocalSplitDetail.current, onBack)
    val leading: @Composable () -> Unit = {
        if (home != null) home.leading()
        else when (splitLeading) {
            is DetailLeading.ShowList -> JIconButton(persona.core.designsystem.icons.PersonaIcons.sidebar, stringResource(R.string.designsystem_sidebar), splitLeading.onClick)
            is DetailLeading.Back -> JBackButton(onClick = splitLeading.onClick)
            DetailLeading.Hidden -> Spacer(Modifier.width(if (style == AppStyle.Brief) Tokens.Space.s else Tokens.Space.sm))
            null -> rootLeading?.invoke()
        }
    }
    val titleBlock: @Composable RowScope.() -> Unit = {
        Row(
            Modifier.weight(1f).padding(horizontal = 4.dp.dense).then(if (onTitleClick != null) Modifier.clickable(role = Role.Button, onClick = onTitleClick) else Modifier),
            verticalAlignment = Alignment.CenterVertically,
        ) {
            if (titleBusy) { JSpinner(Modifier.size(16.dp), stroke = 2.dp); Spacer(Modifier.width(8.dp.dense)) }
            AnimatedVisibility(showTitle, enter = fadeIn(), exit = fadeOut()) {
                Column {
                    Text(
                        title, maxLines = 1, overflow = TextOverflow.Ellipsis, color = palette.ink, modifier = Modifier.semantics { heading() },
                        style = when (style) { AppStyle.Board -> PersonaTheme.type.largeTitle.copy(fontSize = androidx.compose.ui.unit.TextUnit(20f, androidx.compose.ui.unit.TextUnitType.Sp)); else -> PersonaTheme.type.title.copy(fontSize = androidx.compose.ui.unit.TextUnit(17f, androidx.compose.ui.unit.TextUnitType.Sp)) },
                    )
                    subtitle?.let { Text(it, style = PersonaTheme.type.caption, color = palette.ink2, maxLines = 1, overflow = TextOverflow.Ellipsis) }
                }
            }
        }
    }
    when (style) {
        AppStyle.Glass -> {
            val shape = RoundedCornerShape(30.dp.corner)
            Box(barModifier.fillMaxWidth().statusBarsPadding().padding(horizontal = 10.dp.dense, vertical = 6.dp.dense)) {
                Crossfade(showTitle, label = "bar") { full ->
                    if (full) Row(
                        Modifier.fillMaxWidth().height(60.dp).floatingSurface(shape).padding(horizontal = 6.dp.dense),
                        verticalAlignment = Alignment.CenterVertically,
                    ) { leading(); titleBlock(); actions() }
                    else CompositionLocalProvider(LocalIconsRaised provides true) {
                        // Before a large title scrolls away, each control floats on its own above it.
                        Row(Modifier.fillMaxWidth().height(60.dp), verticalAlignment = Alignment.CenterVertically) {
                            leading()
                            Spacer(Modifier.weight(1f))
                            actions()
                        }
                    }
                }
            }
        }
        else -> Row(
            // Board keeps its summary beside the title here. Let their measured lines grow the bar; PageFrame uses
            // that same measured height to place the list and its pull-refresh content. Brief keeps its fixed bar.
            barModifier.fillMaxWidth().background(palette.background).statusBarsPadding()
                .then(if (style == AppStyle.Board) Modifier.heightIn(min = compactHeight) else Modifier.height(compactHeight))
                .padding(horizontal = 4.dp.dense),
            verticalAlignment = Alignment.CenterVertically,
        ) { leading(); titleBlock(); actions() }
    }
}

/** Icon buttons in a glass bar that is not drawn yet float as glass circles of their own. */
val LocalIconsRaised = compositionLocalOf { false }

@Composable
fun JBackButton(enabled: Boolean = true, onClick: () -> Unit) {
    val icon = if (PersonaTheme.style == AppStyle.Glass) PhIcons.CaretLeft else PhIcons.ArrowLeft
    JIconButton(icon, stringResource(R.string.designsystem_back), onClick,
        enabled = enabled, raised = enabled && LocalIconsRaised.current)
}

/**
 * A root or list page whose content is a lazy list. Glass and brief show a large title that scrolls away and
 * hands over to the bar; the board keeps a compact title. [summary] is Persona speaking about the page (brief
 * sets it as the headline; glass shows it under the title). [greet] adds the date and a greeting (home only).
 */
@OptIn(ExperimentalMaterial3Api::class)
@Composable
fun JListPage(
    title: String,
    modifier: Modifier = Modifier,
    subtitle: String? = null,
    summary: String? = null,
    greet: Boolean = false,
    onBack: (() -> Unit)? = null,
    actions: @Composable RowScope.() -> Unit = {},
    titleBusy: Boolean = false,
    onTitleClick: (() -> Unit)? = null,
    ambience: Ambience? = null,
    state: LazyListState = rememberLazyListState(),
    refreshing: Boolean = false,
    onRefresh: (() -> Unit)? = null,
    largeTitle: Boolean = onBack == null,
    bottomBar: (@Composable () -> Unit)? = null,
    /** False when the list should stop above the bottom inset instead of drawing a scroll edge over it. */
    scrollUnderBottom: Boolean = true,
    scrollEdgeStyle: ScrollEdgeStyle = ScrollEdgeStyle.Blurred,
    /** The content takes the pane's whole width rather than [ReadingWidth]: a grid of cards (an extension's dashboard page). */
    fullWidth: Boolean = false,
    /** The list's scroll, so a tap spring can ignore a drag (`listTapScroll`). */
    listScroll: NestedScrollConnection? = null,
    content: LazyListScope.() -> Unit,
) {
    val style = PersonaTheme.style
    val large = largeTitle && style != AppStyle.Board
    // iOS keeps this title inline (InboxView.swift:152-153). Ending a selection inserts nothing and
    // does not shift the list (the same withAnimation as the row close, :632). The large title just
    // appears; rows spring only when the tap changed their order.
    val collapsed by remember(state, large) { derivedStateOf { !large || state.firstVisibleItemIndex > 0 || state.firstVisibleItemScrollOffset > 120 } }
    val barSubtitle = if (style == AppStyle.Board) subtitle ?: summary else null
    AdaptiveListProbe(page = title, state = state)
    PageFrame(
        modifier, ambience,
        topBar = { JTopBar(title, barSubtitle.takeIf { collapsed }, onBack ?: LocalRootBack.current, actions, titleBusy, onTitleClick, showTitle = collapsed) },
        bottomBar = bottomBar,
        underTop = { state.canScrollBackward },
        underBottom = { scrollUnderBottom && state.canScrollForward },
        topScrollEdgeStyle = scrollEdgeStyle,
        bottomScrollEdgeStyle = scrollEdgeStyle,
        fullWidth = fullWidth,
    ) { padding ->
        val pad = pagePadding
        val bottom = padding.calculateBottomPadding()
        val viewport = if (scrollUnderBottom) Modifier.fillMaxSize() else Modifier.fillMaxSize().padding(bottom = bottom)
        val scrolled = if (listScroll == null) viewport else viewport.nestedScroll(listScroll)
        val list: @Composable () -> Unit = {
            LazyColumn(
                scrolled, state,
                contentPadding = PaddingValues(start = pad, end = pad, top = padding.calculateTopPadding() + 4.dp.dense,
                    bottom = if (scrollUnderBottom) bottom + Tokens.Space.section else Tokens.Space.section),
            ) {
                if (large) item("page-title", contentType = "page-title") {
                    PageTitle(title, subtitle, summary, greet, titleBusy, onTitleClick)
                }
                content()
            }
        }
        if (onRefresh == null) list()
        else {
            val pull = rememberPullToRefreshState()
            PullToRefreshBox(
                refreshing, onRefresh, Modifier.fillMaxSize(), state = pull,
                indicator = {
                    PullToRefreshDefaults.Indicator(
                        state = pull, isRefreshing = refreshing, modifier = Modifier.align(Alignment.TopCenter).padding(top = padding.calculateTopPadding()),
                        containerColor = PersonaTheme.palette.surfaceOpaque, color = if (style == AppStyle.Brief) PersonaTheme.palette.ink else PersonaTheme.palette.accent,
                    )
                },
            ) { list() }
        }
    }
}

@Composable
private fun PageTitle(
    title: String, subtitle: String?, summary: String?, greet: Boolean, busy: Boolean, onClick: (() -> Unit)?,
    modifier: Modifier = Modifier,
) {
    val palette = PersonaTheme.palette
    val type = PersonaTheme.type
    val click = if (onClick != null) Modifier.clickable(role = Role.Button, onClick = onClick) else Modifier
    Column(modifier.fillMaxWidth().padding(top = Tokens.Space.s, bottom = Tokens.Space.m)) {
        when (PersonaTheme.style) {
            AppStyle.Brief -> if (summary != null) {
                if (greet) Text(todayLabel(), style = type.caption, color = palette.ink3)
                Spacer(Modifier.height(6.dp.dense))
                Text(if (greet) stringResource(R.string.designsystem_greeting_summary, greeting(), summary) else summary, style = type.voice, color = palette.ink, modifier = click.semantics { heading() })
                subtitle?.let { Spacer(Modifier.height(4.dp.dense)); Text(it, style = type.voice.copy(fontSize = androidx.compose.ui.unit.TextUnit(17f, androidx.compose.ui.unit.TextUnitType.Sp), lineHeight = androidx.compose.ui.unit.TextUnit(27f, androidx.compose.ui.unit.TextUnitType.Sp), fontWeight = androidx.compose.ui.text.font.FontWeight.Normal), color = palette.ink2) }
            } else {
                TitleRow(title, busy, type.largeTitle, click)
                subtitle?.let { Text(it, style = type.callout, color = palette.ink2) }
            }
            else -> {
                TitleRow(title, busy, type.largeTitle, click)
                (summary ?: subtitle)?.let { Text(it, style = type.voice, color = palette.ink2) }
                if (summary != null && subtitle != null) Text(subtitle, style = type.callout, color = palette.ink3)
            }
        }
    }
}

@Composable
private fun TitleRow(title: String, busy: Boolean, style: androidx.compose.ui.text.TextStyle, modifier: Modifier) {
    Row(modifier.semantics { heading() }, verticalAlignment = Alignment.CenterVertically) {
        Text(title, style = style, color = PersonaTheme.palette.ink, maxLines = 2, overflow = TextOverflow.Ellipsis)
        if (busy) { Spacer(Modifier.width(10.dp.dense)); JSpinner(Modifier.size(18.dp)) }
    }
}

@Composable
private fun greeting(): String = stringResource(when (LocalTime.now().hour) {
    in 5..10 -> R.string.designsystem_greeting_morning
    in 11..12 -> R.string.designsystem_greeting_noon
    in 13..17 -> R.string.designsystem_greeting_afternoon
    else -> R.string.designsystem_greeting_evening
})

@Composable
private fun todayLabel(): String = LocalizedFormats.format(java.time.Instant.now(), LocalizedFormats.MONTH_DAY_WEEKDAY, currentLocale)
