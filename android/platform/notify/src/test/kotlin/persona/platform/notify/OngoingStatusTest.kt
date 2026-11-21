package persona.platform.notify

import persona.core.contracts.FleetItem
import persona.core.contracts.FleetState
import persona.core.contracts.FleetStats
import persona.core.contracts.Interaction
import persona.core.contracts.SessionKey
import persona.core.contracts.api.ConnectionState
import kotlinx.coroutines.ExperimentalCoroutinesApi
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.launch
import kotlinx.coroutines.test.advanceTimeBy
import kotlinx.coroutines.test.runCurrent
import kotlinx.coroutines.test.runTest
import org.junit.Assert.*
import org.junit.Test

@OptIn(ExperimentalCoroutinesApi::class)
/** Build.VERSION.SDK_INT_FULL of Android 16 QPR1: SDK_INT * 100 000 + the minor release. */
private const val Android16Qpr1 = 36 * 100_000 + 1

class OngoingStatusTest {
    private val permission = FleetItem("mac/tests", "补齐网络层的单元测试", "MacBook · weather › tests", "permission",
        "Bash: swift test", "ix_build", 100.0)
    private val plan = FleetItem("studio/photos", "照片同步脚本支持 HEIC", "Mac Studio · photo-sync", "plan", "支持 HEIC", "ix_photos", 90.0)
    private val question = FleetItem("macbook/q", "问题", "MacBook", "question", "用哪个框架？", "ix_q", 120.0)
    private val working = FleetItem("server/orders", "排查 /orders 接口变慢", "服务器 · shop-api › perf", "working", since = 1_700.0)

    @Test fun summarizesTheFleetLikeTheLiveActivity() {
        val fleet = FleetState("needsYou", needsYou = 4, working = 2, unread = 1, items = listOf(plan, permission, question))
        val status = OngoingStatus.from(ConnectionState.Online, fleet, details = true)
        assertEquals("4 个需要你", status.title)
        assertEquals("照片同步脚本支持 HEIC · 计划：支持 HEIC", status.text)
        assertEquals("2 个在工作 · 1 个做完了", status.subText)
        assertEquals(listOf(
            "照片同步脚本支持 HEIC · 计划：支持 HEIC（Mac Studio · photo-sync）",
            "补齐网络层的单元测试 · 授权：Bash: swift test（MacBook · weather › tests）",
            "问题 · 用哪个框架？（MacBook）",
            "+4 个",
        ).joinToString("\n"), status.lines)
        // The lock screen never gets names or content.
        assertEquals("有个计划等你批准 · 2 个在工作 · 1 个做完了", status.publicText)
        assertFalse(status.publicText!!.contains("HEIC"))
        assertEquals("💬4", status.criticalText)
        assertNull(status.workingSince)
        assertNull(status.open)
        // The first plan gets "View" / "Approve"; approving needs an unlocked phone, looking does not.
        val action = status.action!!
        assertEquals("ix_photos", action.interaction)
        assertEquals(SessionKey("studio", "photos"), action.session)
        assertEquals(listOf(OngoingButton(R.string.notify_action_view, null, false), OngoingButton(R.string.notify_action_approve, "approve_edits", true)), action.buttons)
        // Stats, the island's sides and the assistant are not drawn: they must not cause redraws.
        assertEquals(status, OngoingStatus.from(ConnectionState.Online, fleet.copy(
            stats = FleetStats(tpm = 1234), assistant = true, left = "tpm"), details = true))
    }

    /** A card with fields to fill in (a password) cannot be allowed from here: only Deny (#4987). */
    @Test fun permissionWithRequiredFieldsGetsOnlyDeny() {
        val card = persona.core.contracts.JsonCodec.decodeFromString<persona.core.contracts.Interaction>(
            """{"id":"ix_build","kind":"permission","fields":[{"key":"password","label":"Password","secret":true}]}""")
        val status = OngoingStatus.from(ConnectionState.Online, FleetState("needsYou", needsYou = 1, items = listOf(permission)),
            pending = listOf(card))
        assertEquals(listOf(OngoingButton(R.string.notify_action_deny, "deny", false)), status.action!!.buttons)
    }

    @Test fun permissionsGetDenyAndAllowAndOneAgentOpensItself() {
        val fleet = FleetState("needsYou", needsYou = 1, items = listOf(permission))
        val status = OngoingStatus.from(ConnectionState.Online, fleet, details = true,
            pending = listOf(Interaction("ix_build", kind = "permission")))
        assertEquals(SessionKey("mac", "tests"), status.open)
        assertNull(status.subText)
        assertEquals("补齐网络层的单元测试 · 授权：Bash: swift test（MacBook · weather › tests）", status.lines)
        assertEquals(listOf(OngoingButton(R.string.notify_action_deny, "deny", false), OngoingButton(R.string.notify_action_allow, "allow", true)), status.action!!.buttons)
        // A question has no buttons here: it needs the conversation.
        assertNull(OngoingStatus.from(ConnectionState.Online, FleetState("needsYou", needsYou = 1, items = listOf(question))).action)
    }

    @Test fun aSecretHandoffOrUnresolvedPermissionGetsOnlyView() {
        val handoff = persona.core.contracts.JsonCodec.decodeFromString<Interaction>(
            """{"id":"ix_build","kind":"permission","tool_name":"secret_handoff","tool_input":{"variables":[{"name":"TOKEN"}]}}""")
        for (cards in listOf(emptyList(), listOf(handoff))) {
            val status = OngoingStatus.from(ConnectionState.Online,
                FleetState("needsYou", needsYou = 1, items = listOf(permission)), pending = cards)
            assertEquals(listOf(OngoingButton(R.string.notify_action_view, null, false)), status.action!!.buttons)
            assertTrue(status.action!!.buttons.none { it.key == "allow" || it.key == "always" })
        }
    }

    @Test fun theBodyAboveTheButtonsIsTheRequestTheySettle() {
        // The hub puts the oldest request first: an older question must not stand above another agent's "Deny" / "Allow".
        val fleet = FleetState("needsYou", needsYou = 2, items = listOf(question, permission))
        val status = OngoingStatus.from(ConnectionState.Online, fleet, details = true)
        assertEquals("ix_build", status.action!!.interaction)
        assertEquals("补齐网络层的单元测试 · 授权：Bash: swift test", status.text)
        val private = OngoingStatus.from(ConnectionState.Online, fleet)
        assertEquals("ix_build", private.action!!.interaction)
        assertEquals("有个授权等你", private.text)
        assertTrue(private.publicText!!.startsWith("有个授权等你"))
    }

    @Test fun onlyWorkingRunsTheClockAndQuietOrEmptyFallsBackToTheConnectionLine() {
        val fleet = FleetState("working", working = 1, items = listOf(working), workingSince = 1_700.0)
        val status = OngoingStatus.from(ConnectionState.Online, fleet, details = true)
        assertEquals("1 个在工作", status.title)
        assertEquals("排查 /orders 接口变慢 · 工作中", status.text)
        assertEquals(1_700_000L, status.workingSince)
        assertEquals("⚙1", status.criticalText)
        assertNull(status.action)
        // A clock only while nothing needs the user.
        assertNull(OngoingStatus.from(ConnectionState.Online, fleet.copy(phase = "needsYou", needsYou = 1,
            items = listOf(permission, working))).workingSince)

        val connected = OngoingStatus.connection(ConnectionState.Online)
        assertEquals(connected, OngoingStatus.from(ConnectionState.Online, FleetState()))
        val done = FleetState("done", unread = 2, items = listOf(FleetItem("a/b", "博客迁移", kind = "unread", detail = "迁移完成")))
        assertEquals("2 个做完了", OngoingStatus.from(ConnectionState.Online, done).title)
        assertEquals("●2", OngoingStatus.from(ConnectionState.Online, done).criticalText)
        assertEquals(connected, OngoingStatus.from(ConnectionState.Online, done, quiet = true))
        assertFalse(connected.summary)
        assertNull(connected.text)

        // A disconnected snapshot must not look like current work or expose an old request.
        val offline = OngoingStatus.from(ConnectionState.WaitingForNetwork, fleet)
        assertEquals(R.string.notify_connection_waiting_network, offline.titleRes)
        assertNull(offline.text)
        assertNull(offline.lines)
        assertNull(offline.action)
        assertEquals(listOf(0, 0, 0), listOf(offline.needsYou, offline.working, offline.unread))
        assertFalse(offline.shouldPromote(Android16Qpr1, true))
    }

    @Test fun withoutLockScreenDetailsNoNameOrRequestShowsAnywhere() {
        // A phone set to show all notification content on its lock screen shows the private version there: with
        // "Show details on lock screen" off (the default) it says what kind of thing waits and how many, nothing more.
        val fleet = FleetState("needsYou", needsYou = 4, working = 2, unread = 1, items = listOf(plan, permission, question))
        val status = OngoingStatus.from(ConnectionState.Online, fleet)
        assertEquals("4 个需要你", status.title)
        assertEquals("有个计划等你批准", status.text)
        assertEquals("2 个在工作 · 1 个做完了", status.subText)
        assertEquals("有个计划等你批准\n有个授权等你\n有个提问等你回答\n+1 个", status.lines)
        val shown = listOfNotNull(status.title, status.text, status.subText, status.lines, status.publicText).joinToString("\n")
        for (item in fleet.items) {
            for (secret in listOf(item.title, item.place, item.detail)) assertFalse(secret, shown.contains(secret))
        }
        assertFalse(shown.contains("MacBook"))
        // The buttons stay: "View" / "Approve" say nothing about the request.
        assertEquals(listOf(OngoingButton(R.string.notify_action_view, null, false), OngoingButton(R.string.notify_action_approve, "approve_edits", true)), status.action!!.buttons)
        // Requests of one kind are counted, not listed.
        val two = OngoingStatus.from(ConnectionState.Online, FleetState("needsYou", needsYou = 2,
            items = listOf(permission, permission.copy(id = "studio/other", interaction = "ix_other"))))
        assertEquals("有个授权等你", two.text)
        assertEquals("2 个授权等你", two.lines)
        // Working or finished only: the title and the counts say it all; the clock still runs.
        val busy = OngoingStatus.from(ConnectionState.Online, FleetState("working", working = 2, unread = 1,
            items = listOf(working, working.copy(id = "server/b")), workingSince = 1_700.0))
        assertEquals("2 个在工作", busy.title)
        assertNull(busy.text)
        assertNull(busy.lines)
        assertEquals("1 个做完了", busy.subText)
        assertEquals(1_700_000L, busy.workingSince)
        assertTrue(busy.summary)
        // One request alone has no list to expand.
        assertNull(OngoingStatus.from(ConnectionState.Online, FleetState("needsYou", needsYou = 1, items = listOf(question))).lines)
    }

    @Test fun promotionNeedsAndroid16Qpr1PermissionAndSomethingToShow() {
        val status = OngoingStatus.from(ConnectionState.Online, FleetState("needsYou", needsYou = 2, items = listOf(permission)))
        assertFalse(status.shouldPromote(35 * 100_000, true))
        // Android 16.0 has no setRequestPromotedOngoing: calling it there throws NoSuchMethodError.
        assertFalse(status.shouldPromote(36 * 100_000, true))
        assertFalse(status.shouldPromote(Android16Qpr1, false))
        assertTrue(status.shouldPromote(Android16Qpr1, true))
        assertFalse(status.shouldPromote(Android16Qpr1, true, enabled = false))
        assertTrue(status.shouldPromote(37 * 100_000, true, enabled = true))
        assertFalse(OngoingStatus.from(ConnectionState.Online, FleetState()).shouldPromote(Android16Qpr1, true))
        assertEquals("⚙999+", OngoingStatus.from(ConnectionState.Online, FleetState("working", working = 1000)).criticalText)
    }

    @Test fun continuousChangesAreConflatedAndFinalValueIsDeliveredWithoutDuplicates() = runTest {
        val source = MutableStateFlow(0)
        val delivered = mutableListOf<Int>()
        val job = backgroundScope.launch { source.throttleOngoing(100).collect { delivered += it } }
        runCurrent()
        assertEquals(listOf(0), delivered)
        repeat(9) {
            advanceTimeBy(10)
            source.value = it + 1
            runCurrent()
        }
        assertEquals(listOf(0), delivered)
        advanceTimeBy(10)
        runCurrent()
        assertEquals(listOf(0, 9), delivered)
        source.value = 10
        runCurrent()
        advanceTimeBy(100)
        runCurrent()
        assertEquals(listOf(0, 9, 10), delivered)
        source.value = 11
        runCurrent()
        source.value = 10
        runCurrent()
        advanceTimeBy(100)
        runCurrent()
        assertEquals(listOf(0, 9, 10), delivered)
        source.value = 12
        runCurrent()
        assertEquals(listOf(0, 9, 10, 12), delivered)
        job.cancel()
        source.value = 13
        advanceTimeBy(100)
        runCurrent()
        assertEquals(listOf(0, 9, 10, 12), delivered)
    }
}
