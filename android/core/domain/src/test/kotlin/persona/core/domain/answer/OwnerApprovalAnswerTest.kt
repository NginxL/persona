package persona.core.domain.answer

import kotlinx.serialization.encodeToString
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test
import persona.core.contracts.Interaction
import persona.core.contracts.JsonCodec
import persona.core.contracts.Resolution

class OwnerApprovalAnswerTest {
    private val proposal = Interaction(
        "ix_owner_batch_1", machineId = "server", sessionId = "main", kind = "permission",
        approvalBatchId = "oab_1", ownerProposal = true, ownerApprovalPhrases = listOf("发", "同意"),
    )

    @Test fun exactReplyUsesOnlyTheAdvertisedPhraseAndNeverSerializesItsRuntimeMarker() {
        val result = AnswerBuilder.build(proposal, AnswerAction.OwnerReply)
        assertTrue(result.allowed)
        assertEquals(Resolution(allow = true, ownerApprovalPhrase = "发"), result.resolution)
        assertEquals("""{"allow":true}""", JsonCodec.encodeToString(result.resolution!!))
        assertTrue(result.resolution!!.always != true)
    }

    @Test fun exactReplyRequiresACompleteUnlockedPendingConversationProposal() {
        for (invalid in listOf(
            proposal.copy(kind = "question"),
            proposal.copy(ownerProposal = false),
            proposal.copy(approvalBatchId = null),
            proposal.copy(machineId = ""),
            proposal.copy(sessionId = ""),
            proposal.copy(ownerApprovalPhrases = null),
            proposal.copy(ownerApprovalPhrases = emptyList()),
        )) assertFalse(AnswerBuilder.build(invalid, AnswerAction.OwnerReply).allowed)

        val nonConversation = AnswerEntry.entries.filter { it != AnswerEntry.Conversation }.map { AnswerContext(entry = it) }
        for (context in nonConversation + listOf(
            AnswerContext(locked = true), AnswerContext(sending = true), AnswerContext(state = "answered"),
        )) assertFalse(AnswerBuilder.build(proposal, AnswerAction.OwnerReply, context = context).allowed)
    }

    @Test fun exactReplyCannotSmuggleComposerOrSecretValues() {
        for (draft in listOf(
            AnswerDraft(feedback = "owner said yes"), AnswerDraft(message = "send anyway"),
            AnswerDraft(password = "secret"), AnswerDraft(values = mapOf("to" to "other@example.test")),
        )) assertFalse(AnswerBuilder.build(proposal, AnswerAction.OwnerReply, draft).allowed)
    }
}
