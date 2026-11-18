package persona.core.domain

import kotlinx.serialization.json.JsonArray
import kotlinx.serialization.json.JsonElement
import kotlinx.serialization.json.JsonNull
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.JsonPrimitive
import kotlinx.serialization.json.booleanOrNull
import kotlinx.serialization.json.contentOrNull
import persona.core.contracts.canonicalMcpToolName
import java.math.BigDecimal

/**
 * The words a tool call reads as when no table names it (#2986, iOS `ToolNaming`): another server's MCP tool as
 * "<Server> · <Tool>" ("mcp__github__create_issue" is "GitHub · Create issue"), any other unknown name in words, a
 * subject picked from its input by the field that says the most, and its arguments as a short key/value list instead
 * of JSON. The shared cases are in spec/fixtures/tool-names.
 */
object ToolNaming {
    /** Words spelled their own way wherever they show up in a name ("github" is "GitHub", "pr" is "PR"). */
    val spelled: Map<String, String> = mapOf(
        "api" to "API", "ai" to "AI", "aws" to "AWS", "ci" to "CI", "cli" to "CLI", "csv" to "CSV", "devtools" to "DevTools",
        "dns" to "DNS", "gcp" to "GCP", "github" to "GitHub", "gitlab" to "GitLab", "graphql" to "GraphQL", "html" to "HTML",
        "http" to "HTTP", "https" to "HTTPS", "id" to "ID", "ids" to "IDs", "ide" to "IDE", "ios" to "iOS", "json" to "JSON",
        "llm" to "LLM", "lsp" to "LSP", "macos" to "macOS", "mcp" to "MCP", "oauth" to "OAuth", "openai" to "OpenAI",
        "pdf" to "PDF", "pr" to "PR", "prs" to "PRs", "sdk" to "SDK", "sql" to "SQL", "ssh" to "SSH", "ui" to "UI",
        "uri" to "URI", "url" to "URL", "urls" to "URLs", "youtube" to "YouTube",
    )

    /**
     * An identifier cut into words: at `_`, `-`, `.`, `/`, `:` and spaces, and where camel case turns ("getPRComments"
     * is get, PR, Comments).
     */
    fun words(identifier: String): List<String> {
        val out = mutableListOf<String>()
        val word = StringBuilder()
        identifier.forEachIndexed { n, c ->
            if (c in "_-./: ") {
                if (word.isNotEmpty()) out += word.toString()
                word.clear()
                return@forEachIndexed
            }
            if (word.isNotEmpty() && c.isUpperCase()) {
                val before = identifier[n - 1]
                val lowerNext = n + 1 < identifier.length && identifier[n + 1].isLowerCase()
                // "fooBar" turns at B; "HTTPServer" keeps HTTP together and turns at the S before "erver".
                if (before.isLowerCase() || before.isDigit() || (before.isUpperCase() && lowerNext)) {
                    out += word.toString()
                    word.clear()
                }
            }
            word.append(c)
        }
        if (word.isNotEmpty()) out += word.toString()
        return out
    }

    /** One word as it reads inside a sentence: the spelled ones as spelled, an acronym as written, the rest lowercase. */
    private fun plain(word: String): String = spelled[word.lowercase()]
        ?: if (word.length > 1 && word.all { it.isUpperCase() || it.isDigit() }) word else word.lowercase()

    /**
     * An identifier in words, the first one capitalized: "create_issue" and "createIssue" are "Create issue",
     * "getPRComments" is "Get PR comments".
     */
    fun humanize(identifier: String): String {
        val w = words(identifier).map(::plain)
        val first = w.firstOrNull() ?: return ""
        // "iOS" stays as spelled; any other first word starts with a capital.
        val head = if (first in spelled.values) first else first.replaceFirstChar { it.uppercase() }
        return (listOf(head) + w.drop(1)).joinToString(" ")
    }

    /**
     * An MCP server's name as a name: "github" is "GitHub", "chrome-devtools" "Chrome DevTools". A plugin's server
     * ("plugin_<plugin>_<server>") and a claude.ai connector ("claude_ai_<name>") read as their own name, once.
     */
    fun serverName(server: String): String {
        var s = server
        for (prefix in listOf("plugin_", "claude_ai_")) if (s.startsWith(prefix) && s.length > prefix.length) s = s.removePrefix(prefix)
        var w = words(s)
        // "plugin_context7_context7": the plugin and its server share the name.
        val half = w.size / 2
        if (half > 0 && w.size % 2 == 0 && w.take(half).map { it.lowercase() } == w.drop(half).map { it.lowercase() }) w = w.take(half)
        return w.joinToString(" ") { word -> spelled[word.lowercase()] ?: word.replaceFirstChar { it.uppercase() } }
    }

    /** Another server's MCP tool, not Persona's own ([isPersonaTool]): no table names it by its bare name. */
    fun isOtherMcp(name: String): Boolean = canonicalMcpToolName(name).startsWith("mcp__") && !isPersonaTool(name)

    /**
     * The name the tool tables match on: a tool's own name ("Read", "agent_read" of mcp__persona__agent_read), or for
     * another server's MCP tool its whole name, which no table has ("mcp__github__search" is not Grep's search).
     */
    fun tableName(name: String): String = if (isOtherMcp(name)) name else toolBaseName(name)

    /** "GitHub · Create issue" for another server's MCP tool; null for any other tool and Persona's own. */
    fun mcpLabel(name: String): String? {
        if (!isOtherMcp(name)) return null
        val server = toolServer(name) ?: return null
        val tool = humanize(name.drop(5 + server.length + 2))
        val shown = serverName(server)
        return if (tool.isEmpty()) shown else "$shown · $tool"
    }

    // Subjects

    /** The fields that say what a call is about, the most telling first. */
    val subjectKeys: List<String> = listOf("description", "title", "subject", "name", "file_path", "filePath", "notebook_path",
        "path", "url", "uri", "query", "pattern", "command", "prompt")
    private val pathKeys = setOf("file_path", "filePath", "notebook_path", "path")

    /** Line breaks as Swift's `Character.isNewline` has them (CR LF is one). */
    private val newline = Regex("\r\n|[\n\u000B\u000C\r\u0085\u2028\u2029]")

    /** A string value with something in it (numbers and flags say little on their own). */
    fun text(value: JsonElement?): String? =
        (value as? JsonPrimitive)?.takeIf { it.isString }?.content?.trim()?.takeIf(String::isNotEmpty)

    /** The first line with words on it. */
    fun firstLine(s: String): String = s.split(newline).map(String::trim).firstOrNull(String::isNotEmpty).orEmpty()

    /** A path's last part ("/a/b/Store.swift" is "Store.swift"); a path that ends in "/" keeps its folder's name. */
    fun lastPathComponent(path: String): String = path.split('/', '\\').lastOrNull(String::isNotEmpty) ?: path

    /**
     * What an unknown tool's call is about: the first of [subjectKeys] it has (a path by its file's name), else its first
     * string by field name; "" when it has none.
     */
    fun subject(input: JsonElement?): String {
        val o = input as? JsonObject ?: return ""
        for (key in subjectKeys) text(o[key])?.let { return if (key in pathKeys) lastPathComponent(it) else firstLine(it) }
        for (key in o.keys.sorted()) text(o[key])?.let { return firstLine(it) }
        return ""
    }

    /**
     * A shell tool's command as its detail shows it ("$ go test ./..."); a Monitor that listens on a WebSocket shows its
     * address. Null for any other tool.
     */
    fun commandLine(name: String, input: JsonElement?): String? {
        val o = input as? JsonObject
        fun s(key: String) = (o?.get(key) as? JsonPrimitive)?.takeUnless { it is JsonNull }?.content
        return when (tableName(name)) {
            "Bash" -> "$ " + s("command").orEmpty()
            "PowerShell" -> "PS> " + s("command").orEmpty()
            "Monitor" -> s("command")?.let { "$ $it" } ?: ((o?.get("ws") as? JsonObject)?.get("url") as? JsonPrimitive)?.contentOrNull
            else -> null
        }
    }

    // Arguments

    /**
     * One argument of a call as its detail lists it: the field as the tool names it ([id]) and in words ([label],
     * "file_path" is "File path"), its [value], whether it is set in the code face (a path, an id, a number, JSON), and
     * how many [lines] it may take (1, or a few for text with lines of its own).
     */
    data class Argument(val id: String, val label: String, val value: String, val code: Boolean, val lines: Int)

    /** How much of a long value a line shows: the rest is in the raw input. */
    private const val VALUE_CAP = 300

    /**
     * A call's input as a short key/value list: the telling fields first ([subjectKeys]), then the rest by name; text on
     * one line (a few for text with lines of its own), lists of words joined, anything nested as one line of JSON.
     */
    fun arguments(input: JsonElement?, skipping: Set<String> = emptySet()): List<Argument> {
        val o = input as? JsonObject ?: return emptyList()
        val first = subjectKeys.filter { it in o }
        val rest = o.keys.filter { it !in first }.sorted()
        return (first + rest).filter { it !in skipping }.mapNotNull { key ->
            argument(o.getValue(key))?.let { (value, code, lines) -> Argument(key, humanize(key), value, code, lines) }
        }
    }

    private fun argument(value: JsonElement): Triple<String, Boolean, Int>? = when (value) {
        JsonNull -> Triple("null", true, 1)
        is JsonPrimitive -> if (!value.isString) Triple(scalar(value), true, 1) else {
            // Paragraphs keep one break between them: a blank line would spend one of the few lines a value gets, and a
            // cut there shows no "…". The raw input keeps the text as sent.
            val t = value.content.split(newline).filterNot { line -> line.all(Char::isWhitespace) }.joinToString("\n").trim()
            // A token (a path, an id, a pattern) in the code face; a sentence someone wrote in the text face.
            if (t.isEmpty()) null else Triple(capped(t), t.none(Char::isWhitespace) && t.all { it.code < 128 }, if ('\n' in t) 3 else 1)
        }
        is JsonArray -> when {
            value.isEmpty() -> null
            value.all { it is JsonPrimitive } ->
                Triple(capped(value.mapNotNull { (it as JsonPrimitive).takeUnless { p -> p is JsonNull }?.let(::plain) }.joinToString(", ")), true, 1)
            else -> Triple(capped(json(value)), true, 1)
        }
        is JsonObject -> if (value.isEmpty()) null else Triple(capped(json(value)), true, 1)
    }

    private fun plain(p: JsonPrimitive): String = if (p.isString) p.content else scalar(p)

    /** A flag as "true" / "false", a number as iOS prints it ([number]). */
    private fun scalar(p: JsonPrimitive): String = p.booleanOrNull?.toString() ?: number(p.content)

    /**
     * A number as iOS prints it (`JSONValue.string`, a Double): a whole one in Long's range as a whole number, any other
     * as Swift writes a Double ("2.5", "1e+20", "1.8446744073709552e+19").
     */
    private fun number(content: String): String {
        val d = content.toDoubleOrNull()?.takeIf(Double::isFinite) ?: return content
        if (d == Math.rint(d) && d >= -9.223372036854775808E18 && d < 9.223372036854775808E18) return d.toLong().toString()
        val shortest = BigDecimal(d.toString())
        val size = kotlin.math.abs(d)
        if (size >= 1e-4 && size <= 1.8014398509481984E16) return shortest.stripTrailingZeros().toPlainString()
        val digits = shortest.unscaledValue().abs().toString().trimEnd('0').ifEmpty { "0" }
        val exponent = shortest.precision() - shortest.scale() - 1
        val mantissa = digits.take(1) + if (digits.length > 1) "." + digits.drop(1) else ""
        return (if (d < 0) "-" else "") + mantissa + "e" + (if (exponent < 0) "-" else "+") + kotlin.math.abs(exponent).toString().padStart(2, '0')
    }

    /** Nested JSON on one line, keys sorted, slashes as they are (iOS's encoder with `sortedKeys`, `withoutEscapingSlashes`). */
    private fun json(value: JsonElement): String = when (value) {
        is JsonObject -> value.keys.sorted().joinToString(",", "{", "}") { JsonPrimitive(it).toString() + ":" + json(value.getValue(it)) }
        is JsonArray -> value.joinToString(",", "[", "]") { json(it) }
        JsonNull -> "null"
        is JsonPrimitive -> if (value.isString) value.toString() else scalar(value)
    }

    /** The start of a long value, never splitting a character in two. */
    private fun capped(s: String): String =
        if (s.codePointCount(0, s.length) <= VALUE_CAP) s else s.substring(0, s.offsetByCodePoints(0, VALUE_CAP))
}

/** A tool's name when no table has words for it: another server's MCP tool as "<Server> · <Tool>", any other in words. */
fun toolWords(name: String): String = ToolNaming.mcpLabel(name) ?: ToolNaming.humanize(toolBaseName(name))
