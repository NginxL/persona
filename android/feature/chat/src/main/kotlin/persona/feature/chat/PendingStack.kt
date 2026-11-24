package persona.feature.chat

import androidx.compose.runtime.Composable
import androidx.compose.runtime.Stable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.saveable.Saver
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.setValue
import androidx.compose.ui.unit.Dp
import androidx.compose.ui.unit.max
import androidx.compose.ui.unit.min
import persona.core.contracts.Interaction
import persona.core.contracts.SessionKey
import persona.core.contracts.parseTime
import persona.core.designsystem.Tokens
import java.time.Instant

/**
 * The cards above a conversation's composer (`DESIGN_SYSTEM.md` section 4.8, iOS `PendingCards`): stacked, the composer
 * still usable, each card folding on its own.
 */
internal object PendingStack {
    /** The share of the page the cards may take before they scroll (iOS `PendingCards.share`). */
    const val SHARE = 0.5f

    /** Permissions first (they hold a tool call back), then everything newest first (iOS `urgentFirst`). */
    fun urgentFirst(cards: List<Interaction>): List<Interaction> = cards.withIndex().sortedWith(
        compareBy<IndexedValue<Interaction>> { if (it.value.kind == "permission") 0 else 1 }
            .thenByDescending { parseTime(it.value.createdAt) ?: Instant.MIN }
            .thenByDescending { it.index },
    ).map { it.value }

    /** As they come while they fit; the most urgent first once they scroll. [first] (opened from a notification) leads both. */
    fun order(cards: List<Interaction>, scrolling: Boolean, first: String? = null): List<Interaction> {
        val ordered = if (scrolling) urgentFirst(cards) else cards
        return if (first == null) ordered else ordered.partition { it.id == first }.let { (opened, rest) -> opened + rest }
    }

    /**
     * How tall one place of cards may be before it scrolls: half the [page], and never so tall that the composer leaves
     * it. The room is the [page] less the [header] over the conversation, the [rest] of the footer (an open tray, the
     * entries, the queue, the composer: everything that is not a card) and the conversation's [floor]. With the
     * [keyboard] up (the page is short, and a card is often what is being answered) the cards take all of that room, so
     * a card is whole or scrolls in all the room there is and the composer stays over the keyboard (iOS
     * `PendingCards.cap`). Never under a finger's height; half the page until the footer is measured.
     *
     * Persona's conversation has two places of cards, its tasks' question above the entries and its own stack below them
     * (iOS draws the question uncapped; Android keeps the composer on the page, so both count toward the room). [other]
     * is the height the other place takes now: null when it has no card, 0 while it is not measured yet. The two places
     * each have at least half of the room, and one that needs less leaves the rest to the other, so together they never
     * take more than the room and the composer stays where it is.
     */
    fun cap(page: Dp, keyboard: Boolean, rest: Dp, header: Dp, floor: Dp, other: Dp? = null): Dp {
        val half = page * SHARE
        if (rest <= Tokens.Space.flush) return if (other == null) half else half / 2f
        val room = page - header - rest - floor
        // the other place's height only counts once it is measured; until then it is taken to hold its half
        val mine = if (other == null) room else max(room / 2f, room - if (other > Tokens.Space.flush) other else room / 2f)
        return max(Tokens.MinTouch.android, if (keyboard) mine else min(half, mine))
    }

    /**
     * Shortest-to-tallest resize range for a lone unfolded pending card when the keyboard is hidden (iOS `FooterRoom.resizeRange`,
     * #12306). Keeps [floor] of conversation visible above the card and leaves [other]'s measured room when a sibling card
     * shares the footer; returns null before the footer is measured or when no room remains above the touch floor.
     */
    fun resizeRange(page: Dp, rest: Dp, header: Dp, floor: Dp, other: Dp? = null): ClosedRange<Dp>? {
        if (rest <= Tokens.Space.flush) return null
        val room = page - header - rest - floor
        val tallest = if (other == null) room else room - if (other > Tokens.Space.flush) other else room / 2f
        val top = max(Tokens.MinTouch.android, tallest)
        if (tallest <= Tokens.MinTouch.android) return null
        val bottom = min(Tokens.Size.box150, top)
        return bottom..top
    }
}

/**
 * The line over a card that is not the conversation's own (iOS `TaskQuestions`): the task it comes from, with the askFirst
 * mark in the needs-you tone, and on the right how many more questions its tasks' Agents ask.
 */
internal data class StackLabel(val title: String, val more: Int = 0)

/**
 * The oldest question of a Persona conversation's tasks (iOS `TaskQuestions`, `DESIGN_SYSTEM.md` section 4.8): a card
 * above its Needs you / In progress entries, answered there, under its task's name with how many more its tasks' Agents ask
 * ([more]); the next comes up once it is answered.
 */
internal data class TaskQuestion(val card: Interaction, val label: StackLabel)

/** Oldest first, one without a time the oldest, then by id: the order the Needs you tray reads a task's cards in (`assistantEntries`). */
private val OldestFirst = compareBy<Interaction> { parseTime(it.createdAt) ?: Instant.MIN }.thenBy { it.id }

/**
 * The card of [requests] that a Persona conversation draws above its entries (#4339 C10): the oldest one asked by an Agent
 * of its tasks ([taskKeys]), named by [titleOf] (the task's title, else what the card knows of where it comes from); null with
 * none. The conversation's own cards are not in it: they are the stack below the entries.
 */
internal fun taskQuestion(requests: List<Interaction>, taskKeys: Set<SessionKey>, titleOf: (Interaction) -> String?): TaskQuestion? {
    val asks = requests.filter { it.sessionKey in taskKeys }.sortedWith(OldestFirst)
    val oldest = asks.firstOrNull() ?: return null
    return TaskQuestion(oldest, StackLabel(titleOf(oldest).orEmpty(), asks.size - 1))
}

/** Which of a conversation's cards are folded to their one line (iOS: each `InteractionPanel`'s own `collapsed`); all open at first. */
@Stable
internal class RequestFolds(initial: Set<String> = emptySet()) {
    var folded by mutableStateOf(initial)
        private set

    fun isFolded(id: String): Boolean = id in folded
    fun set(id: String, fold: Boolean) { folded = if (fold) folded + id else folded - id }
    fun open(id: String) = set(id, false)

    companion object {
        val Saver: Saver<RequestFolds, ArrayList<String>> = Saver(save = { ArrayList(it.folded) }, restore = { RequestFolds(it.toSet()) })
    }
}

@Composable
internal fun rememberRequestFolds(): RequestFolds = rememberSaveable(saver = RequestFolds.Saver) { RequestFolds() }
