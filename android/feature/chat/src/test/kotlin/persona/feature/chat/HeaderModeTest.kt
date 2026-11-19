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

    @Test fun pendingRestartIsReadOffTheSession() {
        val json = """{"machine_id":"m","session_id":"s","permission_mode":"default","bypass_pending":true}"""
        assertTrue(JsonCodec.decodeFromString<Session>(json).bypassPending)
        assertFalse(JsonCodec.decodeFromString<Session>("""{"machine_id":"m","session_id":"s"}""").bypassPending)
    }
}
