package persona.core.contracts

import javax.xml.parsers.DocumentBuilderFactory

/**
 * [CoreText.words] before the app sets it: unit tests run in Simplified Chinese (docs/android/android.md "Internationalization"), and
 * modules that apply the `persona.coretext.zh` build plugin get core/designsystem's `values-zh/strings_core.xml` on their
 * test classpath, so their expected text keeps reading as the app shows it. Without that file (the app before it sets the
 * words, and the tests of every other module) it is English.
 */
internal object CoreTextFallback {
    private val table: Map<String, String>? by lazy {
        runCatching {
            val stream = CoreText::class.java.getResourceAsStream("/strings_core.xml") ?: return@runCatching null
            val root = stream.use { DocumentBuilderFactory.newInstance().newDocumentBuilder().parse(it).documentElement }
            val out = mutableMapOf<String, String>()
            val nodes = root.childNodes
            for (i in 0 until nodes.length) {
                val node = nodes.item(i) as? org.w3c.dom.Element ?: continue
                val text = if (node.tagName == "plurals") node.getElementsByTagName("item").item(0).textContent else node.textContent
                out[node.getAttribute("name")] = text.replace("\\n", "\n").replace("\\'", "'").replace("\\\"", "\"")
            }
            out
        }.getOrNull()
    }

    fun words(text: CoreText): String {
        val strings = table ?: return text.english()
        fun arg(value: Any) = if (value is CoreText) words(value) else value
        return when (text.key) {
            CoreText.Key.Verbatim -> text.args.first().toString()
            CoreText.Key.Joined -> text.args.drop(1).map { words(it as CoreText) }.filter(String::isNotEmpty)
                .joinToString(arg(text.args.first()).toString())
            CoreText.Key.CompactCount -> {
                val count = (text.args.first() as Number).toLong()
                val unit = strings["designsystem_core_compact_unit"]?.toLongOrNull() ?: 10_000
                compactCount(count, unit, { String.format(strings.getValue("designsystem_core_compact_count"), it) },
                    strings["designsystem_core_compact_millions"]?.let { millions -> { units: String -> String.format(millions, units) } })
            }
            else -> {
                val name = "designsystem_core_" + text.key.name.replace(Regex("(?<!^)(?=[A-Z])"), "_").lowercase()
                val format = strings[name] ?: return text.english()
                if (text.args.isEmpty()) format else String.format(java.util.Locale.ROOT, format, *text.args.map(::arg).toTypedArray())
            }
        }
    }
}
