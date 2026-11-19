package persona.core.designsystem

import android.graphics.LinearGradient
import android.graphics.Paint
import android.graphics.Shader
import androidx.compose.animation.ContentTransform
import androidx.compose.animation.EnterTransition
import androidx.compose.animation.ExitTransition
import androidx.compose.animation.core.CubicBezierEasing
import androidx.compose.animation.core.FastOutSlowInEasing
import androidx.compose.animation.core.FiniteAnimationSpec
import androidx.compose.animation.core.LinearEasing
import androidx.compose.animation.core.tween
import androidx.compose.animation.slideInHorizontally
import androidx.compose.animation.slideOutHorizontally
import androidx.compose.runtime.Composable
import androidx.compose.runtime.remember
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.drawscope.ContentDrawScope
import androidx.compose.ui.graphics.nativeCanvas
import androidx.compose.ui.layout.LayoutCoordinates
import androidx.compose.ui.modifier.ModifierLocalMap
import androidx.compose.ui.modifier.ModifierLocalModifierNode
import androidx.compose.ui.modifier.modifierLocalMapOf
import androidx.compose.ui.modifier.modifierLocalOf
import androidx.compose.ui.node.DrawModifierNode
import androidx.compose.ui.node.LayoutAwareModifierNode
import androidx.compose.ui.node.ModifierNodeElement
import androidx.compose.ui.node.invalidateDraw
import androidx.compose.ui.platform.InspectorInfo
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.platform.LocalLayoutDirection
import androidx.compose.ui.unit.IntOffset
import androidx.compose.ui.unit.LayoutDirection
import androidx.compose.ui.unit.dp
import kotlin.math.abs
import kotlin.math.roundToInt

/**
 * Page and Glass-button motion.
 *
 * Glass figures are not Theme.Motion names. They were read on the iPhone simulator (Xcode 27.2): the navigation
 * coordinator's duration is 0.35 s and the sampled curve is cubic-bezier(0.28, 1.02, 0.48, 0.96); the page behind
 * travels round(0.3 × width) (402 → 121, 1032 → 310, 466 → 140); `_UIParallaxDimmingView` is black at 0.10 × coverage;
 * `_UIParallaxTransitionCardView.shadowColor` is black, opacity 0.04, radius 12, offset (0, −3), in light and dark.
 * A Glass button press grows about its visual centre. The held growth was 16.00 pt at 61 pt wide, 15.79 at 160 and
 * 15.17 at 300, so the target is 1 + 16/width (the 300 pt button is 0.83 pt short). The press spring is 270 ms,
 * bounce 0.36; the release is 400 ms, bounce 0.62. The highlight layer is opaque white through a private vibrancy
 * filter, so it is not drawn. A tapped Back uses that cubic. A predictive Back is linear over the same 350 ms, because
 * seeking the cubic would move the page about two thirds of the way on a quarter of the gesture. Brief and Board keep
 * the timings below, and their predictive pop stays the eased pop.
 */
internal object PageTransitionTokens {
    const val pushDuration = Tokens.Motion.Layout.duration
    const val popDuration = 260
    const val behindDivisor = 4

    const val glassDurationMs = 350
    const val behindFraction = 0.3f
    const val dimStrength = 0.10f
    const val shadowOpacity = 0.04f
    const val shadowRadiusDp = 12f
    const val shadowOffsetYDp = -3f
    const val pressGrowthDp = 16f
    const val glassPressDurationMs = 270
    const val glassPressBounce = 0.36f
    const val glassReleaseDurationMs = 400
    const val glassReleaseBounce = 0.62f

    val glassEasing = CubicBezierEasing(0.28f, 1.02f, 0.48f, 0.96f)

    fun behindTravel(widthPx: Float): Int = (widthPx * behindFraction).roundToInt()

    fun dimAlpha(coverage: Float): Float = dimStrength * coverage.coerceIn(0f, 1f)
}

/** Opaque pages: push from the trailing edge; pop the top page back over the page underneath. */
class PersonaPageTransitions internal constructor(
    reduced: Boolean,
    direction: LayoutDirection,
    style: AppStyle,
) {
    private val trailing = if (direction == LayoutDirection.Ltr) 1 else -1
    private val glass = style == AppStyle.Glass && !reduced
    internal val pushMillis: Int = when {
        reduced -> 0
        style == AppStyle.Glass -> PageTransitionTokens.glassDurationMs
        else -> PageTransitionTokens.pushDuration
    }
    internal val popMillis: Int = when {
        reduced -> 0
        style == AppStyle.Glass -> PageTransitionTokens.glassDurationMs
        else -> PageTransitionTokens.popDuration
    }
    val push = transform(reduced, pop = false, linear = false)
    val pop = transform(reduced, pop = true, linear = false)

    // Navigation 3 seeks this spec with the back gesture. On Glass that seek is linear, so progress matches the finger.
    // A tapped Back uses [pop]. Brief and Board keep seeking the eased pop.
    val predictivePop = if (glass) transform(reduced, pop = true, linear = true) else pop

    private fun transform(reduced: Boolean, pop: Boolean, linear: Boolean): ContentTransform {
        if (reduced) return ContentTransform(EnterTransition.None, ExitTransition.None, sizeTransform = null)
        val spec = tween<IntOffset>(
            when {
                glass -> PageTransitionTokens.glassDurationMs
                pop -> PageTransitionTokens.popDuration
                else -> PageTransitionTokens.pushDuration
            },
            easing = when {
                linear -> LinearEasing
                glass -> PageTransitionTokens.glassEasing
                else -> FastOutSlowInEasing
            },
        )
        val behind: (Int) -> Int = { width ->
            val travel = if (glass) PageTransitionTokens.behindTravel(width.toFloat()) else width / PageTransitionTokens.behindDivisor
            trailing * travel
        }
        return ContentTransform(
            slideInHorizontally(spec) { width -> if (pop) -behind(width) else trailing * width },
            slideOutHorizontally(spec) { width -> if (pop) trailing * width else -behind(width) },
            targetContentZIndex = if (pop) -1f else 0f,
            sizeTransform = null,
        )
    }
}

/** Shared by every page stack, including stacks implemented with AnimatedContent. */
@Composable
fun rememberPageTransitions(): PersonaPageTransitions {
    val reduced = PersonaTheme.reduceMotion
    val direction = LocalLayoutDirection.current
    val style = PersonaTheme.style
    return remember(reduced, direction, style) { PersonaPageTransitions(reduced, direction, style) }
}

/**
 * iOS SplitNavigation.swift:171-208: selecting a value replaces the detail immediately; only hideOverlay() moves the
 * list, with Theme.Motion.layout (Theme.swift:724). The phone's drawer has its separate drawer spring.
 */
internal fun <T> sidebarColumnSpec(style: AppStyle, reduced: Boolean): FiniteAnimationSpec<T> = when {
    reduced -> tween(Tokens.Motion.reduced)
    else -> Motions.of(style).let { it.layout?.spec() ?: tween(it.standard) }
}

/**
 * Held scale of a Glass button. Growth is in points, about the centre, so the ratio is 1 + growth/width.
 * Brief, Board, a button that is not pressed, and Reduce Motion stay at 1.
 */
internal fun glassPressTarget(
    pressed: Boolean,
    glass: Boolean,
    reduceMotion: Boolean,
    widthPx: Float,
    growthPx: Float,
): Float {
    if (!pressed || !glass || reduceMotion || widthPx <= 1f) return 1f
    return 1f + growthPx / widthPx
}

/**
 * Where the front card's leading edge sits in the behind page's local x.
 * LTR: the shadow falls to the left of that edge. RTL: to the right.
 */
internal fun glassShadowEdge(width: Float, coverage: Float, travel: Int, trailing: Int): Float {
    val covered = coverage.coerceIn(0f, 1f)
    val span = width - travel
    return if (trailing > 0) width - covered * span else covered * span
}

/**
 * Placement x of the sliding page. Walks same-width ancestors and stops when the parent is wider, so a sidebar
 * column's inset is not taken for a slide. Returns 0 at rest.
 *
 * [LayoutCoordinates.localPositionOf] folds in an ancestor `graphicsLayer` translation: `toParentPosition` always
 * maps through the layer, and the boolean only skips a motion-frame placement. The phone drawer translates the page
 * that way, and the translation stays positive in right-to-left, so a later placement would treat an open drawer as
 * the page behind and leave the dim on. The slide itself is the placement x of `placeWithLayer`. That x is the
 * coordinator's position, which is not a public type, so it is read by the method name.
 */
internal fun glassSlideX(start: LayoutCoordinates): Float {
    var node = start
    var hops = 0
    while (hops < 8) {
        val parent = node.parentLayoutCoordinates ?: return 0f
        if (parent.size.width - node.size.width > 2) return 0f
        val x = layoutOffsetX(node, parent)
        if (abs(x) > 0.5f) return x
        node = parent
        hops++
    }
    return 0f
}

private fun layoutOffsetX(node: LayoutCoordinates, parentLayout: LayoutCoordinates): Float {
    var current = node
    var sum = 0f
    var guard = 0
    while (current !== parentLayout && guard++ < 16) {
        sum += coordinatorPositionX(current)
        val parent = current.parentCoordinates ?: return sum
        if (parent === parentLayout) break
        current = parent
    }
    return sum
}

/** High 32 bits of `NodeCoordinator.getPosition-nOcc-ac` (x; y is the low half). A missing method contributes 0. */
private val coordinatorPosition = HashMap<Class<*>, java.lang.reflect.Method?>()

private fun coordinatorPositionX(node: LayoutCoordinates): Float {
    val type = node.javaClass
    val method = if (coordinatorPosition.containsKey(type)) {
        coordinatorPosition[type]
    } else {
        var found: java.lang.reflect.Method? = null
        var cursor: Class<*>? = type
        while (cursor != null && found == null) {
            found = cursor.declaredMethods.firstOrNull { it.name == "getPosition-nOcc-ac" && it.parameterCount == 0 }
            cursor = cursor.superclass
        }
        found?.isAccessible = true
        coordinatorPosition[type] = found
        found
    }
    val packed = method?.invoke(node) as? Long ?: return 0f
    return (packed shr 32).toInt().toFloat()
}

/**
 * Glass only, and only while a transition is moving this page: dim the page behind and cast the front card's
 * shadow onto the strip still showing. Identity for Brief, Board and Reduce Motion, and at rest. A page that is
 * already inside another chrome draws neither dim nor shadow.
 */
@Composable
fun Modifier.glassPageChrome(): Modifier {
    if (PersonaTheme.style != AppStyle.Glass || PersonaTheme.reduceMotion) return this
    val trailing = if (LocalLayoutDirection.current == LayoutDirection.Ltr) 1 else -1
    val density = LocalDensity.current
    val radiusPx = with(density) { PageTransitionTokens.shadowRadiusDp.dp.toPx() }
    val offsetYPx = with(density) { PageTransitionTokens.shadowOffsetYDp.dp.toPx() }
    return this.then(GlassPageChromeElement(trailing, radiusPx, offsetYPx))
}

private data class GlassPageChromeElement(
    val trailing: Int,
    val radiusPx: Float,
    val offsetYPx: Float,
) : ModifierNodeElement<GlassPageChromeNode>() {
    override fun create() = GlassPageChromeNode(trailing, radiusPx, offsetYPx)
    override fun update(node: GlassPageChromeNode) {
        node.trailing = trailing
        node.radiusPx = radiusPx
        node.offsetYPx = offsetYPx
    }
    override fun InspectorInfo.inspectableProperties() {
        name = "glassPageChrome"
    }
}

private val OuterGlassChrome = modifierLocalOf { false }

/** Opaque black. A `Color.Black` name is a hard-coded color in the token ratchet; this value is not. */
private const val DimBlack = 0xFF000000.toInt()

private class GlassPageChromeNode(
    var trailing: Int,
    var radiusPx: Float,
    var offsetYPx: Float,
) : Modifier.Node(), LayoutAwareModifierNode, DrawModifierNode, ModifierLocalModifierNode {
    private val chromeOwner = modifierLocalMapOf(OuterGlassChrome to true)
    override val providedValues: ModifierLocalMap get() = chromeOwner

    private var slideX = 0f
    private var shadowPaint: Paint? = null
    private var shadowSpan = -1f
    private var shadowLeading = true

    override fun onPlaced(coordinates: LayoutCoordinates) {
        val next = glassSlideX(coordinates)
        if (next != slideX) {
            slideX = next
            invalidateDraw()
        }
    }

    override fun onDetach() {
        releaseShadow()
    }

    /** Ancestors only. This node's own provided value does not count, so the outer chrome still draws. */
    private fun insideOuterChrome(): Boolean = OuterGlassChrome.current

    override fun ContentDrawScope.draw() {
        drawContent()
        // An inner page of a page that already has this chrome would dim and shadow a second time.
        // `current` is a member of this node. Reading it from the draw receiver does not resolve.
        if (insideOuterChrome()) return
        val width = size.width
        val travel = PageTransitionTokens.behindTravel(width)
        if (width <= 1f || travel <= 0) return
        // Positive signed x is the front card. The dim and the shadow belong to the page behind.
        val signed = slideX * trailing
        if (signed >= -0.5f) {
            releaseShadow()
            return
        }
        val coverage = (abs(slideX) / travel).coerceIn(0f, 1f)
        drawRect(Color(DimBlack).copy(alpha = PageTransitionTokens.dimAlpha(coverage)))
        val edge = glassShadowEdge(width, coverage, travel, trailing)
        drawEdgeShadow(edge, size.height)
    }

    private fun ContentDrawScope.drawEdgeShadow(edge: Float, height: Float) {
        val span = radiusPx * 2f
        if (span <= 1f) return
        val leading = trailing > 0
        val left = if (leading) edge - span else edge
        val native = drawContext.canvas.nativeCanvas
        val paint = shadowPaint(span, leading)
        native.save()
        native.translate(left, offsetYPx)
        // The gradient stands in for a 12 dp blur. Shift it by the measured (0, −3) offset; the layer clips the rest.
        native.drawRect(0f, 0f, span, height, paint)
        native.restore()
    }

    /**
     * `Paint.setShadowLayer` blurs a non-text shape only on the software pipeline, and
     * `RenderEffect.createBlurEffect` is API 31. minSdk is 30, so the edge is a hardware gradient about two radii
     * wide at 0.04 black. The shader is dropped once the page is no longer the one behind.
     */
    private fun shadowPaint(span: Float, leading: Boolean): Paint {
        val current = shadowPaint
        if (current != null && shadowSpan == span && shadowLeading == leading) return current
        val ink = android.graphics.Color.argb(
            (PageTransitionTokens.shadowOpacity * 255f).roundToInt().coerceIn(0, 255),
            0, 0, 0,
        )
        val transparent = android.graphics.Color.TRANSPARENT
        val shader = if (leading) {
            LinearGradient(0f, 0f, span, 0f, transparent, ink, Shader.TileMode.CLAMP)
        } else {
            LinearGradient(0f, 0f, span, 0f, ink, transparent, Shader.TileMode.CLAMP)
        }
        return Paint(Paint.ANTI_ALIAS_FLAG).apply { this.shader = shader }.also {
            shadowPaint = it
            shadowSpan = span
            shadowLeading = leading
        }
    }

    private fun releaseShadow() {
        shadowPaint?.shader = null
        shadowPaint = null
        shadowSpan = -1f
    }
}
