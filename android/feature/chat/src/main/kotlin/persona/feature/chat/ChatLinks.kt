package persona.feature.chat

import persona.core.contracts.ProtocolCompatibility

import android.content.res.Resources
import androidx.compose.runtime.saveable.listSaver
import androidx.compose.runtime.staticCompositionLocalOf
import persona.core.contracts.Item
import persona.core.contracts.contextBody
import persona.core.contracts.parseTime
import java.net.URI
import java.net.URLDecoder
import java.time.Duration
import java.time.Instant
import java.util.UUID

internal data class ChatLinkMatch(val url: String, val start: Int, val end: Int)

/** Where a tapped link may go: a web page in the sheet, a page of this app, another app (after asking), or nowhere. */
enum class ChatLinkKind { Web, App, External, Refused }

/**
 * A `persona://` link in a conversation (iOS writes `persona://session/…` as the url of a picked session). They only
 * navigate: anything can open the app's links, so nothing reached this way may send, answer or change anything, and
 * `persona://pair` (replacing the hub) is not navigation: tapped in a conversation it is ignored.
 */
sealed interface ChatAppLink {
    data class Session(val machineId: String, val sessionId: String) : ChatAppLink
    /** `persona` (also `assistant`), `agents` or `inbox`. */
    data class Tab(val tab: String) : ChatAppLink
}

/** Supplied by the app shell: opens a conversation or a tab for a tapped `persona://` link. */
val LocalChatAppLinks = staticCompositionLocalOf<((ChatAppLink) -> Unit)?> { null }

/**
 * Supplied by the app shell: opens a link to a page of the hub's web app (the assistant's `/s/…`, `/persona/…`,
 * `/todos?id=…` links) in the app instead, and says whether it did; a page it has no screen for, or another
 * site, is left to the sheet.
 */
val LocalChatHubLinks = staticCompositionLocalOf<((String) -> Boolean)?> { null }

/** What to ask before a link leaves for another app: a title and one line. */
internal data class ExternalPrompt(val title: String, val text: String)

internal object ChatLinks {
    private val pattern = Regex("https?://[\\x21-\\x7e]+", RegexOption.IGNORE_CASE)
    private val protectedMarkdown = Regex("```[\\s\\S]*?```|~~~[\\s\\S]*?~~~|`[^`]*`|!?\\[[^\\]]*]\\([^\\n]*?\\)|<https?://[^>]*>")
    /** Local files and providers, scripts, inline documents: never followed, from a message or from a page. */
    private val refused = setOf("content", "file", "javascript", "data", "about", "blob", "vbscript")

    fun webUrl(value: String): String? = runCatching {
        val uri = URI(value.trim())
        value.trim().takeIf { uri.scheme?.lowercase() in setOf("http", "https") && !uri.host.isNullOrEmpty() && uri.rawUserInfo == null }
    }.getOrNull()

    fun matches(text: String): List<ChatLinkMatch> = pattern.findAll(text).mapNotNull { match ->
        var url = match.value.substringBefore('<').substringBefore('>').substringBefore('"')
        do {
            val before = url
            url = url.trimEnd('.', ',', ';', ':', '!', '?')
            for ((close, open) in listOf(')' to '(', ']' to '[')) {
                while (url.endsWith(close) && url.count { it == close } > url.count { it == open }) url = url.dropLast(1)
            }
        } while (url != before)
        webUrl(url)?.let { ChatLinkMatch(it, match.range.first, match.range.first + it.length) }
    }.toList()

    fun markdown(text: String): String {
        val protected = protectedMarkdown.findAll(text).map { it.range }.toList()
        val links = matches(text).filter { link -> protected.none { link.start in it } }
        return buildString {
            var cursor = 0
            for (link in links) { append(text.substring(cursor, link.start)); append('<'); append(link.url); append('>'); cursor = link.end }
            append(text.substring(cursor))
        }
    }

    /**
     * Links worth loading before a tap: the first three in Agent replies written in the last 15 minutes, among the
     * last 40 items. Old history never preloads (on iOS, preloading links while paging old replies froze scrolling),
     * and hosts without a dot (localhost and the like, a computer's own) are not pages the phone can load.
     */
    fun toPreload(items: List<Item>, now: Instant): List<String> {
        val cutoff = now.minus(Duration.ofMinutes(15))
        return items.asReversed().asSequence().take(40)
            .filter { it.role == "assistant" && it.kind == "text" && (parseTime(it.ts) ?: Instant.EPOCH).isAfter(cutoff) }
            .flatMap { item -> matches(item.text.orEmpty()).asSequence().map { it.url } }
            .filter { url -> runCatching { URI(url).host }.getOrNull()?.contains('.') == true }
            .distinct().take(3).toList()
    }

    /** http(s) opens in the sheet, `persona:` navigates in the app, mailto: / tel: / intent: and other apps go out. */
    fun kind(url: String): ChatLinkKind {
        val scheme = url.trim().substringBefore(':', "").lowercase()
        return when {
            scheme == "http" || scheme == "https" -> ChatLinkKind.Web
            scheme in ProtocolCompatibility.appSchemes -> ChatLinkKind.App
            scheme.isEmpty() || scheme in refused || !scheme.all { it.isLetterOrDigit() || it == '+' || it == '-' || it == '.' } ->
                ChatLinkKind.Refused
            else -> ChatLinkKind.External
        }
    }

    fun appLink(url: String): ChatAppLink? {
        val uri = runCatching { URI(url.trim()) }.getOrNull() ?: return null
        if (uri.scheme?.lowercase() !in ProtocolCompatibility.appSchemes) return null
        val path = uri.rawPath.orEmpty().split('/').filter { it.isNotEmpty() }.map { URLDecoder.decode(it, "UTF-8") }
        return when (uri.host?.lowercase()) {
            "session" -> if (path.size >= 2 && path[0].isNotBlank() && path[1].isNotBlank()) ChatAppLink.Session(path[0], path[1]) else null
            "assistant" -> ChatAppLink.Tab("persona")
            "inbox" -> ChatAppLink.Tab("inbox")
            "tab" -> ProtocolCompatibility.canonicalTab(path.firstOrNull()?.lowercase())?.let { if (it == "assistant") "persona" else it }
                ?.takeIf { it in setOf("persona", "agents", "inbox") }?.let { ChatAppLink.Tab(it) }
            else -> null // pair, open and anything else: not from a tapped link
        }
    }

    /**
     * Whether an intent built from an external link may start: never with data the app's own (`persona:`), another
     * intent, local files or providers, and never into this app's own package.
     */
    fun allowedIntent(dataScheme: String?, targetPackage: String?, ownPackage: String): Boolean {
        val scheme = dataScheme?.lowercase()
        if (scheme != null && (scheme in ProtocolCompatibility.appSchemes || scheme == "intent" || scheme in refused)) return false
        return targetPackage != ownPackage
    }

    /** Asked before mailto: / tel: / another app's link leaves the app. */
    fun externalPrompt(url: String, resources: Resources): ExternalPrompt {
        val trimmed = url.trim()
        val scheme = trimmed.substringBefore(':').lowercase()
        // The scheme-specific part, percent-decoded (a + in a phone number stays a +).
        val target = (runCatching { URI(trimmed).schemeSpecificPart }.getOrNull() ?: trimmed.substringAfter(':')).substringBefore('?').trim()
        return when (scheme) {
            "mailto" -> ExternalPrompt(resources.getString(R.string.chat_links_mail_title),
                if (target.isEmpty()) resources.getString(R.string.chat_links_mail_text) else resources.getString(R.string.chat_links_mail_to, target))
            "tel" -> ExternalPrompt(resources.getString(R.string.chat_links_tel_title),
                if (target.isEmpty()) resources.getString(R.string.chat_links_tel_text) else resources.getString(R.string.chat_links_tel_to, target))
            "sms", "smsto" -> ExternalPrompt(resources.getString(R.string.chat_links_sms_title),
                if (target.isEmpty()) resources.getString(R.string.chat_links_sms_text) else resources.getString(R.string.chat_links_sms_to, target))
            else -> ExternalPrompt(resources.getString(R.string.chat_links_other_title), resources.getString(R.string.chat_links_other_text, trimmed.take(80)))
        }
    }
}

/**
 * A piece of a page picked in the link sheet, carried in a message as
 * `<persona-context kind="context|edit|quote" title="…" url="…" msg="…">text</persona-context>`, written exactly as iOS's `ContextRef`
 * does: the attributes escaped, the text through [contextBody] (it cannot close the tag).
 */
data class ChatLinkContext(
    val kind: String = "context", val title: String, val url: String, val text: String,
    val id: String = UUID.randomUUID().toString(),
    /** A quoted message's id in its chat ([kind] `quote`, iOS `ContextRef.msg`): one quote per message. */
    val msg: String = "",
    /** Which part of the message is quoted ([PART_SELECTION]: words picked in "Select", iOS `ContextRef.part`); empty: all of it. */
    val part: String = "",
) {
    fun xml(): String = "<persona-context kind=\"${if (kind == "edit" || kind == QUOTE) kind else "context"}\" title=\"${escape(title)}\" url=\"${escape(url)}\"" +
        (if (msg.isEmpty()) "" else " msg=\"${escape(msg)}\"") + (if (part.isEmpty()) "" else " part=\"${escape(part)}\"") +
        ">\n" + contextBody(text) + "\n</persona-context>"

    companion object {
        /**
         * The text of a whole page picked as context: the agent reads the page itself. The same marker as iOS
         * (`LinkSheet.pick`) and web (`links.ts` WHOLE); iOS's quote row keys on it to show "Quote <title>".
         */
        const val WHOLE = "（整篇）" // i18n-ignore: the wire marker iOS and web send and match

        /**
         * What the link card's "Use as context" sends for the whole of a native Feishu view: a document by [WHOLE], a Moments post
         * by its text once [read] (null until then: no button); null for any other page.
         */
        internal fun whole(native: persona.feature.inbox.LarkNativeLink?, read: String?): String? = when (native) {
            is persona.feature.inbox.LarkNativeLink.Document -> WHOLE
            is persona.feature.inbox.LarkNativeLink.MomentsPost -> read?.ifBlank { WHOLE }
            null -> null
        }
        /** "Quote" on a message of this chat (iOS `ContextRef.quote`): [title] is who wrote it, [QUOTE_ME] for the user's own. */
        const val QUOTE = "quote"
        /** The author of the user's own quoted message on the wire, whatever the app's language (iOS writes and reads the Chinese "me"). */
        const val QUOTE_ME = "我" // i18n-ignore: on the wire in every language, iOS writes and reads it
        /** [ChatLinkContext.part] of a quote of the words picked in a selected message (iOS `ContextRef.selectionPart`). */
        const val PART_SELECTION = "selection"
        fun quote(text: String, from: String, msg: String, part: String = "") = ChatLinkContext(QUOTE, from, "", text.trim(), msg = msg, part = part)

        private val tags = Regex("<persona-context\\b([^>]*)>([\\s\\S]*?)</persona-context>\\s*")
        private val attributes = Regex("([a-z]+)=\"([^\"]*)\"")
        private fun escape(s: String) = s.replace("&", "&amp;").replace("\"", "&quot;").replace("<", "&lt;").replace(">", "&gt;")
        private fun unescape(s: String) = s.replace("&quot;", "\"").replace("&lt;", "<").replace("&gt;", ">").replace("&amp;", "&")

        /** The contexts of a message and the message without them; the text is read as iOS reads it (entities decoded). */
        internal fun parse(text: String): Pair<List<ChatLinkContext>, String> {
            val refs = tags.findAll(text).map { tag ->
                val attrs = attributes.findAll(tag.groupValues[1]).associate { it.groupValues[1] to unescape(it.groupValues[2]) }
                ChatLinkContext(attrs["kind"] ?: "context", attrs["title"].orEmpty(), attrs["url"].orEmpty(), unescape(tag.groupValues[2].trim()),
                    msg = attrs["msg"].orEmpty(), part = attrs["part"].orEmpty())
            }.toList()
            return refs to tags.replace(text, "").trim()
        }

        /** "AI edit": the selection and what to do with it, one message to the agent (iOS `LinkSheet`). */
        fun editMessage(selection: ChatLinkContext, instruction: String, resources: Resources): String {
            val ask = instruction.trim()
            return selection.copy(kind = "edit").xml() + "\n\n" +
                if (ask.isEmpty()) resources.getString(R.string.chat_links_edit_default) else resources.getString(R.string.chat_links_edit_ask, ask)
        }

        /** "Hand to Persona" from a page with no conversation behind it: the selection and what Persona is to do with it. */
        fun handToPersonaMessage(selection: ChatLinkContext, instruction: String, resources: Resources): String =
            selection.copy(kind = "context").xml() + "\n\n" + instruction.trim().ifEmpty { resources.getString(R.string.chat_links_hand_default) }
    }
}

/**
 * A message with the picked contexts in front of it, as iOS sends it: the tags a line apart, then a blank line and
 * what was typed ("See the content above." when only contexts go).
 */
internal fun linkMessage(draft: String, contexts: List<ChatLinkContext>, resources: Resources): String =
    if (contexts.isEmpty()) draft
    else contexts.joinToString("\n") { it.xml() } + "\n\n" + draft.ifBlank { resources.getString(R.string.chat_links_context_default) }

/**
 * [ChatLinkContext]s kept across a configuration change: seven values each after a [CONTEXTS_V3] mark (the quoted
 * part the seventh); six after [CONTEXTS_V2] (the quoted message's id the sixth); five each, without a mark, as saved
 * before quotes had one.
 */
internal val ChatLinkContextsSaver = listSaver<List<ChatLinkContext>, String>(
    save = { refs -> listOf(CONTEXTS_V3) + refs.flatMap { listOf(it.kind, it.title, it.url, it.text, it.id, it.msg, it.part) } },
    restore = { values ->
        when (values.firstOrNull()) {
            CONTEXTS_V3 -> values.drop(1).chunked(7).filter { it.size == 7 }.map { ChatLinkContext(it[0], it[1], it[2], it[3], it[4], it[5], it[6]) }
            CONTEXTS_V2 -> values.drop(1).chunked(6).filter { it.size == 6 }.map { ChatLinkContext(it[0], it[1], it[2], it[3], it[4], it[5]) }
            else -> values.chunked(5).filter { it.size == 5 }.map { ChatLinkContext(it[0], it[1], it[2], it[3], it[4]) }
        }
    },
)

private const val CONTEXTS_V2 = "contexts-v2"
private const val CONTEXTS_V3 = "contexts-v3"

/**
 * [contexts] with [quote] added (iOS `bindMenu`, `ContextRef.adding`): a message quoted already is not quoted twice. A message
 * goes over the box once as a whole and once for each part picked from it ([ChatLinkContext.part]; the same words not twice).
 */
internal fun withQuote(contexts: List<ChatLinkContext>, quote: ChatLinkContext): List<ChatLinkContext> =
    if (quote.msg.isNotEmpty() && contexts.any {
        it.kind == ChatLinkContext.QUOTE && it.msg == quote.msg && it.part == quote.part && (quote.part.isEmpty() || it.text == quote.text)
    }) contexts else contexts + quote

/**
 * "Quote" on a message of this chat: the user's own is by [ChatLinkContext.QUOTE_ME] in every language, else by [agent]. [part]:
 * [ChatLinkContext.PART_SELECTION] when [text] is words picked in it.
 */
internal fun messageQuote(text: String, user: Boolean, agent: String, msg: String, part: String = ""): ChatLinkContext =
    ChatLinkContext.quote(text, if (user) ChatLinkContext.QUOTE_ME else agent, msg, part)
