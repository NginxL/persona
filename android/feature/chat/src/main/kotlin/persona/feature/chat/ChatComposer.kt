package persona.feature.chat

import android.Manifest
import android.content.ActivityNotFoundException
import android.graphics.Bitmap
import android.net.Uri
import androidx.activity.compose.rememberLauncherForActivityResult
import androidx.activity.result.PickVisualMediaRequest
import androidx.activity.result.contract.ActivityResultContracts
import androidx.compose.animation.animateContentSize
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.text.BasicTextField
import androidx.compose.material3.Text
import androidx.compose.runtime.*
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.focus.focusRequester
import androidx.compose.ui.focus.onFocusChanged
import androidx.compose.ui.layout.onSizeChanged
import androidx.compose.ui.graphics.SolidColor
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.platform.LocalResources
import androidx.compose.ui.platform.LocalSoftwareKeyboardController
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.semantics.testTagsAsResourceId
import androidx.compose.ui.text.TextRange
import androidx.compose.ui.text.input.TextFieldValue
import androidx.compose.ui.text.style.TextOverflow
import androidx.core.content.FileProvider
import persona.core.contracts.SessionKey
import persona.core.contracts.api.ChatComposerService
import persona.core.contracts.api.ComposerOperationException
import persona.core.contracts.api.DraftUploadState
import persona.core.designsystem.*
import persona.core.designsystem.icons.PhIcons
import persona.feature.chat.media.MediaShare
import java.io.File
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.NonCancellable
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext

/** How long an upload waits for the hub connection to be ready again before it fails (a setting, not measured). */
internal const val UPLOAD_READY_WAIT_MILLIS = 15_000L

@OptIn(androidx.compose.ui.ExperimentalComposeUiApi::class)
@Composable
fun ChatComposer(
    session: SessionKey,
    state: ChatComposerState,
    working: Boolean,
    sending: Boolean,
    canClear: Boolean,
    service: ChatComposerService?,
    onSend: () -> Unit,
    onCommand: (String) -> Unit,
    onNotice: (String) -> Unit,
    // Content queued outside the text field (selected page context) also makes the draft sendable.
    extraContent: Boolean = false,
    enabled: Boolean = true,
    placeholder: String = stringResource(R.string.chat_composer_placeholder),
    leading: (@Composable RowScope.() -> Unit)? = null,
    accessory: (@Composable ColumnScope.() -> Unit)? = null,
    modeHint: String? = null,
    onOverlay: (Boolean) -> Unit = {},
    visible: Boolean = true,
    /** Who /clear restarts, for its confirmation ("Claude will start a new session…"). */
    agentName: String = "Claude",
    /** False for a session whose source does not let the phone interrupt it: no stop while it works. */
    canInterrupt: Boolean = true,
    /** The @ menu's files and sessions (iOS PromptItems); null: no @ button and no list while typing @. */
    mentions: ComposerMentions? = null,
    /** A session picked in the @ menu, as the context that goes with the next message. */
    onMention: (ChatLinkContext) -> Unit = {},
    /** A task picked in the @ menu (Persona's conversations, [ComposerMentions.tasks]): the next message goes to it. */
    onMentionTask: (String) -> Unit = {},
    /** Whether the @ menu is open over the field (iOS `MentionMenuOpen`): New chat's page makes room for it. */
    onMentionOpen: (Boolean) -> Unit = {},
    /** /model in the "/" list, which opens the model choices instead of typing (Claude in a terminal pane). */
    onModel: (() -> Unit)? = null,
    /** /loop in the "/" list, with a line under the box saying what it will do (Claude Code only, web `hasLoop`). */
    loop: Boolean = false,
    /**
     * What has to happen before anything can be sent ("Connect a model first", "Add a computer first": docs/assistant/onboarding.md 4.4): the send button says
     * it and [onBlocked] opens that step. The box still takes typing and keeps the draft; nothing is queued.
     */
    blockedLabel: String? = null,
    onBlocked: () -> Unit = {},
    /**
     * The model pill (#5055, Glass): at the right of the toolbar row, before the mic and Send, while the toolbar shows; it
     * gets how much room the composer has ([ModelPillWidth], the web's container rules). Null: none.
     */
    modelPill: (@Composable (ModelPillWidth) -> Unit)? = null,
) {
    val context = LocalContext.current
    val adaptiveProbePage = "chat:${session.path}"
    val resources = LocalResources.current
    val photosOnly = stringResource(R.string.chat_composer_photos_only)
    val videosOnly = stringResource(R.string.chat_composer_videos_only)
    SideEffect { state.photosOnlyText = photosOnly; state.videosOnlyText = videosOnly }
    val hasContent = state.hasContent || extraContent
    val scope = rememberCoroutineScope()
    var menu by remember { mutableStateOf(false) }
    var confirmClear by remember { mutableStateOf(false) }
    var cameraDenied by remember { mutableStateOf(false) }
    val focusRequester = remember { androidx.compose.ui.focus.FocusRequester() }
    val keyboard = LocalSoftwareKeyboardController.current
    // A photo in the draft being marked up (iOS editingIndex); [editOpening] while it is read for the editor.
    var editing by remember { mutableStateOf<ComposerPhotoEdit?>(null) }
    var editOpening by remember { mutableStateOf(false) }
    // The draft full screen (#5688): offered once the field has grown to its line limit and scrolls.
    var expandedEditor by rememberSaveable { mutableStateOf(false) }
    LaunchedEffect(menu, confirmClear, cameraDenied, editing, expandedEditor) {
        onOverlay(menu || confirmClear || cameraDenied || editing != null || expandedEditor)
    }
    DisposableEffect(Unit) { onDispose { onOverlay(false) } }
    var cameraUri by rememberSaveable { mutableStateOf<String?>(null) }
    var interruptPending by rememberSaveable { mutableStateOf(false) }
    LaunchedEffect(working) { if (!working) interruptPending = false }
    // A photo taken here waits in cache/chat-captures until it leaves this draft: removed, or sent and cleared. Only the
    // draft's own captures go; the folder is never swept (another conversation's draft may still hold one).
    LaunchedEffect(state) {
        var before = state.attachments to state.photoEdits.values.map { it.sourceUri }
        snapshotFlow { state.attachments to state.photoEdits.filterKeys { id -> state.attachments.any { it.id == id } }.values.map { it.sourceUri } }.collect { now ->
            val gone = leftCaptures(before.first, now.first, context.packageName, before.second, now.second)
            before = now
            if (gone.isNotEmpty()) withContext(Dispatchers.IO) { gone.forEach { deleteCapture(context, it) } }
        }
    }

    // [media]: from the photo picker, the camera or the clipboard, photos and videos sharing four places (iOS).
    fun select(uris: List<Uri>, media: Boolean, capture: Boolean = false) {
        scope.launch {
            val allowed = if (media) (MAX_COMPOSER_MEDIA - state.attachments.count { it.media }).coerceAtLeast(0) else uris.size
            if (uris.size > allowed) onNotice(resources.getString(R.string.chat_composer_photo_limit))
            if (capture) withContext(Dispatchers.IO) { uris.drop(allowed).forEach { deleteCapture(context, it.toString()) } }
            for (uri in uris.take(allowed)) {
                try {
                    val attachment = withContext(Dispatchers.IO) {
                        val video = media && isVideo(context, uri)
                        selectAttachment(context, uri, image = media && !video, video = video)
                    }
                    state.beginEdit()
                    state.attachments += attachment
                    attachment.error?.let(onNotice)
                } catch (e: CancellationException) { throw e }
                catch (_: Exception) {
                    if (capture) withContext(Dispatchers.IO) { deleteCapture(context, uri.toString()) }
                    onNotice(resources.getString(R.string.chat_composer_attachment_unreadable))
                }
            }
        }
    }
    val photos = rememberLauncherForActivityResult(ActivityResultContracts.PickMultipleVisualMedia(MAX_COMPOSER_MEDIA)) { select(it, true) }
    val files = rememberLauncherForActivityResult(ActivityResultContracts.OpenMultipleDocuments()) { select(it, false) }
    val camera = rememberLauncherForActivityResult(ActivityResultContracts.TakePicture()) { success ->
        cameraUri?.let { value ->
            if (success) select(listOf(Uri.parse(value)), true, capture = true)
            else runCatching { context.contentResolver.delete(Uri.parse(value), null, null) }
        }
        cameraUri = null
    }
    fun openCamera() {
        val folder = File(context.cacheDir, "chat-captures").apply { mkdirs() }
        val file = File.createTempFile("camera-", ".jpg", folder)
        val uri = FileProvider.getUriForFile(context, "${context.packageName}.chat.files", file)
        cameraUri = uri.toString()
        try { camera.launch(uri) }
        catch (error: Exception) {
            file.delete(); cameraUri = null
            onNotice(resources.getString(if (error is ActivityNotFoundException) R.string.chat_composer_no_camera_app else R.string.chat_composer_camera_failed))
        }
    }
    // The app declares CAMERA (pairing scans codes), so Android starts another app's camera for it only once granted.
    val cameraPermission = rememberLauncherForActivityResult(ActivityResultContracts.RequestPermission()) { granted ->
        if (granted) openCamera() else cameraDenied = true
    }
    fun takePhoto() {
        menu = false
        if (cameraAllowed(context)) openCamera()
        else try { cameraPermission.launch(Manifest.permission.CAMERA) } catch (_: Exception) { cameraDenied = true }
    }
    fun launchPicker(action: () -> Unit) {
        menu = false
        try { action() } catch (_: Exception) { onNotice(resources.getString(R.string.chat_composer_picker_failed)) }
    }

    // A picture on the clipboard, offered over the focused composer on Glass (iOS PasteImageChip): Paste copies it in as a photo.
    val clipboardImages = rememberClipboardImages(state.focused)
    fun pasteImages() {
        val clip = runCatching { clipboardImages.clipboard?.primaryClip }.getOrNull()
        clipboardImages.dismiss()
        if (clip == null) return
        scope.launch {
            val pasted = withContext(Dispatchers.IO) { pastedImages(context, clip, MAX_COMPOSER_MEDIA) }
            if (pasted.isEmpty()) onNotice(resources.getString(R.string.chat_composer_attachment_unreadable))
            else select(pasted, media = true, capture = true)
        }
    }

    // A tap on a photo that is on the computer: the editor opens on it, and "Done" puts the marked-up copy in its place.
    fun editPhoto(photo: ComposerAttachment) {
        if (editing != null || editOpening || sending) return
        editOpening = true
        scope.launch {
            try {
                // An edited photo opens from its source with its edits, still undoable; its source gone, it starts over on itself.
                val prior = state.photoEdits[photo.id]
                val source = prior?.let { runCatching { withContext(Dispatchers.IO) { decodePhoto(context, Uri.parse(it.sourceUri)) } }.getOrNull() }
                editing = if (prior != null && source != null) ComposerPhotoEdit(photo, source, prior.sourceUri, prior)
                    else ComposerPhotoEdit(photo, withContext(Dispatchers.IO) { decodePhoto(context, Uri.parse(photo.uri)) })
            } catch (e: CancellationException) { throw e }
            catch (_: Exception) { onNotice(resources.getString(R.string.chat_attach_edit_unreadable)) }
            finally { editOpening = false }
        }
    }
    // "Done": the editor spins while the new photo is written (JPEG like a picked photo) and uploaded, then closes. Only an
    // upload that worked replaces the original; the old path leaves the message with it.
    fun finishEdit(edit: ComposerPhotoEdit, result: Bitmap, done: ImageEditState) {
        if (result === edit.source && edit.prior == null) { editing = null; return } // nothing done to a photo never edited
        scope.launch {
            var edited: ComposerAttachment? = null
            var kept = false
            try {
                edited = MediaShare.writeEditedPhoto(context, result)
                val carried = PhotoEdits(edit.sourceUri, done).takeUnless { done.edits.isEmpty }
                val outcome = state.replaceWithEdited(edit.original, edited, carried) { photo ->
                    uploadAttachment(context, checkNotNull(service), session, photo)
                }
                kept = outcome == PhotoEditOutcome.Replaced
                if (outcome is PhotoEditOutcome.Failed) onNotice(resources.getString(R.string.chat_attach_edit_failed))
            } catch (e: CancellationException) { throw e }
            catch (_: Exception) { onNotice(resources.getString(R.string.chat_attach_edit_failed)) }
            finally {
                // A copy that did not make it into the draft is not left in cache/chat-captures.
                if (!kept) edited?.let { withContext(NonCancellable + Dispatchers.IO) { deleteCapture(context, it.uri) } }
                if (editing === edit) editing = null
            }
        }
    }

    state.attachments.forEach { attachment ->
        key(attachment.id) {
            LaunchedEffect(attachment.id, attachment.error, service) {
                if (attachment.uploadState == DraftUploadState.Invalid) return@LaunchedEffect
                if (!withContext(Dispatchers.IO) { attachmentReadable(context, attachment) }) {
                    val reason = resources.getString(R.string.chat_composer_attachment_gone)
                    state.attachments = state.attachments.map {
                        if (it.id == attachment.id) it.copy(uploadState = DraftUploadState.Invalid, error = reason) else it
                    }
                    onNotice(reason)
                    return@LaunchedEffect
                }
                if (attachment.path != null) return@LaunchedEffect
                if (attachment.error != null || service == null) return@LaunchedEffect
                try {
                    state.attachments = state.attachments.map {
                        if (it.id == attachment.id) it.copy(uploadState = DraftUploadState.Uploading) else it
                    }
                    val path = uploadAttachment(context, service, session, attachment)
                    if (state.attachments.any { it.id == attachment.id }) {
                        state.attachments = state.attachments.map {
                            if (it.id == attachment.id) it.copy(path = path, uploadState = DraftUploadState.Uploaded) else it
                        }
                    }
                } catch (e: CancellationException) { throw e }
                catch (e: Exception) {
                    val reason = (e as? ComposerOperationException)?.message ?: resources.getString(R.string.chat_composer_upload_failed)
                    state.attachments = state.attachments.map {
                        if (it.id == attachment.id) it.copy(error = reason, uploadState = DraftUploadState.Failed) else it
                    }
                }
            }
        }
    }

    LaunchedEffect(visible) { if (!visible) { menu = false; confirmClear = false; cameraDenied = false; editing = null; expandedEditor = false } }
    editing?.let { edit ->
        var done by remember(edit) { mutableStateOf(ImageEditState()) }
        key(edit) { ImageEditor(edit.source, onCancel = { if (editing === edit) editing = null }, onDone = { finishEdit(edit, it, done) },
            start = edit.prior?.state ?: ImageEditState(), onEdited = { done = it }) }
    }
    // Voice input: what the phone's recognizer heard goes at the end of the draft, to edit before sending.
    val dictation = rememberDictation(onHeard = { state.appendDictation(it) }, onError = onNotice)
    if (!visible) return
    if (AdaptiveProbe.enabled) {
        AdaptiveComposerProbe(adaptiveProbePage, state.value, state.attachments.map { it.id to it.uploadState.name }, state.draftLoaded)
    }
    LaunchedEffect(state.focusRequest) { if (state.focusRequest > 0) focusRequester.requestFocus() }
    // A read-only session (enabled = false) offers nothing to type with: no +, no mic, no "/" list, no send.
    val dictate = dictation?.takeIf { enabled }
    // The main conversation's /clear (#5100): offered while it works too (the hub stops the turn), and it never asks first.
    val mainClear = LocalMainChatClear.current?.takeIf { enabled }
    val suggestions = if (enabled) SlashCommand.suggestions(state.value.text, canClear && !working || mainClear != null,
        model = onModel != null, loop = loop) else emptyList()
    val hint = if (enabled && loop) loopHint(state.value.text, resources) else null
    // @: the word being typed just before the caret, while nothing is selected and no "/" list is open.
    val selection = state.value.selection
    val mention = if (enabled && mentions != null && suggestions.isEmpty() && selection.collapsed) mentionAt(state.value.text, selection.start) else null
    val mentionOpen = mention != null
    LaunchedEffect(mentionOpen) { onMentionOpen(mentionOpen) }
    DisposableEffect(Unit) { onDispose { onMentionOpen(false) } }
    // A typed folder (New chat's page): read from the computer while the list is open.
    val browsing = rememberMentionListing(mentions, mention?.query)
    val mentionRows = remember(mentions, mention?.query, browsing.listing) {
        if (mentions != null && mention != null) mentionRows(mentions, mention.query, browsing.listing) else emptyList()
    }
    val usedFile = stringResource(R.string.chat_mention_used_file)
    val folderWord = stringResource(R.string.chat_mention_folder)
    val sessionFolderWord = stringResource(R.string.chat_mention_session_folder)
    // With tasks in the list each group says what a pick does: iOS's "Message a Task", then the files and sessions.
    val taskSection = stringResource(R.string.chat_tasks_mention_title)
    val otherSection = stringResource(R.string.chat_mention).takeIf { mentionRows.any { it is MentionRow.Task } }
    val mentionItems = mentionRows.map { row ->
        when (row) {
            is MentionRow.Task -> ComposerSuggestion(row.task.title, row.task.place, key = row.key, flexible = true,
                section = taskSection, status = row.task.status)
            is MentionRow.File -> {
                val relative = row.insert.removePrefix("@").trimEnd()
                ComposerSuggestion(relative.substringAfterLast('/'), relative.substringBeforeLast('/', "").ifEmpty { usedFile }, key = row.key,
                    flexible = true, section = otherSection)
            }
            is MentionRow.Quote -> ComposerSuggestion(row.session.displayTitle,
                stringResource(R.string.chat_mention_session_detail, sessionMentionDetail(row.session)), key = row.key, flexible = true,
                section = otherSection)
            // An entry of a folder: its name, a folder's with a slash, and the folder typed so far (iOS `MentionRows`); one of
            // the session's own folder says so ("Folder" for a folder, "Session folder" for a file), among the other groups.
            is MentionRow.Entry -> ComposerSuggestion(if (row.folder) "${row.name}/" else row.name,
                if (!row.top) row.typed else if (row.folder) folderWord else sessionFolderWord, key = row.key, flexible = true,
                section = otherSection.takeIf { row.top })
        }
    }
    // `@` at the caret, whose list opens: the @ button's, and the + menu's Mention a file or session (iOS `typeMention`).
    fun startMention() { state.beginEdit(); state.value = typeMention(state.value); state.focusRequest++ }
    fun pickMention(picked: ComposerSuggestion) {
        val token = mention ?: return
        val row = mentionRows.firstOrNull { it.key == picked.key } ?: return
        state.beginEdit()
        when (row) {
            // The @word goes; the next message goes to the task (iOS `TaskMentionList`), as its Message sets it.
            is MentionRow.Task -> { onMentionTask(row.task.id); state.value = replaceMention(state.value, token, "") }
            is MentionRow.File -> state.value = replaceMention(state.value, token, row.insert)
            // A folder stays typed with its slash, and the list browses it.
            is MentionRow.Entry -> state.value = replaceMention(state.value, token, row.insert)
            // A session goes with the message as a quote, not as text (web pickMention).
            is MentionRow.Quote -> { onMention(sessionQuote(row.session, resources)); state.value = replaceMention(state.value, token, "") }
        }
        state.focusRequest++
    }
    val extensionPrompts = if (enabled) LocalExtensionPrompts.current else emptyList()
    val quick = ExtensionPromptText.matching(extensionPrompts, state.value.text)
    // The main conversation's /clear: the box empties and the hub clears it; /clear goes back into the box if it did not.
    fun clearMain(clear: MainChatClear) {
        state.beginEdit()
        state.value = TextFieldValue()
        clear.clear { state.value = TextFieldValue(SlashCommand.CLEAR, TextRange(SlashCommand.CLEAR.length)) }
    }
    // /clear sent alone in the main conversation clears it instead of going to Persona (web `isClearCommand`).
    fun send() {
        val clear = mainClear
        if (clear != null && isMainClearCommand(state.value.text, state.attachments.size, extraContent)) clearMain(clear) else onSend()
    }
    // Picking a quick message puts its words in the box to send; /clear empties the box and asks first (the main
    // conversation's clears at once).
    fun pick(command: SlashCommand) {
        state.beginEdit()
        when {
            command.clear && mainClear != null -> clearMain(mainClear)
            command.clear -> { state.value = TextFieldValue(); confirmClear = true }
            command.model -> { state.value = TextFieldValue(); onModel?.invoke() }
            else -> state.value = TextFieldValue(command.text, TextRange(command.text.length))
        }
    }

    // The field grows with the draft through six lines, then scrolls inside and offers Expand.
    // The text keeps clear of the button only while it shows.
    val growLines = ComposerGrowLines
    var lineCount by remember { mutableIntStateOf(1) }
    val offersExpand = enabled && composerOffersExpand(lineCount, growLines)
    LaunchedEffect(enabled) { if (!enabled) expandedEditor = false }
    val canSend = enabled && state.ready && !sending
    val homeAssistant = LocalHomeChrome.current?.let { it.items.firstOrNull()?.key == it.selected } == true
    val fullToolbar = !homeAssistant || state.focused || hasContent
    // Expand once on focus, not when the first character arrives. ComposerFrame keeps the field in the same row across
    // this change, so moving the controls cannot dispose the field and cancel the tap or the IME.
    val expanded = state.focused || hasContent
    val composerPlaceholder = if (homeAssistant) stringResource(R.string.chat_composer_ask_persona, LocalPersonaName.current) else placeholder

    // The composer's own width, for how much of the model pill fits (the web measures its composer card the same way).
    val density = androidx.compose.ui.platform.LocalDensity.current
    var composerDp by remember { mutableFloatStateOf(0f) }
    ComposerFrame(
        modifier = if (modelPill != null) Modifier.onSizeChanged { composerDp = with(density) { it.width.toDp().value } } else Modifier,
        // Blank space around the field (its padding, beside the buttons) types too: one tap, not a hunt for the line.
        onBlankTap = { if (enabled) { focusRequester.requestFocus(); keyboard?.show() } },
        expanded = expanded,
        leading = {
            JIconButton(PhIcons.Plus, stringResource(R.string.chat_composer_add), { menu = true }, enabled = !sending && enabled)
            if (expanded) {
                if (mentions != null) JIconButton(PhIcons.At, stringResource(R.string.chat_mention), ::startMention, enabled = enabled)
                leading?.invoke(this)
            }
        },
        trailing = {
            if (expanded && enabled) modelPill?.invoke(ModelPillWidth.of(composerDp))
            // The mic stays (iOS ShellComposerBar): more can be dictated after what is typed. Send beside it once there is
            // something to send, else a stop while the agent works; nothing else while the box is empty. On Glass the button
            // changing grows and shrinks as iOS's does (ComposerActionSlot, #4210 T3).
            dictate?.let { JIconButton(PhIcons.Microphone, stringResource(R.string.chat_composer_dictate), it, enabled = !sending) }
            if (blockedLabel != null && enabled) BlockedSendButton(blockedLabel, onBlocked)
            else ComposerActionSlot(
                when {
                    hasContent -> ComposerAction(SendKind.Send, enabled = canSend, loading = sending)
                    working && canInterrupt -> ComposerAction(SendKind.Stop, enabled = !sending && !interruptPending && service != null)
                    dictate == null -> ComposerAction(SendKind.Send, enabled = false, loading = sending)
                    else -> null
                },
                // A lambda, not `::send`: references to a local function compare equal whatever they capture, so the slot was
                // skipped and kept calling the `send` from before a quote joined the draft (New chat's first message lost it).
                onSend = { send() },
                onStop = {
                    if (service != null) {
                        interruptPending = true
                        scope.launch {
                            try { service.interrupt(session); onNotice(resources.getString(R.string.chat_composer_interrupt_requested)) }
                            catch (e: CancellationException) { throw e }
                            catch (_: Exception) { onNotice(resources.getString(R.string.chat_composer_interrupt_unknown)) }
                        }
                    }
                },
            )
        },
        accessory = {
            if (state.oldVersion) Text(stringResource(R.string.chat_composer_old_version), style = PersonaTheme.type.caption,
                color = PersonaTheme.palette.cautionText)
            if (PersonaTheme.style == AppStyle.Glass && enabled && !sending && service != null && state.focused && clipboardImages.offered)
                PasteImageChip(onPaste = ::pasteImages, onDismiss = clipboardImages::dismiss)
            ComposerAttachmentPreviews(state, service, sending, enabled, onEdit = ::editPhoto)
            if (fullToolbar) accessory?.invoke(this)
            if (fullToolbar) modeHint?.let { JChip(it, icon = PhIcons.SlidersHorizontal) }
            if (working && hasContent && enabled) Text(stringResource(R.string.chat_composer_will_queue), style = PersonaTheme.type.caption, color = PersonaTheme.palette.ink2)
            // A draft that is one "/" word: the quick messages it can be, right above the field.
            ComposerSuggestions(quick.map { ComposerSuggestion(it.title, it.extensionName, key = "${it.extension}/${it.id}", flexible = true) },
                onPick = { picked -> quick.firstOrNull { "${it.extension}/${it.id}" == picked.key }?.let { state.fillQuickPrompt(it) } },
                modifier = Modifier.padding(vertical = Tokens.Space.xs))
            ComposerSuggestions(suggestions.map { ComposerSuggestion(it.word, it.detail, destructive = it.clear && mainClear == null) },
                onPick = { picked -> suggestions.firstOrNull { it.word == picked.word }?.let(::pick) },
                modifier = Modifier.padding(vertical = Tokens.Space.xs))
            // The @ menu: Persona's open tasks, files this chat used, the session folder's entries, then other sessions; a word
            // that matches nothing says so. On Glass it grows in and out in iOS's state motion, rising from the field, as the
            // lines over New chat's composer step aside for it (`NeedsStepAside`).
            Column(Modifier.fillMaxWidth().then(if (PersonaTheme.style == AppStyle.Glass && !PersonaTheme.reduceMotion)
                Modifier.animateContentSize(PersonaTheme.motion.enter(), Alignment.BottomStart) else Modifier)) {
                ComposerSuggestions(mentionItems, onPick = ::pickMention, modifier = Modifier.padding(vertical = Tokens.Space.xs))
                if (mention != null && mentionItems.isEmpty()) Text(stringResource(when {
                        browsing.reading -> R.string.chat_mention_reading_folder
                        browsing.browse != null -> R.string.chat_mention_no_match_folder
                        else -> R.string.chat_mention_no_match
                    }), Modifier.padding(vertical = Tokens.Space.xs), style = PersonaTheme.type.caption, color = PersonaTheme.palette.ink2)
            }
            hint?.let { Text(it.text, style = PersonaTheme.type.caption, color = if (it.missing) PersonaTheme.palette.cautionText else PersonaTheme.palette.ink2) }
        },
    ) {
        BasicTextField(state.value, { state.beginEdit(); state.value = it },
            modifier = Modifier.fillMaxWidth().then(if (AdaptiveProbe.enabled) Modifier.semantics { testTagsAsResourceId = true }
                .testTag("chat-composer-field") else Modifier)
                .then(if (offersExpand) Modifier.padding(end = ComposerExpandInset) else Modifier)
                .focusRequester(focusRequester).onFocusChanged {
                    state.focused = it.isFocused
                    if (AdaptiveProbe.enabled) AdaptiveProbe.publishComposerFocus(adaptiveProbePage, it.isFocused)
                },
            maxLines = growLines, onTextLayout = { lineCount = it.lineCount },
            enabled = enabled, textStyle = PersonaTheme.type.body.copy(color = PersonaTheme.palette.ink),
            cursorBrush = SolidColor(PersonaTheme.palette.ink),
            decorationBox = { field ->
                Box {
                    if (state.value.text.isEmpty()) Text(if (working && enabled && !homeAssistant) stringResource(R.string.chat_composer_placeholder_working) else composerPlaceholder,
                        style = PersonaTheme.type.body, color = PersonaTheme.palette.ink3, maxLines = 2, overflow = TextOverflow.Ellipsis)
                    field()
                }
            })
        if (offersExpand) ComposerExpandButton(expanded = false, onClick = { expandedEditor = true }, modifier = Modifier.align(Alignment.TopEnd))
    }
    // Same draft and caret as the field; collapsing puts the caret back in the field. Sending closes it.
    if (expandedEditor) ExpandedComposer(state.value, { state.beginEdit(); state.value = it }, composerPlaceholder,
        onCollapse = { expandedEditor = false; state.focusRequest++ }, enabled = enabled) {
        SendButton(onClick = { expandedEditor = false; send() }, enabled = canSend && hasContent && blockedLabel == null, loading = sending)
    }
    if (menu) ChatActionsSheet(onDismiss = { menu = false }, title = stringResource(R.string.chat_composer_menu_title), actions = buildList {
        // Photos picks pictures and videos (iOS mediaPicker), four together at most.
        val roomForMedia = state.attachments.count { it.media } < MAX_COMPOSER_MEDIA
        add(JAction(stringResource(R.string.chat_composer_photos), enabled = service != null && roomForMedia) {
            launchPicker { photos.launch(PickVisualMediaRequest(ActivityResultContracts.PickVisualMedia.ImageAndVideo)) }
        })
        add(JAction(stringResource(R.string.chat_composer_take_photo), enabled = service != null && roomForMedia) { takePhoto() })
        add(JAction(stringResource(R.string.chat_composer_files), enabled = service != null) { launchPicker { files.launch(arrayOf("*/*")) } })
        // iOS's Mention a File or Session, after the attachments (`AttachMoreSection.common`): where the composer has an @ list.
        if (mentions != null) add(JAction(stringResource(R.string.chat_mention)) { menu = false; startMention() })
        // Quick messages go out at once; /clear asks first, and only while nothing is running or being cleared.
        extensionPrompts.forEach { prompt -> add(JAction(prompt.title) { menu = false; state.fillQuickPrompt(prompt) }) }
        SlashCommand.all(resources).forEach { command -> add(JAction(command.title) { menu = false; onCommand(command.text) }) }
        if (canClear && !working) add(JAction(SlashCommand.clearCommand(resources).title, destructive = true) { menu = false; confirmClear = true })
        else if (mainClear != null) add(JAction(SlashCommand.clearCommand(resources).title) { menu = false; clearMain(mainClear) })
    })
    if (confirmClear) JDialog(onDismiss = { confirmClear = false }, title = stringResource(R.string.chat_composer_clear_title),
        text = stringResource(R.string.chat_composer_clear_text, agentName),
        confirm = stringResource(R.string.chat_composer_clear) to { confirmClear = false; onCommand(SlashCommand.CLEAR) }, destructive = true)
    if (cameraDenied) JDialog(onDismiss = { cameraDenied = false }, title = stringResource(R.string.chat_composer_camera_title),
        text = stringResource(R.string.chat_composer_camera_text, appLabel(context)),
        confirm = stringResource(R.string.chat_composer_open_settings) to { cameraDenied = false; openAppSettings(context) })
}
