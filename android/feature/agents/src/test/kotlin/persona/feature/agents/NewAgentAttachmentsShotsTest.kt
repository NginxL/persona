package persona.feature.agents

import android.graphics.Bitmap
import android.net.Uri
import androidx.activity.ComponentActivity
import androidx.activity.compose.LocalActivityResultRegistryOwner
import androidx.activity.result.ActivityResultRegistry
import androidx.activity.result.ActivityResultRegistryOwner
import androidx.activity.result.contract.ActivityResultContract
import androidx.activity.result.contract.ActivityResultContracts
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.width
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.asAndroidBitmap
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.test.assertCountEquals
import androidx.compose.ui.test.captureToImage
import androidx.compose.ui.test.filter
import androidx.compose.ui.test.hasText
import androidx.compose.ui.test.junit4.createAndroidComposeRule
import androidx.compose.ui.test.onAllNodesWithTag
import androidx.compose.ui.test.onAllNodesWithText
import androidx.compose.ui.test.onNodeWithContentDescription
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.onSiblings
import androidx.compose.ui.test.performClick
import androidx.compose.ui.test.performScrollToNode
import androidx.compose.ui.test.hasTestTag
import androidx.compose.ui.unit.dp
import androidx.core.app.ActivityOptionsCompat
import androidx.test.ext.junit.runners.AndroidJUnit4
import kotlinx.coroutines.flow.MutableStateFlow
import org.junit.Assert.*
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.Robolectric
import org.robolectric.RuntimeEnvironment
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.core.contracts.Machine
import persona.core.contracts.SessionKey
import persona.core.contracts.api.*
import persona.core.designsystem.AppStyle
import persona.core.designsystem.PersonaTheme
import persona.core.hub.HubError
import persona.core.hub.HubFailure
import persona.feature.chat.ComposerAttachment
import java.io.File

/** A hub response with its own reason, as `HttpChatComposerService` can hand the upload to the page. */
private fun shotUploadError() = HubFailure(
    HubError.Server,
    "The computer is out of disk space",
    status = 507,
    code = "insufficient_storage",
)

/**
 * New Agent's attachments on the page (#2321, iOS NewAgentAttachmentRows): Take photo through the system camera (a stand-in
 * registry answers for it) adds a photo that goes up to the computer, a cancelled one adds nothing and leaves no file, a
 * Codex launcher has no add row; then every style in light and dark with a photo, a file uploading, the line under Start
 * and the toast of a file that failed. PNGs go to `build/new-agent-attachments-shots/` (or `-Dpersona.shots=<dir>`). Machines and paths are made up.
 */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "en-w400dp-h2000dp-xhdpi")
class NewAgentAttachmentsShotsTest {
    @get:Rule val rule = createAndroidComposeRule<ComponentActivity>()

    private fun out(): File = File(System.getProperty("persona.shots") ?: "build/new-agent-attachments-shots").also { it.mkdirs() }

    /**
     * Every test here has a cache directory of its own, so each gets a FileProvider of its own. FileProvider keeps each authority's
     * roots for the process, and a provider keeps the folders of its first use for good; Robolectric makes the provider when
     * something first asks the content resolver. A thumbnail the previous test left decoding can be that something once its test is
     * over (its file is gone, so the decoder asks again), and a provider bound to that test's cache directory deletes nothing: a
     * cancelled camera's file stays, and no wait removes it. This one comes from the manifest, replaces whatever is registered and
     * takes its folders now.
     */
    @org.junit.Before fun ownFileProvider() {
        val cache = androidx.core.content.FileProvider::class.java.getDeclaredField("sCache").apply { isAccessible = true }
        (cache.get(null) as MutableMap<*, *>).clear()
        val provider = Robolectric.buildContentProvider(androidx.core.content.FileProvider::class.java).create().get()
        // There is nothing to delete: the call makes the provider take its folders under this test.
        provider.delete(Uri.parse("content://${context.packageName}.chat.files/chat-captures/none"), null, null)
    }

    private val machines = listOf(Machine("mac-a", "MacBook", online = true), Machine("work-b", "Workstation", online = true))
    private val context get() = RuntimeEnvironment.getApplication()
    private val captures get() = File(context.cacheDir, "chat-captures")

    /** The system camera: [photo] true writes a picture where it was asked to and says so; false is Cancel. */
    private inner class Camera(var photo: Boolean) : ActivityResultRegistry() {
        val launched = mutableListOf<String>()
        override fun <I, O> onLaunch(requestCode: Int, contract: ActivityResultContract<I, O>, input: I, options: ActivityOptionsCompat?) {
            launched += contract.javaClass.simpleName
            when (contract) {
                is ActivityResultContracts.TakePicture -> {
                    val file = File(captures, requireNotNull((input as Uri).lastPathSegment))
                    if (photo) file.outputStream().use { Bitmap.createBitmap(32, 24, Bitmap.Config.ARGB_8888).compress(Bitmap.CompressFormat.JPEG, 80, it) }
                    dispatchResult(requestCode, photo)
                }
                else -> dispatchResult(requestCode, null)
            }
        }
    }

    private class Composer : ChatComposerService {
        override val deliveries = MutableStateFlow<Map<String, ComposerDelivery>>(emptyMap())
        val uploads = mutableListOf<Pair<SessionKey, String>>()
        val failing = mutableSetOf<String>()
        /** Uploads that never end (still "Uploading…" in the shots). */
        val waiting = mutableSetOf<String>()
        override suspend fun upload(session: SessionKey, name: String, file: File): String {
            uploads += session to name
            if (name in failing) throw shotUploadError()
            if (name in waiting) kotlinx.coroutines.awaitCancellation()
            return "/tmp/persona-uploads/$name"
        }
        override suspend fun interrupt(session: SessionKey) = Unit
        override suspend fun retryRefused(clientMessageId: String, clearDraft: Boolean) = error("unused")
    }

    private class Sender : MessageSender {
        override val outbox = MutableStateFlow(emptyList<OutboxMessage>())
        override suspend fun send(session: SessionKey, text: String, clientMessageId: String, clearDraft: String?) = Unit
    }

    private class Store : AgentCreationStore {
        override suspend fun lastMachine(scope: String) = "mac-a"
        override suspend fun defaults(scope: String, machine: String) = AgentDefaults()
        override suspend fun saveDefaults(scope: String, machine: String, defaults: AgentDefaults) = Unit
        override suspend fun pending(scope: String): AgentStartOperation? = null
        override suspend fun pending(scope: String, value: AgentStartOperation?) = Unit
    }

    private class Service : AgentCreationService {
        override val scope = "hub/pairing"
        override suspend fun launchers(machineId: String) =
            listOf(AgentLauncher("claude", model = "opus"), AgentLauncher("codex", agentType = "codex", model = "gpt-5.5"))
        override suspend fun directories(machineId: String, path: String) = AgentDirectories(path.ifEmpty { "/code/persona" })
        override suspend fun start(request: AgentStartRequest, operationId: String): AgentStartResult = AgentStartResult.Unknown
        override suspend fun operation(operationId: String): AgentStartResult = AgentStartResult.Unknown
        override suspend fun newAgentModel(): ModelPurposeDetail? = null
    }

    private fun model(composer: Composer) = NewAgentViewModel(Service(), Store(), MutableStateFlow(machines), Sender(),
        uploads = NewAgentUploads(
            service = { composer },
            // The service gets the picked name; staging the JPEG (ImageDecoder) is the composer's, and Robolectric has none.
            upload = { service, session, attachment, onStaged ->
                onStaged(attachment.size ?: 0)
                service.upload(session, attachment.name, File(attachment.name))
            },
            photosOnly = { "Please look at the photos" },
            filesOnly = { "Please look at the attached files" },
            errorText = { name, error -> uploadFailureRow(name, error).resolve(context.resources) },
            discard = { persona.feature.chat.deleteCapture(context, it.uri) },
    ))

    private fun show(model: NewAgentViewModel, camera: Camera, style: () -> AppStyle = { AppStyle.Glass }, dark: () -> Boolean = { false }) {
        val owner = object : ActivityResultRegistryOwner { override val activityResultRegistry = camera }
        rule.setContent {
            CompositionLocalProvider(LocalActivityResultRegistryOwner provides owner) {
                PersonaTheme(darkTheme = dark(), reduceMotion = true, style = style()) {
                    Box(Modifier.testTag("shot").width(400.dp).height(2000.dp).background(PersonaTheme.palette.background)) {
                        NewAgentPage(model, machines, online = true, onBack = {})
                    }
                }
            }
        }
        rule.waitForIdle()
    }

    /** + › Take photo; tried again while the camera was not asked for (a tap can land while the menu still slides in on a busy machine). */
    private fun takePhoto(camera: Camera) {
        repeat(3) {
            if ("TakePicture" in camera.launched) return
            if (rule.onAllNodesWithText("Take photo").fetchSemanticsNodes().isEmpty()) rule.onNodeWithTag("newAgent.attach").performClick()
            rule.waitUntil(30_000) { rule.onAllNodesWithText("Take photo").fetchSemanticsNodes().isNotEmpty() }
            rule.waitForIdle()
            rule.onNodeWithText("Take photo").performClick()
            rule.waitForIdle()
        }
        assertTrue("the camera was asked for", "TakePicture" in camera.launched)
    }

    @Test fun aPhotoTakenWithTheCameraGoesUpAndShowsAsAThumbnail() {
        captures.deleteRecursively()
        val composer = Composer()
        val model = model(composer)
        val camera = Camera(photo = true)
        show(model, camera)
        takePhoto(camera)
        // The view model's upload runs on the main looper, which Robolectric keeps paused: run what is queued on it while waiting.
        val _ = runCatching { rule.waitUntil(30_000) { org.robolectric.Shadows.shadowOf(android.os.Looper.getMainLooper()).idle(); model.state.value.attachments.singleOrNull()?.path != null } }
        assertNotNull("the photo went up: ${model.state.value.attachments} uploads=${composer.uploads} launched=${camera.launched} main=${kotlinx.coroutines.Dispatchers.Main} files=${captures.list()?.toList()}", model.state.value.attachments.singleOrNull()?.path)
        val photo = model.state.value.attachments.single()
        assertTrue(photo.image)
        assertTrue(photo.uri.startsWith("content://${context.packageName}.chat.files/chat-captures/"))
        assertEquals(SessionKey("mac-a", ""), composer.uploads.single().first)
        assertTrue(composer.uploads.single().second.startsWith("camera-"))
        rule.onNodeWithContentDescription("Photo 1").assertExists()
        assertNull(model.state.value.attachmentsHold)
        // ✕ takes it out, and the capture leaves the cache with it.
        rule.runOnIdle { model.removeAttachment(photo.id) }
        rule.waitUntil(30_000) { captures.listFiles().isNullOrEmpty() }
        assertTrue(model.state.value.attachments.isEmpty())
    }

    @Test fun aCancelledCameraAddsNothingAndLeavesNoFile() {
        captures.deleteRecursively()
        val model = model(Composer())
        val camera = Camera(photo = false)
        show(model, camera)
        takePhoto(camera)
        assertEquals(listOf("TakePicture"), camera.launched)
        // The page deletes the file it gave the camera as soon as the camera says Cancel. Wait for the folder as the removal
        // above does, then check the draft and the folder; a file left comes with what the page asked the content resolver to delete.
        val _ = runCatching { rule.waitUntil(30_000) { captures.listFiles().isNullOrEmpty() } }
        rule.waitForIdle()
        assertTrue(model.state.value.attachments.isEmpty())
        // A busy worker can deliver the camera's cancellation after takePhoto returns. Wait for its cleanup just as the
        // removal test above does; a capture that is never deleted still fails this assertion.
        rule.waitUntil(30_000) {
            org.robolectric.Shadows.shadowOf(android.os.Looper.getMainLooper()).idle()
            captures.listFiles().isNullOrEmpty()
        }
        val deleted = org.robolectric.Shadows.shadowOf(context.contentResolver).deleteStatements.map { it.uri.lastPathSegment }
        assertEquals("deleted through the content resolver: $deleted", emptyList<String>(), captures.list().orEmpty().toList())
    }

    @Test fun aCodexLauncherHasNoAddRowAndSaysWhyAttachmentsHoldTheStart() {
        val model = model(Composer())
        show(model, Camera(photo = true))
        rule.onNodeWithTag("newAgent.attach").assertExists()
        rule.runOnIdle { model.attach(listOf(ComposerAttachment("f", "build.log", "content://docs/build.log", image = false, size = 4096))) }
        rule.runOnIdle { model.selection(model.state.value.selection.copy(launcher = "codex")) }
        rule.waitForIdle()
        assertTrue(rule.onAllNodesWithTag("newAgent.attach").fetchSemanticsNodes().isEmpty())
        rule.onNodeWithText("Codex takes no attachments").assertExists()
        rule.onNodeWithText("This launcher can't take attachments", substring = true).assertExists()
    }

    @Test fun everyStyleLightAndDark() {
        captures.deleteRecursively()
        var style by mutableStateOf(AppStyle.Glass)
        var dark by mutableStateOf(false)
        val composer = Composer().apply { failing += "build.log"; waiting += "notes.txt" }
        val model = model(composer)
        val camera = Camera(photo = true)
        show(model, camera, { style }, { dark })
        takePhoto(camera)
        rule.runOnIdle { model.attach(listOf(ComposerAttachment("n", "notes.txt", "content://docs/notes.txt", image = false, size = 4096))) }
        // As in the upload test above, drive the paused main looper while the upload runs on it; a busy CI worker
        // otherwise leaves the condition waiting even though the upload is queued and ready.
        rule.waitUntil(30_000) {
            org.robolectric.Shadows.shadowOf(android.os.Looper.getMainLooper()).idle()
            model.state.value.attachments.firstOrNull { it.image }?.path != null
        }
        assertEquals(R.string.agents_attach_uploading_hold, model.state.value.attachmentsHold?.id)
        for (s in AppStyle.entries) for (d in listOf(false, true)) {
            rule.runOnIdle { style = s; dark = d }
            // A failed file stays with its reason and Retry; only the separate removal action takes it out.
            val failedId = "f$s$d"
            rule.runOnIdle { model.attach(listOf(ComposerAttachment(failedId, "build.log", "content://docs/build.log", image = false, size = 4096))) }
            rule.waitUntil(30_000) {
                model.state.value.attachments.firstOrNull { it.id == failedId }?.uploadState == DraftUploadState.Failed
            }
            rule.waitForIdle()
            rule.onNodeWithText("Couldn't send “build.log”", substring = true).assertExists()
            val reason = uploadFailureRow("build.log", shotUploadError()).resolve(context.resources)
            rule.onNodeWithText(reason).assertExists()
            // The title says the name once; neither its subtitle nor another part of this row repeats it.
            rule.onNodeWithText("build.log").onSiblings()
                .filter(hasText("build.log", substring = true)).assertCountEquals(0)
            assertEquals(listOf("notes.txt", "build.log"), model.state.value.attachments.filterNot { it.image }.map { it.name })
            rule.onNodeWithText("Retry").assertExists()
            rule.onNodeWithText("Uploading", substring = true).assertExists()
            rule.onNodeWithTag("newAgent.attachHold").assertExists()
            val bitmap = rule.onNodeWithTag("shot").captureToImage().asAndroidBitmap()
            File(out(), "attachments-${s.name.lowercase()}-${if (d) "dark" else "light"}.png").outputStream()
                .use { bitmap.compress(Bitmap.CompressFormat.PNG, 100, it) }
            rule.runOnIdle { model.removeAttachment(failedId) }
        }
    }

    @Test fun aTripToTheCameraOrThePickerKeepsTheAttachments() {
        captures.deleteRecursively()
        val model = model(Composer())
        val camera = Camera(photo = true)
        show(model, camera)
        rule.runOnIdle { model.attach(listOf(ComposerAttachment("f", "notes.txt", "content://docs/notes.txt", image = false, size = 4096))) }
        // The system picker covers the app: the activity stops and comes back, the form stays composed.
        rule.activityRule.scenario.moveToState(androidx.lifecycle.Lifecycle.State.CREATED)
        rule.activityRule.scenario.moveToState(androidx.lifecycle.Lifecycle.State.RESUMED)
        rule.waitForIdle()
        takePhoto(camera)
        rule.waitUntil(30_000) { model.state.value.attachments.size == 2 }
        assertEquals(listOf("notes.txt"), model.state.value.attachments.filterNot { it.image }.map { it.name })
        assertEquals(1, model.state.value.attachments.count { it.image })
    }

    @Test fun aRotationKeepsTheAttachments() {
        val model = model(Composer())
        show(model, Camera(photo = true))
        rule.runOnIdle { model.attach(listOf(ComposerAttachment("f", "notes.txt", "content://docs/notes.txt", image = false, size = 4096))) }
        rule.waitForIdle()
        // A configuration change disposes the form while the activity is changing configurations: nothing is cleared.
        rule.activityRule.scenario.recreate()
        assertEquals(listOf("notes.txt"), model.state.value.attachments.map { it.name })
    }
}
