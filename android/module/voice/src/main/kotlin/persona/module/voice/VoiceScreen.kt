package persona.module.voice

import android.Manifest
import android.app.Activity
import android.content.Context
import android.content.ContextWrapper
import android.content.Intent
import android.content.pm.PackageManager
import android.net.Uri
import android.provider.Settings
import androidx.activity.compose.rememberLauncherForActivityResult
import androidx.activity.result.contract.ActivityResultContracts
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.lazy.itemsIndexed
import androidx.compose.foundation.lazy.rememberLazyListState
import androidx.compose.material3.Text
import androidx.compose.runtime.*
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.platform.LocalView
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.unit.Dp
import androidx.core.app.ActivityCompat
import androidx.core.content.ContextCompat
import androidx.lifecycle.Lifecycle
import androidx.lifecycle.LifecycleEventObserver
import androidx.lifecycle.compose.LocalLifecycleOwner
import persona.core.designsystem.*
import persona.core.designsystem.icons.PhIcons
import persona.core.navigation.Navigator

/**
 * Voice chat (docs/extensions/extensions.md "Voice", docs/android/android.md "Voice chat"): full duplex with the hub's voice extension. It listens
 * all the time this page is open, and only then; talking over an answer stops it. The bar says the extension, the model
 * and the voice; the menu opens the model configuration (when the host has it) and the voices.
 */
@Composable
internal fun VoiceScreen(
    route: VoiceRoute,
    navigator: Navigator,
    extensions: VoiceExtensions,
    identity: String,
    onModelConfiguration: ((String) -> Unit)?,
) {
    val context = LocalContext.current
    val text = rememberVoiceText()
    val title = route.title.ifBlank { text.title }
    var granted by remember { mutableStateOf(microphoneAllowed(context)) }
    var denied by remember { mutableStateOf(false) }
    val ask = rememberLauncherForActivityResult(ActivityResultContracts.RequestPermission()) { allowed ->
        granted = allowed
        denied = !allowed
    }
    LaunchedEffect(Unit) { if (!granted) ask.launch(Manifest.permission.RECORD_AUDIO) }
    // Back from the system settings with the microphone allowed there.
    val lifecycle = LocalLifecycleOwner.current.lifecycle
    DisposableEffect(lifecycle) {
        val observer = LifecycleEventObserver { _, event -> if (event == Lifecycle.Event.ON_RESUME && !granted) granted = microphoneAllowed(context) }
        lifecycle.addObserver(observer)
        onDispose { lifecycle.removeObserver(observer) }
    }
    if (granted) {
        VoiceConversation(route.extensionId, title, navigator, extensions, identity, onModelConfiguration)
        return
    }
    JPage(title, onBack = navigator::back) { padding ->
        Box(Modifier.fillMaxSize().padding(padding), contentAlignment = Alignment.Center) {
            JEmptyState(
                if (denied) text.deniedTitle else text.askTitle,
                if (denied) text.deniedText else text.askText,
                icon = PhIcons.MicrophoneSlash,
                action = {
                    JButton(text.allow, {
                        // Once the system stops asking (denied for good), only its settings can turn the microphone on.
                        val activity = context.activity()
                        if (!denied || activity == null || ActivityCompat.shouldShowRequestPermissionRationale(activity, Manifest.permission.RECORD_AUDIO)) {
                            ask.launch(Manifest.permission.RECORD_AUDIO)
                        } else {
                            context.startActivity(Intent(Settings.ACTION_APPLICATION_DETAILS_SETTINGS, Uri.fromParts("package", context.packageName, null)))
                        }
                    }, icon = PhIcons.Microphone)
                },
            )
        }
    }
}

@Composable
private fun VoiceConversation(
    extensionId: String,
    title: String,
    navigator: Navigator,
    extensions: VoiceExtensions,
    identity: String,
    onModelConfiguration: ((String) -> Unit)?,
) {
    val app = LocalContext.current.applicationContext
    val text = rememberVoiceText()
    val scope = rememberCoroutineScope()
    val page = remember(extensions, extensionId, identity) {
        VoicePage(extensions, extensionId, VoicePrefs.of(app.filesDir, identity), scope, text) {
            VoiceAudioOverride.factory?.invoke() ?: AndroidVoiceAudio(app)
        }
    }
    DisposableEffect(page) {
        page.begin()
        onDispose { page.close() }
    }
    // Leaving the app ends the conversation: the microphone is only ever on while this page is in front.
    val lifecycle = LocalLifecycleOwner.current.lifecycle
    DisposableEffect(lifecycle, page) {
        val observer = LifecycleEventObserver { _, event -> if (event == Lifecycle.Event.ON_STOP) page.pause() }
        lifecycle.addObserver(observer)
        onDispose { lifecycle.removeObserver(observer) }
    }
    val state by page.state.collectAsState()
    val session by page.session.collectAsState()
    val running = session
    val talk = running?.state?.collectAsState()?.value
        ?: VoiceState(phase = if (state.setup is VoiceSetup.Loading) VoiceState.Phase.Connecting else VoiceState.Phase.Ended)
    val level = running?.level?.collectAsState()
    // A conversation, not a page to read: the screen going off would end it.
    val view = LocalView.current
    val awake = talk.phase != VoiceState.Phase.Ended
    DisposableEffect(view, awake) {
        view.keepScreenOn = awake
        onDispose { view.keepScreenOn = false }
    }
    var picking by remember { mutableStateOf(false) }
    val setup = state.setup
    val problem = setup is VoiceSetup.Unavailable || setup is VoiceSetup.Failed
    JPage(
        title, subtitle = state.subtitle, onBack = navigator::back, ambience = ambienceOf(talk, problem),
        actions = {
            VoiceMenu(buildList {
                onModelConfiguration?.let { open -> add(JAction(stringResource(R.string.voice_model_configuration), PhIcons.SlidersHorizontal) { open(voicePurposeId(extensionId)) }) }
                if (setup is VoiceSetup.Ready) add(JAction(text.voices, PhIcons.Waveform) { picking = true })
            })
        },
        bottomBar = if (problem) null else ({
            VoiceControlBar {
                if (talk.phase == VoiceState.Phase.Ended) {
                    VoiceControl(PhIcons.ArrowClockwise, stringResource(R.string.voice_restart), page::begin, kind = VoiceControlKind.Filled)
                } else {
                    VoiceControl(
                        if (talk.muted) PhIcons.MicrophoneSlash else PhIcons.Microphone, if (talk.muted) stringResource(R.string.voice_unmute) else stringResource(R.string.voice_mute),
                        { running?.setMuted(!talk.muted) }, kind = if (talk.muted) VoiceControlKind.Filled else VoiceControlKind.Plain,
                        enabled = running != null,
                    )
                    VoiceControl(PhIcons.HandPalm, stringResource(R.string.voice_interrupt), { running?.interrupt() }, enabled = talk.speaking)
                }
                VoiceControl(PhIcons.PhoneDisconnect, stringResource(R.string.voice_hang_up), navigator::back, kind = VoiceControlKind.Danger)
            }
        }),
    ) { padding ->
        when (setup) {
            is VoiceSetup.Unavailable -> Problem(
                text.modelUnavailable, setup.reason, padding, page::begin,
                onConfigure = onModelConfiguration?.let { open -> { open(voicePurposeId(extensionId)) } },
            )
            is VoiceSetup.Failed -> Problem(text.configUnreadable, setup.message, padding, page::begin)
            else -> Column(Modifier.fillMaxSize().padding(top = padding.calculateTopPadding())) {
                VoiceOrb(orbMode(talk), Modifier.fillMaxWidth().weight(0.44f).padding(vertical = Tokens.Space.l), level = { level?.value ?: 0f })
                VoiceStatus(statusText(talk, text), error = talk.error)
                Captions(talk.lines, Modifier.fillMaxWidth().weight(0.56f), padding.calculateBottomPadding())
            }
        }
    }
    if (picking) VoiceSheet(state, onPick = page::pickVoice, onDismiss = { picking = false })
}

/** The bar's menu: two choices in an anchored menu, a single one as its own button. */
@Composable
private fun VoiceMenu(actions: List<JAction>) {
    var open by remember { mutableStateOf(false) }
    when (actions.size) {
        0 -> Unit
        1 -> actions.single().let { JIconButton(it.icon ?: PhIcons.DotsThree, it.label, it.onClick) }
        else -> Box {
            JIconButton(PhIcons.DotsThree, stringResource(R.string.voice_menu), { open = true })
            JMenu(open, { open = false }, actions)
        }
    }
}

/** What the user said and what Persona says, newest at the bottom and followed as it grows. */
@Composable
private fun Captions(lines: List<Line>, modifier: Modifier, bottom: Dp) {
    val shown = lines.filter { it.text.isNotBlank() }
    val list = rememberLazyListState()
    LaunchedEffect(shown.size, shown.lastOrNull()?.text) {
        // Scrolling to the empty item after the last line keeps the end of a long line in view.
        if (shown.isNotEmpty() && !list.isScrollInProgress) list.animateScrollToItem(shown.size)
    }
    val side = pagePadding
    LazyColumn(
        modifier, state = list,
        contentPadding = PaddingValues(start = side, end = side, top = Tokens.Space.l, bottom = bottom + Tokens.Space.l),
        verticalArrangement = Arrangement.spacedBy(Tokens.Space.xs),
    ) {
        items(shown, key = { it.id }, contentType = { if (it.user) "user" else "persona" }) { VoiceCaption(it.text, it.user, it.final) }
        item("end") { Spacer(Modifier.height(Tokens.Space.xxs)) }
    }
}

/** The model cannot be used, or its configuration could not be read: why, and what can be done. */
@Composable
private fun Problem(title: String, text: String, padding: PaddingValues, onRetry: () -> Unit, onConfigure: (() -> Unit)? = null) {
    Box(Modifier.fillMaxSize().padding(padding), contentAlignment = Alignment.Center) {
        JEmptyState(title, text, icon = PhIcons.WarningCircle, action = {
            Row(horizontalArrangement = Arrangement.spacedBy(Tokens.Space.m)) {
                onConfigure?.let { JButton(stringResource(R.string.voice_model_configuration), it, kind = JButtonKind.Secondary) }
                JButton(stringResource(R.string.voice_retry), onRetry)
            }
        })
    }
}

/** "Voice": the extension's voices for this model as one group, the current one checked; a tap picks it and closes. */
@Composable
private fun VoiceSheet(state: VoicePageState, onPick: (String) -> Unit, onDismiss: () -> Unit) {
    val palette = PersonaTheme.palette
    val text = rememberVoiceText()
    JSheet(onDismiss, title = text.voices) {
        val voices = state.voices
        when {
            voices == null && state.voicesError != null -> JNotice(text.voicesFailed(state.voicesError), icon = PhIcons.WarningCircle)
            voices == null -> JLoading()
            voices.voices.isEmpty() -> Text(text.noVoices, style = PersonaTheme.type.callout, color = palette.ink2)
            else -> LazyColumn {
                itemsIndexed(voices.voices, key = { _, voice -> voice.id }) { index, voice ->
                    val checked = voice.id == state.checkedVoice
                    JGroupItem(GroupPosition.of(index, voices.voices.size)) {
                        JRow(
                            voice.name.ifBlank { voice.id }, subtitle = voiceDetail(voice, voice.id == voices.default, text), chevron = false,
                            trailing = if (checked) ({ JIcon(PhIcons.Check, stringResource(R.string.voice_current_voice), tint = palette.accent) }) else null,
                            onClick = { onDismiss(); onPick(voice.id) },
                        )
                    }
                }
            }
        }
    }
}

internal fun orbMode(state: VoiceState): VoiceOrbMode = when {
    state.phase == VoiceState.Phase.Connecting -> VoiceOrbMode.Connecting
    state.phase == VoiceState.Phase.Ended -> VoiceOrbMode.Ended
    state.speaking -> VoiceOrbMode.Speaking
    state.muted -> VoiceOrbMode.Muted
    else -> VoiceOrbMode.Listening
}

/** The glass light behind the page follows the conversation: blue while it listens, mint while Persona speaks. */
private fun ambienceOf(state: VoiceState, problem: Boolean): Ambience = when {
    problem -> Ambience.NeedsYou
    state.phase == VoiceState.Phase.Ended -> Ambience.Calm
    state.speaking -> Ambience.Connected
    else -> Ambience.Working
}

private fun microphoneAllowed(context: Context) =
    ContextCompat.checkSelfPermission(context, Manifest.permission.RECORD_AUDIO) == PackageManager.PERMISSION_GRANTED

private tailrec fun Context.activity(): Activity? = when (this) {
    is Activity -> this
    is ContextWrapper -> baseContext.activity()
    else -> null
}
