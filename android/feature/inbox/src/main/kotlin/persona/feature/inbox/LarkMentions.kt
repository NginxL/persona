package persona.feature.inbox

import androidx.compose.foundation.layout.ColumnScope
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.res.stringResource
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.delay
import kotlinx.serialization.json.JsonArray
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.JsonPrimitive
import kotlinx.serialization.json.contentOrNull
import persona.core.contracts.api.LarkMember
import persona.core.contracts.api.LarkMention
import persona.core.designsystem.JAvatar
import persona.core.designsystem.JRow
import persona.core.designsystem.Tokens

/** Only a known group or a topic group offers the conversation's member picker. */
fun larkMentionFeed(feedId: String, conversation: LarkConversation): String? =
    feedId.takeIf { conversation.isTopic || conversation.isGroup == true }

/** The name after an @ at the end of the draft, or null for an email-style ASCII word, whitespace or a long query. */
fun larkMentionQuery(text: String): String? {
    val at = text.lastIndexOf('@').takeIf { it >= 0 } ?: return null
    if (at > 0) {
        val before = text[at - 1]
        if (before in 'a'..'z' || before in 'A'..'Z' || before in '0'..'9') return null
    }
    val query = text.substring(at + 1)
    return query.takeIf { it.length <= 20 && it.none(Char::isWhitespace) }
}

/** Replace the @ query at the end with the picked display name and a trailing space. */
fun larkInsertMention(text: String, mention: LarkMention): String {
    val query = larkMentionQuery(text)
    val prefix = if (query == null) text else text.dropLast(query.length + 1)
    return "$prefix@${mention.name} "
}

/**
 * Keep only picked mentions whose literal @name remains. Longer names are matched first so Tommy cannot preserve Tom;
 * duplicate picks and several people with one display name follow the same first-picked rule as iOS.
 */
fun larkMentions(text: String, picked: List<LarkMention>): List<LarkMention> {
    val unique = picked.distinctBy { it.userId }
    var rest = text
    val left = mutableMapOf<String, Int>()
    unique.map { it.name }.toSet().sortedByDescending { it.length }.forEach { name ->
        val token = "@$name"
        left[name] = rest.split(token).size - 1
        rest = rest.replace(token, "\u0000")
    }
    return unique.filter { mention ->
        val count = left[mention.name] ?: 0
        if (count == 0) return@filter false
        left[mention.name] = count - 1
        true
    }
}

private fun JsonObject.memberString(vararg keys: String): String? = keys.firstNotNullOfOrNull { key ->
    (this[key] as? JsonPrimitive)?.takeIf { it.isString }?.contentOrNull?.takeIf { it.isNotBlank() }
}

fun parseLarkMembers(data: JsonObject): List<LarkMember> = (data["members"] as? JsonArray).orEmpty()
    .mapNotNull { it as? JsonObject }.mapNotNull { member ->
        val id = member.memberString("user_id") ?: return@mapNotNull null
        val name = member.memberString("name") ?: return@mapNotNull null
        LarkMember(id, name, member.memberString("avatar_url", "avatar", "avatarUrl"))
    }

private fun offersEveryone(query: String, name: String): Boolean =
    query.isEmpty() || name.startsWith(query) || "all".startsWith(query.lowercase())

/** Everyone first, then the matching members, in the existing document-comment picker's row language. */
@Composable
internal fun LarkMentionPicker(query: String, members: List<LarkMember>, onPick: (LarkMention) -> Unit) {
    val everyoneName = stringResource(R.string.inbox_lark_everyone)
    androidx.compose.foundation.layout.Column(Modifier.testTag("lark.mentionPicker")) {
        if (offersEveryone(query, everyoneName)) {
            val everyone = LarkMention("all", everyoneName)
            JRow(everyoneName, Modifier.testTag("lark.mention.all"), leading = { JAvatar(everyoneName, size = Tokens.Space.xl) },
                onClick = { onPick(everyone) }, chevron = false)
        }
        members.take(5).forEach { member ->
            JRow(member.name, Modifier.testTag("lark.mention.${member.userId}"), leading = { JAvatar(member.name, size = Tokens.Space.xl) },
                onClick = { onPick(LarkMention(member.userId, member.name)) }, chevron = false)
        }
    }
}

/** The same mention behavior in the ordinary group composer and a group's thread reply composer. */
@Composable
internal fun LarkReplyComposerField(
    repository: InboxRepository,
    composer: ReplyComposer,
    state: ReplyComposerState,
    mentionFeed: String?,
    onSend: () -> Unit,
    placeholder: String,
    modifier: Modifier = Modifier,
    accessory: (@Composable ColumnScope.() -> Unit)? = null,
) {
    val query = mentionFeed?.takeIf { state.canEdit }?.let { larkMentionQuery(state.draft.text) }
    var members by remember(mentionFeed) { mutableStateOf<List<LarkMember>>(emptyList()) }
    LaunchedEffect(mentionFeed, query, repository) {
        members = emptyList()
        if (mentionFeed == null || query == null) return@LaunchedEffect
        delay(250)
        members = try {
            repository.larkMembers(mentionFeed, query)
        } catch (error: CancellationException) {
            throw error
        } catch (_: Exception) {
            emptyList()
        }
    }
    ReplyComposerField(state, composer::update, onSend, placeholder, modifier, accessory = {
        if (query != null) {
            LarkMentionPicker(query, members, onPick = { picked ->
                composer.mention(picked)
                members = emptyList()
            })
        }
        accessory?.invoke(this)
    })
}
