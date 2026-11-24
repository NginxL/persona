package persona.feature.agents

import androidx.compose.ui.test.junit4.createComposeRule
import androidx.test.core.app.ApplicationProvider
import androidx.test.ext.junit.runners.AndroidJUnit4
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.cancel
import org.junit.After
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import persona.core.contracts.CodexTakeoverCapability
import persona.core.contracts.Session
import persona.core.contracts.SessionCapabilities
import persona.core.designsystem.JAction
import persona.core.designsystem.PersonaTheme
import persona.core.domain.sort.AgentSession
import persona.feature.chat.CodexTakeoverCoordinator
import persona.feature.chat.codexTakeoverMenuActions

@RunWith(AndroidJUnit4::class)
@Config(sdk = [35], qualifiers = "en-rUS-w400dp-h800dp-xhdpi")
class AgentsCodexTakeoverMenuTest {
    @get:Rule val rule = createComposeRule()
    private val scope = CoroutineScope(SupervisorJob() + Dispatchers.Unconfined)
    private val context = ApplicationProvider.getApplicationContext<android.content.Context>()

    @After fun tearDown() = scope.cancel()

    private fun coordinator() = CodexTakeoverCoordinator(
        res = context.resources,
        client = null,
        scope = scope,
        onAdopted = { _, _ -> },
        onFailure = { _, _, _ -> },
        onSuccessToast = {},
    )

    @Test fun agentMenuIncludesTakeoverAndContinueAfterTerminalItemsWhenResumable() {
        val session = Session(
            machineId = "m1",
            sessionId = "s1",
            title = "Codex CLI",
            cwd = "/work",
            status = "working",
            agent = "codex",
            sourceKind = "codex_history",
            capabilities = SessionCapabilities(
                read = true,
                send = false,
                terminal = true,
                takeover = CodexTakeoverCapability(running = true, canResume = true),
            ),
        )
        lateinit var items: List<JAction>
        rule.setContent {
            PersonaTheme {
                val actions = codexTakeoverMenuActions(session, coordinator(), enabled = true)
                items = AgentSessionMenuItems(
                    row = AgentSession(session),
                    pinned = false,
                    muted = false,
                    busy = false,
                    onOpen = {},
                    onPin = {},
                    onMute = {},
                    onRead = {},
                    onRename = {},
                    onStopLoop = {},
                    onHandoff = {},
                    onClose = {},
                    takeoverActions = actions,
                )
            }
        }
        val labels = items.map { it.label }
        val resumeIndex = labels.indexOf("Copy resume command")
        val takeoverIndex = labels.indexOf("Take over")
        val continueIndex = labels.indexOf("Continue in a new session")
        assertTrue("Expected Copy resume command in $labels", resumeIndex >= 0)
        assertEquals(resumeIndex + 1, takeoverIndex)
        assertEquals(takeoverIndex + 1, continueIndex)
    }

    @Test fun agentMenuOmitsTakeoverWhenCanResumeIsFalseAndOmitsBothWithoutCapability() {
        val continueOnly = Session(
            machineId = "m1",
            sessionId = "s2",
            title = "Sealed Codex",
            cwd = "/work",
            status = "working",
            agent = "codex",
            sourceKind = "codex_history",
            capabilities = SessionCapabilities(
                read = true,
                send = false,
                takeover = CodexTakeoverCapability(running = false, canResume = false, reason = "encrypted_context"),
            ),
        )
        val ordinary = continueOnly.copy(sessionId = "s3", capabilities = SessionCapabilities(read = true, send = false, takeover = null))
        lateinit var continueLabels: List<String>
        lateinit var ordinaryLabels: List<String>
        rule.setContent {
            PersonaTheme {
                val coord = coordinator()
                continueLabels = AgentSessionMenuItems(
                    row = AgentSession(continueOnly), pinned = false, muted = false, busy = false,
                    onOpen = {}, onPin = {}, onMute = {}, onRead = {}, onRename = {}, onStopLoop = {}, onHandoff = {}, onClose = {},
                    takeoverActions = codexTakeoverMenuActions(continueOnly, coord, enabled = true),
                ).map { it.label }
                ordinaryLabels = AgentSessionMenuItems(
                    row = AgentSession(ordinary), pinned = false, muted = false, busy = false,
                    onOpen = {}, onPin = {}, onMute = {}, onRead = {}, onRename = {}, onStopLoop = {}, onHandoff = {}, onClose = {},
                    takeoverActions = codexTakeoverMenuActions(ordinary, coord, enabled = true),
                ).map { it.label }
            }
        }
        assertTrue("Take over" !in continueLabels)
        assertTrue("Continue in a new session" in continueLabels)
        assertTrue("Take over" !in ordinaryLabels)
        assertTrue("Continue in a new session" !in ordinaryLabels)
    }
}
