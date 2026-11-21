package persona.platform.phonetools

import persona.core.contracts.api.*
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.CompletableJob
import kotlinx.coroutines.CoroutineStart
import kotlinx.coroutines.Job
import kotlinx.coroutines.async
import kotlinx.coroutines.coroutineScope
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.withTimeoutOrNull
import kotlinx.serialization.json.JsonObject
import java.util.concurrent.atomic.AtomicBoolean

/**
 * All execution paths, including stale model calls, pass through this gate.
 *
 * Calls are *not* serialised behind one another: a confirmed write waits for the user inside
 * `PhoneConfirmations` (which keeps a single visible card) while reads and other writes continue. Same-call-id
 * deduplication lives in [PhoneToolCallGate]; a repeated delivery either replays the cached result or shares
 * the in-flight leader's outcome, never re-executing the tool.
 *
 * The hub owns how long a confirmed write may wait (`phoneConfirmWait`); there is no app-side countdown that
 * withdraws the card. The local execution timeout still bounds `execute` itself, and on a confirmed write it
 * produces an *uncertain* result instead of an ordinary timeout so the model is not invited to immediately
 * retry a write that may have already happened.
 */
class PhonePreparedConfirmation(
    val text: PhoneConfirmationText,
    /** Re-reads the prepared target and refuses a stale snapshot before any provider write. */
    val execute: suspend () -> PhoneToolResult,
)

/**
 * Arrival tickets for confirmation cards. A call takes a ticket when it arrives (synchronously, before it first
 * suspends) and may only queue its card in `PhoneConfirmations` once every earlier ticket has queued its own card or
 * given up. Preparation can take different times, so without this a quick later call would show its card ahead of an
 * earlier one that was still preparing; the card order then differs from the order the model made the calls.
 */
internal class ConfirmationArrivalOrder {
    class Ticket internal constructor(private var previous: Job?, private val done: CompletableJob) {
        private val released = AtomicBoolean(false)

        /** Suspends until every earlier ticket has been released. */
        suspend fun awaitTurn() { previous?.join() }

        /**
         * Settles this ticket: queued its card, failed, timed out or was cancelled. Idempotent. [done] completes only after
         * every earlier ticket did, so a ticket that gives up early cannot let its successors pass a slower predecessor.
         */
        fun release() {
            if (!released.compareAndSet(false, true)) return
            val earlier = previous
            previous = null
            if (earlier == null) done.complete() else earlier.invokeOnCompletion { done.complete() }
        }
    }

    private val lock = Any()
    private var last: Job? = null

    fun take(): Ticket = synchronized(lock) {
        val done = Job()
        Ticket(last, done).also { last = done }
    }
}

/** A definite pre-write refusal, not an ambiguous provider outcome. */
internal class PhoneToolWriteRefusal(message: String) : Exception(message)

class PhoneToolRegistry(
    private val enabled: () -> Set<PhoneSource>,
    private val available: () -> Boolean,
    private val foreground: () -> Boolean,
    private val permitted: (String, PhoneSource) -> Boolean,
    private val confirm: suspend (PhoneConfirmationText) -> PhoneConfirmation,
    private val execute: suspend (String, JsonObject) -> PhoneToolResult,
    private val prepareConfirmation: (suspend (String, JsonObject) -> PhonePreparedConfirmation)? = null,
) {
    private val gate = PhoneToolCallGate()
    private val arrival = ConfirmationArrivalOrder()

    private val _writesInFlight = MutableStateFlow<Set<String>>(emptySet())
    /** call-ids whose confirmed write has entered its local `execute` and must not be killed by a transient
     * disconnect or permission watcher: the local operation has to finish (or fail) on its own so the result
     * can be returned, possibly as `uncertain`. */
    internal val writesInFlight: StateFlow<Set<String>> = _writesInFlight

    val all: List<PhoneToolSpec> get() = PhoneToolCatalog.all.map { it.spec }
    fun enabledSpecs(): List<PhoneToolSpec> = PhoneToolCatalog.all.filter(::usable).map { it.spec }
    private fun usable(tool: ToolDefinition) = available() && tool.source in enabled() &&
        (!tool.foregroundOnly || foreground()) && permitted(tool.spec.name, tool.source)

    suspend fun call(call: PhoneToolCall): PhoneToolResult {
        return when (val begin = gate.begin(call.callId)) {
            is PhoneToolCallGate.Begin.Cached -> begin.result
            is PhoneToolCallGate.Begin.Follower -> begin.deferred.await()
            PhoneToolCallGate.Begin.Leader -> {
                try {
                    val result = runCall(call)
                    gate.complete(call.callId, result)
                    result
                } catch (cancelled: CancellationException) {
                    gate.fail(call.callId, cancelled)
                    throw cancelled
                } catch (error: Exception) {
                    // Expected tool failures are results already. An unexpected ordinary Exception still releases
                    // the leader and answers followers instead of leaving them waiting forever.
                    val result = PhoneToolText.failed(error)
                    gate.complete(call.callId, result)
                    result
                } catch (fatal: Throwable) {
                    // Fatal Errors are never converted into an ordinary tool failure. Wake followers with the same
                    // error and clear the leader before preserving the process/runtime failure semantics.
                    gate.fail(call.callId, fatal)
                    throw fatal
                }
            }
        }
    }

    private suspend fun runCall(call: PhoneToolCall): PhoneToolResult {
        val tool = PhoneToolCatalog.all.firstOrNull { it.spec.name == call.name }
            ?: return PhoneToolText.unknownTool(call.name, enabledSpecs().map { it.name })
        refusal(tool)?.let { return it }

        if (tool.confirmation == null && !tool.confirmationNeedsPreparation) {
            // Plain read / unconfirmed write: run concurrently with everything else.
            return executeCall(call, tool, isConfirmedWrite = false)
        }

        // Provider reads needed by a before → after card live in the suspend preparation path, never in the
        // synchronous resource formatter. The prepared value carries its own immutable snapshot into execute.
        // The card still takes its place in the order the calls arrived, not the order preparation finished.
        val ticket = arrival.take()
        var prepared: PhonePreparedConfirmation? = null
        val decision = try {
            val text = if (tool.confirmationNeedsPreparation) {
                val hook = prepareConfirmation ?: error("No confirmation preparation is wired for ${tool.spec.name}")
                // Nothing is written yet, so a provider that never answers is a plain timeout, not an uncertain write.
                val value = withTimeoutOrNull(EXECUTION_TIMEOUT_MILLIS) { hook(call.name, call.args) }
                    ?: return PhoneToolText.timedOut()
                prepared = value
                value.text
            } else {
                tool.confirmation!!.invoke(call.args)
            }
            ticket.awaitTurn()
            coroutineScope {
                // `confirm` queues its card before its first suspension, so the next ticket may go once it is running.
                val answer = async(start = CoroutineStart.UNDISPATCHED) { confirm(text) }
                ticket.release()
                answer.await()
            }
        } catch (cancelled: CancellationException) { throw cancelled }
        catch (failure: PhoneToolFailure) {
            return PhoneToolText.failure(failure, tool.source)
        }
        catch (invalid: IllegalArgumentException) { return PhoneToolText.invalid(invalid.message ?: "invalid arguments") }
        catch (_: SecurityException) { return PhoneToolText.denied(tool.source) }
        finally { ticket.release() }

        return when (decision) {
            PhoneConfirmation.Allowed -> {
                // Recheck after the user's Allow: a permission, the source toggle or the connection may have
                // changed while the card was on screen. Refuse rather than executing a stale write.
                refusal(tool)?.let { return it }
                executeCall(call, tool, isConfirmedWrite = true, prepared = prepared)
            }
            // only a tap on Deny is a refusal (#1875)
            PhoneConfirmation.Denied -> PhoneToolText.declined()
            PhoneConfirmation.NotShown -> PhoneToolText.notShown()
            PhoneConfirmation.Withdrawn -> PhoneToolText.interrupted()
            PhoneConfirmation.Dismissed -> PhoneToolText.dismissed()
        }
    }

    private suspend fun executeCall(
        call: PhoneToolCall,
        tool: ToolDefinition,
        isConfirmedWrite: Boolean,
        prepared: PhonePreparedConfirmation? = null,
    ): PhoneToolResult {
        if (isConfirmedWrite) _writesInFlight.value = _writesInFlight.value + call.callId
        try {
            return try {
                withTimeoutOrNull(EXECUTION_TIMEOUT_MILLIS) {
                    prepared?.execute?.invoke() ?: execute(call.name, call.args)
                } ?: if (isConfirmedWrite) PhoneToolText.uncertain() else PhoneToolText.timedOut()
            } catch (cancelled: CancellationException) {
                // A confirmed write that was already executing may have landed on the phone: answer uncertain
                // rather than letting the cancellation propagate as "not done". The gate records it.
                if (isConfirmedWrite) {
                    gate.complete(call.callId, PhoneToolText.uncertain())
                    throw cancelled
                }
                throw cancelled
            }
            catch (refused: PhoneToolWriteRefusal) {
                // The target changed after the card was prepared. No write was attempted, so this is not uncertain.
                PhoneToolResult.text(refused.message.orEmpty(), true)
            }
            catch (failure: PhoneToolFailure) {
                if (isConfirmedWrite) PhoneToolText.uncertain() else PhoneToolText.failure(failure, tool.source)
            }
            catch (invalid: IllegalArgumentException) {
                if (isConfirmedWrite) PhoneToolText.uncertain() else PhoneToolText.invalid(invalid.message ?: "invalid arguments")
            }
            catch (_: SecurityException) {
                if (isConfirmedWrite) PhoneToolText.uncertain() else PhoneToolText.denied(tool.source)
            }
            catch (unavailable: IllegalStateException) {
                if (isConfirmedWrite) PhoneToolText.uncertain() else PhoneToolText.unavailable(unavailable.message)
            }
            catch (error: Exception) {
                if (isConfirmedWrite) PhoneToolText.uncertain() else PhoneToolText.failed(error)
            }
        } finally {
            if (isConfirmedWrite) _writesInFlight.value = _writesInFlight.value - call.callId
        }
    }

    /** Why this tool cannot run now, in words the model can act on; null when it can. */
    private fun refusal(tool: ToolDefinition): PhoneToolResult? = when {
        tool.source !in enabled() -> PhoneToolText.turnedOff(tool.source)
        !permitted(tool.spec.name, tool.source) -> PhoneToolText.denied(tool.source)
        tool.foregroundOnly && !foreground() -> PhoneToolText.needsForeground()
        !available() -> PhoneToolText.unavailable(null)
        else -> null
    }

    private companion object { const val EXECUTION_TIMEOUT_MILLIS = 30_000L }
}

/** The words a refused or failed call returns (as the iOS app says them, with Android's settings path). */
internal object PhoneToolText {
    /** The iOS app's exact sentence, so the model reads the same refusal whichever phone answered. */
    const val DECLINED = "The user declined on the phone."

    fun declined() = PhoneToolResult.text(DECLINED, true)

    /** The user swiped the confirmation notification away without answering (#3861): not a refusal, nothing saved. */
    fun dismissed() = PhoneToolResult.text(
        "手机上的确认通知被划掉了 / The confirmation notification was dismissed on the phone without an answer, so it " +  // i18n-ignore: tool result for the model
            "was not saved. This is not a refusal: ask the user whether they still want it, and call it again if they do.", true)

    /** The confirmation could not be shown at all: Persona was in the background with its notifications off. */
    fun notShown() = PhoneToolResult.text(
        "手机上没能显示确认 / The confirmation could not be shown on the phone: Persona is in the background and its " +  // i18n-ignore: tool result for the model
            "notifications (or the Phone action confirmations channel) are turned off. Nothing was saved, and the user " +
            "did not refuse. Ask the user to open Persona on the phone, or turn its notifications on in Android Settings › " +
            "Apps › Persona › Notifications, then call it again.", true)

    fun unknownTool(name: String, available: List<String>) = PhoneToolResult.text(
        "未知工具 / Unknown tool: $name. 可用工具 / Available tools: ${available.joinToString(", ")}.", true)  // i18n-ignore: tool result for the model

    /** Where the switch for every source lives in the app (Settings › Connectors › This phone). */
    private const val APP_PATH_ZH = "Persona › 设置 › 连接器 › 这台手机"  // i18n-ignore: tool result for the model
    private const val APP_PATH_EN = "Persona › Settings › Connectors › This phone"

    fun turnedOff(source: PhoneSource) = PhoneToolResult.text(
        "“${source.modelName}” 已在手机上关闭 / The ${source.key} data source is turned off on the phone. " +  // i18n-ignore: tool result for the model
            "The user can turn it on in $APP_PATH_EN ($APP_PATH_ZH).", true)

    /**
     * Where to allow [source]: the app's switch first, then the Android setting, because once a permission is refused
     * twice Android stops asking and only the system settings page can grant it.
     */
    fun grantHint(source: PhoneSource?): String {
        val system = source?.let(::systemSetting)
        // Sources that need no permission (clipboard, device, calls) have a switch but no Allow button.
        val zh = "请在安卓手机上打开 $APP_PATH_ZH" +  // i18n-ignore: tool result for the model
            when {
                source == null -> "，点对应数据的「授权」"  // i18n-ignore: tool result for the model
                system == null -> "，打开「${source.modelName}」"  // i18n-ignore: tool result for the model
                else -> "，打开「${source.modelName}」并点「授权」"  // i18n-ignore: tool result for the model
            } + (system?.let { "；如果拒绝过、系统不再弹窗，就去${it.first}。" } ?: "。")  // i18n-ignore: tool result for the model
        val en = "On the Android phone, open $APP_PATH_EN" +
            when {
                source == null -> " and tap Allow for that data"
                system == null -> " and turn on ${source.key}"
                else -> ", turn on ${source.key} and tap Allow"
            } + (system?.let { ". If it was refused before and Android no longer asks, ${it.second}." } ?: ".")
        return "$zh\n$en"
    }

    /** The Android settings page that grants [source], as (Chinese, English) sentences; null when there is none. */
    private fun systemSetting(source: PhoneSource): Pair<String, String>? = when (source) {
        PhoneSource.Contacts -> permission("通讯录", "Contacts")  // i18n-ignore: tool result for the model
        PhoneSource.Calendar -> permission("日历", "Calendar")  // i18n-ignore: tool result for the model
        PhoneSource.Location -> permission("位置信息", "Location")  // i18n-ignore: tool result for the model
        // Android 12 and older grant photos through storage (READ_EXTERNAL_STORAGE).
        PhoneSource.Photos -> if (photosSdk() >= 33) permission("照片和视频", "Photos and videos")  // i18n-ignore: tool result for the model
            else permission("文件和媒体", "Files and media")  // i18n-ignore: tool result for the model
        PhoneSource.Notifications -> "系统设置 › 应用 › Persona › 通知 里打开通知" to  // i18n-ignore: tool result for the model
            "turn notifications on in Android Settings › Apps › Persona › Notifications"
        PhoneSource.Health -> "Health Connect › 应用权限 › Persona 里允许读取" to  // i18n-ignore: tool result for the model
            "allow reading in Health Connect › App permissions › Persona"
        PhoneSource.Clipboard, PhoneSource.Device, PhoneSource.Calls -> null
    }

    /** The SDK level the photos permission name depends on; replaceable in tests. */
    internal var photosSdk: () -> Int = { android.os.Build.VERSION.SDK_INT }

    private fun permission(zh: String, en: String) =
        "系统设置 › 应用 › Persona › 权限 里允许「$zh」" to  // i18n-ignore: tool result for the model
            "allow $en in Android Settings › Apps › Persona › Permissions"

    fun denied(source: PhoneSource?) = PhoneToolResult.text(
        (source?.let { "没有权限：${it.modelName} / Permission denied: ${it.key}.\n" } ?: "没有权限 / Permission denied.\n") +  // i18n-ignore: tool result for the model
            grantHint(source), true)

    fun needsForeground() = PhoneToolResult.text(
        "安卓只允许打开着的 Persona 使用这项数据 / Android only allows this while Persona is open on screen: " +  // i18n-ignore: tool result for the model
            "ask the user to open Persona, then retry.", true)

    fun interrupted() = PhoneToolResult.text(
        "手机工具已关闭或 Persona 已进入后台 / The tool was turned off or Persona left the screen before it finished.", true)  // i18n-ignore: tool result for the model

    fun unavailable(message: String?) = PhoneToolResult.text(message ?: "手机数据暂不可用 / Phone data is not available right now.", true)  // i18n-ignore: tool result for the model

    fun invalid(message: String) = PhoneToolResult.text("参数错误 / Invalid argument: $message", true)  // i18n-ignore: tool result for the model

    fun timedOut() = PhoneToolResult.text(
        "手机操作超时 / The phone did not finish within 30 seconds. Check what happened before trying again.", true)  // i18n-ignore: tool result for the model

    /**
     * A confirmed write whose local execution ended without a clear answer (timeout, mid-write exception,
     * cancellation after Allow): the operation may have landed on the phone. The model must not retry blindly.
     */
    fun uncertain() = PhoneToolResult.text(
        "手机上的写入结果不确定 / The phone did not confirm whether the approved write finished. It may already have " +  // i18n-ignore: tool result for the model
            "been saved. Do not retry automatically: ask the user to check the phone's actual calendar, contacts or data " +
            "first, and only call again if it is really missing.", true)

    fun failed(error: Exception) = PhoneToolResult.text(
        "请求失败 / Request failed: ${error.message ?: error.javaClass.simpleName}", true)  // i18n-ignore: tool result for the model

    fun failure(failure: PhoneToolFailure, source: PhoneSource?) = when (failure.kind) {
        PhoneToolFailure.Kind.InvalidArgument -> invalid(failure.message.orEmpty())
        PhoneToolFailure.Kind.Denied -> PhoneToolResult.text(
            "没有权限 / Permission denied: ${failure.message}\n" + grantHint(source), true)  // i18n-ignore: tool result for the model
        PhoneToolFailure.Kind.Unavailable -> unavailable(failure.message)
    }
}
