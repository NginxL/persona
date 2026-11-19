package persona.android.ui

import android.app.Application
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.LazyListState
import androidx.compose.foundation.lazy.rememberLazyListState
import androidx.compose.material3.Text
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.DisposableEffect
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.SideEffect
import androidx.compose.runtime.key
import androidx.compose.runtime.mutableStateListOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.ui.Modifier
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.test.ext.junit.runners.AndroidJUnit4
import org.junit.Assert.assertEquals
import org.junit.Assert.assertSame
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.rules.RuleChain
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import kotlinx.coroutines.awaitCancellation
import persona.core.designsystem.LocalLargeWindowInput
import persona.core.designsystem.Tokens

@RunWith(AndroidJUnit4::class)
@Config(application = Application::class, sdk = [35])
class AssistantPaneFrameTest {
    val rule = createComposeRule()
    @get:Rule val host = RuleChain.outerRule(ComponentActivityHostRule()).around(rule)

    @Test
    fun windowMarkerChangesKeepEveryAssistantLayerMounted() {
        val regular = mutableStateOf(false)
        val showSidebar = mutableStateOf(false)
        val routes = mutableStateListOf("root")
        val identities = mutableMapOf<String, Any>()
        val disposals = mutableMapOf<String, Int>()
        val seenWindowValues = mutableListOf<Boolean>()
        rule.setContent {
            CompositionLocalProvider(LocalLargeWindowInput provides regular.value) {
                routes.forEach { route ->
                    AssistantPaneFrame(regular.value, showSidebar.value && routes.last() == route,
                        shown = true, sidebar = { Text("Sidebar") }, hinges = emptyList()) {
                        val marker = remember { Any() }
                        val large = LocalLargeWindowInput.current
                        SideEffect {
                            identities[route] = marker
                            if (seenWindowValues.lastOrNull() != large) seenWindowValues += large
                        }
                        DisposableEffect(marker) { onDispose { disposals[route] = disposals.getOrDefault(route, 0) + 1 } }
                    }
                }
            }
        }
        lateinit var root: Any
        rule.runOnIdle {
            root = identities.getValue("root")
            regular.value = true
            showSidebar.value = true
            routes += "inbox"
        }
        lateinit var inbox: Any
        rule.runOnIdle {
            assertSame(root, identities.getValue("root"))
            inbox = identities.getValue("inbox")
            regular.value = false
            showSidebar.value = false
            routes += "mail"
        }
        rule.runOnIdle {
            assertSame(root, identities.getValue("root"))
            assertSame(inbox, identities.getValue("inbox"))
            regular.value = true
            showSidebar.value = false
        }
        rule.runOnIdle {
            assertSame(root, identities.getValue("root"))
            assertSame(inbox, identities.getValue("inbox"))
            assertTrue(seenWindowValues.contains(false))
            assertTrue(seenWindowValues.contains(true))
            assertEquals(emptyMap<String, Int>(), disposals)
        }
    }

    @Test
    fun rightStackNavigationKeepsSidebarIdentityEffectAndAnchor() {
        val routes = mutableStateListOf("root")
        var sidebarMarker: Any? = null
        var sidebarList: LazyListState? = null
        var effectStarts = 0
        var disposals = 0
        rule.setContent {
            CompositionLocalProvider(LocalLargeWindowInput provides true) {
                AssistantPaneFrame(regular = true, showSidebar = true, shown = true, sidebar = {
                    val marker = remember { Any() }
                    val scroll = rememberLazyListState()
                    SideEffect { sidebarMarker = marker; sidebarList = scroll }
                    DisposableEffect(Unit) { onDispose { disposals++ } }
                    LaunchedEffect(Unit) { effectStarts++; awaitCancellation() }
                    LazyColumn(Modifier.height(Tokens.Size.box280), state = scroll) {
                        items(20) { index -> Text("Row $index") }
                    }
                }, hinges = emptyList()) {
                    routes.forEach { route -> key(route) { Text(route) } }
                }
            }
        }
        rule.runOnIdle { sidebarList!!.requestScrollToItem(4, 13) }
        rule.waitForIdle()
        val originalMarker = sidebarMarker
        val originalList = sidebarList
        val first = sidebarList!!.firstVisibleItemIndex
        val offset = sidebarList!!.firstVisibleItemScrollOffset
        routes += "inbox"
        rule.waitForIdle()
        assertSame(originalMarker, sidebarMarker)
        routes += "mail"
        rule.waitForIdle()
        assertSame(originalMarker, sidebarMarker)
        routes.removeLast()
        rule.waitForIdle()
        routes.removeLast()
        rule.waitForIdle()
        assertSame(originalMarker, sidebarMarker)
        assertSame(originalList, sidebarList)
        assertEquals(first, sidebarList!!.firstVisibleItemIndex)
        assertEquals(offset, sidebarList!!.firstVisibleItemScrollOffset)
        assertEquals(1, effectStarts)
        assertEquals(0, disposals)
    }
}
