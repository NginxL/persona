package persona.core.designsystem

import androidx.compose.animation.core.LinearEasing
import org.junit.Assert.assertEquals
import org.junit.Test

/** Curve, travel and dim from the iPhone simulator sample. Not a Theme.Motion token. */
class PageTransitionMathTest {
    @Test fun behindTravelRoundsThreeTenthsOfTheWidth() {
        assertEquals(121, PageTransitionTokens.behindTravel(402f))
        assertEquals(310, PageTransitionTokens.behindTravel(1032f))
        assertEquals(140, PageTransitionTokens.behindTravel(466f))
        assertEquals(120, PageTransitionTokens.behindTravel(400f))
        assertEquals(0, PageTransitionTokens.behindTravel(0f))
    }

    @Test fun dimIsBlackStrengthTimesCoverage() {
        assertEquals(0f, PageTransitionTokens.dimAlpha(0f), 0f)
        assertEquals(0.05f, PageTransitionTokens.dimAlpha(0.5f), 0.0001f)
        assertEquals(0.09226f, PageTransitionTokens.dimAlpha(0.9226f), 0.0001f)
        assertEquals(0.10f, PageTransitionTokens.dimAlpha(1f), 0.0001f)
        assertEquals(0f, PageTransitionTokens.dimAlpha(-1f), 0f)
        assertEquals(0.10f, PageTransitionTokens.dimAlpha(2f), 0.0001f)
    }

    @Test fun curveMatchesTheSampledBezier() {
        val easing = PageTransitionTokens.glassEasing
        assertEquals(0f, easing.transform(0f), 0f)
        assertEquals(1f, easing.transform(1f), 0f)
        // Newton fit of cubic-bezier(0.28, 1.02, 0.48, 0.96). FastOutSlowIn at u=0.5 is about 0.8, not this.
        assertEquals(0.6712f, easing.transform(87f / 350f), 0.02f)
        assertEquals(0.9226f, easing.transform(175f / 350f), 0.02f)
        assertEquals(0.9813f, easing.transform(262f / 350f), 0.02f)
        val y = easing.transform(175f / 350f)
        assertEquals(31.10f, 402f * (1f - y), 1.5f)
        // A quarter of a predictive drag must not take the cubic: easing(0.25) is about 0.67, linear stays 0.25.
        assertEquals(0.25f, LinearEasing.transform(0.25f), 0f)
        assertEquals(0.6712f, easing.transform(0.25f), 0.02f)
    }

    @Test fun shadowEdgeSitsOnTheFrontCardsLeadingEdge() {
        val coverage = 0.922643f
        val travel = 120
        assertEquals(400f - coverage * (400f - travel), glassShadowEdge(400f, coverage, travel, trailing = 1), 0.05f)
        assertEquals(coverage * (400f - travel), glassShadowEdge(400f, coverage, travel, trailing = -1), 0.05f)
    }

    @Test fun pressScaleIsGrowthOverWidthAboutTheCentre() {
        assertEquals(1f + 16f / 61f, glassPressTarget(true, true, false, 61f, 16f), 0.0001f)
        assertEquals(1f + 16f / 160f, glassPressTarget(true, true, false, 160f, 16f), 0.0001f)
        assertEquals(1f + 16f / 300f, glassPressTarget(true, true, false, 300f, 16f), 0.0001f)
        assertEquals(1f, glassPressTarget(false, true, false, 160f, 16f), 0f)
        assertEquals(1f, glassPressTarget(true, false, false, 160f, 16f), 0f)
        assertEquals(1f, glassPressTarget(true, true, true, 160f, 16f), 0f)
        assertEquals(1f, glassPressTarget(true, true, false, 0f, 16f), 0f)
    }
}
