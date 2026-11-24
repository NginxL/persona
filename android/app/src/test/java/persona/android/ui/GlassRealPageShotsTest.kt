@file:OptIn(androidx.compose.material3.adaptive.ExperimentalMaterial3AdaptiveApi::class)

package persona.android.ui

import android.app.Application
import android.graphics.Bitmap
import android.os.PowerManager
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.material3.adaptive.currentWindowAdaptiveInfo
import androidx.compose.material3.adaptive.layout.calculatePaneScaffoldDirective
import androidx.compose.runtime.Composable
import androidx.compose.runtime.snapshots.Snapshot
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.asAndroidBitmap
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.test.captureToImage
import androidx.compose.ui.test.getUnclippedBoundsInRoot
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onAllNodesWithTag
import androidx.compose.ui.test.onNodeWithTag
import androidx.navigation3.runtime.NavBackStack
import androidx.navigation3.runtime.NavEntry
import androidx.navigation3.runtime.NavKey
import androidx.navigation3.runtime.entryProvider
import androidx.navigation3.ui.NavDisplay
import androidx.test.core.app.ApplicationProvider
import androidx.test.ext.junit.runners.AndroidJUnit4
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.flowOf
import org.junit.Assert.assertEquals
import org.junit.Rule
import org.junit.Test
import org.junit.rules.RuleChain
import org.junit.runner.RunWith
import org.robolectric.Shadows
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.android.navigation.SettingsSheetNavigator
import persona.core.contracts.InboxEventFields
import persona.core.domain.inbox.InboxItem
import persona.core.contracts.api.ConnectionState
import persona.core.contracts.api.InboxEntry
import persona.core.designsystem.AppStyle
import persona.core.designsystem.PersonaTheme
import persona.core.designsystem.glassPageChrome
import persona.core.designsystem.rememberPageTransitions
import persona.core.navigation.AppTab
import persona.core.navigation.InboxRoute
import persona.core.navigation.Navigator
import persona.core.navigation.SettingsPage
import persona.core.navigation.SettingsPageRoute
import persona.core.navigation.SettingsRoute
import persona.feature.inbox.InboxConversations
import persona.feature.inbox.InboxEventRoute
import persona.feature.inbox.InboxEventScreen
import persona.core.inbox.InboxPreferences
import persona.core.inbox.InboxRecord
import persona.feature.inbox.InboxRepository
import persona.feature.inbox.InboxScreen
import persona.core.inbox.InboxSnapshot
import persona.core.inbox.LarkConversation
import persona.core.inbox.ReplyDraft
import persona.core.inbox.ReplyRequest
import persona.core.inbox.ReplyTarget
import persona.feature.settings.SettingsScreen
import persona.feature.settings.demoSettingsServices
import persona.feature.settings.toInitialPage
import java.io.File
import kotlin.math.abs

private const val BREATH_MS = 1_000L

/**
 * Real Glass pushes inside NavDisplay, on a 1 ms frame clock: the settings sheet into Diagnostics, and the phone
 * page slot from Inbox into an item. Light and dark, at 87 / 175 / 262 ms and settled.
 *
 * The pools behind Glass follow the frame clock, so two rests a few hundred milliseconds apart differ across the
 * whole page. Battery saver holds those pools on the rest pose the product already uses, and the page transition
 * still runs. Later rests are one Needs You breath apart (1 s), so that ring is the same frame too. After a push
 * and a pop the resting picture matches the one from before it: zero pixels differ. PNGs go to
 * `build/glass-real-shots/` (or `-Dpersona.shots=<dir>`).
 */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(application = Application::class, sdk = [35], qualifiers = "en-w400dp-h800dp-mdpi")
class GlassRealPageShotsTest {
    val rule = createComposeRule()

    @get:Rule val host = RuleChain.outerRule(ComponentActivityHostRule()).around(rule)

    @Test fun settingsLight() = settings(dark = false)

    @Test fun settingsDark() = settings(dark = true)

    @Test fun inboxLight() = inbox(dark = false)

    @Test fun inboxDark() = inbox(dark = true)

    private fun settings(dark: Boolean) {
        useOneMillisecondFrames()
        val services = demoSettingsServices(ApplicationProvider.getApplicationContext<Application>().resources)
        val stack = NavBackStack<NavKey>(SettingsRoute)
        val navigator = SettingsSheetNavigator(stack, close = {}, outside = object : Navigator {
            override fun navigate(key: NavKey) = Unit
            override fun back() = Unit
            override fun selectTab(tab: AppTab) = Unit
        })
        rule.mainClock.autoAdvance = false
        rule.setContent {
            HoldAmbientLight()
            PersonaTheme(darkTheme = dark, reduceMotion = false, style = AppStyle.Glass) {
                Box(Modifier.fillMaxSize().testTag("stage")) {
                    val directive = calculatePaneScaffoldDirective(currentWindowAdaptiveInfo())
                    SettingsSheetPanes(
                        stack,
                        navigator::back,
                        regular = false,
                        directive.onePane(),
                        emptyList(),
                        entries = { key ->
                            when (key) {
                                is SettingsPageRoute -> NavEntry(key) {
                                    Box(Modifier.fillMaxSize().testTag("page-front")) {
                                        SettingsScreen(
                                            services,
                                            initialPage = key.page.toInitialPage(),
                                            onBack = navigator::back,
                                        )
                                    }
                                }
                                else -> NavEntry(key) {
                                    Box(Modifier.fillMaxSize().testTag("page-back")) {
                                        SettingsScreen(services, onOpen = { navigator.navigate(SettingsPageRoute(it)) })
                                    }
                                }
                            }
                        },
                    ) { emptyMap() }
                }
            }
        }
        shoot("settings", if (dark) "dark" else "light", push = {
            navigator.navigate(SettingsPageRoute(SettingsPage.Diagnostics))
        }, pop = navigator::back)
    }

    private fun inbox(dark: Boolean) {
        useOneMillisecondFrames()
        val records = GlassShotInbox(listOf(glassInboxItem("first", "Approve Friday launch")))
        val stack = NavBackStack<NavKey>(InboxRoute)
        val navigator = object : Navigator {
            override fun navigate(key: NavKey) {
                if (stack.lastOrNull() != key) stack.add(key)
            }
            override fun back() {
                if (stack.size > 1) stack.removeAt(stack.lastIndex)
            }
            override fun selectTab(tab: AppTab) = Unit
        }
        rule.mainClock.autoAdvance = false
        rule.setContent {
            HoldAmbientLight()
            PersonaTheme(darkTheme = dark, reduceMotion = false, style = AppStyle.Glass) {
                Box(Modifier.fillMaxSize().testTag("stage")) {
                    InboxSlot(stack, navigator, records)
                }
            }
        }
        shoot("inbox", if (dark) "dark" else "light", push = {
            navigator.navigate(InboxEventRoute("first"))
        }, pop = navigator::back)
    }

    @Composable
    private fun InboxSlot(stack: NavBackStack<NavKey>, navigator: Navigator, inbox: InboxRepository) {
        val transitions = rememberPageTransitions()
        NavDisplay(
            backStack = stack,
            modifier = Modifier.fillMaxSize(),
            onBack = navigator::back,
            transitionSpec = { transitions.push },
            popTransitionSpec = { transitions.pop },
            predictivePopTransitionSpec = { transitions.predictivePop },
            entryProvider = entryProvider {
                entry<InboxRoute> {
                    Box(Modifier.fillMaxSize().glassPageChrome().testTag("page-back")) {
                        InboxScreen(inbox, navigator)
                    }
                }
                entry<InboxEventRoute> { route ->
                    Box(Modifier.fillMaxSize().glassPageChrome().testTag("page-front")) {
                        InboxEventScreen(inbox, route, navigator)
                    }
                }
            },
        )
    }

    private fun shoot(surface: String, mode: String, push: () -> Unit, pop: () -> Unit) {
        // Past the one-shot fades. Ambient pools are already on the battery-saver rest pose.
        rule.mainClock.advanceTimeBy(BREATH_MS)
        val beforeClock = rule.mainClock.currentTime
        val before = capture()
        save(surface, mode, "rest-before", before)
        // Same breath phase, no navigation. A leftover card would show up here.
        rule.mainClock.advanceTimeBy(BREATH_MS)
        val still = capture()
        save(surface, mode, "rest-still", still)
        val pushStart = begin("page-front", push)
        for (ms in intArrayOf(87, 175, 262)) {
            rule.mainClock.advanceTimeBy(pushStart + ms - rule.mainClock.currentTime)
            assertEquals("$surface $mode push elapsed", ms.toLong(), rule.mainClock.currentTime - pushStart)
            save(surface, mode, "push-${ms.toString().padStart(3, '0')}", capture())
        }
        rule.mainClock.advanceTimeBy(pushStart + 400 - rule.mainClock.currentTime)
        save(surface, mode, "push-settled", capture())
        val popStart = begin("page-front", pop)
        for (ms in intArrayOf(87, 175, 262)) {
            rule.mainClock.advanceTimeBy(popStart + ms - rule.mainClock.currentTime)
            assertEquals("$surface $mode pop elapsed", ms.toLong(), rule.mainClock.currentTime - popStart)
            save(surface, mode, "pop-${ms.toString().padStart(3, '0')}", capture())
        }
        rule.mainClock.advanceTimeBy(popStart + 400 - rule.mainClock.currentTime)
        save(surface, mode, "pop-settled", capture())
        alignBreath(beforeClock)
        val after = capture()
        save(surface, mode, "rest-after", after)
        val noise = differing(before, still)
        val residue = differing(before, after)
        val worstNoise = maxChannelDelta(before, still)
        val worstResidue = maxChannelDelta(before, after)
        println("REST $surface $mode noise=$noise residue=$residue worstNoise=$worstNoise worstResidue=$worstResidue")
        assertEquals("$surface $mode front gone", 0, rule.onAllNodesWithTag("page-front").fetchSemanticsNodes().size)
        assertEquals(
            "$surface $mode rest noise=$noise (max $worstNoise) residue=$residue (max $worstResidue)",
            "0/0",
            "$noise/$residue",
        )
    }

    /** Battery saver, on the context this tree reads, before the pools subscribe to the clock. */
    @Composable
    private fun HoldAmbientLight() {
        val power = LocalContext.current.getSystemService(PowerManager::class.java)
            ?: error("no power manager")
        Shadows.shadowOf(power).setIsPowerSaveMode(true)
    }

    /** Land on the same Needs You breath as [mark]. The breath restarts every [BREATH_MS]. */
    private fun alignBreath(mark: Long) {
        val elapsed = rule.mainClock.currentTime - mark
        val extra = (BREATH_MS - elapsed % BREATH_MS) % BREATH_MS
        if (extra != 0L) rule.mainClock.advanceTimeBy(extra)
    }

    private fun capture(): Bitmap = rule.onNodeWithTag("stage").captureToImage().asAndroidBitmap()

    private fun save(surface: String, mode: String, name: String, bitmap: Bitmap) {
        val out = File(System.getProperty("persona.shots") ?: "build/glass-real-shots").also { it.mkdirs() }
        File(out, "$surface-$mode-$name.png").outputStream().use {
            bitmap.compress(Bitmap.CompressFormat.PNG, 100, it)
        }
    }

    private fun differing(before: Bitmap, after: Bitmap): Int {
        var different = 0
        for (y in 0 until before.height) {
            for (x in 0 until before.width) {
                if (before.getPixel(x, y) != after.getPixel(x, y)) different++
            }
        }
        return different
    }

    /** Largest per-channel change. A leftover dim moves flat pixels by more than a blur's rounding. */
    private fun maxChannelDelta(before: Bitmap, after: Bitmap): Int {
        var worst = 0
        for (y in 0 until before.height) {
            for (x in 0 until before.width) {
                val a = before.getPixel(x, y)
                val b = after.getPixel(x, y)
                if (a == b) continue
                for (shift in intArrayOf(16, 8, 0)) {
                    val delta = kotlin.math.abs(((a shr shift) and 0xFF) - ((b shr shift) and 0xFF))
                    if (delta > worst) worst = delta
                }
            }
        }
        return worst
    }

    /**
     * 1 ms frames need a patched clock. Compose UI test 1.12.1 always builds a 16 ms `TestMonotonicFrameClock`;
     * `effectContext` does not replace it, and `advanceTimeBy(1, ignoreFrameDuration = true)` does not dispatch a frame.
     * `frameDelayNanos` has no setter.
     */
    private fun useOneMillisecondFrames() {
        rule.mainClock.autoAdvance = false
        val environment = declaredField(rule.javaClass, "environment").get(rule)
            ?: error("compose test environment is not ready")
        val frameClock = declaredField(environment.javaClass, "frameClock").get(environment)
            ?: error("frame clock is missing")
        putLong(frameClock, "frameDelayNanos", 1_000_000L)
        putLong(rule.mainClock, "frameDelayMillis", 1L)
    }

    /** Clock time of the frame where [tag] is still at its initial offset. The next frame is the first motion. */
    private fun begin(tag: String, change: () -> Unit): Long {
        rule.runOnIdle { change() }
        Snapshot.sendApplyNotifications()
        // A frame-to-frame threshold of 1 px misses a slow first step. The start is the last sample still
        // within half a point of where the page first appeared.
        var origin: Float? = null
        var originClock = rule.mainClock.currentTime
        var last = origin
        var guard = 0
        while (guard++ < 120) {
            rule.mainClock.advanceTimeBy(1)
            val left = leftOf(tag) ?: continue
            val start = origin
            last = left
            if (start == null) {
                origin = left
                originClock = rule.mainClock.currentTime
                continue
            }
            if (abs(left - start) <= 0.5f) {
                originClock = rule.mainClock.currentTime
                continue
            }
            return originClock
        }
        error("$tag did not move from $origin (last $last)")
    }

    private fun leftOf(tag: String): Float? {
        if (rule.onAllNodesWithTag(tag).fetchSemanticsNodes().isEmpty()) return null
        return rule.onNodeWithTag(tag).getUnclippedBoundsInRoot().left.value
    }

    private fun declaredField(start: Class<*>, name: String): java.lang.reflect.Field {
        var type: Class<*>? = start
        while (type != null) {
            try {
                return type.getDeclaredField(name).apply { isAccessible = true }
            } catch (_: NoSuchFieldException) {
                type = type.superclass
            }
        }
        error("no field $name on ${start.name}")
    }

    private fun putLong(target: Any, name: String, value: Long) {
        val field = declaredField(target.javaClass, name)
        val unsafeClass = Class.forName("sun.misc.Unsafe")
        val unsafe = unsafeClass.getDeclaredField("theUnsafe").apply { isAccessible = true }.get(null)
        val offset = unsafeClass.getMethod("objectFieldOffset", java.lang.reflect.Field::class.java)
            .invoke(unsafe, field) as Long
        unsafeClass.getMethod("putLong", Any::class.java, java.lang.Long.TYPE, java.lang.Long.TYPE)
            .invoke(unsafe, target, offset, value)
    }
}

private class GlassShotInbox(records: List<InboxRecord>) : InboxRepository {
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
    override suspend fun downloadFile(file: persona.core.inbox.LarkPart.File): String = error("not used")
    override suspend fun item(itemId: String): InboxRecord? = snapshot.value.records.firstOrNull { it.item.id == itemId }
}

private fun glassInboxItem(id: String, title: String) = InboxRecord(
    InboxItem(
        id, account = "chat", kind = "group", title = title, sender = "Avery",
        preview = "The launch checklist is ready.", summary = "Ready for a decision", tier = "action", status = "action",
        updatedAt = "2026-11-13T10:00:00Z", version = "v-$id",
    ),
    source = "lark", sourceLabel = "Chat connector", feedId = "shared-room", isGroup = true,
    groupName = "Planning room", count = 2, read = false, event = InboxEventFields(status = "action"),
)
