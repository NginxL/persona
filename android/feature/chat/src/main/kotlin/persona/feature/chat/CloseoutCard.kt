package persona.feature.chat

import androidx.compose.animation.AnimatedVisibility
import androidx.compose.animation.EnterTransition
import androidx.compose.animation.ExitTransition
import androidx.compose.animation.expandVertically
import androidx.compose.animation.fadeIn
import androidx.compose.animation.fadeOut
import androidx.compose.animation.shrinkVertically
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.ExperimentalLayoutApi
import androidx.compose.foundation.layout.FlowRow
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.text.selection.SelectionContainer
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.platform.LocalUriHandler
import androidx.compose.ui.platform.LocalWindowInfo
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.semantics.Role
import androidx.compose.ui.semantics.heading
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.semantics.stateDescription
import androidx.compose.ui.text.style.TextOverflow
import persona.core.contracts.Closeout
import persona.core.contracts.WorkArtifact
import persona.core.designsystem.JCard
import persona.core.designsystem.JIcon
import persona.core.designsystem.PersonaTheme
import persona.core.designsystem.SharedMotion
import persona.core.designsystem.Tokens
import persona.core.designsystem.JButton
import persona.core.designsystem.JButtonKind
import persona.core.designsystem.JButtonSize
import persona.core.designsystem.icons.PhIcon
import persona.core.designsystem.icons.PhIcons
import persona.core.domain.lines.ChatLine
import persona.core.domain.sort.ClaimState
import persona.core.domain.sort.CloseoutClaim
import persona.core.domain.sort.CloseoutSubject
import persona.core.domain.sort.claim
import persona.core.domain.sort.contradicted
import persona.core.domain.sort.stripCloseout

/** The share of the window's height the card's fields may take before they scroll: the transcript keeps the rest. */
private const val CloseoutFieldsShare = 0.4f

/**
 * The reply that carried the closeout the card shows: the last turn's answer, the newest reply after the newest thing
 * the user sent; null when that turn said nothing. [newestFirst] is the page's lines as the list draws them.
 */
internal fun closeoutReplyId(newestFirst: List<ChatLine>): String? {
    for (line in newestFirst) {
        val row = (line as? ChatLine.Row)?.row ?: continue
        if (row.isUserInput) return null
        if (row.type == "reply") return row.id
    }
    return null
}

/** The whole reply was the closeout block (and it is not cut short): with the card above, nothing is left to draw. */
internal fun onlyCloseout(text: String?, cut: Boolean): Boolean = !cut && !text.isNullOrBlank() && stripCloseout(text).isEmpty()

/** An artifact's reference as a web link, when it is one (http or https only). */
internal fun artifactLink(reference: String): String? =
    reference.trim().takeIf { it.startsWith("https://", ignoreCase = true) || it.startsWith("http://", ignoreCase = true) }

/**
 * A stopped agent's closeout, above its transcript (#3612, docs/assistant/agent-work.md "Closeout"; the web's
 * CloseoutCard): whether it says the work is done, what changed, what it produced, whether it is merged and deployed,
 * what it verified and what it still needs. It is the agent's own claim and the card says so. Beside "merged" and
 * "deployed" stands what the hub found when it looked itself: confirmed, contradicted (in the caution color, with the
 * warning mark: color is never the only signal) or not checked. It is not part of the transcript: the reply that
 * carried the raw block shows without it while this card is there ([closeoutReplyId], [stripCloseout]).
 */
@OptIn(ExperimentalLayoutApi::class)
@Composable
internal fun CloseoutCard(closeout: Closeout, artifacts: List<WorkArtifact>, sessionKey: String, modifier: Modifier = Modifier) {
    val palette = PersonaTheme.palette
    val type = PersonaTheme.type
    var open by rememberSaveable(sessionKey) { mutableStateOf(true) }
    val merged = closeout.claim(CloseoutSubject.Merged)
    val deployed = closeout.claim(CloseoutSubject.Deployed)
    val contradicted = closeout.contradicted
    val did = closeout.did.orEmpty().filter(String::isNotBlank)
    val verified = closeout.verified?.takeIf(String::isNotBlank)
    val missing = closeout.missing?.takeIf(String::isNotBlank)
    val waiting = closeout.waiting?.takeIf(String::isNotBlank)
    val status = when {
        closeout.status == "blocked" && waiting != null -> stringResource(R.string.chat_closeout_blocked_on, waiting)
        closeout.status == "blocked" -> stringResource(R.string.chat_closeout_status_blocked)
        closeout.status == "not_done" -> stringResource(R.string.chat_closeout_status_not_done)
        else -> stringResource(R.string.chat_closeout_status_done)
    }
    val (icon, tint) = when {
        contradicted -> PhIcons.Warning to palette.cautionText
        closeout.status == "done" -> PhIcons.CheckCircle to palette.done.text
        closeout.status == "blocked" -> PhIcons.Clock to palette.ink2
        else -> PhIcons.Info to palette.ink2
    }
    val hasBody = did.isNotEmpty() || artifacts.isNotEmpty() || merged != null || deployed != null || verified != null || missing != null
    val label = stringResource(R.string.chat_closeout_label)
    val expanded = stringResource(persona.core.designsystem.R.string.designsystem_expanded)
    val collapsed = stringResource(persona.core.designsystem.R.string.designsystem_collapsed)
    val fieldsMax = with(LocalDensity.current) { (LocalWindowInfo.current.containerSize.height * CloseoutFieldsShare).toDp() }
    // iOS CloseoutCard.swift:48: withAnimation(Theme.Motion.reveal) { open.toggle() }. The details use the
    // default .opacity transition, so height and fade both follow Theme.Motion.reveal (`Theme.swift:784`),
    // opening and closing; not `.unfolding`. Reduce Motion jumps (nil animation).
    val reduceMotion = PersonaTheme.reduceMotion
    val enter = if (reduceMotion) EnterTransition.None
        else fadeIn(SharedMotion.reveal()) + expandVertically(SharedMotion.reveal(), expandFrom = Alignment.Top)
    val exit = if (reduceMotion) ExitTransition.None
        else fadeOut(SharedMotion.reveal()) + shrinkVertically(SharedMotion.reveal(), shrinkTowards = Alignment.Top)
    // A neutral card in every style: where it stands is said by the head's mark and words, never by the container.
    JCard(modifier.fillMaxWidth().testTag("closeout-card"), padding = Tokens.Space.m) {
        Row(
            Modifier.fillMaxWidth().heightIn(min = Tokens.MinTouch.android)
                .then(if (hasBody) Modifier.clickable(role = Role.Button, onClickLabel = stringResource(
                    if (open) persona.core.designsystem.R.string.designsystem_collapse else persona.core.designsystem.R.string.designsystem_expand)) { open = !open } else Modifier)
                .semantics(mergeDescendants = true) { heading(); if (hasBody) stateDescription = if (open) expanded else collapsed }
                .testTag("closeout-head"),
            verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(Tokens.Space.s),
        ) {
            JIcon(icon, label, size = Tokens.Space.l, tint = tint)
            Column(Modifier.weight(1f)) {
                Text(status, style = type.bodyStrong, color = palette.ink, maxLines = 1, overflow = TextOverflow.Ellipsis)
                Text(stringResource(R.string.chat_closeout_own_claim), style = type.caption, color = palette.ink2, maxLines = 1, overflow = TextOverflow.Ellipsis)
            }
            if (hasBody) JIcon(if (open) PhIcons.CaretDown else PhIcons.CaretRight, null, size = Tokens.Space.m, tint = palette.ink3, decorative = true)
        }
        // iOS only animates the user's toggle (`CloseoutCard.swift:48`); body arriving while already open
        // is not animated. Compose `AnimatedVisibility` skips enter when first composed already visible.
        if (hasBody) AnimatedVisibility(visible = open, enter = enter, exit = exit) {
            SelectionContainer {
                Column(
                    Modifier.fillMaxWidth().heightIn(max = fieldsMax).verticalScroll(rememberScrollState())
                        .padding(top = Tokens.Space.xs).testTag("closeout-fields"),
                    verticalArrangement = Arrangement.spacedBy(Tokens.Space.s),
                ) {
                    if (did.isNotEmpty()) CloseoutField(stringResource(R.string.chat_closeout_did)) {
                        did.forEach { Text(it, style = type.callout, color = palette.ink) }
                    }
                    if (artifacts.isNotEmpty()) CloseoutField(stringResource(R.string.chat_closeout_artifacts)) {
                        FlowRow(
                            horizontalArrangement = Arrangement.spacedBy(Tokens.Space.s),
                            itemVerticalAlignment = Alignment.CenterVertically,
                        ) {
                            artifacts.forEach { CloseoutArtifact(it) }
                        }
                    }
                    merged?.let { CloseoutField(stringResource(R.string.chat_closeout_merged)) { ClaimLine(CloseoutSubject.Merged, it) } }
                    deployed?.let { CloseoutField(stringResource(R.string.chat_closeout_deployed)) { ClaimLine(CloseoutSubject.Deployed, it) } }
                    verified?.let { CloseoutField(stringResource(R.string.chat_closeout_verified)) { Text(it, style = type.callout, color = palette.ink) } }
                    missing?.let { CloseoutField(stringResource(R.string.chat_closeout_missing)) { Text(it, style = type.callout, color = palette.ink) } }
                }
            }
        }
    }
}

@Composable
private fun CloseoutField(label: String, content: @Composable () -> Unit) {
    Column(verticalArrangement = Arrangement.spacedBy(Tokens.Space.xxs)) {
        Text(label, style = PersonaTheme.type.caption, color = PersonaTheme.palette.ink2, maxLines = 1, overflow = TextOverflow.Ellipsis)
        content()
    }
}

private fun artifactIcon(kind: String): PhIcon = when (kind) {
    "pr" -> PhIcons.GitDiff
    "commit" -> PhIcons.Hash
    "branch" -> PhIcons.GitBranch
    "doc" -> PhIcons.FileText
    "build" -> PhIcons.Wrench
    else -> PhIcons.LinkSimple
}

/**
 * One artifact: a link is a small button that opens it (a pull request reads "PR #12", any other link as its address);
 * a name (a commit, a branch) is just text.
 */
@Composable
private fun CloseoutArtifact(artifact: WorkArtifact) {
    val palette = PersonaTheme.palette
    val link = artifactLink(artifact.ref)
    val uri = LocalUriHandler.current
    if (link != null) {
        val text = (if (artifact.kind == "pr") trayPullRequestNumber(link)?.let { stringResource(R.string.chat_tray_pr_number, it) } else null)
            ?: link.substringAfter("://")
        JButton(text, { uri.openUri(link) }, Modifier.testTag("closeout-artifact"), kind = JButtonKind.Secondary, size = JButtonSize.Small,
            icon = artifactIcon(artifact.kind))
        return
    }
    Row(Modifier.testTag("closeout-artifact"), verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(Tokens.Space.xs)) {
        JIcon(artifactIcon(artifact.kind), null, size = Tokens.Space.m, tint = palette.ink2, decorative = true)
        Text(artifact.ref.trim(), style = PersonaTheme.type.code, color = palette.ink, maxLines = 1, overflow = TextOverflow.Ellipsis)
    }
}

/** What the agent said, then what the hub found: three states that differ in words, mark and color. */
@OptIn(ExperimentalLayoutApi::class)
@Composable
private fun ClaimLine(subject: CloseoutSubject, claim: CloseoutClaim) {
    val palette = PersonaTheme.palette
    val type = PersonaTheme.type
    val said = stringResource(when (claim.said) {
        null -> R.string.chat_closeout_not_said
        true -> R.string.chat_closeout_yes
        false -> R.string.chat_closeout_no
    })
    val found = claim.found?.let { yes ->
        stringResource(when (subject) {
            CloseoutSubject.Merged -> if (yes) R.string.chat_closeout_found_merged_yes else R.string.chat_closeout_found_merged_no
            CloseoutSubject.Deployed -> if (yes) R.string.chat_closeout_found_deployed_yes else R.string.chat_closeout_found_deployed_no
        })
    }.orEmpty()
    FlowRow(Modifier.testTag("closeout-claim-${subject.name.lowercase()}-${claim.state.name.lowercase()}"),
        horizontalArrangement = Arrangement.spacedBy(Tokens.Space.s), verticalArrangement = Arrangement.spacedBy(Tokens.Space.xxs)) {
        Text(said, style = type.callout, color = palette.ink)
        when (claim.state) {
            ClaimState.Confirmed -> Finding(PhIcons.Check, stringResource(R.string.chat_closeout_confirmed), palette.done.text)
            // the caution role's text color in every style, with the warning mark and the hub's own words
            ClaimState.Contradicted -> Finding(PhIcons.Warning, found, palette.cautionText, strong = true)
            ClaimState.Found -> Finding(null, found, palette.ink2)
            ClaimState.Unchecked -> Finding(null, stringResource(R.string.chat_closeout_unchecked), palette.ink3)
        }
    }
}

@Composable
private fun Finding(icon: PhIcon?, text: String, color: androidx.compose.ui.graphics.Color, strong: Boolean = false) {
    Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(Tokens.Space.xxs)) {
        icon?.let { JIcon(it, null, size = Tokens.Space.m, tint = color, decorative = true) }
        Text(text, style = if (strong) PersonaTheme.type.bodyStrong else PersonaTheme.type.callout, color = color)
    }
}
