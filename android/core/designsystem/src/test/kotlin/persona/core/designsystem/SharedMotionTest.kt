package persona.core.designsystem

import androidx.compose.animation.core.CubicBezierEasing
import androidx.compose.animation.core.EaseIn
import androidx.compose.animation.core.EaseOut
import androidx.compose.animation.core.SpringSpec
import androidx.compose.animation.core.TweenSpec
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test
import kotlin.math.sqrt

/**
 * Shared motions from `spec/tokens/motion.json` as Compose specs (#9897), matching iOS `Theme.Motion`
 * (`apple/Shared/Design/Theme.swift`).
 */
class SharedMotionTest {
    @Test fun expandIsTheIosSmoothSpring() {
        assertEquals(400, Tokens.Motion.Expand.duration)
        assertEquals(0f, Tokens.Motion.Expand.bounce)
        val spring = SharedMotion.expand<Float>() as SpringSpec
        assertEquals(IosSpring(400, 0f).dampingRatio, spring.dampingRatio, 1e-6f)
        assertEquals(IosSpring(400, 0f).stiffness, spring.stiffness, 0.01f)
    }

    @Test fun revealAndConcealAreTheIosEasedTweens() {
        val reveal = SharedMotion.reveal<Float>() as TweenSpec
        assertEquals(Tokens.Motion.Reveal.duration, reveal.durationMillis)
        assertEquals("easeOut", Tokens.Motion.Reveal.curve)
        val conceal = SharedMotion.conceal<Float>() as TweenSpec
        assertEquals(Tokens.Motion.Conceal.duration, conceal.durationMillis)
        assertEquals("easeIn", Tokens.Motion.Conceal.curve)
        // SwiftUI Animation.easeOut / easeIn match the CSS cubics Compose exposes as EaseOut / EaseIn.
        val iosEaseOut = CubicBezierEasing(0f, 0f, 0.58f, 1f)
        val iosEaseIn = CubicBezierEasing(0.42f, 0f, 1f, 1f)
        assertEquals(EaseOut, reveal.easing)
        assertEquals(EaseIn, conceal.easing)
        for (t in listOf(0f, 0.25f, 0.5f, 0.75f, 1f)) {
            assertEquals(iosEaseOut.transform(t), reveal.easing.transform(t), 1e-5f)
            assertEquals(iosEaseIn.transform(t), conceal.easing.transform(t), 1e-5f)
        }
    }

    @Test fun drawerAndSpringBackMatchTokens() {
        assertEquals(320, Tokens.Motion.Drawer.duration)
        assertEquals(0f, Tokens.Motion.Drawer.bounce)
        assertEquals(300, Tokens.Motion.SpringBack.duration)
        assertEquals(0.25f, Tokens.Motion.SpringBack.bounce)
        val drawer = SharedMotion.drawer<Float>() as SpringSpec
        val springBack = SharedMotion.springBack<Float>() as SpringSpec
        assertEquals(IosSpring(320, 0f).dampingRatio, drawer.dampingRatio, 1e-6f)
        assertEquals(IosSpring(320, 0f).stiffness, drawer.stiffness, 0.01f)
        assertEquals(IosSpring(300, 0.25f).dampingRatio, springBack.dampingRatio, 1e-6f)
        assertEquals(IosSpring(300, 0.25f).stiffness, springBack.stiffness, 0.01f)
    }

    @Test fun keyboardIsTheIosMassSpring() {
        assertEquals(555.027f, Tokens.Motion.Keyboard.stiffness)
        assertEquals(47.118f, Tokens.Motion.Keyboard.damping)
        val spring = IosMassSpring(
            stiffness = Tokens.Motion.Keyboard.stiffness,
            damping = Tokens.Motion.Keyboard.damping,
        )
        // mass 1: damping / (2√stiffness) ≈ 1 (critically damped); Compose stiffness equals iOS stiffness.
        assertEquals(1f, spring.dampingRatio, 0.001f)
        assertEquals(Tokens.Motion.Keyboard.stiffness, spring.composeStiffness, 1e-3f)
        val spec = SharedMotion.keyboard<Float>() as SpringSpec
        assertEquals(spring.dampingRatio, spec.dampingRatio, 1e-6f)
        assertEquals(spring.composeStiffness, spec.stiffness, 1e-3f)
    }

    @Test fun iosMassSpringDividesStiffnessByMass() {
        val mass = 2f
        val stiffness = 400f
        val damping = 40f
        val spring = IosMassSpring(mass = mass, stiffness = stiffness, damping = damping)
        assertEquals((damping / (2f * sqrt(stiffness * mass))), spring.dampingRatio, 1e-5f)
        assertEquals(stiffness / mass, spring.composeStiffness, 1e-5f)
        val spec = spring.spec<Float>() as SpringSpec
        assertEquals(spring.dampingRatio, spec.dampingRatio, 1e-6f)
        assertEquals(spring.composeStiffness, spec.stiffness, 1e-5f)
        assertTrue(spec.stiffness < stiffness)
    }
}
