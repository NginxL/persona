package persona.core.designsystem

import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.ExperimentalLayoutApi
import androidx.compose.foundation.layout.FlowRow
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.width
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalResources
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.semantics.LiveRegionMode
import androidx.compose.ui.semantics.liveRegion
import androidx.compose.ui.semantics.semantics
import java.time.Instant
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.delay
import kotlinx.coroutines.launch
import persona.core.contracts.ShellQuestion
import persona.core.designsystem.icons.PersonaIcons
import persona.core.domain.tasks.StartFailureCode
import persona.core.domain.tasks.startFailureCode

/** The short row line for a coded start failure; null keeps the caller's existing raw-text classifier. */
@Composable
fun startFailureCodeLine(code: String?, args: Map<String, String>?): String? = when (val failure = startFailureCode(code, args)) {
    is StartFailureCode.TerminalLauncher -> stringResource(R.string.designsystem_start_code_launcher_terminal, failure.launcher)
    StartFailureCode.WorktreePathTaken -> stringResource(R.string.designsystem_start_code_worktree_path_taken)
    null -> null
}

/** A password is never entered here; a new SSH host key is never trusted here. Other answers keep the hub's order. */
fun answerableShellChoices(question: ShellQuestion): List<String> = when {
    question.secret -> question.choices.filter { it == "cancel" }
    question.kind == "ssh_host_key" -> question.choices.filter { it in setOf("n", "no", "cancel") }
    else -> question.choices
}

/** The answer button's wording, including the key the computer expects where it has no plain yes/no equivalent. */
@Composable
fun shellChoiceLabel(question: ShellQuestion, id: String): String = when (id) {
    "enter" -> when (question.default) {
        "y" -> stringResource(R.string.designsystem_shell_choice_enter_yes)
        "n" -> stringResource(R.string.designsystem_shell_choice_enter_no)
        else -> stringResource(R.string.designsystem_shell_choice_enter)
    }
    "cancel" -> stringResource(R.string.designsystem_shell_choice_cancel)
    "yes" -> stringResource(R.string.designsystem_shell_choice_yes)
    "no" -> stringResource(R.string.designsystem_shell_choice_no)
    else -> when (question.kind to id) {
        "update" to "y" -> stringResource(R.string.designsystem_shell_choice_update_yes, id)
        "update" to "n" -> stringResource(R.string.designsystem_shell_choice_update_no, id)
        "compinit" to "y" -> stringResource(R.string.designsystem_shell_choice_compinit_yes, id)
        "compinit" to "n" -> stringResource(R.string.designsystem_shell_choice_compinit_no, id)
        "zsh_newuser" to "q" -> stringResource(R.string.designsystem_shell_choice_newuser_quit, id)
        "zsh_newuser" to "0" -> stringResource(R.string.designsystem_shell_choice_newuser_empty, id)
        "p10k_wizard" to "q" -> stringResource(R.string.designsystem_shell_choice_p10k_quit, id)
        "rvm_trust" to "n" -> stringResource(R.string.designsystem_shell_choice_rvm_no, id)
        "rvm_trust" to "c" -> stringResource(R.string.designsystem_shell_choice_rvm_cancel, id)
        else -> when (id) {
            "y" -> stringResource(R.string.designsystem_shell_choice_key_yes, id)
            "n" -> stringResource(R.string.designsystem_shell_choice_key_no, id)
            else -> stringResource(R.string.designsystem_shell_choice_key, id)
        }
    }
}

/**
 * A start's terminal question, for Agents and the failed tasks they run. The caller sends only a choice the hub listed;
 * true means the hub confirmed it. One answer is busy at a time, a refusal stays inline, and the terminal's last lines
 * stay folded until asked for. [fixedNow] is for deterministic component tests; production reevaluates expiry every 30 s.
 */
@OptIn(ExperimentalLayoutApi::class)
@Composable
fun ShellQuestionCard(
    question: ShellQuestion,
    answer: suspend (String) -> Boolean,
    modifier: Modifier = Modifier,
    enabled: Boolean = true,
    fixedNow: Instant? = null,
    onAnswered: (String) -> Unit = {},
) {
    val resources = LocalResources.current
    val scope = rememberCoroutineScope()
    var now by remember(question.keptUntil) { mutableStateOf(Instant.now()) }
    LaunchedEffect(question.keptUntil) {
        while (true) {
            delay(30_000)
            now = Instant.now()
        }
    }
    var busy by remember(question.line, question.keptUntil) { mutableStateOf<String?>(null) }
    var failure by remember(question.line, question.keptUntil) { mutableStateOf<String?>(null) }
    var details by remember(question.line, question.keptUntil) { mutableStateOf(false) }
    if (!question.isOpenAt(fixedNow ?: now)) return
    val hint = when {
        question.secret -> stringResource(R.string.designsystem_shell_question_hint_secret)
        question.kind == "ssh_host_key" -> stringResource(R.string.designsystem_shell_question_hint_ssh)
        else -> stringResource(R.string.designsystem_shell_question_hint)
    }
    val until = question.keptUntil?.let { raw -> runCatching { Instant.parse(raw) }.getOrNull() }
    val choices = answerableShellChoices(question)
    JCard(modifier.testTag("shell-question"), padding = Tokens.Space.m) {
        Column(verticalArrangement = Arrangement.spacedBy(Tokens.Space.s)) {
            Row(verticalAlignment = Alignment.Top) {
                JIcon(PersonaIcons.terminal, decorative = true, size = Tokens.Size.iconS, tint = PersonaTheme.palette.ink2)
                Spacer(Modifier.width(Tokens.Space.sm))
                Text(
                    stringResource(R.string.designsystem_shell_question_asking, question.line),
                    modifier = Modifier.weight(1f),
                    style = PersonaTheme.type.callout,
                    color = PersonaTheme.palette.ink,
                )
            }
            Text(
                listOfNotNull(
                    hint,
                    until?.let { stringResource(R.string.designsystem_shell_question_kept_until,
                        LocalizedFormats.format(it, LocalizedFormats.TIME, currentLocale)) },
                ).joinToString(" "),
                style = PersonaTheme.type.caption,
                color = PersonaTheme.palette.ink2,
            )
            failure?.let {
                Text(it, Modifier.semantics { liveRegion = LiveRegionMode.Polite },
                    style = PersonaTheme.type.caption, color = PersonaTheme.palette.danger.text)
            }
            if (choices.isNotEmpty()) FlowRow(
                Modifier.fillMaxWidth(),
                horizontalArrangement = Arrangement.spacedBy(Tokens.Space.s),
                verticalArrangement = Arrangement.spacedBy(Tokens.Space.s),
            ) {
                choices.forEach { choice ->
                    JButton(
                        shellChoiceLabel(question, choice),
                        onClick = {
                            if (busy != null || !enabled) return@JButton
                            busy = choice
                            failure = null
                            scope.launch {
                                try {
                                    if (answer(choice)) onAnswered(choice)
                                } catch (cancelled: CancellationException) {
                                    throw cancelled
                                } catch (error: Exception) {
                                    failure = operationFeedback(error).text.resolve(resources)
                                } finally {
                                    busy = null
                                }
                            }
                        },
                        modifier = Modifier.testTag("shell-choice-$choice"),
                        kind = JButtonKind.Secondary,
                        size = JButtonSize.Small,
                        enabled = enabled && busy == null,
                        loading = busy == choice,
                    )
                }
            }
            if (!question.screen.isNullOrEmpty()) {
                JTextButton(
                    stringResource(if (details) R.string.designsystem_details_collapse else R.string.designsystem_details_expand),
                    { details = !details },
                    modifier = Modifier.testTag("shell-question-details"),
                )
                if (details) JCodeBlock(question.screen.orEmpty(), small = true)
            }
        }
    }
}
