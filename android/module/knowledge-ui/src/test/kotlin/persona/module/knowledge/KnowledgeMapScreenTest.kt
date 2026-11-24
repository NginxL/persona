package persona.module.knowledge

import persona.core.navigation.KnowledgeDetailRoute
import androidx.activity.ComponentActivity
import androidx.compose.ui.test.ExperimentalTestApi
import androidx.compose.ui.test.hasText
import androidx.compose.ui.test.junit4.createAndroidComposeRule
import androidx.compose.ui.test.onNodeWithContentDescription
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.navigation3.runtime.NavKey
import androidx.test.ext.junit.runners.AndroidJUnit4
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.put
import org.junit.Assert.assertEquals
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.core.contracts.HubScope
import persona.core.contracts.api.*
import persona.core.designsystem.PersonaTheme
import persona.core.navigation.AppTab
import persona.core.navigation.Navigator
import java.io.IOException

/** What the knowledge map and a topic's page show and offer. */
@OptIn(ExperimentalTestApi::class)
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "en-w400dp-h1100dp-xhdpi")
class KnowledgeMapScreenTest {
    @get:Rule val rule = createAndroidComposeRule<ComponentActivity>()

    private class Repo : KnowledgeRepository {
        override val scope = HubScope("demo", "map-screen")
        var fail = false
        override suspend fun list(kind: KnowledgeKind, tag: String?): KnowledgeList {
            if (fail) throw IOException("offline")
            return KnowledgeList(listOf(KnowledgeEntry("topics", "gardening", "Gardening", "Balcony vegetables", extra = "1")))
        }
        override suspend fun detail(kind: KnowledgeKind, id: String) = KnowledgeDetail(kind.wire, id, "data/${kind.wire}/$id",
            meta = buildJsonObject { put("name", "Gardening") },
            points = listOf(TopicPoint("g1", null, "concept", "Tomatoes need six hours of sun")))
        override suspend fun entry(kind: KnowledgeKind, id: String) = KnowledgeDetailResponse(detail(kind, id))
        override suspend fun search(query: String) = emptyList<KnowledgeHit>()
    }

    private object Nav : Navigator {
        val opened = mutableListOf<NavKey>()
        override fun navigate(key: NavKey) { opened += key }
        override fun back() = Unit
        override fun selectTab(tab: AppTab) = Unit
    }

    private object NoSender : MainConversationSender {
        override val outbox = MutableStateFlow(emptyList<OutboxMessage>())
        override suspend fun sendToMain(text: String, clientMessageId: String, draft: DraftSnapshot?) = Unit
    }

    @Test fun aTopicsPageKeepsTheTagActionAndOpensTheEditor() {
        val connection = object : KnowledgeConnection { override val current = MutableStateFlow<KnowledgeRepository?>(Repo()) }
        val model = KnowledgeDetailViewModel(KnowledgeKind.Topics, "gardening", connection, NoSender)
        rule.setContent { PersonaTheme { KnowledgeDetailScreen(model, Nav, openLinkInApp = {}) } }
        rule.waitUntilExactlyOneExists(hasText("Tomatoes need six hours of sun"), 10_000)
        rule.onNodeWithContentDescription("Tag").performClick()
        rule.waitUntilExactlyOneExists(hasText("Importance"), 10_000)
    }

    @Test fun aFailedRefreshOfTheMapSaysSoOverTheOutlineItStillShows() {
        val repo = Repo()
        val connection = object : KnowledgeConnection { override val current = MutableStateFlow<KnowledgeRepository?>(repo) }
        val model = KnowledgeMapViewModel(connection)
        rule.setContent { PersonaTheme { KnowledgeMapScreen(model, Nav, "assistant") } }
        rule.waitUntilExactlyOneExists(hasText("Gardening"), 10_000)
        rule.waitUntil(10_000) { !model.page.value.loading }
        repo.fail = true
        rule.onNodeWithContentDescription("Refresh knowledge base").performClick()
        rule.waitUntilExactlyOneExists(hasText("Can't reach the knowledge base. Try again later."), 10_000)
        rule.onNodeWithText("Gardening").assertExists()
    }

    @Test fun theMapOpensEntriesAsTheAssistantItWasOpenedFor() {
        val connection = object : KnowledgeConnection { override val current = MutableStateFlow<KnowledgeRepository?>(Repo()) }
        val model = KnowledgeMapViewModel(connection, owner = "m-work")
        Nav.opened.clear()
        rule.setContent { PersonaTheme { KnowledgeMapScreen(model, Nav, "m-work") } }
        rule.waitUntilExactlyOneExists(hasText("Gardening"), 10_000)
        rule.onNodeWithText("Gardening").performClick()
        assertEquals(listOf<NavKey>(KnowledgeDetailRoute(KnowledgeKind.Topics, "gardening", "m-work")), Nav.opened)
    }
}
