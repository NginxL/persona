package persona.feature.agents

import android.content.res.Resources
import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.ExperimentalLayoutApi
import androidx.compose.foundation.layout.FlowRow
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.alpha
import androidx.compose.ui.draw.clip
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.semantics.Role
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextOverflow
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.JsonPrimitive
import persona.core.contracts.*
import persona.core.designsystem.*
import persona.core.designsystem.icons.PersonaIcons
import persona.core.designsystem.icons.PhIcon
import persona.core.domain.answer.*
import persona.core.domain.sort.NeedsYouRow
import java.time.Instant

/** One row inside the Agents page's compact grouped Glass "Needs you" card (matching iOS `InteractionPreviewCard`). */
internal sealed interface GlassNeedsYouItem {
    val itemKey: String

    data class Waiting(val row: NeedsYouRow, val owner: Session?, val machine: String?) : GlassNeedsYouItem {
        override val itemKey: String = "needs-you:${row.id}"
    }

    data class Card(
        val request: Interaction,
        val owner: Session?,
        val machine: String?,
        /** Shown on a session's first pending card only, matching iOS `InteractionPreviewCard.showsPlace`. */
        val showsPlace: Boolean,
    ) : GlassNeedsYouItem {
        override val itemKey: String = "request:${request.id}"
    }
}

internal fun flattenGlassNeedsYou(
    rows: List<NeedsYouRow>,
    ownerOf: (NeedsYouRow) -> Session?,
    machineOf: (String) -> String?,
    includeOwnerlessWaiting: Boolean,
): List<GlassNeedsYouItem> = buildList {
    for (row in rows) {
        val owner = ownerOf(row)
        if (row.cards.isEmpty()) {
            if (owner != null || includeOwnerlessWaiting) {
                add(GlassNeedsYouItem.Waiting(row, owner, machineOf(row.key.machineId)))
            }
        } else {
            row.cards.forEachIndexed { index, request ->
                add(GlassNeedsYouItem.Card(request, owner, machineOf(request.machineId), showsPlace = index == 0))
            }
        }
    }
}

internal fun glassRequestIcon(request: Interaction): PhIcon = when (request.kind) {
    "permission" -> PersonaIcons.waitsForYou
    "screen" -> PersonaIcons.terminal
    else -> request.decisionKind.icon
}

/** Concise 1-3 line ask summary matching iOS `InteractionPreviewCard.summary`. */
internal fun glassRequestAskLine(request: Interaction, res: Resources, permissionText: String = ""): String? = when (request.kind) {
    "permission" -> {
        if (request.isSecretRun || request.isSecretHandoff) {
            markdownPreview(request.cardSummary(res)).lineSequence().firstOrNull()
        } else {
            val input = request.toolInput as? JsonObject
            val command = (input?.get("command") as? JsonPrimitive)?.takeIf { it.isString }?.content
            val tool = request.toolName?.takeIf { it.isNotBlank() }
            if (tool == "Bash" && !command.isNullOrBlank()) {
                "$tool: $command"
            } else {
                permissionText.takeIf { it.isNotBlank() }
            }
        }
    }
    "question" -> request.quickQuestion()?.question?.takeIf { it.isNotBlank() }
        ?: markdownPreview(request.cardSummary(res)).lineSequence().firstOrNull()
    else -> markdownPreview(request.cardSummary(res)).lineSequence().firstOrNull()
}

internal fun glassPlaceLine(sessionTitle: String?, machine: String?): String =
    listOfNotNull(
        sessionTitle?.trim()?.takeIf { it.isNotEmpty() },
        machine?.trim()?.takeIf { it.isNotEmpty() },
    ).joinToString(" · ")

@OptIn(ExperimentalLayoutApi::class)
@Composable
internal fun GlassNeedsYouCardRow(
    request: Interaction,
    machine: String?,
    now: Instant,
    online: Boolean,
    delivery: RequestDelivery?,
    showsPlace: Boolean,
    onOpen: () -> Unit,
    onAnswer: AnswerRequest,
    approvable: Boolean = true,
    readOnly: Boolean = false,
) {
    val palette = PersonaTheme.palette
    val type = PersonaTheme.type
    val res = agentResources()
    val expired = request.expired(now)
    val enabled = online && !readOnly && approvable && !expired && !RequestDeliveries.locks(delivery)
    val question = request.quickQuestion()
    val place = if (showsPlace) glassPlaceLine(request.sessionTitle, machine) else ""
    val permissionText = if (request.kind == "permission") request.permissionSummary().text.text() else ""
    val askLine = glassRequestAskLine(request, res, permissionText)
    val answer: (AnswerAction) -> Unit = { onAnswer(request, it, AnswerDraft(), false) }
    val blockedNote = when {
        readOnly -> null
        !approvable -> stringResource(R.string.agents_approve_blocked)
        !online && !expired -> stringResource(R.string.agents_request_offline)
        else -> null
    }

    Column(
        Modifier
            .fillMaxWidth()
            .clickable(role = Role.Button, onClick = onOpen)
            .padding(horizontal = Tokens.Space.l, vertical = Tokens.Space.m),
        verticalArrangement = Arrangement.spacedBy(Tokens.Space.s),
    ) {
        Row(
            verticalAlignment = Alignment.CenterVertically,
            horizontalArrangement = Arrangement.spacedBy(Tokens.Space.s),
        ) {
            Box(Modifier.width(Tokens.Size.icon), contentAlignment = Alignment.Center) {
                JIcon(
                    glassRequestIcon(request),
                    null,
                    size = Tokens.Size.iconXS,
                    tint = palette.needsYou.dot,
                    selected = true,
                    decorative = true,
                )
            }
            Column(
                Modifier.weight(1f),
                verticalArrangement = Arrangement.spacedBy(Tokens.Space.xxs),
            ) {
                Text(
                    request.cardTitle(res),
                    style = type.headline,
                    color = palette.ink,
                    maxLines = 2,
                    overflow = TextOverflow.Ellipsis,
                )
                if (place.isNotEmpty()) {
                    Text(
                        place,
                        style = type.caption,
                        color = palette.ink2,
                        maxLines = 1,
                        overflow = TextOverflow.Ellipsis,
                    )
                }
            }
            JIcon(
                PersonaIcons.disclosure,
                null,
                size = Tokens.Size.markS,
                tint = palette.ink3,
                decorative = true,
            )
        }

        if (!askLine.isNullOrBlank()) {
            Text(
                askLine,
                style = type.callout,
                color = palette.ink,
                maxLines = 3,
                overflow = TextOverflow.Ellipsis,
            )
        }

        blockedNote?.let {
            Text(it, style = type.caption, color = palette.cautionText)
        }
        delivery?.let { RequestDeliveryStatus(it) }

        if (!readOnly) {
            when (request.kind) {
                "permission" -> {
                    FlowRow(
                        horizontalArrangement = Arrangement.spacedBy(Tokens.Space.s),
                        verticalArrangement = Arrangement.spacedBy(Tokens.Space.xs),
                    ) {
                        if (request.isSecretRun || request.isSecretHandoff || request.needsFieldValues) {
                            GlassNeedsYouPill(
                                label = stringResource(R.string.agents_request_view),
                                prominent = true,
                                enabled = true,
                                onClick = onOpen,
                            )
                        } else {
                            GlassNeedsYouPill(
                                label = stringResource(R.string.agents_request_allow),
                                prominent = true,
                                enabled = enabled,
                                onClick = { answer(AnswerAction.Allow) },
                            )
                            GlassNeedsYouPill(
                                label = stringResource(R.string.agents_request_deny),
                                prominent = false,
                                enabled = enabled,
                                onClick = { answer(AnswerAction.Deny) },
                            )
                        }
                    }
                }
                "question" -> {
                    if (question != null && question.options.size in 1..4) {
                        FlowRow(
                            horizontalArrangement = Arrangement.spacedBy(Tokens.Space.s),
                            verticalArrangement = Arrangement.spacedBy(Tokens.Space.xs),
                        ) {
                            question.options.forEachIndexed { index, option ->
                                val label = AnswerBuilder.display(option.label)
                                GlassNeedsYouPill(
                                    label = label.text,
                                    prominent = false,
                                    enabled = enabled,
                                    onClick = { onAnswer(request, AnswerAction.PickOption, AnswerDraft(optionIndex = index), false) },
                                )
                            }
                        }
                    }
                }
            }
        }
    }
}

@Composable
internal fun GlassNeedsYouWaitingRow(
    owner: Session?,
    machine: String?,
    sidebar: Boolean,
    onClick: () -> Unit,
) {
    val palette = PersonaTheme.palette
    val type = PersonaTheme.type
    val sessionName = owner?.let {
        if (sidebar) it.displayTitle else it.title.takeIf(String::isNotBlank) ?: it.sessionId
    }
    val place = glassPlaceLine(sessionName, machine)
    Row(
        Modifier
            .fillMaxWidth()
            .clickable(role = Role.Button, onClick = onClick)
            .padding(horizontal = Tokens.Space.l, vertical = Tokens.Space.m),
        verticalAlignment = Alignment.CenterVertically,
        horizontalArrangement = Arrangement.spacedBy(Tokens.Space.s),
    ) {
        Box(Modifier.width(Tokens.Size.icon), contentAlignment = Alignment.Center) {
            JIcon(
                PersonaIcons.terminal,
                null,
                size = Tokens.Size.iconXS,
                tint = palette.needsYou.dot,
                selected = true,
                decorative = true,
            )
        }
        Column(
            Modifier.weight(1f),
            verticalArrangement = Arrangement.spacedBy(Tokens.Space.xxs),
        ) {
            Text(
                stringResource(persona.core.designsystem.R.string.designsystem_decision_screen),
                style = type.headline,
                color = palette.ink,
                maxLines = 1,
                overflow = TextOverflow.Ellipsis,
            )
            if (place.isNotEmpty()) {
                Text(
                    place,
                    style = type.caption,
                    color = palette.ink2,
                    maxLines = 1,
                    overflow = TextOverflow.Ellipsis,
                )
            }
        }
        JIcon(
            PersonaIcons.disclosure,
            null,
            size = Tokens.Size.markS,
            tint = palette.ink3,
            decorative = true,
        )
    }
}

@Composable
private fun GlassNeedsYouPill(
    label: String,
    prominent: Boolean,
    enabled: Boolean,
    onClick: () -> Unit,
) {
    val palette = PersonaTheme.palette
    val fill = if (prominent) palette.needsYou.dot else palette.sunken
    val ink = if (prominent) palette.needsYou.onDot else palette.needsYou.text
    Box(
        Modifier
            .clip(CircleShape)
            .background(fill)
            .alpha(if (enabled) 1f else 0.45f)
            .clickable(enabled = enabled, role = Role.Button, onClick = onClick)
            .padding(horizontal = Tokens.Space.l, vertical = Tokens.Space.sm),
        contentAlignment = Alignment.Center,
    ) {
        Text(
            label,
            style = PersonaTheme.type.caption.copy(fontWeight = FontWeight.Medium),
            color = ink,
            maxLines = 1,
            overflow = TextOverflow.Ellipsis,
        )
    }
}
