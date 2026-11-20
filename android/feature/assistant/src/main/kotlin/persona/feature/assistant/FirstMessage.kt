package persona.feature.assistant

import android.content.Context
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.verticalScroll
import androidx.compose.runtime.Composable
import androidx.compose.runtime.remember
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.res.stringResource
import persona.core.designsystem.*
import java.time.Instant
import java.time.ZoneId

/**
 * A new conversation's first message while the conversation starts (iOS `PendingStart`), or one whose start failed
 * (iOS `FailedStart.Message`, [failed] with [reason]). [space] is the space it starts in (Health's), null for Persona's
 * own; [launcher] the launcher picked for it on the page, null for the default; [at] when it was sent (epoch millis), which
 * the day over its bubble says (iOS `FailedStart.Message.at`).
 */
internal data class FirstMessage(val text: String, val failed: Boolean = false, val reason: String? = null,
                                 val space: String? = null, val launcher: String? = null, val at: Long = System.currentTimeMillis())

/**
 * A first message whose start failed, kept on the device for the page it was sent from (one per assistant) until it is
 * sent again or deleted, as iOS keeps its `FailedStart` across launches: the composer gave its words to it.
 */
internal class FailedStarts(context: Context) {
    private val prefs = context.applicationContext.getSharedPreferences("assistant-failed-start", Context.MODE_PRIVATE)

    fun read(page: String): FirstMessage? = prefs.getString("$page.text", null)?.let { text ->
        FirstMessage(text, failed = true, reason = prefs.getString("$page.reason", null),
            space = prefs.getString("$page.space", null), launcher = prefs.getString("$page.launcher", null),
            at = prefs.getLong("$page.at", System.currentTimeMillis()))
    }

    /** Keeps [message] for [page] while it failed; anything else (sending, gone) clears it. */
    fun write(page: String, message: FirstMessage?) {
        val failed = message?.takeIf { it.failed }
        prefs.edit().apply {
            if (failed == null) listOf("text", "reason", "space", "launcher", "at").forEach { remove("$page.$it") }
            else {
                putString("$page.text", failed.text)
                putString("$page.reason", failed.reason)
                putString("$page.space", failed.space)
                putString("$page.launcher", failed.launcher)
                putLong("$page.at", failed.at)
            }
        }.apply()
    }
}

/**
 * The first message at the foot of the blank page, where the conversation will show it (iOS `FirstMessagePending`):
 * your bubble and "Sending…" while the conversation starts; the conversation then takes over and says "Persona is
 * getting ready…". A start that failed keeps the message as a chat keeps one not sent: why, Resend and Delete ([busy]
 * while a resend is on its way). On Glass the day stands over the bubble, as the conversation's own will have it (iOS
 * `DayLine`, #1911: it does not appear later when the transcript takes over).
 */
@Composable
internal fun FirstMessagePending(message: FirstMessage, contentPadding: PaddingValues, busy: Boolean,
                                 onResend: () -> Unit, onDelete: () -> Unit, modifier: Modifier = Modifier) {
    Box(modifier.fillMaxSize(), contentAlignment = Alignment.BottomCenter) {
        Column(Modifier.fillMaxWidth().verticalScroll(rememberScrollState())
            .padding(start = pagePadding, end = pagePadding, top = contentPadding.calculateTopPadding() + Tokens.Space.m,
                bottom = contentPadding.calculateBottomPadding() + Tokens.Space.m)
            .testTag(if (message.failed) "assistant-first-failed" else "assistant-first-sending"),
            verticalArrangement = Arrangement.spacedBy(Tokens.Space.m)) {
            if (PersonaTheme.style == AppStyle.Glass) DayLine(remember(message.at) { Instant.ofEpochMilli(message.at).atZone(ZoneId.systemDefault()).toLocalDate() },
                Modifier.testTag("assistant-first-day"))
            Column(Modifier.fillMaxWidth(), horizontalAlignment = Alignment.End, verticalArrangement = Arrangement.spacedBy(Tokens.Space.xs)) {
                // Sessions picked with @ go in front of the words as tags; a forward keeps its card while it waits.
                if (persona.feature.chat.carriesForward(message.text)) persona.feature.chat.PendingForwardBubble(message.text, "assistant-first")
                else UserBubble(remember(message.text) { persona.feature.chat.messageWords(message.text) })
                if (message.failed) {
                    PendingBadge(PendingStatus.Refused, reason = message.reason)
                    Row(horizontalArrangement = Arrangement.spacedBy(Tokens.Space.s)) {
                        JButton(stringResource(R.string.assistant_first_resend), onResend, kind = JButtonKind.Secondary,
                            size = JButtonSize.Small, enabled = !busy)
                        JButton(stringResource(persona.feature.chat.R.string.chat_delivery_delete), onDelete, kind = JButtonKind.Secondary,
                            size = JButtonSize.Small, enabled = !busy)
                    }
                }
            }
            if (!message.failed) TypingLine(stringResource(R.string.assistant_first_sending))
        }
    }
}
