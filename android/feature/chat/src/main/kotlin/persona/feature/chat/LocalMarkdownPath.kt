package persona.feature.chat

import java.net.URLDecoder
import java.util.Locale

internal data class LocalPathTarget(val path: String, val line: Int? = null)

/**
 * Classifies and resolves local-path Markdown destinations in chat messages (#12288, aligned with iOS PR #12102).
 *
 * Accepts absolute (`/…`, `~/…`, Windows drive `C:\…` / `C:/…`) and session `cwd`-relative paths.
 * Strictly excludes `http:`, `https:`, `//…`, `file:`, `data:`, `blob:`, `mailto:`, custom URI schemes,
 * in-page anchors (`#…`), and hub internal API routes (`/api/v1/…`).
 */
internal object LocalMarkdownPath {
    private val windowsDrive = Regex("^[A-Za-z]:[\\\\/]")
    private val windowsRoot = Regex("^[A-Za-z]:[\\\\/]|^\\\\\\\\")
    private val uriScheme = Regex("^[A-Za-z][A-Za-z0-9+.-]*:")
    private val lineFragment = Regex("^L(\\d{1,7})(?:-L?\\d{1,7})?$", RegexOption.IGNORE_CASE)

    fun isWindowsDrivePath(value: String): Boolean = windowsDrive.containsMatchIn(value.trim())

    fun parseLocalPath(rawDestination: String): LocalPathTarget? {
        val raw = rawDestination.trim().removeSurrounding("<", ">").trim()
        if (raw.isEmpty() || raw.startsWith("#") || raw.startsWith("//")) return null
        val lower = raw.lowercase(Locale.ROOT)
        if (lower.startsWith("/api/v1/")) return null
        if (!isWindowsDrivePath(raw) && uriScheme.containsMatchIn(raw)) return null

        val fragment = raw.substringAfter('#', "")
        val line = lineFragment.matchEntire(fragment)?.groupValues?.getOrNull(1)?.toIntOrNull()?.takeIf { it > 0 }
        val beforeFragment = raw.substringBefore('#').substringBefore('?')
        if (beforeFragment.isEmpty()) return null
        val decoded = decodePercent(beforeFragment)
        if (decoded.isEmpty()) return null
        return LocalPathTarget(decoded, line)
    }

    fun resolve(path: String, cwd: String): String {
        val trimmed = path.trim()
        if (trimmed.isEmpty()) return cwd
        if (windowsRoot.containsMatchIn(trimmed)) {
            val unc = trimmed.startsWith("\\\\")
            val segs = (if (unc) trimmed.removePrefix("\\\\") else trimmed).split('/', '\\').filter { it.isNotEmpty() }
            val rootCount = if (unc) 2 else 1
            if (segs.size < rootCount) return trimmed
            val root = if (unc) "\\\\${segs[0]}\\${segs[1]}" else "${segs[0]}\\"
            return resolveWindows(root, segs.drop(rootCount).joinToString("\\"))
        }
        if (trimmed.startsWith("/") || trimmed == "~" || trimmed.startsWith("~/")) {
            return normalizeUnix(trimmed)
        }
        if (windowsRoot.containsMatchIn(cwd)) {
            return resolveWindows(cwd, trimmed)
        }
        if (cwd.isBlank()) return normalizeUnix(trimmed)
        val joined = if (cwd.endsWith("/")) cwd + trimmed else "$cwd/$trimmed"
        return normalizeUnix(joined)
    }

    fun fileName(path: String): String {
        val trimmed = path.trimEnd('/', '\\')
        if (trimmed.isEmpty()) return path
        val slash = maxOf(trimmed.lastIndexOf('/'), trimmed.lastIndexOf('\\'))
        return if (slash >= 0 && slash + 1 < trimmed.length) trimmed.substring(slash + 1) else trimmed
    }

    private fun resolveWindows(base: String, rel: String): String {
        val unc = base.startsWith("\\\\")
        val out = ArrayDeque<String>()
        for (part in base.split('/', '\\')) {
            if (part.isNotEmpty()) out.addLast(part)
        }
        val root = if (unc) 2 else 1
        for (part in rel.split('/', '\\')) {
            when (part) {
                "", "." -> Unit
                ".." -> if (out.size > root) out.removeLast()
                else -> out.addLast(part)
            }
        }
        val body = out.joinToString("\\")
        return (if (unc) "\\\\" else "") + body + if (out.size == root && !unc) "\\" else ""
    }

    private fun normalizeUnix(path: String): String {
        val home = path == "~" || path.startsWith("~/")
        val absolute = path.startsWith("/")
        val parts = ArrayDeque<String>()
        val source = if (home) path.removePrefix("~") else path
        for (part in source.split('/')) {
            when (part) {
                "", "." -> Unit
                ".." -> {
                    if (parts.isNotEmpty() && parts.last() != "..") parts.removeLast()
                    else if (!absolute && !home) parts.addLast("..")
                }
                else -> parts.addLast(part)
            }
        }
        val body = parts.joinToString("/")
        return when {
            home -> if (body.isEmpty()) "~" else "~/$body"
            absolute -> "/$body"
            else -> body
        }
    }

    private fun decodePercent(value: String): String =
        if ('%' !in value) value else runCatching { URLDecoder.decode(value.replace("+", "%2B"), "UTF-8") }.getOrDefault(value)
}
