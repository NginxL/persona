package persona.core.contracts

/**
 * What a Developing Persona package may contain. Recorded events use code-chosen words and numbers; the free text that
 * remains (an error message or log line) passes [text]. Route words are identical to web/src/lib/error-reports.ts.
 */
object DevScrub {
    /** The longest free text kept, in UTF-8 bytes. */
    const val maxText: Int = 1024

    val apiWords: Set<String> = (
        "accounts agents archive assistant attach auth auto-upgrade before bind binding blob blocked botmux bots browser bulk catalog " +
            "channels chats check claude-dirs close code comments computer config connectors contributions conversations convert create " +
            "current data days decisions deploy diff dirs discover dismiss doc doctor documents done draft enabled engine error-reports " +
            "events extensions file files find fix flags fork fresh fs full git health herdr history hub image import inbox info install " +
            "interactions interrupt items kb kb-search lark launchers log login logout looks machines mail managers markets memory messages " +
            "metrics missing model-config modes moments move mutes notify oauth owner pair pair-codes panes people personas pinned pins posts " +
            "prefs prepare presets preview providers purposes queue quick quota raw reactions read reconnect refresh reject rename replies " +
            "reply resolve restore retry rewind route run runs screen secrets seen send sessions settings show skills spaces split start " +
            "status step suggestion tabs task-plans tasks thread thumb today todos token tokens translate trash tree triage ui unarchive " +
            "update upgrade upgrades usage windows workspace-board workspaces worktree"
        ).split(' ').toSet()

    /** A URL or path with no origin, query, fragment or id, and no leading `/api/v1`. */
    fun route(raw: String): String {
        var path = raw.trim()
        val scheme = path.indexOf("://")
        if (scheme >= 0) {
            val rest = path.substring(scheme + 3)
            val cut = rest.indexOfFirst { it == '/' || it == '?' || it == '#' }
            path = if (cut >= 0) rest.substring(cut) else "/"
        }
        path = path.substringBefore('?').substringBefore('#')
        if (path.startsWith("/api/v1") && (path.length == 7 || path[7] == '/')) path = path.substring(7)
        val out = path.split('/').joinToString("/") { segment ->
            if (segment.isEmpty() || segment in apiWords) segment else ":id"
        }
        return out.ifEmpty { "/" }
    }

    /** A code-chosen socket or screen word, never arbitrary user text. */
    fun word(raw: String): String {
        val allowed = raw.all { (it.code < 128 && it.isLetterOrDigit()) || it == '_' || it == '-' || it == '.' || it == ':' }
        return if (raw.isNotEmpty() && raw.length <= 48 && allowed) raw else "other"
    }

    private val rules = listOf(
        // A URL keeps only its path. Origins, queries and fragments can carry accounts, hosts and pairing secrets.
        Regex("""(?i)\b[a-z][a-z0-9+.-]*://[^\s/?#'"<>()]*([^\s?#'"<>()]*)[^\s'"<>()]*""") to "$1",
        Regex("""(/[^\s'"<>()?#]*)[?#][^\s'"<>()]*""") to "$1",
        Regex("""(?i)\b(Bearer|Basic|Token)\s+[A-Za-z0-9._~+/=-]+""") to "$1 <redacted>",
        Regex("""(?i)\b(access_token|refresh_token|id_token|token|api_key|apikey|key|secret|client_secret|password|passwd|pwd|cookie|set-cookie|authorization|auth|session_token|signature|sig)(\s*[=:]\s*)("[^"]*"|'[^']*'|[^\s,;&'"]+)""") to "$1$2<redacted>",
        Regex("""\beyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+""") to "<redacted>",
        Regex("""[\w.+-]+@[\w-]+(?:\.[\w-]+)+""") to "<email>",
        Regex("""(?<![\w.])(?:25[0-5]|2[0-4]\d|1?\d?\d)(?:\.(?:25[0-5]|2[0-4]\d|1?\d?\d)){3}(?![\w.])""") to "<ip>",
        Regex("""(?i)(?<![\w:])(?:[0-9a-f]{1,4}:){4,7}[0-9a-f]{1,4}(?![\w:])""") to "<ip>",
        Regex("""(?i)(?<![\w:])[0-9a-f]{0,4}(?::[0-9a-f]{0,4})*::[0-9a-f]{0,4}(?::[0-9a-f]{0,4})*(?![\w:])""") to "<ip>",
        Regex("""(?i)\b(?:[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?\.)+(?:com|net|org|io|cn|xyz|cloud|local|lan|internal|arpa)\b""") to "<host>",
        Regex("""(/Users|/home)/[^/\s'"]+""") to "~",
        Regex("""(?<![A-Za-z0-9+/_-])(?=[A-Za-z0-9+/_-]*\d)(?=[A-Za-z0-9+/_-]*[A-Za-z])[A-Za-z0-9+/_-]{32,}={0,2}""") to "<redacted>",
    )

    /** Scrub every free-text field before it reaches the local store or an export. */
    fun text(raw: String): String = clip(rules.fold(raw) { value, (rule, replacement) -> rule.replace(value, replacement) }, maxText)

    /** Cut to [bytes] UTF-8 bytes without splitting a code point, adding an ellipsis when text was removed. */
    fun clip(raw: String, bytes: Int): String {
        require(bytes >= 4)
        if (raw.toByteArray(Charsets.UTF_8).size <= bytes) return raw
        val out = StringBuilder()
        var used = 0
        var index = 0
        while (index < raw.length) {
            val next = raw.offsetByCodePoints(index, 1)
            val size = raw.substring(index, next).toByteArray(Charsets.UTF_8).size
            if (used + size > bytes - 3) break
            out.append(raw, index, next)
            used += size
            index = next
        }
        return out.append('…').toString()
    }
}
