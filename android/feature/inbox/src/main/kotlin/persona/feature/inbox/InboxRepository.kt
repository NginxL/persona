package persona.feature.inbox

import kotlinx.coroutines.flow.Flow
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.flowOf
import persona.core.inbox.*

private val emptyReplyComposers: StateFlow<ReplyComposers?> = MutableStateFlow(null)
private val emptyConversationInbox: StateFlow<InboxConversations?> = MutableStateFlow(null)
private val emptyLarkMessages: StateFlow<LarkMessagesRepository?> = MutableStateFlow(null)
private val emptyMessageReads: StateFlow<Set<InboxMessageRead>> = MutableStateFlow(emptySet())

/** The Inbox pages' repository: the shared contract ([persona.core.inbox.InboxRepository]) and what only these pages read. */
interface InboxRepository : persona.core.inbox.InboxRepository {
    val conversationInbox: StateFlow<InboxConversations?> get() = emptyConversationInbox
    /** Optional all-chats browser; null in demo mode, lightweight fakes and while a real pairing assembles DI.
     * No request is made merely by creating it. */
    val larkMessages: StateFlow<LarkMessagesRepository?> get() = emptyLarkMessages
    val messageReads: StateFlow<Set<InboxMessageRead>> get() = emptyMessageReads
    val replyComposers: StateFlow<ReplyComposers?> get() = emptyReplyComposers
    suspend fun readingStore(): LarkReadingStore? = null
    /** Where Hand to AI last sent each source's items (#2121, [InboxHandOffMemory]): the group's own, latest first. */
    val handOffMemory: Flow<List<HandOffMemoryEntry>> get() = flowOf(emptyList())
    suspend fun saveHandOffMemory(value: List<HandOffMemoryEntry>) = Unit

    /** A chat's "Pinned messages", a page at a time ([accountId] null: the hub's current account, which the page names). */
    suspend fun larkChatPins(feedId: String, accountId: String?, cursor: String?): LarkChatPinsPage = error("Chat pins unavailable")
    /** Where a pin's message is: the message itself, and the thread's root when it is a reply in one ([topic]: a topic group). */
    suspend fun larkPinTarget(feedId: String, messageId: String, accountId: String, topic: Boolean): LarkPinTarget = error("Chat pins unavailable")
    /** "Add pin" ([pinId] null) or "Remove pin"; throws [LarkPinFailure] when the hub answers otherwise. */
    suspend fun larkChangePin(feedId: String, messageId: String, pinId: String?, accountId: String?): Unit = error("Chat pins unavailable")
    /** Read receipts of my messages; ones the Lark gateway has none for are left out. */
    suspend fun larkReadStates(feedId: String, messageIds: List<String>): List<LarkReadState> = emptyList()

    suspend fun larkMessageDisplay(): LarkMessageDisplay? = null
}

/** [this] as the pages' repository: itself when it already is one, else with the pages' extras left at their defaults. */
fun persona.core.inbox.InboxRepository.forPages(): InboxRepository =
    this as? InboxRepository ?: object : InboxRepository, persona.core.inbox.InboxRepository by this {}
