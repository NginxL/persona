package persona.feature.assistant

import android.content.Context
import android.content.res.Configuration
import android.graphics.Bitmap
import androidx.test.core.app.ApplicationProvider
import androidx.test.ext.junit.runners.AndroidJUnit4
import kotlinx.coroutines.CompletableDeferred
import kotlinx.coroutines.test.StandardTestDispatcher
import kotlinx.coroutines.test.TestScope
import kotlinx.coroutines.test.runCurrent
import kotlinx.coroutines.test.runTest
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import persona.core.contracts.Session
import persona.core.contracts.api.ExtensionIconPath
import persona.core.contracts.api.HubExtension
import persona.core.contracts.api.HubManager
import persona.core.contracts.api.ManagerRoster
import persona.core.designsystem.Brand
import java.io.ByteArrayOutputStream
import java.util.Locale

/**
 * #10358: a conversation an extension opened is marked with that extension's own icon, by the web's and iOS's rules
 * (`opened_by` else `origin_ref`; the conversation's manager's copy; the hub's icon routes only; the Lark extension keeps
 * Lark's mark), and named after it only while that icon is drawn. Made-up extensions only.
 */
@RunWith(AndroidJUnit4::class)
@Config(sdk = [35])
class ConversationOpenerTest {
    private val context get() = ApplicationProvider.getApplicationContext<Context>()
    private val installed = "/api/v1/extensions/chat-bridge/-/icon?v=1.2.0"
    private val catalog = "/api/v1/extensions/-/catalog/main/chat-bridge/icon?sha256=" + "ab".repeat(32)
    private val bridge = HubExtension(id = "chat-bridge", name = " Chat Bridge ", iconUrl = installed,
        contributes = mapOf("messaging" to listOf("examplechat")))
    private val larkExtension = HubExtension(id = "lark-ext", name = "Lark", iconUrl = "/api/v1/extensions/lark-ext/-/icon?v=3",
        contributes = mapOf("connectors" to listOf("lark"), "skills" to listOf("lark-docs")))
    private val anyManager: (String?) -> Boolean = { true }
    private val bridgeOpened = ConversationOpener("chat-bridge")

    private fun resourcesIn(language: String) = context.createConfigurationContext(
        Configuration(context.resources.configuration).apply { setLocale(Locale.forLanguageTag(language)) }).resources

    @Test fun theOpenerIsOpenedByElseOriginRefForTheTwoExternalOriginsOnly() {
        assertEquals(ConversationOpener("chat-bridge", "work"), ConversationOpener.of("lark_agent", " chat-bridge ", "other", "work"))
        assertEquals(ConversationOpener("chat-bridge"), ConversationOpener.of("extension", null, "chat-bridge"))
        assertEquals("a blank opened_by falls to origin_ref", ConversationOpener("chat-bridge"), ConversationOpener.of(" lark_agent ", "  ", "chat-bridge"))
        assertNull("neither: a conversation the backfill judged", ConversationOpener.of("lark_agent", null, null))
        assertNull(ConversationOpener.of("extension", "", " "))
        listOf(null, "", "user", "channel", "inbox", "task", "spawned", "something_new").forEach { origin ->
            assertNull("no opener for $origin", ConversationOpener.of(origin, "chat-bridge", "chat-bridge"))
        }
        val session = Session(machineId = "hub", sessionId = "s", origin = "lark_agent", originRef = "chat-bridge", openedBy = "chat-bridge", manager = "work")
        assertEquals(ConversationOpener("chat-bridge", "work"), ConversationOpener.of(session))
        assertNull("the row's own origin wins (the background supervisor is drawn as a task's)", ConversationOpener.of(session, "task"))
    }

    @Test fun theIconIsTheInstalledExtensionsOwnOnTheHubsRoutes() {
        assertEquals(OpenerIcon(installed, null, "Chat Bridge"), openerIcon(listOf(bridge), bridgeOpened, Brand.Lark, anyManager))
        assertEquals(OpenerIcon(catalog, "work", "Chat Bridge"),
            openerIcon(listOf(bridge.copy(iconUrl = catalog, manager = "work")), bridgeOpened, null, anyManager))
        listOf("https://cdn.example.com/chat-bridge.png", "/api/v1/extensions/chat-bridge/-/icon", "/api/v1/extensions/chat-bridge/-/icon?v=1#x",
            " $installed", "/api/v1/extensions/../-/icon?v=1", "/api/v1/extensions/-/catalog/main/chat-bridge/icon?sha256=abc",
            "/api/v1/extensions/chat-bridge/-/ui/image?sig=1", "").forEach { url ->
            assertNull("not a hub icon route: $url", openerIcon(listOf(bridge.copy(iconUrl = url)), bridgeOpened, null, anyManager))
        }
        // A resolver that knows the hub's address takes its absolute routes as the path (HttpExtensionManagement.iconPath).
        val absolute = "https://hub.example.com$installed"
        assertEquals(installed, openerIcon(listOf(bridge.copy(iconUrl = absolute)), bridgeOpened, null, anyManager,
            iconPath = { it.removePrefix("https://hub.example.com").takeIf(ExtensionIconPath::matches) })?.path)
        assertNull("no name: the icon, with the origin's words", openerIcon(listOf(bridge.copy(name = "  ")), bridgeOpened, null, anyManager)?.name)
    }

    @Test fun theOpenerMustBeTheConversationsManagers() {
        val roster = ManagerRoster(listOf(HubManager("assistant", "Home", isDefault = true), HubManager("work", "Work")), "assistant")
        val list = listOf(bridge.copy(manager = "work", iconUrl = "/api/v1/extensions/chat-bridge/-/icon?v=9"), bridge.copy(manager = "assistant"))
        fun pick(conversation: String?, shown: String, r: ManagerRoster? = roster) =
            openerIcon(list, ConversationOpener("chat-bridge", conversation), null, { openerSameManager(it, conversation, shown, r) })
        assertEquals("work", pick("work", "assistant")?.manager)
        assertEquals("assistant", pick("assistant", "work")?.manager)
        assertEquals("no stamp: the shown assistant's", "work", pick(null, "work")?.manager)
        assertNull("installed only under another manager", openerIcon(listOf(bridge.copy(manager = "work")), ConversationOpener("chat-bridge", "assistant"), null,
            { openerSameManager(it, "assistant", "assistant", roster) }))
        assertTrue("an unstamped extension is the default's", openerSameManager(null, "assistant", "work", roster))
        assertFalse(openerSameManager(null, "work", "work", roster))
        val one = ManagerRoster(listOf(HubManager("assistant", isDefault = true)), "assistant")
        assertTrue("one manager: every one is", openerSameManager("elsewhere", null, "assistant", one))
        assertTrue("no roster yet", openerSameManager("elsewhere", "work", "assistant", null))
    }

    @Test fun theLarkExtensionKeepsLarksMarkForLarkAgent() {
        assertNull("its own /agent: Lark's mark, its icon never asked", openerIcon(listOf(larkExtension), ConversationOpener("lark-ext"), Brand.Lark, anyManager))
        assertNull("a messaging source of Lark's brand", openerIcon(listOf(bridge.copy(contributes = mapOf("messaging" to listOf("larkcli")))),
            bridgeOpened, Brand.Lark, anyManager))
        assertNotNull("a conversation it opened as an extension draws its icon", openerIcon(listOf(larkExtension), ConversationOpener("lark-ext"), null, anyManager))
        assertNotNull("another brand's extension reusing lark.agent is not Lark", openerIcon(listOf(bridge), bridgeOpened, Brand.Lark, anyManager))
        assertFalse(larkExtension.standsFor(null))
        assertFalse("only connectors and messaging count", bridge.copy(contributes = mapOf("skills" to listOf("lark"))).standsFor(Brand.Lark))
        assertTrue(larkExtension.standsFor(AssistantConversationOrigin.LarkAgent.brand))
        assertNull(AssistantConversationOrigin.Extension.brand)
    }

    @Test fun everyFallbackKeepsTheOriginsMark() {
        assertNull("the list is not read yet", openerIcon(null, bridgeOpened, null, anyManager))
        assertNull("not installed (or removed)", openerIcon(emptyList(), bridgeOpened, null, anyManager))
        assertNull("another extension", openerIcon(listOf(bridge.copy(id = "chat-bridge-2")), bridgeOpened, null, anyManager))
        assertNull("no icon", openerIcon(listOf(bridge.copy(iconUrl = null)), bridgeOpened, null, anyManager))
        assertNull("only public artwork", openerIcon(listOf(bridge.copy(iconUrl = "https://example.com/a.png")), bridgeOpened, null, anyManager))
        assertNull("another manager's", openerIcon(listOf(bridge), bridgeOpened, null, { false }))
    }

    @Test fun theWordsNameTheExtensionOnlyWhileItsIconIsDrawn() {
        val zh = context.resources
        assertEquals("来自 Chat Bridge", originMarkLabel(AssistantConversationOrigin.LarkAgent, "Chat Bridge", zh))
        assertEquals("来自 Chat Bridge", originMarkLabel(AssistantConversationOrigin.Extension, "Chat Bridge", zh))
        assertEquals("来自 飞书 /agent", originMarkLabel(AssistantConversationOrigin.LarkAgent, null, zh))
        assertEquals("来自扩展", originMarkLabel(AssistantConversationOrigin.Extension, null, zh))
        assertEquals("From Chat Bridge", originMarkLabel(AssistantConversationOrigin.LarkAgent, "Chat Bridge", resourcesIn("en")))
        assertEquals("From an extension", originMarkLabel(AssistantConversationOrigin.Extension, null, resourcesIn("en")))
        assertEquals("Chat Bridgeから", originMarkLabel(AssistantConversationOrigin.LarkAgent, "Chat Bridge", resourcesIn("ja")))
    }

    /** The hub as the store sees it, answering at once, or once [answer] completes with the list it had when asked. */
    private class FakeHub : OpenerIcons.Source {
        var lists = true
        var list = emptyList<HubExtension>()
        var listFails = false
        var iconFails = false
        var listReads = 0
        var answer: CompletableDeferred<Unit>? = null
        val iconReads = mutableListOf<Pair<String, String?>>()
        override val listsExtensions get() = lists
        override suspend fun extensions(): List<HubExtension> {
            listReads++
            val asked = list
            val fails = listFails
            answer?.await()
            if (fails) error("offline")
            return asked
        }
        override fun iconPath(url: String) = url.takeIf(ExtensionIconPath::matches)
        override suspend fun icon(path: String, manager: String?): ByteArray {
            iconReads += path to manager
            if (iconFails) error("404")
            return png()
        }
    }

    private fun TestScope.store(hub: FakeHub, clock: () -> Long) =
        OpenerIcons(backgroundScope, hub, io = StandardTestDispatcher(testScheduler), now = clock)

    /** What the mark draws for [opener] under [origin], as the composable reads it: the icon's name once its picture is in. */
    private fun OpenerIcons.drawnName(opener: ConversationOpener, origin: AssistantConversationOrigin = AssistantConversationOrigin.LarkAgent): String? =
        icon(opener, origin, { true })?.takeIf { drawn(it).image != null }?.name

    @Test fun theListIsReadOnceAMarkNeedsItAndAgainAfterAMinuteOrAChange() = runTest {
        val hub = FakeHub().apply { list = listOf(bridge); lists = false }
        var now = 0L
        val store = store(hub) { now }
        runCurrent()
        assertEquals("nothing at launch", 0, hub.listReads)
        store.ensure()
        runCurrent()
        assertEquals("not before the hub says it lists extensions", 0, hub.listReads)
        hub.lists = true
        store.stale() // the hello's features
        runCurrent()
        assertEquals(1, hub.listReads)
        store.ensure()
        runCurrent()
        assertEquals("read once", 1, hub.listReads)
        now += OpenerIcons.FRESH_MS - 1
        store.ensure()
        runCurrent()
        assertEquals(1, hub.listReads)
        now += 1
        store.ensure()
        runCurrent()
        assertEquals("a minute later", 2, hub.listReads)
        store.stale() // an install, an upgrade or a removal
        runCurrent()
        assertEquals(3, hub.listReads)
        hub.listFails = true
        store.stale()
        runCurrent()
        assertEquals(4, hub.listReads)
        assertEquals("a failed read keeps the last list", listOf(bridge), store.extensions)
        hub.listFails = false
        hub.list = emptyList()
        store.stale()
        runCurrent()
        assertNull("removed: the origin's mark", store.icon(bridgeOpened, AssistantConversationOrigin.LarkAgent, { true }))
    }

    @Test fun eachIconIsReadOnceFromItsOwnManagerAndNamedOnlyOnceDrawn() = runTest {
        val hub = FakeHub().apply { list = listOf(bridge.copy(manager = "work"), larkExtension) }
        val store = store(hub) { 0L }
        val opener = ConversationOpener("chat-bridge", "work")
        assertNull("before the list", store.drawnName(opener))
        store.ensure()
        runCurrent()
        val icon = checkNotNull(store.icon(opener, AssistantConversationOrigin.LarkAgent, { true })) { "the conversation's manager's copy" }
        assertNull("listed, not drawn yet: the origin's words", store.drawnName(opener))
        store.load(icon, 42)
        store.load(icon, 42)
        assertNull("while it loads", store.drawnName(opener))
        runCurrent()
        assertEquals(listOf(installed to "work"), hub.iconReads)
        assertEquals("Chat Bridge", store.drawnName(opener))
        store.load(icon, 42)
        runCurrent()
        assertEquals("kept: read once", 1, hub.iconReads.size)
        // The Lark extension's own /agent never asks for its icon: there is none to ask for.
        assertNull(store.icon(ConversationOpener("lark-ext"), AssistantConversationOrigin.LarkAgent, { true }))
    }

    @Test fun aFailedIconKeepsTheOriginsMarkUntilTheListIsReadAgain() = runTest {
        val hub = FakeHub().apply { list = listOf(bridge); iconFails = true }
        val store = store(hub) { 0L }
        store.ensure()
        runCurrent()
        val icon = store.icon(bridgeOpened, AssistantConversationOrigin.LarkAgent, { true })!!
        store.load(icon, 42)
        runCurrent()
        assertNull("failed: the origin's mark and words", store.drawnName(bridgeOpened))
        store.load(icon, 42)
        runCurrent()
        assertEquals("not asked again", 1, hub.iconReads.size)
        hub.iconFails = false
        val revision = store.revision
        store.stale()
        runCurrent()
        assertTrue("the same list read again: the marks on screen ask again", store.revision > revision)
        store.load(icon, 42)
        runCurrent()
        assertEquals(2, hub.iconReads.size)
        assertEquals("Chat Bridge", store.drawnName(bridgeOpened))
        // A picture that is not a PNG is none (decodeOpenerIcon).
        assertNull(decodeOpenerIcon(byteArrayOf(1, 2, 3), 42))
        assertNull(decodeOpenerIcon(ByteArray(MAX_OPENER_ICON_BYTES + 1), 42))
        assertNotNull(decodeOpenerIcon(png(), 42))
    }

    @Test fun aChangeWhileTheListIsReadHasItReadAgainOnceThatReadAnswers() = runTest {
        val hub = FakeHub()
        val store = store(hub) { 0L }
        val first = CompletableDeferred<Unit>()
        hub.answer = first
        store.ensure()
        runCurrent()
        assertEquals(1, hub.listReads)
        hub.list = listOf(bridge) // installed while the first read is on its way
        hub.answer = null
        store.stale()
        store.ensure()
        runCurrent()
        assertEquals("one read at a time", 1, hub.listReads)
        first.complete(Unit)
        runCurrent()
        assertEquals("read again: the first answer predates the install", 2, hub.listReads)
        assertEquals(listOf(bridge), store.extensions)
        store.ensure()
        runCurrent()
        assertEquals("the second answer is fresh", 2, hub.listReads)
        // A read that fails after the change is asked again too, not a minute later.
        val failing = CompletableDeferred<Unit>()
        hub.answer = failing
        hub.listFails = true
        store.stale()
        runCurrent()
        assertEquals(3, hub.listReads)
        hub.answer = null
        hub.listFails = false
        hub.list = emptyList() // removed
        store.stale()
        failing.complete(Unit)
        runCurrent()
        assertEquals(4, hub.listReads)
        assertNull("removed: the origin's mark", store.icon(bridgeOpened, AssistantConversationOrigin.LarkAgent, { true }))
    }

    @Test fun anotherHubForgetsWhatTheLastOneRead() = runTest {
        val hub = FakeHub().apply { list = listOf(bridge) }
        val store = store(hub) { 0L }
        store.ensure()
        runCurrent()
        val icon = store.icon(bridgeOpened, AssistantConversationOrigin.LarkAgent, { true })!!
        store.load(icon, 42)
        runCurrent()
        assertEquals("Chat Bridge", store.drawnName(bridgeOpened))
        val generation = store.generation
        store.reset()
        assertTrue("the marks ask the new hub", store.generation > generation)
        assertNull(store.extensions)
        assertNull(store.drawnName(bridgeOpened))
        store.stale()
        runCurrent()
        assertEquals("nothing read until a mark on the new hub asks", 1, hub.listReads)
        store.ensure()
        runCurrent()
        assertEquals(2, hub.listReads)
    }

    private companion object {
        /** A made-up 64 px icon. */
        fun png(): ByteArray {
            val bitmap = Bitmap.createBitmap(64, 64, Bitmap.Config.ARGB_8888).apply { eraseColor(android.graphics.Color.GREEN) }
            return ByteArrayOutputStream().use { bitmap.compress(Bitmap.CompressFormat.PNG, 100, it); it.toByteArray() }
        }
    }
}
