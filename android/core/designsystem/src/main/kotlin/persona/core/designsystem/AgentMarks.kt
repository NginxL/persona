package persona.core.designsystem

import androidx.annotation.DrawableRes
import androidx.compose.foundation.Image
import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.offset
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.Immutable
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.graphics.ColorFilter
import androidx.compose.ui.layout.ContentScale
import androidx.compose.ui.res.painterResource
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.semantics.clearAndSetSemantics
import androidx.compose.ui.semantics.contentDescription
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.unit.Dp
import androidx.compose.ui.unit.TextUnit
import androidx.compose.ui.unit.TextUnitType
import androidx.compose.ui.unit.dp
import persona.core.designsystem.icons.PhIcons

/**
 * Which agent runs in a session, as its mark draws it (#6390, the web's `AgentMark`, iOS `AgentKind`): Claude's
 * mark in its own coral, Codex's (OpenAI's) and TRAE's in the text colour, else the first letter of [name] on a tile;
 * and the client it runs in. Pure data, built by `core:domain` from the session.
 */
@Immutable
data class AgentMarkSpec(val kind: AgentMarkKind?, val name: String, val surface: AgentSurface?)

/** The agents whose own mark the design system draws. */
enum class AgentMarkKind(@DrawableRes internal val drawable: Int, internal val tinted: Boolean, internal val fit: Float) {
    Claude(R.drawable.brand_claude, tinted = false, fit = 1f),
    Codex(R.drawable.brand_openai, tinted = true, fit = 1f),
    Trae(R.drawable.brand_trae, tinted = true, fit = 0.92f),
    OpenCode(R.drawable.brand_opencode, tinted = true, fit = 0.88f),
    Pi(R.drawable.brand_pi, tinted = true, fit = 0.88f),
    Gemini(R.drawable.brand_gemini, tinted = true, fit = 1f),
    Cursor(R.drawable.brand_cursor, tinted = true, fit = 0.92f),
    Grok(R.drawable.brand_grok, tinted = true, fit = 1f),
    Antigravity(R.drawable.brand_agy, tinted = true, fit = 1f),
    Dsh(R.drawable.brand_dsh, tinted = true, fit = 1f),
    Kimi(R.drawable.brand_kimi, tinted = true, fit = 0.88f),
    Copilot(R.drawable.brand_copilot, tinted = true, fit = 1f),
}

/** The client an agent runs in: its desktop app, or its command line (the mark carries </>). */
enum class AgentSurface { Desktop, Cli }

/** The box an agent's mark is drawn in: beside a row's state mark, in a chat's title line. */
private val MarkBox = 14.dp
private const val BadgeShare = 0.62f
private const val BadgeOffsetX = 0.22f
private const val BadgeOffsetY = 0.18f
private val BadgeCorner = 3.dp
private const val TileShare = 0.92f
private const val LetterScale = 0.6f
private val Hairline = 1.dp
private const val BadgeInset = 0.14f

/**
 * The mark of the agent in a session, in a square [size] box so a column of rows lines up; drawn before the state
 * mark, never in its place. One run from its command line carries a </> badge on the lower right, ringed in the page's
 * colour so it reads on any row. Read out as "Claude Code, command line".
 */
@Composable
fun JAgentMark(spec: AgentMarkSpec, modifier: Modifier = Modifier, size: Dp = MarkBox) {
    val palette = PersonaTheme.palette
    val spoken = when (spec.surface) {
        AgentSurface.Cli -> stringResource(R.string.designsystem_agent_mark_cli, spec.name)
        AgentSurface.Desktop -> stringResource(R.string.designsystem_agent_mark_desktop, spec.name)
        null -> spec.name
    }
    Box(modifier.size(size).clearAndSetSemantics { contentDescription = spoken }, contentAlignment = Alignment.Center) {
        val kind = spec.kind
        if (kind != null) {
            Image(
                painterResource(kind.drawable), null, Modifier.size(size * kind.fit), contentScale = ContentScale.Fit,
                colorFilter = if (kind.tinted) ColorFilter.tint(palette.ink2) else null,
            )
        } else {
            val tile = size * TileShare
            Box(Modifier.size(tile).clip(RoundedCornerShape(BadgeCorner)).background(palette.surfaceStrong), contentAlignment = Alignment.Center) {
                Text(
                    spec.name.firstOrNull { it.isLetterOrDigit() }?.uppercase() ?: "?", color = palette.ink2, maxLines = 1,
                    style = PersonaTheme.type.label.copy(fontWeight = FontWeight.SemiBold,
                        fontSize = TextUnit(tile.value * LetterScale, TextUnitType.Sp), lineHeight = TextUnit(tile.value * LetterScale, TextUnitType.Sp)),
                )
            }
        }
        if (spec.surface == AgentSurface.Cli) {
            val badge = size * BadgeShare
            val shape = RoundedCornerShape(BadgeCorner)
            Box(
                Modifier.align(Alignment.BottomEnd).offset(size * BadgeOffsetX, size * BadgeOffsetY).size(badge).clip(shape)
                    .background(palette.ink).border(Hairline, palette.background, shape),
                contentAlignment = Alignment.Center,
            ) {
                JIcon(PhIcons.Code, null, size = badge, tint = palette.background, modifier = Modifier.padding(badge * BadgeInset))
            }
        }
    }
}
