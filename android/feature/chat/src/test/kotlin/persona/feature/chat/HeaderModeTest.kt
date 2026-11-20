package persona.feature.chat

import androidx.test.ext.junit.runners.AndroidJUnit4
import kotlinx.serialization.decodeFromString
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import persona.core.contracts.JsonCodec
import persona.core.contracts.Session
import persona.core.contracts.SessionCapabilities
import persona.core.designsystem.ModeTone
import persona.core.designsystem.icons.PhIcons
import persona.core.domain.sort.canChangeEffortOnly
import persona.core.domain.sort.canChangeModes
import persona.core.domain.sort.canSwitchListedModes
import persona.core.domain.sort.listedModes

/**
 * The Agent header's permission mode (#3957), the rules of iOS `SessionModes` (#3719, HeaderModeTests): each mode's tone
 * and symbol, never a guessed Ask First; when the header shows it; Allow Everything offered only where the agent can take
 * it while it runs; what a switch asks first.
 */
@RunWith(AndroidJUnit4::class)
@Config(sdk = [35])
class HeaderModeTest {
    private fun claude(mode: String?, bypass: Boolean = false, status: String = "idle") =
        Session(machineId = "m", sessionId = "s", agent = "claude", status = status, permissionMode = mode, bypassAvailable = bypass)

    @Test fun eachModeHasItsToneAndSymbol() {
        assertEquals(ModeTone.Asks, SessionModes.headerTone("default"))
        assertEquals(ModeTone.Plans, SessionModes.headerTone("plan"))
        assertEquals(ModeTone.Acts, SessionModes.headerTone("acceptEdits"))
        assertEquals(ModeTone.Acts, SessionModes.headerTone("auto"))
        assertEquals(ModeTone.AllowsAll, SessionModes.headerTone("bypassPermissions"))
        assertEquals(PhIcons.HandPalm, SessionModes.headerIcon("default"))
        assertEquals(PhIcons.ShieldWarning, SessionModes.headerIcon("bypassPermissions"))
        // not reported yet, or a mode this app does not know: the plain symbol, never Ask First
        for (id in listOf(null, "", "dontAsk")) {
            assertEquals(ModeTone.Unknown, SessionModes.headerTone(id))
            assertEquals(PhIcons.Shield, SessionModes.headerIcon(id))
        }
    }

    @Test fun theHeaderShowsAModeToSeeOrToChange() {
        assertTrue(claude("default").canChangeModes)
        assertTrue(SessionModes.showsInHeader(claude("default")))
        // before its first prompt: nothing reported, but it can be set
        assertTrue(SessionModes.showsInHeader(claude(null)))
        // ended, with its last mode: still shown, not changeable
        assertTrue(SessionModes.showsInHeader(claude("plan", status = "offline")))
        assertFalse(SessionModes.showsInHeader(claude(null, status = "offline")))
        // Codex has none; an assistant conversation has its own bar
        assertFalse(SessionModes.showsInHeader(Session(machineId = "m", sessionId = "c", agent = "codex", status = "idle")))
        assertFalse(SessionModes.showsInHeader(claude("default").copy(assistant = true)))
    }

    @Test fun allowEverythingIsOfferedOnlyWhereTheAgentCanTakeIt() {
        assertFalse(SessionModes.switchable(claude("default")).any { it.id == SessionModes.BYPASS })
        assertEquals(SessionModes.BYPASS, SessionModes.bypassUnavailable(claude("default"))?.id)
        val can = claude("default", bypass = true)
        assertTrue(SessionModes.switchable(can).any { it.id == SessionModes.BYPASS })
        assertNull(SessionModes.bypassUnavailable(can))
        // already in it: listed as the current one, not greyed again
        assertNull(SessionModes.bypassUnavailable(claude("bypassPermissions")))
        // not running: nothing to offer
        assertNull(SessionModes.bypassUnavailable(claude("default", status = "offline")))
    }

    @Test fun switchingAsksFirstIntoAllowEverythingAndOutOfItWithNoWayBack() {
        assertEquals(SessionModes.Confirmation.AllowEverything, SessionModes.confirmation(claude("default", bypass = true), "bypassPermissions"))
        assertEquals(SessionModes.Confirmation.LeaveAllowEverything, SessionModes.confirmation(claude("bypassPermissions"), "default"))
        // it can come back: no question
        assertNull(SessionModes.confirmation(claude("bypassPermissions", bypass = true), "default"))
        assertNull(SessionModes.confirmation(claude("default"), "plan"))
        assertNull(SessionModes.confirmation(claude("plan"), "plan"))
        // an assistant conversation switches without asking
        assertNull(SessionModes.confirmation(claude("default").copy(assistant = true), "bypassPermissions"))
    }

    /** A Paseo provider that is not Claude Code (#8261): its row lists the modes it takes, and Claude Code's chips stay off. */
    private fun grok(mode: String?, modes: List<String>? = listOf("default", "bypassPermissions"), status: String = "idle") =
        Session(machineId = "m", sessionId = "paseo-agent-1", agent = "paseo", agentLabelHint = "Grok", launcher = "grok", status = status,
            permissionMode = mode, sourceKind = "runtime", capabilities = SessionCapabilities(read = true, send = true, interrupt = true,
                approve = true, modes = false, model = true, permissionModes = modes))

    @Test fun aProviderThatListsItsModesSwitchesBetweenThemBothWays() {
        val own = grok(null)
        assertEquals(listOf("default", "bypassPermissions"), own.listedModes)
        assertTrue(own.canSwitchListedModes)
        assertFalse(own.canChangeModes)
        // Its own approval in its own words, never Ask First; Allow Everything saying it can be switched back.
        val rows = SessionModes.switchable(own)
        assertEquals(listOf("default", "bypassPermissions"), rows.map { it.id })
        assertEquals(listOf(R.string.chat_modes_launcher_default, R.string.chat_modes_bypass), rows.map { it.titleRes })
        assertEquals(R.string.chat_modes_bypass_listed_detail, rows[1].detailRes)
        assertNull(SessionModes.bypassUnavailable(own))
        // No mode reported is its own approval: shown, under the plain shield in no tone.
        assertEquals("default", SessionModes.current(own))
        assertTrue(SessionModes.showsInHeader(own))
        assertEquals(ModeTone.Unknown, SessionModes.headerTone(own))
        assertEquals(PhIcons.Shield, SessionModes.headerIcon(own))
        val all = grok("bypassPermissions")
        assertEquals(ModeTone.AllowsAll, SessionModes.headerTone(all))
        assertEquals(PhIcons.ShieldWarning, SessionModes.headerIcon(all))
        // Into Allow Everything asks first; out of it does not, since it can be turned on again; the current one asks nothing.
        assertEquals(SessionModes.Confirmation.AllowEverything, SessionModes.confirmation(own, "bypassPermissions"))
        assertNull(SessionModes.confirmation(all, "default"))
        assertNull(SessionModes.confirmation(own, "default"))
    }

    @Test fun onlyTwoListedModesWhereClaudeCodesDoNotStandMakeASwitch() {
        // Its own approval alone, an older node's row without the list: nothing to switch, nothing shown.
        for (modes in listOf(listOf("default"), null)) {
            assertNull(grok(null, modes).listedModes)
            assertFalse(SessionModes.showsInHeader(grok(null, modes)))
        }
        // A Persona conversation, and a row where Claude Code's chips stand, keep their own.
        assertNull(grok(null).copy(assistant = true).listedModes)
        assertNull(grok("default").copy(agent = "claude", capabilities = grok("default").capabilities?.copy(modes = true)).listedModes)
        // Not running: still shown with the mode it ran in, nothing to pick.
        val stopped = grok("bypassPermissions", status = "offline")
        assertTrue(SessionModes.showsInHeader(stopped))
        assertFalse(stopped.canSwitchListedModes)
    }

    @Test fun theListIsReadOffTheRowsCapabilities() {
        val json = """{"machine_id":"m","session_id":"s","agent":"paseo","capabilities":{"read":true,"send":true,"modes":false,"permission_modes":["default","bypassPermissions"]}}"""
        assertEquals(listOf("default", "bypassPermissions"), JsonCodec.decodeFromString<Session>(json).capabilities?.permissionModes)
        // Not a list of words: left out, the row kept.
        val odd = """{"machine_id":"m","session_id":"s","capabilities":{"read":true,"permission_modes":"default"}}"""
        assertNull(JsonCodec.decodeFromString<Session>(odd).capabilities?.permissionModes)
        assertTrue(JsonCodec.decodeFromString<Session>(odd).capabilities?.read == true)
    }

    @Test fun bypassAvailableIsReadOffTheSession() {
        val json = """{"machine_id":"m","session_id":"s","permission_mode":"bypassPermissions","bypass_available":true}"""
        assertTrue(JsonCodec.decodeFromString<Session>(json).bypassAvailable)
        assertFalse(JsonCodec.decodeFromString<Session>("""{"machine_id":"m","session_id":"s"}""").bypassAvailable)
    }

    /** Full access by a restart (#6513, the web's `bypassRestarts` from #6476): Claude Code in a pane without a bypass flag. */
    private fun paned(mode: String? = "default", bypass: Boolean = false) = claude(mode, bypass).copy(paneId = "w1:p1")

    @Test fun allowEverythingIsOfferedByARestartForClaudeInAPane() {
        val s = paned()
        assertTrue(SessionModes.bypassRestarts(s))
        val row = SessionModes.switchable(s).single { it.id == SessionModes.BYPASS }
        assertEquals(R.string.chat_modes_bypass_restart, row.detailRes)
        assertNull(SessionModes.bypassUnavailable(s))
        // all five modes, Plan included
        assertEquals(listOf("default", "acceptEdits", "plan", "auto", SessionModes.BYPASS), SessionModes.switchable(s).map { it.id })
        // it still asks first, the dialog carrying the note
        assertEquals(SessionModes.Confirmation.AllowEverything, SessionModes.confirmation(s, SessionModes.BYPASS))
        assertEquals(R.string.chat_modes_bypass_restart, SessionModes.bypassNote(s))
    }

    @Test fun noRestartWhereItIsNotNeededOrTheNodeCannot() {
        // the process can take it: at once, no note
        assertFalse(SessionModes.bypassRestarts(paned(bypass = true)))
        assertNull(SessionModes.bypassNote(paned(bypass = true)))
        // already in it
        assertFalse(SessionModes.bypassRestarts(paned("bypassPermissions")))
        // no pane (a headless agent), a source, Codex, Antigravity, a Persona conversation
        assertFalse(SessionModes.bypassRestarts(claude("default")))
        assertFalse(SessionModes.bypassRestarts(paned().copy(sourceKind = "codex_app")))
        assertFalse(SessionModes.bypassRestarts(paned().copy(agent = "codex")))
        assertFalse(SessionModes.bypassRestarts(paned().copy(agent = "agy")))
        assertFalse(SessionModes.bypassRestarts(paned().copy(assistant = true)))
        // partial capabilities without send/terminal still allow restart for an eligible paned Claude (#7996 review)
        assertTrue(SessionModes.bypassRestarts(paned().copy(capabilities = SessionCapabilities(modes = true))))
        assertTrue(SessionModes.bypassRestarts(paned().copy(capabilities = SessionCapabilities(rewind = true))))
        // explicit modes refusal, sourced session without terminal, or offline session
        assertFalse(SessionModes.bypassRestarts(paned().copy(capabilities = SessionCapabilities(modes = false))))
        assertFalse(SessionModes.bypassRestarts(paned().copy(sourceKind = "claude_cli", capabilities = SessionCapabilities(send = true, terminal = false))))
        assertFalse(SessionModes.bypassRestarts(paned().copy(status = "offline")))
        // disabled with its reason where it can't
        assertEquals(SessionModes.BYPASS, SessionModes.bypassUnavailable(claude("default"))?.id)
    }

    @Test fun aPendingRestartKeepsTheModeItRunsIn() {
        val s = paned("acceptEdits").copy(bypassPending = true)
        // the header and the chip draw the mode it runs in until the restart
        assertEquals(ModeTone.Acts, SessionModes.headerTone(s.permissionMode))
        assertEquals(R.string.chat_modes_bypass_pending, SessionModes.bypassNote(s))
        val row = SessionModes.switchable(s).single { it.id == SessionModes.BYPASS }
        assertEquals(R.string.chat_modes_bypass_pending, row.detailRes)
        val res = androidx.test.core.app.ApplicationProvider.getApplicationContext<android.content.Context>().resources
        assertEquals(res.getString(R.string.chat_modes_bypass_pending), SessionModes.pendingNote(s, res))
        assertNull(SessionModes.pendingNote(paned("acceptEdits"), res))
        // the hint above the composer says it, after the mode
        assertTrue(chatModeHint(s, res)!!.endsWith(res.getString(R.string.chat_modes_bypass_pending)))
    }

    /** Picking the checked mode while a restart waits sends it without a confirmation so the node drops the restart (#7996 review). */
    @Test fun pickingTheCheckedModeCallsOffAPendingRestart() {
        val normal = paned("acceptEdits")
        assertFalse(SessionModes.appliesPick(normal, "acceptEdits"))
        assertTrue(SessionModes.appliesPick(normal, "plan"))
        val pending = normal.copy(bypassPending = true)
        assertTrue(SessionModes.appliesPick(pending, "acceptEdits"))
        assertNull(SessionModes.confirmation(pending, "acceptEdits"))
        assertTrue(SessionModes.appliesPick(pending, SessionModes.BYPASS))
        assertEquals(SessionModes.Confirmation.AllowEverything, SessionModes.confirmation(pending, SessionModes.BYPASS))
    }

    @Test fun aPersonaConversationTakesItFromTheNextMessage() {
        val s = claude("default").copy(assistant = true)
        assertEquals(R.string.chat_modes_bypass_next_message, SessionModes.bypassNote(s))
        assertEquals(R.string.chat_modes_bypass_next_message, SessionModes.switchable(s).single { it.id == SessionModes.BYPASS }.detailRes)
        assertNull(SessionModes.bypassNote(claude("bypassPermissions").copy(assistant = true)))
    }

    /** /permissions only where the composer draws the picker it opens and the mode can change (#7996 review). */
    @Test fun permissionsIsOfferedOnlyWhereItsPickerIs() {
        val live = paned("default")
        assertTrue(SessionModes.offersPermissions(live, glass = true, modeSheet = false))
        assertTrue(SessionModes.offersPermissions(live, glass = false, modeSheet = true))
        // Brief / Board without the "Mode" sheet: nothing to open
        assertFalse(SessionModes.offersPermissions(live, glass = false, modeSheet = false))
        // an agent that can't change its mode (offline, Codex) or no session: an ordinary message
        assertFalse(SessionModes.offersPermissions(live.copy(status = "offline"), glass = true, modeSheet = false))
        assertFalse(SessionModes.offersPermissions(paned().copy(agent = "codex"), glass = true, modeSheet = true))
        assertFalse(SessionModes.offersPermissions(null, glass = true, modeSheet = true))
        // a Persona conversation, where its mode can change
        assertTrue(SessionModes.offersPermissions(claude("default").copy(assistant = true), glass = true, modeSheet = false))
    }

    @Test fun bypassPendingIsReadOffTheSession() {
        val json = """{"machine_id":"m","session_id":"s","permission_mode":"auto","bypass_pending":true}"""
        assertTrue(JsonCodec.decodeFromString<Session>(json).bypassPending)
        assertFalse(JsonCodec.decodeFromString<Session>("""{"machine_id":"m","session_id":"s"}""").bypassPending)
        assertFalse(JsonCodec.decodeFromString<Session>("""{"machine_id":"m","session_id":"s","bypass_pending":null}""").bypassPending)
    }

    /** Antigravity (`agy`) switches all five modes including Allow Everything directly via ACP (no restart, no confirmation), and keeps `/permissions` even when it also reports `efforts`. */
    @Test fun antigravityOffersAllowEverythingAndPermissionsDirectly() {
        val agy = Session(machineId = "m", sessionId = "s", agent = "agy", status = "idle", permissionMode = "auto",
            sourceKind = "runtime", efforts = listOf("low", "high"),
            capabilities = SessionCapabilities(read = true, send = true, modes = true))
        assertTrue(agy.canChangeModes)
        assertFalse(agy.canChangeEffortOnly)
        assertTrue(SessionModes.showsInHeader(agy))
        assertTrue(SessionModes.showsInComposer(agy))
        assertTrue(SessionModes.offersPermissions(agy, glass = true, modeSheet = false))
        assertTrue(SessionModes.offersPermissions(agy, glass = false, modeSheet = true))
        assertFalse(SessionModes.bypassRestarts(agy))
        assertNull(SessionModes.bypassNote(agy))
        assertNull(SessionModes.bypassUnavailable(agy))
        val rows = SessionModes.switchable(agy)
        assertEquals(listOf("default", "acceptEdits", "plan", "auto", SessionModes.BYPASS), rows.map { it.id })
        assertEquals(R.string.chat_modes_bypass_detail_running, rows.single { it.id == SessionModes.BYPASS }.detailRes)
        assertNull(SessionModes.confirmation(agy, SessionModes.BYPASS))
        assertNull(SessionModes.confirmation(agy.copy(permissionMode = SessionModes.BYPASS), "default"))
    }

    @Test fun pendingRestartIsReadOffTheSession() {
        val json = """{"machine_id":"m","session_id":"s","permission_mode":"default","bypass_pending":true}"""
        assertTrue(JsonCodec.decodeFromString<Session>(json).bypassPending)
        assertFalse(JsonCodec.decodeFromString<Session>("""{"machine_id":"m","session_id":"s"}""").bypassPending)
    }
}
