package persona.feature.inbox

import persona.core.contracts.api.MailAddress
import persona.core.contracts.api.MailMessage
import persona.core.contracts.api.SendMailRequest
import android.content.res.Resources
import persona.core.designsystem.LocalizedFormats
import java.net.URLDecoder
import java.time.Instant
import java.time.ZoneId
import java.util.Locale
import persona.core.inbox.*

/** A mail being written ("New mail" / "Reply" / "Forward"): addresses as typed, the quote already in [body]. */
data class MailDraft(
    val accountId: String,
    val to: String = "",
    val cc: String = "",
    val bcc: String = "",
    val subject: String = "",
    val body: String = "",
    val inReplyTo: String? = null,
    val references: List<String> = emptyList(),
) {
    /** The page's title. */
    @get:androidx.annotation.StringRes val title: Int
        get() = when {
            inReplyTo != null -> R.string.inbox_mail_reply
            subject.startsWith("Fwd:") -> R.string.inbox_mail_forward
            else -> R.string.inbox_mail_new
        }
}

/**
 * Building replies / forwards, parsing mailto URIs, and reading address fields.
 */
object MailCompose {
    /** How many lines of the original a reply quotes (the hub's /reply quotes the same 40). */
    const val QUOTE_LINES = 40

    /**
     * Addresses typed into To / Cc / Bcc: split on commas, semicolons (half and full width) and new lines, but not
     * inside a `"quoted name"` or `<…>` (#5076: `"Doe, John" <a@b>`); `Name <a@b>` keeps the name, unquoted. A line
     * with an unclosed quote or bracket splits on every separator, as before. Empty pieces are dropped.
     */
    fun parseAddresses(s: String): List<MailAddress> =
        s.split('\n').flatMap(::splitLine)
            .map { it.trim() }
            .filter { it.isNotEmpty() }
            .map { piece ->
                val lt = piece.lastIndexOf('<')
                val gt = piece.lastIndexOf('>')
                if (lt >= 0 && gt > lt) {
                    MailAddress(unquote(piece.substring(0, lt).trim()), piece.substring(lt + 1, gt).trim())
                } else MailAddress("", piece)
            }

    private val SEPARATORS = charArrayOf(',', '，', ';', '；')

    private fun splitLine(line: String): List<String> {
        val pieces = mutableListOf<String>()
        var start = 0
        var quoted = false
        var angle = false
        var i = 0
        while (i < line.length) {
            val c = line[i]
            when {
                quoted && c == '\\' -> i++
                c == '"' && !angle -> quoted = !quoted
                quoted -> {}
                c == '<' -> angle = true
                c == '>' -> angle = false
                !angle && c in SEPARATORS -> { pieces += line.substring(start, i); start = i + 1 }
            }
            i++
        }
        if (quoted || angle) return line.split(*SEPARATORS)
        pieces += line.substring(start)
        return pieces
    }

    /** `"Doe, John"` → `Doe, John` (`\"` and `\\` unescaped); unquoted names only lose stray quotes, as before. */
    private fun unquote(name: String): String =
        if (name.length >= 2 && name.startsWith('"') && name.endsWith('"')) {
            name.substring(1, name.length - 1).replace(Regex("""\\(.)"""), "$1").trim()
        } else name.trim('"').trim()

    /**
     * Back to the text field: `Name <a@b>` when there is a name, else the bare address. A name with a separator, quote or
     * backslash is written as an RFC 5322 quoted string so [parseAddresses] reads it back whole.
     */
    fun formatAddresses(list: List<MailAddress>): String =
        list.joinToString(", ") { if (it.name.isNotBlank() && it.name != it.email) "${quoteName(it.name)} <${it.email}>" else it.email }

    private fun quoteName(name: String): String =
        if (name.any { it in SEPARATORS || it == '"' || it == '\\' }) {
            "\"" + name.replace("\\", "\\\\").replace("\"", "\\\"") + "\""
        } else name

    /** A plausible address (one @, something on both sides, a dot after it). Only gates the send button. */
    fun looksValid(email: String): Boolean {
        val trimmed = email.trim()
        val at = trimmed.indexOf('@')
        if (at <= 0 || at != trimmed.lastIndexOf('@') || trimmed.any { it.isWhitespace() }) return false
        val domain = trimmed.substring(at + 1)
        return domain.contains('.') && !domain.startsWith('.') && !domain.endsWith('.')
    }

    /** Every address of a field is valid (and there is at least one when [required]). */
    fun fieldValid(s: String, required: Boolean): Boolean {
        val list = parseAddresses(s)
        if (list.isEmpty()) return !required
        return list.all { looksValid(it.email) }
    }

    fun replySubject(subject: String?): String {
        val s = subject.orEmpty().trim()
        val l = s.lowercase()
        return if (l.startsWith("re:") || l.startsWith("re：") || s.startsWith("回复：") || s.startsWith("回复:")) s else "Re: $s" // i18n-ignore: matches subjects other clients wrote
    }

    fun forwardSubject(subject: String?): String {
        val s = subject.orEmpty().trim()
        val l = s.lowercase()
        return if (l.startsWith("fwd:") || l.startsWith("fw:") || s.startsWith("转发：") || s.startsWith("转发:")) s else "Fwd: $s" // i18n-ignore: matches subjects other clients wrote
    }

    /** The References header of a reply: the original's chain plus its own Message-ID, without duplicates. */
    fun replyReferences(original: MailMessage): List<String> =
        (original.references + listOfNotNull(original.messageId.takeIf { it.isNotBlank() })).distinct()

    /** A full date and time in [locale]'s own order (Nov 4, 2026, 6:00 PM in English; year-month-day in Chinese). */
    fun formatDate(t: Instant?, locale: Locale, zone: ZoneId = ZoneId.systemDefault()): String =
        t?.let { LocalizedFormats.format(it, LocalizedFormats.DATE_TIME, locale, zone) }.orEmpty()

    /** The original's text for quoting / forwarding: plain text, or the HTML reduced to text. */
    fun plainText(m: MailMessage): String =
        m.text.takeIf { it.isNotBlank() } ?: mailPlainText(m.html)

    /** Parse an ISO-8601 or RFC-style date string into Instant if possible. */
    private fun parseDate(dateStr: String): Instant? = try {
        Instant.parse(dateStr)
    } catch (_: Exception) {
        null
    }

    /**
     * "\n\n<date>, <sender> wrote:\n> ..." (first [QUOTE_LINES] lines), in the language of [res]: it goes into the mail
     * the user sends.
     */
    fun quote(res: Resources, m: MailMessage, zone: ZoneId = ZoneId.systemDefault()): String {
        val lines = plainText(m).trimEnd().split('\n').take(QUOTE_LINES).joinToString("\n") { "> $it" }
        val who = m.from.display()
        val instant = parseDate(m.date)
        val date = if (instant != null) formatDate(instant, res.mailLocale, zone) else m.date
        val known = listOf(date, who).filter { it.isNotBlank() }
        val line = when (known.size) {
            2 -> res.getString(R.string.inbox_mail_quote_both, date, who)
            1 -> res.getString(R.string.inbox_mail_quote_one, known[0])
            else -> res.getString(R.string.inbox_mail_quote_none)
        }
        return "\n\n" + line + "\n" + lines
    }

    /**
     * "Reply" / "Reply all". [me] = the receiving account's address (left out of reply-all). Reply-all sends to the sender plus
     * the original "To", and copies the original "Cc".
     */
    fun reply(res: Resources, m: MailMessage, accountId: String, me: String?, all: Boolean, zone: ZoneId = ZoneId.systemDefault()): MailDraft {
        val mine = me?.lowercase()?.trim()
        val sender = listOfNotNull(m.from.takeIf { it.email.isNotBlank() })
        var to = sender
        var cc = emptyList<MailAddress>()
        if (all) {
            val seen = HashSet<String>()
            fun keep(a: MailAddress) = a.email.isNotBlank() && a.email.lowercase().trim() != mine && seen.add(a.email.lowercase().trim())
            to = (sender + m.to).filter(::keep)
            cc = m.cc.filter(::keep)
            // Replying to yourself (a mail you sent): the original recipients become the To.
            if (to.isEmpty()) to = sender
        }
        return MailDraft(
            accountId = accountId,
            to = formatAddresses(to),
            cc = formatAddresses(cc),
            subject = replySubject(m.subject),
            body = quote(res, m, zone),
            inReplyTo = m.messageId.takeIf { it.isNotBlank() },
            references = replyReferences(m),
        )
    }

    /** "Forward": no recipients, the original's header block and full text below. */
    fun forward(res: Resources, m: MailMessage, accountId: String, zone: ZoneId = ZoneId.systemDefault()): MailDraft {
        val instant = parseDate(m.date)
        val dateText = if (instant != null) formatDate(instant, res.mailLocale, zone) else m.date
        fun line(label: Int, value: String) = res.getString(R.string.inbox_mail_header_line, res.getString(label), value) + "\n"
        val head = buildString {
            append("\n\n").append(res.getString(R.string.inbox_mail_forward_header)).append('\n')
            if (m.from.email.isNotBlank()) append(line(R.string.inbox_mail_from, formatAddresses(listOf(m.from))))
            if (dateText.isNotBlank()) append(line(R.string.inbox_mail_date, dateText))
            append(line(R.string.inbox_mail_subject, m.subject))
            if (m.to.isNotEmpty()) append(line(R.string.inbox_mail_to, formatAddresses(m.to)))
            if (m.cc.isNotEmpty()) append(line(R.string.inbox_mail_cc, formatAddresses(m.cc)))
            append('\n')
        }
        return MailDraft(
            accountId = accountId,
            subject = forwardSubject(m.subject),
            body = head + plainText(m).trimEnd(),
        )
    }

    /**
     * Parses a `mailto:` URI into a [MailDraft].
     * Example: `mailto:alice@example.com?subject=Hello&body=World&cc=bob@example.com`
     */
    fun parseMailto(uriString: String, defaultAccountId: String): MailDraft {
        if (!uriString.startsWith("mailto:", ignoreCase = true)) {
            return MailDraft(accountId = defaultAccountId)
        }
        val withoutScheme = uriString.substring(7)
        val questionMark = withoutScheme.indexOf('?')
        val rawTo = if (questionMark >= 0) withoutScheme.substring(0, questionMark) else withoutScheme
        val queryString = if (questionMark >= 0) withoutScheme.substring(questionMark + 1) else ""

        var subject = ""
        var body = ""
        var cc = ""
        var bcc = ""
        var extraTo = ""

        if (queryString.isNotEmpty()) {
            queryString.split('&').forEach { param ->
                val eq = param.indexOf('=')
                if (eq > 0) {
                    val key = param.substring(0, eq).lowercase()
                    val rawVal = param.substring(eq + 1)
                    val value = decodePercent(rawVal)
                    when (key) {
                        "subject" -> subject = value
                        "body" -> body = value
                        "cc" -> cc = if (cc.isEmpty()) value else "$cc, $value"
                        "bcc" -> bcc = if (bcc.isEmpty()) value else "$bcc, $value"
                        "to" -> extraTo = if (extraTo.isEmpty()) value else "$extraTo, $value"
                    }
                }
            }
        }

        // RFC 6068: to= values add to the recipients in the path.
        val to = listOf(decodePercent(rawTo), extraTo).filter { it.isNotEmpty() }.joinToString(", ")

        return MailDraft(
            accountId = defaultAccountId,
            to = to.replace(';', ','),
            cc = cc.replace(';', ','),
            bcc = bcc.replace(';', ','),
            subject = subject,
            body = body,
        )
    }

    /** Percent-decodes [raw] as UTF-8; unlike form decoding, `+` stays literal (RFC 6068). */
    private fun decodePercent(raw: String): String = try {
        URLDecoder.decode(raw.replace("+", "%2B"), "UTF-8")
    } catch (_: Exception) {
        raw
    }

    /** The request for [draft] as edited (`references` space-joined, the way the hub's SendRequest takes it). */
    fun request(draft: MailDraft, bcc: String = draft.bcc): SendMailRequest = SendMailRequest(
        accountId = draft.accountId,
        to = parseAddresses(draft.to),
        cc = parseAddresses(draft.cc),
        bcc = parseAddresses(bcc),
        subject = draft.subject,
        text = draft.body,
        inReplyTo = draft.inReplyTo,
        references = draft.references.takeIf { it.isNotEmpty() }?.joinToString(" "),
    )
}

/** The language [this]'s text is in; dates in a mail use it. */
internal val Resources.mailLocale: Locale get() = configuration.locales[0]
