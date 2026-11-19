package persona.feature.knowledge

import android.content.Context
import androidx.test.core.app.ApplicationProvider
import androidx.test.ext.junit.runners.AndroidJUnit4
import org.junit.Assert.assertEquals
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config

/** Every meeting hand-off string ships in the three Android languages (#10092 AC4). */
@RunWith(AndroidJUnit4::class)
@Config(sdk = [35], qualifiers = "en")
class MeetingHandoffLocalizationTest {
    private val context get() = ApplicationProvider.getApplicationContext<Context>()
    private val ids = listOf(
        R.string.knowledge_handoff_title,
        R.string.knowledge_handoff_open_dialog,
        R.string.knowledge_handoff_pick,
        R.string.knowledge_handoff_ask_label,
        R.string.knowledge_handoff_ask_placeholder,
        R.string.knowledge_handoff_transcript,
        R.string.knowledge_handoff_gets,
        R.string.knowledge_handoff_send,
        R.string.knowledge_handoff_running,
        R.string.knowledge_handoff_no_material,
        R.string.knowledge_handoff_problem_not_agent,
        R.string.knowledge_handoff_problem_not_allowed,
        R.string.knowledge_handoff_problem_unavailable,
        R.string.knowledge_handoff_problem_send_failed,
        R.string.knowledge_handoff_agent,
        R.string.knowledge_handoff_row_sent,
        R.string.knowledge_handoff_row_failed,
        R.string.knowledge_handoff_retry,
        R.string.knowledge_handoff_open,
    )

    @Test fun english() = assertStrings(
        "Hand to Agent",
        "Hand to Agent…",
        "Pick an Agent",
        "What should it do?",
        "What should it do? (optional)",
        "Include the transcript",
        "It gets this meeting's minutes: the summary, decisions, to-dos and open questions.",
        "Send",
        "The meeting is still going. Hand it over once it ends.",
        "This meeting has nothing to hand over yet: no minutes or transcript.",
        "That's a Persona chat, not an Agent.",
        "This Agent can't take meetings.",
        "Its computer isn't reachable, or the chat is closed.",
        "The message didn't go through.",
        "Agent",
        "Handed to \"Website redesign\"",
        "Couldn't hand to \"Website redesign\"",
        "Retry",
        "Open",
    )

    @Config(sdk = [35], qualifiers = "zh")
    @Test fun simplifiedChinese() = assertStrings(
        "交给 Agent",
        "交给 Agent…",
        "选一个 Agent",
        "要它做什么",
        "要它做什么（可选）",
        "附上逐字稿",
        "它会收到这场会的纪要：总结、结论、待办和没定的事。",
        "发送",
        "会还在开，结束后再交。",
        "这场会还没有可交的内容：没有纪要，也没有逐字稿。",
        "这是 Persona 的对话，不是 Agent。",
        "这个 Agent 不能接收会议内容。",
        "它的电脑连不上，或者对话已关闭。",
        "消息没发出去。",
        "Agent",
        "已交给「Website redesign」",
        "没能交给「Website redesign」",
        "重试",
        "打开",
    )

    @Config(sdk = [35], qualifiers = "ja")
    @Test fun japanese() = assertStrings(
        "エージェントに任せる",
        "エージェントに任せる…",
        "エージェントを選ぶ",
        "何をしてほしいか",
        "何をしてほしいか（任意）",
        "文字起こしも付ける",
        "この会議の議事録（要約、決定事項、ToDo、未決事項）が届きます。",
        "送信",
        "会議はまだ続いています。終わってから任せてください。",
        "この会議にはまだ任せられる内容がありません（議事録も文字起こしもありません）。",
        "これは Persona の会話で、エージェントではありません。",
        "このエージェントには会議を任せられません。",
        "コンピュータに接続できないか、会話が閉じています。",
        "メッセージを送れませんでした。",
        "エージェント",
        "「Website redesign」に任せました",
        "「Website redesign」に任せられませんでした",
        "再試行",
        "開く",
    )

    private fun assertStrings(vararg expected: String) {
        assertEquals("expected value count", ids.size, expected.size)
        ids.zip(expected).forEach { (id, value) ->
            assertEquals(context.resources.getResourceEntryName(id), value, context.getString(id, "Website redesign"))
        }
    }
}
