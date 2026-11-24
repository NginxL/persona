package persona.core.designsystem

import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent
import android.content.IntentFilter
import android.os.PowerManager
import androidx.compose.animation.core.animateFloatAsState
import androidx.compose.animation.core.snap
import androidx.compose.animation.core.tween
import androidx.compose.foundation.Canvas
import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.runtime.*
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.draw.clipToBounds
import androidx.compose.ui.draw.drawWithContent
import androidx.compose.ui.geometry.Offset
import androidx.compose.ui.geometry.Size
import androidx.compose.ui.graphics.BlendMode
import androidx.compose.ui.graphics.Brush
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.CompositingStrategy
import androidx.compose.ui.graphics.graphicsLayer
import androidx.compose.ui.graphics.lerp
import androidx.compose.ui.layout.onGloballyPositioned
import androidx.compose.ui.layout.positionInWindow
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.platform.LocalWindowInfo
import androidx.core.content.ContextCompat
import androidx.compose.ui.unit.dp
import dev.chrisbanes.haze.HazeInput
import dev.chrisbanes.haze.HazeState
import dev.chrisbanes.haze.blur.HazeBlurStyle
import dev.chrisbanes.haze.blur.HazeColorEffect
import dev.chrisbanes.haze.blur.hazeBlur
import dev.chrisbanes.haze.glass.GlassOptics
import dev.chrisbanes.haze.glass.GlassStyle
import dev.chrisbanes.haze.glass.OpticalSizeValue
import dev.chrisbanes.haze.glass.RefractionProfile
import dev.chrisbanes.haze.glass.hazeGlass
import dev.chrisbanes.haze.hazeSource
import kotlinx.coroutines.delay
import kotlin.math.PI
import kotlin.math.atan2
import kotlin.math.cos
import kotlin.math.sin

/**
 * What floating glass shows through. The shell provides one per tab so its dock and the page's own floating
 * bars sample the same content; a page without a shell creates its own.
 */
val LocalGlassSource = staticCompositionLocalOf<HazeState?> { null }

/** Marks content that floating glass above it may refract or blur. No-op outside the glass style. */
@Composable
fun Modifier.glassSource(): Modifier {
    val state = LocalGlassSource.current
    return if (state == null || PersonaTheme.style != AppStyle.Glass || PersonaTheme.glass == GlassLevel.Solid) this
    else this.hazeSource(state)
}

private val FloatingGlassBlurRadius = 28.dp

private val HomeNavigationGlass = GlassStyle.regular.then {
    shape(CircleShape)
    // Keep the bend at the curved edge and use the same capture for the whole surface.
    optics(
        GlassOptics(
            refractionProfile = RefractionProfile.Edge(Tokens.Android.HomeSwitcherGlass.refractionBand),
            refractionDisplacement = Tokens.Android.HomeSwitcherGlass.refractionDisplacement,
            refractionDetailIntensity = 0f,
            blurRadius = OpticalSizeValue.Fixed(Tokens.Android.HomeSwitcherGlass.blur),
        )
    )
    lightPosition(Alignment.TopStart)
    specularIntensity(0.55f)
    ambientResponse(0.25f)
    edgeShadow(Tokens.Android.HomeSwitcherGlass.edgeShadow)
}

private val InteractiveHomeNavigationGlass = HomeNavigationGlass.then {
    pressed {
        // A white-point change would invalidate the refracted output throughout the press.
        lightingIntensity(HomeTokens.switcherPressLightIntensity)
    }
}

@Composable
internal fun Modifier.homeNavigationGlassSurface(interactive: Boolean): Modifier {
    val palette = PersonaTheme.palette
    val state = LocalGlassSource.current
    val level = PersonaTheme.glass
    if (state != null && level == GlassLevel.Liquid) {
        val material = if (interactive) InteractiveHomeNavigationGlass else HomeNavigationGlass
        val tint = if (palette.dark) Tokens.Android.HomeSwitcherGlass.darkTint else Tokens.Android.HomeSwitcherGlass.lightTint
        val style = remember(material, tint) { material.then { tint(tint) } }
        return hazeGlass(
            HazeInput.Sources(state),
            style = style,
        )
    }

    val edge = Brush.linearGradient(
        listOf(palette.strokeTop, palette.strokeTop.copy(alpha = 0f), Tokens.Android.HomeSwitcherGlass.edgeShadow),
    )
    val surface = if (state != null && level == GlassLevel.Frosted) {
        val tint = if (palette.dark) HomeTokens.chromeDark else HomeTokens.chromeLight
        this.clip(CircleShape).hazeBlur(HazeInput.Sources(state), style = HazeBlurStyle {
            blurRadius(FloatingGlassBlurRadius)
            colorEffects(listOf(HazeColorEffect.tint(tint)))
        })
    } else {
        this.background(palette.surfaceOpaque, CircleShape)
    }
    return surface.border(Tokens.Android.HomeSwitcherGlass.fallbackStroke, edge, CircleShape)
}

/**
 * Floating chrome (bars, dock, decision sheets): refraction when the device can, plain blur otherwise, an
 * opaque panel as the last resort. Outside the glass style it is a normal raised surface.
 */
@Composable
fun Modifier.floatingSurface(shape: RoundedCornerShape, accentEdge: Color? = null): Modifier {
    val palette = PersonaTheme.palette
    val state = LocalGlassSource.current
    val edgeTop = accentEdge ?: palette.strokeTop
    val edgeBottom = palette.strokeBottom
    val edge = remember(edgeTop, edgeBottom) { Brush.verticalGradient(listOf(edgeTop, edgeBottom)) }
    if (PersonaTheme.style != AppStyle.Glass) {
        return this.clip(shape).background(palette.surfaceOpaque).border(1.dp, accentEdge ?: palette.line, shape)
    }
    val homeChrome = LocalHomeChrome.current != null || LocalHomeDrawer.current || LocalFlatPageSurface.current
    val tint = if (homeChrome) {
        if (palette.dark) HomeTokens.chromeDark else HomeTokens.chromeLight
    } else if (palette.dark) Color(0xB80A1020) else Color(0xCCFFFFFF)
    return when {
        state == null || PersonaTheme.glass == GlassLevel.Solid ->
            this.clip(shape).background(palette.surfaceOpaque.copy(alpha = 0.97f)).border(1.dp, edge, shape)
        PersonaTheme.glass == GlassLevel.Liquid -> {
            val style = remember(shape, tint) { GlassStyle.regular.then { shape(shape); tint(tint) } }
            this.hazeGlass(HazeInput.Sources(state), style = style).border(1.dp, edge, shape)
        }
        else -> {
            val blurStyle = remember(tint) {
                HazeBlurStyle { blurRadius(FloatingGlassBlurRadius); colorEffects(listOf(HazeColorEffect.tint(tint))) }
            }
            this.clip(shape).hazeBlur(HazeInput.Sources(state), style = blurStyle).border(1.dp, edge, shape)
        }
    }
}

/** What the page is about, for the ambient light behind glass. */
enum class Ambience { Calm, NeedsYou, Working, Connected, Alert }

/** The shell's guess for root pages; pages may pass their own. */
val LocalAmbience = compositionLocalOf { Ambience.Calm }

/**
 * Where one colour's light rests (fractions of the area, radius of its shorter side). [seed] tells the pools apart:
 * it shifts their phases and stretches their periods so no two move in step.
 */
internal class Pool(val x: Float, val y: Float, val radius: Float, val seed: Int)

/** One circle of light at a moment: [weight] is its share of the pool's strength, [lean] how far it turns to the neighbour colour. */
internal class LightBlob(val center: Offset, val radius: Float, val weight: Float, val lean: Float)

// The light's motion (2026-11-03, #959): slow enough to read as weather, not animation. Periods are seconds and
// unrelated to each other, so the picture never visibly repeats; amplitudes are fractions of the shorter side.
private const val Wander = 0.12
private const val Breathe = 0.14
private const val Pulse = 0.10
private const val Lean = 0.22
private const val SplitPeriod = 130.0
private const val Separation = 0.65
private const val TwoPi = 2 * PI

/**
 * The circles one pool draws at [t] seconds: it drifts around where it rests, grows and shrinks, brightens and dims,
 * leans a little toward its neighbour colour, and now and then buds off a smaller light that wanders toward the middle
 * of the screen (where it may meet another colour's) and later drifts back and melts into it again. A pure function of
 * the time, so every copy of the background draws the same light. [still] (reduce motion) is the pool at rest.
 */
internal fun Pool.blobs(t: Double, area: Size, still: Boolean): List<LightBlob> {
    val rest = Offset(area.width * x, area.height * y)
    val unit = area.minDimension
    if (still) return listOf(LightBlob(rest, unit * radius, 1f, 0f))
    val stretch = 1 + 0.11 * seed
    fun wave(period: Double, channel: Int) = sin(TwoPi * t / (period * stretch) + seed * 2.39996 + channel * 1.61803)
    val drift = Offset(
        (unit * Wander * (0.65 * wave(53.0, 1) + 0.35 * wave(23.0, 2))).toFloat(),
        (unit * Wander * (0.65 * wave(61.0, 3) + 0.35 * wave(29.0, 4))).toFloat(),
    )
    val size = unit * radius * (1 + Breathe * wave(41.0, 5)).toFloat()
    val pulse = (1 + Pulse * wave(33.0, 6)).toFloat()
    val lean = (Lean * (0.5 + 0.5 * wave(71.0, 7))).toFloat()
    // Long stretches as one light or as two, and half a minute to go from one to the other.
    val cycle = 0.5 - 0.5 * cos(TwoPi * t / (SplitPeriod * stretch) + seed * 1.7)
    val apart = smooth(((cycle - 0.15) / 0.7).coerceIn(0.0, 1.0)).toFloat()
    val center = rest + drift
    val main = LightBlob(center, size * (1 - 0.15f * apart), pulse * (1 - 0.3f * apart), lean)
    if (apart <= 0.01f) return listOf(main)
    // The bud heads for the middle of the area, swinging up to about 60° either side of it. Measured from where the
    // pool rests, not where it has drifted: a pool passing the middle would otherwise whip its bud around.
    val inward = atan2(area.height / 2.0 - rest.y, area.width / 2.0 - rest.x)
    val angle = inward + 1.1 * wave(240.0, 8)
    val way = Offset(cos(angle).toFloat(), sin(angle).toFloat())
    val reach = unit * Separation.toFloat() * apart
    return listOf(
        LightBlob(center - way * (reach * 0.25f), main.radius, main.weight, lean),
        LightBlob(center + way * (reach * 0.75f), size * (0.45f + 0.15f * apart), pulse * 0.6f * apart, (lean + 0.3f * apart).coerceAtMost(1f)),
    )
}

private fun smooth(x: Double) = x * x * (3 - 2 * x)

/** How often the moving light is redrawn. It moves too slowly to show steps, and every change makes the glass above blur again. */
private const val LightStepNanos = 1_000_000_000L / 12

/** Light time in seconds, in [LightStepNanos] steps. */
internal fun lightSeconds(nanos: Long) = nanos / LightStepNanos * LightStepNanos / 1e9

/**
 * The light's redraw loop: every [LightStepNanos] it waits for a frame ([frame]; `withFrameNanos` in the app) and
 * [publish]es the [clock]'s time at that frame. Once the clock is idle it stops asking for frames and parks until the
 * next input, so a page nobody touches draws nothing for the light.
 */
internal suspend fun driveLight(clock: AmbientClock, frame: suspend ((Long) -> Unit) -> Unit, publish: (Double) -> Unit) {
    while (true) {
        var idle = false
        frame { at ->
            publish(lightSeconds(clock.activeNanos(at)))
            idle = clock.isIdle(at)
        }
        if (idle) clock.awaitActivity() else delay(LightStepNanos / 1_000_000)
    }
}

/**
 * Seconds of light time ([AmbientClock.Shared]) in [LightStepNanos] steps, starting from the clock's current value (not
 * zero, so a page that appears mid-way draws the same light as the ones already on screen). While the app is in use
 * that is the frame clock's time, as before; [AmbientIdleAfterNanos] after the last input it holds still, and it
 * carries on from there at the next one. The frame clock pauses while the app is not visible.
 */
@Composable
private fun lightTime(running: Boolean): State<Double> {
    val clock = AmbientClock.Shared
    val time = remember { mutableDoubleStateOf(lightSeconds(clock.activeNanos(System.nanoTime()))) }
    if (running) LaunchedEffect(Unit) {
        driveLight(clock, frame = { onFrame -> withFrameNanos(onFrame) }) { time.doubleValue = it }
    }
    return time
}

/** Battery saver is on. The moving light is decoration, so it rests then; other motion stays the system's to scale. */
@Composable
private fun powerSaving(): Boolean {
    val context = LocalContext.current
    val power = remember(context) { context.getSystemService(PowerManager::class.java) }
    var saving by remember(power) { mutableStateOf(power?.isPowerSaveMode == true) }
    DisposableEffect(context, power) {
        val receiver = object : BroadcastReceiver() {
            override fun onReceive(context: Context?, intent: Intent?) { saving = power?.isPowerSaveMode == true }
        }
        ContextCompat.registerReceiver(context, receiver, IntentFilter(PowerManager.ACTION_POWER_SAVE_MODE_CHANGED), ContextCompat.RECEIVER_NOT_EXPORTED)
        saving = power?.isPowerSaveMode == true
        onDispose { context.unregisterReceiver(receiver) }
    }
    return saving
}

/**
 * Soft pools of light behind the glass. Each pool belongs to one status colour; the page's [Ambience] decides
 * how strong each one is, and changes fade over about a second. The pools drift, breathe and split and merge
 * slowly ([blobs]); with reduce motion or battery saver they stay where they rest. Five seconds after the last input
 * they hold still where they are ([AmbientClock]) and carry on from there at the next one.
 *
 * [inWindow] lays the pools out against the whole window instead of this box: every page, pane and nested page that
 * paints it then paints the same light at the same place, so whatever sits side by side or inside another reads as
 * one surface with no seam (pages use it through [PageBackdrop]). A miniature of a screen keeps its own layout.
 * [mask] fades the light by the brush's alpha (the wash under the floating bars).
 */
@Composable
fun AmbientLight(ambience: Ambience, modifier: Modifier = Modifier, inWindow: Boolean = false, mask: Brush? = null) {
    val palette = PersonaTheme.palette
    val animate = !PersonaTheme.reduceMotion
    fun target(vararg pairs: Pair<Ambience, Float>, default: Float) = pairs.firstOrNull { it.first == ambience }?.second ?: default
    val spec = if (animate) tween<Float>(900) else snap()
    val amber by animateFloatAsState(target(Ambience.NeedsYou to 1f, default = 0f), spec, label = "amber")
    val blue by animateFloatAsState(target(Ambience.Working to 1f, Ambience.Calm to 0.7f, default = 0f), spec, label = "blue")
    val mint by animateFloatAsState(target(Ambience.Connected to 1f, default = 0f), spec, label = "mint")
    val red by animateFloatAsState(target(Ambience.Alert to 0.9f, default = 0f), spec, label = "red")
    val glow = if (palette.dark) 0.08f else 0.04f
    val window = LocalWindowInfo.current.containerSize
    val moving = animate && !powerSaving()
    val time = lightTime(moving)
    // Where this box sits in the window. Read only while drawing: following a sliding page redraws the light
    // without recomposing or relaying out anything.
    val origin = remember { mutableStateOf(Offset.Zero) }
    val placed = if (inWindow) Modifier.onGloballyPositioned { origin.value = it.positionInWindow() } else Modifier
    val masked = if (mask == null) Modifier else Modifier
        .graphicsLayer { compositingStrategy = CompositingStrategy.Offscreen }
        .drawWithContent { drawContent(); drawRect(mask, blendMode = BlendMode.DstIn) }
    // Clipped: pools centred outside the box (the window's other side) must not paint over the panes beside it.
    Canvas(modifier.fillMaxSize().then(placed).clipToBounds().then(masked).background(palette.background)) {
        val area = if (inWindow && window.width > 0 && window.height > 0) Size(window.width.toFloat(), window.height.toFloat()) else size
        val shift = if (inWindow) origin.value else Offset.Zero
        val t = time.value
        fun pool(color: Color, neighbour: Color, strength: Float, at: Pool) {
            if (strength <= 0.01f) return
            for (blob in at.blobs(t, area, still = !moving)) {
                val alpha = glow * strength * blob.weight
                val tone = lerp(color, neighbour, blob.lean)
                val center = blob.center - shift
                drawCircle(
                    Brush.radialGradient(
                        listOf(tone.copy(alpha = alpha), tone.copy(alpha = alpha * 0.35f), Color.Transparent),
                        center = center, radius = blob.radius,
                    ),
                    radius = blob.radius, center = center,
                )
            }
        }
        pool(palette.needsYou.dot, palette.needsYou.dot, amber, Pool(0.05f, 0.10f, 0.95f, seed = 0))
        pool(palette.danger.dot, palette.needsYou.dot, red, Pool(0.9f, 0.08f, 0.85f, seed = 1))
        pool(palette.working.dot, palette.done.dot, blue, Pool(1.0f, 0.52f, 0.9f, seed = 2))
        val connected = ambience == Ambience.Connected
        pool(palette.done.dot, palette.working.dot, mint, Pool(if (connected) 0.5f else 0.08f, if (connected) 0.06f else 0.98f, 0.7f, seed = 3))
    }
}

/**
 * The background under a page, a pane, the shell and anything else that needs one: the page colour, and in glass
 * the ambient light laid out against the whole window. Everything that paints it lines up, so the rail, panes side by
 * side, a page inside another page and the room between them read as one surface; the cards on it are what
 * separate things (2026-11-01: no second layer under the content, no hard band at the top or bottom).
 */
@Composable
fun PageBackdrop(modifier: Modifier = Modifier, ambience: Ambience = LocalAmbience.current) {
    if (PersonaTheme.style == AppStyle.Glass && LocalHomeChrome.current == null && !LocalFlatPageSurface.current) AmbientLight(ambience, modifier, inWindow = true)
    else Box(modifier.fillMaxSize().background(PersonaTheme.palette.background))
}
