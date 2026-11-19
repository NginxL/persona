package persona.feature.chat

import android.content.Context
import androidx.test.core.app.ApplicationProvider
import androidx.test.ext.junit.runners.AndroidJUnit4
import org.junit.Assert.assertEquals
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import persona.core.contracts.DialogOption
import persona.core.contracts.Interaction
import persona.core.contracts.Question
import persona.core.contracts.Resolution
import persona.core.contracts.ScreenDialog

/** What the chat says when the hub reports a card over (#1688): the answer that took effect, or how it ended. */
@RunWith(AndroidJUnit4::class)
@Config(sdk = [35])
class AnsweredTextTest {
    private val res get() = ApplicationProvider.getApplicationContext<Context>().resources

    private val plan = Interaction("p", kind = "plan")
    private val permission = Interaction("w", kind = "permission")
    private val questions = Interaction("q", kind = "question", questions = listOf(Question("Which?"), Question("When?")))
    private val dialog = Interaction("s", kind = "screen", dialog = ScreenDialog(options = listOf(DialogOption("1", "Yes"), DialogOption("2", "No"))))

    @Config(qualifiers = "en")
    @Test fun eachKindReadsAsItWasAnswered() {
        assertEquals("Approved, Auto accept edits", AnsweredText.summary(res, plan, Resolution(approve = true, mode = "acceptEdits")))
        assertEquals("Sent back: shorter", AnsweredText.summary(res, plan, Resolution(approve = false, feedback = "shorter")))
        assertEquals("Always allowed", AnsweredText.summary(res, permission, Resolution(allow = true, always = true)))
        assertEquals("Denied", AnsweredText.summary(res, permission, Resolution(allow = false)))
        assertEquals("Answered by the assistant", AnsweredText.summary(res, permission, Resolution(allow = true, answeredBy = "assistant")))
        assertEquals("Handed back to the computer", AnsweredText.summary(res, questions, Resolution(dismiss = true)))
        assertEquals("Which?: A, When?: (No answer)", AnsweredText.summary(res, questions, Resolution(answers = mapOf("Which?" to "A"))))
        assertEquals("No", AnsweredText.summary(res, dialog, Resolution(option = "2")))
        assertEquals("Yes, No, (Entered)", AnsweredText.summary(res, dialog, Resolution(options = listOf("1", "2"), text = "<redacted>")))
        assertEquals("Handled", AnsweredText.summary(res, dialog, Resolution()))
    }

    @Config(qualifiers = "en")
    @Test fun theNoticeNamesHowACardEnded() {
        fun notice(reply: InteractionReply) = reply.notice(res, permission)
        assertEquals("Answered already: Denied", notice(InteractionReply(InteractionSubmission.AlreadyAnswered, effective = Resolution(allow = false))))
        assertEquals("Answered already", notice(InteractionReply(InteractionSubmission.AlreadyAnswered)))
        assertEquals("This request expired, so the answer wasn't delivered", notice(InteractionReply(InteractionSubmission.ResolvedElsewhere, endedStatus = "expired")))
        assertEquals("The computer withdrew this request, so the answer wasn't delivered", notice(InteractionReply(InteractionSubmission.ResolvedElsewhere, endedStatus = "cancelled")))
        assertEquals("This request was handed back to the computer, so the answer wasn't delivered", notice(InteractionReply(InteractionSubmission.ResolvedElsewhere, endedStatus = "dismissed")))
        assertEquals("Handled on the computer", notice(InteractionReply(InteractionSubmission.ResolvedElsewhere)))
    }

    @Config(qualifiers = "zh")
    @Test fun chineseReadsAsChinese() {
        assertEquals("已经回答过了：批准，默认权限", InteractionReply(InteractionSubmission.AlreadyAnswered,
            effective = Resolution(approve = true, mode = "default")).notice(res, plan))
        assertEquals("这个请求已经过期，回答没有送到", InteractionReply(InteractionSubmission.ResolvedElsewhere, endedStatus = "expired").notice(res, plan))
    }
}
