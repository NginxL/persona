package persona.feature.chat

import android.content.Context
import androidx.test.core.app.ApplicationProvider
import androidx.test.ext.junit.runners.AndroidJUnit4
import org.junit.Assert.assertEquals
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import persona.core.contracts.ModeChange

/** The web/iOS permission-mode sentences, using Android's own mode names in every shipped chat language. */
@RunWith(AndroidJUnit4::class)
@Config(sdk = [35])
class ModeChangeWordsTest {
    private val res get() = ApplicationProvider.getApplicationContext<Context>().resources
    private val at = "2026-11-18T09:01:00Z"

    private fun set(
        from: String? = "default", to: String = "acceptEdits", applied: String = "now",
        actor: String = "device", device: String? = "Phone",
    ) = ModeChange(kind = "set", from = from, to = to, applied = applied, actorKind = actor, deviceName = device, at = at)

    private fun outcome(kind: String, reason: String? = null) = ModeChange(
        kind = kind, to = "bypassPermissions", reason = reason, at = at,
    )

    private data class Expected(
        val changed: String,
        val changedTo: String,
        val queued: String,
        val hub: String,
        val unknownDevice: String,
        val restarted: String,
        val already: String,
        val failed: String,
        val dropped: String,
        val reasons: Map<String, String>,
    )

    private fun check(expected: Expected) {
        val ordinary = set()
        assertEquals("set", expected.changed, ModeChangeWords.label(ordinary, res))
        for (from in listOf(null, "", "acceptEdits")) {
            assertEquals("no from: $from", expected.changedTo, ModeChangeWords.label(ordinary.copy(from = from), res))
        }
        assertEquals("queued restart", expected.queued,
            ModeChangeWords.label(set(to = "bypassPermissions", applied = "restart"), res))
        assertEquals("hub actor", expected.hub,
            ModeChangeWords.label(ordinary.copy(actorKind = "hub", deviceName = null), res))
        assertEquals("unnamed device", expected.unknownDevice,
            ModeChangeWords.label(ordinary.copy(deviceName = "  "), res))

        assertEquals("restart done", expected.restarted, ModeChangeWords.label(outcome("restart_done"), res))
        assertEquals("already in", expected.already, ModeChangeWords.label(outcome("restart_done", "already_in"), res))
        for (reason in listOf(null, "", "claude 4101, the process it was picked for, has exited")) {
            assertEquals("no known failure reason: $reason", expected.failed,
                ModeChangeWords.label(outcome("restart_failed", reason), res))
            assertEquals("no known dropped reason: $reason", expected.dropped,
                ModeChangeWords.label(outcome("restart_dropped", reason), res))
        }
        for ((reason, words) in expected.reasons) {
            val kind = if (reason in setOf("not_stopped", "start_failed", "stopped")) "restart_failed" else "restart_dropped"
            assertEquals("$kind/$reason", words, ModeChangeWords.label(outcome(kind, reason), res))
        }
    }

    @Config(qualifiers = "en")
    @Test fun englishWordsAndEveryKnownReason() = check(Expected(
        changed = "Phone changed the permission mode from “Default permissions” to “Auto accept edits”",
        changedTo = "Phone changed the permission mode to “Auto accept edits”",
        queued = "Phone picked “Full access”: the session restarts in it once this turn ends",
        hub = "The hub changed the permission mode from “Default permissions” to “Auto accept edits”",
        unknownDevice = "A device changed the permission mode from “Default permissions” to “Auto accept edits”",
        restarted = "Restarted in “Full access”",
        already = "Already in “Full access”, no restart needed",
        failed = "Couldn't restart in “Full access”",
        dropped = "Called off, won't restart in “Full access”",
        reasons = linkedMapOf(
            "cancelled" to "Called off, won't restart in “Full access”: the session stays in its current mode",
            "replaced" to "Called off, won't restart in “Full access”: another switch took its place",
            "process_exited" to "Called off, won't restart in “Full access”: the process it was picked for has exited",
            "process_replaced" to "Called off, won't restart in “Full access”: the session runs in another process now",
            "process_unknown" to "Called off, won't restart in “Full access”: it couldn't tell which process to restart",
            "expired" to "Called off, won't restart in “Full access”: it waited over two hours",
            "not_stopped" to "Couldn't restart in “Full access”: the agent didn't quit",
            "start_failed" to "Couldn't restart in “Full access”: the new process didn't start, so the session carries on as before",
            "stopped" to "Couldn't restart in “Full access”: the new process didn't start, and the session has stopped",
        ),
    ))

    @Config(qualifiers = "zh")
    @Test fun chineseWordsAndEveryKnownReason() = check(Expected(
        changed = "Phone 把权限模式从「默认权限」改成了「允许编辑」",
        changedTo = "Phone 把权限模式改成了「允许编辑」",
        queued = "Phone 选了「完全访问」，这一轮结束后会重启会话再切过去",
        hub = "hub 把权限模式从「默认权限」改成了「允许编辑」",
        unknownDevice = "某台设备 把权限模式从「默认权限」改成了「允许编辑」",
        restarted = "已重启，切到了「完全访问」",
        already = "已经是「完全访问」了，不用重启",
        failed = "没能重启到「完全访问」",
        dropped = "已取消，不会重启到「完全访问」",
        reasons = linkedMapOf(
            "cancelled" to "已取消，不会重启到「完全访问」：会话保持当前模式",
            "replaced" to "已取消，不会重启到「完全访问」：被另一次切换取代",
            "process_exited" to "已取消，不会重启到「完全访问」：原进程已经退出",
            "process_replaced" to "已取消，不会重启到「完全访问」：会话换了进程",
            "process_unknown" to "已取消，不会重启到「完全访问」：不知道该重启哪个进程",
            "expired" to "已取消，不会重启到「完全访问」：等了两个多小时",
            "not_stopped" to "没能重启到「完全访问」：原进程没有退出",
            "start_failed" to "没能重启到「完全访问」：新进程没起来，会话按原来的模式继续",
            "stopped" to "没能重启到「完全访问」：新进程没起来，会话已经停下",
        ),
    ))

    @Config(qualifiers = "ja")
    @Test fun japaneseWordsAndEveryKnownReason() = check(Expected(
        changed = "Phoneが権限モードを「デフォルト権限」から「編集を自動承認」に変更しました",
        changedTo = "Phoneが権限モードを「編集を自動承認」に変更しました",
        queued = "Phoneが「フルアクセス」を選びました。このターンが終わるとセッションを再起動して切り替えます",
        hub = "ハブが権限モードを「デフォルト権限」から「編集を自動承認」に変更しました",
        unknownDevice = "デバイスが権限モードを「デフォルト権限」から「編集を自動承認」に変更しました",
        restarted = "「フルアクセス」で再起動しました",
        already = "すでに「フルアクセス」です。再起動は不要です",
        failed = "「フルアクセス」で再起動できませんでした",
        dropped = "取り消しました。「フルアクセス」では再起動しません",
        reasons = linkedMapOf(
            "cancelled" to "取り消しました。「フルアクセス」では再起動しません：セッションは現在のモードのままです",
            "replaced" to "取り消しました。「フルアクセス」では再起動しません：別の切り替えに置き換えられました",
            "process_exited" to "取り消しました。「フルアクセス」では再起動しません：元のプロセスが終了しました",
            "process_replaced" to "取り消しました。「フルアクセス」では再起動しません：セッションが別のプロセスに変わりました",
            "process_unknown" to "取り消しました。「フルアクセス」では再起動しません：どのプロセスを再起動するか分かりませんでした",
            "expired" to "取り消しました。「フルアクセス」では再起動しません：2時間以上待ちました",
            "not_stopped" to "「フルアクセス」で再起動できませんでした：元のプロセスが終了しませんでした",
            "start_failed" to "「フルアクセス」で再起動できませんでした：新しいプロセスが起動せず、セッションは元のモードのまま続いています",
            "stopped" to "「フルアクセス」で再起動できませんでした：新しいプロセスが起動せず、セッションは停止しました",
        ),
    ))
}
