package persona.core.domain

import persona.core.contracts.Interaction
import persona.core.contracts.Item
import persona.core.contracts.MessageEcho
import persona.core.contracts.Session
import persona.core.contracts.SessionKey
import persona.core.contracts.UndeliveredMessage
import persona.core.contracts.api.OutboxMessage
import persona.core.domain.answer.RequestExpiry
import java.time.Instant

/** What an answer did to its card, as the chat page reads the hub's reply (feature/chat InteractionSubmission). */
enum class CardAnswer {
    /** Taken (now, or already), ended elsewhere, or the prompt changed under it: the card leaves this page. */
    Withdrawn,
    /** No word from the hub (the connection lost the reply, a 5xx): it may have gone through, so the card stays locked. */
    Unconfirmed,
    /** The hub said no and did nothing: the card can be answered again (#1881). */
    Refused,
}

/**
 * The chat page's taps on its cards: the card whose answer is in flight ([busyId]: every card waits for it), the cards
 * answered here ([resolved]: off the page), and the cards whose answer got no word from the hub ([uncertain]: locked
 * until the hub's list says). A plain value, so the chat sync scenarios (spec/fixtures/sync) play the page's own rules.
 */
data class ChatTaps(
    val busyId: String? = null,
    val resolved: Set<String> = emptySet(),
    val uncertain: Set<String> = emptySet(),
) {
    val busy: Boolean get() = busyId != null

    /** The cards the page lists: [pending] without the ones answered here. */
    fun shown(pending: List<Interaction>): List<Interaction> = pending.filter { it.id !in resolved }

    /** Whether [interaction]'s answer buttons work at [now]. */
    fun answerable(interaction: Interaction, now: Instant): Boolean =
        busyId == null && interaction.id !in uncertain && interaction.id !in resolved &&
            !RequestExpiry.expired(interaction.expiresAt, now)

    /** Whether a tap on [interaction] sends its answer: it can be answered, and it is still the card [pending] lists. */
    fun canTap(interaction: Interaction, pending: List<Interaction>, now: Instant): Boolean =
        answerable(interaction, now) && pending.any { it == interaction }

    /** A card was tapped (answered or discarded): its request is in flight. */
    fun tapped(id: String): ChatTaps = copy(busyId = id)

    /** The hub replied to the answer for [id]. The request stays in flight until [idle]. */
    fun answered(id: String, answer: CardAnswer): ChatTaps = when (answer) {
        CardAnswer.Withdrawn -> copy(resolved = resolved + id, uncertain = uncertain - id)
        CardAnswer.Unconfirmed -> copy(uncertain = uncertain + id)
        CardAnswer.Refused -> copy(uncertain = uncertain - id)
    }

    /** The card [id] was put away on this device only. */
    fun discarded(id: String): ChatTaps = copy(resolved = resolved + id, uncertain = uncertain - id)

    /** The tapped card's request is over, however it went: the next tap may go. */
    fun idle(): ChatTaps = copy(busyId = null)

    /** The hub's pending cards are now [ids]: marks for cards it no longer lists are dropped. */
    fun pending(ids: Set<String>): ChatTaps = copy(resolved = resolved intersect ids, uncertain = uncertain intersect ids)
}

/**
 * The messages sent from here still drawn as bubbles in [session]'s chat: those whose copy is not among the
 * transcript's [items] (an item with the message's id, or one the hub tagged with it).
 */
fun chatBubbles(outbox: List<OutboxMessage>, session: SessionKey, items: List<Item>): List<OutboxMessage> =
    outbox.filter { message ->
        message.session == session &&
            items.none { item -> item.id == message.clientMessageId || MessageEcho.tag(item) == message.clientMessageId }
    }

/**
 * Kept messages to draw from the session row, without duplicating a transcript line or a local bubble still drawn for
 * the same client message. UUID casing differs between runtimes, so identity comparison ignores case (web and iOS).
 */
fun shownUndelivered(session: Session?, items: List<Item>, drawn: List<OutboxMessage>): List<UndeliveredMessage> {
    val kept = session?.undelivered.orEmpty()
    if (kept.isEmpty()) return emptyList()
    val landed = items.filter(MessageEcho::candidate).flatMap { item ->
        listOfNotNull(MessageEcho.tag(item), item.id)
    }
    val skip = (landed + drawn.map { it.clientMessageId }).mapTo(mutableSetOf()) { it.lowercase() }
    return kept.filter { it.id.lowercase() !in skip }
}
