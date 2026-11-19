package persona.core.designsystem

import androidx.compose.animation.AnimatedContent
import androidx.compose.animation.EnterTransition
import androidx.compose.animation.ExitTransition
import androidx.compose.animation.core.SpringSpec
import androidx.compose.animation.core.TweenSpec
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.runtime.snapshots.Snapshot
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalLayoutDirection
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.test.getUnclippedBoundsInRoot
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.unit.LayoutDirection
import androidx.test.ext.junit.runners.AndroidJUnit4
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNotSame
import org.junit.Assert.assertSame
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config

@RunWith(AndroidJUnit4::class)
@Config(sdk = [35], qualifiers = "en-w400dp-h800dp-mdpi")
class PageTransitionsTest {
    @get:Rule val rule = createComposeRule()

    @Test fun pageTimingAndPredictivePopShareTheExistingEasedTransform() {
        assertEquals(300, PageTransitionTokens.pushDuration)
        assertEquals(260, PageTransitionTokens.popDuration)
        assertEquals(350, PageTransitionTokens.glassDurationMs)
        val glass = PersonaPageTransitions(false, LayoutDirection.Ltr, AppStyle.Glass)
        assertEquals(350, glass.pushMillis)
        assertEquals(350, glass.popMillis)
        // A tapped Back keeps the cubic. The gesture seeks a different, linear spec with the same travel.
        assertNotSame(glass.pop, glass.predictivePop)
        assertEquals(-1f, glass.pop.targetContentZIndex)
        assertEquals(-1f, glass.predictivePop.targetContentZIndex)
        for (style in listOf(AppStyle.Brief, AppStyle.Board)) {
            val other = PersonaPageTransitions(false, LayoutDirection.Ltr, style)
            assertEquals(300, other.pushMillis)
            assertEquals(260, other.popMillis)
            assertSame(other.pop, other.predictivePop)
        }
        for (style in AppStyle.entries) {
            for (direction in LayoutDirection.entries) {
                val reduced = PersonaPageTransitions(true, direction, style)
                assertEquals(0, reduced.pushMillis)
                assertEquals(0, reduced.popMillis)
                for (transform in listOf(reduced.push, reduced.pop, reduced.predictivePop)) {
                    assertEquals(EnterTransition.None, transform.targetContentEnter)
                    assertEquals(ExitTransition.None, transform.initialContentExit)
                    assertEquals(null, transform.sizeTransform)
                }
            }
        }
    }

    @Test fun columnsUseTheIosLayoutSpringAndKeepOtherStylesTweens() {
        val glass = sidebarColumnSpec<Float>(AppStyle.Glass, false) as SpringSpec
        assertEquals(1f, glass.dampingRatio, 0f)
        assertEquals(IosSpring(Tokens.Motion.Layout.duration, Tokens.Motion.Layout.bounce).stiffness, glass.stiffness, 0f)
        for ((style, duration) in listOf(AppStyle.Brief to 240, AppStyle.Board to 160)) {
            assertEquals(duration, (sidebarColumnSpec<Float>(style, false) as TweenSpec).durationMillis)
        }
        for (style in AppStyle.entries) {
            assertEquals(0, (sidebarColumnSpec<Float>(style, true) as TweenSpec).durationMillis)
        }
    }

    @Test fun briefPagesPushAndPopWithMirroredQuarterParallax() {
        assertMirrored(AppStyle.Brief, travel = 100f)
    }

    @Test fun boardPagesPushAndPopWithMirroredQuarterParallax() {
        assertMirrored(AppStyle.Board, travel = 100f)
    }

    @Test fun glassPushAndPopUseTheMeasuredTravel() {
        assertMirrored(AppStyle.Glass, travel = 120f)
    }

    private fun assertMirrored(style: AppStyle, travel: Float) {
        var page by mutableStateOf(0)
        var direction by mutableStateOf(LayoutDirection.Ltr)
        rule.mainClock.autoAdvance = false
        rule.setContent {
            PersonaTheme(reduceMotion = false, style = style) {
                CompositionLocalProvider(LocalLayoutDirection provides direction) {
                    val transitions = rememberPageTransitions()
                    AnimatedContent(page, Modifier.fillMaxSize(), transitionSpec = {
                        if (targetState > initialState) transitions.push else transitions.pop
                    }) { key ->
                        Box(Modifier.fillMaxSize().background(PersonaTheme.palette.background).testTag("page-$key"))
                    }
                }
            }
        }
        rule.mainClock.advanceTimeByFrame()
        val limit = travel + 1f
        for (rtl in listOf(false, true)) {
            rule.runOnIdle { direction = if (rtl) LayoutDirection.Rtl else LayoutDirection.Ltr }
            Snapshot.sendApplyNotifications()
            rule.mainClock.advanceTimeByFrame()
            rule.runOnIdle { page = 1 }
            Snapshot.sendApplyNotifications()
            repeat(2) { rule.mainClock.advanceTimeByFrame() }
            rule.mainClock.advanceTimeBy(80)
            val sign = if (rtl) -1 else 1
            val incoming = rule.onNodeWithTag("page-1").getUnclippedBoundsInRoot().left.value * sign
            val outgoing = rule.onNodeWithTag("page-0").getUnclippedBoundsInRoot().left.value * sign
            assertTrue("$style push: $incoming / $outgoing", incoming in 1f..399f && outgoing in -limit..-1f)
            assertEquals(400f + outgoing * (400f / travel), incoming, 4f)
            rule.mainClock.advanceTimeBy(400)
            rule.onNodeWithTag("page-0").assertDoesNotExist()
            assertEquals(0f, rule.onNodeWithTag("page-1").getUnclippedBoundsInRoot().left.value, 0f)

            rule.runOnIdle { page = 0 }
            Snapshot.sendApplyNotifications()
            repeat(2) { rule.mainClock.advanceTimeByFrame() }
            rule.mainClock.advanceTimeBy(80)
            val underneath = rule.onNodeWithTag("page-0").getUnclippedBoundsInRoot().left.value * sign
            val popped = rule.onNodeWithTag("page-1").getUnclippedBoundsInRoot().left.value * sign
            assertTrue("$style pop: $underneath / $popped", underneath in -limit..-1f && popped in 1f..399f)
            assertEquals(400f + underneath * (400f / travel), popped, 4f)
            rule.mainClock.advanceTimeBy(400)
            rule.onNodeWithTag("page-1").assertDoesNotExist()
        }
    }
}
