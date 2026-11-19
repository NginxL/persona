package persona.feature.chat

import android.graphics.Bitmap
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.width
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.asAndroidBitmap
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.test.SemanticsNodeInteraction
import androidx.compose.ui.test.captureToImage
import androidx.compose.ui.semantics.SemanticsProperties
import androidx.compose.ui.semantics.getOrNull
import androidx.compose.ui.test.hasAnyAncestor
import androidx.compose.ui.test.isDialog
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onNodeWithContentDescription
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.compose.ui.unit.dp
import androidx.test.ext.junit.runners.AndroidJUnit4
import kotlinx.serialization.json.JsonPrimitive
import kotlinx.serialization.json.buildJsonObject
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.core.contracts.Interaction
import persona.core.contracts.Item
import persona.core.contracts.Machine
import persona.core.contracts.Question
import persona.core.contracts.QuestionOption
import persona.core.contracts.Session
import persona.core.contracts.api.DraftUploadState
import persona.core.designsystem.*
import persona.core.domain.lines.ChatLine
import persona.core.domain.lines.chatLines
import persona.core.domain.lines.LineItem
import java.io.File

/**
 * The chat's chrome in each of the app's languages (docs/protocol/i18n.md, #2453): the top bar and its menu, the composer with
 * attachments and the queue strip, the request dock (tool approval, plan, question), the mode sheet, an API error card
 * and the message menu, Glass light and dark. PNGs in `build/i18n-shots/` (or `-Dpersona.shots=<dir>`).
 */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
class ChatChromeLanguageShotsTest {
    @get:Rule val rule = createComposeRule()

    private fun out(): File = File(System.getProperty("persona.shots") ?: "build/i18n-shots").also { it.mkdirs() }

    @Config(sdk = [35], qualifiers = "zh-w400dp-h900dp-xhdpi")
    @Test fun simplifiedChinese() = shots("zh", more = "更多操作", mode = "模式", allow = "允许")

    @Config(sdk = [35], qualifiers = "en-w400dp-h900dp-xhdpi")
    @Test fun english() = shots("en", more = "More actions", mode = "Mode", allow = "Allow")

    @Config(sdk = [35], qualifiers = "ja-w400dp-h900dp-xhdpi")
    @Test fun japanese() = shots("ja", more = "その他の操作", mode = "モード", allow = "許可")

    @Config(sdk = [35], qualifiers = "zh-w400dp-h900dp-xhdpi")
    @Test fun assistantChatTitlesAndMessagesDoNotBecomeRosterIdentity() {
        val only = persona.core.contracts.api.HubManager("assistant", "Personal assistant name", online = true)
        var roster by mutableStateOf<persona.core.contracts.api.ManagerRoster?>(null)
        var style by mutableStateOf(AppStyle.Glass)
        var dark by mutableStateOf(false)
        val conversation = session.copy(title = "Plan a sample trip", assistant = true, manager = "assistant", status = "idle")
        val answer = Item("answer", role = "assistant", kind = "text", text = "The sample itinerary is ready.")
        rule.setContent {
            PersonaTheme(darkTheme = dark, reduceMotion = true, style = style) {
                val scope = rememberCoroutineScope()
                val resources = androidx.compose.ui.platform.LocalResources.current
                val chrome = remember { ChatChromeState(resources, null, scope, {}, {}) }
                androidx.compose.runtime.CompositionLocalProvider(LocalManagerRoster provides roster) {
                    Column(Modifier.testTag("identity-chat").width(400.dp).height(860.dp).background(PersonaTheme.palette.background)) {
                        ChatHeader(conversation, machine, listOf(conversation), chrome, null, false,
                            onBack = {}, onOpenSession = {}, onConnectionDetails = {}, onOverlay = {})
                        Box(Modifier.padding(Tokens.Space.l)) { MessageContent(answer, user = false, boxed = true) }
                    }
                }
            }
        }
        for (s in AppStyle.entries) for (d in listOf(false, true)) {
            rule.runOnIdle { style = s; dark = d; roster = null }
            val before = rule.onNodeWithTag("identity-chat").captureToImage().asAndroidBitmap()
            for ((case, installed) in listOf("single" to listOf(only),
                "multiple" to listOf(only, persona.core.contracts.api.HubManager("work", "Other assistant", online = false)))) {
                rule.runOnIdle { roster = persona.core.contracts.api.ManagerRoster(installed, "assistant") }
                rule.onNodeWithText("Plan a sample trip").assertExists()
                rule.onNodeWithText("The sample itinerary is ready.").assertExists()
                rule.onNodeWithText("Personal assistant name").assertDoesNotExist()
                rule.onNodeWithText("Other assistant").assertDoesNotExist()
                val rendered = rule.onNodeWithTag("identity-chat").captureToImage().asAndroidBitmap()
                assertTrue("Roster identity changed the conversation's chrome or attribution", before.sameAs(rendered))
                File(out(), "identity-$case-${s.key}-${if (d) "dark" else "light"}.png").outputStream().use {
                    assertTrue(rendered.compress(Bitmap.CompressFormat.PNG, 100, it))
                }
            }
        }
    }

    private enum class Scene { Header, HeaderMenu, Composer, Permission, Plan, Question, Modes, ApiError, MessageMenu }

    private val machine = Machine(id = "box", name = "workstation", online = true)
    private val session = Session(machineId = "box", sessionId = "s1", title = "Refactor the sync layer", agent = "claude",
        status = "working", paneId = "p1", cwd = "/srv/app", permissionMode = "acceptEdits", effort = "xhigh",
        queued = listOf("Also update the changelog", "Then run the tests"))

    private fun shots(language: String, more: String, mode: String, allow: String) {
        var scene by mutableStateOf<Scene?>(null)
        var dark by mutableStateOf(false)
        val permission = Interaction("p", machineId = "box", sessionId = "s1", kind = "permission", toolName = "Bash",
            toolInput = buildJsonObject { put("command", JsonPrimitive("npm test")) })
        val plan = Interaction("plan", machineId = "box", sessionId = "s1", kind = "plan", plan = "1. Read the code\n2. Write the tests\n3. Refactor")
        val question = Interaction("q", machineId = "box", sessionId = "s1", kind = "question", questions = listOf(
            Question("Which database?", header = "Storage", multiSelect = true,
                options = listOf(QuestionOption("Postgres", "The one we run"), QuestionOption("SQLite")))))
        val error = chatLines(listOf(LineItem(Item(id = "e#0", seq = 1, role = "assistant", kind = "text",
            text = "API Error: 502 no available GPU", ts = "2026-11-02T08:01:05Z")))).lines.last() as ChatLine.ApiError
        val menu = MessageMenuState()
        rule.setContent {
            PersonaTheme(darkTheme = dark, reduceMotion = true, style = AppStyle.Glass) {
                val scope = rememberCoroutineScope()
                val resources = androidx.compose.ui.platform.LocalResources.current
                val chrome = remember { ChatChromeState(resources, null, scope, {}, {}) }
                Box(Modifier.testTag("shot").width(400.dp).height(860.dp).background(PersonaTheme.palette.background)) {
                    Column(Modifier.fillMaxWidth()) {
                        when (scene) {
                            Scene.Header, Scene.HeaderMenu -> ChatHeader(session, machine, listOf(session), chrome, null, false,
                                onBack = {}, onOpenSession = {}, onConnectionDetails = {}, onOverlay = {}, onFiles = {}, onSearch = {})
                            Scene.Composer -> {
                                Spacer(Modifier.height(400.dp))
                                val composer = remember {
                                    rememberlessComposer()
                                }
                                ChatQueueStrip(session, null, composer, {}, {})
                                ChatComposer(session.key, composer, working = true, sending = false, canClear = true, service = null,
                                    onSend = {}, onCommand = {}, onNotice = {}, modeHint = chatModeHint(session),
                                    leading = { ChatModeBar(session, chrome, online = true) })
                            }
                            Scene.Permission -> Form(permission, machine)
                            Scene.Plan -> Form(plan, machine)
                            Scene.Question -> Form(question, machine)
                            Scene.Modes -> ChatModeBar(session, chrome, online = true, onPickModel = {})
                            Scene.ApiError -> ApiErrorLine(error)
                            Scene.MessageMenu -> MessageMenuSheet(menu, writes = true, onQuote = {}, onEdit = {}, onContext = {},
                                onForward = {}, onNotice = {})
                            null -> Unit
                        }
                    }
                }
            }
        }
        for (s in Scene.entries) for (d in listOf(false, true)) {
            scene = s; dark = d
            rule.waitForIdle()
            val node: SemanticsNodeInteraction? = when (s) {
                Scene.HeaderMenu -> { rule.onNodeWithContentDescription(more).performClick(); sheetTexts(language, "header-menu"); null }
                Scene.Modes -> { rule.onNodeWithContentDescription(mode, substring = true).performClick(); sheetTexts(language, "modes"); null }
                Scene.MessageMenu -> { menu.open(MessageTarget("m", "Run the tests", user = true)); sheetTexts(language, "message-menu"); null }
                Scene.ApiError -> { rule.onNodeWithText("502", substring = true).performClick(); rule.waitForIdle(); rule.onNodeWithTag("shot") }
                Scene.Permission -> { rule.onNodeWithText(allow).assertExists(); rule.onNodeWithTag("shot") }
                else -> rule.onNodeWithTag("shot")
            }
            node?.captureToImage()?.asAndroidBitmap()?.let { bitmap ->
                File(out(), "chat-$language-${s.name.lowercase()}-${if (d) "dark" else "light"}.png").outputStream()
                    .use { bitmap.compress(Bitmap.CompressFormat.PNG, 100, it) }
            }
            // Close what the scene opened so the next one starts clean.
            if (s == Scene.MessageMenu) menu.close()
            scene = null
            rule.waitForIdle()
        }
    }

    /** The request's form as the dock shows it (the dock's editor reads its drafts through Hilt, not here). */
    @androidx.compose.runtime.Composable
    private fun Form(request: Interaction, machine: Machine) {
        var draft by remember(request.id) { mutableStateOf(persona.core.domain.answer.AnswerDraft()) }
        Column(Modifier.fillMaxWidth().padding(Tokens.Space.l)) {
            InteractionForm(request, machine, draft, { draft = it }, busy = false, uncertain = false, full = false,
                onAnswer = { _, _, _ -> }, onTerminal = null, onExpand = {})
        }
    }

    /**
     * A bottom sheet does not slide up under Robolectric (its window is drawn, the sheet stays below it), so a sheet's
     * words are written out as text instead, one per line, and en must have no Chinese among them (ja is checked by eye: its kanji are Han too).
     */
    private fun sheetTexts(language: String, name: String) {
        repeat(5) { rule.mainClock.advanceTimeBy(200); rule.waitForIdle() }
        val texts = rule.onAllNodes(hasAnyAncestor(isDialog()), useUnmergedTree = true).fetchSemanticsNodes().flatMap { node ->
            node.config.getOrNull(SemanticsProperties.Text).orEmpty().map { it.text } +
                node.config.getOrNull(SemanticsProperties.ContentDescription).orEmpty()
        }.distinct()
        assertTrue("$name drew no words", texts.isNotEmpty())
        if (language == "en") texts.forEach { assertFalse("$name: $it", Regex("[\\u4e00-\\u9fff]").containsMatchIn(it)) }
        File(out(), "chat-$language-$name.txt").writeText(texts.joinToString("\n") + "\n")
    }

    private fun rememberlessComposer() = ChatComposerState(ChatComposerDraft("Please also check the ")).apply {
        attachments = listOf(
            ComposerAttachment("photo", "screen.jpg", "", true, "/tmp/screen.jpg", uploadState = DraftUploadState.Uploaded),
            ComposerAttachment("file", "notes.txt", "", false, "/tmp/notes.txt", uploadState = DraftUploadState.Uploaded),
            ComposerAttachment("big", "recording.mov", "", false, uploadState = DraftUploadState.Failed, error = null),
        )
    }
}
