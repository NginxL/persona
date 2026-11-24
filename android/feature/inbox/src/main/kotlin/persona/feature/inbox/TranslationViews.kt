package persona.feature.inbox

import androidx.compose.ui.res.stringResource

import androidx.compose.foundation.layout.*
import androidx.compose.material3.Text
import androidx.compose.runtime.*
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import kotlinx.coroutines.flow.distinctUntilChanged
import kotlinx.coroutines.flow.map
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import persona.core.contracts.api.ItemTranslation
import persona.core.contracts.api.TranslationPrefs
import persona.core.designsystem.*
import persona.core.designsystem.icons.PhIcons
import persona.core.inbox.*

/** Settings › Translation on a mail row: the hub's translation of its subject and preview, while mail is translated. */
@Composable
internal fun rememberItemTranslation(record: InboxRecord): ItemTranslation? {
    val translator = LocalTranslator.current ?: return null
    val prefs by translator.prefs.collectAsStateWithLifecycle()
    if (!prefs.translatesMail || record.mail == null) return null
    return record.translations[prefs.target]
}

/**
 * Above a mail's body while it is translated or being translated: what happened and the way back to the original (or
 * to the translation). An error says so in one line; the original stays.
 */
@Composable
internal fun MailTranslationBar(target: String, translating: Boolean, translated: Boolean, showingOriginal: Boolean,
    error: String?, onToggle: () -> Unit, modifier: Modifier = Modifier) {
    val palette = PersonaTheme.palette
    val language = TranslationPrefs.languageName(target)
    when {
        translating -> Row(modifier, verticalAlignment = Alignment.CenterVertically) {
            JSpinner(Modifier.size(Tokens.Space.Standard.m))
            Spacer(Modifier.width(Tokens.Space.s))
            Text(stringResource(R.string.inbox_translating, language), style = PersonaTheme.type.caption, color = palette.ink3, maxLines = 1)
        }
        translated -> Row(modifier, verticalAlignment = Alignment.CenterVertically) {
            JIcon(PhIcons.Translate, null, size = Tokens.Space.l, tint = palette.ink3, decorative = true)
            Spacer(Modifier.width(Tokens.Space.xs))
            Text(if (showingOriginal) stringResource(R.string.inbox_translation_original) else stringResource(R.string.inbox_translated_into, language), Modifier.weight(1f, fill = false),
                style = PersonaTheme.type.caption, color = palette.ink3, maxLines = 1)
            Spacer(Modifier.width(Tokens.Space.xs))
            JTextButton(stringResource(if (showingOriginal) R.string.inbox_show_translation else R.string.inbox_show_original), onToggle)
        }
        error != null -> Row(modifier, verticalAlignment = Alignment.CenterVertically) {
            JIcon(PhIcons.WarningCircle, null, size = Tokens.Space.l, tint = palette.ink3, decorative = true)
            Spacer(Modifier.width(Tokens.Space.xs))
            Text(error, style = PersonaTheme.type.caption, color = palette.ink3, maxLines = 2)
        }
    }
}

/**
 * Under a Feishu message from someone else, in a chat picked in Settings › Translation, when it is in another language: its
 * translation in a grey line. Asked for as the bubble shows (in batches); nothing shows until it comes back.
 */
@Composable
internal fun LarkTranslationLine(message: LarkMessage, feedId: String?) {
    val translator = LocalTranslator.current ?: return
    val prefs by translator.prefs.collectAsStateWithLifecycle()
    if (message.mine || message.system || !message.translatable || !prefs.translates(feedId)) return
    val text = remember(message) { message.parts.filterIsInstance<LarkPart.Text>().joinToString("\n") { it.text }.trim() }
    if (text.isEmpty() || !translator.isForeign(text, prefs.target)) return
    LaunchedEffect(text, prefs.target) { translator.request(text, prefs.target) }
    // Only this message's translation: a batch of others coming back does not redraw this bubble.
    val translation by remember(text, prefs.target) {
        translator.texts.map { it[translator.key(text, prefs.target)] }.distinctUntilChanged()
    }.collectAsStateWithLifecycle(translator.translation(text, prefs.target))
    val shown = translation ?: return
    Row(verticalAlignment = Alignment.Top) {
        JIcon(PhIcons.Translate, stringResource(R.string.inbox_translation), size = Tokens.Space.l, tint = PersonaTheme.palette.ink3)
        Spacer(Modifier.width(Tokens.Space.xs))
        Text(shown, style = PersonaTheme.type.callout, color = PersonaTheme.palette.ink2)
    }
}
