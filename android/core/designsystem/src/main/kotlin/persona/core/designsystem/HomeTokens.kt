package persona.core.designsystem

import androidx.compose.ui.graphics.Color
import androidx.compose.ui.unit.dp

/** Application chrome shared with the compact iPhone shell, independent of platform system bars. */
internal object HomeTokens {
    const val drawerFraction = 0.82f

    /** Flick faster than this, in dp/s, settles that way. The iPhone's 300 pt/s (`ShellPager.swift:193-196`). */
    const val drawerFlickDpPerSecond = 300f

    /** How far the half-way rule looks ahead, in seconds (`ShellPager.swift:195`). */
    const val drawerProjectionSeconds = 0.2f
    val drawerMaximum = 380.dp
    val drawerPageRadius = 55.dp
    val drawerShadow = 24.dp
    const val drawerShadowAlpha = 0.18f
    val drawerLight = Color(0xFFF9F9F9)
    val drawerDark = Color(0xFF171717)
    val drawerRowHeight = 52.dp
    val sidebarBadgeSize = 22.dp
    /** An assistant's monogram: in the sidebar's head, and in the switcher's rows. */
    val assistantAvatar = 40.dp
    val assistantAvatarSmall = 32.dp
    /** In the head of Settings' first group. */
    val assistantAvatarHeader = 24.dp
    const val assistantInitialShare = 0.42f
    const val assistantOfflineAlpha = 0.45f
    /** An assistant's presence dot at its monogram's corner, and the ring cut around it. */
    val assistantPresenceDot = 8.dp
    val assistantPresenceRing = 2.dp
    val chromeLight = Color(0xCCFFFFFF)
    val chromeDark = Color(0xB83A3A3C)
    val settingsGlyph = 54.dp
    val repositoryLight = Color(0xFFF5F5F5)
    val repositoryDark = Color(0xFF2A2A2A)
    const val filterSelectedAlpha = 0.15f
    const val filterStrokeAlpha = 0.35f
    val composerRadius = 32.dp
    val composerShadow = 12.dp
    val composerStroke = 0.5.dp
    const val composerShadowAlpha = 0.08f
    val composerLight = Color.White
    val composerDark = Color(0xFF1F1F21)
    val personaAnswerLight = Color(0xFFF0F0F3)
    val personaAnswerDark = Color(0xFF2A2A2E)
    val userMessageLight = Color(0xFFE3EDFD)
    val userMessageDark = Color(0xFF1C2F47)
    val composerStrokeLight = Color(0xFFE3E3E3)
    val composerStrokeDark = Color(0xFF3A3A3C)
    val homeFaceSize = 46.dp
    val headerHeight = 70.dp
    val segmentWidth = 72.dp
    val segmentHeight = 36.dp
    val segmentLight = Color(0xFFE6E6E6)
    val segmentDark = Color(0xFF3A3A3C)
    const val switcherPressLightIntensity = 0.5f
    const val switcherPressScale = 1.04f
    const val switcherPressedSelectionAlpha = 0.84f
    const val actionPressScale = 4f / 3f
    const val actionPressVeil = 0.36f
    const val shellTextStep = 19f / 17f
    const val shellScaleMaximum = 1.6f
}
