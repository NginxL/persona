package persona.feature.settings

import android.graphics.Bitmap
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.width
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.asAndroidBitmap
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.test.captureToImage
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.performClick
import androidx.compose.ui.unit.dp
import androidx.test.ext.junit.runners.AndroidJUnit4
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.core.contracts.JsonCodec
import persona.core.contracts.api.ExtensionTile
import persona.core.contracts.api.ExtensionView
import persona.core.contracts.api.ExtensionViewBadge
import persona.core.contracts.api.ExtensionViewItem
import persona.core.contracts.api.ExtensionViewSection
import persona.core.contracts.generated.ExtensionAction
import persona.core.contracts.generated.ExtensionBadge
import persona.core.contracts.generated.ExtensionGauge
import persona.core.contracts.generated.ExtensionGraphEdge
import persona.core.contracts.generated.ExtensionGraphNode
import persona.core.contracts.generated.ExtensionPush
import persona.core.designsystem.AppStyle
import persona.core.designsystem.PersonaTheme
import persona.core.designsystem.Tokens
import java.io.File

/**
 * UI versions 9 and 10, rendered with native Robolectric graphics in every style, light and dark. The large fixture is
 * the same 200-node tree as the contract's mock page. PNGs go to `build/extension-graph-shots/` (or -Dpersona.shots).
 */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "w400dp-h1000dp-mdpi")
class ExtensionGraphShotsTest {
    @get:Rule val rule = createComposeRule()

    private enum class Scene { GaugePage, GaugeCompact, GraphPage, GraphLayered, GraphCompact, LargeTree }

    private val handle = ExtensionViewHandle({ _, _ -> }, { _, _ -> }, {}, {})
    private val state = ExtensionViewState()
    private val rings = listOf(
        ExtensionTile(key = "five_hours", title = "5 hours", value = "4.69", gauge = ExtensionGauge(0.94, "USD", "green")),
        ExtensionTile(key = "week", title = "This week", value = "38.20", gauge = ExtensionGauge(0.76, "USD", "green")),
        ExtensionTile(
            key = "fable_week", title = "Fable this week", value = "0.62",
            badge = ExtensionViewBadge("Low", color = "amber"),
            gauge = ExtensionGauge(0.12, "USD", "amber"),
        ),
        ExtensionTile(key = "percent", title = "Used", gauge = ExtensionGauge(0.425, color = "blue")),
    )
    private val graph = graphExample("View.v10-work-tree.json")
    private val layeredGraph = graphExample("View.v10-plan-layered.json")
    private val largeTree = workTree()
    private val inPlaceState by lazy {
        ExtensionViewState(view = ExtensionView(sections = listOf(ExtensionViewSection(items = listOf(graph)))))
    }

    @Test fun gaugesGraphsAndTwoHundredNodeTreeInEveryStyleAndTheme() {
        assertEquals(200, largeTree.nodes?.size)
        var scene by mutableStateOf(Scene.GaugePage)
        var style by mutableStateOf(AppStyle.Glass)
        var dark by mutableStateOf(false)
        rule.setContent {
            PersonaTheme(darkTheme = dark, reduceMotion = true, style = style) {
                Box(Modifier.testTag("shot").width(400.dp).background(PersonaTheme.palette.background)) {
                    when (scene) {
                        Scene.GaugePage -> gauges(compact = false)
                        Scene.GaugeCompact -> gauges(compact = true)
                        Scene.GraphPage -> ExtensionGraph(graph, state, handle)
                        Scene.GraphLayered -> ExtensionGraph(layeredGraph, state, handle)
                        Scene.GraphCompact -> ExtensionViewColumn(inPlaceState, handle)
                        Scene.LargeTree -> ExtensionGraph(largeTree, state, handle)
                    }
                }
            }
        }
        for (nextScene in Scene.entries) {
            scene = nextScene
            for (nextStyle in AppStyle.entries) for (nextDark in listOf(false, true)) {
                style = nextStyle
                dark = nextDark
                capture(nextScene.name, nextStyle, nextDark)
            }
        }
    }

    @Test fun fullScreenUsesEveryStyleAndTheme() {
        var style by mutableStateOf(AppStyle.Glass)
        var dark by mutableStateOf(false)
        rule.setContent {
            PersonaTheme(darkTheme = dark, reduceMotion = true, style = style) {
                ExtensionGraph(graph, state, handle, Modifier.fillMaxSize())
            }
        }
        rule.onNodeWithTag("graph-open-full").performClick()
        for (nextStyle in AppStyle.entries) for (nextDark in listOf(false, true)) {
            style = nextStyle
            dark = nextDark
            capture("GraphFullScreen", nextStyle, nextDark, "graph-full-screen")
        }
    }

    @androidx.compose.runtime.Composable
    private fun gauges(compact: Boolean) {
        Column(Modifier.padding(Tokens.Space.m)) {
            ExtensionGrid(ExtensionViewItem(type = "grid", key = "rings", tiles = rings, maxColumns = 3),
                state, handle, compact = compact)
            ExtensionCarousel(ExtensionViewItem(type = "carousel", key = "carousel", tiles = rings),
                state, handle, Modifier.padding(top = Tokens.Space.m).height(200.dp), compact = compact)
        }
    }

    private fun capture(scene: String, style: AppStyle, dark: Boolean, tag: String = "shot") {
        rule.waitForIdle()
        val bitmap = rule.onNodeWithTag(tag).captureToImage().asAndroidBitmap()
        val background = bitmap.getPixel(1, 1)
        var other = 0
        for (y in 0 until bitmap.height step 7) for (x in 0 until bitmap.width step 7) {
            if (bitmap.getPixel(x, y) != background) other++
        }
        assertTrue("$scene ${style.name} dark=$dark drew nothing", other > 100)
        val name = "${scene.kebab()}-${style.name.lowercase()}-${if (dark) "dark" else "light"}.png"
        val out = File(System.getProperty("persona.shots") ?: "build/extension-graph-shots").also { it.mkdirs() }
        File(out, name).outputStream().use { bitmap.compress(Bitmap.CompressFormat.PNG, 100, it) }
    }

    private fun String.kebab() = replace(Regex("([a-z])([A-Z])"), "$1-$2").lowercase()

    private fun graphExample(name: String): ExtensionViewItem {
        val source = File(System.getProperty("persona.repo.root"), "schema/extension/examples/$name").readText()
        return JsonCodec.decodeFromString(ExtensionView.serializer(), source)
            .sections.flatMap { it.items }.single { it.type == "graph" }
    }

    private fun workTree(): ExtensionViewItem {
        val nodes = mutableListOf<ExtensionGraphNode>()
        val edges = mutableListOf<ExtensionGraphEdge>()
        val states = listOf("Done" to "positive", "Running" to "caution", "Queued" to null, "Failed" to "danger")
        fun add(id: String, title: String, parent: String? = null, node: ExtensionGraphNode.() -> ExtensionGraphNode = { this }) {
            if (nodes.size >= 200) return
            nodes += ExtensionGraphNode(
                id = id, title = title,
                action = ExtensionAction(id = "open", values = mapOf("id" to id), push = ExtensionPush(ui = "work", state = id)),
            ).node()
            if (parent != null) edges += ExtensionGraphEdge(parent, id)
        }
        add("root", "Ship the spring release") {
            copy(subtitle = "8 parts", icon = "list", badge = ExtensionBadge(text = "Running", tone = "caution"), progress = 0.3)
        }
        var step = 0
        for (part in 0 until 8) {
            val partId = "p$part"
            add(partId, "Part ${part + 1}", "root") { copy(subtitle = "6 steps", icon = "folder") }
            for (index in 0 until 6) {
                val stepId = "$partId.s$index"
                val (text, tone) = states[(part + index) % states.size]
                step++
                add(stepId, if (index < 2) {
                    if (index == 0) "Smallest change" else "Remove the cause"
                } else "Step ${index + 1}", partId) {
                    copy(
                        badge = ExtensionBadge(text = text, tone = tone), tone = tone,
                        progress = if (text == "Running") 0.5 else null,
                        group = if (index < 2) "Two ways" else null,
                    )
                }
                for (check in 0 until (part + index) % 5) {
                    add("$stepId.c$check", "Check ${check + 1}", stepId) {
                        copy(subtitle = if (check % 2 == 0) "Unit tests" else "Review")
                    }
                }
            }
        }
        var followUp = 0
        while (nodes.size < 200) {
            add("p${followUp % 8}.s5.x${followUp / 8}", "Follow-up ${followUp + 1}", "p${followUp % 8}.s5")
            followUp++
        }
        assertEquals(48, step)
        return ExtensionViewItem(
            type = "graph", title = "Work", key = "work", nodes = nodes, edges = edges,
            folded = listOf("p6"), selected = "p2.s3",
        )
    }
}
