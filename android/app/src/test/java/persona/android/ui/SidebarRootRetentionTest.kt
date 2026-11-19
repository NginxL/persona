package persona.android.ui

import android.app.Application
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.material3.Text
import androidx.compose.material3.TextField
import androidx.compose.runtime.Composable
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.getValue
import androidx.compose.runtime.key
import androidx.compose.runtime.mutableIntStateOf
import androidx.compose.runtime.mutableStateListOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.layout.layout
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.semantics.SemanticsProperties
import androidx.compose.ui.test.assert
import androidx.compose.ui.test.hasText
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.performClick
import androidx.compose.ui.test.performTextInput
import androidx.navigation3.runtime.NavKey
import androidx.navigation3.runtime.entryProvider
import androidx.navigation3.runtime.rememberSaveableStateHolderNavEntryDecorator
import androidx.navigation3.ui.NavDisplay
import androidx.navigationevent.compose.LocalNavigationEventDispatcherOwner
import androidx.navigationevent.compose.rememberNavigationEventDispatcherOwner
import androidx.test.ext.junit.runners.AndroidJUnit4
import kotlinx.coroutines.delay
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.rules.RuleChain
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import persona.android.navigation.SidebarRootHosts
import persona.core.designsystem.AppStyle
import persona.core.designsystem.PersonaTheme
import persona.core.navigation.AssistantRoute
import persona.core.navigation.InboxRoute

/**
 * The same unplaced-per-host `NavDisplay` `Root` uses. A draft held in `remember` and a coroutine
 * started by `LaunchedEffect` survive Assistant → Inbox → Assistant, because neither host is popped.
 */
@RunWith(AndroidJUnit4::class)
@Config(application = Application::class, sdk = [35])
class SidebarRootRetentionTest {
    val rule = createComposeRule()
    @get:Rule val host = RuleChain.outerRule(ComponentActivityHostRule()).around(rule)

    @Test
    fun aDraftAndARunningEffectSurviveAnotherRoot() {
        rule.setContent {
            PersonaTheme(style = AppStyle.Glass, darkTheme = false, reduceMotion = true) {
                RetentionHosts()
            }
        }
        rule.onNodeWithTag("draft").performTextInput("Keep this")
        rule.waitUntil(timeoutMillis = 5_000) { ticks() >= 5 }
        val before = ticks()
        rule.onNodeWithTag("open-inbox").performClick()
        rule.waitForIdle()
        rule.mainClock.advanceTimeBy(400)
        rule.onNodeWithTag("open-assistant").performClick()
        rule.waitForIdle()
        rule.onNodeWithTag("draft").assert(hasText("Keep this"))
        assertTrue("the effect kept running while Inbox was in front (before $before, after ${ticks()})", ticks() >= before)
    }

    private fun ticks(): Int = rule.onNodeWithTag("ticks").fetchSemanticsNode()
        .config[SemanticsProperties.Text].joinToString("") { it.text }.toInt()
}

@Composable
private fun RetentionHosts() {
    val conversation = remember { mutableStateListOf<NavKey>(AssistantRoute) }
    val hosts = remember { SidebarRootHosts(conversation) }
    Column(Modifier.fillMaxSize()) {
        androidx.compose.material3.TextButton({ hosts.activate(InboxRoute, listOf(InboxRoute)) }, Modifier.testTag("open-inbox")) {
            Text("Open Inbox")
        }
        androidx.compose.material3.TextButton({ hosts.activate(AssistantRoute, listOf(AssistantRoute)) }, Modifier.testTag("open-assistant")) {
            Text("Open Assistant")
        }
        Box(Modifier.fillMaxSize()) {
            hosts.visited.forEach { hostId ->
                val stack = hosts.stack(hostId)
                if (stack != null) key(hostId) {
                    val shown = hosts.current === stack
                    val back = rememberNavigationEventDispatcherOwner(enabled = shown)
                    ShellTabLifecycle(shown) {
                        CompositionLocalProvider(LocalNavigationEventDispatcherOwner provides back) {
                            NavDisplay(
                                backStack = stack,
                                modifier = Modifier.fillMaxSize().then(if (shown) Modifier else Modifier.unplacedForTest()),
                                onBack = {},
                                entryDecorators = listOf(rememberSaveableStateHolderNavEntryDecorator()),
                                entryProvider = entryProvider {
                                    entry<AssistantRoute> {
                                        var draft by remember { mutableStateOf("") }
                                        var count by remember { mutableIntStateOf(0) }
                                        androidx.compose.runtime.LaunchedEffect(Unit) {
                                            while (true) {
                                                delay(100)
                                                count++
                                            }
                                        }
                                        Column {
                                            TextField(draft, { draft = it }, Modifier.testTag("draft"))
                                            Text("$count", Modifier.testTag("ticks"))
                                        }
                                    }
                                    entry<InboxRoute> { Text("Inbox", Modifier.testTag("inbox-page")) }
                                },
                            )
                        }
                    }
                }
            }
        }
    }
}

private fun Modifier.unplacedForTest(): Modifier = layout { measurable, constraints ->
    val placeable = measurable.measure(constraints)
    layout(placeable.width, placeable.height) {}
}
