package persona.core.designsystem

import android.graphics.Bitmap
import androidx.compose.animation.core.FiniteAnimationSpec
import androidx.compose.animation.core.SpringSpec
import androidx.compose.animation.core.TweenSpec
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.PaddingValues
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.ui.input.nestedscroll.nestedScroll
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.LazyListState
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.lazy.rememberLazyListState
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.SideEffect
import androidx.compose.runtime.snapshots.Snapshot
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.asAndroidBitmap
import androidx.compose.ui.layout.onPlaced
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.test.captureToImage
import androidx.compose.ui.test.getUnclippedBoundsInRoot
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.unit.dp
import androidx.test.ext.junit.runners.AndroidJUnit4
import kotlinx.coroutines.cancelAndJoin
import kotlinx.coroutines.runBlocking
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.Shadows.shadowOf
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import android.os.Looper
import java.io.File
import java.util.concurrent.CountDownLatch
import java.util.concurrent.TimeUnit
import kotlin.math.abs

/**
 * Tap-driven row motion (#9600). A feed insert or removal is instant. A fold and a pin move on Glass,
 * and the frames are the list at a phone's width and an upright foldable's (673 dp).
 */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "en-w800dp-h1000dp-mdpi")
class ListTapMotionTest {
    @get:Rule val rule = createComposeRule()

    @Test fun specsFollowTheStyleAndAreNullWhenMotionIsOff() {
        val glass = Motions.of(AppStyle.Glass)
        val state = listTapSpecs(ListTapMotionKind.State, glass, false)
        val stateSpring = IosSpring(Tokens.Motion.State.duration, Tokens.Motion.State.bounce)
        assertSpring(stateSpring, state.fade)
        assertSpring(stateSpring, state.placement)
        val layout = listTapSpecs(ListTapMotionKind.Layout, glass, false)
        val layoutSpring = IosSpring(Tokens.Motion.Layout.duration, Tokens.Motion.Layout.bounce)
        assertSpring(layoutSpring, layout.fade)
        assertSpring(layoutSpring, layout.placement)
        assertNull(listTapSpecs(null, glass, false).placement)
        assertNull(listTapSpecs(ListTapMotionKind.State, glass, true).fade)
        assertNull(listTapSpecs(ListTapMotionKind.Layout, glass, true).placement)
        val brief = Motions.of(AppStyle.Brief)
        assertEquals(240, (listTapSpecs(ListTapMotionKind.State, brief, false).placement as TweenSpec<*>).durationMillis)
        assertEquals(240, (listTapSpecs(ListTapMotionKind.Layout, brief, false).fade as TweenSpec<*>).durationMillis)
        val board = Motions.of(AppStyle.Board)
        assertEquals(160, (listTapSpecs(ListTapMotionKind.State, board, false).placement as TweenSpec<*>).durationMillis)
        assertEquals(160, (listTapSpecs(ListTapMotionKind.Layout, board, false).fade as TweenSpec<*>).durationMillis)
    }

    @Test fun feedInsertAndRemoveDoNotAnimate() {
        rule.mainClock.autoAdvance = false
        var rows by mutableStateOf(listOf("Alpha", "Beta", "Gamma"))
        val motion = ListTapMotion()
        rule.setContent {
            PersonaTheme(darkTheme = false, reduceMotion = false, style = AppStyle.Glass) {
                TapRows(motion, rows, 411.dp)
            }
        }
        settle()
        val before = top("row-Gamma")
        rows = listOf("Alpha", "Gamma")
        val removed = samples("row-Gamma")
        assertEquals(removed[1], removed[0], 0.5f)
        assertEquals(removed[2], removed[0], 0.5f)
        assertTrue(removed[0] < before - 8f)
        rows = listOf("New", "Alpha", "Gamma")
        val inserted = samples("row-Gamma")
        assertEquals(inserted[1], inserted[0], 0.5f)
        assertEquals(inserted[2], inserted[0], 0.5f)
        assertTrue(inserted[0] > removed[0] + 8f)
        assertNull(motion.kind)
    }

    @Test fun reducedMotionFoldIsInstant() {
        rule.mainClock.autoAdvance = false
        var rows by mutableStateOf(listOf("Project", "Review", "Notes", "Next"))
        val motion = ListTapMotion()
        rule.setContent {
            PersonaTheme(darkTheme = false, reduceMotion = true, style = AppStyle.Glass) {
                TapRows(motion, rows, 411.dp)
            }
        }
        settle()
        val before = top("row-Next")
        motion.arm(ListTapMotionKind.State)
        rows = listOf("Project", "Next")
        val folded = samples("row-Next")
        assertEquals(folded[1], folded[0], 0.5f)
        assertEquals(folded[2], folded[0], 0.5f)
        assertTrue(folded[0] < before - 8f)
        assertNull(motion.kind)
    }

    @Test fun foldAndPinFrames() {
        rule.mainClock.autoAdvance = false
        val drive = Drive()
        rule.setContent {
            PersonaTheme(darkTheme = drive.dark, reduceMotion = false, style = AppStyle.Glass) {
                TapRows(drive.motion, drive.rows, drive.width)
            }
        }
        val out = File(System.getProperty("persona.shots") ?: "build/list-tap-shots").also { it.mkdirs() }
        for (dark in listOf(false, true)) {
            for ((form, width) in listOf("phone" to 411.dp, "foldable" to 673.dp)) {
                val scheme = if (dark) "dark" else "light"
                shoot(drive, out, "fold-$scheme-$form", dark, width,
                    listOf("Project", "Review", "Notes", "Next"), listOf("Project", "Next"), "row-Next")
                shoot(drive, out, "pin-$scheme-$form", dark, width,
                    listOf("Review", "Notes", "Release"), listOf("Release", "Review", "Notes"), "row-Release")
            }
        }
    }

    private fun shoot(
        drive: Drive, out: File, name: String, dark: Boolean, width: androidx.compose.ui.unit.Dp,
        startRows: List<String>, endRows: List<String>, moving: String,
    ) {
        rule.runOnIdle {
            drive.dark = dark
            drive.width = width
            drive.rows = startRows
            drive.motion = ListTapMotion()
        }
        Snapshot.sendApplyNotifications()
        settle()
        val start = top(moving)
        save(out, "$name-start.png")
        rule.runOnIdle { drive.motion.arm(ListTapMotionKind.State) }
        rule.mainClock.advanceTimeByFrame()
        rule.runOnIdle { drive.rows = endRows }
        Snapshot.sendApplyNotifications()
        val frames = (1..8).map { frame ->
            rule.mainClock.advanceTimeByFrame()
            if (frame == 3) save(out, "$name-mid.png")
            top(moving)
        }
        rule.mainClock.advanceTimeBy(600)
        val end = top(moving)
        save(out, "$name-end.png")
        val low = minOf(start, end) - 2f
        val high = maxOf(start, end) + 2f
        assertTrue("$name left the path $start -> $end: $frames", frames.all { it in low..high })
        assertTrue("$name frames $frames start $start end $end", frames.any { y ->
            (y - start) * (y - end) < 0f && abs(y - start) > 4f && abs(y - end) > 4f
        })
        assertTrue("$name did not settle", abs(end - start) > 8f)
        assertNull("$name flag still set", drive.motion.kind)
    }

    @Test fun unchangedTapDropsOnTheNextFrame() {
        rule.mainClock.autoAdvance = false
        val rows = listOf("Alpha", "Beta")
        val motion = ListTapMotion()
        rule.setContent {
            PersonaTheme(darkTheme = false, reduceMotion = false, style = AppStyle.Glass) { TapRows(motion, rows, 411.dp) }
        }
        settle()
        motion.arm(ListTapMotionKind.State)
        Snapshot.sendApplyNotifications()
        // The frame after the tap draws the spec. The one after that is where a deferred write would land.
        // Nothing wrote, so the flag drops then, before a later hub push can use it.
        rule.mainClock.advanceTimeByFrame()
        rule.mainClock.advanceTimeByFrame()
        rule.mainClock.advanceTimeByFrame()
        assertNull(motion.kind)
    }

    @Test fun pageThatDoesNotReorderDropsWhenItLands() {
        rule.mainClock.autoAdvance = false
        val rows = listOf("Alpha", "Beta")
        val motion = ListTapMotion()
        rule.setContent {
            PersonaTheme(darkTheme = false, reduceMotion = false, style = AppStyle.Glass) { TapRows(motion, rows, 411.dp) }
        }
        settle()
        motion.arm(ListTapMotionKind.State, awaitPage = true)
        Snapshot.sendApplyNotifications()
        rule.mainClock.advanceTimeBy(40)
        assertNotNull(motion.kind)
        motion.land()
        Snapshot.sendApplyNotifications()
        // The landing frame composes before the flag can drop, so the drop is the frame after that.
        rule.mainClock.advanceTimeByFrame()
        rule.mainClock.advanceTimeByFrame()
        assertNull(motion.kind)
    }

    @Test fun hubInsertAfterAFoldIsInstant() {
        rule.mainClock.autoAdvance = false
        var rows by mutableStateOf(listOf("Project", "Review", "Notes", "Next"))
        val motion = ListTapMotion()
        rule.setContent {
            PersonaTheme(darkTheme = false, reduceMotion = false, style = AppStyle.Glass) { TapRows(motion, rows, 411.dp) }
        }
        for (delayMs in listOf(50L, 150L)) {
            rule.runOnIdle { rows = listOf("Project", "Review", "Notes", "Next") }
            Snapshot.sendApplyNotifications()
            settle()
            motion.arm(ListTapMotionKind.State)
            rows = listOf("Project", "Next")
            Snapshot.sendApplyNotifications()
            rule.mainClock.advanceTimeBy(delayMs)
            assertTrue("the fold's row lost its spec at ${delayMs}ms", "Next" in motion.movingKeys)
            if (delayMs == 50L) {
                val moving = top("row-Next")
                rule.mainClock.advanceTimeBy(80)
                assertTrue("Next had already finished ($moving -> ${top("row-Next")})", abs(top("row-Next") - moving) > 1f)
            }
            assertSnaps("row-Project") { rows = listOf("New-$delayMs", "Project", "Next") }
        }
    }

    @Test fun flagDropsWhileTheMoveContinues() {
        rule.mainClock.autoAdvance = false
        var rows by mutableStateOf(listOf("Review", "Notes", "Release"))
        val motion = ListTapMotion()
        rule.setContent {
            PersonaTheme(darkTheme = false, reduceMotion = false, style = AppStyle.Glass) {
                BindListTapMotion(motion, rows)
                LazyColumn(Modifier.width(411.dp).height(480.dp).background(PersonaTheme.palette.background).testTag("shot")) {
                    with(ListTapLazyScope(this, motion)) {
                        items(rows, key = { it }) { title ->
                            val color = if (title == "Release") androidx.compose.ui.graphics.Color.Red else PersonaTheme.palette.background
                            Box(Modifier.height(56.dp).fillMaxWidth().background(color).testTag("row-$title"))
                        }
                    }
                }
            }
        }
        settle()
        val start = redRow()
        motion.arm(ListTapMotionKind.State)
        rows = listOf("Release", "Review", "Notes")
        Snapshot.sendApplyNotifications()
        val trace = (0 until 10).map {
            rule.mainClock.advanceTimeByFrame()
            redRow()
        }
        assertEquals(setOf("Release", "Review", "Notes"), motion.movingKeys)
        assertTrue("trace $trace start $start", trace.distinct().filter { it >= 0 }.size > 2)
        rows = listOf("Hub", "Release", "Review", "Notes")
        Snapshot.sendApplyNotifications()
        rule.mainClock.advanceTimeByFrame()
        rule.mainClock.advanceTimeByFrame()
        val hub = top("row-Hub")
        rule.mainClock.advanceTimeBy(100)
        assertEquals(hub, top("row-Hub"), 0.5f)
        assertTrue("Hub was given the fold's spec", "Hub" !in motion.movingKeys)
    }

    @Test fun refreshAfterPinLandsInstantly() {
        rule.mainClock.autoAdvance = false
        var rows by mutableStateOf(listOf("Review", "Notes", "Release"))
        val motion = ListTapMotion()
        rule.setContent {
            PersonaTheme(darkTheme = false, reduceMotion = false, style = AppStyle.Glass) { TapRows(motion, rows, 411.dp) }
        }
        settle()
        motion.arm(ListTapMotionKind.State, awaitPage = true)
        rule.mainClock.advanceTimeBy(40)
        assertNotNull(motion.kind)
        rows = listOf("Release", "Review", "Notes")
        Snapshot.sendApplyNotifications()
        motion.land()
        repeat(4) { rule.mainClock.advanceTimeByFrame() }
        assertTrue(motion.captured)
        rows = listOf("Release", "Hub", "Review", "Notes")
        Snapshot.sendApplyNotifications()
        rule.mainClock.advanceTimeByFrame()
        rule.mainClock.advanceTimeByFrame()
        val hub = top("row-Hub")
        rule.mainClock.advanceTimeBy(100)
        assertEquals(hub, top("row-Hub"), 0.5f)
        assertTrue("Hub" !in motion.movingKeys)
    }

    @Test fun removalBelowDoesNotFlyDown() {
        rule.mainClock.autoAdvance = false
        var rows by mutableStateOf(listOf("head", "Release", "Weekly", "fyi", "Office"))
        val motion = ListTapMotion()
        rule.setContent {
            PersonaTheme(darkTheme = false, reduceMotion = false, style = AppStyle.Glass) {
                BindListTapMotion(motion, rows)
                LazyColumn(Modifier.width(411.dp).height(900.dp).background(PersonaTheme.palette.background).testTag("shot")) {
                    with(ListTapLazyScope(this, motion)) {
                        items(rows, key = { it }) { title ->
                            val color = if (title == "Office") androidx.compose.ui.graphics.Color.Red else androidx.compose.ui.graphics.Color(0xFFE4E4E4)
                            Box(Modifier.height(96.dp).fillMaxWidth().background(color).testTag("row-$title"))
                        }
                    }
                }
            }
        }
        settle()
        val start = redRow()
        motion.arm(ListTapMotionKind.State)
        rule.mainClock.advanceTimeByFrame()
        rows = listOf("head", "Release", "fyi", "Office")
        Snapshot.sendApplyNotifications()
        val trace = (0 until 8).map {
            rule.mainClock.advanceTimeByFrame()
            redRow()
        }
        // The row under a removal moves up, by about its own height. A frame below the start is the fly.
        assertTrue(
            "flew or snapped start $start trace $trace",
            trace.all { it <= start + 2 } && trace.any { it < start - 8 },
        )
    }

    @Test fun pinnedHeaderDoesNotJump() {
        rule.mainClock.autoAdvance = false
        var above by mutableStateOf((1..8).map { "A$it" })
        var below by mutableStateOf((1..8).map { "B$it" })
        var jump by mutableStateOf(false)
        val motion = ListTapMotion()
        lateinit var scroll: LazyListState
        rule.setContent {
            scroll = rememberLazyListState()
            PersonaTheme(darkTheme = false, reduceMotion = false, style = AppStyle.Glass) {
                // "group" is in the diff, so without the pin check the header would slide.
                BindListTapMotion(motion, above + listOf("group") + below)
                LaunchedEffect(jump) { if (jump) scroll.scrollToItem(above.size) }
                LazyColumn(state = scroll, modifier = Modifier.width(411.dp).height(160.dp).testTag("shot")) {
                    with(ListTapLazyScope(this, motion, scroll)) {
                        items(above, key = { it }) { title ->
                            Box(Modifier.height(48.dp).fillMaxWidth().testTag("row-$title"))
                        }
                        stickyHeader(key = "group") {
                            Box(Modifier.height(40.dp).fillMaxWidth().background(PersonaTheme.palette.background).testTag("pinned"))
                        }
                        items(below, key = { it }) { title ->
                            Box(Modifier.height(48.dp).fillMaxWidth().testTag("row-$title"))
                        }
                    }
                }
            }
        }
        settle()
        jump = true
        Snapshot.sendApplyNotifications()
        repeat(4) { rule.mainClock.advanceTimeByFrame() }
        val pinned = top("pinned")
        assertTrue("header not pinned ($pinned)", pinned < 2f)
        assertTrue("pinned header is not stuck", motion.isStuck("group"))
        motion.arm(ListTapMotionKind.State)
        below = listOf("B1", "B2", "B3", "B4")
        Snapshot.sendApplyNotifications()
        val foldingBelow = (1..8).map {
            rule.mainClock.advanceTimeByFrame()
            top("pinned")
        }
        motion.arm(ListTapMotionKind.State)
        above = emptyList()
        Snapshot.sendApplyNotifications()
        val foldingAbove = (1..8).map {
            rule.mainClock.advanceTimeByFrame()
            top("pinned")
        }
        val out = File("build/list-tap-shots").also { it.mkdirs() }
        save(out, "sticky-after-fold.png")
        assertTrue("below $foldingBelow pinned $pinned", foldingBelow.all { abs(it - pinned) < 2f })
        // One clamp when the group above goes, and no value between the two ends.
        assertTrue("above slid $foldingAbove", foldingAbove.distinct().size <= 2)
    }

    @Test fun unpinnedHeaderSlidesWithTheRows() {
        rule.mainClock.autoAdvance = false
        var above by mutableStateOf((1..6).map { "A$it" })
        val below = (1..3).map { "B$it" }
        val motion = ListTapMotion()
        lateinit var scroll: LazyListState
        rule.setContent {
            scroll = rememberLazyListState()
            PersonaTheme(darkTheme = false, reduceMotion = false, style = AppStyle.Glass) {
                BindListTapMotion(motion, above + listOf("group") + below)
                LazyColumn(state = scroll, modifier = Modifier.width(411.dp).height(900.dp).testTag("shot")) {
                    with(ListTapLazyScope(this, motion, scroll)) {
                        items(above, key = { it }) { title ->
                            Box(Modifier.height(48.dp).fillMaxWidth().testTag("row-$title"))
                        }
                        stickyHeader(key = "group") {
                            Box(Modifier.height(40.dp).fillMaxWidth().background(PersonaTheme.palette.background).testTag("header"))
                        }
                        items(below, key = { it }) { title ->
                            Box(Modifier.height(48.dp).fillMaxWidth().testTag("row-$title"))
                        }
                    }
                }
            }
        }
        settle()
        val start = top("header")
        assertTrue("header started pinned ($start)", start > 40f)
        val out = File("build/list-tap-shots").also { it.mkdirs() }
        save(out, "unpinned-header-start.png")
        motion.arm(ListTapMotionKind.State)
        above = listOf("A1", "A2")
        Snapshot.sendApplyNotifications()
        var savedMid = false
        val frames = (1..10).map {
            rule.mainClock.advanceTimeByFrame()
            val y = top("header")
            if (!savedMid && abs(y - start) > 4f) {
                save(out, "unpinned-header-mid.png")
                savedMid = true
            }
            y
        }
        rule.mainClock.advanceTimeBy(400)
        val end = top("header")
        save(out, "unpinned-header-end.png")
        assertTrue("header did not move ($start -> $end)", abs(start - end) > 20f)
        assertTrue("no frame between $start and $end: $frames", frames.any { y ->
            (y - start) * (y - end) < 0f && abs(y - start) > 4f && abs(y - end) > 4f
        })
    }

    @Test fun severalChildrenInOneItemStack() {
        rule.setContent {
            PersonaTheme(darkTheme = false, reduceMotion = false, style = AppStyle.Glass) {
                LazyColumn(Modifier.width(411.dp).height(400.dp)) {
                    item("raw") {
                        Box(Modifier.height(36.dp).fillMaxWidth().testTag("raw-bay"))
                        Box(Modifier.height(36.dp).fillMaxWidth().testTag("raw-segment"))
                    }
                    with(ListTapLazyScope(this, ListTapMotion())) {
                        item("filters") {
                            Box(Modifier.height(36.dp).fillMaxWidth().testTag("bay"))
                            Box(Modifier.height(36.dp).fillMaxWidth().testTag("segment"))
                        }
                    }
                }
            }
        }
        val rawBay = rule.onNodeWithTag("raw-bay").getUnclippedBoundsInRoot().top.value
        val rawSegment = rule.onNodeWithTag("raw-segment").getUnclippedBoundsInRoot().top.value
        val bay = rule.onNodeWithTag("bay").getUnclippedBoundsInRoot().top.value
        val segment = rule.onNodeWithTag("segment").getUnclippedBoundsInRoot().top.value
        assertTrue("a plain item overlapped ($rawBay, $rawSegment)", rawSegment > rawBay + 20f)
        assertTrue("segment overlapped the bay ($bay, $segment)", segment > bay + 20f)
        assertEquals(rawSegment - rawBay, segment - bay, 1f)
    }

    @Test fun restingPixelsMatchAListWithoutTheModifier() {
        rule.mainClock.autoAdvance = false
        val rows = listOf("One", "Two", "Three")
        var wrapped by mutableStateOf(false)
        val motion = ListTapMotion()
        rule.setContent {
            PersonaTheme(darkTheme = false, reduceMotion = false, style = AppStyle.Glass) {
                if (wrapped) BindListTapMotion(motion, rows)
                LazyColumn(Modifier.width(411.dp).height(240.dp).background(PersonaTheme.palette.background).testTag("shot")) {
                    val draw: androidx.compose.foundation.lazy.LazyListScope.() -> Unit = {
                        items(rows, key = { it }) { title -> JRow(title, Modifier.testTag("row-$title")) }
                    }
                    if (wrapped) with(ListTapLazyScope(this, motion)) { draw() } else draw()
                }
            }
        }
        settle()
        val bare = rule.onNodeWithTag("shot").captureToImage().asAndroidBitmap()
        wrapped = true
        Snapshot.sendApplyNotifications()
        settle()
        val withModifier = rule.onNodeWithTag("shot").captureToImage().asAndroidBitmap()
        assertEquals(bare.width, withModifier.width)
        assertEquals(bare.height, withModifier.height)
        var different = 0
        for (y in 0 until bare.height) for (x in 0 until bare.width) {
            if (bare.getPixel(x, y) != withModifier.getPixel(x, y)) different++
        }
        assertEquals(0, different)
    }

    @Test fun hubPushCostOnALongList() {
        rule.mainClock.autoAdvance = false
        var wrapped by mutableStateOf(false)
        var rows by mutableStateOf((0 until 300).map { "r$it" })
        var counting by mutableStateOf(false)
        val seen = intArrayOf(0)
        val motion = ListTapMotion()
        rule.setContent {
            PersonaTheme(darkTheme = false, reduceMotion = false, style = AppStyle.Glass) {
                if (wrapped) BindListTapMotion(motion, rows)
                LazyColumn(Modifier.width(411.dp).height(480.dp).testTag("shot")) {
                    val draw: androidx.compose.foundation.lazy.LazyListScope.() -> Unit = {
                        items(rows, key = { it }) {
                            if (counting) SideEffect { seen[0]++ }
                            Box(Modifier.height(48.dp).fillMaxWidth())
                        }
                    }
                    if (wrapped) with(ListTapLazyScope(this, motion)) { draw() } else draw()
                }
            }
        }
        fun push(): Pair<Long, Int> {
            settle()
            seen[0] = 0
            counting = true
            Snapshot.sendApplyNotifications()
            rule.mainClock.advanceTimeByFrame()
            seen[0] = 0
            val started = System.nanoTime()
            rows = listOf("hub-${rows.size}") + rows
            Snapshot.sendApplyNotifications()
            repeat(4) { rule.mainClock.advanceTimeByFrame() }
            counting = false
            return (System.nanoTime() - started) to seen[0]
        }
        val bare = push()
        wrapped = true
        val withModifier = push()
        assertTrue(
            "hub push recomposed ${withModifier.second} rows, bare ${bare.second}",
            withModifier.second <= bare.second + 40,
        )
    }

    @Test fun shortChildAndZeroItemMatchARawItem() {
        rule.setContent {
            PersonaTheme(darkTheme = false, reduceMotion = false, style = AppStyle.Glass) {
                LazyColumn(Modifier.width(411.dp).height(320.dp)) {
                    item("raw") {
                        JTextButton("Earlier", {}, Modifier.testTag("raw-button"))
                        Box(Modifier.size(0.dp).testTag("raw-zero"))
                    }
                    with(ListTapLazyScope(this, ListTapMotion())) {
                        item("wrapped") {
                            JTextButton("Earlier", {}, Modifier.testTag("wrap-button"))
                            Box(Modifier.size(0.dp).testTag("wrap-zero"))
                        }
                    }
                }
            }
        }
        val raw = rule.onNodeWithTag("raw-button").getUnclippedBoundsInRoot()
        val wrap = rule.onNodeWithTag("wrap-button").getUnclippedBoundsInRoot()
        assertEquals((raw.right - raw.left).value, (wrap.right - wrap.left).value, 1f)
        assertEquals(raw.left.value, wrap.left.value, 1f)
        val zero = rule.onNodeWithTag("wrap-zero").getUnclippedBoundsInRoot()
        val rawZero = rule.onNodeWithTag("raw-zero").getUnclippedBoundsInRoot()
        assertEquals((rawZero.right - rawZero.left).value, (zero.right - zero.left).value, 0.5f)
        assertEquals((rawZero.bottom - rawZero.top).value, (zero.bottom - zero.top).value, 0.5f)
        assertEquals(0f, (zero.right - zero.left).value, 0.5f)
        assertEquals(0f, (zero.bottom - zero.top).value, 0.5f)
    }

    @Test fun aRowThatLeftDoesNotFlyBackFromItsOldPlace() {
        rule.mainClock.autoAdvance = false
        var rows by mutableStateOf(listOf("Keep", "A1", "A2", "Tail"))
        val motion = ListTapMotion()
        rule.setContent {
            PersonaTheme(darkTheme = false, reduceMotion = false, style = AppStyle.Glass) {
                TapRows(motion, rows, 411.dp)
            }
        }
        settle()
        motion.arm(ListTapMotionKind.State)
        rows = listOf("Keep", "Tail")
        Snapshot.sendApplyNotifications()
        rule.mainClock.advanceTimeBy(700)
        rows = listOf("New", "Keep", "Tail")
        Snapshot.sendApplyNotifications()
        settle()
        motion.arm(ListTapMotionKind.State)
        rows = listOf("New", "Keep", "A1", "A2", "Tail")
        Snapshot.sendApplyNotifications()
        val frames = (1..6).map {
            rule.mainClock.advanceTimeByFrame()
            top("row-A1")
        }
        assertTrue("A1 flew from a stale y: $frames", frames.max() - frames.min() < 3f)
    }

    @Test fun aSecondTapContinuesFromThePixelsStillMoving() {
        rule.mainClock.autoAdvance = false
        var rows by mutableStateOf(listOf("Project", "Review", "Notes", "Next"))
        val motion = ListTapMotion()
        rule.setContent {
            PersonaTheme(darkTheme = false, reduceMotion = false, style = AppStyle.Glass) { TapRows(motion, rows, 411.dp) }
        }
        settle()
        motion.arm(ListTapMotionKind.State)
        rows = listOf("Project", "Next")
        Snapshot.sendApplyNotifications()
        repeat(3) { rule.mainClock.advanceTimeByFrame() }
        val mid = top("row-Next")
        assertTrue("the first spring had not started ($mid)", abs(motion.shiftPx("Next")) > 4)
        motion.arm(ListTapMotionKind.State)
        rows = listOf("Next", "Project")
        Snapshot.sendApplyNotifications()
        rule.mainClock.advanceTimeByFrame()
        val after = top("row-Next")
        assertTrue("second tap jumped $mid -> $after", abs(after - mid) < 28f)
    }

    @Test fun theSpringIsNotCutAtTheTokenDuration() {
        rule.mainClock.autoAdvance = false
        var rows by mutableStateOf((0 until 12).map { "r$it" })
        val motion = ListTapMotion()
        rule.setContent {
            PersonaTheme(darkTheme = false, reduceMotion = false, style = AppStyle.Glass) {
                BindListTapMotion(motion, rows)
                LazyColumn(Modifier.width(411.dp).height(1000.dp).testTag("shot")) {
                    with(ListTapLazyScope(this, motion)) {
                        items(rows, key = { it }) { title ->
                            Box(Modifier.height(80.dp).fillMaxWidth().testTag("row-$title"))
                        }
                    }
                }
            }
        }
        settle()
        motion.arm(ListTapMotionKind.State)
        rows = listOf("r0", "r11")
        Snapshot.sendApplyNotifications()
        rule.mainClock.advanceTimeBy(Tokens.Motion.State.duration.toLong())
        val atDuration = top("row-r11")
        rule.mainClock.advanceTimeByFrame()
        val next = top("row-r11")
        assertTrue("cut the spring $atDuration -> $next", abs(next - atDuration) < 6f)
    }

    @Test fun aScrollDuringTheSpringDoesNotSnap() {
        rule.mainClock.autoAdvance = false
        var rows by mutableStateOf(listOf("Project", "Review", "Notes", "Next", "Last"))
        var drag by mutableStateOf(0)
        val motion = ListTapMotion()
        lateinit var scroll: LazyListState
        rule.setContent {
            scroll = rememberLazyListState()
            PersonaTheme(darkTheme = false, reduceMotion = false, style = AppStyle.Glass) {
                BindListTapMotion(motion, rows)
                LaunchedEffect(drag) { if (drag > 0) scroll.scroll { scrollBy(drag.toFloat()) } }
                LazyColumn(state = scroll, modifier = Modifier.nestedScroll(listTapScroll(motion)).width(411.dp).height(480.dp).testTag("shot")) {
                    with(ListTapLazyScope(this, motion, scroll)) {
                        items(rows, key = { it }) { title -> Box(Modifier.height(72.dp).fillMaxWidth().testTag("row-$title")) }
                    }
                }
            }
        }
        settle()
        motion.arm(ListTapMotionKind.State)
        rows = listOf("Project", "Next", "Last")
        Snapshot.sendApplyNotifications()
        repeat(3) { rule.mainClock.advanceTimeByFrame() }
        val moving = motion.shiftPx("Next")
        assertTrue("spring was not running ($moving)", abs(moving) > 4)
        val before = top("row-Next")
        drag = 80
        Snapshot.sendApplyNotifications()
        repeat(2) { rule.mainClock.advanceTimeByFrame() }
        assertTrue("scroll snapped the spring (${motion.shiftPx("Next")})", abs(motion.shiftPx("Next")) > 2)
        assertTrue("row ignored the scroll $before -> ${top("row-Next")}", top("row-Next") < before - 20f)
    }

    @Test fun scrollingWithoutATapDoesNotRecomposeTheHeader() {
        rule.mainClock.autoAdvance = false
        val above = (1..12).map { "A$it" }
        val below = (1..8).map { "B$it" }
        var jump by mutableStateOf(false)
        val seen = intArrayOf(0)
        lateinit var scroll: LazyListState
        rule.setContent {
            scroll = rememberLazyListState()
            PersonaTheme(darkTheme = false, reduceMotion = false, style = AppStyle.Glass) {
                LaunchedEffect(jump) { if (jump) scroll.scroll { scrollBy(140f) } }
                LazyColumn(state = scroll, modifier = Modifier.width(411.dp).height(220.dp)) {
                    with(ListTapLazyScope(this, ListTapMotion(), scroll)) {
                        items(above, key = { it }) { Box(Modifier.height(48.dp).fillMaxWidth()) }
                        stickyHeader(key = "group") {
                            SideEffect { seen[0]++ }
                            Box(Modifier.height(40.dp).fillMaxWidth().testTag("header"))
                        }
                        items(below, key = { it }) { Box(Modifier.height(48.dp).fillMaxWidth()) }
                    }
                }
            }
        }
        settle()
        val composed = seen[0]
        jump = true
        Snapshot.sendApplyNotifications()
        repeat(4) { rule.mainClock.advanceTimeByFrame() }
        assertTrue("header recomposed on scroll (${seen[0]} vs $composed)", seen[0] <= composed)
    }

    @Test fun aStalePageDoesNotTakeTheTap() {
        rule.mainClock.autoAdvance = false
        var rows by mutableStateOf(listOf("Alpha", "Beta", "Gamma"))
        val motion = ListTapMotion()
        rule.setContent {
            PersonaTheme(darkTheme = false, reduceMotion = false, style = AppStyle.Glass) { TapRows(motion, rows, 411.dp) }
        }
        settle()
        motion.arm(ListTapMotionKind.State, awaitPage = true, epoch = 2)
        Snapshot.sendApplyNotifications()
        motion.land(1)
        rule.mainClock.advanceTimeByFrame()
        assertNotNull(motion.kind)
        rows = listOf("Gamma", "Alpha")
        Snapshot.sendApplyNotifications()
        motion.land(2)
        rule.mainClock.advanceTimeByFrame()
        assertTrue(motion.captured)
        assertTrue("Gamma" in motion.movingKeys)
    }

    @Test fun aStalePageDoesNotAnimateAHubInsert() {
        rule.mainClock.autoAdvance = false
        var rows by mutableStateOf(listOf("A", "B", "C", "D"))
        var epoch by mutableStateOf(0)
        val motion = ListTapMotion()
        rule.setContent {
            PersonaTheme(darkTheme = false, reduceMotion = false, style = AppStyle.Glass) {
                BindListTapMotion(motion)
                LazyColumn(Modifier.width(411.dp).height(480.dp).testTag("shot")) {
                    val tap = ListTapLazyScope(this, motion, epoch = epoch)
                    with(tap) {
                        items(rows, key = { it }) { title ->
                            Box(Modifier.height(48.dp).fillMaxWidth().testTag("row-$title"))
                        }
                    }
                    tap.commit()
                }
            }
        }
        settle()
        motion.arm(ListTapMotionKind.State, awaitPage = true, epoch = 2)
        rows = listOf("Hub", "A", "B", "C", "D")
        epoch = 1
        Snapshot.sendApplyNotifications()
        repeat(3) { rule.mainClock.advanceTimeByFrame() }
        val hub = top("row-Hub")
        rule.mainClock.advanceTimeBy(80)
        assertEquals("hub insert sprang", hub, top("row-Hub"), 0.5f)
        assertEquals(0, motion.shiftPx("Hub"))
        val before = top("row-D")
        rows = listOf("Hub", "A", "D")
        epoch = 2
        Snapshot.sendApplyNotifications()
        motion.land(2)
        val trace = (1..8).map {
            rule.mainClock.advanceTimeByFrame()
            top("row-D")
        }
        val end = trace.last()
        assertEquals("hub moved with the fold", hub, top("row-Hub"), 1f)
        assertTrue(
            "folded rows did not spring shift=${motion.shiftPx("D")} captured=${motion.captured} moving=${motion.movingKeys} ($before -> $end): $trace",
            abs(before - end) > 8f && trace.any { (it - before) * (it - end) < 0f && abs(it - before) > 4f && abs(it - end) > 4f },
        )
    }

    @Test fun pinnedHeaderWithPagePaddingDoesNotJump() {
        rule.mainClock.autoAdvance = false
        var above by mutableStateOf((1..8).map { "A$it" })
        var below by mutableStateOf((1..8).map { "B$it" })
        var jump by mutableStateOf(false)
        val motion = ListTapMotion()
        lateinit var scroll: LazyListState
        rule.setContent {
            scroll = rememberLazyListState()
            PersonaTheme(darkTheme = false, reduceMotion = false, style = AppStyle.Glass) {
                BindListTapMotion(motion, above + listOf("group") + below)
                LaunchedEffect(jump) { if (jump) scroll.scrollToItem(above.size) }
                LazyColumn(
                    state = scroll,
                    contentPadding = PaddingValues(top = 48.dp),
                    modifier = Modifier.width(411.dp).height(220.dp).testTag("shot"),
                ) {
                    with(ListTapLazyScope(this, motion, scroll)) {
                        items(above, key = { it }) { title ->
                            Box(Modifier.height(48.dp).fillMaxWidth().testTag("row-$title"))
                        }
                        stickyHeader(key = "group") {
                            Box(Modifier.height(40.dp).fillMaxWidth().background(PersonaTheme.palette.background).testTag("pinned"))
                        }
                        items(below, key = { it }) { title ->
                            Box(Modifier.height(48.dp).fillMaxWidth().testTag("row-$title"))
                        }
                    }
                }
            }
        }
        settle()
        jump = true
        Snapshot.sendApplyNotifications()
        repeat(4) { rule.mainClock.advanceTimeByFrame() }
        val pinned = top("pinned")
        assertTrue("header not pinned ($pinned)", pinned < 56f)
        assertTrue("padded header is not stuck at $pinned", motion.isStuck("group"))
        motion.arm(ListTapMotionKind.State)
        below = listOf("B1", "B2", "B3", "B4")
        Snapshot.sendApplyNotifications()
        val folding = (1..8).map {
            rule.mainClock.advanceTimeByFrame()
            top("pinned")
        }
        assertTrue("padded pin slid $folding from $pinned", folding.all { abs(it - pinned) < 2f })
    }

    @Test fun armDoesNotRecomposeRows() {
        rule.mainClock.autoAdvance = false
        val rows = (0 until 20).map { "r$it" }
        val seen = intArrayOf(0)
        val motion = ListTapMotion()
        rule.setContent {
            PersonaTheme(darkTheme = false, reduceMotion = false, style = AppStyle.Glass) {
                BindListTapMotion(motion, rows)
                LazyColumn(Modifier.width(411.dp).height(480.dp)) {
                    with(ListTapLazyScope(this, motion)) {
                        items(rows, key = { it }) {
                            SideEffect { seen[0]++ }
                            Box(Modifier.height(48.dp).fillMaxWidth())
                        }
                    }
                }
            }
        }
        settle()
        seen[0] = 0
        motion.arm(ListTapMotionKind.State)
        Snapshot.sendApplyNotifications()
        repeat(3) { rule.mainClock.advanceTimeByFrame() }
        motion.disarm()
        Snapshot.sendApplyNotifications()
        repeat(3) { rule.mainClock.advanceTimeByFrame() }
        assertEquals("arm or disarm recomposed rows", 0, seen[0])
    }

    @Test fun aRowMissingFromTheLastFrameDoesNotSpring() {
        rule.mainClock.autoAdvance = false
        var rows by mutableStateOf(listOf("Above", "Live", "Stale"))
        val motion = ListTapMotion()
        rule.setContent {
            PersonaTheme(darkTheme = false, reduceMotion = false, style = AppStyle.Glass) {
                BindListTapMotion(motion, rows)
                LazyColumn(Modifier.width(411.dp).height(90.dp).testTag("shot")) {
                    with(ListTapLazyScope(this, motion)) {
                        items(rows, key = { it }) { title ->
                            Box(Modifier.height(48.dp).fillMaxWidth().testTag("row-$title"))
                        }
                    }
                }
            }
        }
        settle()
        motion.arm(ListTapMotionKind.State)
        rows = listOf("Live", "Stale")
        Snapshot.sendApplyNotifications()
        val live = (1..6).map {
            rule.mainClock.advanceTimeByFrame()
            top("row-Live")
        }
        val staleAt = top("row-Stale")
        rule.mainClock.advanceTimeBy(200)
        assertEquals("stale row sprang", staleAt, top("row-Stale"), 0.5f)
        assertTrue("the row on screen did not spring $live", abs(live.first() - live.last()) > 4f || live.max() - live.min() > 4f)
    }

    @Test fun aRowKeptComposedButNotVisibleDoesNotSpring() {
        rule.mainClock.autoAdvance = false
        val state = LazyListState()
        val motion = ListTapMotion()
        val scope = kotlinx.coroutines.CoroutineScope(kotlinx.coroutines.Dispatchers.Unconfined)
        rule.setContent {
            PersonaTheme(darkTheme = false, reduceMotion = false, style = AppStyle.Glass) {
                LazyColumn(Modifier.width(411.dp).height(120.dp), state) {
                    with(ListTapLazyScope(this, motion, state)) {
                        items(listOf("A", "B"), key = { it }) { title ->
                            Box(Modifier.height(48.dp).fillMaxWidth().testTag("row-$title"))
                        }
                    }
                }
            }
        }
        settle()
        motion.notePlaced("Ghost", 400f, scope, motion.liveFade)
        motion.noteComposed(listOf("A", "B", "Ghost"))
        motion.arm(ListTapMotionKind.State)
        motion.capture(listOf("Ghost", "A", "B"))
        motion.notePlaced("Ghost", 0f, scope, motion.liveFade)
        assertEquals("a row that was not on screen sprang", 0, motion.shiftPx("Ghost"))
    }

    @Test fun idleFramesStillSpringEveryVisibleRow() {
        rule.mainClock.autoAdvance = false
        var rows by mutableStateOf(listOf("A", "B", "C", "D"))
        val state = LazyListState()
        val motion = ListTapMotion()
        rule.setContent {
            PersonaTheme(darkTheme = false, reduceMotion = false, style = AppStyle.Glass) {
                BindListTapMotion(motion, rows)
                LazyColumn(Modifier.width(411.dp).height(400.dp), state) {
                    with(ListTapLazyScope(this, motion, state)) {
                        items(rows, key = { it }) { title ->
                            Box(Modifier.height(40.dp).fillMaxWidth().testTag("row-$title"))
                        }
                    }
                }
            }
        }
        settle()
        // Several frames with no tap. A wall-clock bucket would roll here and then skip a row.
        repeat(12) { rule.mainClock.advanceTimeByFrame() }
        val start = listOf("B", "C", "D").associateWith { top("row-$it") }
        motion.arm(ListTapMotionKind.State)
        rows = listOf("B", "C", "D")
        Snapshot.sendApplyNotifications()
        val path = listOf("B", "C", "D").associateWith { mutableListOf<Float>() }
        repeat(8) {
            rule.mainClock.advanceTimeByFrame()
            for (title in path.keys) path.getValue(title).add(top("row-$title"))
        }
        rule.mainClock.advanceTimeBy(400)
        for (title in listOf("B", "C", "D")) {
            val frames = path.getValue(title)
            val end = top("row-$title")
            val began = start.getValue(title)
            assertTrue(
                "$title did not spring ($began -> $end via $frames)",
                abs(began - end) > 8f && frames.any { y -> abs(y - began) > 2f && abs(y - end) > 2f },
            )
        }
    }

    @Test fun scrollingDoesNotKeepEveryRowThatPassed() {
        rule.mainClock.autoAdvance = false
        var jump by mutableStateOf(0)
        val state = LazyListState()
        val motion = ListTapMotion()
        rule.setContent {
            PersonaTheme(darkTheme = false, reduceMotion = false, style = AppStyle.Glass) {
                LaunchedEffect(jump) { if (jump > 0) state.scrollToItem(jump) }
                LazyColumn(Modifier.width(411.dp).height(80.dp), state) {
                    with(ListTapLazyScope(this, motion, state)) {
                        items(40, key = { "r$it" }) { Box(Modifier.height(40.dp).fillMaxWidth()) }
                    }
                }
            }
        }
        settle()
        repeat(15) { step ->
            jump = step + 1
            Snapshot.sendApplyNotifications()
            repeat(3) { rule.mainClock.advanceTimeByFrame() }
        }
        assertTrue("kept ${motion.retainedRows} rows", motion.retainedRows <= 6)
    }

    @Test fun anUnchangedRowBelowAnInsertIsNotTheSharedShift() {
        val motion = ListTapMotion()
        // Holds the launched spring so the assertion reads the offset it started at.
        val scope = kotlinx.coroutines.CoroutineScope(object : kotlinx.coroutines.CoroutineDispatcher() {
            override fun dispatch(context: kotlin.coroutines.CoroutineContext, block: Runnable) = Unit
        })
        fun place(key: String, y: Float) = motion.notePlaced(key, y, scope, motion.liveFade)
        motion.noteComposed(listOf("A", "B", "C", "D"))
        place("A", 0f)
        place("B", 50f)
        place("C", 150f)
        place("D", 200f)
        motion.arm(ListTapMotionKind.State)
        // Insert X and remove B. C's index stays 2, but it moved. It must not name the shared shift.
        motion.capture(listOf("X", "A", "C", "D"))
        place("X", 0f)
        place("A", 50f)
        place("C", 100f)
        place("D", 150f)
        assertEquals("A sprang the anchor's movement too", -50, motion.shiftPx("A"))
        assertEquals("C sprang", 0, motion.shiftPx("C"))
    }

    @Test fun anIdleReorderDoesNotSpringUntappedRows() {
        rule.mainClock.autoAdvance = false
        var rows by mutableStateOf(listOf("H", "A", "B", "C", "D"))
        val motion = ListTapMotion()
        rule.setContent {
            PersonaTheme(darkTheme = false, reduceMotion = false, style = AppStyle.Glass) {
                BindListTapMotion(motion, rows)
                LazyColumn(Modifier.width(411.dp).height(400.dp)) {
                    with(ListTapLazyScope(this, motion)) {
                        items(rows, key = { it }) { title ->
                            Box(Modifier.height(40.dp).fillMaxWidth().testTag("row-$title"))
                        }
                    }
                }
            }
        }
        settle()
        rows = listOf("H", "A", "C", "B", "D")
        Snapshot.sendApplyNotifications()
        repeat(4) { rule.mainClock.advanceTimeByFrame() }
        assertNull(motion.kind)
        val startC = top("row-C")
        val startD = top("row-D")
        motion.arm(ListTapMotionKind.State)
        rows = listOf("H", "A", "C", "D")
        Snapshot.sendApplyNotifications()
        val path = (1..8).map {
            rule.mainClock.advanceTimeByFrame()
            top("row-C")
        }
        rule.mainClock.advanceTimeBy(400)
        assertTrue("C sprang ($startC via $path -> ${top("row-C")})", path.all { abs(it - startC) < 2f })
        assertTrue("D did not spring ($startD -> ${top("row-D")})", abs(top("row-D") - startD) > 8f)
    }

    @Test fun aHubInsertIsOpaqueOnItsFirstFrame() {
        rule.mainClock.autoAdvance = false
        var rows by mutableStateOf(listOf("A"))
        val motion = ListTapMotion()
        rule.setContent {
            PersonaTheme(darkTheme = false, reduceMotion = false, style = AppStyle.Glass) {
                BindListTapMotion(motion, rows)
                LazyColumn(Modifier.width(120.dp).height(160.dp).background(Color.White).testTag("shot")) {
                    with(ListTapLazyScope(this, motion)) {
                        items(rows, key = { it }) { title ->
                            Box(Modifier.height(40.dp).fillMaxWidth().background(if (title == "H") Color.Red else Color.Blue))
                        }
                    }
                }
            }
        }
        settle()
        assertNull(motion.kind)
        rows = listOf("H", "A")
        Snapshot.sendApplyNotifications()
        rule.mainClock.advanceTimeByFrame()
        assertTrue("the insert flashed transparent (${redPixels()})", redPixels() > 20)
    }

    @Test fun unkeyedBlocksDoNotShareAKey() {
        rule.mainClock.autoAdvance = false
        val motion = ListTapMotion()
        rule.setContent {
            PersonaTheme(darkTheme = false, reduceMotion = false, style = AppStyle.Glass) {
                LazyColumn(Modifier.width(411.dp).height(400.dp)) {
                    with(ListTapLazyScope(this, motion)) {
                        item { Box(Modifier.height(20.dp).testTag("one")) }
                        items(2) { Box(Modifier.height(20.dp).testTag("many-$it")) }
                        commit()
                    }
                }
            }
        }
        settle()
        assertEquals(listOf("item-0", "item-1", "item-2"), motion.composedKeys)
    }

    @Test fun aRemovedRowFadesWithoutBeingRecomposed() {
        rule.mainClock.autoAdvance = false
        var rows by mutableStateOf(listOf("A", "B"))
        val motion = ListTapMotion()
        val seen = intArrayOf(0)
        rule.setContent {
            PersonaTheme(darkTheme = false, reduceMotion = false, style = AppStyle.Glass) {
                BindListTapMotion(motion, rows)
                LazyColumn(Modifier.width(120.dp).height(160.dp).background(Color.White).testTag("shot")) {
                    with(ListTapLazyScope(this, motion)) {
                        items(rows, key = { it }) { title ->
                            if (title == "A") SideEffect { seen[0]++ }
                            Box(Modifier.height(40.dp).fillMaxWidth().background(if (title == "A") Color.Red else Color.Blue))
                        }
                    }
                }
            }
        }
        settle()
        seen[0] = 0
        val before = redPixels()
        // Arm one frame before removing the row (as InboxScreen.animateChange / markAll and AgentsViewModel do)
        // so exitFade attaches fadeOutSpec while idle rows keep null animateItem specs with zero GraphicsLayer overhead.
        // Trigger a frame draw via redPixels() on the arm frame (on a device Choreographer draws every vsync;
        // Robolectric advanceTimeByFrame() only runs composition and layout).
        motion.arm(ListTapMotionKind.State)
        Snapshot.sendApplyNotifications()
        rule.mainClock.advanceTimeByFrame()
        assertTrue("row disappeared before removal", redPixels() > 20)
        rows = listOf("B")
        Snapshot.sendApplyNotifications()
        rule.mainClock.advanceTimeByFrame()
        val early = redPixels()
        repeat(3) { rule.mainClock.advanceTimeByFrame() }
        val mid = redPixels()
        rule.mainClock.advanceTimeBy(500)
        val late = redPixels()
        assertEquals("removing the row recomposed it", 0, seen[0])
        assertTrue("nothing red to fade ($before)", before > 20)
        assertTrue("the row popped ($before -> $early -> $mid -> $late)", early > 10 && mid > 10 && late < early / 2)
    }

    @Test fun idleRemoveAndReinsertLeavesNoGhostAndRemainsVisible() {
        rule.mainClock.autoAdvance = false
        var rows by mutableStateOf(listOf("A", "B", "C"))
        val motion = ListTapMotion()
        rule.setContent {
            PersonaTheme(darkTheme = false, reduceMotion = false, style = AppStyle.Glass) {
                BindListTapMotion(motion, rows)
                LazyColumn(Modifier.width(120.dp).height(160.dp).background(Color.White).testTag("shot")) {
                    with(ListTapLazyScope(this, motion)) {
                        items(rows, key = { it }) { title ->
                            Box(
                                Modifier
                                    .height(40.dp)
                                    .fillMaxWidth()
                                    .background(if (title == "A") Color.Red else Color.Blue)
                                    .testTag("row-$title"),
                            )
                        }
                    }
                }
            }
        }
        settle()
        assertNull(motion.kind)
        assertTrue("A was not drawn initially", redPixels() > 20)

        // Idle removal (e.g. reconnect/sync flicker) must not allocate a disappearing layer.
        rows = listOf("B", "C")
        Snapshot.sendApplyNotifications()
        rule.mainClock.advanceTimeByFrame()
        assertEquals("idle removal left disappearing red pixels", 0, redPixels())

        // Re-inserting the same key on the next frame must render at full alpha (never stuck at alpha = 0).
        rows = listOf("B", "A", "C")
        Snapshot.sendApplyNotifications()
        rule.mainClock.advanceTimeByFrame()
        val reinserted = redPixels()
        assertTrue("re-inserted row stayed invisible ($reinserted)", reinserted > 20)
        assertEquals("re-inserted row stayed at old slot", 40f, top("row-A"), 2f)

        // Removing it again while idle leaves zero ghost pixels behind.
        rows = listOf("B", "C")
        Snapshot.sendApplyNotifications()
        rule.mainClock.advanceTimeByFrame()
        assertEquals("second idle removal left ghost pixels", 0, redPixels())
    }

    @Test fun rowMoveNodeKeyReassignmentResetsStalePlacementShift() {
        rule.mainClock.autoAdvance = false
        val motion = ListTapMotion()
        val heldScope = kotlinx.coroutines.CoroutineScope(object : kotlinx.coroutines.CoroutineDispatcher() {
            override fun dispatch(context: kotlin.coroutines.CoroutineContext, block: Runnable) = Unit
        })
        motion.noteComposed(listOf("Old", "Other"))
        motion.notePlaced("Old", 160f, heldScope, motion.liveFade)
        motion.notePlaced("Other", 200f, heldScope, motion.liveFade)
        motion.arm(ListTapMotionKind.State)
        motion.capture(listOf("Other", "Old"))
        motion.notePlaced("Old", 0f, heldScope, motion.liveFade)
        assertEquals("Old should hold an active shift", 160, motion.shiftPx("Old"))

        var activeKey by mutableStateOf("Old")
        var attached by mutableStateOf(true)
        rule.setContent {
            PersonaTheme(darkTheme = false, reduceMotion = false, style = AppStyle.Glass) {
                Box(Modifier.width(120.dp).height(300.dp)) {
                    if (attached) {
                        Box(
                            Modifier
                                .then(RowMoveElement(motion, activeKey, heldScope, pin = null, stick = false))
                                .height(40.dp)
                                .fillMaxWidth()
                                .testTag("slot"),
                        )
                    }
                }
            }
        }
        rule.mainClock.advanceTimeByFrame()
        assertEquals("slot did not apply Old's active shift", 160f, top("slot"), 2f)

        // Reassigning the same RowMoveNode to a key with 0 shift must re-observe and invalidate placement immediately.
        activeKey = "New"
        Snapshot.sendApplyNotifications()
        rule.mainClock.advanceTimeByFrame()
        assertEquals("RowMoveNode kept stale shift after key reassignment", 0f, top("slot"), 2f)

        // Detach + reattach on a key whose shift cleared while detached must place cleanly at 0.
        activeKey = "Old"
        Snapshot.sendApplyNotifications()
        rule.mainClock.advanceTimeByFrame()
        assertEquals(160f, top("slot"), 2f)
        attached = false
        Snapshot.sendApplyNotifications()
        rule.mainClock.advanceTimeByFrame()
        motion.cancelAllShifts()
        attached = true
        Snapshot.sendApplyNotifications()
        rule.mainClock.advanceTimeByFrame()
        assertEquals("RowMoveNode kept stale shift across reattach", 0f, top("slot"), 2f)
    }

    @Test fun pruneRetainedWakesObserverAndResetsShiftToZero() {
        rule.mainClock.autoAdvance = false
        var rows by mutableStateOf(listOf("A", "B", "C", "D"))
        val motion = ListTapMotion()
        rule.setContent {
            PersonaTheme(darkTheme = false, reduceMotion = false, style = AppStyle.Glass) {
                BindListTapMotion(motion, rows)
                LazyColumn(Modifier.width(411.dp).height(400.dp)) {
                    with(ListTapLazyScope(this, motion)) {
                        items(rows, key = { it }) { title ->
                            Box(Modifier.height(60.dp).fillMaxWidth().testTag("row-$title"))
                        }
                    }
                }
            }
        }
        settle()
        motion.arm(ListTapMotionKind.State)
        rows = listOf("A", "D")
        Snapshot.sendApplyNotifications()
        repeat(2) { rule.mainClock.advanceTimeByFrame() }
        assertTrue("expected D to hold a live shift", abs(motion.shiftPx("D")) > 10)
        val displaced = top("row-D")
        assertTrue("expected D to be displaced below its resting slot 60 ($displaced)", displaced > 70f)

        // Dropping D via pruneRetained while its node is still attached must cancel the shift and wake placement to 0.
        motion.pruneRetained(setOf("A"))
        Snapshot.sendApplyNotifications()
        rule.mainClock.advanceTimeByFrame()
        assertEquals(0, motion.shiftPx("D"))
        assertEquals("pruneRetained failed to reset row-D placement to resting slot", 60f, top("row-D"), 2f)
    }

    @Test fun programmaticScrollDuringArmedTapCancelsShiftsAndPreventsOverlap() {
        rule.mainClock.autoAdvance = false
        var rows by mutableStateOf((0 until 25).map { "r$it" })
        val state = LazyListState(firstVisibleItemIndex = 12)
        val motion = ListTapMotion()
        var jumpToTop by mutableStateOf(false)
        rule.setContent {
            PersonaTheme(darkTheme = false, reduceMotion = false, style = AppStyle.Glass) {
                BindListTapMotion(motion, rows)
                LaunchedEffect(jumpToTop) {
                    if (jumpToTop) {
                        motion.onProgrammaticScroll()
                        state.scrollToItem(0)
                    }
                }
                LazyColumn(
                    Modifier
                        .width(411.dp)
                        .height(320.dp)
                        .nestedScroll(listTapScroll(motion)),
                    state = state,
                ) {
                    with(ListTapLazyScope(this, motion, state)) {
                        items(rows, key = { it }) { title ->
                            Box(Modifier.height(56.dp).fillMaxWidth().testTag("row-$title"))
                        }
                    }
                }
            }
        }
        settle()

        // Concurrent tap + programmatic scroll jump + list mutation (simulating reading-position correction / restore).
        motion.arm(ListTapMotionKind.State)
        jumpToTop = true
        rows = listOf("r12", "r13", "r0", "r1", "r2", "r3", "r4") + rows.drop(5)
        Snapshot.sendApplyNotifications()
        repeat(4) { rule.mainClock.advanceTimeByFrame() }

        assertEquals(0, motion.shiftPx("r12"))
        assertEquals(0, motion.shiftPx("r13"))
        assertEquals(0, motion.shiftPx("r0"))
        val b0 = rule.onNodeWithTag("row-r12").getUnclippedBoundsInRoot()
        val b1 = rule.onNodeWithTag("row-r13").getUnclippedBoundsInRoot()
        val b2 = rule.onNodeWithTag("row-r0").getUnclippedBoundsInRoot()
        assertTrue("r12 overlapped r13 ($b0 vs $b1)", b0.bottom <= b1.top + 1.dp)
        assertTrue("r13 overlapped r0 ($b1 vs $b2)", b1.bottom <= b2.top + 1.dp)
    }

    @Test fun notePlacedIgnoresLiveFadeWhenReduceMotionOrDisarmed() {
        rule.mainClock.autoAdvance = false
        var reduce by mutableStateOf(true)
        val rows = listOf("A", "B")
        val motion = ListTapMotion()
        val heldScope = kotlinx.coroutines.CoroutineScope(object : kotlinx.coroutines.CoroutineDispatcher() {
            override fun dispatch(context: kotlin.coroutines.CoroutineContext, block: Runnable) = Unit
        })
        rule.setContent {
            PersonaTheme(darkTheme = false, reduceMotion = reduce, style = AppStyle.Glass) {
                BindListTapMotion(motion, rows)
                Box(Modifier.size(10.dp))
            }
        }
        settle()

        // Under Reduce Motion, even passing liveFade explicitly must not launch a shift.
        motion.notePlaced("B", 120f, heldScope, motion.liveFade)
        motion.arm(ListTapMotionKind.State)
        motion.capture(listOf("B", "A"))
        motion.notePlaced("B", 0f, heldScope, motion.liveFade)
        assertEquals(0, motion.shiftPx("B"))
        assertFalse("Reduce Motion launched a shift", motion.hasActiveShifts())

        // After disarm(), passing liveFade must not launch a shift either.
        reduce = false
        Snapshot.sendApplyNotifications()
        settle()
        motion.notePlaced("B", 120f, heldScope, motion.liveFade)
        motion.arm(ListTapMotionKind.State)
        motion.capture(listOf("B", "A"))
        motion.disarm()
        motion.notePlaced("B", 0f, heldScope, motion.liveFade)
        assertEquals(0, motion.shiftPx("B"))
        assertFalse("disarmed motion launched a shift", motion.hasActiveShifts())
    }

    @Test fun backgroundThreadScopeCancellationCleansUpShiftSafelyOnMainThread() {
        val motion = ListTapMotion()
        val parentJob = kotlinx.coroutines.SupervisorJob()
        val entered = CountDownLatch(1)
        // A real dispatcher whose launch suspends on the test frame clock (never completes on its own)
        // so cancelling parentJob from a background thread immediately transitions the child Job to Completed
        // and runs invokeOnCompletion on that background thread.
        val scope = kotlinx.coroutines.CoroutineScope(
            parentJob + kotlinx.coroutines.Dispatchers.Default +
                object : androidx.compose.runtime.MonotonicFrameClock {
                    override suspend fun <R> withFrameNanos(onFrame: (Long) -> R): R =
                        kotlinx.coroutines.suspendCancellableCoroutine { entered.countDown() }
                },
        )
        motion.noteComposed(listOf("A", "B"))
        motion.notePlaced("B", 120f, scope, motion.liveFade)
        motion.arm(ListTapMotionKind.State)
        motion.capture(listOf("B", "A"))
        motion.notePlaced("B", 0f, scope, motion.liveFade)
        assertEquals(120, motion.shiftPx("B"))
        assertTrue("shift coroutine never reached frame suspension", entered.await(5, TimeUnit.SECONDS))

        val done = CountDownLatch(1)
        val worker = Thread {
            runBlocking { parentJob.cancelAndJoin() }
            done.countDown()
        }
        worker.start()
        assertTrue(done.await(5, TimeUnit.SECONDS))
        // Drain the main-looper cleanup posted by the worker thread's completion callback.
        shadowOf(Looper.getMainLooper()).idle()

        assertEquals(0, motion.shiftPx("B"))
        assertFalse("background cancellation left an active shift", motion.hasActiveShifts())
    }

    @Test fun perKeyTicksDoNotInvalidateUnrelatedRows() {
        rule.mainClock.autoAdvance = false
        var rows by mutableStateOf(listOf("Stay", "Drop", "Move"))
        val motion = ListTapMotion()
        val stayPlaced = intArrayOf(0)
        val movePlaced = intArrayOf(0)
        rule.setContent {
            PersonaTheme(darkTheme = false, reduceMotion = false, style = AppStyle.Glass) {
                BindListTapMotion(motion, rows)
                LazyColumn(Modifier.width(411.dp).height(400.dp)) {
                    with(ListTapLazyScope(this, motion)) {
                        items(rows, key = { it }) { title ->
                            Box(
                                Modifier
                                    .height(60.dp)
                                    .fillMaxWidth()
                                    .onPlaced {
                                        if (title == "Stay") stayPlaced[0]++
                                        if (title == "Move") movePlaced[0]++
                                    }
                                    .testTag("row-$title"),
                            )
                        }
                    }
                }
            }
        }
        settle()
        motion.arm(ListTapMotionKind.State)
        rows = listOf("Stay", "Move")
        Snapshot.sendApplyNotifications()
        // Advance past the initial list-change layout pass, then count per-frame spring ticks only.
        repeat(2) { rule.mainClock.advanceTimeByFrame() }
        assertTrue("Move should be mid-spring", abs(motion.shiftPx("Move")) > 4)
        stayPlaced[0] = 0
        movePlaced[0] = 0
        repeat(6) { rule.mainClock.advanceTimeByFrame() }
        assertTrue("animating row did not re-place during spring (${movePlaced[0]})", movePlaced[0] >= 4)
        assertEquals("unrelated stationary row was invalidated by another row's tick", 0, stayPlaced[0])
    }

    @Test fun readingPositionCorrectionPreservesPendingTapAwaitingEpoch() {
        rule.mainClock.autoAdvance = false
        var rows by mutableStateOf((0 until 20).map { "r$it" })
        var epoch by mutableStateOf(0)
        val state = LazyListState(firstVisibleItemIndex = 8)
        val motion = ListTapMotion()
        rule.setContent {
            PersonaTheme(darkTheme = false, reduceMotion = false, style = AppStyle.Glass) {
                BindListTapMotion(motion)
                LazyColumn(
                    Modifier
                        .width(411.dp)
                        .height(320.dp)
                        .nestedScroll(listTapScroll(motion)),
                    state = state,
                ) {
                    val tap = ListTapLazyScope(this, motion, state, epoch = epoch)
                    with(tap) {
                        items(rows, key = { it }) { title ->
                            Box(Modifier.height(56.dp).fillMaxWidth().testTag("row-$title"))
                        }
                    }
                    tap.commit()
                }
            }
        }
        settle()

        // User taps while scrolled down; waiting for throttled tapEpoch = 2.
        motion.arm(ListTapMotionKind.State, awaitPage = true, epoch = 2)
        // Before epoch 2 lands, an older hub page (epoch 1) inserts a row above the viewport and
        // KeepAgentReadingPosition corrects the scroll offset without disarming the pending tap.
        rows = listOf("Hub") + rows
        epoch = 1
        Snapshot.sendApplyNotifications()
        rule.mainClock.advanceTimeByFrame()
        rule.runOnIdle {
            motion.cancelShiftsAndResetBaseline()
            state.requestScrollToItem(9, 0)
        }
        Snapshot.sendApplyNotifications()
        repeat(3) { rule.mainClock.advanceTimeByFrame() }

        assertEquals("reading correction disarmed pending tap awaiting epoch 2", ListTapMotionKind.State, motion.kind)
        assertFalse("stale epoch should not have captured the tap", motion.captured)
        assertEquals(0, motion.shiftPx("r10"))

        val beforeTap = top("row-r10")
        // Now the tap's own epoch 2 lands, removing r8 above r10.
        rows = rows.filterNot { it == "r8" }
        epoch = 2
        Snapshot.sendApplyNotifications()
        motion.land(2)
        val trace = (1..8).map {
            rule.mainClock.advanceTimeByFrame()
            top("row-r10")
        }
        rule.mainClock.advanceTimeBy(400)
        val afterTap = top("row-r10")
        val low = minOf(beforeTap, afterTap) - 2f
        val high = maxOf(beforeTap, afterTap) + 2f
        assertTrue(
            "r10 did not spring smoothly after pending tap survived reading correction ($beforeTap -> $afterTap via $trace)",
            abs(beforeTap - afterTap) > 8f &&
                trace.all { it in low..high } &&
                trace.any { y -> (y - beforeTap) * (y - afterTap) < 0f && abs(y - beforeTap) > 4f && abs(y - afterTap) > 4f },
        )

        // Contrast: once a tap diff IS already captured, cancelShiftsAndResetBaseline cancels shifts AND disarms.
        motion.arm(ListTapMotionKind.State, awaitPage = true, epoch = 3)
        rows = rows.filterNot { it == "r9" }
        epoch = 3
        Snapshot.sendApplyNotifications()
        motion.land(3)
        repeat(2) { rule.mainClock.advanceTimeByFrame() }
        assertTrue("expected epoch 3 tap to be captured", motion.captured)
        motion.cancelShiftsAndResetBaseline()
        assertNull("captured tap should disarm on reading-position correction", motion.kind)
        assertFalse(motion.hasActiveShifts())
    }

    @Test fun noOpTapAfterStaleRebaseDisarmsImmediately() {
        rule.mainClock.autoAdvance = false
        var rows by mutableStateOf(listOf("A", "B", "C", "D"))
        var epoch by mutableStateOf(0)
        val state = LazyListState()
        val motion = ListTapMotion()
        rule.setContent {
            PersonaTheme(darkTheme = false, reduceMotion = false, style = AppStyle.Glass) {
                BindListTapMotion(motion)
                LazyColumn(Modifier.width(411.dp).height(320.dp), state = state) {
                    val tap = ListTapLazyScope(this, motion, state, epoch = epoch)
                    with(tap) {
                        items(rows, key = { it }) { title ->
                            Box(Modifier.height(48.dp).fillMaxWidth().testTag("row-$title"))
                        }
                    }
                    tap.commit()
                }
            }
        }
        settle()

        // Pending tap awaits epoch 2 (e.g., Expand All on an already-expanded list).
        motion.arm(ListTapMotionKind.State, awaitPage = true, epoch = 2)
        // Stale epoch 1 hub page arrives and rebases armedBaseline.
        rows = listOf("Hub", "A", "B", "C", "D")
        epoch = 1
        Snapshot.sendApplyNotifications()
        repeat(2) { rule.mainClock.advanceTimeByFrame() }
        motion.cancelShiftsAndResetBaseline()

        // Tap's epoch 2 lands with no diff relative to the rebased epoch 1 list -> must disarm promptly.
        epoch = 2
        Snapshot.sendApplyNotifications()
        motion.land(2)
        repeat(4) { rule.mainClock.advanceTimeByFrame() }
        assertNull("no-op tap after stale rebase stayed armed", motion.kind)
        assertFalse(motion.captured)

        // A subsequent unrelated hub update reusing epoch 2 must snap rather than animating.
        val beforeD = top("row-D")
        rows = listOf("Hub2", "Hub", "A", "B", "C", "D")
        Snapshot.sendApplyNotifications()
        rule.mainClock.advanceTimeByFrame()
        assertEquals(0, motion.shiftPx("D"))
        assertTrue("hub insert did not move D", top("row-D") > beforeD + 20f)
    }

    @Test fun prepareExitFadeDoesNotLetHubPushInPrepFrameStealLocalFold() {
        rule.mainClock.autoAdvance = false
        var rows by mutableStateOf(listOf("A", "B", "C"))
        val motion = ListTapMotion()
        rule.setContent {
            PersonaTheme(darkTheme = false, reduceMotion = false, style = AppStyle.Glass) {
                BindListTapMotion(motion)
                LazyColumn(Modifier.width(160.dp).height(320.dp).background(Color.White).testTag("shot")) {
                    val tap = ListTapLazyScope(this, motion)
                    with(tap) {
                        items(rows, key = { it }) { title ->
                            Box(
                                Modifier
                                    .height(48.dp)
                                    .fillMaxWidth()
                                    .background(if (title == "B") Color.Red else Color.Blue)
                                    .testTag("row-$title"),
                            )
                        }
                    }
                    tap.commit()
                }
            }
        }
        settle()

        // Prep frame attaches fadeOutSpec without arming placement.
        motion.prepareExitFade()
        Snapshot.sendApplyNotifications()
        rule.mainClock.advanceTimeByFrame()
        // A hub push arrives during the prep frame -> must NOT consume the tap or animate.
        rows = listOf("Hub", "A", "B", "C")
        Snapshot.sendApplyNotifications()
        rule.mainClock.advanceTimeByFrame()
        assertNull("prep frame should not arm placement kind", motion.kind)
        assertFalse("hub push in prep frame captured tap", motion.captured)
        assertEquals(0, motion.shiftPx("C"))
        // Draw the prep frame (mirroring Choreographer's per-frame draw) so B's fadeOut GraphicsLayer is populated.
        assertTrue("B should be visible before fold", redPixels() > 20)

        val beforeHub = top("row-Hub")
        val beforeC = top("row-C")
        // Next frame: local fold arms placement and removes B.
        motion.arm(ListTapMotionKind.State)
        rows = listOf("Hub", "A", "C")
        Snapshot.sendApplyNotifications()
        rule.mainClock.advanceTimeByFrame()
        val earlyFadeRed = redPixels()
        val traceC = (1..8).map {
            rule.mainClock.advanceTimeByFrame()
            top("row-C")
        }
        rule.mainClock.advanceTimeBy(500)
        val afterC = top("row-C")
        val lateFadeRed = redPixels()

        assertTrue("removed row B did not fade out ($earlyFadeRed -> $lateFadeRed)", earlyFadeRed > 10 && lateFadeRed < earlyFadeRed / 2)
        assertEquals("hub row moved during fold", beforeHub, top("row-Hub"), 1f)
        assertTrue(
            "row C did not spring after hub push during prep frame ($beforeC -> $afterC via $traceC)",
            abs(beforeC - afterC) > 8f && traceC.any { y -> (y - beforeC) * (y - afterC) < 0f && abs(y - beforeC) > 4f && abs(y - afterC) > 4f },
        )
    }

    @Test fun staleShiftSafetyNetClearsAfterDisarm() {
        rule.mainClock.autoAdvance = false
        var rows by mutableStateOf(listOf("A", "B"))
        val motion = ListTapMotion()
        val heldScope = kotlinx.coroutines.CoroutineScope(object : kotlinx.coroutines.CoroutineDispatcher() {
            override fun dispatch(context: kotlin.coroutines.CoroutineContext, block: Runnable) = Unit
        })
        rule.setContent {
            PersonaTheme(darkTheme = false, reduceMotion = false, style = AppStyle.Glass) {
                BindListTapMotion(motion, rows)
                Box(Modifier.size(10.dp))
            }
        }
        settle()
        // Detach hostScope temporarily so launchShift uses the stalled scope whose animateTo never completes.
        motion.hostScope = null
        motion.notePlaced("B", 120f, heldScope, motion.liveFade)
        motion.arm(ListTapMotionKind.State)
        rows = listOf("B", "A")
        Snapshot.sendApplyNotifications()
        rule.mainClock.advanceTimeByFrame()
        motion.hostScope = null
        motion.notePlaced("B", 0f, heldScope, motion.liveFade)
        assertEquals(120, motion.shiftPx("B"))

        rule.mainClock.advanceTimeBy(Tokens.Motion.State.duration.toLong() + MAX_SHIFT_DURATION_MS + 100L)
        assertEquals("post-disarm safety net failed to clear stalled shift", 0, motion.shiftPx("B"))
        assertFalse(motion.hasActiveShifts())
    }

    @Test fun pinningExpandedHierarchyAcrossViewportDisposeNeverLeavesRowsStuckOverlappingOnScroll() {
        rule.mainClock.autoAdvance = false
        val fillers = (1..14).map { "filler-$it" }
        val hierarchy = listOf("session:task", "children:task", "session:child-1", "session:child-2")
        var dark by mutableStateOf(false)
        var rows by mutableStateOf(fillers + hierarchy)
        val state = LazyListState(firstVisibleItemIndex = 11)
        val motion = ListTapMotion()
        var scrollTarget by mutableStateOf(-1)
        rule.setContent {
            PersonaTheme(darkTheme = dark, reduceMotion = false, style = AppStyle.Glass) {
                BindListTapMotion(motion, rows)
                LaunchedEffect(scrollTarget) { if (scrollTarget >= 0) state.scrollToItem(scrollTarget) }
                LazyColumn(
                    Modifier.width(411.dp).height(320.dp).background(PersonaTheme.palette.background)
                        .nestedScroll(listTapScroll(motion)).testTag("shot"),
                    state = state,
                ) {
                    with(ListTapLazyScope(this, motion, state)) {
                        items(rows, key = { it }) { key ->
                            JRow(key, Modifier.height(52.dp).testTag("row-$key"))
                        }
                    }
                }
            }
        }
        settle()
        val out = File("build/pin-overlap-shots").also { it.mkdirs() }

        // 1. Cross-list Pin jump where hierarchy leaves viewport and scrolls back to Pinned
        motion.arm(ListTapMotionKind.State)
        rows = listOf("group:pinned") + hierarchy + fillers.drop(1)
        Snapshot.sendApplyNotifications()
        rule.mainClock.advanceTimeByFrame()
        scrollTarget = 0
        Snapshot.sendApplyNotifications()
        rule.mainClock.advanceTimeBy(800)
        settle()

        // 2. Direct cancelled-item-scope race (e.g. dispose + recompose in LazyColumn before spring dispatch)
        // both with hostScope bound and with no hostScope bound:
        val deadScope = kotlinx.coroutines.CoroutineScope(kotlinx.coroutines.SupervisorJob().apply { cancel() })
        motion.noteComposed(listOf("group:pinned") + hierarchy + fillers.drop(1))
        motion.notePlaced("children:task", 420f, deadScope, motion.liveFade)
        motion.arm(ListTapMotionKind.State)
        motion.capture(listOf("group:pinned", "session:other") + hierarchy + fillers.drop(1))
        motion.notePlaced("children:task", 120f, deadScope, motion.liveFade)
        rule.mainClock.advanceTimeBy(800)
        settle()

        assertEquals("children:task shift froze with hostScope bound", 0, motion.shiftPx("children:task"))
        save(out, "pin-hierarchy-after-light.png")
        rule.runOnIdle { dark = true }
        Snapshot.sendApplyNotifications()
        settle()
        save(out, "pin-hierarchy-after-dark.png")

        val childBounds = rule.onNodeWithTag("row-children:task").getUnclippedBoundsInRoot()
        val nextBounds = rule.onNodeWithTag("row-session:child-1").getUnclippedBoundsInRoot()
        assertTrue(
            "expanded hierarchy row overlapped neighbor (${childBounds.top}..${childBounds.bottom} vs ${nextBounds.top}..${nextBounds.bottom})",
            childBounds.bottom <= nextBounds.top + 1.dp,
        )

        // 3. Standalone motion without hostScope: a cancelled item scope never leaves a frozen shift
        val standalone = ListTapMotion()
        standalone.noteComposed(listOf("a", "b"))
        standalone.notePlaced("b", 300f, deadScope, standalone.liveFade)
        standalone.arm(ListTapMotionKind.State)
        standalone.capture(listOf("b", "a"))
        standalone.notePlaced("b", 40f, deadScope, standalone.liveFade)
        assertEquals("cancelled item scope must never leave a frozen shift", 0, standalone.shiftPx("b"))
    }

    @Test fun aRowComposedDuringATapSnapsWhenAHubRemovesIt() {
        rule.mainClock.autoAdvance = false
        var rows by mutableStateOf(listOf("A"))
        val motion = ListTapMotion()
        rule.setContent {
            PersonaTheme(darkTheme = false, reduceMotion = false, style = AppStyle.Glass) {
                BindListTapMotion(motion, rows)
                LazyColumn(Modifier.width(120.dp).height(160.dp).background(Color.White).testTag("shot")) {
                    with(ListTapLazyScope(this, motion)) {
                        items(rows, key = { it }) { title ->
                            Box(Modifier.height(40.dp).fillMaxWidth().background(if (title == "H") Color.Red else Color.Blue))
                        }
                    }
                }
            }
        }
        settle()
        motion.arm(ListTapMotionKind.State)
        rows = listOf("A", "H")
        Snapshot.sendApplyNotifications()
        rule.mainClock.advanceTimeBy(500)
        assertTrue("the row never appeared", redPixels() > 20)
        assertNull(motion.kind)
        rows = listOf("A")
        Snapshot.sendApplyNotifications()
        repeat(2) { rule.mainClock.advanceTimeByFrame() }
        assertEquals("a hub removal faded", 0, redPixels())
    }

    private fun redPixels(): Int {
        val image = rule.onNodeWithTag("shot").captureToImage().asAndroidBitmap()
        var reds = 0
        for (y in 0 until image.height step 2) for (x in 0 until image.width step 4) {
            val pixel = image.getPixel(x, y)
            if (android.graphics.Color.red(pixel) > 200 && android.graphics.Color.blue(pixel) < 80) reds++
        }
        return reds
    }

    /** The row's place after an insert, on the frame it appears and again later. A running move disagrees. */
    private fun assertSnaps(tag: String, change: () -> Unit) {
        val before = top(tag)
        change()
        Snapshot.sendApplyNotifications()
        rule.mainClock.advanceTimeByFrame()
        rule.mainClock.advanceTimeByFrame()
        val first = top(tag)
        rule.mainClock.advanceTimeBy(120)
        val later = top(tag)
        assertEquals(later, first, 0.5f)
        assertTrue("$tag did not move ($before -> $first)", abs(first - before) > 8f)
    }

    /** Top of the reddest row, in the shot's pixels. Bounds jump during a reorder; the pixels do not. */
    private fun redRow(): Int {
        val image = rule.onNodeWithTag("shot").captureToImage().asAndroidBitmap()
        var bestY = -1
        var best = 0
        val x = image.width / 2
        for (y in 0 until image.height) {
            val pixel = image.getPixel(x, y)
            val red = android.graphics.Color.red(pixel)
            val other = android.graphics.Color.green(pixel) + android.graphics.Color.blue(pixel)
            if (red > 180 && red > other && red > best) {
                best = red
                bestY = y
            }
        }
        return bestY
    }

    /** After the change has been placed, then 120 ms and 400 ms later. A running move disagrees with itself. */
    private fun samples(tag: String): List<Float> {
        Snapshot.sendApplyNotifications()
        repeat(2) { rule.mainClock.advanceTimeByFrame() }
        val first = top(tag)
        rule.mainClock.advanceTimeBy(120)
        val mid = top(tag)
        rule.mainClock.advanceTimeBy(400)
        return listOf(first, mid, top(tag))
    }

    private fun assertSpring(expected: IosSpring, actual: FiniteAnimationSpec<*>?) {
        val spring = actual as SpringSpec<*>
        assertEquals(expected.dampingRatio, spring.dampingRatio, 0f)
        assertEquals(expected.stiffness, spring.stiffness, 0.05f)
    }

    private fun settle() { repeat(6) { rule.mainClock.advanceTimeByFrame() } }
    private fun top(tag: String) = rule.onNodeWithTag(tag).getUnclippedBoundsInRoot().top.value
    private fun save(out: File, name: String) {
        val bitmap = rule.onNodeWithTag("shot").captureToImage().asAndroidBitmap()
        File(out, name).outputStream().use { bitmap.compress(Bitmap.CompressFormat.PNG, 100, it) }
    }
}

private class Drive {
    var dark by mutableStateOf(false)
    var width by mutableStateOf(411.dp)
    var rows by mutableStateOf(listOf("Project", "Review", "Notes", "Next"))
    var motion by mutableStateOf(ListTapMotion())
}

@androidx.compose.runtime.Composable
private fun TapRows(motion: ListTapMotion, rows: List<String>, width: androidx.compose.ui.unit.Dp) {
    BindListTapMotion(motion, rows)
    LazyColumn(Modifier.width(width).height(480.dp).background(PersonaTheme.palette.background).testTag("shot")) {
        with(ListTapLazyScope(this, motion)) {
            items(rows, key = { it }) { title -> JRow(title, Modifier.testTag("row-$title")) }
        }
    }
}
