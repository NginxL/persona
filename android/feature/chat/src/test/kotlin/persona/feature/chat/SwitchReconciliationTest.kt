package persona.feature.chat

import androidx.compose.material3.Text
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.SideEffect
import androidx.compose.ui.platform.LocalResources
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onNodeWithText
import androidx.test.ext.junit.runners.AndroidJUnit4
import kotlinx.coroutines.flow.MutableSharedFlow
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.serialization.json.JsonObject
import org.junit.Assert.*
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import persona.core.contracts.Hello
import persona.core.contracts.PendingSwitch
import persona.core.contracts.ServerEvent
import persona.core.contracts.Session
import persona.core.contracts.SessionInfo
import persona.core.contracts.SessionsListEvent
import persona.core.contracts.api.ChatAction
import persona.core.contracts.api.ChatActionResult
import persona.core.contracts.api.ChatActions
import persona.core.contracts.api.ConnectionState
import persona.core.contracts.api.HubConnection
import persona.core.contracts.api.SocketHello
import persona.core.designsystem.AppStyle
import persona.core.designsystem.LocalHubFeatures
import persona.core.designsystem.PersonaTheme

@RunWith(AndroidJUnit4::class)
@Config(sdk = [35])
class SwitchReconciliationTest {
    @get:Rule val rule = createComposeRule()

    private val full = Session("box", "agent", agent = "claude", launcher = "claude", status = "idle", rev = 40)
    private val summary = full.copy(waiting = false, launcher = null)
    private val scheduled = PendingSwitch("codex", "gpt-5.5", agent = "codex")

    private class Wire(initial: Session) : ChatActions, HubConnection {
        var answer = initial
        override val state = MutableStateFlow(ConnectionState.Online)
        override val events = MutableSharedFlow<ServerEvent>(extraBufferCapacity = 4)
        override val currentHello = MutableStateFlow<SocketHello?>(null)
        override fun reconnect() = Unit
        override fun send(message: JsonObject) = false
        override suspend fun isMuted(session: Session) = false
        override suspend fun execute(session: Session, action: ChatAction) = ChatActionResult.Done
        override suspend fun verify(session: Session, action: ChatAction) = ChatActionResult.Done
        override suspend fun info(session: Session) = SessionInfo(session.sessionId)
        override suspend fun completeRow(session: Session): Session = answer
    }

    private lateinit var mounted: ChatPageCloseout
    @Volatile private var shown: Session? = null
    @Volatile private var observed = 0L

    private fun mount(wire: Wire) {
        val chrome = ChatChromeBindings(actions = wire, connection = wire)
        rule.setContent {
            PersonaTheme(reduceMotion = true, style = AppStyle.Glass) {
                CompositionLocalProvider(LocalHubFeatures provides setOf("engine_switch", "sessions_lookup")) {
                    val page = rememberChatPageSession(summary.key, chrome, ChatRenderStyle.Agent, historyReadOnly = false,
                        historyFooter = null, historySession = null, res = LocalResources.current,
                        sessionList = listOf(summary), listedSessions = listOf(summary), adoptedSessions = emptyList(),
                        keptReceipts = emptyList())
                    val closeout = rememberChatPageCloseout(summary.key, chrome, ChatRenderStyle.Agent, null, null,
                        page.row, page.session, page.switchObservation)
                    Text(closeout.switchSession?.pendingSwitch?.model ?: "No pending switch")
                    SideEffect {
                        mounted = closeout
                        shown = closeout.switchSession
                        observed = page.switchObservation
                    }
                }
            }
        }
        rule.waitUntil(10_000) { observed >= 1L }
    }

    @Test fun aListHintClearsTheLocalScheduleEvenWhenTheWholeRowIsUnchanged() {
        val wire = Wire(full)
        mount(wire)
        rule.runOnIdle { mounted.drawnSwitch.value = DrawnSwitch(scheduled, observed) }
        rule.onNodeWithText(scheduled.model!!).assertExists()
        val before = observed
        rule.runOnIdle {
            // The other device canceled before this Android chat had observed the scheduled value.
            // Both reads therefore return the same whole row and neither size nor summary changes.
            assertTrue(wire.events.tryEmit(SessionsListEvent("new-list-revision")))
        }
        rule.waitUntil(10_000) { observed > before && shown?.pendingSwitch == null }
        rule.runOnIdle {
            assertEquals(full.rev, shown?.rev)
            assertFalse(switchesNext(shown, true))
            assertNull(mounted.drawnSwitch.value)
        }
    }

    @Test fun reconnectRevealsARemoteScheduleAfterALocalCancelAtTheSameTranscriptSize() {
        val wire = Wire(full.copy(pendingSwitch = scheduled))
        mount(wire)
        rule.runOnIdle { mounted.drawnSwitch.value = DrawnSwitch(null, observed) }
        rule.onNodeWithText("No pending switch").assertExists()
        val newPick = scheduled.copy(model = "gpt-5.4")
        val before = observed
        rule.runOnIdle {
            wire.answer = full.copy(pendingSwitch = newPick)
            // The authenticated socket changed without a visible offline transition or a list hint.
            wire.currentHello.value = SocketHello(2, Hello(features = listOf("engine_switch", "sessions_lookup")))
        }
        rule.waitUntil(10_000) { observed > before && shown?.pendingSwitch == newPick }
        rule.runOnIdle {
            assertEquals(full.rev, shown?.rev)
            assertTrue(switchesNext(shown, true))
            assertNull(mounted.drawnSwitch.value)
        }
    }
}
