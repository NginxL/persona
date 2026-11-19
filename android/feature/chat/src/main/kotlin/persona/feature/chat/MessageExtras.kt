package persona.feature.chat

import android.content.res.Resources
import androidx.compose.runtime.Composable
import androidx.compose.runtime.State
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.produceState
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import androidx.compose.ui.platform.LocalResources
import androidx.compose.ui.res.stringResource
import persona.core.contracts.Item
import persona.core.contracts.Session
import androidx.compose.foundation.layout.Box
import androidx.compose.ui.Modifier
import androidx.compose.ui.semantics.CustomAccessibilityAction
import androidx.compose.ui.semantics.customActions
import androidx.compose.ui.semantics.semantics
import persona.core.designsystem.JAction
import persona.core.designsystem.JChip
import persona.core.designsystem.JMenu
import persona.core.designsystem.JTextButton
import persona.core.designsystem.LocalizedFormats
import persona.core.designsystem.icons.PhIcons
import java.time.Duration
import java.time.Instant
import java.time.ZoneId
import java.util.Locale
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.delay
import kotlinx.coroutines.launch

/** The words the hub adds where it cut a long text for the page (a Chinese "…(truncated, N bytes in all)" suffix). */
private const val CUT_MARK = "\n…（已截断" // i18n-ignore: the hub's cut mark, matched

/** [text] without the hub's cut mark: the part a page carries of a text with `full_size`. */
internal fun cutText(text: String): String {
    val at = text.lastIndexOf(CUT_MARK)
    return if (at >= 0) text.substring(0, at) else text
}

/**
 * "12 KB", "3.4 MB": decimal units, like iOS `ByteCountFormatter`. Rounded before the unit is picked, as the machine pages'
 * `MetricFormat.bytes`: kilobytes to the nearest, and a size that rounds up to the next unit is shown in it (999,999 bytes
 * is 1.0 MB, not 1000 KB; 999.95 MB is 1.0 GB, not 1000.0 MB) (#9890).
 */
internal fun formatBytes(bytes: Long, resources: Resources): String {
    val kb = (bytes + 500) / 1_000
    val mbTenths = (bytes + 50_000) / 100_000
    return when {
        bytes < 1_000 -> resources.getQuantityString(R.plurals.chat_bytes, bytes.toInt(), bytes.toInt())
        kb < 1_000 -> "$kb KB"
        mbTenths < 10_000 -> String.format(Locale.ROOT, "%.1f MB", mbTenths / 10.0)
        else -> String.format(Locale.ROOT, "%.1f GB", (bytes + 50_000_000) / 100_000_000 / 10.0)
    }
}

/**
 * "Just now", "5 min ago", "3 hr ago", "Yesterday 17:05", "Oct 3", in the language of [resources]. Yesterday is the day
 * before [now]'s in [zone], as on iOS and the web, not any time 24 to 48 hours back (#9878).
 */
internal fun relativeTime(time: Instant, now: Instant, resources: Resources, zone: ZoneId = ZoneId.systemDefault()): String {
    val seconds = Duration.between(time, now).seconds.coerceAtLeast(0)
    val locale = resources.configuration.locales[0]
    val yesterday = time.atZone(zone).toLocalDate() == now.atZone(zone).toLocalDate().minusDays(1)
    return when {
        seconds < 60 -> resources.getString(R.string.chat_time_just_now)
        seconds < 3600 -> (seconds / 60).toInt().let { resources.getQuantityString(R.plurals.chat_time_minutes_ago, it, it) }
        seconds < 86_400 -> (seconds / 3600).toInt().let { resources.getQuantityString(R.plurals.chat_time_hours_ago, it, it) }
        yesterday -> resources.getString(R.string.chat_time_yesterday, LocalizedFormats.format(time, LocalizedFormats.TIME, locale, zone))
        else -> LocalizedFormats.format(time, LocalizedFormats.MONTH_DAY, locale, zone)
    }
}

/** The current time, ticking every [periodMillis] while composed. */
@Composable
internal fun rememberNow(periodMillis: Long, enabled: Boolean = true): State<Instant> = produceState(Instant.now(), periodMillis, enabled) {
    while (enabled) {
        delay(periodMillis)
        value = Instant.now()
    }
}

/**
 * "Show full text (12 KB)" under a text the hub cut (`full_size`): loads the whole text and hands it to [onLoaded], which
 * shows it in place of the cut one; "Loading…" meanwhile, "Couldn't load, tap to retry" when it fails.
 */
@Composable
internal fun FullTextButton(item: Item, onLoaded: (String) -> Unit) {
    val actions = LocalChatRowActions.current ?: return
    val size = item.fullSize ?: return
    val scope = rememberCoroutineScope()
    var loading by remember(item.id) { mutableStateOf(false) }
    var failed by remember(item.id) { mutableStateOf(false) }
    val resources = LocalResources.current
    JTextButton(
        when {
            loading -> stringResource(R.string.chat_full_text_loading)
            failed -> stringResource(R.string.chat_full_text_failed)
            else -> stringResource(R.string.chat_full_text_open, formatBytes(size.toLong(), resources))
        },
        {
            loading = true
            failed = false
            scope.launch {
                try {
                    onLoaded(actions.fullText(item))
                } catch (e: CancellationException) {
                    throw e
                } catch (_: Exception) {
                    failed = true
                } finally {
                    loading = false
                }
            }
        },
        enabled = !loading, icon = if (failed) PhIcons.ArrowClockwise else PhIcons.ArrowSquareOut,
    )
}

/**
 * The next message Claude suggests (X1, grey in its own input box): a chip over the empty composer of an idle session;
 * a tap puts it in the composer to edit or send. A safe suggestion with [onSend] also offers "Send now" in a long-press
 * menu and as an accessibility action; a risky one only fills.
 */
@Composable
internal fun ComposerSuggestion(session: Session?, composer: ChatComposerState, onSend: ((String) -> Unit)? = null) {
    val suggestion = suggestionFor(session, composer.hasContent) ?: return
    val directSend = suggestionDirectSend(session, onSend)
    var menu by remember(suggestion) { mutableStateOf(false) }
    val fill: () -> Unit = { composer.editMessage(suggestion); composer.focusRequest++ }
    val sendLabel = stringResource(R.string.chat_suggestion_send)
    val fillLabel = stringResource(R.string.chat_suggestion_fill)
    Box {
        JChip(suggestion, Modifier.then(if (directSend == null) Modifier else Modifier.semantics {
            customActions = listOf(CustomAccessibilityAction(sendLabel) { directSend(suggestion); true })
        }), onClick = fill, icon = PhIcons.Sparkle, onLongClick = directSend?.let { { menu = true } })
        if (directSend != null) JMenu(menu, { menu = false }, listOf(
            JAction(sendLabel, PhIcons.PaperPlaneRight) { directSend(suggestion) },
            JAction(fillLabel, PhIcons.PencilSimple, onClick = fill),
        ))
    }
}

/** Only a safe, present suggestion can have a direct sender; filling the composer is always still available. */
internal fun suggestionDirectSend(session: Session?, onSend: ((String) -> Unit)?): ((String) -> Unit)? =
    if (session == null || session.suggestionRisky) null else onSend

/** The suggestion to offer: the session's, while it is idle and nothing is typed yet. */
internal fun suggestionFor(session: Session?, typed: Boolean): String? =
    session?.suggestion?.trim()?.takeIf { it.isNotEmpty() && !typed && session?.status != "working" }
