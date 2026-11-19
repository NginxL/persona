package persona.core.designsystem

import androidx.compose.ui.res.stringResource
import android.content.res.Configuration
import android.graphics.Bitmap
import androidx.compose.animation.AnimatedVisibility
import androidx.compose.animation.EnterTransition
import androidx.compose.animation.ExitTransition
import androidx.compose.animation.core.animate
import androidx.compose.animation.core.tween
import androidx.compose.animation.fadeIn
import androidx.compose.animation.fadeOut
import androidx.compose.foundation.Image
import androidx.compose.foundation.background
import androidx.compose.foundation.gestures.awaitEachGesture
import androidx.compose.foundation.gestures.awaitFirstDown
import androidx.compose.foundation.gestures.calculateCentroid
import androidx.compose.foundation.gestures.calculatePan
import androidx.compose.foundation.gestures.calculateZoom
import androidx.compose.foundation.gestures.detectTapGestures
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.aspectRatio
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.navigationBarsPadding
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.statusBarsPadding
import androidx.compose.foundation.layout.widthIn
import androidx.compose.foundation.pager.HorizontalPager
import androidx.compose.foundation.pager.rememberPagerState
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.Immutable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.DisposableEffect
import androidx.compose.runtime.derivedStateOf
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableFloatStateOf
import androidx.compose.runtime.mutableIntStateOf
import androidx.compose.runtime.mutableStateMapOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.produceState
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.rememberUpdatedState
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.drawBehind
import androidx.compose.ui.geometry.Offset
import androidx.compose.ui.geometry.Size
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.asImageBitmap
import androidx.compose.ui.graphics.graphicsLayer
import androidx.compose.ui.input.pointer.PointerEvent
import androidx.compose.ui.input.pointer.pointerInput
import androidx.compose.ui.input.pointer.positionChanged
import androidx.compose.ui.layout.ContentScale
import androidx.compose.ui.layout.SubcomposeLayout
import androidx.compose.ui.layout.onSizeChanged
import androidx.compose.ui.platform.LocalConfiguration
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.platform.LocalViewConfiguration
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.Constraints
import androidx.compose.ui.unit.IntSize
import androidx.compose.ui.unit.dp
import dev.chrisbanes.haze.HazeState
import persona.core.designsystem.icons.PhIcon
import persona.core.designsystem.icons.PhIcons
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.Job
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext
import kotlin.math.abs

/**
 * One picture for [ImageViewer]. [load] returns its encoded bytes (PNG, JPEG, WebP…); a throw or empty bytes means it
 * failed, and the viewer offers "Retry". [key] identifies the picture: two with the same key are the same picture (the
 * viewer keeps its bytes once). [width] and [height], when known, shape the placeholder while it loads.
 */
@Immutable
class ViewerImage(
    val key: String,
    /** What TalkBack reads for the picture; empty reads "Image". */
    val description: String = "",
    val width: Int? = null,
    val height: Int? = null,
    /** External pages require a tap even if the pager composes them ahead of a swipe. */
    val askToLoad: Boolean = false,
    val load: suspend () -> ByteArray,
) {
    override fun equals(other: Any?): Boolean = other is ViewerImage && other.key == key &&
        other.description == description && other.width == width && other.height == height && other.askToLoad == askToLoad

    override fun hashCode(): Int = (key.hashCode() * 31 + description.hashCode()) * 31 + (width ?: 0) * 7 + (height ?: 0)
}

/**
 * A bottom action of [ImageViewer], enabled once the current page's bytes are loaded. [onClick] gets the page and its
 * original bytes and runs in the viewer: the action stays busy until it returns, and the text it returns shows as a
 * notice inside the viewer (null shows nothing). The app's own notices sit in its window, under the viewer's
 * full-screen window, so a result the user should see while the viewer stays open comes back here.
 */
@Immutable
data class ViewerAction(
    val label: String,
    val icon: PhIcon,
    val onClick: suspend (index: Int, bytes: ByteArray) -> String?,
)

/**
 * Full-screen pictures over the current page, always in the dark appearance (light system bar icons): swipe through
 * [images] from [initialIndex]; pinch 1–6×, double tap zooms to 2.5× at the finger and back, drag while zoomed, a tap
 * shows or hides the controls; at 1× drag up or down to pull the picture away (past about 120dp it closes, the
 * background fades while pulling). Top: ✕ and "n / m" (more than one picture). Bottom: [actions], one line each,
 * icons only when the labels do not fit. Pictures load with a spinner, a failed one says "Image failed to load" with "Retry"; decoding
 * runs off the main thread, sampled for the screen. Back and ✕ call [onDismiss].
 */
@Composable
fun ImageViewer(
    images: List<ViewerImage>,
    initialIndex: Int,
    onDismiss: () -> Unit,
    actions: List<ViewerAction> = emptyList(),
    modifier: Modifier = Modifier,
) {
    if (images.isEmpty()) {
        val dismiss by rememberUpdatedState(onDismiss)
        LaunchedEffect(Unit) { dismiss() }
        return
    }
    AlwaysDark {
        val palette = PersonaTheme.palette
        // JFullScreen paints the page background edge to edge; the viewer paints its own, so a picture pulled away shows
        // the page under it through the fading background.
        CompositionLocalProvider(LocalPalette provides palette.copy(background = Color.Transparent)) {
            JFullScreen(onDismiss) {
                CompositionLocalProvider(LocalPalette provides palette, LocalReadingInset provides 0.dp) {
                    ViewerBody(images, initialIndex, onDismiss, actions, modifier)
                }
            }
        }
    }
}

/**
 * The viewer and the editor are always dark: the theme, and the configuration that `isSystemInDarkTheme()` reads (Haze's
 * glass takes its defaults from it, so a light phone would otherwise get light glass on the dark viewer).
 */
@Composable
internal fun AlwaysDark(content: @Composable () -> Unit) {
    val base = LocalConfiguration.current
    val night = remember(base) {
        Configuration(base).apply {
            uiMode = (uiMode and Configuration.UI_MODE_NIGHT_MASK.inv()) or Configuration.UI_MODE_NIGHT_YES
        }
    }
    val reduceMotion = PersonaTheme.reduceMotion
    CompositionLocalProvider(LocalConfiguration provides night) {
        PersonaTheme(darkTheme = true, reduceMotion = reduceMotion, content = content)
    }
}

private sealed interface ViewerPicture {
    data object Loading : ViewerPicture
    data object Failed : ViewerPicture
    class Ready(val bitmap: Bitmap, val bytes: ByteArray) : ViewerPicture
}

/** Bytes of the last few pictures, so swiping back does not load them again. Touched on the main thread only. */
private class ViewerBytes(private val limit: Int = 8) {
    private val entries = object : LinkedHashMap<String, ByteArray>(16, 0.75f, true) {
        override fun removeEldestEntry(eldest: MutableMap.MutableEntry<String, ByteArray>) = size > limit
    }
    operator fun get(key: String): ByteArray? = entries[key]
    operator fun set(key: String, bytes: ByteArray) { entries[key] = bytes }
}

private val PullToClose = 120.dp
private const val ZoomedAbove = 1.01f
private const val DoubleTapScale = 2.5f
private const val MaxScale = 6f

@Composable
private fun ViewerBody(
    images: List<ViewerImage>,
    initialIndex: Int,
    onDismiss: () -> Unit,
    actions: List<ViewerAction>,
    modifier: Modifier,
) {
    val actionFailed = stringResource(R.string.designsystem_viewer_action_failed)
    val palette = PersonaTheme.palette
    val motion = PersonaTheme.motion
    val reduce = PersonaTheme.reduceMotion
    val density = LocalDensity.current
    val configuration = LocalConfiguration.current
    val closeAt = with(density) { PullToClose.toPx() }
    val maxSide = remember(configuration.screenWidthDp, configuration.screenHeightDp, density) {
        ImageMath.viewerSide(with(density) { maxOf(configuration.screenWidthDp, configuration.screenHeightDp).dp.roundToPx() })
    }
    val latest by rememberUpdatedState(images)
    val pager = rememberPagerState(initialPage = initialIndex.coerceIn(0, images.lastIndex)) { latest.size }
    val bytes = remember { ViewerBytes() }
    val ready = remember { mutableStateMapOf<Int, ByteArray>() }
    val zoomed = remember { mutableStateMapOf<Int, Boolean>() }
    var chrome by remember { mutableStateOf(true) }
    var pull by remember { mutableFloatStateOf(0f) }
    var settle by remember { mutableStateOf<Job?>(null) }
    var running by remember { mutableStateOf<Int?>(null) }
    // The viewer is its own window, so it draws its own toast (the app's can't reach over it).
    val toast = rememberToastState()
    val scope = rememberCoroutineScope()
    // The controls float over the picture: their glass samples the pager, not the page under this window.
    val haze = remember { HazeState() }
    val showChrome by remember { derivedStateOf { chrome && pull == 0f } }
    val page = pager.currentPage.coerceIn(0, images.lastIndex)
    val fade: EnterTransition = if (reduce) EnterTransition.None else fadeIn(tween(motion.quick))
    val vanish: ExitTransition = if (reduce) ExitTransition.None else fadeOut(tween(motion.quick))

    fun pullBy(delta: Float) {
        settle?.cancel()
        pull += delta
    }

    fun pullEnd() {
        if (abs(pull) > closeAt) { onDismiss(); return }
        settle = scope.launch {
            if (reduce) {
                pull = 0f
            } else {
                animate(pull, 0f, animationSpec = motion.enter()) { value, _ -> pull = value }
            }
        }
    }

    fun perform(index: Int) {
        val target = pager.currentPage
        val data = ready[target] ?: return
        val action = actions.getOrNull(index) ?: return
        if (running != null) return
        running = index
        scope.launch {
            val notice = try {
                action.onClick(target, data)
            } catch (e: CancellationException) {
                throw e
            } catch (_: Exception) {
                actionFailed
            } finally {
                running = null
            }
            if (notice != null) toast.show(FeedbackNotice(notice, style = if (notice == actionFailed) FeedbackStyle.Error else FeedbackStyle.Success))
        }
    }

    CompositionLocalProvider(LocalGlassSource provides haze) {
        Box(modifier.fillMaxSize().drawBehind { drawRect(palette.background, alpha = ImageMath.pullFade(pull, closeAt)) }) {
            HorizontalPager(
                state = pager,
                modifier = Modifier.fillMaxSize().glassSource(),
                beyondViewportPageCount = 1,
                pageSpacing = Tokens.Space.l,
                userScrollEnabled = zoomed[pager.currentPage] != true,
            ) { index ->
                images.getOrNull(index)?.let { image ->
                    DisposableEffect(index) { onDispose { ready.remove(index); zoomed.remove(index) } }
                    ViewerPage(
                        image, bytes, maxSide,
                        onReady = { data ->
                            if (data != null) {
                                ready[index] = data
                            } else {
                                ready.remove(index)
                            }
                        },
                        onZoomed = { zoomed[index] = it },
                        onTap = { chrome = !chrome },
                        onPull = ::pullBy,
                        onPullEnd = ::pullEnd,
                        pull = { pull },
                    )
                }
            }
            AnimatedVisibility(showChrome, Modifier.align(Alignment.TopCenter), enter = fade, exit = vanish) {
                Row(
                    Modifier.fillMaxWidth().statusBarsPadding().padding(horizontal = Tokens.Space.m, vertical = Tokens.Space.s),
                    verticalAlignment = Alignment.CenterVertically,
                ) {
                    JIconButton(PhIcons.X, stringResource(R.string.designsystem_close), onDismiss, raised = true)
                    Spacer(Modifier.weight(1f))
                    if (images.size > 1) Text(
                        "${page + 1} / ${images.size}",
                        style = PersonaTheme.type.labelSmall.copy(fontFeatureSettings = TabularFigures), color = palette.ink, maxLines = 1,
                        modifier = Modifier.floatingSurface(CircleShape).padding(horizontal = 14.dp.dense, vertical = Tokens.Space.s),
                    )
                }
            }
            // Below the top bar's buttons, so the toast never covers Close.
            JToastHost(toast, Modifier.align(Alignment.TopCenter).statusBarsPadding().padding(top = Tokens.Space.xxl + Tokens.Space.xxl))
            Column(Modifier.align(Alignment.BottomCenter).fillMaxWidth(), horizontalAlignment = Alignment.CenterHorizontally) {
                if (actions.isNotEmpty()) AnimatedVisibility(showChrome, enter = fade, exit = vanish) {
                    ViewerBar {
                        ViewerActionRow(actions, enabled = ready[page] != null && running == null, running = running, onAction = ::perform)
                    }
                }
                if (!showChrome || actions.isEmpty()) Spacer(Modifier.navigationBarsPadding())
            }
        }
    }
}

/** The bottom bar: the glass capsule floats; brief and board dock it, down over the navigation bar too. */
@Composable
private fun ViewerBar(content: @Composable () -> Unit) {
    val docked = PersonaTheme.style != AppStyle.Glass
    Box(
        Modifier.fillMaxWidth().then(if (docked) Modifier.background(PersonaTheme.palette.background) else Modifier).navigationBarsPadding(),
        contentAlignment = Alignment.TopCenter,
    ) {
        JBottomBar(Modifier.widthIn(max = ReadingWidth + 40.dp)) { content() }
    }
}

/**
 * The actions in one line: every label when they fit, else only the first (the main action) keeps its label, else
 * icons only. Labels never wrap.
 */
@Composable
private fun ViewerActionRow(actions: List<ViewerAction>, enabled: Boolean, running: Int?, onAction: (Int) -> Unit) {
    SubcomposeLayout(Modifier.fillMaxWidth()) { constraints ->
        var chosen = subcompose(0) { ViewerActions(actions, labels = actions.size, enabled, running, onAction) }.first().measure(Constraints())
        if (constraints.hasBoundedWidth && chosen.width > constraints.maxWidth) {
            chosen = subcompose(1) { ViewerActions(actions, labels = 1, enabled, running, onAction) }.first().measure(Constraints())
        }
        if (constraints.hasBoundedWidth && chosen.width > constraints.maxWidth) {
            chosen = subcompose(2) { ViewerActions(actions, labels = 0, enabled, running, onAction) }.first().measure(Constraints())
        }
        val width = if (constraints.hasBoundedWidth) constraints.maxWidth else chosen.width
        layout(width, chosen.height) { chosen.placeRelative(((width - chosen.width) / 2).coerceAtLeast(0), 0) }
    }
}

/** The first [labels] actions as labelled buttons (the first one primary), the rest as icons. */
@Composable
private fun ViewerActions(actions: List<ViewerAction>, labels: Int, enabled: Boolean, running: Int?, onAction: (Int) -> Unit) {
    Row(horizontalArrangement = Arrangement.spacedBy(Tokens.Space.s), verticalAlignment = Alignment.CenterVertically) {
        actions.forEachIndexed { index, action ->
            if (index < labels) JButton(
                action.label, { onAction(index) },
                kind = if (index == 0) JButtonKind.Primary else JButtonKind.Secondary, size = JButtonSize.Medium,
                icon = action.icon, enabled = enabled, loading = running == index,
            )
            else JIconButton(action.icon, action.label, { onAction(index) }, enabled = enabled)
        }
    }
}

private val ViewerImage.ratio: Float?
    get() {
        val w = width ?: return null
        val h = height ?: return null
        return if (w > 0 && h > 0) w.toFloat() / h else null
    }

@Composable
private fun ViewerPage(
    image: ViewerImage,
    cache: ViewerBytes,
    maxSide: Int,
    onReady: (ByteArray?) -> Unit,
    onZoomed: (Boolean) -> Unit,
    onTap: () -> Unit,
    onPull: (Float) -> Unit,
    onPullEnd: () -> Unit,
    pull: () -> Float,
) {
    val palette = PersonaTheme.palette
    var allowed by remember(image.key, image.askToLoad) { mutableStateOf(!image.askToLoad) }
    if (!allowed && cache[image.key] == null) {
        Box(Modifier.fillMaxSize(), contentAlignment = Alignment.Center) {
            JButton(stringResource(R.string.designsystem_load_image), { allowed = true }, icon = PhIcons.Image)
        }
        return
    }
    val load by rememberUpdatedState(image.load)
    var attempt by remember(image.key) { mutableIntStateOf(0) }
    val state by produceState<ViewerPicture>(ViewerPicture.Loading, image.key, attempt, maxSide) {
        value = ViewerPicture.Loading
        value = try {
            val data = cache[image.key] ?: withContext(Dispatchers.IO) { load() }.also {
                require(it.isNotEmpty()) { "No picture" }
                cache[image.key] = it
            }
            ViewerPicture.Ready(withContext(Dispatchers.Default) { decodePicture(data, maxSide) }, data)
        } catch (e: CancellationException) {
            throw e
        } catch (_: Throwable) {
            ViewerPicture.Failed
        }
    }
    val report by rememberUpdatedState(onReady)
    LaunchedEffect(state) { report((state as? ViewerPicture.Ready)?.bytes) }
    when (val picture = state) {
        ViewerPicture.Loading -> Box(Modifier.fillMaxSize(), contentAlignment = Alignment.Center) {
            image.ratio?.let { Box(Modifier.aspectRatio(it).background(palette.surface)) }
            JSpinner(Modifier.size(28.dp))
        }
        ViewerPicture.Failed -> Column(
            Modifier.fillMaxSize().padding(Tokens.Space.xl),
            verticalArrangement = Arrangement.spacedBy(Tokens.Space.m, Alignment.CenterVertically),
            horizontalAlignment = Alignment.CenterHorizontally,
        ) {
            JIcon(PhIcons.Image, null, size = 40.dp, tint = palette.ink3)
            Text(stringResource(R.string.designsystem_viewer_load_failed), style = PersonaTheme.type.callout, color = palette.ink2, maxLines = 1, overflow = TextOverflow.Ellipsis)
            JButton(stringResource(R.string.designsystem_retry), { attempt++ }, kind = JButtonKind.Secondary, size = JButtonSize.Medium, icon = PhIcons.ArrowClockwise)
        }
        is ViewerPicture.Ready -> ZoomablePicture(picture.bitmap, image.description.ifEmpty { stringResource(R.string.designsystem_image) }, pull, onZoomed, onTap, onPull, onPullEnd)
    }
}

/**
 * One picture: pinch to zoom (1–[MaxScale]×) around the fingers, drag to pan while zoomed, a double tap goes to
 * [DoubleTapScale]× at the finger or back, a tap goes to [onTap]; at 1× a mostly vertical drag pulls the picture away
 * ([onPull] deltas, [onPullEnd] on release), while a horizontal one is left to the pager.
 */
@Composable
private fun ZoomablePicture(
    bitmap: Bitmap,
    description: String,
    pull: () -> Float,
    onZoomed: (Boolean) -> Unit,
    onTap: () -> Unit,
    onPull: (Float) -> Unit,
    onPullEnd: () -> Unit,
) {
    val picture = remember(bitmap) { bitmap.asImageBitmap() }
    val imageSize = remember(bitmap) { Size(bitmap.width.toFloat(), bitmap.height.toFloat()) }
    var box by remember { mutableStateOf(IntSize.Zero) }
    var scale by remember(bitmap) { mutableFloatStateOf(1f) }
    var pan by remember(bitmap) { mutableStateOf(Offset.Zero) }
    var zooming by remember { mutableStateOf<Job?>(null) }
    val scope = rememberCoroutineScope()
    val motion = PersonaTheme.motion
    val reduce = PersonaTheme.reduceMotion
    val slop = LocalViewConfiguration.current.touchSlop
    val zoomed by rememberUpdatedState(onZoomed)
    val tapped by rememberUpdatedState(onTap)
    val pulled by rememberUpdatedState(onPull)
    val released by rememberUpdatedState(onPullEnd)

    fun boxSize() = Size(box.width.toFloat(), box.height.toFloat())
    fun fitted() = ImageMath.fitRect(imageSize, boxSize()).size
    fun center() = Offset(box.width / 2f, box.height / 2f)
    fun consume(event: PointerEvent) = event.changes.forEach { if (it.positionChanged()) it.consume() }

    fun zoomTo(target: Float, finger: Offset) {
        zooming?.cancel()
        val startScale = scale
        val startPan = pan
        val endPan = if (target <= 1f) Offset.Zero
        else ImageMath.clampPan(ImageMath.zoomPan(pan, scale, target, finger - center()), target, fitted(), boxSize())
        zoomed(target > ZoomedAbove)
        zooming = scope.launch {
            if (reduce) {
                scale = target
                pan = endPan
            } else {
                animate(0f, 1f, animationSpec = motion.enter()) { t, _ ->
                    scale = startScale + (target - startScale) * t
                    pan = startPan + (endPan - startPan) * t
                }
            }
        }
    }

    Box(
        Modifier.fillMaxSize()
            .onSizeChanged { box = it }
            .pointerInput(bitmap) {
                detectTapGestures(
                    onDoubleTap = { finger -> zoomTo(if (scale > ZoomedAbove) 1f else DoubleTapScale, finger) },
                    onTap = { tapped() },
                )
            }
            .pointerInput(bitmap) {
                awaitEachGesture {
                    awaitFirstDown(requireUnconsumed = false)
                    zooming?.cancel()
                    var moved = Offset.Zero
                    var pulling = false
                    var paging = false
                    var pinched = false
                    do {
                        val event = awaitPointerEvent()
                        val fingers = event.changes.count { it.pressed }
                        when {
                            pulling -> { pulled(event.calculatePan().y); consume(event) }
                            paging -> Unit
                            fingers >= 2 -> {
                                pinched = true
                                val target = (scale * event.calculateZoom()).coerceIn(1f, MaxScale)
                                val centroid = event.calculateCentroid(useCurrent = true)
                                val focus = if (centroid == Offset.Unspecified) Offset.Zero else centroid - center()
                                pan = ImageMath.clampPan(ImageMath.zoomPan(pan, scale, target, focus) + event.calculatePan(), target, fitted(), boxSize())
                                scale = target
                                zoomed(scale > ZoomedAbove)
                                consume(event)
                            }
                            scale > ZoomedAbove -> {
                                pan = ImageMath.clampPan(pan + event.calculatePan(), scale, fitted(), boxSize())
                                consume(event)
                            }
                            !pinched -> {
                                val delta = event.calculatePan()
                                moved += delta
                                if (abs(moved.y) > slop && abs(moved.y) > abs(moved.x) * 1.5f) {
                                    pulling = true
                                    pulled(delta.y)
                                    consume(event)
                                } else if (abs(moved.x) > slop) paging = true
                            }
                        }
                    } while (event.changes.any { it.pressed })
                    if (pulling) released()
                    if (scale <= ZoomedAbove) {
                        scale = 1f
                        pan = Offset.Zero
                        zoomed(false)
                    }
                }
            },
    ) {
        Image(
            picture, description,
            Modifier.fillMaxSize().graphicsLayer {
                scaleX = scale
                scaleY = scale
                translationX = pan.x
                translationY = pan.y + pull()
            },
            contentScale = ContentScale.Fit,
        )
    }
}
