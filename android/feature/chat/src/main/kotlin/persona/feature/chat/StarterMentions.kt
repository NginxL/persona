package persona.feature.chat

import android.content.res.Resources
import androidx.compose.runtime.Composable
import androidx.compose.runtime.MutableState
import androidx.compose.runtime.Stable
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.saveable.rememberSaveable
import persona.core.contracts.Session

/**
 * New chat's @ (iOS `StarterComposer`: `MentionSource(machine:)` and `StarterDraft.quotes`). A conversation that does not
 * exist yet has no tasks, files or folder, so @ lists the other agents' sessions, every computer's, most recently active
 * first, and a typed absolute or `~` path lists that folder on the assistant's computer ([starterMentionSource]). A
 * picked session waits over the composer as a quote card ([StarterQuoteCards]) and goes in front of the first message
 * as a `<persona-context>` tag, as in a chat ([message]).
 */
@Stable
class StarterMentions internal constructor(private val state: MutableState<List<ChatLinkContext>>) {
    /** The sessions picked, in the order they were picked. */
    val quotes: List<ChatLinkContext> get() = state.value

    /** A session picked in the @ list; one already quoted is not quoted twice (iOS compares their links). */
    fun add(quote: ChatLinkContext) {
        if (quotes.none { it.url == quote.url }) state.value = quotes + quote
    }

    fun remove(quote: ChatLinkContext) {
        state.value = quotes.filterNot { it.id == quote.id }
    }

    /** The first message as it goes: the quotes' tags first, then what was typed, or "See the content above." with nothing typed. */
    fun message(typed: String, resources: Resources): String = linkMessage(typed, quotes, resources)

    /** [sent] went with a message: out of the box; sessions picked since stay. */
    fun sent(sent: List<ChatLinkContext>) {
        if (sent.isNotEmpty()) state.value = quotes.filterNot { quote -> sent.any { it.id == quote.id } }
    }
}

/** New chat's picked sessions, kept across a configuration change; [inputs] start them over (another assistant's page). */
@Composable
fun rememberStarterMentions(vararg inputs: Any?): StarterMentions {
    val quotes = rememberSaveable(*inputs, stateSaver = ChatLinkContextsSaver) { mutableStateOf(emptyList<ChatLinkContext>()) }
    return remember(quotes) { StarterMentions(quotes) }
}

/**
 * What New chat's @ list offers: [sessions] (it leaves out Persona's conversations) and, with [folders], the folder a
 * typed absolute or `~` path names; no tasks, files or folder of its own.
 */
fun starterMentionSource(sessions: List<Session>, folders: MentionFolders?): ComposerMentions =
    ComposerMentions(files = emptyList(), cwd = "", sessions = sessions, except = null, folders = folders)

/** The sessions picked in New chat's @ list, over its composer as a chat's quote cards; Remove takes one out. */
@Composable
fun StarterQuoteCards(mentions: StarterMentions) {
    if (mentions.quotes.isNotEmpty()) ComposerContextCards(mentions.quotes) { removed -> removed.forEach(mentions::remove) }
}

/** A message as its bubble shows the words: the `<persona-context>` tags in front of them left out. */
fun messageWords(text: String): String = ChatLinkContext.parse(text).second
