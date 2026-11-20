package persona.feature.settings

import java.math.BigDecimal
import kotlinx.serialization.json.*
import persona.core.contracts.api.ExtensionViewAction
import persona.core.contracts.stream.*

/** Field-level renderer support is checked before commit, independently of the negotiated wire component tags. */
internal object NativeExtensionProfile {
    val check: (JsonObject) -> List<String> = { view -> gaps(view) }
    private val unsupported = setOf("spark", "icon_color", "color", "center", "rising", "line_url", "pages", "earliest", "latest")
    // A field-profile limit, not a rendered size: avoid unbounded lazy-item constraints from arbitrary media ratios.
    private val autoImageAspect = 0.25..4.0
    fun gaps(view: JsonObject): List<String> {
        val gaps = linkedSetOf<String>()
        fun visit(value: JsonElement, path: String) {
            when (value) {
                is JsonObject -> {
                    val type = value["type"]?.jsonPrimitive?.content
                    val isAction = "id" in value && value.keys.all { it in setOf("id", "confirm", "submit", "reply", "open", "pop", "push", "values") }
                    if (type != null && type !in NativeExtensionStreamAdapter.Components) gaps.add("$path.type")
                    // UI version 8: a section's icon colour is drawn (a row's, a metric's and a tile's are not yet)
                    val section = path == "view.sections[]"
                    val gauge = path.endsWith(".gauge")
                    value.keys.filter {
                        it in unsupported && !(section && it == "icon_color") && !(gauge && it == "color")
                    }.forEach { gaps.add("$path.$it") }
                    if (type == "metric" && "icon" in value) gaps.add("$path.icon")
                    value["icon"]?.jsonPrimitive?.content?.let { icon ->
                        if (icon !in setOf("plugin", "extension") && ExtensionIcons.of(icon) == ExtensionIcons.of("extension")) gaps.add("$path.icon")
                    }
                    if (type == "text" && value["style"]?.jsonPrimitive?.content in setOf("title", "headline")) gaps.add("$path.style")
                    if (type == null && value["active"] == JsonPrimitive(true) && "icon" !in value && "gauge" !in value) gaps.add("$path.active")
                    if (type == "field" && value["kind"]?.jsonPrimitive?.content !in setOf("text", "number", "toggle", "choice", "multiline")) gaps.add("$path.kind")
                    if (type == "chart" && value["kind"]?.jsonPrimitive?.content !in setOf("line", "bar")) gaps.add("$path.kind")
                    if ("points" in value) value["points"]?.jsonArray?.forEach { point ->
                        if (point.jsonObject.keys.any { it in setOf("open", "high", "low", "close", "volume") }) gaps.add("$path.points.ohlc")
                    }
                    if (isAction && "reply" in value) gaps.add("$path.reply")
                    if (isAction && value["open"]?.jsonPrimitive?.content?.let { !ExtensionViewText.isHttps(it) } == true) gaps.add("$path.open")
                    if ("gauge" in value && "image" in value) gaps.add("$path.image")
                    if ("src" in value || "image" in value) {
                        val src = (value["src"] ?: value["image"]) as? JsonObject
                        // No concatenating relative hub paths, forwarding credentials or guessing a ref endpoint.
                        if (src != null && ("ref" in src || "path" in src || "url" !in src)) gaps.add("$path.media.hub_ref")
                        if (type == "image" && value["aspect"] == JsonPrimitive("auto")) {
                            val width = src?.get("width")?.jsonPrimitive?.doubleOrNull
                            val height = src?.get("height")?.jsonPrimitive?.doubleOrNull
                            // Intrinsic loaded-image aspect is not implemented; never guess or allocate a giant frame.
                            if (width == null || height == null || width / height !in autoImageAspect) gaps.add("$path.media.aspect")
                        }
                    }
                    if ("avatar" in value && type != "row" && "url" in value.getValue("avatar").jsonObject) gaps.add("$path.avatar.url")
                    if ("control" in value) {
                        val control = value.getValue("control").jsonObject
                        val kind = control["kind"]?.jsonPrimitive?.content
                        if (type == null && kind != "switch") gaps.add("$path.control.kind")
                        if (kind != "switch") {
                            val min = control["min"]?.jsonPrimitive?.doubleOrNull ?: 0.0
                            val max = control["max"]?.jsonPrimitive?.doubleOrNull ?: 100.0
                            val step = control["step"]?.jsonPrimitive?.doubleOrNull ?: if (kind == "slider") (max - min) / 100 else 1.0
                            if (!min.isFinite() || !max.isFinite() || !min.toFloat().isFinite() || !max.toFloat().isFinite() ||
                                !(max - min).toFloat().isFinite() || min >= max || min.toFloat() >= max.toFloat() || !step.isFinite() || step.toFloat() <= 0 ||
                                !step.toFloat().isFinite() || (max - min) / step > 10000 ||
                                kind == "slider" && (min.toFloat() + step.toFloat() <= min.toFloat() || max.toFloat() - step.toFloat() >= max.toFloat())) gaps.add("$path.control.range")
                        }
                    }
                    if (type == "file_tree") value["entries"]?.jsonArray?.forEach { entry ->
                        entry.jsonObject.keys.filter { it in setOf("viewed", "generated") }.forEach { gaps.add("$path.entries.$it") }
                    }
                    if ("crumbs" in value) value.getValue("crumbs").jsonArray.lastOrNull()?.jsonObject?.let {
                        if ("action" in it) gaps.add("$path.crumbs.last.action")
                    }
                    if ("refresh_after" in value && value["refresh_after"] != JsonPrimitive(0)) gaps.add("$path.refresh_after")
                    value.forEach { (key, child) -> if (!(isAction && key == "values")) visit(child, "$path.$key") }
                }
                is JsonArray -> value.forEach { visit(it, "$path[]") }
                else -> Unit
            }
        }
        visit(view, "view")
        return gaps.toList()
    }
}

/** One display/submit default; absent numeric values start at zero clamped to the range, without rewriting raw presence. */
internal fun nativeControlValue(control: JsonObject, draft: JsonElement? = null): String {
    val kind = control["kind"]?.jsonPrimitive?.content
    if (kind == "switch") return ((draft ?: control["on"]) as? JsonPrimitive)?.content ?: "false"
    val min = control["min"]?.jsonPrimitive?.content?.toBigDecimalOrNull() ?: BigDecimal.ZERO
    val max = control["max"]?.jsonPrimitive?.content?.toBigDecimalOrNull() ?: BigDecimal(100)
    val value = ((draft ?: control["value"]) as? JsonPrimitive)?.content?.toBigDecimalOrNull() ?: BigDecimal.ZERO
    return value.coerceIn(min, max).stripTrailingZeros().toPlainString()
}

/** Capture values at the displayed press/confirmation, not when a later dialog executes. Raw tree is never rewritten. */
internal fun nativeStreamValues(state: NativeStreamState, key: String, action: ExtensionViewAction,
                                own: Map<String, String>, roots: Map<String, String>): JsonObject {
    val tree = checkNotNull(state.tree)
    fun source(node: JsonObject) = (node["control"] as? JsonObject) ?: node
    val fields = if (action.submit) tree.nodes.mapNotNull { (nodeKey, node) ->
        val input = source(node)
        val name = input["name"]?.jsonPrimitive?.content ?: return@mapNotNull null
        if (node["type"]?.jsonPrimitive?.content !in setOf("field", "composer", "segmented", "row") && "control" !in node) return@mapNotNull null
        if (input["kind"] == JsonPrimitive("secret")) return@mapNotNull null
        val value = state.drafts[nodeKey]?.value ?: input["value"] ?: input["on"]
        name to JsonPrimitive(if ("control" in node) nativeControlValue(input, state.drafts[nodeKey]?.value)
            else (value as? JsonPrimitive)?.content ?: if (input["kind"] == JsonPrimitive("toggle")) "false" else "")
    }.toMap() else emptyMap()
    val node = tree.nodes[key]
    val ownValues = own.mapKeys { (name, _) ->
        if (name == key && node?.get("type")?.jsonPrimitive?.content in setOf("field", "composer", "segmented"))
            node?.get("name")?.jsonPrimitive?.content ?: name else name
    }.mapValues { JsonPrimitive(it.value) }
    val rootValues = if (action.submit) listOf("tabs", "filter").mapNotNull { slot ->
        val control = tree.viewRaw()[slot] as? JsonObject ?: return@mapNotNull null
        val name = control["name"]?.jsonPrimitive?.content ?: return@mapNotNull null
        name to (roots[name]?.let(::JsonPrimitive) ?: control["value"] ?: JsonPrimitive(""))
    }.toMap() else emptyMap()
    return JsonObject(fields + rootValues + ownValues + action.values.orEmpty().mapValues { JsonPrimitive(it.value) })
}

/**
 * Mirrors the native list's fixed recipes, never row heights or section ordinals as callback identities. [columns] is
 * how many columns of cards the page drew (UI version 8): a columns view's grid is one item per row of cards.
 */
internal fun nativeDiffIndex(tree: NativeStreamTree, path: String, columns: Int = 1): Pair<String, Int>? {
    val view = tree.viewRaw()
    val sections = view.getValue("sections").jsonArray.map { it.jsonObject }
    val shown = persona.core.contracts.JsonCodec.decodeFromJsonElement(persona.core.contracts.api.ExtensionView.serializer(), view)
    fun diff(node: JsonObject) = node["type"] == JsonPrimitive("diff") && node["path"] == JsonPrimitive(path)
    var index = 0
    if ((view["tabs"] as? JsonObject)?.get("items")?.jsonArray?.size?.let { it >= 2 } == true) index++
    if (sections.any { s -> s.getValue("items").jsonArray.any { it.jsonObject["type"] == JsonPrimitive("diff") } }) index++
    if ("search" in view || "filter" in view) index++
    if (ExtensionColumns.grid(shown, columns)) {
        ExtensionColumns.rows(shown.sections, columns).forEach { row ->
            row.firstNotNullOfOrNull { s -> sections[s].getValue("items").jsonArray.map { it.jsonObject }.firstOrNull(::diff) }
                ?.let { return it.getValue("key").jsonPrimitive.content to index }
            index++
        }
        return null
    }
    val split = view["layout"] == JsonPrimitive("split")
    if (split && sections.any { it["pane"] == JsonPrimitive("side") }) index++
    sections.forEachIndexed { s, section ->
        if (split && section["pane"] == JsonPrimitive("side")) return@forEachIndexed
        val drawn = shown.sections.getOrNull(s)
        if (section["title"]?.jsonPrimitive?.content?.isNotBlank() == true || drawn?.let(ExtensionColumns::head) == true || s > 0) index++
        val items = section.getValue("items").jsonArray.map { it.jsonObject }
        var i = 0
        while (i < items.size) {
            val start = i
            if (nativeGrouped(items[i])) while (i < items.size && nativeGrouped(items[i])) i++ else i++
            // UI version 8: rows side by side are one block of lines
            if (drawn?.let(ExtensionColumns::sideBySide) == true && nativeGrouped(items[start])) { index++; continue }
            for (node in items.subList(start, i)) {
                if (diff(node)) return node.getValue("key").jsonPrimitive.content to index
                index++
            }
        }
        if ("more" in section) index++
        if (section["footer"]?.jsonPrimitive?.content?.isNotBlank() == true) index++
    }
    return null
}

private fun nativeGrouped(node: JsonObject): Boolean = ExtensionViewText.grouped(persona.core.contracts.api.ExtensionViewItem(
    type = node["type"]?.jsonPrimitive?.content.orEmpty(), kind = node["kind"]?.jsonPrimitive?.contentOrNull))

internal fun NativeStreamTree.viewRaw() = raw.getValue("view").jsonObject
