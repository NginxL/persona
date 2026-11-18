package persona.core.domain

import java.util.Collections

/**
 * A message split into what its row shows (docs/protocol/protocol.md "Tags in messages", iOS `UserRowText`): [text] without the
 * image notes and Claude Code's `[Image #N]` placeholders, the photo paths of its `<attached_images>` ([images],
 * thumbnails read from the session's machine), and the file-looking paths in the text ([files], chips that open them).
 */
data class MessageText(
    val text: String,
    val images: List<String> = emptyList(),
    val files: List<String> = emptyList(),
)

/** A file-looking path and its source range, for incremental consumers that partition a whole-message scan. */
data class FilePathRef(val start: Int, val end: Int, val path: String)

private val pastedTags = Regex("</?pasted_content[^>]*>")
private val imagePlaceholders = Regex("\\[Image #\\d+]\\s*")

/** `<attached_images>` and the plain-text notes older builds appended (iOS `UserBubble.notePatterns`). */
private val imageNotes = listOf(
    Regex("\\s*<attached_images>[\\s\\S]*?</attached_images>\\s*"),
    Regex("(?:\\A|\\n)\\s*相关图片：[^\\n]*\\z"), // i18n-ignore: the hub's photo note it strips
    Regex("\\s*（我从手机发来了图片，请用 Read 查看：[^）]*）\\s*\\z"), // i18n-ignore: the hub's photo note it strips
)
private val noteWords = listOf("<attached_images>", "</attached_images>", "相关图片：", "（我从手机发来了图片，请用 Read 查看：", "）") // i18n-ignore: the hub's photo note it strips

/**
 * [raw] as its row shows it. The photos a phone sends ride along as `<attached_images>` paths (what the composer
 * writes, and what Claude Code reads), so they come back here as [MessageText.images], never as text. [files] false
 * skips the path scan.
 */
fun messageText(raw: String, files: Boolean = true): MessageText {
    val unwrapped = pastedTags.replace(raw, "")
    val images = imagePaths(unwrapped)
    var shown = unwrapped
    for (note in imageNotes) shown = note.replace(shown, "\n")
    shown = imagePlaceholders.replace(shown, "").trim()
    val paths = if (files) FilePaths.find(shown).filterNot { it in images } else emptyList()
    return MessageText(shown, images, paths)
}

/** The photo paths the first image note in [text] lists: the absolute (`/…`) and home (`~/…`) ones. */
fun imagePaths(text: String): List<String> {
    for (note in imageNotes) {
        val match = note.find(text) ?: continue
        var body = match.value
        for (word in noteWords) body = body.replace(word, "\n")
        return body.split('\n', '、').map { it.trim() }.filter { it.startsWith("/") || it.startsWith("~") }
    }
    return emptyList()
}

/**
 * File-looking paths in a message (iOS `FilePaths`): `~/x`, `/x/y`, or a relative path with a folder and an
 * extension. Links, `scheme://` fragments and model names such as `deepseek/v4.1-flash` are not paths. A path ends
 * at the first character that cannot be in one (ASCII only, like links), so Chinese text right after it stays out.
 */
object FilePaths {
    private val pattern = Regex(
        "(?<![\\w/:.\\-~])(~(?:/[\\w.@+\\-]+)+/?|/[\\w.@+\\-]+(?:/[\\w.@+\\-]+)+/?|" +
            "[\\w.@\\-]+(?:/[\\w.@+\\-]+)+\\.[A-Za-z][A-Za-z0-9]{0,7})(?![\\w/\\-])",
    )
    private const val LIMIT = 6
    private val memo: MutableMap<String, List<String>> = Collections.synchronizedMap(
        object : LinkedHashMap<String, List<String>>(256, 0.75f, true) {
            override fun removeEldestEntry(eldest: MutableMap.MutableEntry<String, List<String>>?): Boolean = size > 2000
        },
    )

    /** The paths in [text] in order, without repeats, at most six; cached by text (rows redraw all the time). */
    fun find(text: String): List<String> = memo[text] ?: findDetailed(text).map(FilePathRef::path).also { memo[text] = it }

    /** The same matches as [find], with source positions for settled/tail partitioning. */
    fun findDetailed(text: String): List<FilePathRef> {
        val out = ArrayList<FilePathRef>()
        val paths = ArrayList<String>()
        for (match in pattern.findAll(text)) {
            var path = match.groupValues[1]
            while (path.isNotEmpty() && path.last() in ".,;:)") path = path.dropLast(1)
            val start = match.range.first
            val before = if (start >= 3) text.substring(start - 3, start) else ""
            if (before.endsWith("://") || path.startsWith("//") || path.length < 4 || path in paths) continue
            if (path.startsWith("/") && path.split('/').count { it.isNotEmpty() } < 2) continue
            paths += path
            out += FilePathRef(start, match.range.last + 1, path)
            if (out.size >= LIMIT) break
        }
        return out
    }
}
