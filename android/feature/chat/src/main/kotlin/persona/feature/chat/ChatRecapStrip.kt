package persona.feature.chat

import android.content.Context
import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.IntrinsicSize
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxHeight
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.compositionLocalOf
import androidx.compose.runtime.getValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.platform.LocalResources
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.semantics.Role
import androidx.compose.ui.semantics.clearAndSetSemantics
import androidx.compose.ui.semantics.contentDescription
import androidx.compose.ui.semantics.role
import androidx.compose.ui.semantics.onClick
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextOverflow
import persona.core.contracts.Item
import persona.core.contracts.SessionKey
import persona.core.contracts.parseTime
import persona.core.designsystem.JIcon
import persona.core.designsystem.PersonaTheme
import persona.core.designsystem.Tokens
import persona.core.designsystem.icons.PersonaIcons
import persona.core.domain.lines.ChatLine
import persona.core.domain.plainMarkdown

/**
 * The newest "while you were away" recap open as an Agent chat's last line (iOS `pendingRecap`, `ChatLines.openRecap`):
 * the recap's id while the chat's newest line is that recap, alone or the last of a folded run, and it wasn't closed
 * ([closed], the last recap closed in this chat). [lines] are newest first.
 */
internal fun openRecapId(lines: List<ChatLine>, closed: String?): String? {
    val newest = when (val line = lines.firstOrNull { it !is ChatLine.ModeChange }) {
        is ChatLine.Recaps -> line.items.lastOrNull()?.item
        is ChatLine.Row -> line.row.item?.item?.takeIf { line.row.type == "recap" }
        else -> null
    } ?: return null
    return newest.id.takeIf { it != closed }
}

/** The recap [RecapLine] draws open, and how to close it: a tap on it, or typing in the composer. */
internal class OpenRecap(val id: String, val close: () -> Unit)

/** Set by the chat page while its newest recap is open (Glass); [RecapLine] draws that one as [RecapStrip]. */
internal val LocalOpenRecap = compositionLocalOf<OpenRecap?> { null }

/** How many chats' closed recaps are kept (iOS keeps 200 once it passes 300). */
private const val CLOSED_RECAPS_KEPT = 200
private const val CLOSED_RECAPS_MAX = 300

/**
 * Which recap was closed in each chat, on this device (iOS `closedRecaps`): a recap closed once stays a one-line marker,
 * also after the app restarts; a newer one opens again.
 */
internal class ClosedRecaps(context: Context) {
    private val prefs = context.applicationContext.getSharedPreferences("closed_recaps", Context.MODE_PRIVATE)

    /** The recap last closed in [session]'s chat. */
    fun closed(session: SessionKey): String? = prefs.getString(session.path, null)?.substringAfter(' ')

    fun close(session: SessionKey, recap: String, now: Long = System.currentTimeMillis()) {
        val stored = prefs.all.mapNotNull { (key, value) -> (value as? String)?.let { key to it } }.toMap() + (session.path to "$now $recap")
        val kept = keptClosedRecaps(stored)
        prefs.edit().apply { if (kept.size < stored.size) clear(); kept.forEach { (key, value) -> putString(key, value) } }.apply()
    }
}

/** Past [CLOSED_RECAPS_MAX] chats, the [CLOSED_RECAPS_KEPT] closed most recently; each value is "<millis> <recap id>". */
internal fun keptClosedRecaps(stored: Map<String, String>): Map<String, String> =
    if (stored.size <= CLOSED_RECAPS_MAX) stored
    else stored.entries.sortedByDescending { it.value.substringBefore(' ').toLongOrNull() ?: 0L }
        .take(CLOSED_RECAPS_KEPT).associate { it.key to it.value }

/** Sentence ends, ASCII and full-width: a recap that ends on one gets no second from [recapSpoken]'s template. */
private val SentenceEnds = setOf('.', '!', '?', '。', '！', '？', '．', '｡')

/**
 * What the open recap says to TalkBack: "Recap while you were away: <the recap>. Tap to close". The template's own full stop
 * after the recap goes when the recap already ends a sentence, so it never reads "。。" or "..".
 */
internal fun recapSpoken(res: android.content.res.Resources, text: String): String {
    val words = text.trimEnd()
    val template = res.getString(R.string.chat_recap_open_spoken, RECAP_MARK)
    val at = template.indexOf(RECAP_MARK)
    if (at < 0) return res.getString(R.string.chat_recap_open_spoken, words)
    var after = template.substring(at + RECAP_MARK.length)
    if (words.lastOrNull() in SentenceEnds && after.firstOrNull() in SentenceEnds) after = after.drop(1)
    return template.substring(0, at) + words + after
}

/** Stands in for the recap while [recapSpoken] finds where the template puts it (a private-use character). */
private const val RECAP_MARK = "\uE000"

/**
 * The newest recap in full as the chat's last line when you come back to it (iOS `RecapStrip`): the accent bar, "While
 * you were away · 21 min ago" with a close mark, and the recap as plain words, up to eight lines. A tap closes it to the
 * one-line marker ([RecapLine]); the marker opens the whole recap.
 */
@Composable
internal fun RecapStrip(item: Item, onClose: () -> Unit) {
    val palette = PersonaTheme.palette
    val type = PersonaTheme.type
    val resources = LocalResources.current
    val now by rememberNow(60_000)
    val text = plainMarkdown(item.text.orEmpty())
    val spoken = recapSpoken(resources, text)
    val shape = RoundedCornerShape(Tokens.Radius.card)
    Row(
        Modifier.fillMaxWidth().height(IntrinsicSize.Min).clip(shape).background(palette.accentSoft)
            .clickable(role = Role.Button, onClick = onClose)
            .clearAndSetSemantics { contentDescription = spoken; role = Role.Button; onClick { onClose(); true } }
            .padding(Tokens.Space.m),
        horizontalArrangement = Arrangement.spacedBy(Tokens.Space.m),
    ) {
        Box(Modifier.width(Tokens.Size.bar).fillMaxHeight().clip(CircleShape).background(palette.accent))
        Column(Modifier.weight(1f), verticalArrangement = Arrangement.spacedBy(Tokens.Space.xs)) {
            Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(Tokens.Space.sm)) {
                JIcon(RecapIcon, null, size = Tokens.Space.l, tint = palette.accent, decorative = true)
                Row(Modifier.weight(1f), horizontalArrangement = Arrangement.spacedBy(Tokens.Space.sm)) {
                    // The title keeps its words; the time gives way when the line is short of room.
                    Text(stringResource(R.string.chat_recap_title), style = type.caption.copy(fontWeight = FontWeight.SemiBold),
                        color = palette.ink, maxLines = 1, overflow = TextOverflow.Ellipsis)
                    parseTime(item.ts)?.let {
                        Text("· " + relativeTime(it, now, resources), style = type.caption, color = palette.ink2, maxLines = 1,
                            overflow = TextOverflow.Ellipsis, modifier = Modifier.weight(1f, fill = false))
                    }
                }
                JIcon(PersonaIcons.close, null, size = Tokens.Space.m, tint = palette.ink3, decorative = true)
            }
            Text(text, style = type.callout, color = palette.ink, maxLines = 8, overflow = TextOverflow.Ellipsis)
        }
    }
}
