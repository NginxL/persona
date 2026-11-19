package persona.android.ui

import android.app.Application
import android.content.ComponentName
import android.graphics.Bitmap
import androidx.activity.ComponentActivity
import androidx.activity.compose.setContent
import androidx.compose.foundation.IndicationNodeFactory
import androidx.compose.foundation.LocalIndication
import androidx.compose.foundation.interaction.InteractionSource
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.padding
import androidx.compose.material3.Text
import androidx.compose.material3.TextField
import androidx.compose.runtime.Composable
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.key
import androidx.compose.runtime.mutableIntStateOf
import androidx.compose.runtime.mutableStateListOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import androidx.compose.runtime.snapshots.Snapshot
import androidx.compose.ui.Modifier
import androidx.compose.ui.node.DelegatableNode
import androidx.compose.ui.graphics.asAndroidBitmap
import androidx.compose.ui.layout.layout
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.semantics.SemanticsProperties
import androidx.compose.ui.test.assert
import androidx.compose.ui.test.assertCountEquals
import androidx.compose.ui.test.assertIsNotDisplayed
import androidx.compose.ui.test.assertIsSelected
import androidx.compose.ui.test.captureToImage
import androidx.compose.ui.test.SemanticsMatcher
import androidx.compose.ui.test.hasClickAction
import androidx.compose.ui.test.hasContentDescription
import androidx.compose.ui.test.hasText
import androidx.compose.ui.test.junit4.createEmptyComposeRule
import androidx.compose.ui.test.onAllNodesWithContentDescription
import androidx.compose.ui.test.onAllNodesWithText
import androidx.compose.ui.test.onNodeWithContentDescription
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.compose.ui.test.performTextReplacement
import androidx.compose.ui.unit.dp
import androidx.lifecycle.viewmodel.navigation3.rememberViewModelStoreNavEntryDecorator
import androidx.navigation3.runtime.NavEntry
import androidx.navigation3.runtime.NavEntryDecorator
import androidx.navigation3.runtime.NavKey
import androidx.navigation3.runtime.entryProvider
import androidx.navigation3.runtime.rememberSaveableStateHolderNavEntryDecorator
import androidx.navigation3.ui.NavDisplay
import androidx.navigationevent.compose.LocalNavigationEventDispatcherOwner
import androidx.navigationevent.compose.rememberNavigationEventDispatcherOwner
import androidx.lifecycle.Lifecycle
import androidx.test.core.app.ActivityScenario
import androidx.test.core.app.ApplicationProvider
import androidx.test.ext.junit.runners.AndroidJUnit4
import kotlinx.coroutines.delay
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.flowOf
import org.junit.After
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.Shadows
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.android.R
import persona.android.navigation.HomeLeadingAction
import persona.android.navigation.SidebarPageRoots
import persona.android.navigation.SidebarRootHosts
import persona.android.navigation.applySidebarDestination
import persona.android.navigation.noPageMotion
import persona.android.navigation.personaSceneRootSwapped
import persona.android.navigation.placeRootFor
import persona.android.navigation.replaceBackStack
import persona.android.navigation.resolveHomeLeadingAction
import persona.android.navigation.showsPlaceRootLeading
import persona.android.navigation.sidebarShadeKey
import persona.core.contracts.InboxEventFields
import persona.core.contracts.Session
import persona.core.contracts.Todo
import persona.core.contracts.api.AssistantManagerChoice
import persona.core.contracts.api.AssistantRepository
import persona.core.contracts.api.AssistantSnapshot
import persona.core.contracts.api.ConnectionState
import persona.core.contracts.api.InboxEntry
import persona.core.designsystem.AppStyle
import persona.core.designsystem.HomeChrome
import persona.core.designsystem.JPage
import persona.core.designsystem.JPersonaButton
import persona.core.designsystem.JPersonaButtonRoom
import persona.core.designsystem.JSidebarColumn
import persona.core.designsystem.LocalHomeChrome
import persona.core.designsystem.LocalIsSidebarRoot
import persona.core.designsystem.LocalRootBack
import persona.core.designsystem.LocalRootLeading
import persona.core.designsystem.PersonaState
import persona.core.designsystem.PersonaTheme
import persona.core.designsystem.rememberPageTransitions
import persona.core.domain.inbox.InboxItem
import persona.core.navigation.AppTab
import persona.core.navigation.AssistantRoute
import persona.core.navigation.InboxRoute
import persona.core.navigation.Navigator
import persona.core.navigation.TodosRoute
import persona.feature.agents.TodosScreen
import persona.feature.assistant.AssistantSidebarContent
import persona.feature.inbox.InboxConversations
import persona.feature.inbox.InboxEventRoute
import persona.feature.inbox.InboxLanding
import persona.feature.inbox.InboxPreferences
import persona.feature.inbox.InboxRecord
import persona.feature.inbox.InboxRepository
import persona.feature.inbox.InboxScreen
import persona.feature.inbox.InboxSnapshot
import persona.feature.inbox.LocalInboxLanding
import persona.feature.inbox.LarkConversation
import persona.feature.inbox.ReplyDraft
import persona.feature.inbox.ReplyRequest
import persona.feature.inbox.ReplyTarget
import java.io.File

/**
 * Regular-width sidebar roots (#9667) on the real Inbox and To-dos pages, each root its own NavDisplay.
 * Glass, Brief and Board, light and dark. Inbox → To-dos has no page transition (UnifiedSidebar.swift:67–74).
 * A drill-down still pushes.
 * The conversation page is a draft field and a ticking effect: AssistantScreen needs a shell session.
 * The item page is a JPage on the real InboxEventRoute; InboxEventScreen also needs hand-off and reply composers.
 */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(application = Application::class, sdk = [35])
class SidebarRootsShotsTest {
    @get:Rule val rule = createEmptyComposeRule()

    @After fun resetAssistant() { AssistantManagerChoice.reset() }

    @Test
    @Config(qualifiers = "en-w673dp-h951dp-mdpi")
    fun uprightOverlay() = exercise("673x951", overlay = true)

    @Test
    @Config(qualifiers = "en-w951dp-h673dp-mdpi")
    fun landscapeBeside() = exercise("951x673", overlay = false)

    private fun exercise(size: String, overlay: Boolean) {
        AssistantManagerChoice.choose("hub", null, "assistant")
        val app = ApplicationProvider.getApplicationContext<Application>()
        Shadows.shadowOf(app.packageManager).addActivityIfNotPresent(ComponentName(app, ComponentActivity::class.java))
        val main = Session("computer", "assistant", title = "Main conversation", assistant = true)
        val snapshot = AssistantSnapshot(mainSession = main.key, conversations = listOf(main))
        val repository = object : AssistantRepository {
            override val assistant = MutableStateFlow(snapshot)
            override suspend fun refresh() = Unit
        }
        val inbox = ShotInbox(listOf(
            item("first", "Approve Friday launch"),
            item("second", "Review budget update"),
            item("third", "Weekly note", tier = "fyi"),
        ))
        val landing = InboxLanding()
        var dark by mutableStateOf(false)
        var style by mutableStateOf(AppStyle.Glass)
        val columnShown = mutableStateOf(true)
        val overlayShown = mutableStateOf(overlay)
        val conversation = mutableStateListOf<NavKey>(AssistantRoute)
        val hosts = SidebarRootHosts(conversation)
        val roots = SidebarPageRoots()
        fun pick(key: NavKey) {
            overlayShown.value = false
            applySidebarDestination(
                key, regular = true, roots, hosts, conversation,
                showHost = { root, next -> hosts.activate(root, next) },
                assignPersona = {},
                landing = landing,
                records = inbox.snapshot.value.records,
            )
        }
        fun reset() {
            landing.clear()
            roots.forget()
            hosts.forgetPlaces()
            replaceBackStack(conversation, listOf(AssistantRoute))
            overlayShown.value = overlay
            columnShown.value = true
        }
        rule.mainClock.autoAdvance = false
        ActivityScenario.launch(ComponentActivity::class.java).use { scenario ->
            scenario.onActivity { activity ->
                activity.setContent {
                    PersonaTheme(style = style, darkTheme = dark, reduceMotion = false) {
                        SidebarRootsHarness(snapshot, repository, inbox, hosts, roots, overlay, columnShown, overlayShown, landing, ::pick)
                    }
                }
            }
            for (nextStyle in AppStyle.entries) {
            for (isDark in listOf(false, true)) {
                rule.runOnIdle { style = nextStyle; dark = isDark; reset(); overlayShown.value = false }
                val tone = if (isDark) "dark" else "light"
                val styleName = nextStyle.key
                settle()
                // TextField keeps the tag on the unmerged node. The overlay starts open and would cover the field.
                rule.onNodeWithTag("draft", useUnmergedTree = true).performTextReplacement("Keep this draft")
                settle(400)
                val ticksBefore = ticks()
                save(shot("window"), "$size-draft-before-$styleName-$tone")

                reveal(overlay)
                rule.onNode(hasText("Inbox") and hasClickAction()).performClick()
                settle(1_000)
                rule.onAllNodesWithContentDescription("Back").assertCountEquals(0)
                rule.onNodeWithText("Approve Friday launch").assertExists()
                // A covered host stays composed, so its button remains in the semantics tree.
                if (overlay) placedSidebar().assertExists()
                else rule.onAllNodesWithContentDescription("Sidebar").assertCountEquals(0)
                save(shot("window"), "$size-inbox-rest-before-$styleName-$tone")
                if (!overlay) {
                    // Beside a shown column the page slot is the empty room (SplitNavigation.swift:419–420).
                    // Folding the column is the existing Show Sidebar control, the same one the conversation uses.
                    rule.runOnIdle { columnShown.value = false }
                    settle(800)
                    placedSidebar().assertExists()
                    save(shot("window"), "$size-inbox-rest-folded-$styleName-$tone")
                    rule.runOnIdle { columnShown.value = true }
                    // The column opens on the layout spring (300ms, no bounce). settle() is 32ms, which
                    // leaves that spring mid-travel: the next frame then shows the whole window, column
                    // included, sliding left. Production does not reopen the column for a drill-down
                    // (AssistantPaneFrame keeps it outside the host NavDisplay). Let it finish first.
                    settle(800)
                }
                val columnAtRest = if (overlay) null else shot("window")

                rule.onNodeWithText("Approve Friday launch").performClick()
                advance(80)
                val pushMid = shot("window")
                save(pushMid, "$size-item-push-mid-$styleName-$tone")
                if (columnAtRest != null) {
                    // The left of the column, short of where a page sliding out of the slot could cover it.
                    assertTrue("$size $styleName $tone drill-down leaves the sidebar column put",
                        matches(leftStrip(columnAtRest, 240), leftStrip(pushMid, 240)))
                    // The whole column, including the edge a slide used to paint (#9896).
                    assertColumnUntouched(columnAtRest, pushMid, "$size $styleName $tone mid-push")
                }
                settle(1_000)
                val pushEnd = shot("window")
                save(pushEnd, "$size-item-push-end-$styleName-$tone")
                assertTrue("$size $styleName $tone item push mid frame differs", moved(maskSidebarHeader(pushMid), maskSidebarHeader(pushEnd)))
                rule.onNodeWithContentDescription("Back").assertExists()
                rule.onNodeWithTag("page-item").assertExists()
                if (overlay) {
                    rule.onAllNodes(hasContentDescription("Sidebar") and placedNode(), useUnmergedTree = true)
                        .assertCountEquals(0)
                    rule.onNodeWithContentDescription("Back").performClick()
                    settle(1_000)
                    val backToList = shot("window")
                    // The pop's departing page can keep a Back node after it has slid away (semantics ignore
                    // the slide translation). The page in front has no chevron.
                    assertNoVisibleChevron(backToList, "$size $styleName $tone back to the list", overlay)
                    if (overlay) placedSidebar().assertExists()
                    rule.onNodeWithText("Approve Friday launch").assertExists()
                } else {
                    reveal(overlay)
                    rule.onNode(hasText("To-dos") and hasClickAction()).performClick()
                    settle(1_000)
                    rule.onNodeWithText("Ship the notes").assertExists()
                    reveal(overlay)
                    rule.onNode(hasText("Inbox") and hasClickAction()).performClick()
                    settle(1_000)
                    rule.onNodeWithTag("page-item").assertExists()
                    rule.onNode(hasText("Inbox") and hasClickAction()).performClick()
                    advance(80)
                    val popMid = shot("window")
                    save(popMid, "$size-inbox-pop-mid-$styleName-$tone")
                    if (columnAtRest != null) {
                        assertColumnUntouched(columnAtRest, popMid, "$size $styleName $tone mid-pop")
                    }
                    settle(1_000)
                    val popEnd = shot("window")
                    save(popEnd, "$size-inbox-pop-end-$styleName-$tone")
                    assertTrue("$size $styleName $tone re-pick pop mid frame differs", moved(maskSidebarHeader(popMid), maskSidebarHeader(popEnd)))
                    rule.onNodeWithText("Approve Friday launch").assertExists()
                    assertNoVisibleChevron(popEnd, "$size $styleName $tone re-pick", overlay)
                    val inboxShade = sample(popEnd, "Inbox", nextStyle)
                    val todosShade = sample(popEnd, "To-dos", nextStyle)
                    val scheduleShade = sample(popEnd, "Scheduled tasks", nextStyle)
                    assertTrue("$size $styleName $tone inbox row ($inboxShade) is shaded and To-dos ($todosShade) is not",
                        channelDelta(inboxShade, todosShade) > 8)
                    assertTrue("$size $styleName $tone inbox row ($inboxShade) differs from Scheduled tasks ($scheduleShade)",
                        channelDelta(inboxShade, scheduleShade) > 8)
                }

                reveal(overlay)
                rule.onNode(hasText("To-dos") and hasClickAction()).performClick()
                // Beside the page this is a root swap with no push. Over the page the column slides on its own
                // spec: Glass layout spring 300ms, Brief tween 240ms, Board tween 160ms (PersonaTheme.kt Motions).
                advance(columnSlideMidMs(nextStyle))
                val swapMid = shot("window")
                save(swapMid, "$size-inbox-todos-mid-$styleName-$tone")
                settle(1_000)
                val swapEnd = shot("window")
                save(swapEnd, "$size-inbox-todos-end-$styleName-$tone")
                rule.onNodeWithText("Ship the notes").assertExists()
                // A covered host can still expose a Back node from a drill that already slid away.
                assertNoVisibleChevron(swapEnd, "$size $styleName $tone to-dos", overlay)
                if (overlay) placedSidebar().assertExists()
                else {
                    val inboxShade = sample(swapEnd, "Inbox", nextStyle)
                    val todosShade = sample(swapEnd, "To-dos", nextStyle)
                    val scheduleShade = sample(swapEnd, "Scheduled tasks", nextStyle)
                    assertTrue("$size $styleName $tone only To-dos is shaded ($todosShade) not Inbox ($inboxShade)",
                        channelDelta(todosShade, inboxShade) > 8)
                    assertTrue("$size $styleName $tone To-dos ($todosShade) differs from Scheduled tasks ($scheduleShade)",
                        channelDelta(todosShade, scheduleShade) > 8)
                }
                val pageMid = if (overlay) mask(shot("page-todos"), 320, swapMid.height) else pageBesideColumn(swapMid)
                val pageEnd = if (overlay) mask(shot("page-todos"), 320, swapEnd.height) else pageBesideColumn(swapEnd)
                assertTrue("$size $styleName $tone Inbox → To-dos page mid frame equals the end frame",
                    matches(pageMid, pageEnd, tolerance = if (overlay) 24 else 8))
                if (overlay) {
                    assertTrue("$size $styleName $tone overlay column is still moving at the mid frame", moved(swapMid, swapEnd))
                }

                reveal(overlay)
                rule.onNode(hasText("Inbox") and hasClickAction()).performClick()
                settle(1_000)
                rule.onNodeWithText("Approve Friday launch").assertExists()
                rule.onNodeWithText("Review budget update").assertExists()
                val inboxAgain = shot("window")
                assertNoVisibleChevron(inboxAgain, "$size $styleName $tone inbox again", overlay)
                if (overlay) placedSidebar().assertExists()
                save(inboxAgain, "$size-todos-inbox-end-$styleName-$tone")

                if (!overlay) {
                    rule.onNode(hasText("FYI") and SemanticsMatcher.keyIsDefined(SemanticsProperties.Selected)).performClick()
                    settle(1_000)
                    rule.onNodeWithText("Weekly note").assertExists()
                    rule.onNode(hasText("To-dos") and hasClickAction()).performClick()
                    settle(1_000)
                    rule.onNode(hasText("Inbox") and hasClickAction()).performClick()
                    settle(1_000)
                    rule.onNode(hasText("Needs you") and SemanticsMatcher.keyIsDefined(SemanticsProperties.Selected)).assertIsSelected()
                    rule.onNodeWithText("Approve Friday launch").assertExists()
                    rule.onNodeWithText("Weekly note").assertDoesNotExist()
                    save(shot("window"), "$size-inbox-needs-you-after-fyi-$styleName-$tone")
                }

                scenario.onActivity { it.onBackPressedDispatcher.onBackPressed() }
                settle(1_000)
                assertEquals(Lifecycle.State.RESUMED, scenario.state)
                assertEquals(listOf<NavKey>(AssistantRoute), hosts.current.toList())
                rule.onNodeWithTag("draft", useUnmergedTree = true).assert(hasText("Keep this draft"))
                assertTrue("$size $styleName $tone the draft's effect kept running", ticks() > ticksBefore)
                // The inbox host stays composed, unplaced. Its row must not be the page in front.
                val covered = rule.onAllNodesWithText("Approve Friday launch")
                    .fetchSemanticsNodes(atLeastOneRootRequired = false)
                if (covered.isNotEmpty()) {
                    rule.onAllNodesWithText("Approve Friday launch")[0].assertIsNotDisplayed()
                }
                save(shot("window"), "$size-draft-after-$styleName-$tone")
            }
            }
        }
    }

    /**
     * Mid-travel of the overlaid column (`sidebarColumnSpec`).
     * Glass keeps 80ms inside the 300ms layout spring (`Tokens.Motion.Layout.duration`).
     * Brief's tween is `standard` 240ms, so the mid frame is 120ms.
     * Board's tween is `standard` 160ms, so the mid frame is 80ms.
     * Page push (300ms) and pop (260ms) do not change with the style (`PageTransitions.kt`); those frames stay at 80ms.
     */
    private fun columnSlideMidMs(style: AppStyle): Long = when (style) {
        AppStyle.Glass -> 80
        AppStyle.Brief -> 120
        AppStyle.Board -> 80
    }

    private fun ticks(): Int = rule.onNodeWithTag("ticks", useUnmergedTree = true).fetchSemanticsNode()
        .config[SemanticsProperties.Text].joinToString("") { it.text }.toInt()

    /** The unplaced root is still in the semantics tree. Only the placed button is on screen. */
    private fun placedNode(): SemanticsMatcher = SemanticsMatcher("is placed") { it.layoutInfo.isPlaced }

    private fun placedSidebar() =
        rule.onNode(hasContentDescription("Sidebar") and placedNode(), useUnmergedTree = true)

    /**
     * A beside-page chevron sits in this glyph box (the item drill has about 30 contrasting pixels;
     * an empty root slot has none). An overlay root draws the persona face in that corner instead,
     * so the face is checked with [placedSidebar]. An exit transition can leave a Back node behind.
     */
    private fun assertNoVisibleChevron(bitmap: Bitmap, label: String, overlay: Boolean) {
        if (overlay) return
        val background = bitmap.getPixel(344, 18)
        var differ = 0
        for (y in 18 until 46) for (x in 344 until 372) {
            if (channelDelta(bitmap.getPixel(x, y), background) > 40) differ++
        }
        assertTrue("$label visible chevron pixels $differ", differ < 12)
    }

    /**
     * A pixel of the selected-row mark.
     * Glass shades the row (`Lists.kt` drawer fill is `palette.sunken`); the sample stays in the fill,
     * past the label and short of the badge.
     * Brief and Board paint `statusSurface`, the column's own paper (`sidebarBackground`), so that fill
     * matches an unselected row. The mark is the edge (`Adaptive.kt` `selectedRow`): Brief's ink bar
     * starts 12dp before the row, Board's accent bar is the first 3dp of the row.
     */
    private fun sample(bitmap: Bitmap, label: String, style: AppStyle): Int {
        val window = rule.onNodeWithTag("window").fetchSemanticsNode().boundsInRoot
        val row = rule.onAllNodes(hasText(label) and hasClickAction()).fetchSemanticsNodes().minBy { it.boundsInRoot.left }
        val bounds = row.boundsInRoot
        val x = when (style) {
            AppStyle.Glass -> bounds.left - window.left + bounds.width * 0.72f
            AppStyle.Brief -> bounds.left - window.left - 12f + 1f
            AppStyle.Board -> bounds.left - window.left + 1f
        }.toInt().coerceIn(0, bitmap.width - 1)
        val y = (bounds.top - window.top + bounds.height * 0.5f).toInt().coerceIn(0, bitmap.height - 1)
        return bitmap.getPixel(x, y)
    }

    private fun reveal(overlay: Boolean) {
        if (!overlay) return
        val visible = rule.onAllNodes(hasText("To-dos") and hasClickAction()).fetchSemanticsNodes()
        if (visible.isEmpty()) {
            placedSidebar().performClick()
            settle()
        }
    }

    private fun settle(ms: Long = 32) {
        Snapshot.sendApplyNotifications()
        // Step the clock. One large jump leaves a click ripple and a spring column half-drawn.
        var left = ms
        while (left > 0) {
            val step = minOf(16L, left)
            rule.mainClock.advanceTimeBy(step)
            left -= step
        }
        repeat(2) { rule.mainClock.advanceTimeByFrame() }
    }

    private fun advance(ms: Long) {
        Snapshot.sendApplyNotifications()
        rule.mainClock.advanceTimeBy(ms)
    }

    private fun shot(tag: String): Bitmap = rule.onNodeWithTag(tag).captureToImage().asAndroidBitmap()

    private fun save(bitmap: Bitmap, name: String) {
        val out = File("build/sidebar-roots").also { it.mkdirs() }
        File(out, "$name.png").outputStream().use { assertTrue(bitmap.compress(Bitmap.CompressFormat.PNG, 100, it)) }
    }

    private fun maskSidebarHeader(bitmap: Bitmap): Bitmap = mask(bitmap, 320, 220)

    /** Exact RGBA of the beside column (320dp at mdpi). A push stays in the detail slot. */
    private fun assertColumnUntouched(rest: Bitmap, frame: Bitmap, label: String) {
        val width = minOf(320, rest.width, frame.width)
        val height = minOf(rest.height, frame.height)
        for (y in 0 until height) {
            for (x in 0 until width) {
                assertEquals("$label column pixel ($x,$y)", rest.getPixel(x, y), frame.getPixel(x, y))
            }
        }
    }

    private fun leftStrip(bitmap: Bitmap, width: Int): Bitmap =
        Bitmap.createBitmap(bitmap, 0, 0, minOf(width, bitmap.width), bitmap.height)

    private fun pageBesideColumn(bitmap: Bitmap): Bitmap {
        val start = minOf(320, bitmap.width)
        return Bitmap.createBitmap(bitmap, start, 0, bitmap.width - start, bitmap.height)
    }

    private fun matches(a: Bitmap, b: Bitmap, tolerance: Int = 8): Boolean {
        if (a.width != b.width || a.height != b.height) return false
        for (y in 0 until a.height) for (x in 0 until a.width) {
            if (channelDelta(a.getPixel(x, y), b.getPixel(x, y)) > tolerance) return false
        }
        return true
    }

    private fun moved(a: Bitmap, b: Bitmap): Boolean = !matches(a, b)

    private fun channelDelta(a: Int, b: Int): Int {
        var delta = 0
        for (shift in intArrayOf(0, 8, 16, 24)) {
            val part = kotlin.math.abs(((a shr shift) and 0xFF) - ((b shr shift) and 0xFF))
            if (part > delta) delta = part
        }
        return delta
    }

    private fun mask(bitmap: Bitmap, width: Int, height: Int): Bitmap {
        val copy = bitmap.copy(Bitmap.Config.ARGB_8888, true)
        val w = minOf(width, copy.width)
        val h = minOf(height, copy.height)
        for (y in 0 until h) for (x in 0 until w) copy.setPixel(x, y, 0)
        return copy
    }
}

@Composable
private fun SidebarRootsHarness(
    snapshot: AssistantSnapshot,
    repository: AssistantRepository,
    inbox: InboxRepository,
    hosts: SidebarRootHosts,
    roots: SidebarPageRoots,
    overlay: Boolean,
    columnShown: androidx.compose.runtime.MutableState<Boolean>,
    overlayShown: androidx.compose.runtime.MutableState<Boolean>,
    landing: InboxLanding,
    pick: (NavKey) -> Unit,
) {
    val scope = rememberCoroutineScope()
    // A click's ripple stays dark under the test clock and reads as a second shaded row.
    // Selection is the row background; the ripple is not part of the shot.
    val rootKeys = remember { listOf(AssistantRoute).map { NavEntry<NavKey>(it) {}.contentKey }.toSet() }
    val sidebarLabel = stringResource(R.string.app_sidebar)
    val navigator = remember(hosts, roots) {
        object : Navigator {
            override fun navigate(key: NavKey) {
                val place = placeRootFor(key)
                if (place != null && roots.current == place && key != place) {
                    hosts.current.add(key)
                    return
                }
                hosts.current.add(key)
            }
            override fun back() {
                val stack = hosts.current
                if (stack.size > 1) {
                    stack.removeAt(stack.lastIndex)
                    return
                }
                if (stack.firstOrNull() != null && stack.firstOrNull() != AssistantRoute) {
                    val step = roots.select(AssistantRoute, emptyList())
                    hosts.activate(step.root, step.stack)
                }
            }
            override fun selectTab(tab: AppTab) = Unit
        }
    }
    val chrome = NavEntryDecorator<NavKey> { entry ->
        val primary = entry.contentKey in rootKeys
        val hostRoot = hosts.current.firstOrNull()?.let { NavEntry<NavKey>(it) {}.contentKey }
        val placeRoot = showsPlaceRootLeading(regular = true, persona = true, primary = primary,
            entryIsStackRoot = hostRoot != null && entry.contentKey == hostRoot)
        val leading: @Composable () -> Unit = {
            val action = resolveHomeLeadingAction(assistantRoot = true, regular = true, columnsOver = overlay,
                sidebarColumnShown = columnShown.value)
            when (action) {
                HomeLeadingAction.HiddenSpacer -> JPersonaButtonRoom()
                else -> JPersonaButton(PersonaState.Idle, sidebarLabel, {
                    if (action == HomeLeadingAction.ToggleOverlay) overlayShown.value = !overlayShown.value
                    else columnShown.value = true
                })
            }
        }
        CompositionLocalProvider(
            LocalIsSidebarRoot provides placeRoot,
            LocalRootLeading provides if (placeRoot) leading else null,
            LocalRootBack provides if (primary || placeRoot) null else navigator::back,
            LocalHomeChrome provides if (primary) HomeChrome(emptyList(), "Persona", {}, leading = leading) else null,
        ) { entry.Content() }
    }
    val transitions = rememberPageTransitions()
    val shown = if (overlay) overlayShown.value else columnShown.value
    CompositionLocalProvider(LocalIndication provides QuietIndication, LocalInboxLanding provides landing) {
    Box(Modifier.fillMaxSize().testTag("window")) {
        JSidebarColumn(
            sidebar = {
                AssistantSidebarContent(snapshot, repository, snapshot.mainSession, scope, demo = true,
                    onOpen = { _ -> }, onAction = { _ -> }, onSettings = {},
                    onInbox = { pick(InboxRoute) }, onKnowledge = {}, onTodos = { pick(TodosRoute) },
                    todoCount = 1, showTodos = true, inboxCount = 2, settingsCount = 0, settingsDot = false,
                    settingsBadge = null, pages = emptyList(), onPage = { _ -> }, onRetired = { _ -> },
                    shadedPlace = hosts.current.firstOrNull()?.let { sidebarShadeKey(it) })
            },
            width = if (overlay) persona.android.navigation.PaneSplits.DefaultLeft.dp else 320.dp,
            pageStart = if (overlay) 0.dp else 320.dp,
            shown = shown, overlay = overlay, onDismiss = { overlayShown.value = false },
        ) {
            Box(Modifier.fillMaxSize()) {
                hosts.visited.forEach { hostId ->
                    val stack = hosts.stack(hostId)
                    if (stack != null) key(hostId) {
                        val hostShown = hosts.current === stack
                        val back = rememberNavigationEventDispatcherOwner(enabled = hostShown)
                        ShellTabLifecycle(hostShown) {
                            CompositionLocalProvider(LocalNavigationEventDispatcherOwner provides back) {
                                val root = stack.firstOrNull()
                                // Same order as SidebarRootsRuntime: the place-root handler is before the display,
                                // so a page handler inside it wins while it is enabled.
                                PlaceRootBack(hostShown && stack.size <= 1 && root != null && root != AssistantRoute, navigator::back) {
                                NavDisplay(
                                    backStack = stack,
                                    modifier = Modifier.fillMaxSize().then(if (hostShown) Modifier else Modifier.layout { measurable, constraints ->
                                        val placeable = measurable.measure(constraints)
                                        layout(placeable.width, placeable.height) {}
                                    }),
                                    onBack = navigator::back,
                                    entryDecorators = listOf(
                                        rememberSaveableStateHolderNavEntryDecorator(),
                                        rememberViewModelStoreNavEntryDecorator(),
                                        chrome,
                                    ),
                                    transitionSpec = {
                                        if (personaSceneRootSwapped(true, true, initialState, targetState)) noPageMotion() else transitions.push
                                    },
                                    popTransitionSpec = {
                                        if (personaSceneRootSwapped(true, true, initialState, targetState)) noPageMotion() else transitions.pop
                                    },
                                    predictivePopTransitionSpec = { transitions.predictivePop },
                                    entryProvider = entryProvider {
                                        entry<AssistantRoute> {
                                            var draft by remember { mutableStateOf("") }
                                            var count by remember { mutableIntStateOf(0) }
                                            LaunchedEffect(Unit) {
                                                while (true) {
                                                    delay(100)
                                                    count++
                                                }
                                            }
                                            // A real conversation draws the sidebar button through the home bar. This
                                            // stand-in does too, so an overlaid column can be opened again.
                                            // Glass floats the bar over the content. The padding keeps the draft
                                            // below that bar so the round-trip shots show the text.
                                            JPage("", onBack = null) { padding ->
                                                Column(Modifier.fillMaxSize().padding(padding).testTag("page-assistant")) {
                                                    TextField(draft, { draft = it }, Modifier.testTag("draft"))
                                                    Text("$count", Modifier.testTag("ticks"))
                                                }
                                            }
                                        }
                                        entry<InboxRoute> {
                                            Box(Modifier.fillMaxSize().testTag("page-inbox")) { InboxScreen(inbox, navigator) }
                                        }
                                        entry<TodosRoute> {
                                            Box(Modifier.fillMaxSize().testTag("page-todos")) {
                                                TodosScreen(listOf(Todo(id = "t1", title = "Ship the notes")), emptyList(), null, navigator::back, {})
                                            }
                                        }
                                        entry<InboxEventRoute> {
                                            Box(Modifier.fillMaxSize().testTag("page-item")) {
                                                JPage("Opened item", onBack = navigator::back) {}
                                            }
                                        }
                                    },
                                )
                                }
                            }
                        }
                    }
                }
            }
        }
    }
    }
}

/** No press overlay. [IndicationNodeFactory] so the local stays equal across recompositions. */
private object QuietIndication : IndicationNodeFactory {
    override fun create(interactionSource: InteractionSource): DelegatableNode = object : Modifier.Node() {}
    override fun equals(other: Any?): Boolean = other === this
    override fun hashCode(): Int = 0
}

private class ShotInbox(records: List<InboxRecord>) : InboxRepository {
    override val entries = MutableStateFlow(emptyList<InboxEntry>())
    override val snapshot = MutableStateFlow(InboxSnapshot(records = records, loading = false, refreshes = 1))
    override val connectionState = MutableStateFlow(ConnectionState.Online)
    override val preferences = MutableStateFlow(InboxPreferences(expanded = listOf("action", "fyi", "noise", "done")))
    override val conversationInbox = MutableStateFlow<InboxConversations?>(null)
    override suspend fun refresh() = Unit
    override fun conversation(feedId: String, startPosition: Long?) = flowOf(LarkConversation())
    override suspend fun loadOlder(feedId: String) = Unit
    override suspend fun refreshConversation(feedId: String) = Unit
    override suspend fun setDone(itemId: String, done: Boolean) = Unit
    override suspend fun readDraft(target: ReplyTarget): ReplyDraft? = null
    override suspend fun saveDraft(target: ReplyTarget, draft: ReplyDraft) = Unit
    override suspend fun reply(target: ReplyTarget, request: ReplyRequest) = Unit
    override suspend fun downloadFile(file: persona.feature.inbox.LarkPart.File): String = error("not used")
}

private fun item(id: String, title: String, tier: String = "action") = InboxRecord(
    InboxItem(id, account = "chat", kind = "group", title = title, sender = "Avery",
        preview = "The launch checklist is ready.", summary = "Ready for a decision", tier = tier, status = tier,
        updatedAt = "2026-11-13T10:00:00Z", version = "v-$id"),
    source = "lark", sourceLabel = "Chat connector", feedId = "shared-room", isGroup = true,
    groupName = "Planning room", count = 2, read = false, event = InboxEventFields(status = "action"),
)
