package persona.core.contracts

import kotlinx.serialization.json.Json
import kotlinx.serialization.json.jsonArray
import kotlinx.serialization.json.jsonObject
import org.junit.Assert.*
import org.junit.Test
import persona.core.contracts.api.ExtensionCatalog
import persona.core.contracts.api.ExtensionMarket
import persona.core.contracts.api.ExtensionUiPlace

// docs/extensions/extensions.md "UI slots" "Badges": the hub pushes a nav page's badge as an extension_ui event (plugin_ui before #1844,
// both since); null clears it.
class ExtensionUiContractTest {
    @Test fun aBadgeEventDecodes() {
        val event = ServerEvent.decode("""{"type":"extension_ui","extension":"example-gateway","id":"usage","badge":{"text":"3","tone":"danger"}}""")
        assertEquals(ExtensionUiEvent("example-gateway", "usage", persona.core.contracts.api.ExtensionViewBadge("3", "danger")), event)
        val legacy = ServerEvent.decode("""{"type":"plugin_ui","plugin":"example-gateway","id":"usage","badge":{"text":"3","tone":"danger"}}""")
        assertEquals(event, legacy)
        val cleared = ServerEvent.decode("""{"type":"plugin_ui","plugin":"example-gateway","id":"usage","badge":null}""") as ExtensionUiEvent
        assertNull(cleared.badge)
        assertNull((ServerEvent.decode("""{"type":"extension_ui","extension":"example-gateway","id":"usage"}""") as ExtensionUiEvent).badge)
        // Without the extension or the id it is not one.
        assertNull(ServerEvent.decode("""{"type":"plugin_ui","id":"usage"}"""))
        assertNull(ServerEvent.decode("""{"type":"extension_ui","id":"usage"}"""))
        // ui.invalidate (UI version 3): no id is all of the extension's; a badge must name its UI.
        val all = ServerEvent.decode("""{"type":"extension_ui","extension":"notes","invalidate":true}""") as ExtensionUiEvent
        assertTrue(all.invalidate)
        assertNull(all.id)
        assertNull(ServerEvent.decode("""{"type":"extension_ui","extension":"notes","badge":{"text":"1"}}"""))
    }

    // UI version 3's parts of a view (#1850): search, filter, "Load more", a push on an action, pop / push in an answer.
    @Test fun aVersionThreeView() {
        val json = Json(JsonCodec) {}
        val view = json.decodeFromString(persona.core.contracts.api.ExtensionView.serializer(), """{"title":"笔记",
            "search":{"placeholder":"搜笔记","value":"周","action":{"id":"search"}},
            "filter":{"name":"folder","choices":[{"value":"all","title":"全部"},{"value":"work"}],"value":"all","action":{"id":"folder"}},
            "sections":[{"more":{"action":{"id":"more"}},"items":[
              {"type":"row","title":"周报","key":"n1","icon":"doc","avatar":{"text":"周"},"action":{"id":"open","push":{"state":"note=n1","title":"周报"}}},
              {"type":"table","columns":[{"title":"名称"},{"title":"大小","align":"end"}],"rows":[["a","1 KB"]],"caption":"共 1 个"},
              {"type":"markdown","text":"**粗**"},{"type":"empty","title":"没有了","icon":"list"},{"type":"loading"}]}]}""")
        assertEquals("周", (view.search!!.value).zhAny())
        assertEquals("folder", (view.filter!!.name).zhAny())
        assertEquals("more", (view.sections[0].more!!.action.id).zhAny())
        val row = view.sections[0].items[0]
        assertEquals("n1", (row.key).zhAny())
        assertEquals("note=n1", (row.action!!.push!!.state).zhAny())
        assertEquals("end", (view.sections[0].items[1].columns!![1].align).zhAny())
        assertEquals(listOf(listOf("a", "1 KB")), view.sections[0].items[1].rows)
        val back = json.decodeFromString(persona.core.contracts.api.ExtensionUiResult.serializer(), """{"pop":true,"toast":"已保存"}""")
        assertTrue(back.pop)
    }

    // The schema's own example of every UI version 3 component (schema/extension/examples/View.components-v3.json) reads
    // into the app's types with nothing left behind: every key of every component lands in a field.
    @Test fun theSchemasVersionThreeExampleReads() {
        val file = java.io.File(System.getProperty("persona.repo.root"), "schema/extension/examples/View.components-v3.json")
        val raw = JsonCodec.parseToJsonElement(file.readText())
        val view = JsonCodec.decodeFromJsonElement(persona.core.contracts.api.ExtensionView.serializer(), raw)
        val strict = Json { ignoreUnknownKeys = false }
        // Items: every key the schema writes is one the app reads.
        raw.jsonObject.getValue("sections").jsonArray.flatMap { it.jsonObject.getValue("items").jsonArray }.forEach {
            val _ = strict.decodeFromJsonElement(persona.core.contracts.api.ExtensionViewItem.serializer(), it)
        }
        assertTrue(view.sections.flatMap { it.items }.map { it.type }.containsAll(listOf("markdown", "table", "empty", "loading", "row")))
    }

    // UI version 8 (#9158): the schema's dashboard and section head examples read with every key of every section, and of
    // its link, in a field of the app's types.
    @Test fun theSchemasVersionEightExamplesRead() {
        val strict = Json { ignoreUnknownKeys = false }
        listOf("View.v8-today.json", "View.v8-section-header.json").forEach { name ->
            val raw = JsonCodec.parseToJsonElement(java.io.File(System.getProperty("persona.repo.root"), "schema/extension/examples/$name").readText())
            raw.jsonObject.getValue("sections").jsonArray.forEach {
                val _ = strict.decodeFromJsonElement(persona.core.contracts.api.ExtensionViewSection.serializer(), it)
            }
        }
        val header = JsonCodec.decodeFromJsonElement(persona.core.contracts.api.ExtensionView.serializer(),
            JsonCodec.parseToJsonElement(java.io.File(System.getProperty("persona.repo.root"), "schema/extension/examples/View.v8-section-header.json").readText()))
        val section = header.sections.single()
        assertEquals("today", section.key)
        assertEquals("chart", section.icon)
        assertEquals("History", section.link!!.title)
        assertEquals(mapOf("range" to "month"), section.link!!.action.values)
        assertNull(section.span)
        assertNull(section.columns)
    }

    // UI versions 9 and 10 (#9337, #9727): gauges and graphs also read through the handwritten ordinary-view DTOs.
    @Test fun theSchemasVersionNineAndTenExamplesRead() {
        val strict = Json { ignoreUnknownKeys = false }
        val items = listOf(
            "View.v9-gauge-percent.json", "View.v9-quota.json",
            "View.v10-plan-layered.json", "View.v10-work-tree.json",
        ).flatMap { name ->
            val raw = JsonCodec.parseToJsonElement(
                java.io.File(System.getProperty("persona.repo.root"), "schema/extension/examples/$name").readText(),
            )
            raw.jsonObject.getValue("sections").jsonArray.flatMap { section ->
                strict.decodeFromJsonElement(persona.core.contracts.api.ExtensionViewSection.serializer(), section).items
            }
        }
        val gauges = items.flatMap { it.tiles.orEmpty() }.mapNotNull { it.gauge }
        assertEquals(5, gauges.size)
        assertEquals(1.0, gauges.maxOf { it.ratio }, 0.0)
        val graphs = items.filter { it.type == "graph" }
        assertEquals(setOf("plan", "work"), graphs.map { it.key }.toSet())
        assertEquals(listOf("layered", null), graphs.map { it.layout })
        assertEquals(listOf(6, 12), graphs.map { it.nodes.orEmpty().size })
        assertEquals(listOf("docs"), graphs.single { it.key == "work" }.folded)
    }

    // The new routes answer extension / extension_name / extensions, the old ones plugin / plugin_name / plugins.
    @Test fun bothKeysDecode() {
        val json = Json(JsonCodec) {}
        val current = json.decodeFromString(ExtensionUiPlace.serializer(),
            """{"extension":"example-usage","extension_name":"Example Usage","id":"usage","title":"用量","placement":"nav_page"}""")
        val legacy = json.decodeFromString(ExtensionUiPlace.serializer(),
            """{"plugin":"example-usage","plugin_name":"Example Usage","id":"usage","title":"用量","placement":"nav_page"}""")
        assertEquals("example-usage", (current.extension).zhAny())
        assertEquals("Example Usage", (current.extensionName).zhAny())
        assertEquals(current, legacy)
        assertEquals(3, json.decodeFromString(ExtensionMarket.serializer(), """{"id":"m","extensions":3}""").extensions)
        assertEquals(3, json.decodeFromString(ExtensionMarket.serializer(), """{"id":"m","plugins":3}""").extensions)
        val catalog = """{"markets":[],"plugins":[{"market":"m","id":"x","name":"X","version":"1.0.0"}]}"""
        assertEquals(1, json.decodeFromString(ExtensionCatalog.serializer(), catalog).extensions.size)
        val signed = json.decodeFromString(ExtensionMarket.serializer(), """{"id":"m","signature":{"state":"verified","key_id":"32ed46cdbe349656"}}""")
        assertTrue(signed.signature!!.verified)
        // The hub says `signed` (server/internal/extension/market_sig.go, #4812); `verified` stays accepted.
        val hubSigned = json.decodeFromString(ExtensionMarket.serializer(), """{"id":"m","signature":{"state":"signed"}}""").signature!!
        assertTrue(hubSigned.verified && !hubSigned.failed)
        assertTrue(json.decodeFromString(ExtensionMarket.serializer(), """{"id":"m","signature":{"state":"key_changed"}}""").signature!!.failed)
    }
}
