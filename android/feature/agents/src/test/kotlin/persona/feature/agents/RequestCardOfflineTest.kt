package persona.feature.agents

import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.ui.semantics.SemanticsProperties
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onAllNodesWithText
import androidx.compose.ui.test.onNodeWithText
import androidx.test.ext.junit.runners.AndroidJUnit4
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.put
import org.junit.Assert.assertEquals
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import persona.core.contracts.Interaction
import persona.core.designsystem.AppStyle
import persona.core.designsystem.PersonaTheme
import java.time.Instant

/**
 * A request card in the Agents list while its hub is unreachable (#2483): Allow and Deny stay greyed out, and the card says
 * why, as the request's full page does, instead of buttons that are just disabled. In every style: the board's cards are
 * collapsed in the list, where the body that says it is not drawn, so the reason sits above the quick buttons there.
 */
@RunWith(AndroidJUnit4::class)
@Config(sdk = [35], qualifiers = "en-w400dp-h1200dp")
class RequestCardOfflineTest {
    @get:Rule val rule = createComposeRule()

    private val now = Instant.parse("2026-11-09T08:00:00Z")
    private val offline = "Connect to the hub to send your choice"
    private val computer = "This session can't be approved from the phone. Open the session or handle it on the computer."
    private val card = Interaction("ix_build", machineId = "macbook", sessionId = "tests", sessionTitle = "Run the test suite",
        kind = "permission", toolName = "Bash", expiresAt = now.plusSeconds(600).toString(),
        toolInput = buildJsonObject { put("command", "swift test"); put("description", "Run the package tests") })
    private var style by mutableStateOf(AppStyle.Glass)

    /** Shows the card, then runs [check] in each style. */
    private fun show(online: Boolean, request: Interaction = card, approvable: Boolean = true, check: (AppStyle) -> Unit) {
        rule.setContent {
            PersonaTheme(style = style, darkTheme = false, reduceMotion = true) {
                RequestCard(request, "MacBook", AgentsMetadata(), now, online = online, delivery = null, onOpen = {},
                    onAnswer = { _, _, _, _ -> }, approvable = approvable)
            }
        }
        for (each in AppStyle.entries) { style = each; rule.waitForIdle(); check(each) }
    }

    private fun count(text: String) = rule.onAllNodesWithText(text).fetchSemanticsNodes().size
    private fun enabled(text: String) = SemanticsProperties.Disabled !in rule.onNodeWithText(text).fetchSemanticsNode().config

    @Test fun offlineTheCardSaysWhyItsButtonsAreGreyedOut() = show(online = false) {
        assertEquals("$it", 1, count(offline))
        assertEquals("$it Allow", false, enabled("Allow"))
        assertEquals("$it Deny", false, enabled("Deny"))
    }

    @Test fun onlineTheCardSaysNothingAndItsButtonsWork() = show(online = true) {
        assertEquals("$it", 0, count(offline))
        assertEquals("$it Allow", true, enabled("Allow"))
        assertEquals("$it Deny", true, enabled("Deny"))
    }

    @Test fun anExpiredCardDoesNotBlameTheConnection() = show(online = false, request = card.copy(expiresAt = now.minusSeconds(1).toString())) {
        assertEquals("$it", 0, count(offline))
    }

    private fun prepared(expiresAt: java.time.Instant) = persona.core.contracts.JsonCodec.decodeFromString<Interaction>(
        """{"id":"ix_prepared","machine_id":"macbook","session_id":"tests","session_title":"Run the test suite",""" +
            """"kind":"permission","tool_name":"Bash","expires_at":"$expiresAt","can_clarify":true}""",
    )

    @Test fun aPreparedCardStaysAnswerableUnderItsExtendedDeadline() =
        show(online = true, request = prepared(now.plusSeconds(3_600))) {
            assertEquals("$it Allow", true, enabled("Allow"))
            assertEquals("$it Deny", true, enabled("Deny"))
            assertEquals("$it", 0, count(offline))
        }

    @Test fun aPreparedCardAtItsActualDeadlineStillCannotExecute() =
        show(online = true, request = prepared(now.minusSeconds(1))) {
            assertEquals("$it Allow", false, enabled("Allow"))
            assertEquals("$it Deny", false, enabled("Deny"))
            assertEquals("$it", 0, count(offline))
        }

    @Test fun aCardThePhoneCannotApproveSaysThatInstead() = show(online = false, approvable = false) {
        assertEquals("$it", 0, count(offline))
        assertEquals("$it", 1, count(computer))
    }
}
