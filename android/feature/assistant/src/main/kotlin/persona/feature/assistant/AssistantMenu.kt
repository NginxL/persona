package persona.feature.assistant

import android.content.res.Resources
import androidx.annotation.StringRes
import androidx.compose.runtime.Composable
import androidx.compose.ui.res.stringResource
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.launch
import persona.core.contracts.Machine
import persona.core.contracts.Session
import persona.core.contracts.SessionKey
import persona.core.contracts.api.AssistantConfig
import persona.core.contracts.api.AssistantSnapshot
import persona.core.contracts.api.ChatComposerService
import persona.core.contracts.api.ConnectionState
import persona.core.designsystem.AppStyle
import persona.core.designsystem.JAction
import persona.core.designsystem.JActionSheet
import persona.core.designsystem.PersonaTheme
import persona.core.designsystem.icons.PersonaIcons
import persona.core.designsystem.icons.PhIcon
import persona.core.domain.modelShortName

/**
 * One item of the title menu (iOS `AssistantTopBar.menuItems`) with its words and iOS's icon for it (`Theme.Icon`, by
 * meaning); Android's Voice chat takes iOS's voice-chat icon. Glass draws the icons; Brief and Board keep the words alone.
 */
internal enum class AssistantMenuItem(val icon: PhIcon, @StringRes val label: Int) {
    ConnectionDetails(PersonaIcons.offline, R.string.assistant_connection_details), Knowledge(PersonaIcons.knowledge, R.string.assistant_info_knowledge),
    Details(PersonaIcons.person, R.string.assistant_details), Voice(PersonaIcons.voiceChannel, R.string.assistant_voice_chat),
    BackToMain(PersonaIcons.home, R.string.assistant_back_to_main), CopyDirectory(PersonaIcons.folder, R.string.assistant_copy_directory),
    CopyInfo(PersonaIcons.copy, R.string.assistant_copy_info), CopySessionId(PersonaIcons.id, R.string.assistant_copy_session_id),
    CopyDiagnostics(PersonaIcons.diagnostics, R.string.assistant_copy_diagnostics), Compact(PersonaIcons.compactContext, R.string.assistant_compact),
    // Clear context takes the web's mark for it (#5095): the icon set has no meaning of iOS's eraser.
    ClearContext(PersonaIcons.retry, R.string.assistant_clear_context),
    NewMain(PersonaIcons.newConversation, R.string.assistant_new_main), SessionInfo(PersonaIcons.info, R.string.assistant_info_session),
    Interrupt(PersonaIcons.interrupt, R.string.assistant_interrupt),
}

/**
 * The title menu's items in iOS's order and sections (#4339 C18): while the hub is out of reach Connection details,
 * then Knowledge, Details (the assistant's own page, iOS's profile), Voice chat (Android's, while the hub runs a voice
 * extension) and Back to main chat (off the main conversation); the copies (the directory and the info always, the
 * session ID and diagnostics with a conversation open); Compact context, the main conversation's Clear context (#5100,
 * between them as on the web) and New main chat; Session
 * info and, while it works, Interrupt. The per-conversation launcher and model are #5055's pill and panel, not menu items,
 * and the menu's heading names them ([AssistantCopyText.menuTitle]); Screen is not done: Android has no live screen. Each
 * list is one section; a rule goes above every section after the first.
 */
internal fun assistantMenu(offline: Boolean, voice: Boolean, main: SessionKey?, active: SessionKey?, compact: Boolean,
                           newMain: Boolean, working: Boolean, clear: Boolean = false): List<List<AssistantMenuItem>> = listOf(
    listOfNotNull(AssistantMenuItem.ConnectionDetails.takeIf { offline }, AssistantMenuItem.Knowledge, AssistantMenuItem.Details,
        AssistantMenuItem.Voice.takeIf { voice }, AssistantMenuItem.BackToMain.takeIf { main != null && main != active }),
    listOfNotNull(AssistantMenuItem.CopyDirectory, AssistantMenuItem.CopyInfo,
        AssistantMenuItem.CopySessionId.takeIf { active != null }, AssistantMenuItem.CopyDiagnostics.takeIf { active != null }),
    listOfNotNull(AssistantMenuItem.Compact.takeIf { compact }, AssistantMenuItem.ClearContext.takeIf { clear },
        AssistantMenuItem.NewMain.takeIf { newMain }),
    listOfNotNull(AssistantMenuItem.SessionInfo.takeIf { active != null }, AssistantMenuItem.Interrupt.takeIf { active != null && working }),
).filter { it.isNotEmpty() }

/** Where Persona keeps its files when neither the conversation nor the settings say (iOS `AssistantCopyText.cwd`). */
internal const val AssistantDefaultCwd = "~/.persona/assistant"

/**
 * What Copy directory and Copy info read (iOS `AssistantCopyText`) for [session], the conversation on screen (null on a
 * blank page), with [config] the assistant's settings as last read and [assistantMachine] its machine.
 */
internal class AssistantCopyText(private val session: Session?, private val key: SessionKey?, private val main: Boolean,
                                 private val config: AssistantConfig?, private val assistantMachine: String,
                                 private val machines: List<Machine>) {
    /** Where the conversation runs. */
    val cwd: String get() = session?.cwd?.takeIf(String::isNotBlank) ?: config?.cwd?.takeIf(String::isNotBlank) ?: AssistantDefaultCwd

    /**
     * The conversation's launcher: the one its stored pick names (#5055, `model_pick`; iOS `assistantLaunchers[key]`), else the
     * main conversation follows the settings and another keeps its own.
     */
    val launcher: String get() {
        val picked = session?.modelPick?.launcher?.takeIf(String::isNotBlank)
        val configured = config?.launcher?.takeIf(String::isNotBlank)
        val own = session?.launcher?.takeIf(String::isNotBlank)
        return picked ?: (if (main) configured ?: own else own ?: configured) ?: "claude"
    }

    /**
     * The model its next message runs on as far as the app knows: its stored pick's, else the one it runs now (iOS
     * `pickedModel ?? session?.model`), by its short name ("Opus 4.5", `modelShortName`: the pill and the Agents rows say it
     * so). iOS's `Fmt.model` shows the id with only its provider path cut; the spec keeps raw model IDs out of what people
     * read (`DESIGN_SYSTEM.md` 3.4, 5.6), so this differs on purpose. Copy info keeps the id: that text is for a terminal.
     */
    val model: String? get() = listOf(session?.modelPick?.model, session?.model).firstNotNullOfOrNull { modelShortName(it).takeIf(String::isNotEmpty) }

    /**
     * The menu's heading (iOS `AssistantTopBar.menuTitle`): which conversation and what it runs on, "Main chat · claude · Opus 4.5"
     * for the main conversation, "<title> · claude" for another (the persona's name for one with no title of its own), and
     * "New chat · claude" on a blank page. With several hub groups paired the selected one's name ends it ([group]).
     */
    fun menuTitle(res: Resources, persona: String, group: String? = null): String {
        val title = if (key == null) res.getString(R.string.assistant_menu_title_new, launcher) else {
            val name = if (main) res.getString(R.string.assistant_main_chat)
                else session?.title?.takeUnless { it.isBlank() || it == "Claude Code" } ?: persona
            listOfNotNull(name, launcher, model).joinToString(" · ")
        }
        return if (group == null) title else "$title · $group"
    }

    fun info(res: Resources): String {
        val machineId = key?.machineId ?: config?.machineId?.takeIf(String::isNotBlank) ?: assistantMachine
        return assistantCopyInfo(res, session?.title?.takeUnless { it.isBlank() || it == "Claude Code" }, main, key?.sessionId,
            machines.firstOrNull { it.id == machineId }?.name, machineId, cwd, launcher, modelShown(session?.model))
    }
}

/** A model as the menu shows it (iOS `Fmt.model`): the last part of a provider path. */
internal fun modelShown(model: String?): String? = model?.takeIf(String::isNotBlank)?.substringAfterLast('/')

/**
 * Copy info's text (iOS `AssistantCopyText.info`): enough for another agent, or the user at a terminal, to find the
 * conversation. Which one it is, its session ID, the machine (its name and id when they differ), the working directory,
 * the launcher and model, and how to go on with it there.
 */
internal fun assistantCopyInfo(res: Resources, title: String?, main: Boolean, sessionId: String?, machine: String?,
                               machineId: String, cwd: String, launcher: String, model: String?): String {
    val name = listOfNotNull(res.getString(R.string.assistant_main_chat).takeIf { main },
        title?.let { res.getString(R.string.assistant_copy_info_quoted, it) }).joinToString(" ")
    val place = machine?.let { if (it == machineId) it else res.getString(R.string.assistant_copy_info_machine_id, it, machineId) } ?: machineId
    return listOfNotNull(
        when {
            sessionId == null -> res.getString(R.string.assistant_copy_info_hub)
            name.isEmpty() -> res.getString(R.string.assistant_copy_info_chat)
            else -> res.getString(R.string.assistant_copy_info_chat_named, name)
        },
        sessionId?.let { res.getString(R.string.assistant_copy_info_session, it) },
        res.getString(R.string.assistant_copy_info_machine, place),
        res.getString(R.string.assistant_copy_info_cwd, cwd),
        model?.let { res.getString(R.string.assistant_copy_info_launcher_model, launcher, it) }
            ?: res.getString(R.string.assistant_copy_info_launcher, launcher),
        sessionId?.let { res.getString(R.string.assistant_copy_info_resume, cwd, it) },
    ).joinToString("\n")
}

/** The existing title-menu actions at a real composable boundary, with the page retaining every state holder. */
@Composable
internal fun AssistantTitleMenu(
    assistantName: String,
    hubGroup: String?,
    active: SessionKey?,
    onMain: Boolean,
    snapshot: AssistantSnapshot,
    assistantMachine: String,
    machines: List<Machine>,
    menuConfig: AssistantConfig?,
    composerService: ChatComposerService?,
    demo: Boolean,
    connection: ConnectionState?,
    mainActions: MainActions,
    onVoice: (() -> Unit)?,
    mainConversation: MainConversationController,
    copy: (String, String?) -> Boolean,
    scope: CoroutineScope,
    onDismiss: () -> Unit,
    onKnowledge: () -> Unit,
    onDetails: () -> Unit,
    onInfo: (AssistantInfo) -> Unit,
    onOpenConversation: (SessionKey) -> Unit,
    onCopyId: (SessionKey) -> Unit,
    onCopyDiagnostics: (Session) -> Unit,
    onCompact: () -> Unit,
    onClear: () -> Unit,
    onNotice: (String) -> Unit,
    onConnectionDetails: () -> Unit,
) {
    val resources = androidx.compose.ui.platform.LocalResources.current
    val shown = active?.let { key -> snapshot.conversations.firstOrNull { it.key == key } }
    val compacting = mainConversation.compacting
    val compaction = mainConversation.compaction
    val clearBusy = mainConversation.clearBusy
    val newMainBusy = mainConversation.newMainBusy
    val copyText = AssistantCopyText(shown, active, onMain, menuConfig, assistantMachine, machines)
    val interrupt = composerService.takeIf { !demo }
    val sections = assistantMenu(offline = connection != null && connection != ConnectionState.Online,
        voice = onVoice != null, main = snapshot.mainSession, active = active, compact = active != null && mainActions.compact,
        newMain = onMain && mainActions.newMain, working = interrupt != null && persona.core.domain.lines.isLiveStatus(shown?.status),
        clear = onMain && mainActions.clear)
        fun rawAction(item: AssistantMenuItem, separator: Boolean): JAction = when (item) {
            AssistantMenuItem.ConnectionDetails -> JAction(resources.getString(item.label), separatorBefore = separator,
                onClick = onConnectionDetails)
            AssistantMenuItem.Knowledge -> JAction(resources.getString(item.label), separatorBefore = separator, onClick = {
                onKnowledge()
            })
            // iOS's Details opens Persona's profile: here the assistant's own page, its settings.
            AssistantMenuItem.Details -> JAction(resources.getString(item.label), separatorBefore = separator, onClick = {
                onDetails()
            })
            AssistantMenuItem.Voice -> JAction(resources.getString(item.label), separatorBefore = separator,
                onClick = { onVoice?.invoke() })
            // In place of the home page's Home (#4339 C11), as iOS's menu has it: off the main conversation only.
            AssistantMenuItem.BackToMain -> JAction(resources.getString(item.label), PersonaIcons.home,
                separatorBefore = separator, onClick = { snapshot.mainSession?.let(onOpenConversation) })
            AssistantMenuItem.CopyDirectory -> JAction(resources.getString(item.label), separatorBefore = separator,
                onClick = { copy(copyText.cwd, resources.getString(R.string.assistant_directory_copied)) })
            AssistantMenuItem.CopyInfo -> JAction(resources.getString(item.label), separatorBefore = separator,
                onClick = { copy(copyText.info(resources), resources.getString(R.string.assistant_info_copied)) })
            AssistantMenuItem.CopySessionId -> JAction(resources.getString(item.label), separatorBefore = separator,
                onClick = { active?.let(onCopyId) })
            AssistantMenuItem.CopyDiagnostics -> JAction(resources.getString(item.label), separatorBefore = separator,
                onClick = { active?.let { key -> onCopyDiagnostics(shown ?: Session(machineId = key.machineId, sessionId = key.sessionId)) } })
            AssistantMenuItem.Compact -> JAction(resources.getString(item.label), separatorBefore = separator,
                enabled = !compacting && compaction.line != CompactionLine.Running, onClick = onCompact)
            AssistantMenuItem.ClearContext -> JAction(resources.getString(item.label), separatorBefore = separator,
                enabled = !clearBusy, onClick = onClear)
            AssistantMenuItem.NewMain -> JAction(resources.getString(item.label), separatorBefore = separator,
                enabled = !newMainBusy, onClick = { mainConversation.newMainOpen = true })
            AssistantMenuItem.SessionInfo -> JAction(resources.getString(item.label), separatorBefore = separator,
                onClick = { onInfo(AssistantInfo.Session) })
            AssistantMenuItem.Interrupt -> JAction(resources.getString(item.label), separatorBefore = separator, onClick = {
                val key = active
                if (interrupt != null && key != null) scope.launch {
                    try { interrupt.interrupt(key); onNotice(resources.getString(persona.feature.chat.R.string.chat_composer_interrupt_requested)) }
                    catch (cancelled: CancellationException) { throw cancelled }
                    catch (_: Exception) { onNotice(resources.getString(persona.feature.chat.R.string.chat_composer_interrupt_unknown)) }
                }
            })
        }
        // iOS's icon on every item: Glass draws them; Brief and Board keep their words (Back to main chat has had its house all along).
        val glassIcons = PersonaTheme.style == AppStyle.Glass
        fun action(item: AssistantMenuItem, separator: Boolean): JAction = rawAction(item, separator)
            .copy(icon = item.icon.takeIf { glassIcons || item == AssistantMenuItem.BackToMain })
        // iOS's heading: which conversation and what it runs on ("Main chat · claude · Opus 4.5").
        JActionSheet(title = copyText.menuTitle(resources, assistantName, hubGroup), onDismiss = onDismiss,
            actions = sections.flatMapIndexed { index, items -> items.mapIndexed { i, item -> action(item, separator = index > 0 && i == 0) } })
}
