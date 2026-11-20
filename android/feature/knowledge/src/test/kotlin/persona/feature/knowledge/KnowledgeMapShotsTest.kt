package persona.feature.knowledge

import android.graphics.Bitmap
import androidx.activity.ComponentActivity
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.width
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.asAndroidBitmap
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.test.ExperimentalTestApi
import androidx.compose.ui.test.captureToImage
import androidx.compose.ui.test.junit4.createAndroidComposeRule
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.unit.dp
import androidx.navigation3.runtime.NavKey
import androidx.test.ext.junit.runners.AndroidJUnit4
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.put
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.core.contracts.HubScope
import persona.core.contracts.api.*
import persona.core.designsystem.*
import persona.core.navigation.AppTab
import persona.core.navigation.Navigator
import java.io.File

/**
 * The knowledge map (topics and projects) and a topic's page with its points, in the three styles, light and dark. Written as PNGs to
 * `build/i18n-shots/` (or `-Dpersona.shots=<dir>`) to look at.
 */
@OptIn(ExperimentalTestApi::class)
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
class KnowledgeMapShotsTest {
    @get:Rule val rule = createAndroidComposeRule<ComponentActivity>()

    private fun out(): File = File(System.getProperty("persona.shots") ?: "build/i18n-shots").also { it.mkdirs() }

    private val variants = listOf(AppStyle.Glass, AppStyle.Brief, AppStyle.Board).flatMap { s -> listOf(false, true).map { s to it } }

    @Config(sdk = [35], qualifiers = "en-w400dp-h1100dp-xhdpi")
    @Test fun topicsMap() = mapShots("topics", MapSide.Topics, "Gardening")

    @Config(sdk = [35], qualifiers = "en-w400dp-h1100dp-xhdpi")
    @Test fun projectsMap() = mapShots("projects", MapSide.Projects, "Weather App")

    @Config(sdk = [35], qualifiers = "zh-w400dp-h1100dp-xhdpi")
    @Test fun topicsMapChinese() = mapShots("topics-zh", MapSide.Topics, "Gardening", only = listOf(AppStyle.Glass to false))

    @Config(sdk = [35], qualifiers = "ja-w400dp-h1100dp-xhdpi")
    @Test fun topicsMapJapanese() = mapShots("topics-ja", MapSide.Topics, "Gardening", only = listOf(AppStyle.Glass to false))

    @Config(sdk = [35], qualifiers = "en-w400dp-h1500dp-xhdpi")
    @Test fun topicPage() {
        var style by mutableStateOf(AppStyle.Glass)
        var dark by mutableStateOf(false)
        val model = KnowledgeDetailViewModel(KnowledgeKind.Topics, "gardening", connection, MapNoSender)
        rule.setContent {
            PersonaTheme(darkTheme = dark, reduceMotion = true, style = style) {
                Box(Modifier.testTag("shot").width(400.dp).height(1460.dp).background(PersonaTheme.palette.background)) {
                    KnowledgeDetailScreen(model, MapNoNavigator, openLinkInApp = {})
                }
            }
        }
        for ((s, d) in variants) {
            style = s; dark = d
            rule.waitUntilExactlyOneExists(androidx.compose.ui.test.hasText("Overwatering rots the roots"), 10_000)
            capture("knowledge-topic-page-en-${s.name.lowercase()}-${if (d) "dark" else "light"}.png")
        }
    }

    private fun mapShots(name: String, side: MapSide, expected: String, only: List<Pair<AppStyle, Boolean>> = variants) {
        var style by mutableStateOf(AppStyle.Glass)
        var dark by mutableStateOf(false)
        val model = KnowledgeMapViewModel(connection).also { it.side(side) }
        rule.setContent {
            PersonaTheme(darkTheme = dark, reduceMotion = true, style = style) {
                Box(Modifier.testTag("shot").width(400.dp).height(1060.dp).background(PersonaTheme.palette.background)) {
                    KnowledgeMapScreen(model, MapNoNavigator, "assistant")
                }
            }
        }
        for ((s, d) in only) {
            style = s; dark = d
            rule.waitUntilExactlyOneExists(androidx.compose.ui.test.hasText(expected), 10_000)
            val language = if (name.endsWith("-zh")) "zh" else if (name.endsWith("-ja")) "ja" else "en"
            capture("knowledge-map-${name.removeSuffix("-zh").removeSuffix("-ja")}-$language-${s.name.lowercase()}-${if (d) "dark" else "light"}.png")
        }
    }

    private fun capture(file: String) {
        rule.waitForIdle()
        val bitmap = rule.onNodeWithTag("shot").captureToImage().asAndroidBitmap()
        File(out(), file).outputStream().use { bitmap.compress(Bitmap.CompressFormat.PNG, 100, it) }
    }

    private val connection = object : KnowledgeConnection { override val current = MutableStateFlow<KnowledgeRepository?>(DemoMap) }
}

private object MapNoNavigator : Navigator {
    override fun navigate(key: NavKey) = Unit
    override fun back() = Unit
    override fun selectTab(tab: AppTab) = Unit
}

private object MapNoSender : MainConversationSender {
    override val outbox = MutableStateFlow(emptyList<OutboxMessage>())
    override suspend fun sendToMain(text: String, clientMessageId: String, draft: DraftSnapshot?) = Unit
}

/** Made-up topics with points in every kind, and projects of every status. */
private object DemoMap : KnowledgeRepository {
    override val scope = HubScope("demo", "shots")

    private val topics = listOf(
        KnowledgeEntry("topics", "gardening", "Gardening", "Balcony vegetables and herbs", extra = "7"),
        KnowledgeEntry("topics", "coffee", "Coffee", "Brewing at home", extra = "4"),
        KnowledgeEntry("topics", "travel", "Travel", "Trips planned this year", extra = "3"),
    )
    private val projects = listOf(
        KnowledgeEntry("projects", "weather", "Weather App", "Widgets and a watch face for the forecast", "P0", 92, listOf("ios"), "2026-11-04T09:30:00+08:00", "short · active"),
        KnowledgeEntry("projects", "site", "Website", "The landing page and docs", "P2", 45, listOf("web"), "2026-11-01T18:05:00+08:00", "long · active"),
        KnowledgeEntry("projects", "launch", "Beta launch", "Shipped to testers", "P1", 60, emptyList(), "2026-10-20T10:00:00+08:00", "short · done"),
        KnowledgeEntry("projects", "garden-plan", "Balcony plan", null, "P3", 20, emptyList(), null, "short · paused"),
    )

    private fun point(id: String, kind: String, text: String, why: String? = null, path: List<String>? = null, day: String? = "2026-11-02") =
        TopicPoint(id, path, kind, text, why, listOf(TopicSource("chats", "c1", "2026-11-01")), day)

    private val gardening = listOf(
        point("g1", "concept", "Tomatoes need six hours of direct sun"),
        point("g2", "pitfall", "Overwatering rots the roots", "The soil stays wet and the roots cannot breathe"),
        point("g3", "decision", "Use raised pots on the south side", "They drain better and get the most light"),
        point("g4", "practice", "Water in the morning", path = listOf("Watering")),
        point("g5", "question", "Which herbs survive the winter outside?", path = listOf("Herbs")),
        point("g6", "fact", "Basil stops growing below 10 degrees", path = listOf("Herbs", "Basil")),
        point("g7", "practice", "Pinch the flowers to keep the leaves", path = listOf("Herbs", "Basil")),
    )
    private val coffee = listOf(
        point("c1", "practice", "Grind right before brewing"),
        point("c2", "fact", "Water at 93 degrees extracts best"),
        point("c3", "decision", "Buy whole beans in small bags", "They stay fresh longer", path = listOf("Beans")),
        point("c4", "pitfall", "Boiling water burns the grounds", "It over-extracts and turns bitter", path = listOf("Brewing")),
    )
    private val travel = listOf(
        point("t1", "decision", "Travel in spring", "Fewer crowds and mild weather"),
        point("t2", "question", "Is a rail pass worth it for ten days?"),
        point("t3", "fact", "Museums close on Mondays", path = listOf("Planning")),
    )

    override suspend fun list(kind: KnowledgeKind, tag: String?) = KnowledgeList(when (kind) {
        KnowledgeKind.Topics -> topics
        KnowledgeKind.Projects -> projects
        else -> emptyList()
    }, emptyList())

    override suspend fun detail(kind: KnowledgeKind, id: String): KnowledgeDetail = when (kind) {
        KnowledgeKind.Topics -> KnowledgeDetail("topics", id, "~/.persona/data/topics/$id",
            meta = buildJsonObject { put("name", topics.first { it.id == id }.name) },
            points = when (id) { "gardening" -> gardening; "coffee" -> coffee; else -> travel },
            links = listOf(KnowledgeEntry("chats", "c1", "Balcony chat", "Plants for the south side")))
        else -> KnowledgeDetail(kind.wire, id, "~/.persona/data/projects/$id",
            meta = projects.first { it.id == id }.let { entry ->
                buildJsonObject {
                    put("name", entry.name); put("kind", "short"); put("status", entry.extra.orEmpty().substringAfter(" · "))
                    entry.importance?.let { put("importance", it) }
                }
            },
            summary = "# ${projects.first { it.id == id }.name}\n\n- Goal: ship the first version\n- Latest: in review",
            log = listOf("- 2026-11-03 10:00 · Agent · laptop · Step done"),
            links = listOf(KnowledgeEntry("people", "alex", "Alex", "Designer")))
    }

    override suspend fun search(query: String) = emptyList<KnowledgeHit>()
}
